#if os(macOS)
import Foundation
import Security

nonisolated enum PackageInstaller {
    struct Canceled: Error {}

    private typealias ExecuteWithPrivileges = @convention(c) (
        AuthorizationRef,
        UnsafePointer<CChar>,
        AuthorizationFlags,
        UnsafePointer<UnsafeMutablePointer<CChar>?>,
        UnsafeMutablePointer<UnsafeMutablePointer<FILE>?>?
    ) -> OSStatus

    private static let exitStatusMarker = "__INSTALLER_EXIT_STATUS__="

    static func authorize() throws -> AuthorizationRef {
        var authorization: AuthorizationRef?
        var status = AuthorizationCreate(nil, nil, [], &authorization)
        guard status == errAuthorizationSuccess, let authorization else {
            throw MessageError("AuthorizationCreate", status: status)
        }
        status = kAuthorizationRightExecute.withCString { name in
            var item = AuthorizationItem(name: name, valueLength: 0, value: nil, flags: 0)
            return withUnsafeMutablePointer(to: &item) { item in
                var rights = AuthorizationRights(count: 1, items: item)
                return AuthorizationCopyRights(authorization, &rights, nil, [.interactionAllowed, .extendRights, .preAuthorize], nil)
            }
        }
        guard status == errAuthorizationSuccess else {
            AuthorizationFree(authorization, [])
            if status == errAuthorizationCanceled {
                throw Canceled()
            }
            throw MessageError("AuthorizationCopyRights", status: status)
        }
        return authorization
    }

    static func install(_ package: URL, authorization: AuthorizationRef) throws {
        defer { AuthorizationFree(authorization, []) }
        guard let symbol = dlsym(UnsafeMutableRawPointer(bitPattern: -2), "AuthorizationExecuteWithPrivileges") else {
            throw MessageError("AuthorizationExecuteWithPrivileges is unavailable")
        }
        let executeWithPrivileges = unsafeBitCast(symbol, to: ExecuteWithPrivileges.self)
        let path = "'" + package.path.replacingOccurrences(of: "'", with: "'\\''") + "'"
        var arguments = [strdup("-c"), strdup("/usr/sbin/installer -pkg \(path) -target / 2>&1; echo \(exitStatusMarker)$?"), nil]
        defer { arguments.forEach { free($0) } }
        var pipe: UnsafeMutablePointer<FILE>?
        let status = executeWithPrivileges(authorization, "/bin/sh", [], &arguments, &pipe)
        guard status == errAuthorizationSuccess, let pipe else {
            throw MessageError("AuthorizationExecuteWithPrivileges", status: status)
        }
        defer { fclose(pipe) }
        var data = Data()
        var buffer = [UInt8](repeating: 0, count: 4096)
        while case let count = fread(&buffer, 1, buffer.count, pipe), count > 0 {
            data.append(contentsOf: buffer[..<count])
        }
        let output = String(decoding: data, as: UTF8.self)
        guard let marker = output.range(of: exitStatusMarker, options: .backwards) else {
            throw MessageError(output.isEmpty ? "installer exited without a status" : output)
        }
        let log = output[..<marker.lowerBound].trimmingCharacters(in: .whitespacesAndNewlines)
        let exitStatus = output[marker.upperBound...].trimmingCharacters(in: .whitespacesAndNewlines)
        guard exitStatus == "0" else {
            throw MessageError(log.isEmpty ? "installer exited with status \(exitStatus)" : log)
        }
    }
}
#endif
