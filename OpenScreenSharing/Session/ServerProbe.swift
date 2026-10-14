import Foundation
import Libsharing

nonisolated struct ServerAuthentication: Codable, Hashable, Sendable {
    var account: Bool
    var requestPermission: Bool
    var vncPassword: Bool
    var noPassword: Bool
    var grantsRequests: Bool

    var openCredentials: SessionCredentials? {
        if noPassword {
            return SessionCredentials(loginType: .none, username: "", password: "")
        }
        if grantsRequests {
            return SessionCredentials(loginType: .askForPermission, username: "", password: "")
        }
        return nil
    }
}

nonisolated struct ProbedServer: Hashable, Sendable {
    var address: ComputerAddress
    var remoteProtocol: RemoteProtocol
    var authentication: ServerAuthentication
    var fingerprint: String
}

nonisolated enum ServerProber {
    static let timeout: Double = 15

    static func probe(
        host: String, port: Int?, remoteProtocol: RemoteProtocol?, timeout: Double = ServerProber.timeout
    ) async -> Result<ProbedServer, SessionFailure> {
        await Task.detached(priority: .userInitiated) {
            LibsharingLog.installed
            let probe =
                switch remoteProtocol {
                case .vnc: LibsharingProbeServer(host, Int32(port ?? RemoteProtocol.vnc.defaultPort), timeout)
                case .rdp: LibsharingProbeRDPServer(host, Int32(port ?? RemoteProtocol.rdp.defaultPort), timeout)
                case nil: LibsharingProbeProtocol(host, Int32(port ?? 0), timeout)
                }
            guard let probe else {
                return .failure(SessionFailure(.other, message: "libsharing returned no probe result"))
            }
            guard probe.errorCode == LibsharingErrorNone else {
                return .failure(failure(code: probe.errorCode, message: probe.errorMessage, fingerprint: probe.fingerprint))
            }
            return .success(
                ProbedServer(
                    address: ComputerAddress(host: host, port: Int(probe.port)),
                    remoteProtocol: probe.protocol == LibsharingProtocolRDP ? .rdp : .vnc,
                    authentication: ServerAuthentication(
                        account: probe.supportsAccount,
                        requestPermission: probe.supportsRequestControl || probe.supportsRequestObserve,
                        vncPassword: probe.supportsVNCPassword,
                        noPassword: probe.supportsNoPassword,
                        grantsRequests: probe.grantsRequests
                    ),
                    fingerprint: probe.fingerprint
                ))
        }.value
    }

    static func checkLogin(_ server: ProbedServer, credentials: SessionCredentials) async -> Result<Bool, SessionFailure> {
        await Task.detached(priority: .userInitiated) {
            LibsharingLog.installed
            guard let options = LibsharingNewSessionOptions() else {
                return .failure(SessionFailure(.other, message: "libsharing returned no session options"))
            }
            options.protocol = server.remoteProtocol == .rdp ? LibsharingProtocolRDP : LibsharingProtocolRFB
            options.host = server.address.host
            options.port = Int32(server.address.port)
            options.trustedFingerprint = server.fingerprint
            options.username = credentials.username
            options.password = credentials.password
            switch credentials.loginType {
            case .account:
                options.loginType = credentials.username.isEmpty ? LibsharingLoginVNCPassword : LibsharingLoginAccount
            case .askForPermission:
                options.loginType = LibsharingLoginRequestControl
            case .vncPassword:
                options.loginType = LibsharingLoginVNCPassword
            case .none:
                options.loginType = LibsharingLoginNone
            }
            guard let check = LibsharingCheckLogin(options, timeout) else {
                return .failure(SessionFailure(.other, message: "libsharing returned no login result"))
            }
            guard check.errorCode == LibsharingErrorNone else {
                return .failure(failure(code: check.errorCode, message: check.errorMessage, fingerprint: check.fingerprint))
            }
            return .success(check.supportsHighPerformance)
        }.value
    }

    private static func failure(code: Int32, message: String, fingerprint: String) -> SessionFailure {
        switch code {
        case LibsharingErrorAuthentication: SessionFailure(.authenticationFailed, message: message)
        case LibsharingErrorPermissionDenied: SessionFailure(.permissionDenied, message: message)
        case LibsharingErrorHostNotFound: SessionFailure(.hostNotFound, message: message)
        case LibsharingErrorOwnScreen: SessionFailure(.ownScreen, message: message)
        case LibsharingErrorNetwork: SessionFailure(.unreachable, message: message)
        case LibsharingErrorUnsupportedServer: SessionFailure(.unsupported, message: message)
        case LibsharingErrorCertificateChanged: SessionFailure(.certificateChanged(fingerprint: fingerprint), message: message)
        default: SessionFailure(.other, message: message)
        }
    }
}
