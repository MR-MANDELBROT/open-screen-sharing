#if os(iOS)
import Combine
import GameController
#endif
import SwiftUI

struct KeyboardShortcutsView: View {
    @ObservedObject private var shortcuts = KeyShortcuts.shared
    #if os(iOS)
    @ObservedObject private var keyboard = ConnectedKeyboard.shared
    #endif
    @State private var recording: Recording?
    @State private var alert: AlertState?
    @State private var editing: KeyMapping?

    var body: some View {
        Group {
            #if os(iOS)
            if keyboard.isConnected {
                form
            } else {
                ContentUnavailableViewCompat {
                    Label("No Keyboard", systemImage: "keyboard")
                } description: {
                    Text("Connect a keyboard to set up shortcuts.")
                }
            }
            #else
            form
            #endif
        }
        #if os(iOS)
        .navigationBarTitleDisplayMode(.inline)
        #endif
        .navigationTitle("Keyboard Shortcuts")
        .toolbar {
            ToolbarItem(placement: .primaryAction) {
                Menu {
                    Button(role: .destructive) {
                        recording = nil
                        shortcuts.restoreDefaults()
                    } label: {
                        Label("Restore Defaults", systemImage: "eraser.fill")
                    }
                    .disabled(!shortcuts.isCustomized)
                } label: {
                    Label("Others", systemImage: "ellipsis.circle")
                }
            }
        }
        .alert($alert)
        .sheet(item: $editing) { mapping in
            KeyMappingEditor(mapping: mapping, isNew: !shortcuts.mappings.contains { $0.id == mapping.id })
        }
        #if os(macOS)
        .recordsShortcuts($recording, modifierChord: .pointerRelease, onPress: record)
        #else
        .recordsShortcuts($recording, onPress: record)
        #endif
    }

    private var form: some View {
        Form {
            Section("Key Mappings") {
                ForEach(shortcuts.mappings) { mapping in
                    HStack {
                        Button {
                            editing = mapping
                        } label: {
                            HStack {
                                Text(verbatim: mapping.trigger.displayName)
                                Image(systemName: "arrow.right")
                                    .foregroundStyle(.secondary)
                                Text(verbatim: mapping.displayName)
                                    .lineLimit(1)
                                Spacer()
                            }
                            .contentShape(Rectangle())
                        }
                        .buttonStyle(.plain)
                        Button {
                            shortcuts.removeMapping(mapping.id)
                        } label: {
                            Image(systemName: "xmark.circle.fill")
                        }
                        .buttonStyle(.borderless)
                        .foregroundStyle(.secondary)
                        .accessibilityLabel(Text("Delete Mapping"))
                    }
                }
                .onDelete { offsets in
                    for mapping in offsets.map({ shortcuts.mappings[$0] }) {
                        shortcuts.removeMapping(mapping.id)
                    }
                }
                Button {
                    editing = KeyMapping(id: UUID(), trigger: .unset, steps: [])
                } label: {
                    Label("Add Mapping", systemImage: "plus")
                }
                .formRowButtonStyleCompat()
            }
            ForEach(ShortcutGroup.allCases) { group in
                Section(group.title) {
                    ForEach(group.actions) { action in
                        row(.action(action), title: action.title, shortcut: shortcuts.shortcut(for: action)?.displayName) {
                            shortcuts.set(nil, for: action)
                        }
                        #if os(macOS)
                        if action == .pointerLock {
                            row(.pointerRelease, title: String(localized: "Release Pointer"), shortcut: EventModifiers(shortcuts.pointerRelease).symbols, clear: nil)
                        }
                        #endif
                    }
                }
            }
        }
        #if os(macOS)
        .formStyle(.grouped)
        .unboundedFormWidth()
        #endif
    }

    private func row(_ target: Recording, title: String, shortcut: String?, clear: (() -> Void)?) -> some View {
        HStack {
            Text(verbatim: title)
            Spacer()
            if let clear, shortcut != nil, recording != target {
                Button(action: clear) {
                    Image(systemName: "xmark.circle.fill")
                }
                .buttonStyle(.borderless)
                .foregroundStyle(.secondary)
                .accessibilityLabel(Text("Clear Shortcut"))
            }
            ShortcutButton(isRecording: recording == target, title: title, shortcut: shortcut) {
                recording = recording == target ? nil : target
            }
        }
    }

    private enum Recording: Hashable {
        case action(ShortcutAction)
        #if os(macOS)
        case pointerRelease
        #endif
    }

    private func record(_ target: Recording, _ press: ShortcutPress) -> Bool {
        switch press {
        case .cancel:
            recording = nil
        case .clear:
            guard case .action(let action) = target else { return false }
            recording = nil
            shortcuts.set(nil, for: action)
        case .shortcut(let shortcut):
            guard case .action(let action) = target, shortcut.hasCommandModifier else { return false }
            recording = nil
            assign(shortcut, to: action)
        #if os(macOS)
        case .modifierChord(let flags):
            recording = nil
            shortcuts.setPointerRelease(flags)
        #endif
        }
        return true
    }

    private func assign(_ shortcut: KeyShortcut, to action: ShortcutAction) {
        if let other = shortcuts.action(using: shortcut), other != action {
            alert = AlertState(
                title: String(localized: "“\(shortcut.displayName)” Is Already in Use"),
                message: String(localized: "It’s assigned to “\(other.title)”. Replacing it removes that shortcut."),
                actions: [
                    AlertState.Action(label: String(localized: "Cancel"), role: .cancel) {},
                    AlertState.Action(label: String(localized: "Replace"), role: nil, isDefault: true) {
                        shortcuts.set(nil, for: other)
                        shortcuts.set(shortcut, for: action)
                    },
                ]
            )
        } else if let other = shortcuts.mapping(triggeredBy: shortcut) {
            alert = AlertState(
                title: String(localized: "“\(shortcut.displayName)” Is Already in Use"),
                message: String(localized: "It’s mapped to \(other.displayName). Replacing it removes that mapping."),
                actions: [
                    AlertState.Action(label: String(localized: "Cancel"), role: .cancel) {},
                    AlertState.Action(label: String(localized: "Replace"), role: nil, isDefault: true) {
                        shortcuts.removeMapping(other.id)
                        shortcuts.set(shortcut, for: action)
                    },
                ]
            )
        } else {
            shortcuts.set(shortcut, for: action)
        }
    }
}

struct ShortcutButton: View {
    let isRecording: Bool
    let title: String
    let shortcut: String?
    var prompt: LocalizedStringKey = "Type Shortcut"
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            if isRecording {
                Text(prompt)
            } else if let shortcut {
                Text(verbatim: shortcut)
            } else {
                Text("None")
                    .foregroundStyle(.secondary)
            }
        }
        .buttonStyle(.bordered)
        .tint(isRecording ? .accentColor : nil)
        .id(isRecording)
        .accessibilityLabel(Text(verbatim: title))
        .accessibilityValue(isRecording ? Text(prompt) : shortcut.map { Text(verbatim: $0) } ?? Text("None"))
    }
}

private struct KeyMappingEditor: View {
    let id: UUID
    let isNew: Bool

    @ObservedObject private var shortcuts = KeyShortcuts.shared
    @State private var trigger: KeyShortcut
    @State private var steps: [Step]
    @State private var recording: Field?
    @State private var alert: AlertState?
    @Environment(\.dismiss) private var dismiss

    init(mapping: KeyMapping, isNew: Bool) {
        id = mapping.id
        self.isNew = isNew
        _trigger = State(initialValue: mapping.trigger)
        _steps = State(initialValue: mapping.steps.map(Step.init))
    }

    private struct Step: Identifiable {
        let id = UUID()
        let sendsText: Bool
        var shortcut: KeyShortcut
        var text: String

        init(_ step: KeyMapping.Step) {
            switch step {
            case .key(let shortcut):
                sendsText = false
                self.shortcut = shortcut
                text = ""
            case .text(let text):
                sendsText = true
                shortcut = .unset
                self.text = text
            }
        }

        var value: KeyMapping.Step {
            sendsText ? .text(text) : .key(shortcut)
        }

        var isComplete: Bool {
            sendsText ? !text.isEmpty : !shortcut.isUnset && (shortcut.hasCommandModifier || shortcut.isSpecial)
        }
    }

    private enum Field: Hashable {
        case trigger
        case key(UUID)
    }

    private static let modifiers: [(flag: EventModifiers, symbol: String, title: String)] = [
        (.control, "⌃", String(localized: "Control Key", defaultValue: "Control")),
        (.option, "⌥", String(localized: "Option Key", defaultValue: "Option")),
        (.shift, "⇧", String(localized: "Shift Key", defaultValue: "Shift")),
        (.command, "⌘", String(localized: "Command Key", defaultValue: "Command")),
    ]

    private var mapping: KeyMapping {
        KeyMapping(id: id, trigger: trigger, steps: steps.map(\.value))
    }

    private var isComplete: Bool {
        !trigger.isUnset && !steps.isEmpty && steps.allSatisfy(\.isComplete)
    }

    var body: some View {
        NavigationStackCompat {
            Form {
                Section {
                    HStack {
                        Text("Shortcut")
                        Spacer()
                        ShortcutButton(isRecording: recording == .trigger, title: String(localized: "Shortcut"), shortcut: trigger.isUnset ? nil : trigger.displayName) {
                            recording = recording == .trigger ? nil : .trigger
                        }
                    }
                }
                Section("Sends") {
                    ForEach($steps) { $step in
                        HStack {
                            #if os(macOS)
                            Button {
                                recording = nil
                                steps.removeAll { $0.id == step.id }
                            } label: {
                                Image(systemName: "minus.circle.fill")
                                    .symbolRenderingMode(.palette)
                                    .foregroundStyle(.white, .red)
                                    .font(.title2)
                            }
                            .buttonStyle(.plain)
                            .accessibilityLabel(Text("Delete"))
                            #endif
                            if step.sendsText {
                                if #available(iOS 16, *) {
                                    TextField("Text", text: $step.text, prompt: Text("Text"), axis: .vertical)
                                        .labelsHidden()
                                } else {
                                    TextField("Text", text: $step.text)
                                }
                            } else {
                                ForEach(Self.modifiers, id: \.symbol) { modifier in
                                    Toggle(isOn: Binding(
                                        get: { step.shortcut.eventModifiers.contains(modifier.flag) },
                                        set: { on in
                                            var flags = step.shortcut.eventModifiers
                                            if on { flags.insert(modifier.flag) } else { flags.remove(modifier.flag) }
                                            step.shortcut = KeyShortcut(key: step.shortcut.key, modifiers: flags)
                                        }
                                    )) {
                                        Text(verbatim: modifier.symbol)
                                    }
                                    .toggleStyle(.button)
                                    .accessibilityLabel(Text(verbatim: modifier.title))
                                }
                                ShortcutButton(
                                    isRecording: recording == .key(step.id), title: String(localized: "Key"),
                                    shortcut: step.shortcut.isUnset ? nil : KeyShortcut(key: step.shortcut.key, modifiers: []).displayName, prompt: "Type Key"
                                ) {
                                    recording = recording == .key(step.id) ? nil : .key(step.id)
                                }
                                Spacer(minLength: 0)
                            }
                            #if os(macOS)
                            Image(systemName: "line.3.horizontal")
                                .foregroundStyle(.tertiary)
                                .accessibilityHidden(true)
                            #endif
                        }
                        #if os(macOS)
                        .contextMenu {
                            Button {
                                guard let index = steps.firstIndex(where: { $0.id == step.id }), index > 0 else { return }
                                steps.swapAt(index, index - 1)
                            } label: {
                                Label("Move Up", systemImage: "arrow.up")
                            }
                            .disabled(steps.first?.id == step.id)
                            Button {
                                guard let index = steps.firstIndex(where: { $0.id == step.id }), index < steps.count - 1 else { return }
                                steps.swapAt(index, index + 1)
                            } label: {
                                Label("Move Down", systemImage: "arrow.down")
                            }
                            .disabled(steps.last?.id == step.id)
                        }
                        #endif
                    }
                    .onMove { source, destination in steps.move(fromOffsets: source, toOffset: destination) }
                    .onDelete { offsets in
                        recording = nil
                        steps.remove(atOffsets: offsets)
                    }
                    Menu {
                        Button {
                            steps.append(Step(.key(.unset)))
                        } label: {
                            Label("Key", systemImage: "keyboard")
                        }
                        Button {
                            steps.append(Step(.text("")))
                        } label: {
                            Label("Text", systemImage: "character.cursor.ibeam")
                        }
                    } label: {
                        HStack {
                            Image(systemName: "plus.circle.fill")
                                .symbolRenderingMode(.palette)
                                .foregroundStyle(.white, .green)
                                .font(.title2)
                                .accessibilityHidden(true)
                            Text("Add")
                                .foregroundStyle(.primary)
                            Spacer(minLength: 0)
                        }
                        .contentShape(Rectangle())
                    }
                    #if os(macOS)
                    .menuStyle(.button)
                    .buttonStyle(.plain)
                    .menuIndicator(.hidden)
                    #endif
                }
            }
            #if os(iOS)
            .environment(\.editMode, .constant(.active))
            .navigationBarTitleDisplayMode(.inline)
            #else
            .formStyle(.grouped)
            #endif
            .navigationTitle(isNew ? String(localized: "Add Mapping") : String(localized: "Edit Mapping"))
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done", action: save)
                        .disabled(!isComplete)
                }
            }
        }
        .alert($alert)
        .recordsShortcuts($recording, onPress: record)
        #if os(macOS)
        .frame(minWidth: 440, minHeight: 400)
        #endif
    }

    private func record(_ field: Field, _ press: ShortcutPress) -> Bool {
        switch press {
        case .cancel:
            recording = nil
        case .clear:
            recording = nil
            switch field {
            case .trigger:
                trigger = .unset
            case .key(let stepID):
                guard let index = steps.firstIndex(where: { $0.id == stepID }) else { break }
                steps[index].shortcut = KeyShortcut(key: "", modifiers: steps[index].shortcut.eventModifiers)
            }
        case .shortcut(let shortcut):
            switch field {
            case .trigger:
                guard shortcut.hasCommandModifier else { return false }
                recording = nil
                trigger = shortcut
            case .key(let stepID):
                recording = nil
                guard let index = steps.firstIndex(where: { $0.id == stepID }) else { break }
                let modifiers = shortcut.eventModifiers.isEmpty ? steps[index].shortcut.eventModifiers : shortcut.eventModifiers
                steps[index].shortcut = KeyShortcut(key: shortcut.key, modifiers: modifiers)
            }
        #if os(macOS)
        case .modifierChord:
            return false
        #endif
        }
        return true
    }

    private func save() {
        let shortcut = trigger
        if let other = shortcuts.action(using: shortcut) {
            alert = AlertState(
                title: String(localized: "“\(shortcut.displayName)” Is Already in Use"),
                message: String(localized: "It’s assigned to “\(other.title)”. Replacing it removes that shortcut."),
                actions: [
                    AlertState.Action(label: String(localized: "Cancel"), role: .cancel) {},
                    AlertState.Action(label: String(localized: "Replace"), role: nil, isDefault: true) {
                        shortcuts.set(nil, for: other)
                        commit()
                    },
                ]
            )
        } else if let other = shortcuts.mapping(triggeredBy: shortcut), other.id != id {
            alert = AlertState(
                title: String(localized: "“\(shortcut.displayName)” Is Already in Use"),
                message: String(localized: "It’s mapped to \(other.displayName). Replacing it removes that mapping."),
                actions: [
                    AlertState.Action(label: String(localized: "Cancel"), role: .cancel) {},
                    AlertState.Action(label: String(localized: "Replace"), role: nil, isDefault: true) {
                        shortcuts.removeMapping(other.id)
                        commit()
                    },
                ]
            )
        } else {
            commit()
        }
    }

    private func commit() {
        if isNew {
            shortcuts.add(mapping)
        } else {
            shortcuts.update(mapping)
        }
        dismiss()
    }
}

#if os(iOS)
private final class ConnectedKeyboard: ObservableObject {
    static let shared = ConnectedKeyboard()

    @Published private(set) var isConnected = GCKeyboard.coalesced != nil

    private init() {
        NotificationCenter.default.addObserver(forName: .GCKeyboardDidConnect, object: nil, queue: .main) { _ in
            MainActor.assumeIsolated { ConnectedKeyboard.shared.isConnected = true }
        }
        NotificationCenter.default.addObserver(forName: .GCKeyboardDidDisconnect, object: nil, queue: .main) { _ in
            MainActor.assumeIsolated { ConnectedKeyboard.shared.isConnected = false }
        }
    }
}
#endif
