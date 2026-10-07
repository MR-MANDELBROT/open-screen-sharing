import Foundation
import GRDB

nonisolated struct Computer: Codable, Identifiable, Hashable, FetchableRecord, PersistableRecord {
    static let databaseTableName = "computers"

    var id = UUID()
    var name = ""
    var host = ""
    var port = ComputerAddress.defaultPort
    var username = ""
    var password = ""
    var model = ""
    var enclosureColor = ""
    var lastConnected: Date?
    var thumbnail: Data?
    var createdAt = Date()
    var supportsHighPerformance = false
    var networkQuality = NetworkQuality.localNetwork
    var internetMaximumFrameRate = MaximumFrameRate.sixty
    var serverType = ServerType.vnc

    var quality = SessionQuality.adaptive
    var observes = false
    var sharesClipboard = true
    var usesDynamicResolution = true
    var resolutionScale = VirtualDisplayMode.defaultResolutionScale
    var displayID: Int64 = -1
    var usesHDR = false
    var remoteAudio = RemoteAudio.fromMac
    var keyboardSources: [String] = []

    init(name: String, address: ComputerAddress, username: String = "") {
        self.name = name
        host = address.host
        port = address.port
        self.username = username
    }

    var deviceType: DeviceType {
        DeviceType(model: model) ?? .mac
    }

    var networkConfiguration: NetworkConfiguration {
        get { NetworkConfiguration(quality: networkQuality, internetMaximumFrameRate: internetMaximumFrameRate) }
        set {
            networkQuality = newValue.quality
            internetMaximumFrameRate = newValue.internetMaximumFrameRate
        }
    }

    var displaySelection: DisplaySelection? {
        get {
            switch displayID {
            case 0: .all
            case 1...: .display(displayID)
            default: nil
            }
        }
        set {
            switch newValue {
            case .all: displayID = 0
            case .display(let id): displayID = id
            case nil: displayID = -1
            }
        }
    }

    var address: ComputerAddress {
        get { ComputerAddress(host: host, port: port) }
        set {
            host = newValue.host
            port = newValue.port
        }
    }

    var displayName: String {
        name.isEmpty ? address.description : name
    }

    var device: DeviceAppearance {
        DeviceAppearance(model: model, enclosureColor: enclosureColor)
    }

    var memory: SessionMemory {
        SessionMemory(
            quality: quality,
            observes: observes,
            sharesClipboard: sharesClipboard,
            usesDynamicResolution: usesDynamicResolution,
            resolutionScale: resolutionScale,
            display: displaySelection,
            usesHDR: usesHDR,
            remoteAudio: remoteAudio
        )
    }

    mutating func remember(_ memory: SessionMemory) {
        quality = memory.quality
        observes = memory.observes
        sharesClipboard = memory.sharesClipboard
        usesDynamicResolution = memory.usesDynamicResolution
        resolutionScale = memory.resolutionScale
        displaySelection = memory.display
        usesHDR = memory.usesHDR
        remoteAudio = memory.remoteAudio
    }

    mutating func apply(_ device: DeviceInfo) {
        model = device.model
        if !device.enclosureColor.isEmpty {
            enclosureColor = device.enclosureColor
        }
    }
}

nonisolated enum LoginType: String, Codable, Sendable {
    case account
    case askForPermission
    case vncPassword
    case none
}

nonisolated enum ServerType: String, Codable, CaseIterable, Identifiable, Sendable {
    case vnc
    case apple

    var id: Self { self }
}

nonisolated enum DeviceType: String, Codable, CaseIterable, Identifiable, Sendable {
    case mac
    case iPhone
    case iPad

    var id: Self { self }

    init?(model: String) {
        let model = model.trimmingCharacters(in: .whitespaces)
        if model.hasPrefix("iPhone") {
            self = .iPhone
        } else if model.hasPrefix("iPad") {
            self = .iPad
        } else if model.hasPrefix("Mac") || model.hasPrefix("iMac") {
            self = .mac
        } else {
            return nil
        }
    }

    var isTouchScreen: Bool { self != .mac }
}

nonisolated enum SessionQuality: String, Codable, CaseIterable, Identifiable, Sendable {
    case adaptive
    case full

    var id: Self { self }
}
