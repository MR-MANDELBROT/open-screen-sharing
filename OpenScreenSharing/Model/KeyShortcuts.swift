import Combine
import SwiftUI
#if os(iOS)
import UIKit
#else
import AppKit
#endif

enum ShortcutGroup: CaseIterable, Identifiable {
    case file, edit, view, sound, control, debug

    var id: Self { self }

    var title: String {
        switch self {
        case .file: String(localized: "File")
        case .edit: String(localized: "Edit Menu", defaultValue: "Edit")
        case .view: String(localized: "View")
        case .sound: String(localized: "Sound")
        case .control: String(localized: "Control")
        case .debug: String(localized: "Debug")
        }
    }

    var actions: [ShortcutAction] {
        ShortcutAction.allCases.filter { $0.group == self }
    }
}

enum ShortcutAction: String, CaseIterable, Identifiable {
    case newConnection, disconnect
    case syncClipboard, receiveClipboard, sendClipboard
    #if os(macOS)
    case viewAsIcons, viewAsList
    #endif
    case zoomMode, actualSize
    case display1, display2, display3, display4, display5, display6, display7, display8, allDisplays
    case adaptiveQuality, fullQuality, hdr
    #if os(iOS)
    case toolbar
    #endif
    case customizeToolbar
    case noAudio, receiveAudio, sendAudio
    case observe, pointerLock
    #if os(iOS)
    case keyboard
    #endif
    case home, appSwitcher, lock, volumeUp, volumeDown
    case missionControl, appWindows, showDesktop, launchpad, spotlight, commandTab
    case debugPanel

    var id: Self { self }

    static let displays: [ShortcutAction] = [.display1, .display2, .display3, .display4, .display5, .display6, .display7, .display8]

    static func display(at index: Int) -> ShortcutAction? {
        displays.indices.contains(index) ? displays[index] : nil
    }

    static func quality(_ quality: SessionQuality) -> ShortcutAction {
        switch quality {
        case .adaptive: .adaptiveQuality
        case .full: .fullQuality
        }
    }

    static func audio(_ audio: RemoteAudio) -> ShortcutAction {
        switch audio {
        case .off: .noAudio
        case .fromMac: .receiveAudio
        case .toMac: .sendAudio
        }
    }

    static func system(_ command: SystemCommand) -> ShortcutAction {
        switch command {
        case .missionControl: .missionControl
        case .appWindows: .appWindows
        case .showDesktop: .showDesktop
        case .launchpad: .launchpad
        }
    }

    var group: ShortcutGroup {
        switch self {
        case .newConnection, .disconnect: .file
        case .syncClipboard, .receiveClipboard, .sendClipboard: .edit
        case .noAudio, .receiveAudio, .sendAudio: .sound
        case .observe, .pointerLock, .home, .appSwitcher, .lock, .volumeUp, .volumeDown, .missionControl, .appWindows, .showDesktop, .launchpad,
            .spotlight, .commandTab:
            .control
        #if os(iOS)
        case .keyboard: .control
        #endif
        case .debugPanel: .debug
        default: .view
        }
    }

    var title: String {
        switch self {
        case .newConnection: return String(localized: "New Connection")
        case .disconnect: return String(localized: "Disconnect")
        case .syncClipboard: return String(localized: "Sync Clipboard")
        case .receiveClipboard: return String(localized: "Receive Clipboard")
        case .sendClipboard: return String(localized: "Send Clipboard")
        #if os(macOS)
        case .viewAsIcons: return String(localized: "View as Icons")
        case .viewAsList: return String(localized: "View as List")
        #endif
        case .zoomMode: return String(localized: "Zoom Mode")
        case .actualSize: return String(localized: "Zoom to Fit / Actual Size")
        case .display1, .display2, .display3, .display4, .display5, .display6, .display7, .display8:
            return String(localized: "Display \((Self.displays.firstIndex(of: self) ?? 0) + 1)")
        case .allDisplays: return String(localized: "All Displays")
        case .adaptiveQuality: return String(localized: SessionQuality.adaptive.title)
        case .fullQuality: return String(localized: SessionQuality.full.title)
        case .hdr: return String(localized: "High Dynamic Range")
        #if os(iOS)
        case .toolbar: return String(localized: "Show/Hide Toolbar")
        #endif
        case .customizeToolbar: return String(localized: "Customize Toolbar")
        #if os(iOS)
        case .keyboard: return String(localized: "Show/Hide Keyboard")
        #endif
        case .noAudio: return String(localized: RemoteAudio.off.title)
        case .receiveAudio: return String(localized: RemoteAudio.fromMac.title)
        case .sendAudio: return String(localized: RemoteAudio.toMac.title)
        case .observe: return String(localized: "Observe Mode")
        case .pointerLock: return String(localized: "Pointer Lock")
        case .home: return String(localized: SessionCommand.home.title)
        case .appSwitcher: return String(localized: SessionCommand.appSwitcher.title)
        case .lock: return String(localized: SessionCommand.lock.title)
        case .volumeUp: return String(localized: SessionCommand.volumeUp.title)
        case .volumeDown: return String(localized: SessionCommand.volumeDown.title)
        case .missionControl: return String(localized: SystemCommand.missionControl.title)
        case .appWindows: return String(localized: SystemCommand.appWindows.title)
        case .showDesktop: return String(localized: SystemCommand.showDesktop.title)
        case .launchpad: return String(localized: SystemCommand.launchpad.title)
        case .spotlight: return String(localized: "Spotlight")
        case .commandTab: return String(localized: "Command-Tab")
        case .debugPanel: return String(localized: "Debug Panel")
        }
    }
}

struct KeyShortcut: Codable, Hashable {
    var key: String
    var modifiers: Int

    init(key: String, modifiers: EventModifiers) {
        self.key = key
        self.modifiers = modifiers.rawValue
    }

    init(from decoder: any Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        key = try container.decode(String.self, forKey: .key)
        modifiers = try container.decode(Int.self, forKey: .modifiers)
        guard key.count == 1 || isSpecial else {
            throw DecodingError.dataCorruptedError(forKey: .key, in: container, debugDescription: "unknown key \"\(key)\"")
        }
    }

    private struct SpecialKey {
        let name: String
        let equivalent: KeyEquivalent
        let symbol: String
        #if os(iOS)
        let usage: UIKeyboardHIDUsage
        #else
        let keyCode: UInt16
        #endif
    }

    #if os(iOS)
    private static let specialKeys = [
        SpecialKey(name: "upArrow", equivalent: .upArrow, symbol: "↑", usage: .keyboardUpArrow),
        SpecialKey(name: "downArrow", equivalent: .downArrow, symbol: "↓", usage: .keyboardDownArrow),
        SpecialKey(name: "leftArrow", equivalent: .leftArrow, symbol: "←", usage: .keyboardLeftArrow),
        SpecialKey(name: "rightArrow", equivalent: .rightArrow, symbol: "→", usage: .keyboardRightArrow),
        SpecialKey(name: "return", equivalent: .return, symbol: "↩", usage: .keyboardReturnOrEnter),
        SpecialKey(name: "tab", equivalent: .tab, symbol: "⇥", usage: .keyboardTab),
        SpecialKey(name: "space", equivalent: .space, symbol: "Space", usage: .keyboardSpacebar),
        SpecialKey(name: "delete", equivalent: .delete, symbol: "⌫", usage: .keyboardDeleteOrBackspace),
        SpecialKey(name: "deleteForward", equivalent: .deleteForward, symbol: "⌦", usage: .keyboardDeleteForward),
        SpecialKey(name: "home", equivalent: .home, symbol: "↖", usage: .keyboardHome),
        SpecialKey(name: "end", equivalent: .end, symbol: "↘", usage: .keyboardEnd),
        SpecialKey(name: "pageUp", equivalent: .pageUp, symbol: "⇞", usage: .keyboardPageUp),
        SpecialKey(name: "pageDown", equivalent: .pageDown, symbol: "⇟", usage: .keyboardPageDown),
    ]
    #else
    private static let specialKeys = [
        SpecialKey(name: "upArrow", equivalent: .upArrow, symbol: "↑", keyCode: 126),
        SpecialKey(name: "downArrow", equivalent: .downArrow, symbol: "↓", keyCode: 125),
        SpecialKey(name: "leftArrow", equivalent: .leftArrow, symbol: "←", keyCode: 123),
        SpecialKey(name: "rightArrow", equivalent: .rightArrow, symbol: "→", keyCode: 124),
        SpecialKey(name: "return", equivalent: .return, symbol: "↩", keyCode: 36),
        SpecialKey(name: "tab", equivalent: .tab, symbol: "⇥", keyCode: 48),
        SpecialKey(name: "space", equivalent: .space, symbol: "Space", keyCode: 49),
        SpecialKey(name: "delete", equivalent: .delete, symbol: "⌫", keyCode: 51),
        SpecialKey(name: "deleteForward", equivalent: .deleteForward, symbol: "⌦", keyCode: 117),
        SpecialKey(name: "home", equivalent: .home, symbol: "↖", keyCode: 115),
        SpecialKey(name: "end", equivalent: .end, symbol: "↘", keyCode: 119),
        SpecialKey(name: "pageUp", equivalent: .pageUp, symbol: "⇞", keyCode: 116),
        SpecialKey(name: "pageDown", equivalent: .pageDown, symbol: "⇟", keyCode: 121),
    ]
    #endif

    private static let macKeys: [String: MacKey] = [
        "upArrow": .up, "downArrow": .down, "leftArrow": .left, "rightArrow": .right, "return": .returnKey, "tab": .tab,
        "space": .space, "delete": .backspace, "deleteForward": .forwardDelete, "home": .home, "end": .end,
        "pageUp": .pageUp, "pageDown": .pageDown,
    ]

    static let unset = KeyShortcut(key: "", modifiers: [])

    var isUnset: Bool { key.isEmpty }

    var isSpecial: Bool { Self.specialKeys.contains { $0.name == key } }

    var eventModifiers: EventModifiers { EventModifiers(rawValue: modifiers) }

    var chord: KeyChord? {
        guard let macKey else { return nil }
        var modifiers: [ModifierKey] = []
        if eventModifiers.contains(.control) { modifiers.append(.control) }
        if eventModifiers.contains(.option) { modifiers.append(.option) }
        if eventModifiers.contains(.shift) { modifiers.append(.shift) }
        if eventModifiers.contains(.command) { modifiers.append(.command) }
        return KeyChord(modifiers: modifiers, key: macKey)
    }

    private var macKey: MacKey? {
        if let named = Self.macKeys[key] { return named }
        guard key.count == 1, let scalar = key.unicodeScalars.first else { return nil }
        return MacKey(keysym: Keysym.character(scalar))
    }

    var keyboardShortcut: KeyboardShortcut {
        KeyboardShortcut(Self.specialKeys.first { $0.name == key }?.equivalent ?? KeyEquivalent(Character(key)), modifiers: eventModifiers)
    }

    var hasCommandModifier: Bool {
        !eventModifiers.isDisjoint(with: [.command, .option, .control])
    }

    var displayName: String {
        let name = eventModifiers.symbols
        if let special = Self.specialKeys.first(where: { $0.name == key }) {
            return name + (special.name == "space" ? String(localized: "Space") : special.symbol)
        }
        return name + key.uppercased()
    }

    private init?(character: String?, modifiers: EventModifiers) {
        guard let character = character?.lowercased(), character.count == 1, let scalar = character.unicodeScalars.first,
            !CharacterSet.controlCharacters.contains(scalar), !(0xF700...0xF8FF).contains(scalar.value)
        else { return nil }
        self.init(key: character, modifiers: modifiers)
    }

    #if os(iOS)
    init?(key press: UIKey) {
        var modifiers: EventModifiers = []
        if press.modifierFlags.contains(.control) { modifiers.insert(.control) }
        if press.modifierFlags.contains(.alternate) { modifiers.insert(.option) }
        if press.modifierFlags.contains(.shift) { modifiers.insert(.shift) }
        if press.modifierFlags.contains(.command) { modifiers.insert(.command) }
        if let special = Self.specialKeys.first(where: { $0.usage == press.keyCode }) {
            self.init(key: special.name, modifiers: modifiers)
        } else {
            self.init(character: press.charactersIgnoringModifiers, modifiers: modifiers)
        }
    }
    #else
    init?(event: NSEvent) {
        let modifiers = EventModifiers(event.modifierFlags)
        if let special = Self.specialKeys.first(where: { $0.keyCode == event.keyCode }) {
            self.init(key: special.name, modifiers: modifiers)
        } else {
            self.init(character: event.characters(byApplyingModifiers: []), modifiers: modifiers)
        }
    }
    #endif
}

extension EventModifiers {
    var symbols: String {
        var symbols = ""
        if contains(.control) { symbols += "⌃" }
        if contains(.option) { symbols += "⌥" }
        if contains(.shift) { symbols += "⇧" }
        if contains(.command) { symbols += "⌘" }
        return symbols
    }

    #if os(macOS)
    init(_ flags: NSEvent.ModifierFlags) {
        self = []
        if flags.contains(.control) { insert(.control) }
        if flags.contains(.option) { insert(.option) }
        if flags.contains(.shift) { insert(.shift) }
        if flags.contains(.command) { insert(.command) }
    }
    #endif
}

struct KeyMapping: Codable, Hashable, Identifiable {
    enum Step: Codable, Hashable {
        case keys(KeyShortcut)
        case text(String)

        var displayName: String {
            switch self {
            case .keys(let keys):
                keys.displayName
            case .text(let text):
                "“"
                    + text.map { character in
                        switch character {
                        case "\n", "\r", "\r\n": "↩"
                        case "\t": "⇥"
                        default: String(character)
                        }
                    }.joined() + "”"
            }
        }
    }

    var id: UUID
    var trigger: KeyShortcut
    var steps: [Step]

    var displayName: String {
        steps.map(\.displayName).joined(separator: " ")
    }
}

final class KeyShortcuts: ObservableObject {
    static let shared = KeyShortcuts()

    private static let key = "keyboardShortcuts"
    private static let mappingsKey = "keyboardMappings"

    @Published private var assigned: [String: KeyShortcut]
    @Published private(set) var mappings: [KeyMapping]
    @Published var isRecording = false

    #if os(macOS)
    private static let pointerReleaseKey = "pointerReleaseModifiers"
    static let defaultPointerRelease: NSEvent.ModifierFlags = [.control, .option]

    @Published private(set) var pointerRelease: NSEvent.ModifierFlags
    #endif

    private let store: PreferenceStore

    init(store: PreferenceStore = .shared) {
        self.store = store
        assigned = store.value([String: KeyShortcut].self, for: Self.key) ?? [:]
        mappings = store.value([KeyMapping].self, for: Self.mappingsKey) ?? []
        #if os(macOS)
        pointerRelease = store.value(UInt.self, for: Self.pointerReleaseKey).map(NSEvent.ModifierFlags.init(rawValue:)) ?? Self.defaultPointerRelease
        #endif
    }

    var isCustomized: Bool {
        #if os(macOS)
        if pointerRelease != Self.defaultPointerRelease { return true }
        #endif
        return !assigned.isEmpty || !mappings.isEmpty
    }

    func shortcut(for action: ShortcutAction) -> KeyShortcut? {
        assigned[action.rawValue]
    }

    func action(using shortcut: KeyShortcut) -> ShortcutAction? {
        ShortcutAction.allCases.first { self.shortcut(for: $0) == shortcut }
    }

    func set(_ shortcut: KeyShortcut?, for action: ShortcutAction) {
        assigned[action.rawValue] = shortcut
        store.set(assigned, for: Self.key)
    }

    func mapping(triggeredBy shortcut: KeyShortcut) -> KeyMapping? {
        mappings.first { $0.trigger == shortcut }
    }

    func add(_ mapping: KeyMapping) {
        mappings.append(mapping)
        store.set(mappings, for: Self.mappingsKey)
    }

    func update(_ mapping: KeyMapping) {
        guard let index = mappings.firstIndex(where: { $0.id == mapping.id }) else { return }
        mappings[index] = mapping
        store.set(mappings, for: Self.mappingsKey)
    }

    func removeMapping(_ id: UUID) {
        mappings.removeAll { $0.id == id }
        store.set(mappings, for: Self.mappingsKey)
    }

    func restoreDefaults() {
        assigned = [:]
        mappings = []
        store.remove(Self.key)
        store.remove(Self.mappingsKey)
        #if os(macOS)
        pointerRelease = Self.defaultPointerRelease
        store.remove(Self.pointerReleaseKey)
        #endif
    }

    #if os(macOS)
    func setPointerRelease(_ modifiers: NSEvent.ModifierFlags) {
        pointerRelease = modifiers
        store.set(modifiers.rawValue, for: Self.pointerReleaseKey)
    }

    func isAssigned(_ event: NSEvent) -> Bool {
        KeyShortcut(event: event).map { action(using: $0) != nil } ?? false
    }
    #endif
}

private struct ActionShortcut: ViewModifier {
    @ObservedObject private var shortcuts = KeyShortcuts.shared
    let action: ShortcutAction?

    func body(content: Content) -> some View {
        if let action, !shortcuts.isRecording, let shortcut = shortcuts.shortcut(for: action) {
            content.keyboardShortcut(shortcut.keyboardShortcut)
        } else {
            content
        }
    }
}

extension View {
    func keyboardShortcut(for action: ShortcutAction?) -> some View {
        modifier(ActionShortcut(action: action))
    }
}
