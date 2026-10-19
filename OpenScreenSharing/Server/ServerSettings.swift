import Foundation
import Perception

@Perceptible
final class ServerSettings {
    static let shared = ServerSettings()

    static let defaultPort = 5900
    static let portRange = 1...65533

    private enum Key {
        static let enabled = "serverEnabled"
        static let port = "serverPort"
        static let authenticates = "serverAuthenticates"
        static let username = "serverUsername"
        static let password = "serverPassword"
        static let runsInBackground = "serverRunsInBackground"
        static let backgroundMode = "serverBackgroundMode"
        static let autoUnlocks = "serverAutoUnlocks"
        static let autoUnlockPasscodeType = "serverAutoUnlockPasscodeType"
        static let autoUnlockPasscode = "serverAutoUnlockPasscode"
    }

    var isEnabled: Bool {
        didSet { store.set(isEnabled, for: Key.enabled) }
    }

    var port: Int {
        didSet { store.set(port, for: Key.port) }
    }

    var authenticates: Bool {
        didSet { store.set(authenticates, for: Key.authenticates) }
    }

    var username: String {
        didSet { store.set(username, for: Key.username) }
    }

    var runsInBackground: Bool {
        didSet { store.set(runsInBackground, for: Key.runsInBackground) }
    }

    var backgroundMode: ServerBackgroundMode {
        didSet { store.set(backgroundMode, for: Key.backgroundMode) }
    }

    var password: String {
        didSet { store.set(password, for: Key.password) }
    }

    var autoUnlocks: Bool {
        didSet { store.set(autoUnlocks, for: Key.autoUnlocks) }
    }

    var autoUnlockPasscodeType: ServerPasscodeType {
        didSet { store.set(autoUnlockPasscodeType, for: Key.autoUnlockPasscodeType) }
    }

    var autoUnlockPasscode: String {
        didSet { store.set(autoUnlockPasscode, for: Key.autoUnlockPasscode) }
    }

    private let store = PreferenceStore.shared

    init() {
        isEnabled = store.value(Bool.self, for: Key.enabled) ?? false
        port = store.value(Int.self, for: Key.port) ?? Self.defaultPort
        authenticates = store.value(Bool.self, for: Key.authenticates) ?? false
        username = store.value(String.self, for: Key.username) ?? ""
        runsInBackground = store.value(Bool.self, for: Key.runsInBackground) ?? true
        backgroundMode = store.value(ServerBackgroundMode.self, for: Key.backgroundMode) ?? .location
        password = store.value(String.self, for: Key.password) ?? ""
        autoUnlocks = store.value(Bool.self, for: Key.autoUnlocks) ?? false
        autoUnlockPasscodeType = store.value(ServerPasscodeType.self, for: Key.autoUnlockPasscodeType) ?? .pin
        autoUnlockPasscode = store.value(String.self, for: Key.autoUnlockPasscode) ?? ""
    }

    var effectiveUsername: String {
        username.trimmingCharacters(in: .whitespaces)
    }

    var isComplete: Bool {
        (!authenticates || (!effectiveUsername.isEmpty && !password.isEmpty)) && autoUnlockProblem == nil
    }

    var autoUnlockProblem: String? {
        autoUnlocks ? autoUnlockPasscodeType.problem(for: autoUnlockPasscode) : nil
    }
}

enum ServerPasscodeType: Int32, Codable {
    case pin = 0
    case numeric = 1
    case alphanumeric = 2

    func problem(for passcode: String) -> String? {
        guard !passcode.isEmpty else { return String(localized: "Enter this device’s lock screen passcode.") }
        guard passcode.utf8.count <= 64 else { return String(localized: "Automatic unlock supports passcodes up to 64 characters.") }
        let isNumeric = passcode.utf8.allSatisfy { (0x30...0x39).contains($0) }
        switch self {
        case .pin:
            guard isNumeric && (passcode.utf8.count == 4 || passcode.utf8.count == 6) else {
                return String(localized: "Enter a 4-digit or 6-digit passcode, or select the passcode type used by this device.")
            }
        case .numeric:
            guard isNumeric else { return String(localized: "A custom numeric passcode can contain only digits from 0 to 9.") }
        case .alphanumeric:
            guard passcode.utf8.allSatisfy({ (0x20...0x7e).contains($0) }) else {
                return String(localized: "Automatic unlock supports English letters, numbers, spaces, and standard punctuation. Other characters aren’t supported.")
            }
        }
        return nil
    }
}

enum ServerBackgroundMode: String, Codable {
    case location
    case audio
}
