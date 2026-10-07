import Foundation
import Libsharing

nonisolated struct ServerProblem: Equatable, Sendable {
    enum Kind: Sendable {
        case noTunnel
        case pairingRejected
        case developerModeOff
        case noDiskImage
        case diskImage
        case listen
        case unsupported
        case screenInUse
        case pairing
        case loopbackAddress
        case locationDenied
        case other
    }

    var kind: Kind
    var message: String
}

extension ServerProblem {
    init(code: Int32, message: String) {
        let kind: Kind =
            switch code {
            case LibsharingDeviceErrorNoTunnel: .noTunnel
            case LibsharingDeviceErrorPairingRejected: .pairingRejected
            case LibsharingDeviceErrorDeveloperModeOff: .developerModeOff
            case LibsharingDeviceErrorNoDiskImage: .noDiskImage
            case LibsharingDeviceErrorDiskImage: .diskImage
            case LibsharingDeviceErrorListen: .listen
            case LibsharingDeviceErrorUnsupported: .unsupported
            case LibsharingDeviceErrorScreenInUse: .screenInUse
            case LibsharingDeviceErrorPairing: .pairing
            case LibsharingDeviceErrorLoopbackAddress: .loopbackAddress
            default: .other
            }
        self.init(kind: kind, message: message)
    }

    var title: String {
        switch kind {
        case .noTunnel: String(localized: "Waiting for the VPN")
        case .pairingRejected: String(localized: "This Device Rejected the Pairing")
        case .developerModeOff: String(localized: "Developer Mode Is Off")
        case .noDiskImage: String(localized: "No Developer Disk Image")
        case .diskImage: String(localized: "Couldn’t Mount the Developer Disk Image")
        case .listen: String(localized: "A Port Is in Use")
        case .unsupported: String(localized: "This Device Can’t Share Its Screen")
        case .screenInUse: String(localized: "The Screen Is Shared Elsewhere")
        case .pairing: String(localized: "Pairing Failed")
        case .loopbackAddress: String(localized: "The VPN Gives a Loopback Address")
        case .locationDenied: String(localized: "No Location Access")
        case .other: String(localized: "Sharing Stopped")
        }
    }
}
