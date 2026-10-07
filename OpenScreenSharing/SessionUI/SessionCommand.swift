import SwiftUI

enum SessionCommand: String, CaseIterable, Identifiable {
    case zoomMode, scale, displays, quality, resolution, hdr
    case audio
    case observe = "control"
    case pointerLock, keyboard, missionControl, appWindows, showDesktop, launchpad, spotlight, commandTab
    case home, appSwitcher, lock, volumeUp, volumeDown
    case clipboard
    case debugInfo

    var id: Self { self }

    var title: String.LocalizationValue {
        switch self {
        case .zoomMode: "Zoom Mode"
        case .scale: "Scaling"
        case .displays: "Displays"
        case .quality: "Quality"
        case .resolution: "Resolution"
        case .hdr: "High Dynamic Range"
        case .audio: "Sound"
        case .observe: "Observe Mode"
        case .pointerLock: "Pointer Lock"
        case .keyboard: "Keyboard"
        case .missionControl, .appWindows, .showDesktop, .launchpad: systemCommand!.title
        case .spotlight: "Spotlight"
        case .commandTab: "Command-Tab"
        case .home: "Home Screen"
        case .appSwitcher: "App Switcher"
        case .lock: "Lock"
        case .volumeUp: "Volume Up"
        case .volumeDown: "Volume Down"
        case .clipboard: "Clipboard"
        case .debugInfo: "Debug Panel"
        }
    }

    var systemImage: String {
        switch self {
        case .zoomMode: "plus.magnifyingglass"
        case .scale: "arrow.up.left.and.arrow.down.right"
        case .displays: "display.2"
        case .quality: "sparkles.tv"
        case .resolution: "aspectratio"
        case .hdr: "sun.max"
        case .audio: "speaker.wave.2"
        case .observe: "eye"
        case .pointerLock: "computermouse"
        case .keyboard: "keyboard"
        case .missionControl, .appWindows, .showDesktop, .launchpad: systemCommand!.systemImage
        case .spotlight: "magnifyingglass"
        case .commandTab: "command"
        case .home: "house"
        case .appSwitcher: if #available(iOS 18, *) { "iphone.app.switcher" } else { "square.stack" }
        case .lock: "lock"
        case .volumeUp: if #available(iOS 16, *) { "speaker.plus" } else { "speaker.wave.3" }
        case .volumeDown: if #available(iOS 16, *) { "speaker.minus" } else { "speaker.wave.1" }
        case .clipboard: if #available(iOS 16, *) { "list.clipboard" } else { "doc.on.clipboard" }
        case .debugInfo: if #available(iOS 17, *) { "gauge.with.dots.needle.67percent" } else { "gauge" }
        }
    }

    var category: SessionCommandCategory {
        switch self {
        case .zoomMode, .scale, .displays, .quality, .resolution, .hdr: .view
        case .audio: .sound
        case .observe, .pointerLock, .keyboard, .missionControl, .appWindows, .showDesktop, .launchpad, .spotlight, .commandTab,
            .home, .appSwitcher, .lock, .volumeUp, .volumeDown:
            .control
        case .clipboard: .clipboard
        case .debugInfo: .debug
        }
    }

    var systemCommand: SystemCommand? {
        switch self {
        case .missionControl: .missionControl
        case .appWindows: .appWindows
        case .showDesktop: .showDesktop
        case .launchpad: .launchpad
        default: nil
        }
    }

    func applies(toTouchScreen touchScreen: Bool) -> Bool {
        switch self {
        case .home, .appSwitcher, .lock, .volumeUp, .volumeDown: touchScreen
        case .displays, .quality, .resolution, .hdr, .missionControl, .appWindows, .showDesktop, .launchpad, .commandTab:
            !touchScreen
        case .zoomMode, .scale, .audio, .observe, .pointerLock, .keyboard, .spotlight, .clipboard, .debugInfo: true
        }
    }

    func isOffered(by controller: SessionController) -> Bool {
        guard applies(toTouchScreen: controller.isTouchScreen) else { return false }
        let capabilities = controller.capabilities
        let known = !capabilities.isEmpty
        func offers(_ capability: SessionCapabilities) -> Bool { !known || capabilities.contains(capability) }
        switch self {
        case .zoomMode, .debugInfo: return true
        case .scale: return !controller.pictureMatchesWindow
        case .quality: return !controller.usesHighPerformance
        case .audio: return controller.usesHighPerformance
        case .displays: return controller.showsDisplayPicker
        case .resolution: return controller.offersResolution
        case .hdr:
            return controller.usesHighPerformance && controller.displaySupportsHDR && offers(.highPerformance)
                && controller.networkSupportsHDR
        case .observe, .keyboard, .spotlight, .commandTab, .lock, .volumeUp, .volumeDown: return offers(.control)
        case .pointerLock: return offers(.control) && PointerLock.hasMouse
        case .home: return !known || controller.canPerform(.showDesktop)
        case .appSwitcher: return !known || controller.canPerform(.missionControl)
        case .missionControl, .appWindows, .showDesktop, .launchpad:
            return !known || controller.canPerform(systemCommand!)
        case .clipboard: return offers(.clipboard)
        }
    }

    func isEnabled(in controller: SessionController) -> Bool {
        let connected = controller.phase == .connected
        switch self {
        case .zoomMode, .scale, .quality, .audio, .debugInfo: return true
        case .displays, .clipboard: return connected
        case .resolution: return controller.isControlling
        case .hdr: return controller.canUseHDR
        case .observe: return controller.capabilities.contains(.control)
        case .pointerLock: return controller.canLockPointer
        case .keyboard, .missionControl, .appWindows, .showDesktop, .launchpad, .spotlight, .commandTab, .home, .appSwitcher, .lock,
            .volumeUp, .volumeDown:
            return controller.isControlling
        }
    }
}

enum SessionCommandCategory: CaseIterable, Identifiable {
    case view, sound, control, clipboard, debug

    var id: Self { self }

    var title: String.LocalizationValue {
        switch self {
        case .view: "View"
        case .sound: "Sound"
        case .control: "Control"
        case .clipboard: "Clipboard"
        case .debug: "Debug"
        }
    }

    var systemImage: String {
        switch self {
        case .view: "display"
        case .sound: "speaker.wave.2"
        case .control: "cursorarrow.rays"
        case .clipboard: if #available(iOS 16, *) { "list.clipboard" } else { "doc.on.clipboard" }
        case .debug: "ladybug"
        }
    }

    var commands: [SessionCommand] {
        SessionCommand.allCases.filter { $0.category == self }
    }
}

extension SessionController {
    var showsDisplayPicker: Bool {
        displays.count > 1 && !usesHighPerformance
    }

    var offersResolution: Bool {
        (usesHighPerformance || usesDynamicResolution) && (capabilities.isEmpty || capabilities.contains(.virtualDisplay))
    }

    var audioOptions: [RemoteAudio] {
        isTouchScreen ? [.off, .fromMac] : RemoteAudio.allCases
    }
}

enum SessionCommandStyle {
    case barItem
    case menuItem

    var isBar: Bool { self != .menuItem }
}

struct SessionCommandView: View {
    let command: SessionCommand
    @ObservedObject var controller: SessionController
    var style: SessionCommandStyle = .menuItem

    @ObservedObject private var settings = AppSettings.shared

    var body: some View {
        content
            .disabled(!command.isEnabled(in: controller))
    }

    @ViewBuilder
    private var content: some View {
        switch command {
        case .zoomMode:
            toggle(isOn: Binding(get: { controller.isZoomModeOn }, set: { controller.setZoomMode($0) }))
        case .scale:
            ScaleButton(controller: controller)
        case .displays:
            items { DisplayPicker(controller: controller, inOwnMenu: style.isBar).pickerStyle(.inline) }
        case .quality:
            items { QualityPicker(controller: controller, inOwnMenu: style.isBar).pickerStyle(.inline) }
        case .resolution:
            switch style {
            case .barItem:
                Button { controller.isShowingResolution = true } label: { label }
                    .resolutionPopover(controller: controller, isPresented: $controller.isShowingResolution)
            case .menuItem:
                Button { controller.isShowingResolution = true } label: { Label("Resolution…", systemImage: command.systemImage) }
            }
        case .hdr:
            toggle(
                isOn: Binding(get: { controller.hdrEnabled && controller.canUseHDR }, set: { controller.setHDR($0) }),
                subtitle: !controller.usesDynamicResolution ? "Only available with Dynamic Resolution"
                    : !DecoderCapabilities.current.supportsHDR ? "This Device Can’t Show HDR" : nil)
        case .audio:
            items { AudioPicker(controller: controller).pickerStyle(.inline) }
        case .observe:
            toggle(isOn: Binding(get: { controller.mode == .observe }, set: { controller.setMode($0 ? .observe : .control) }))
        case .pointerLock:
            toggle(isOn: Binding(get: { controller.isPointerLockOn }, set: { controller.setPointerLock($0) }))
        case .keyboard:
            KeyboardButton(controller: controller) {
                if style == .barItem, controller.isToolbarVisible {
                    controller.toggleToolbar()
                }
            }
        case .missionControl, .appWindows, .showDesktop, .launchpad:
            let systemCommand = command.systemCommand!
            Button {
                controller.perform(systemCommand)
            } label: { label }
        case .spotlight:
            Button { controller.sendChord(.spotlight) } label: { label }
        case .commandTab:
            Button { controller.sendChord(.commandTab) } label: { label }
        case .home:
            Button { controller.perform(.showDesktop) } label: { label }
        case .appSwitcher:
            Button { controller.perform(.missionControl) } label: { label }
        case .lock:
            Button { controller.sendChord(.sideButton) } label: { label }
        case .volumeUp:
            Button { controller.sendChord(.volumeUp) } label: { label }
        case .volumeDown:
            Button { controller.sendChord(.volumeDown) } label: { label }
        case .clipboard:
            items { ClipboardMenuItems(controller: controller) }
        case .debugInfo:
            toggle(isOn: $settings.showsDebugInfo)
        }
    }

    private var label: some View {
        Label(String(localized: command.title), systemImage: command.systemImage)
    }

    @ViewBuilder
    private func toggle(isOn: Binding<Bool>, subtitle: LocalizedStringKey? = nil) -> some View {
        switch style {
        case .barItem: Toggle(isOn: isOn) { label }.toggleStyle(BarToggleStyle())
        case .menuItem:
            Toggle(isOn: isOn) {
                label
                if let subtitle {
                    Text(subtitle)
                }
            }
        }
    }

    @ViewBuilder
    private func items(@ViewBuilder _ items: () -> some View) -> some View {
        if style.isBar {
            Menu {
                items()
            } label: {
                label
            }
        } else {
            items()
        }
    }
}

struct BarToggleStyle: ToggleStyle {
    func makeBody(configuration: Configuration) -> some View {
        Button {
            configuration.isOn.toggle()
        } label: {
            configuration.label
                .background {
                    if configuration.isOn {
                        Circle().fill(Color.accentColor).padding(3)
                    }
                }
        }
        .accessibilityAddTraits(configuration.isOn ? .isSelected : [])
    }
}
