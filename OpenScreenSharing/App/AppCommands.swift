import SwiftUI

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
    #if os(macOS)
    @Environment(\.openWindow) private var openWindow
    #endif

    private var homeEnabled: Bool { home != nil && session == nil }
    private var connected: Bool { session?.phase == .connected }
    private var controlling: Bool { session?.isControlling ?? false }
    private var touchScreen: Bool { session?.isTouchScreen ?? false }

    private func deviceButton(_ command: SessionCommand, shortcut: ShortcutAction, action: @escaping (SessionController) -> Void) -> some View {
        Button(String(localized: command.title)) {
            if let session { action(session) }
        }
        .keyboardShortcut(for: shortcut)
        .disabled(session.map { !command.isOffered(by: $0) || !command.isEnabled(in: $0) } ?? true)
    }

    private func keyMenu(_ command: SessionCommand, @ViewBuilder items: @escaping (SessionController) -> some View) -> some View {
        Menu(String(localized: command.title)) {
            if let session {
                items(session)
            }
        }
        .disabled(session.map { !command.isOffered(by: $0) || !command.isEnabled(in: $0) } ?? true)
    }

    #if os(macOS)
    private func openHome(_ request: HomeRequest.Pending) {
        HomeRequest.shared.pending = request
        openWindow(id: "home")
    }
    #endif

    var body: some Commands {
        CommandGroup(replacing: .newItem) {
            #if os(iOS)
            Button("New Connection…") { home?.newConnection() }
                .keyboardShortcut(for: .newConnection)
                .disabled(!homeEnabled)
            #else
            Button("New Connection…") { openHome(.newConnection) }
                .keyboardShortcut(for: .newConnection)
            #endif
        }

        #if os(macOS)
        CommandGroup(replacing: .appSettings) {
            Button("Settings…") { openHome(.settings) }
                .keyboardShortcut(",")
        }
        #endif

        CommandGroup(after: .newItem) {
            ObservingSession(session: session) {
                Section {
                    Button("Disconnect") { session?.close() }
                        .keyboardShortcut(for: .disconnect)
                        .disabled(session == nil)
                }
            }
        }

        CommandGroup(after: .pasteboard) {
            ObservingSession(session: session) {
                Section {
                    Toggle(isOn: Binding(get: { session?.clipboardSharing ?? false }, set: { session?.setClipboardSharing($0) })) { Label("Sync Clipboard", systemImage: "arrow.up.arrow.down") }
                        .keyboardShortcut(for: .syncClipboard)
                        .disabled(session == nil)
                    Button { session?.getClipboardFromMac() } label: { Label("Receive Clipboard", systemImage: "doc.on.clipboard") }
                        .keyboardShortcut(for: .receiveClipboard)
                        .disabled(!connected || session?.clipboardSharing == true)
                    Button { session?.sendClipboardToMac() } label: { Label("Send Clipboard", systemImage: "arrow.up.doc.on.clipboard") }
                        .keyboardShortcut(for: .sendClipboard)
                        .disabled(!connected || session?.clipboardSharing == true)
                }
            }
        }

        CommandGroup(before: .toolbar) {
            #if os(macOS)
            if homeEnabled {
                ComputerLayoutToggles()
            }
            #endif
            ObservingSession(session: session) {
                Section {
                    Toggle("Zoom Mode", isOn: Binding(get: { session?.isZoomModeOn ?? false }, set: { session?.setZoomMode($0) }))
                        .keyboardShortcut(for: .zoomMode)
                        .disabled(session == nil)
                    if session?.pictureMatchesWindow != true {
                        Button(session?.scaleMode == .fit ? "Zoom to Actual Size" : "Zoom to Fit") {
                            session?.setScale(session?.scaleMode == .fit ? .actualSize : .fit)
                        }
                        .keyboardShortcut(for: .actualSize)
                        .disabled(session == nil)
                    }
                }
                if let session, session.showsDisplayPicker {
                    Section {
                        ForEach(Array(session.displays.enumerated()), id: \.element.id) { index, display in
                            Toggle(
                                display.name,
                                isOn: Binding(
                                    get: { session.displaySelection == .display(display.id) },
                                    set: { _ in session.selectDisplay(.display(display.id)) }
                                )
                            )
                            .keyboardShortcut(for: ShortcutAction.display(at: index))
                        }
                        Toggle("All Displays", isOn: Binding(get: { session.displaySelection == .all }, set: { _ in session.selectDisplay(.all) }))
                            .keyboardShortcut(for: .allDisplays)
                    }
                    .disabled(session.phase != .connected)
                }
                if session?.usesHighPerformance != true {
                    Section {
                        ForEach(SessionQuality.allCases) { quality in
                            Toggle(isOn: Binding(get: { session?.quality == quality }, set: { _ in session?.setQuality(quality) })) {
                                Text(String(localized: quality.title))
                            }
                            .keyboardShortcut(for: ShortcutAction.quality(quality))
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
                        .keyboardShortcut(for: .hdr)
                        .disabled(!(session?.canUseHDR ?? false))
                }
                Section {
                    #if os(iOS)
                    Button(session?.isToolbarVisible == true ? "Hide Toolbar" : "Show Toolbar") { session?.toggleToolbar() }
                        .keyboardShortcut(for: .toolbar)
                        .disabled(session == nil || session?.isToolbarDocked == true)
                    #endif
                    Button("Customize Toolbar…") { session?.isCustomizingToolbar = true }
                        .keyboardShortcut(for: .customizeToolbar)
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
                    .keyboardShortcut(for: ShortcutAction.audio(audio))
                }
                .disabled(!(session?.usesHighPerformance ?? false))
            }
        }

        CommandMenu("Control") {
            ObservingSession(session: session) {
                Section {
                    Toggle("Observe Mode", isOn: Binding(get: { session?.mode == .observe }, set: { session?.setMode($0 ? .observe : .control) }))
                        .keyboardShortcut(for: .observe)
                        .disabled(!(session?.capabilities.contains(.control) ?? false))
                    Toggle("Pointer Lock", isOn: Binding(get: { session?.isPointerLockOn ?? false }, set: { session?.setPointerLock($0) }))
                        .keyboardShortcut(for: .pointerLock)
                        .disabled(session.map { !SessionCommand.pointerLock.isOffered(by: $0) || !$0.canLockPointer } ?? true)
                    #if os(iOS)
                    Button(session?.isKeyboardVisible == true ? "Hide Keyboard" : "Show Keyboard") { session?.toggleKeyboard() }
                        .keyboardShortcut(for: .keyboard)
                        .disabled(!controlling)
                    #endif
                }
                if touchScreen {
                    Section {
                        deviceButton(.home, shortcut: .home) { $0.perform(.showDesktop) }
                        deviceButton(.appSwitcher, shortcut: .appSwitcher) { $0.perform(.missionControl) }
                        deviceButton(.lock, shortcut: .lock) { $0.sendChord(.sideButton) }
                    }
                    Section {
                        deviceButton(.volumeUp, shortcut: .volumeUp) { $0.sendChord(.volumeUp) }
                        deviceButton(.volumeDown, shortcut: .volumeDown) { $0.sendChord(.volumeDown) }
                    }
                } else {
                    Section {
                        ForEach(SystemCommand.allCases) { command in
                            Button(String(localized: command.title)) { session?.perform(command) }
                                .keyboardShortcut(for: ShortcutAction.system(command))
                                .disabled(!controlling || !(session?.canPerform(command) ?? false))
                        }
                        Button("Spotlight") { session?.sendChord(.spotlight) }
                            .keyboardShortcut(for: .spotlight)
                            .disabled(!controlling)
                        Button("Command-Tab") { session?.sendChord(.commandTab) }
                            .keyboardShortcut(for: .commandTab)
                            .disabled(!controlling)
                    }
                    #if os(macOS)
                    Section {
                        keyMenu(.functionKeys) { FunctionKeyItems(controller: $0) }
                        keyMenu(.moreKeys) { MoreKeyItems(controller: $0) }
                    }
                    #endif
                }
            }
        }

        CommandMenu("Debug") {
            DebugInfoToggle()
        }

        #if os(macOS)
        CommandGroup(replacing: .help) {}
        #endif
    }
}

#if os(macOS)
private struct ComputerLayoutToggles: View {
    @ObservedObject private var settings = AppSettings.shared

    var body: some View {
        Section {
            Toggle("as Icons", isOn: Binding(get: { settings.computerLayout == .grid }, set: { _ in settings.computerLayout = .grid }))
                .keyboardShortcut(for: .viewAsIcons)
            Toggle("as List", isOn: Binding(get: { settings.computerLayout == .list }, set: { _ in settings.computerLayout = .list }))
                .keyboardShortcut(for: .viewAsList)
        }
    }
}
#endif

private struct DebugInfoToggle: View {
    @ObservedObject private var settings = AppSettings.shared

    var body: some View {
        Toggle("Debug Panel", isOn: $settings.showsDebugInfo)
            .keyboardShortcut(for: .debugPanel)
    }
}
