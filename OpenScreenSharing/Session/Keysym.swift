import Foundation
#if os(iOS)
import UIKit
#endif

nonisolated enum Keysym {
    static let backSpace: UInt32 = 0xff08
    static let tab: UInt32 = 0xff09
    static let returnKey: UInt32 = 0xff0d
    static let escape: UInt32 = 0xff1b
    static let forwardDelete: UInt32 = 0xffff
    static let home: UInt32 = 0xff50
    static let left: UInt32 = 0xff51
    static let up: UInt32 = 0xff52
    static let right: UInt32 = 0xff53
    static let down: UInt32 = 0xff54
    static let pageUp: UInt32 = 0xff55
    static let pageDown: UInt32 = 0xff56
    static let end: UInt32 = 0xff57
    static let insert: UInt32 = 0xff63
    static let menu: UInt32 = 0xff67
    static let help: UInt32 = 0xff6a
    static let numLock: UInt32 = 0xff7f
    static let space: UInt32 = 0x20

    static let shiftLeft: UInt32 = 0xffe1
    static let shiftRight: UInt32 = 0xffe2
    static let controlLeft: UInt32 = 0xffe3
    static let controlRight: UInt32 = 0xffe4
    static let capsLock: UInt32 = 0xffe5
    static let optionLeft: UInt32 = 0xffe9
    static let optionRight: UInt32 = 0xffea
    static let commandLeft: UInt32 = 0xffeb
    static let commandRight: UInt32 = 0xffec

    static let lowerVolume: UInt32 = 0x1008_ff11
    static let raiseVolume: UInt32 = 0x1008_ff13
    static let powerOff: UInt32 = 0x1008_ff2a

    static func function(_ number: Int) -> UInt32 {
        0xffbe + UInt32(max(1, min(number, 35)) - 1)
    }

    static func character(_ scalar: Unicode.Scalar) -> UInt32 {
        switch scalar.value {
        case 0x0a, 0x0d: returnKey
        case 0x09: tab
        case 0x08, 0x7f: backSpace
        case 0x1b: escape
        case 0x20...0x7e, 0xa0...0xff: scalar.value
        default: 0x0100_0000 | scalar.value
        }
    }

    static func typesInSecureInput(_ keysym: UInt32) -> Bool {
        // ScreensharingAgent rejects the Unicode fallback under secure input (PostKeyEvent).
        // arm64e: macOS 26A434 at 0x100027728; 26B5101f at 0x1000277b4.
        keysym < 0x80 || (0xff00...0xffff).contains(keysym)
    }

    static func isModifier(_ keysym: UInt32) -> Bool {
        (shiftLeft...0xffee).contains(keysym)
    }

    #if os(iOS)
    static func special(for usage: UIKeyboardHIDUsage) -> UInt32? {
        switch usage {
        case .keyboardReturnOrEnter, .keyboardReturn: returnKey
        case .keyboardEscape: escape
        case .keyboardDeleteOrBackspace: backSpace
        case .keyboardTab: tab
        case .keyboardSpacebar: space
        case .keyboardDeleteForward: forwardDelete
        case .keyboardHome: home
        case .keyboardEnd: end
        case .keyboardPageUp: pageUp
        case .keyboardPageDown: pageDown
        case .keyboardLeftArrow: left
        case .keyboardRightArrow: right
        case .keyboardUpArrow: up
        case .keyboardDownArrow: down
        case .keyboardInsert: insert
        case .keyboardHelp: help
        case .keyboardMenu, .keyboardApplication: menu
        case .keypadNumLock: numLock
        case .keypadEnter: 0xff8d
        case .keypadAsterisk: 0xffaa
        case .keypadPlus: 0xffab
        case .keypadHyphen: 0xffad
        case .keypadPeriod: 0xffae
        case .keypadSlash: 0xffaf
        case .keypadEqualSign: 0xffbd
        case .keypad0: 0xffb0
        case .keypad1: 0xffb1
        case .keypad2: 0xffb2
        case .keypad3: 0xffb3
        case .keypad4: 0xffb4
        case .keypad5: 0xffb5
        case .keypad6: 0xffb6
        case .keypad7: 0xffb7
        case .keypad8: 0xffb8
        case .keypad9: 0xffb9
        case .keyboardLeftShift: shiftLeft
        case .keyboardRightShift: shiftRight
        case .keyboardLeftControl: controlLeft
        case .keyboardRightControl: controlRight
        case .keyboardLeftAlt: optionLeft
        case .keyboardRightAlt: optionRight
        case .keyboardLeftGUI: commandLeft
        case .keyboardRightGUI: commandRight
        default: functionKey(for: usage)
        }
    }

    private static func functionKey(for usage: UIKeyboardHIDUsage) -> UInt32? {
        let raw = usage.rawValue
        if (UIKeyboardHIDUsage.keyboardF1.rawValue...UIKeyboardHIDUsage.keyboardF12.rawValue).contains(raw) {
            return function(raw - UIKeyboardHIDUsage.keyboardF1.rawValue + 1)
        }
        if (UIKeyboardHIDUsage.keyboardF13.rawValue...UIKeyboardHIDUsage.keyboardF24.rawValue).contains(raw) {
            return function(raw - UIKeyboardHIDUsage.keyboardF13.rawValue + 13)
        }
        return nil
    }
    #else
    static func special(forKeyCode keyCode: UInt16) -> UInt32? {
        specialKeyCodes[keyCode]
    }

    private static let specialKeyCodes: [UInt16: UInt32] = {
        var table: [UInt16: UInt32] = [
            36: returnKey, 53: escape, 51: backSpace, 48: tab, 49: space, 117: forwardDelete, 115: home, 119: end,
            116: pageUp, 121: pageDown, 123: left, 124: right, 126: up, 125: down, 114: help, 110: menu, 71: numLock,
            76: 0xff8d, 67: 0xffaa, 69: 0xffab, 78: 0xffad, 65: 0xffae, 75: 0xffaf, 81: 0xffbd,
            82: 0xffb0, 83: 0xffb1, 84: 0xffb2, 85: 0xffb3, 86: 0xffb4, 87: 0xffb5, 88: 0xffb6, 89: 0xffb7, 91: 0xffb8, 92: 0xffb9,
            56: shiftLeft, 60: shiftRight, 59: controlLeft, 62: controlRight, 58: optionLeft, 61: optionRight,
            55: commandLeft, 54: commandRight,
        ]
        for number in 1...20 {
            if let keyCode = MacKey.function(number).keyCode {
                table[keyCode] = function(number)
            }
        }
        return table
    }()
    #endif
}

nonisolated enum ModifierKey: CaseIterable, Hashable, Sendable {
    case control
    case option
    case shift
    case command

    init?(keysym: UInt32) {
        switch keysym {
        case Keysym.controlLeft, Keysym.controlRight: self = .control
        case Keysym.optionLeft, Keysym.optionRight: self = .option
        case Keysym.shiftLeft, Keysym.shiftRight: self = .shift
        case Keysym.commandLeft, Keysym.commandRight: self = .command
        default: return nil
        }
    }

    var keysym: UInt32 {
        switch self {
        case .control: Keysym.controlLeft
        case .option: Keysym.optionLeft
        case .shift: Keysym.shiftLeft
        case .command: Keysym.commandLeft
        }
    }

    var symbol: String {
        switch self {
        case .control: "⌃"
        case .option: "⌥"
        case .shift: "⇧"
        case .command: "⌘"
        }
    }

    var systemImage: String {
        switch self {
        case .control: "control"
        case .option: "option"
        case .shift: "shift"
        case .command: "command"
        }
    }

    var title: String.LocalizationValue {
        switch self {
        case .control: "Control Key"
        case .option: "Option Key"
        case .shift: "Shift Key"
        case .command: "Command Key"
        }
    }
}

nonisolated struct KeyChord: Hashable, Sendable {
    var modifiers: [ModifierKey]
    var key: MacKey

    init(modifiers: [ModifierKey], key: MacKey) {
        self.modifiers = modifiers
        self.key = key
    }

    init(modifiers: [ModifierKey], keysym: UInt32) {
        self.init(modifiers: modifiers, key: MacKey(keysym: keysym))
    }
}

extension KeyChord {
    static let spotlight = KeyChord(modifiers: [.command], key: .space)
    static let commandTab = KeyChord(modifiers: [.command], key: .tab)
    static let previousInputSource = KeyChord(modifiers: [.control], key: .space)

    static let sideButton = KeyChord(modifiers: [], key: MacKey(keyCode: nil, keysym: Keysym.powerOff))
    static let volumeUp = KeyChord(modifiers: [], key: MacKey(keyCode: nil, keysym: Keysym.raiseVolume))
    static let volumeDown = KeyChord(modifiers: [], key: MacKey(keyCode: nil, keysym: Keysym.lowerVolume))
}

extension SystemCommand {
    var fallbackChord: KeyChord? {
        switch self {
        case .missionControl: KeyChord(modifiers: [.control], keysym: Keysym.up)
        case .appWindows: KeyChord(modifiers: [.control], keysym: Keysym.down)
        case .showDesktop: KeyChord(modifiers: [], keysym: Keysym.function(11))
        case .launchpad: nil
        }
    }
}
