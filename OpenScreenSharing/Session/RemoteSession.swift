import CoreGraphics
import CoreMedia
import CoreVideo
import Foundation

nonisolated enum SessionPhase: Equatable, Sendable {
    case idle
    case connecting
    case authenticating
    case waitingForPermission
    case choosingSession(SessionChoicePrompt)
    case connected
    case reconnecting(attempt: Int)
    case disconnected
    case failed(SessionFailure)

    var isActive: Bool {
        switch self {
        case .connected, .reconnecting: true
        default: false
        }
    }
}

nonisolated struct SessionFailure: Error, Equatable, Sendable {
    enum Kind: Equatable, Sendable {
        case authenticationFailed
        case permissionDenied
        case hostNotFound
        case ownScreen
        case unreachable
        case connectionLost
        case closedByServer
        case unsupported
        case highPerformance(MediaFailure.Kind)
        case drawingFailed
        case ended
        case other
    }

    var kind: Kind
    var message: String

    init(_ kind: Kind, message: String = "") {
        self.kind = kind
        self.message = message
    }
}

nonisolated struct SessionChoicePrompt: Equatable, Sendable {
    var id: Int64
    var consoleUser: String
    var allowsShareConsole: Bool
    var allowsOwnSession: Bool
}

nonisolated enum SessionChoice: Sendable {
    case shareConsole
    case ownSession
    case cancel
}

nonisolated enum SessionMode: Equatable, Sendable {
    case control
    case observe

    var controls: Bool { self == .control }
}

nonisolated struct RemoteDisplay: Identifiable, Hashable, Sendable {
    var id: Int64
    var name: String
    var width: Int
    var height: Int
    var isMain: Bool
    var isVirtual: Bool = false
}

nonisolated enum DisplaySelection: Hashable, Sendable {
    case display(Int64)
    case all
}

nonisolated struct MediaFailure: Equatable, Sendable {
    enum Kind: Equatable, Sendable {
        case busy
        case network
        case refused
        case silent
        case unsupported
        case undecodable
        case other
    }

    var kind: Kind
    var message: String

    init(_ kind: Kind, message: String = "") {
        self.kind = kind
        self.message = message
    }
}

nonisolated enum RemoteAudio: String, Codable, CaseIterable, Identifiable, Sendable {
    case off
    case fromMac
    case toMac

    var id: Self { self }
}

nonisolated enum SessionEvent: Sendable {
    case bell
    case displaysAsleep
    case displaysAwake
    case userSessionChanged
    case sessionEnding
    case busy(Bool)
    case pointerVisible(Bool)
    case controlAllowed(Bool)
    case clipboardUnreadable(String)
    case clipboardNotRestored(String)
}

typealias ClipboardContent = [[String: Data]]

nonisolated struct FileTransferError: Error, Equatable, Sendable {
    enum Kind: Equatable, Sendable {
        case cancelled
        case notAllowed
        case notFound
        case notWritable
        case diskFull
        case permission
        case local
        case connection
        case observeOnly
        case unsupported
        case failed
    }

    var kind: Kind
    var message: String

    init(_ kind: Kind, message: String = "") {
        self.kind = kind
        self.message = message
    }
}

nonisolated struct PointerButtons: OptionSet, Hashable, Sendable {
    let rawValue: UInt8

    static let primary = PointerButtons(rawValue: 1 << 0)
    static let middle = PointerButtons(rawValue: 1 << 1)
    static let secondary = PointerButtons(rawValue: 1 << 2)
}

nonisolated struct CursorShape: @unchecked Sendable {
    var image: CGImage
    var hotspot: CGPoint
}

nonisolated struct DeviceInfo: Equatable, Sendable {
    var model: String
    var enclosureColor: String
}

nonisolated struct SessionCredentials: Sendable, Equatable {
    var loginType: LoginType
    var username: String
    var password: String
}

nonisolated struct SessionConfiguration: Sendable {
    var address: ComputerAddress
    var displayName: String
    var clientName: String
    var credentials: SessionCredentials
    var quality: SessionQuality
    var startsInObserveMode: Bool
    var sharesClipboard: Bool
    var remoteAudio: RemoteAudio
    var updateRate: Int
    var networkConfiguration = NetworkConfiguration()
}

nonisolated struct SessionInfo: Equatable, Sendable {
    var videoCodec: String?
    var framebufferWidth: Int = 0
    var framebufferHeight: Int = 0
    var framebufferPixelsPerCursorPixel: Double = 1
    var isMac: Bool = true
    var keyboardSource: String?
    var secureInput: Bool = false
}

nonisolated struct SessionCapabilities: OptionSet, Hashable, Sendable {
    let rawValue: UInt32

    static let control = SessionCapabilities(rawValue: 1 << 0)
    static let highPerformance = SessionCapabilities(rawValue: 1 << 2)
    static let clipboard = SessionCapabilities(rawValue: 1 << 3)
    static let displaySelection = SessionCapabilities(rawValue: 1 << 4)
    static let virtualDisplay = SessionCapabilities(rawValue: 1 << 5)
    static let fileTransfer = SessionCapabilities(rawValue: 1 << 6)
    static let keyboardSource = SessionCapabilities(rawValue: 1 << 7)
    static let gestures = SessionCapabilities(rawValue: 1 << 8)
    static let assistCursor = SessionCapabilities(rawValue: 1 << 9)
    static let missionControl = SessionCapabilities(rawValue: 1 << 10)
    static let appWindows = SessionCapabilities(rawValue: 1 << 11)
    static let showDesktop = SessionCapabilities(rawValue: 1 << 12)
    static let launchpad = SessionCapabilities(rawValue: 1 << 13)
    static let keyCodes = SessionCapabilities(rawValue: 1 << 14)
}

nonisolated enum GesturePhase: Sendable {
    case mayBegin
    case began
    case changed
    case ended
    case cancelled
}

nonisolated enum SwipeDirection: Sendable {
    case back
    case forward
    case up
    case down
}

nonisolated enum SystemCommand: CaseIterable, Identifiable, Sendable {
    case missionControl
    case appWindows
    case showDesktop
    case launchpad

    var id: Self { self }

    var capability: SessionCapabilities {
        switch self {
        case .missionControl: .missionControl
        case .appWindows: .appWindows
        case .showDesktop: .showDesktop
        case .launchpad: .launchpad
        }
    }
}

nonisolated enum AssistPointer: Sendable {
    case highlight
    case arrow
}

nonisolated enum VideoCodec: String, Codable, Sendable {
    case h264
    case hevc
}

nonisolated struct VideoFormat: @unchecked Sendable {
    var codec: VideoCodec
    var parameterSets: [Data]
    var bands: Int
    var canvasHeight: Int = 0
    var isHDR = false
}

nonisolated struct VideoAccessUnit: @unchecked Sendable {
    var data: Data
    var isSync: Bool
    var presentationTime: CMTime
    var band: Int
    var bands: Int
    var frameID: Int64
    var frameEnd: Bool
    var changedBands: Int
    var discontinuity: Bool
    var turns: PictureTurns
    var arrival: CFTimeInterval
    var libraryHold: TimeInterval
}

nonisolated struct FrameTiming: Sendable {
    var arrival: CFTimeInterval
    var libraryHold: TimeInterval
    var decode: TimeInterval
    var capture: CFTimeInterval
}

nonisolated struct DecodedVideoFrame: @unchecked Sendable {
    var pixelBuffer: CVPixelBuffer?
    var strips: [CVPixelBuffer]
    var canvasHeight: Int
    var turns: PictureTurns
    var timing: FrameTiming
}

nonisolated struct AudioFormat: Sendable {
    var sampleRate: Double
    var channels: UInt32
    var framesPerPacket: UInt32
    var magicCookie: Data
}

nonisolated struct AudioAccessUnit: Sendable {
    var data: Data
    var timestamp: Int64
    var lost: Int
}

nonisolated struct SessionStatistics: Sendable {
    var roundTripMilliseconds: Double
    var receivedBytes: Int64
    var encoding: String?
    var video: VideoStreamStatistics?
}

nonisolated struct VideoStreamStatistics: Sendable {
    var packets: Int64
    var expectedPackets: Int64
    var keyframeRequests: Int64
    var queuingDelayMilliseconds: Double
}
