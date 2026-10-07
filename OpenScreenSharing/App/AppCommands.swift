import SwiftUI

private let sessionModifiers: EventModifiers = [.control, .option, .command]

nonisolated private struct SessionControllerKey: FocusedValueKey {
    typealias Value = SessionController
}

nonisolated private struct HomeActionsKey: FocusedValueKey {
    typealias Value = HomeActions
}

nonisolated extension FocusedValues {
    var sessionController: SessionController? {
        get { self[SessionControllerKey.self] }
        set { self[SessionControllerKey.self] = newValue }
    }

    var homeActions: HomeActions? {
        get { self[HomeActionsKey.self] }
        set { self[HomeActionsKey.self] = newValue }
    }
}

private struct ObservingSession<Content: View>: View {
    let session: SessionController?
    @ViewBuilder let content: () -> Content

    var body: some View {
        if let session {
            Observed(session: session, content: content)
        } else {
            content()
        }
    }

    private struct Observed: View {
        @ObservedObject var session: SessionController
        let content: () -> Content

        var body: some View {
            content()
        }
    }
}

struct AppCommands: Commands {
    @FocusedValue(\.sessionController) private var session
    @FocusedValue(\.homeActions) private var home

    private var homeEnabled: Bool { home != nil && session == nil }
    private var connected: Bool { session?.phase == .connected }
    private var controlling: Bool { session?.isControlling ?? false }
    private var touchScreen: Bool { session?.isTouchScreen ?? false }

    private func deviceButton(_ command: SessionCommand, key: KeyEquivalent, action: @escaping (SessionController) -> Void) -> some View {
        Button(String(localized: command.title)) {
            if let session { action(session) }
        }
        .keyboardShortcut(key, modifiers: sessionModifiers)
        .disabled(session.map { !command.isOffered(by: $0) || !command.isEnabled(in: $0) } ?? true)
    }

    var body: some Commands {
        CommandGroup(replacing: .newItem) {
            Button("New Connection…") { home?.newConnection() }
                .keyboardShortcut("n")
                .disabled(!homeEnabled)
        }

        CommandGroup(after: .newItem) {
            ObservingSession(session: session) {
                Section {
                    Button("Disconnect") { session?.close() }
                        .keyboardShortcut("w", modifiers: sessionModifiers)
                        .disabled(session == nil)
                }
            }
        }

        CommandGroup(after: .pasteboard) {
            ObservingSession(session: session) {
                Section {
                    Toggle(isOn: Binding(get: { session?.clipboardSharing ?? false }, set: { session?.setClipboardSharing($0) })) { Label("Sync Clipboard", systemImage: "arrow.up.arrow.down") }
                        .disabled(session == nil)
                    Button { session?.getClipboardFromMac() } label: { Label("Receive Clipboard", systemImage: "doc.on.clipboard") }
                        .keyboardShortcut("c", modifiers: sessionModifiers)
                        .disabled(!connected || session?.clipboardSharing == true)
                    Button { session?.sendClipboardToMac() } label: { Label("Send Clipboard", systemImage: "arrow.up.doc.on.clipboard") }
                        .keyboardShortcut("v", modifiers: sessionModifiers)
                        .disabled(!connected || session?.clipboardSharing == true)
                }
            }
        }

        CommandGroup(before: .toolbar) {
            ObservingSession(session: session) {
                Section {
                    Toggle("Zoom Mode", isOn: Binding(get: { session?.isZoomModeOn ?? false }, set: { session?.setZoomMode($0) }))
                        .disabled(session == nil)
                    if session?.pictureMatchesWindow != true {
                        Button(session?.scaleMode == .fit ? "Zoom to Actual Size" : "Zoom to Fit") {
                            session?.setScale(session?.scaleMode == .fit ? .actualSize : .fit)
                        }
                        .disabled(session == nil)
                    }
                }
                if let session, session.showsDisplayPicker {
                    Section {
                        ForEach(Array(session.displays.enumerated()), id: \.element.id) { index, display in
                            let toggle = Toggle(
                                display.name,
                                isOn: Binding(
                                    get: { session.displaySelection == .display(display.id) },
                                    set: { _ in session.selectDisplay(.display(display.id)) }
                                )
                            )
                            if index < 8 {
                                toggle.keyboardShortcut(KeyEquivalent(Character("\(index + 1)")), modifiers: sessionModifiers)
                            } else {
                                toggle
                            }
                        }
                        Toggle("All Displays", isOn: Binding(get: { session.displaySelection == .all }, set: { _ in session.selectDisplay(.all) }))
                    }
                    .disabled(session.phase != .connected)
                }
                if session?.usesHighPerformance != true {
                    Section {
                        ForEach(SessionQuality.allCases) { quality in
                            Toggle(isOn: Binding(get: { session?.quality == quality }, set: { _ in session?.setQuality(quality) })) {
                                Text(String(localized: quality.title))
                            }
                        }
                        .disabled(session == nil)
                    }
                }
                Section {
                    Menu("Resolution") {
                        if let session {
                            ResolutionOptions(controller: session)
                        }
                    }
                    .disabled(!(session?.offersResolution ?? false) || !controlling)
                    Toggle("High Dynamic Range", isOn: Binding(get: { session?.hdrEnabled ?? false }, set: { session?.setHDR($0) }))
                        .disabled(!(session?.canUseHDR ?? false))
                }
                Section {
                    Button(session?.isToolbarVisible == true ? "Hide Toolbar" : "Show Toolbar") { session?.toggleToolbar() }
                        .keyboardShortcut("t", modifiers: sessionModifiers)
                        .disabled(session == nil || session?.isToolbarDocked == true)
                    Button("Customize Toolbar…") { session?.isCustomizingToolbar = true }
                        .disabled(session == nil)
                }
            }
        }

        CommandMenu("Sound") {
            ObservingSession(session: session) {
                ForEach(session?.audioOptions ?? RemoteAudio.allCases) { audio in
                    Toggle(isOn: Binding(get: { session?.remoteAudio == audio }, set: { _ in session?.setRemoteAudio(audio) })) {
                        Text(String(localized: audio.title))
                    }
                }
                .disabled(!(session?.usesHighPerformance ?? false))
            }
        }

        CommandMenu("Control") {
            ObservingSession(session: session) {
                Section {
                    Toggle("Observe Mode", isOn: Binding(get: { session?.mode == .observe }, set: { session?.setMode($0 ? .observe : .control) }))
                        .keyboardShortcut("o", modifiers: sessionModifiers)
                        .disabled(!(session?.capabilities.contains(.control) ?? false))
                    Toggle("Pointer Lock", isOn: Binding(get: { session?.isPointerLockOn ?? false }, set: { session?.setPointerLock($0) }))
                        .keyboardShortcut("p", modifiers: sessionModifiers)
                        .disabled(session.map { !SessionCommand.pointerLock.isOffered(by: $0) || !$0.canLockPointer } ?? true)
                    Button(session?.isKeyboardVisible == true ? "Hide Keyboard" : "Show Keyboard") { session?.toggleKeyboard() }
                        .keyboardShortcut("k", modifiers: sessionModifiers)
                        .disabled(!controlling)
                }
                if touchScreen {
                    Section {
                        deviceButton(.home, key: "h") { $0.perform(.showDesktop) }
                        deviceButton(.appSwitcher, key: "a") { $0.perform(.missionControl) }
                        deviceButton(.lock, key: "l") { $0.sendChord(.sideButton) }
                    }
                    Section {
                        deviceButton(.volumeUp, key: .upArrow) { $0.sendChord(.volumeUp) }
                        deviceButton(.volumeDown, key: .downArrow) { $0.sendChord(.volumeDown) }
                    }
                } else {
                    Section {
                        ForEach(SystemCommand.allCases) { command in
                            Button(String(localized: command.title)) { session?.perform(command) }
                                .disabled(!controlling || !(session?.canPerform(command) ?? false))
                        }
                        Button("Spotlight") { session?.sendChord(.spotlight) }
                            .disabled(!controlling)
                        Button("Command-Tab") { session?.sendChord(.commandTab) }
                            .disabled(!controlling)
                    }
                }
            }
        }

        CommandMenu("Debug") {
            DebugInfoToggle()
        }
    }
}

private struct DebugInfoToggle: View {
    @ObservedObject private var settings = AppSettings.shared

    var body: some View {
        Toggle("Debug Panel", isOn: $settings.showsDebugInfo)
    }
}
