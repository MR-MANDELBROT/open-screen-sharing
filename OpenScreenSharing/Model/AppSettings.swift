import Combine
import Foundation

final class AppSettings: ObservableObject {
    static let shared = AppSettings()

    private enum Key {
        static let showsDebugInfo = "showsDebugInfo"
        static let usesRemotePointerStyle = "usesRemotePointerStyle"
        static let computerLayout = "computerLayout"
        static let computerOrder = "computerOrder"
        static let seenTutorials = "seenTutorials"
    }

    @Published var showsDebugInfo: Bool {
        didSet { store.set(showsDebugInfo, for: Key.showsDebugInfo) }
    }

    @Published var usesRemotePointerStyle: Bool {
        didSet { store.set(usesRemotePointerStyle, for: Key.usesRemotePointerStyle) }
    }

    @Published var computerLayout: ComputerLayout {
        didSet { store.set(computerLayout, for: Key.computerLayout) }
    }

    @Published var computerOrder: [UUID] {
        didSet { store.set(computerOrder, for: Key.computerOrder) }
    }

    @Published var seenTutorials: Set<SessionTutorial> {
        didSet { store.set(seenTutorials, for: Key.seenTutorials) }
    }

    private let store = PreferenceStore.shared

    init() {
        showsDebugInfo = store.value(Bool.self, for: Key.showsDebugInfo) ?? false
        usesRemotePointerStyle = store.value(Bool.self, for: Key.usesRemotePointerStyle) ?? false
        computerLayout = store.value(ComputerLayout.self, for: Key.computerLayout) ?? .grid
        computerOrder = store.value([UUID].self, for: Key.computerOrder) ?? []
        seenTutorials = store.value(Set<SessionTutorial>.self, for: Key.seenTutorials) ?? []
    }
}

enum ComputerLayout: String, Codable {
    case grid
    case list
}
