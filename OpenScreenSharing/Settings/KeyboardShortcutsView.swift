import SwiftUI

struct KeyboardShortcutsView: View {
    @ObservedObject private var shortcuts = KeyShortcuts.shared
    @State private var recording: Recording?
    @State private var alert: AlertState?
    @State private var editing: KeyMapping?

    var body: some View {
        Form {
            Section("Key Mappings") {
                ForEach(shortcuts.mappings) { mapping in
                    Button {
                        editing = mapping
                    } label: {
                        HStack {
                            Text(verbatim: mapping.trigger.displayName)
                            Image(systemName: "arrow.right")
                                .foregroundStyle(.secondary)
                            Text(verbatim: mapping.target.displayName)
                            Spacer()
                        }
                        .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                }
                .onDelete { offsets in
                    for mapping in offsets.map({ shortcuts.mappings[$0] }) {
                        shortcuts.removeMapping(mapping.id)
                    }
                }
                Button {
                    editing = KeyMapping(id: UUID(), trigger: .unset, target: .unset)
                } label: {
                    Label("Add Mapping", systemImage: "plus")
                }
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
            Section {
                Button("Restore Defaults") {
                    recording = nil
                    shortcuts.restoreDefaults()
                }
                .disabled(!shortcuts.isCustomized)
            }
        }
        #if os(iOS)
        .navigationBarTitleDisplayMode(.inline)
        #else
        .formStyle(.grouped)
        #endif
        .navigationTitle("Keyboard Shortcuts")
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
                message: String(localized: "It’s mapped to “\(other.target.displayName)”. Replacing it removes that mapping."),
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
    @State var mapping: KeyMapping
    let isNew: Bool

    @ObservedObject private var shortcuts = KeyShortcuts.shared
    @State private var recording: Field?
    @State private var alert: AlertState?
    @Environment(\.dismiss) private var dismiss

    private enum Field: Hashable {
        case trigger, key
    }

    private static let modifiers: [(flag: EventModifiers, symbol: String, title: String)] = [
        (.control, "⌃", String(localized: "Control Key", defaultValue: "Control")),
        (.option, "⌥", String(localized: "Option Key", defaultValue: "Option")),
        (.shift, "⇧", String(localized: "Shift Key", defaultValue: "Shift")),
        (.command, "⌘", String(localized: "Command Key", defaultValue: "Command")),
    ]

    private var isComplete: Bool { !mapping.trigger.isUnset && !mapping.target.isUnset }

    var body: some View {
        NavigationStackCompat {
            Form {
                Section {
                    HStack {
                        Text("Shortcut")
                        Spacer()
                        ShortcutButton(isRecording: recording == .trigger, title: String(localized: "Shortcut"), shortcut: mapping.trigger.isUnset ? nil : mapping.trigger.displayName) {
                            recording = recording == .trigger ? nil : .trigger
                        }
                    }
                }
                Section("Sends") {
                    HStack {
                        Text("Modifiers")
                        Spacer()
                        ForEach(Self.modifiers, id: \.symbol) { modifier in
                            Toggle(isOn: Binding(
                                get: { mapping.target.eventModifiers.contains(modifier.flag) },
                                set: { on in
                                    var flags = mapping.target.eventModifiers
                                    if on { flags.insert(modifier.flag) } else { flags.remove(modifier.flag) }
                                    mapping.target = KeyShortcut(key: mapping.target.key, modifiers: flags)
                                }
                            )) {
                                Text(verbatim: modifier.symbol)
                            }
                            .toggleStyle(.button)
                            .accessibilityLabel(Text(verbatim: modifier.title))
                        }
                    }
                    HStack {
                        Text("Key")
                        Spacer()
                        ShortcutButton(
                            isRecording: recording == .key, title: String(localized: "Key"),
                            shortcut: mapping.target.isUnset ? nil : KeyShortcut(key: mapping.target.key, modifiers: []).displayName, prompt: "Type Key"
                        ) {
                            recording = recording == .key ? nil : .key
                        }
                    }
                }
                if !isNew {
                    Section {
                        Button("Delete Mapping", role: .destructive) {
                            shortcuts.removeMapping(mapping.id)
                            dismiss()
                        }
                    }
                }
            }
            #if os(iOS)
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
        .frame(minWidth: 440, minHeight: 320)
        #endif
    }

    private func record(_ field: Field, _ press: ShortcutPress) -> Bool {
        switch press {
        case .cancel:
            recording = nil
        case .clear:
            recording = nil
            switch field {
            case .trigger: mapping.trigger = .unset
            case .key: mapping.target = KeyShortcut(key: "", modifiers: mapping.target.eventModifiers)
            }
        case .shortcut(let shortcut):
            switch field {
            case .trigger:
                guard shortcut.hasCommandModifier else { return false }
                recording = nil
                mapping.trigger = shortcut
            case .key:
                recording = nil
                let modifiers = shortcut.eventModifiers.isEmpty ? mapping.target.eventModifiers : shortcut.eventModifiers
                mapping.target = KeyShortcut(key: shortcut.key, modifiers: modifiers)
            }
        #if os(macOS)
        case .modifierChord:
            return false
        #endif
        }
        return true
    }

    private func save() {
        let shortcut = mapping.trigger
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
        } else if let other = shortcuts.mapping(triggeredBy: shortcut), other.id != mapping.id {
            alert = AlertState(
                title: String(localized: "“\(shortcut.displayName)” Is Already in Use"),
                message: String(localized: "It’s mapped to “\(other.target.displayName)”. Replacing it removes that mapping."),
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
