import SwiftUI
import UniformTypeIdentifiers

struct SessionView: View {
    @StateObject private var controller: SessionController
    @ObservedObject private var settings = AppSettings.shared
    private let hasOwnWindow: Bool
    private let onClose: () -> Void
    @State private var isDropTargeted = false
    #if os(iOS)
    @ObservedObject private var macToolbar = ToolbarLayout.mac
    @ObservedObject private var touchScreenToolbar = ToolbarLayout.touchScreen

    @Environment(\.scenePhase) private var scenePhase
    @State private var toolbarWidth: CGFloat = 0
    @ScaledMetric(relativeTo: .body) private var toolbarItemWidth: CGFloat = 44

    private var isFullScreen: Bool { controller.isFullScreen }

    private var dock: ToolbarDock? {
        controller.pictureMatchesWindow ? nil : ToolbarDock(letterbox: controller.letterbox, itemWidth: toolbarItemWidth)
    }

    private var toolbarSlots: Int {
        ToolbarLayout.capsuleSlots(width: dock?.length ?? toolbarWidth, itemWidth: toolbarItemWidth)
    }

    private var locksPointer: Bool {
        controller.isPointerLockOn && pointerLockAvailable && !pointerLockPaused && controller.pointerLockCanResume
            && (dock == nil || controller.isSystemPointerOverPicture)
    }
    #else
    @State private var showsPointerLockHint = false

    private var locksPointer: Bool {
        controller.isPointerLockOn && pointerLockAvailable && !pointerLockPaused
    }

    private var minimumWindowSize: CGSize {
        controller.pictureMatchesWindow ? VirtualDisplayMode.minimumWindowPoints : CGSize(width: 320, height: 200)
    }
    #endif

    private var pointerLockAvailable: Bool {
        SessionCommand.pointerLock.isOffered(by: controller) && controller.canLockPointer
    }

    private var pointerLockPaused: Bool {
        controller.isToolbarVisible || controller.isZoomModeOn || !controller.isControlling || controller.isKeyboardVisible
            || controller.picture == .none
            || controller.isShowingResolution
            || controller.isCustomizingToolbar
            || controller.credentialPrompt != nil || controller.alert != nil
    }

    private var colorScheme: ColorScheme? {
        controller.picture == .none ? .dark : nil
    }

    init(route: SessionRoute, hasOwnWindow: Bool = true, onClose: @escaping () -> Void) {
        _controller = StateObject(wrappedValue: SessionController(route: route))
        self.hasOwnWindow = hasOwnWindow
        self.onClose = onClose
    }

    #if os(iOS)
    private var deviceScreen: CGSize? {
        guard let screen = controller.touchScreenSize else { return nil }
        let picture = controller.imageSize
        guard picture.width > 0, picture.height > 0, (picture.width > picture.height) != (screen.width > screen.height)
        else { return screen }
        return CGSize(width: screen.height, height: screen.width)
    }

    var body: some View {
        let dock = dock
        let plan = ToolbarLayout.layout(for: controller).plan(for: controller, slots: toolbarSlots)
        screen
            .ignoresSafeArea()
            .background {
                Color.black.ignoresSafeArea()
                FullScreenReader(isFullScreen: $controller.isFullScreen)
                    .ignoresSafeArea()
                if #available(iOS 26, *), controller.isTouchScreen, hasOwnWindow {
                    DeviceWindowShaper(screen: deviceScreen, isFullScreen: isFullScreen)
                }
            }
            .overlay(alignment: .topTrailing) {
                if settings.showsDebugInfo, controller.phase.isActive {
                    DebugInfoView(controller: controller)
                        .padding(8)
                        .allowsHitTesting(false)
                }
            }
            .overlay(alignment: .top) {
                FloatingToolbar(controller: controller, plan: plan, dock: dock, closes: isFullScreen || !hasOwnWindow, onClose: close)
            }
            .onGeometryChangeCompat(for: CGFloat.self) { proxy in
                proxy.size.width - proxy.safeAreaInsets.leading - proxy.safeAreaInsets.trailing
            } action: { width in
                toolbarWidth = width
            }
            .preferredColorScheme(colorScheme)
            .statusBarHidden(isFullScreen)
            .persistentSystemOverlaysCompat(isFullScreen ? .hidden : .automatic)
            .defersSystemGesturesCompat(on: controller.isControlling ? .all : [])
            .requestsPointerLock(locksPointer)
            .onChangeCompat(of: pointerLockAvailable) { available in
                if !available { controller.setPointerLock(false) }
            }
            .onChangeCompat(of: pointerLockPaused) { paused in
                if !paused, !controller.pointerLockCanResume { controller.setPointerLock(false) }
            }
            .focusedSceneValue(\.sessionController, controller)
            .sheet(isPresented: $controller.isCustomizingToolbar) {
                CustomizeToolbarView(controller: controller, slots: toolbarSlots)
            }
            .alert($controller.alert)
            .sheet(
                item: Binding(
                    get: { controller.credentialPrompt },
                    set: { prompt in
                        if prompt == nil, controller.credentialPrompt != nil {
                            controller.cancelCredentials()
                        }
                    })
            ) { prompt in
                CredentialsView(prompt: prompt, computerName: controller.title) { credentials, remember in
                    controller.submitCredentials(credentials, remember: remember)
                } onCancel: {
                    controller.cancelCredentials()
                }
            }
            .onAppear {
                controller.onClose = onClose
                controller.start()
            }
            .onDisappear {
                controller.disconnect()
            }
            .onChangeCompat(of: controller.isFullScreen) {
                controller.isToolbarVisible = false
            }
            .onChangeCompat(of: dock != nil, initial: true) { docked in
                controller.isToolbarDocked = docked
                if docked {
                    controller.isToolbarVisible = false
                }
            }
            .onChangeCompat(of: scenePhase) { phase in
                switch phase {
                case .background: controller.sceneDidEnterBackground()
                case .inactive: ClipboardAccess.shared.sceneDidBecomeInactive()
                case .active: controller.sceneDidBecomeActive()
                @unknown default: break
                }
            }
    }
    #else
    var body: some View {
        screen
            .background(Color.black)
            .overlay(alignment: .topTrailing) {
                if settings.showsDebugInfo, controller.phase.isActive {
                    DebugInfoView(controller: controller)
                        .padding(8)
                        .allowsHitTesting(false)
                }
            }
            .overlay(alignment: .top) {
                if showsPointerLockHint {
                    Text("Press \(EventModifiers(KeyShortcuts.shared.pointerRelease).symbols) to release the pointer")
                        .font(.callout)
                        .padding(.horizontal, 14)
                        .padding(.vertical, 8)
                        .background(.regularMaterial, in: Capsule())
                        .padding(.top, 12)
                        .allowsHitTesting(false)
                        .transition(.opacity)
                }
            }
            .frame(minWidth: minimumWindowSize.width, minHeight: minimumWindowSize.height)
            .navigationTitle(controller.title)
            .navigationSubtitle(controller.mode == .observe ? String(localized: "Observing") : "")
            .toolbar {
                SessionWindowToolbar(controller: controller, layout: .layout(for: controller))
            }
            .windowToolbarFullScreenVisibilityCompat()
            .sheet(isPresented: $controller.isCustomizingToolbar) {
                CustomizeToolbarView(controller: controller, slots: nil)
            }
            .preferredColorScheme(colorScheme)
            .onChange(of: pointerLockAvailable) { _, available in
                if !available { controller.setPointerLock(false) }
            }
            .onChange(of: locksPointer, initial: true) { _, locks in
                controller.screen?.setLocksPointer(locks)
            }
            .task(id: controller.isPointerLocked) {
                guard controller.isPointerLocked else {
                    showsPointerLockHint = false
                    return
                }
                withAnimation { showsPointerLockHint = true }
                try? await Task.sleep(seconds: 3)
                withAnimation { showsPointerLockHint = false }
            }
            .focusedSceneValue(\.sessionController, controller)
            .alert($controller.alert)
            .sheet(
                item: Binding(
                    get: { controller.credentialPrompt },
                    set: { prompt in
                        if prompt == nil, controller.credentialPrompt != nil {
                            controller.cancelCredentials()
                        }
                    }),
                onDismiss: {
                    if controller.isClosing { onClose() }
                }
            ) { prompt in
                CredentialsView(prompt: prompt, computerName: controller.title) { credentials, remember in
                    controller.submitCredentials(credentials, remember: remember)
                } onCancel: {
                    controller.cancelCredentials()
                }
            }
            .onAppear {
                controller.onClose = onClose
                controller.start()
            }
            .onDisappear {
                controller.disconnect()
            }
    }
    #endif

    private var screen: some View {
        ZStack {
            RemoteScreenView(controller: controller)
                .onDrop(of: [.item], delegate: FileDropDelegate(canAccept: controller.canTransferFiles, isTargeted: $isDropTargeted, perform: drop))
            if isDropTargeted && controller.canTransferFiles {
                RoundedRectangle(cornerRadius: 24)
                    .strokeBorder(Color.accentColor, lineWidth: 4)
                    .overlay {
                        Label("Send to Desktop", systemImage: "arrow.up.doc")
                            .font(.headline)
                            .padding(.horizontal, 20)
                            .padding(.vertical, 14)
                            .glassEffectCompat(in: Capsule())
                    }
                    .allowsHitTesting(false)
            }
            SessionStatusOverlay(controller: controller, onClose: close)
                .environment(\.colorScheme, .dark)
        }
    }

    private func drop(_ providers: [NSItemProvider]) -> Bool {
        guard controller.canTransferFiles else { return false }
        let folder = FileManager.default.temporaryDirectory.appendingPathComponent("Drops", isDirectory: true)
            .appendingPathComponent(UUID().uuidString, isDirectory: true)
        try? FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
        for provider in providers {
            let type = provider.registeredTypeIdentifiers.first { UTType($0)?.conforms(to: .item) == true } ?? UTType.item.identifier
            let suggestedName = provider.suggestedName
            _ = provider.loadFileRepresentation(forTypeIdentifier: type) { url, error in
                guard let url else {
                    Task { @MainActor in controller.reportSendFailure(suggestedName ?? String(localized: "Item"), error: error) }
                    return
                }
                let destination = folder.appendingPathComponent(
                    suggestedName.map { name in
                        url.pathExtension.isEmpty || name.hasSuffix(url.pathExtension) ? name : "\(name).\(url.pathExtension)"
                    } ?? url.lastPathComponent)
                do {
                    try FileManager.default.copyItem(at: url, to: destination)
                } catch {
                    Task { @MainActor in controller.reportSendFailure(destination.lastPathComponent, error: error) }
                    return
                }
                Task { @MainActor in
                    controller.uploadFiles([destination], to: "~/Desktop", temporary: true)
                }
            }
        }
        return true
    }

    private func close() {
        controller.close()
    }
}

private struct FileDropDelegate: DropDelegate {
    let canAccept: Bool
    @Binding var isTargeted: Bool
    let perform: ([NSItemProvider]) -> Bool

    func validateDrop(info: DropInfo) -> Bool { canAccept }

    func dropEntered(info: DropInfo) { isTargeted = true }

    func dropExited(info: DropInfo) { isTargeted = false }

    func dropUpdated(info: DropInfo) -> DropProposal? {
        DropProposal(operation: canAccept ? .copy : .forbidden)
    }

    func performDrop(info: DropInfo) -> Bool {
        isTargeted = false
        return perform(info.itemProviders(for: [.item]))
    }
}

#if os(iOS)
@available(iOS 26, *)
private struct DeviceWindowShaper: UIViewRepresentable {
    let screen: CGSize?
    let isFullScreen: Bool

    func makeUIView(context: Context) -> ShaperView {
        ShaperView()
    }

    func updateUIView(_ view: ShaperView, context: Context) {
        view.screen = screen
        view.isFullScreen = isFullScreen
        view.shape()
    }

    final class ShaperView: UIView {
        var screen: CGSize?
        var isFullScreen = false
        private var observation: NSKeyValueObservation?
        private var hasShaped = false
        private var requested: CGSize?
        private var minimumSize: CGSize?
        private var raises = 0

        override init(frame: CGRect) {
            super.init(frame: frame)
            isUserInteractionEnabled = false
        }

        required init?(coder: NSCoder) {
            fatalError()
        }

        override func didMoveToWindow() {
            super.didMoveToWindow()
            observation = window?.windowScene?.observe(\.effectiveGeometry) { [weak self] _, _ in
                DispatchQueue.main.async { self?.shape() }
            }
            shape()
        }

        func shape() {
            guard let scene = window?.windowScene, let restrictions = scene.sizeRestrictions, let screen,
                screen.width > 0, screen.height > 0, !isFullScreen
            else { return }
            if scene.effectiveGeometry.isInteractivelyResizing {
                requested = nil
                return
            }
            let target = target(for: scene, screen: screen, atLeastScreen: !hasShaped)
            hasShaped = true
            guard let target, !Self.isClose(target, requested) else { return }
            requested = target
            let minimum = minimumSize ?? restrictions.minimumSize
            minimumSize = minimum
            restrictions.minimumSize = target
            raises += 1
            let raise = raises
            DispatchQueue.main.asyncAfter(deadline: .now() + 0.25) { [weak self] in
                guard self?.raises == raise else { return }
                restrictions.minimumSize = minimum
            }
        }

        private func target(for scene: UIWindowScene, screen: CGSize, atLeastScreen: Bool) -> CGSize? {
            let current = scene.effectiveGeometry.coordinateSpace.bounds.size
            guard current.width > 0, current.height > 0 else { return nil }
            var target =
                current.width / current.height < screen.width / screen.height
                ? CGSize(width: current.height * screen.width / screen.height, height: current.height)
                : CGSize(width: current.width, height: current.width * screen.height / screen.width)
            if atLeastScreen, target.width < screen.width {
                target = screen
            }
            let available = scene.screen.bounds.size
            let fit = min(1, available.width / target.width, available.height / target.height)
            target = CGSize(width: target.width * fit, height: target.height * fit)
            return target.width > current.width + 2 || target.height > current.height + 2 ? target : nil
        }

        private static func isClose(_ size: CGSize, _ other: CGSize?) -> Bool {
            guard let other else { return false }
            return abs(size.width - other.width) <= 2 && abs(size.height - other.height) <= 2
        }
    }
}

private struct FullScreenReader: UIViewRepresentable {
    @Binding var isFullScreen: Bool

    func makeUIView(context: Context) -> ReaderView {
        ReaderView()
    }

    func updateUIView(_ view: ReaderView, context: Context) {
        view.onChange = { value in
            if isFullScreen != value {
                isFullScreen = value
            }
        }
        view.report()
    }

    final class ReaderView: UIView {
        var onChange: ((Bool) -> Void)?

        override init(frame: CGRect) {
            super.init(frame: frame)
            isUserInteractionEnabled = false
        }

        required init?(coder: NSCoder) {
            fatalError()
        }

        override func didMoveToWindow() {
            super.didMoveToWindow()
            report()
        }

        override func layoutSubviews() {
            super.layoutSubviews()
            report()
        }

        func report() {
            guard let window, let screen = window.windowScene?.screen else { return }
            let windowSize = window.bounds.size
            let screenSize = screen.bounds.size
            func fills(_ width: CGFloat, _ height: CGFloat) -> Bool {
                windowSize.width >= width - 1 && windowSize.height >= height - 1
            }
            let value =
                traitCollection.userInterfaceIdiom == .phone || fills(screenSize.width, screenSize.height)
                || fills(screenSize.height, screenSize.width)
            DispatchQueue.main.async { [weak self] in
                self?.onChange?(value)
            }
        }
    }
}
#else
private struct SessionWindowToolbar: ToolbarContent {
    @ObservedObject var controller: SessionController
    @ObservedObject var layout: ToolbarLayout

    var body: some ToolbarContent {
        ForEach(layout.plan(for: controller, slots: nil).inBar) { command in
            ToolbarItem {
                SessionCommandView(command: command, controller: controller, style: .barItem)
                    .help(String(localized: command.title))
            }
        }
        ToolbarItem {
            Button {
                controller.isCustomizingToolbar = true
            } label: {
                Label("Customize Toolbar…", systemImage: "slider.horizontal.3")
            }
            .help("Customize Toolbar")
        }
    }
}
#endif
