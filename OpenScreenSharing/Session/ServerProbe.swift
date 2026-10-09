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

nonisolated enum ServerProber {
    private static let timeout: Double = 15

    static func probe(_ address: ComputerAddress) async -> Result<ServerAuthentication, SessionFailure> {
        await Task.detached(priority: .userInitiated) {
            LibsharingLog.installed
            guard let probe = LibsharingProbeServer(address.host, Int32(address.port), timeout) else {
                return .failure(SessionFailure(.other, message: "libsharing returned no probe result"))
            }
            let message = probe.errorMessage
            switch probe.errorCode {
            case LibsharingErrorNone:
                return .success(
                    ServerAuthentication(
                        account: probe.supportsAccount,
                        requestPermission: probe.supportsRequestControl || probe.supportsRequestObserve,
                        vncPassword: probe.supportsVNCPassword,
                        noPassword: probe.supportsNoPassword,
                        grantsRequests: probe.grantsRequests
                    ))
            case LibsharingErrorHostNotFound:
                return .failure(SessionFailure(.hostNotFound, message: message))
            case LibsharingErrorOwnScreen:
                return .failure(SessionFailure(.ownScreen, message: message))
            case LibsharingErrorNetwork:
                return .failure(SessionFailure(.unreachable, message: message))
            case LibsharingErrorUnsupportedServer:
                return .failure(SessionFailure(.unsupported, message: message))
            default:
                return .failure(SessionFailure(.other, message: message))
            }
        }.value
    }

    static func checkLogin(_ address: ComputerAddress, credentials: SessionCredentials) async -> Result<Bool, SessionFailure> {
        await Task.detached(priority: .userInitiated) {
            LibsharingLog.installed
            guard let options = LibsharingNewSessionOptions() else {
                return .failure(SessionFailure(.other, message: "libsharing returned no session options"))
            }
            options.host = address.host
            options.port = Int32(address.port)
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
            let message = check.errorMessage
            switch check.errorCode {
            case LibsharingErrorNone:
                return .success(check.supportsHighPerformance)
            case LibsharingErrorAuthentication:
                return .failure(SessionFailure(.authenticationFailed, message: message))
            case LibsharingErrorHostNotFound:
                return .failure(SessionFailure(.hostNotFound, message: message))
            case LibsharingErrorOwnScreen:
                return .failure(SessionFailure(.ownScreen, message: message))
            case LibsharingErrorNetwork:
                return .failure(SessionFailure(.unreachable, message: message))
            case LibsharingErrorUnsupportedServer:
                return .failure(SessionFailure(.unsupported, message: message))
            default:
                return .failure(SessionFailure(.other, message: message))
            }
        }.value
    }
}
