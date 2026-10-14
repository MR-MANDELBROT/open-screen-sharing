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
    case missionControl, appWindows, showDesktop, launchpad
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
        case .observe, .pointerLock, .home, .appSwitcher, .lock, .volumeUp, .volumeDown, .missionControl, .appWindows, .showDesktop, .launchpad:
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
        guard key.count == 1 || isNamed else {
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
    private static let specialKeys: [SpecialKey] = [
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
    ] + (1...12).map { number in
        SpecialKey(
            name: "f\(number)", equivalent: functionKeyEquivalent(number), symbol: "F\(number)",
            usage: UIKeyboardHIDUsage(rawValue: UIKeyboardHIDUsage.keyboardF1.rawValue + number - 1)!)
    }
    #else
    private static let specialKeys: [SpecialKey] = [
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
    ] + (1...12).map { number in
        SpecialKey(name: "f\(number)", equivalent: functionKeyEquivalent(number), symbol: "F\(number)", keyCode: MacKey.function(number).keyCode!)
    }
    #endif

    private static func functionKeyEquivalent(_ number: Int) -> KeyEquivalent {
        KeyEquivalent(Character(Unicode.Scalar(0xF703 + UInt32(number))!))
    }

    private static let macKeys: [String: MacKey] = {
        var keys: [String: MacKey] = [
            "upArrow": .up, "downArrow": .down, "leftArrow": .left, "rightArrow": .right, "return": .returnKey, "tab": .tab,
            "space": .space, "delete": .backspace, "deleteForward": .forwardDelete, "home": .home, "end": .end,
            "pageUp": .pageUp, "pageDown": .pageDown,
        ]
        for number in 1...12 {
            keys["f\(number)"] = .function(number)
        }
        return keys
    }()

    private static let pcKeysyms: [String: UInt32] = {
        var keysyms: [String: UInt32] = [
            "escape": Keysym.escape, "insert": Keysym.insert, "printScreen": Keysym.printScreen, "scrollLock": Keysym.scrollLock,
            "pause": Keysym.pause, "menu": Keysym.menu, "numLock": Keysym.numLock, "capsLock": Keysym.capsLock,
            "keypadEnter": Keysym.keypadEnter, "keypadPlus": Keysym.keypadPlus, "keypadMinus": Keysym.keypadMinus,
            "keypadMultiply": Keysym.keypadMultiply, "keypadDivide": Keysym.keypadDivide, "keypadDecimal": Keysym.keypadDecimal,
        ]
        for number in 13...24 {
            keysyms["f\(number)"] = Keysym.function(number)
        }
        for digit in 0...9 {
            keysyms["keypad\(digit)"] = Keysym.keypad(digit)
        }
        return keysyms
    }()

    private static let pcKeyNames = [
        "return": "Enter", "tab": "Tab", "space": "Space", "delete": "Backspace", "deleteForward": "Delete", "home": "Home", "end": "End",
        "pageUp": "Page Up", "pageDown": "Page Down", "escape": "Esc", "insert": "Insert", "printScreen": "Print Screen",
        "scrollLock": "Scroll Lock", "pause": "Pause", "menu": "Menu", "numLock": "Num Lock", "capsLock": "Caps Lock",
        "keypadEnter": "Numpad Enter", "keypadPlus": "Numpad +", "keypadMinus": "Numpad -", "keypadMultiply": "Numpad *",
        "keypadDivide": "Numpad /", "keypadDecimal": "Numpad .",
    ]

    static let pcKeyGroups: [PCKeyGroup] = [
        PCKeyGroup(title: String(localized: "Editing"), keys: ["return", "tab", "space", "delete", "deleteForward", "escape", "insert"]),
        PCKeyGroup(title: String(localized: "Navigation"), keys: ["home", "end", "pageUp", "pageDown", "upArrow", "downArrow", "leftArrow", "rightArrow"]),
        PCKeyGroup(title: String(localized: "Function Keys"), keys: (1...24).map { "f\($0)" }),
        PCKeyGroup(
            title: String(localized: "Keypad"),
            keys: ["numLock"] + (0...9).map { "keypad\($0)" } + ["keypadPlus", "keypadMinus", "keypadMultiply", "keypadDivide", "keypadDecimal", "keypadEnter"]
        ),
        PCKeyGroup(title: String(localized: "System"), keys: ["printScreen", "scrollLock", "pause", "menu", "capsLock"]),
    ]

    struct PCKeyGroup: Identifiable {
        let title: String
        let keys: [String]

        var id: String { title }
    }

    static func pcKeyLabel(_ name: String) -> String {
        if let label = pcKeyNames[name] { return label }
        if let special = specialKeys.first(where: { $0.name == name }) { return special.symbol }
        if name.hasPrefix("f"), let number = Int(name.dropFirst()) { return "F\(number)" }
        if name.hasPrefix("keypad"), let digit = Int(name.dropFirst(6)) { return "Numpad \(digit)" }
        return name.uppercased()
    }

    static let unset = KeyShortcut(key: "", modifiers: [])

    var isUnset: Bool { key.isEmpty }

    var isSpecial: Bool { Self.specialKeys.contains { $0.name == key } }

    var isNamed: Bool { isSpecial || Self.pcKeysyms[key] != nil }

    var eventModifiers: EventModifiers { EventModifiers(rawValue: modifiers) }

    var chord: KeyChord? {
        guard let macKey else { return nil }
        return KeyChord(modifiers: chordModifiers, key: macKey)
    }

    var pcChord: KeyChord? {
        guard let key = Self.pcKeysyms[key].map({ MacKey(keyCode: nil, keysym: $0) }) ?? macKey else { return nil }
        return KeyChord(modifiers: chordModifiers, key: key)
    }

    private var chordModifiers: [ModifierKey] {
        ModifierKey.allCases.filter { modifier in
            switch modifier {
            case .control: eventModifiers.contains(.control)
            case .option: eventModifiers.contains(.option)
            case .shift: eventModifiers.contains(.shift)
            case .command: eventModifiers.contains(.command)
            }
        }
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

    var pcDisplayName: String {
        var names: [String] = []
        if eventModifiers.contains(.control) { names.append("Ctrl") }
        if eventModifiers.contains(.option) { names.append("Alt") }
        if eventModifiers.contains(.shift) { names.append("Shift") }
        if eventModifiers.contains(.command) { names.append("Win") }
        names.append(Self.pcKeyLabel(key))
        return names.joined(separator: "+")
    }

    func displayName(for target: MappingTarget) -> String {
        target == .pc ? pcDisplayName : displayName
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

enum MappingTarget: CaseIterable, Identifiable {
    case apple
    case pc

    var id: Self { self }

    var title: String {
        switch self {
        case .apple: String(localized: "Apple Key Mappings")
        case .pc: String(localized: "PC Key Mappings")
        }
    }
}

struct KeyMapping: Codable, Hashable, Identifiable {
    enum Step: Codable, Hashable {
        case key(KeyShortcut)
        case text(String)

        func displayName(for target: MappingTarget) -> String {
            switch self {
            case .key(let shortcut):
                shortcut.displayName(for: target)
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

    func displayName(for target: MappingTarget) -> String {
        steps.map { $0.displayName(for: target) }.joined(separator: " ")
    }
}

final class KeyShortcuts: ObservableObject {
    static let shared = KeyShortcuts()

    private static let key = "keyboardShortcuts"

    private static func mappingsKey(_ target: MappingTarget) -> String {
        switch target {
        case .apple: "keyboardMappings"
        case .pc: "pcKeyboardMappings"
        }
    }

    @Published private var assigned: [String: KeyShortcut]
    @Published private var mappings: [MappingTarget: [KeyMapping]]
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
        mappings = Dictionary(uniqueKeysWithValues: MappingTarget.allCases.map { target in
            (target, store.value([KeyMapping].self, for: Self.mappingsKey(target)) ?? [])
        })
        #if os(macOS)
        pointerRelease = store.value(UInt.self, for: Self.pointerReleaseKey).map(NSEvent.ModifierFlags.init(rawValue:)) ?? Self.defaultPointerRelease
        #endif
    }

    var isCustomized: Bool {
        #if os(macOS)
        if pointerRelease != Self.defaultPointerRelease { return true }
        #endif
        return !assigned.isEmpty || mappings.values.contains { !$0.isEmpty }
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

    func mappings(for target: MappingTarget) -> [KeyMapping] {
        mappings[target] ?? []
    }

    func mapping(triggeredBy shortcut: KeyShortcut, for target: MappingTarget) -> KeyMapping? {
        mappings(for: target).first { $0.trigger == shortcut }
    }

    func add(_ mapping: KeyMapping, to target: MappingTarget) {
        var list = mappings(for: target)
        list.append(mapping)
        setMappings(list, for: target)
    }

    func update(_ mapping: KeyMapping, in target: MappingTarget) {
        var list = mappings(for: target)
        guard let index = list.firstIndex(where: { $0.id == mapping.id }) else { return }
        list[index] = mapping
        setMappings(list, for: target)
    }

    func removeMapping(_ id: UUID, from target: MappingTarget) {
        var list = mappings(for: target)
        list.removeAll { $0.id == id }
        setMappings(list, for: target)
    }

    private func setMappings(_ list: [KeyMapping], for target: MappingTarget) {
        mappings[target] = list
        store.set(list, for: Self.mappingsKey(target))
    }

    func restoreDefaults() {
        assigned = [:]
        store.remove(Self.key)
        for target in MappingTarget.allCases {
            mappings[target] = []
            store.remove(Self.mappingsKey(target))
        }
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
    #else
    // The SwiftUI App lifecycle on iPadOS consumes these chords before the first responder's pressesBegan,
    // unless the responder chain declares a key command for them; one whose action nobody performs lets the press through.
    private static let swiftUIConsumed: [(input: String, flags: UIKeyModifierFlags)] = [
        ("b", .command), ("i", .command), ("u", .command), ("e", .command), ("f", .command), ("f", [.command, .alternate]),
        ("g", .command), ("g", [.command, .shift]), ("-", .command), ("=", [.command, .shift]), ("[", [.command, .shift]),
        ("]", [.command, .shift]), ("\\", [.command, .shift]), ("s", [.command, .control]),
    ]

    func passthroughKeyCommands(includingAssigned: Bool) -> [UIKeyCommand] {
        Self.swiftUIConsumed.compactMap { input, flags in
            var modifiers: EventModifiers = []
            if flags.contains(.control) { modifiers.insert(.control) }
            if flags.contains(.alternate) { modifiers.insert(.option) }
            if flags.contains(.shift) { modifiers.insert(.shift) }
            if flags.contains(.command) { modifiers.insert(.command) }
            guard includingAssigned || action(using: KeyShortcut(key: input, modifiers: modifiers)) == nil else { return nil }
            let command = UIKeyCommand(input: input, modifierFlags: flags, action: NSSelectorFromString("passKeyThrough:"))
            command.wantsPriorityOverSystemBehavior = true
            return command
        }
    }
    #endif
}

struct ActionShortcut: ViewModifier {
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
