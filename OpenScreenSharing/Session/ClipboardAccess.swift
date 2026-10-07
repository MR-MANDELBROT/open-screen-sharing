import SwiftUI
import UIKit
import UniformTypeIdentifiers

final class ClipboardAccess {
    enum Setting: String, Codable {
        case allow
        case ask
    }

    static let shared = ClipboardAccess()

    private(set) var setting: Setting?

    private var reading = false
    private var systemAsked = false
    private let store: PreferenceStore

    private enum Key {
        static let setting = "pasteFromOtherApps"
        static let ownChangeCount = "ownClipboardChangeCount"
        static let explained = "explainedPasteFromOtherApps"
    }

    init(store: PreferenceStore = .shared) {
        self.store = store
        setting = store.value(Setting.self, for: Key.setting)
    }

    var allowsReading: Bool { setting == .allow }

    private var explained: Bool { store.value(Bool.self, for: Key.explained) == true }

    func sceneDidBecomeInactive() {
        if reading {
            systemAsked = true
        }
    }

    func read() async -> (content: ClipboardContent, explanation: AlertState?) {
        let (content, found) = await readAndRecord()
        guard found == .ask, !explained else { return (content, nil) }
        store.set(true, for: Key.explained)
        return (content, explanation)
    }

    func check() async -> AlertState? {
        let pasteboard = UIPasteboard.general
        guard pasteboard.numberOfItems > 0, pasteboard.changeCount != store.value(Int.self, for: Key.ownChangeCount),
            setting != .ask || !explained
        else { return nil }
        return await read().explanation
    }

    func noteOwnWrite() {
        store.set(UIPasteboard.general.changeCount, for: Key.ownChangeCount)
    }

    private func readAndRecord() async -> (content: ClipboardContent, found: Setting?) {
        let own = UIPasteboard.general.changeCount == store.value(Int.self, for: Key.ownChangeCount)
        reading = true
        systemAsked = false
        let (content, readable) = await Task.detached { Self.readClipboard() }.value
        reading = false
        var found: Setting?
        if systemAsked {
            found = .ask
        } else if readable, !own {
            found = .allow
        }
        if let found {
            record(found)
        }
        return (content, found)
    }

    private func record(_ setting: Setting) {
        guard setting != self.setting else { return }
        self.setting = setting
        store.set(setting, for: Key.setting)
    }

    private var explanation: AlertState {
        AlertState(
            title: String(localized: "Clipboard Sync Asks Before Each Paste"),
            message: String(localized: "New connections start with clipboard sync turned off. To sync what you copy to the Mac without being asked each time, choose Allow for Paste from Other Apps in Settings."),
            actions: [
                AlertState.Action(label: String(localized: "Open Settings"), role: nil) {
                    if let url = URL(string: UIApplication.openSettingsURLString) {
                        UIApplication.shared.open(url)
                    }
                },
                AlertState.Action(label: String(localized: "OK"), role: .cancel) {},
            ]
        )
    }

    nonisolated static func readClipboard() -> (content: ClipboardContent, readable: Bool) {
        let items = UIPasteboard.general.items
        let content: ClipboardContent = items.compactMap { item in
            var flavors: [String: Data] = [:]
            for (type, value) in item {
                switch value {
                case let data as Data: flavors[type] = data
                case let text as String: flavors[type] = Data(text.utf8)
                case let url as URL: flavors[type] = Data(url.absoluteString.utf8)
                case let image as UIImage: flavors[type] = image.pngData()
                default: break
                }
            }
            if flavors[UTType.utf8PlainText.identifier] == nil, let text = item[UTType.plainText.identifier] as? String ?? item[UTType.text.identifier] as? String {
                flavors[UTType.utf8PlainText.identifier] = Data(text.utf8)
            }
            return flavors.isEmpty ? nil : flavors
        }
        return (content, items.contains { !$0.isEmpty })
    }
}
