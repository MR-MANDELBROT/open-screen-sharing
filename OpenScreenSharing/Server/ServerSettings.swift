import Combine
import Foundation

final class ServerSettings: ObservableObject {
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
    }

    @Published var isEnabled: Bool {
        didSet { store.set(isEnabled, for: Key.enabled) }
    }

    @Published var port: Int {
        didSet { store.set(port, for: Key.port) }
    }

    @Published var authenticates: Bool {
        didSet { store.set(authenticates, for: Key.authenticates) }
    }

    @Published var username: String {
        didSet { store.set(username, for: Key.username) }
    }

    @Published var runsInBackground: Bool {
        didSet { store.set(runsInBackground, for: Key.runsInBackground) }
    }

    @Published var backgroundMode: ServerBackgroundMode {
        didSet { store.set(backgroundMode, for: Key.backgroundMode) }
    }

    @Published var password: String {
        didSet { store.set(password, for: Key.password) }
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
    }

    var effectiveUsername: String {
        username.trimmingCharacters(in: .whitespaces)
    }

    var isComplete: Bool {
        !authenticates || (!effectiveUsername.isEmpty && !password.isEmpty)
    }
}

enum ServerBackgroundMode: String, Codable {
    case location
    case audio
}
