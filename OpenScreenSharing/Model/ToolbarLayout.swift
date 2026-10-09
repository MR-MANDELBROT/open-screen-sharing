import Combine
import Foundation
import SwiftUI

final class ToolbarLayout: ObservableObject {
    #if os(iOS)
    static let mac = ToolbarLayout(touchScreen: false, key: "SessionToolbar.v1", defaultBar: [.keyboard, .clipboard, .missionControl, .zoomMode, .scale, .displays])
    static let touchScreen = ToolbarLayout(
        touchScreen: true, key: "SessionToolbar.TouchScreen.v1", defaultBar: [.home, .appSwitcher, .keyboard, .clipboard, .zoomMode, .scale])
    #else
    static let mac = ToolbarLayout(touchScreen: false, key: "SessionToolbar.v1", defaultBar: [.scale, .displays, .observe, .missionControl, .clipboard])
    static let touchScreen = ToolbarLayout(
        touchScreen: true, key: "SessionToolbar.TouchScreen.v1", defaultBar: [.scale, .observe, .home, .appSwitcher, .clipboard])
    #endif

    static func layout(for controller: SessionController) -> ToolbarLayout {
        controller.isTouchScreen ? touchScreen : mac
    }

    static let maximumSlots = 8

    let defaultBar: [SessionCommand]
    private let forTouchScreen: Bool
    private let key: String

    @Published private(set) var bar: [SessionCommand]
    @Published private(set) var isCustomized: Bool
    private var known: Set<String>
    private let store: PreferenceStore

    private struct Stored: Codable {
        var bar: [String]
        var known: [String]
    }

    private init(touchScreen: Bool, key: String, defaultBar: [SessionCommand], store: PreferenceStore = .shared) {
        forTouchScreen = touchScreen
        self.key = key
        self.defaultBar = defaultBar
        self.store = store
        guard let stored = store.value(Stored.self, for: key) else {
            bar = defaultBar
            isCustomized = false
            known = []
            return
        }
        let known = Set(stored.known)
        bar = Self.merge(stored: stored.bar, known: known, defaultBar: defaultBar)
        isCustomized = true
        self.known = known
    }

    func otherCommands(in category: SessionCommandCategory) -> [SessionCommand] {
        category.commands.filter { !bar.contains($0) && $0.applies(toTouchScreen: forTouchScreen) }
    }

    var isDefault: Bool { bar == defaultBar }

    func move(fromOffsets source: IndexSet, toOffset destination: Int) {
        bar.move(fromOffsets: source, toOffset: destination)
        save()
    }

    func move(_ command: SessionCommand, by offset: Int) {
        guard let index = bar.firstIndex(of: command) else { return }
        let target = index + offset
        guard bar.indices.contains(target) else { return }
        bar.swapAt(index, target)
        save()
    }

    func remove(atOffsets offsets: IndexSet) {
        bar.remove(atOffsets: offsets)
        save()
    }

    func remove(_ command: SessionCommand) {
        bar.removeAll { $0 == command }
        save()
    }

    func add(_ command: SessionCommand) {
        guard !bar.contains(command) else { return }
        bar.append(command)
        save()
    }

    func reset() {
        bar = defaultBar
        isCustomized = false
        known = []
        store.remove(key)
    }

    func plan(for controller: SessionController, slots: Int?) -> ToolbarPlan {
        let offered = bar.filter { $0.isOffered(by: controller) }
        let count = min(max(slots ?? offered.count, 0), offered.count)
        return ToolbarPlan(inBar: Array(offered.prefix(count)), overflow: Array(offered.dropFirst(count)))
    }

    private func save() {
        known.formUnion(defaultBar.map(\.rawValue))
        isCustomized = true
        store.set(Stored(bar: bar.map(\.rawValue), known: known.sorted()), for: key)
    }

    static func merge(stored ids: [String], known: Set<String>, defaultBar: [SessionCommand]) -> [SessionCommand] {
        var bar: [SessionCommand] = []
        for id in ids {
            if let command = SessionCommand(rawValue: id), !bar.contains(command) {
                bar.append(command)
            }
        }
        for (index, command) in defaultBar.enumerated() where !known.contains(command.rawValue) && !bar.contains(command) {
            let neighbour = defaultBar[..<index].reversed().first { bar.contains($0) }
            let position = neighbour.flatMap { bar.firstIndex(of: $0) }.map { $0 + 1 } ?? 0
            bar.insert(command, at: position)
        }
        return bar
    }


    static func capsuleSlots(width: CGFloat, itemWidth: CGFloat) -> Int {
        let fixed = 2 * itemWidth + 2 * capsuleGroupSpacing + 2 * capsuleGroupPadding + 2 * capsuleMargin
        return slots(for: width - fixed, itemWidth: itemWidth)
    }

    private static func slots(for width: CGFloat, itemWidth: CGFloat) -> Int {
        guard width > 0, itemWidth > 0 else { return 0 }
        return min(maximumSlots, Int((width / itemWidth).rounded(.down)))
    }

    static let capsuleGroupSpacing: CGFloat = 8
    static let capsuleGroupPadding: CGFloat = 4
    static let capsuleMargin: CGFloat = 16
    static let dockClearance: CGFloat = 4
    static let dockMargin: CGFloat = 8
}

struct ToolbarPlan: Equatable {
    var inBar: [SessionCommand] = []
    var overflow: [SessionCommand] = []
}
