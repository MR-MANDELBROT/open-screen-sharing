import Foundation

nonisolated struct MacInputSource: Hashable, Sendable {
    let id: String

    var isInputMethod: Bool { !id.hasPrefix("com.apple.keylayout.") }
}
