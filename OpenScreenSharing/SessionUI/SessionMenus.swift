import SwiftUI

struct ScaleButton: View {
    @ObservedObject var controller: SessionController

    var body: some View {
        if controller.scaleMode == .fit {
            Button { controller.setScale(.actualSize) } label: { Label("Zoom to Actual Size", systemImage: "arrow.up.left.and.arrow.down.right") }
        } else {
            Button { controller.setScale(.fit) } label: { Label("Zoom to Fit", systemImage: "arrow.down.right.and.arrow.up.left") }
        }
    }
}

struct DisplayPicker: View {
    @ObservedObject var controller: SessionController
    var inOwnMenu = false

    var body: some View {
        Picker(selection: Binding(get: { controller.displaySelection }, set: { controller.selectDisplay($0) })) {
            ForEach(controller.displays) { display in
                Text(display.name).tag(DisplaySelection.display(display.id))
            }
            Text(inOwnMenu ? "All" : "All Displays").tag(DisplaySelection.all)
        } label: {
            Label("Displays", systemImage: SessionCommand.displays.systemImage)
        }
    }
}

extension SessionQuality {
    var title: String.LocalizationValue {
        switch self {
        case .adaptive: "Adaptive Quality"
        case .full: "Full Quality"
        }
    }

    var shortTitle: String.LocalizationValue {
        switch self {
        case .adaptive: "Adaptive"
        case .full: "Full"
        }
    }
}

struct QualityPicker: View {
    @ObservedObject var controller: SessionController
    var inOwnMenu = false

    var body: some View {
        Picker(selection: Binding(get: { controller.quality }, set: { controller.setQuality($0) })) {
            ForEach(SessionQuality.allCases) { quality in
                Text(String(localized: inOwnMenu ? quality.shortTitle : quality.title)).tag(quality)
            }
        } label: {
            Label("Quality", systemImage: SessionCommand.quality.systemImage)
        }
    }
}

extension RemoteAudio {
    var title: String.LocalizationValue {
        switch self {
        case .off: "No Audio"
        case .fromMac: "Receive Audio"
        case .toMac: "Send Audio"
        }
    }
}

struct AudioPicker: View {
    @ObservedObject var controller: SessionController

    var body: some View {
        Picker(selection: Binding(get: { controller.remoteAudio }, set: { controller.setRemoteAudio($0) })) {
            ForEach(controller.audioOptions) { audio in
                Text(String(localized: audio.title)).tag(audio)
            }
        } label: {
            Label("Sound", systemImage: SessionCommand.audio.systemImage)
        }
    }
}

struct ResolutionOptions: View {
    @ObservedObject var controller: SessionController

    var body: some View {
        ForEach(VirtualDisplayMode.resolutionScales, id: \.self) { scale in
            Toggle(
                resolutionScaleTitle(scale),
                isOn: Binding(get: { controller.resolutionScale == scale }, set: { _ in controller.setResolutionScale(scale) }))
        }
    }
}

func resolutionScaleTitle(_ scale: Double) -> String {
    scale == 1 ? String(localized: "Native") : String(localized: "\(scale.formatted(.number.precision(.fractionLength(0...2))))×")
}

struct ResolutionPanel: View {
    @ObservedObject var controller: SessionController

    @State private var draggedScale: Double?

    private var scale: Double { draggedScale ?? controller.resolutionScale }

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack {
                Text("Resolution")
                Spacer()
                Text(resolutionScaleTitle(scale))
                    .monospacedDigit()
                    .foregroundStyle(.secondary)
            }
            Slider(
                value: Binding(
                    get: { scale },
                    set: { value in
                        if draggedScale != nil {
                            draggedScale = value
                        } else {
                            controller.setResolutionScale(value)
                        }
                    }),
                in: VirtualDisplayMode.resolutionScaleRange,
                step: VirtualDisplayMode.resolutionScaleStep
            ) {
                Text("Resolution")
            } onEditingChanged: { editing in
                if editing {
                    draggedScale = controller.resolutionScale
                } else if let draggedScale {
                    self.draggedScale = nil
                    controller.setResolutionScale(draggedScale)
                }
            }
            Text("Raising the resolution by scaling reduces sharpness and increases bandwidth use and picture latency.")
                .font(.footnote)
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
        }
        .font(.body)
        .padding()
        .frame(width: 340)
    }
}

extension View {
    func resolutionPopover(controller: SessionController, isPresented: Binding<Bool>) -> some View {
        popover(isPresented: isPresented) {
            ResolutionPanel(controller: controller)
                .popoverOnCompact()
        }
        .onDisappear { controller.isShowingResolution = false }
    }
}

struct ClipboardMenuItems: View {
    @ObservedObject var controller: SessionController

    var body: some View {
        Toggle(isOn: Binding(get: { controller.clipboardSharing }, set: { controller.setClipboardSharing($0) })) { Label("Sync Clipboard", systemImage: "arrow.up.arrow.down") }
        Section {
            Button { controller.getClipboardFromMac() } label: { Label("Receive Clipboard", systemImage: "doc.on.clipboard") }
                .disabled(!controller.capabilities.contains(.clipboardRequest))
            Button { controller.sendClipboardToMac() } label: { Label("Send Clipboard", systemImage: "arrow.up.doc.on.clipboard") }
        }
        .disabled(controller.clipboardSharing)
    }
}

struct FunctionKeyItems: View {
    @ObservedObject var controller: SessionController

    var body: some View {
        ForEach(1...12, id: \.self) { number in
            Button("F\(number)") {
                controller.typeKey(.function(number))
            }
        }
    }
}

struct MoreKeyItems: View {
    @ObservedObject var controller: SessionController

    var body: some View {
        if controller.platform == .pc {
            Section {
                pcKey("Insert", systemImage: "text.insert", key: .insert)
                pcKey("Delete", systemImage: "delete.right", key: .forwardDelete)
            }
            Section {
                pcKey("Home", systemImage: "arrow.up.to.line", key: .home)
                pcKey("End", systemImage: "arrow.down.to.line", key: .end)
                pcKey("Page Up", systemImage: "arrow.up.to.line.compact", key: .pageUp)
                pcKey("Page Down", systemImage: "arrow.down.to.line.compact", key: .pageDown)
            }
            Section {
                pcKey("Print Screen", systemImage: "camera.viewfinder", key: .printScreen)
                pcKey("Menu", systemImage: "contextualmenu.and.cursorarrow", key: .menu)
            }
        } else {
            Section {
                Button { controller.typeKey(.home) } label: { Label("Home", systemImage: "arrow.up.to.line") }
                Button { controller.typeKey(.end) } label: { Label("End", systemImage: "arrow.down.to.line") }
                Button { controller.typeKey(.pageUp) } label: { Label("Page Up", systemImage: "arrow.up.to.line.compact") }
                Button { controller.typeKey(.pageDown) } label: { Label("Page Down", systemImage: "arrow.down.to.line.compact") }
            }
            Section {
                Button { controller.typeKey(.forwardDelete) } label: { Label("Forward Delete", systemImage: "delete.right") }
            }
        }
    }

    private func pcKey(_ legend: String, systemImage: String, key: MacKey) -> some View {
        Button { controller.typeKey(key) } label: {
            Label { Text(verbatim: legend) } icon: { Image(systemName: systemImage) }
        }
    }
}

extension SystemCommand {
    var title: String.LocalizationValue {
        switch self {
        case .missionControl: "Mission Control"
        case .appWindows: "App Windows"
        case .showDesktop: "Desktop"
        case .launchpad: "Apps"
        }
    }

    var systemImage: String {
        switch self {
        case .missionControl: "rectangle.3.group"
        case .appWindows: "rectangle.stack"
        case .showDesktop: "menubar.dock.rectangle"
        case .launchpad: "square.grid.3x3"
        }
    }
}

#if os(iOS)
struct SessionMoreMenuContent: View {
    @ObservedObject var controller: SessionController

    var body: some View {
        ForEach(SessionCommandCategory.allCases) { category in
            let commands = category.commands.filter { $0.isOffered(by: controller) }
            if !commands.isEmpty {
                Menu {
                    if category == .control {
                        let local: [SessionCommand] = [.observe, .pointerLock, .nativeTouch, .keyboard]
                        Section {
                            items(commands.filter { local.contains($0) })
                        }
                        Section {
                            items(commands.filter { !local.contains($0) })
                        }
                    } else {
                        items(commands)
                    }
                } label: {
                    Label(String(localized: category.title), systemImage: category.systemImage)
                }
            }
        }
        Section {
            Button { controller.isCustomizingToolbar = true } label: { Label("Customize Toolbar…", systemImage: "slider.horizontal.3") }
        }
    }

    private func items(_ commands: [SessionCommand]) -> some View {
        ForEach(commands) { command in
            SessionCommandView(command: command, controller: controller)
        }
    }
}
#endif
