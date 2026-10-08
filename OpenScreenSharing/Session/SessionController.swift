import AVFoundation
import Atomics
import Combine
import CoreGraphics
import Foundation
import SwiftUI
import UIKit
import UniformTypeIdentifiers

enum ScaleMode: Equatable {
    case fit
    case actualSize
    case custom
}

struct CredentialPrompt: Identifiable {
    enum Form: Equatable {
        case account(allowsRequest: Bool)
        case password
    }

    let id = UUID()
    var form: Form
    var username: String
    var remembers: Bool
    var failed: Bool
    var errorMessage = ""
}

struct FileTransfer: Identifiable, Equatable {
    enum Direction: Equatable {
        case upload
        case download
    }

    enum State: Equatable {
        case running
        case finished
        case failed(FileTransferError)
        case cancelled
    }

    let id: Int64
    let direction: Direction
    var name: String
    var transferred: Int64 = 0
    var total: Int64 = 0
    var currentItem: String?
    var state: State = .running
    var localFolder: URL?
    var scopedURLs: [URL] = []
    var temporaryURLs: [URL] = []

    var isActive: Bool { state == .running }

    var fraction: Double {
        total > 0 ? min(1, Double(transferred) / Double(total)) : 0
    }
}

nonisolated extension FileTransferError: LocalizedError {
    var summary: String {
        switch kind {
        case .cancelled: String(localized: "Canceled")
        case .notAllowed: String(localized: "The Mac doesn’t allow file transfers")
        case .notFound: String(localized: "The item isn’t available on the Mac")
        case .notWritable: String(localized: "The Mac can’t write to the folder")
        case .diskFull: String(localized: "The Mac’s disk is full")
        case .permission: String(localized: "The Mac doesn’t have permission to read the item")
        case .local: String(localized: "This device can’t read or write the file")
        case .connection: String(localized: "Connection interrupted")
        case .observeOnly: String(localized: "Not available while observing")
        case .unsupported: String(localized: "The Mac doesn’t support file transfers")
        case .failed: String(localized: "File transfer failed")
        }
    }

    var errorDescription: String? { message }
}

nonisolated struct SessionMemory: Equatable, Sendable {
    var quality = SessionQuality.adaptive
    var observes = false
    var sharesClipboard = true
    var usesDynamicResolution = true
    var resolutionScale = VirtualDisplayMode.defaultResolutionScale
    var display: DisplaySelection?
    var usesHDR = false
    var remoteAudio = RemoteAudio.fromMac
}

final class SessionController: ObservableObject {
    let route: SessionRoute
    @Published private(set) var title: String
    @Published private(set) var phase: SessionPhase = .idle
    @Published private(set) var mode: SessionMode = .control
    @Published private(set) var displays: [RemoteDisplay] = []
    @Published private(set) var displaySelection: DisplaySelection = .display(1)
    @Published private(set) var capabilities: SessionCapabilities = []
    @Published private(set) var serverType: ServerType?
    @Published private(set) var quality: SessionQuality = .adaptive
    @Published private var networkConfiguration = NetworkConfiguration()
    @Published private(set) var clipboardSharing = true
    @Published private(set) var info = SessionInfo()
    @Published private(set) var keyboardSources: [MacInputSource] = []
    @Published private(set) var imageSize: CGSize = .zero
    @Published private(set) var picture = ScreenPicture.none
    @Published private(set) var isVideo = false
    @Published private(set) var usesDynamicResolution = true
    @Published private(set) var resolutionScale = VirtualDisplayMode.defaultResolutionScale
    @Published private(set) var hdrEnabled = false
    @Published var displaySupportsHDR = false
    @Published private(set) var remotePointerVisible = true
    @Published private(set) var remoteAudio: RemoteAudio = .fromMac
    @Published var scaleMode: ScaleMode = .fit
    @Published var isFullScreen = false
    @Published var isToolbarVisible = false
    @Published var isToolbarDocked = false
    @Published var isToolbarHandleHighlighted = false
    @Published var letterbox: Letterbox?
    @Published private(set) var isPointerLockOn = false
    @Published private(set) var isZoomModeOn = false
    @Published var isPointerLocked = false {
        didSet {
            if isPointerLocked { pointerLockHasLocked = true }
        }
    }
    @Published var isSystemPointerOverPicture = false
    @Published private(set) var isKeyboardVisible = false
    @Published private(set) var usesHardwareKeyboard = false
    @Published private(set) var composingText: String?
    @Published var stickyModifiers: Set<ModifierKey> = []
    @Published private(set) var lockedModifiers: Set<ModifierKey> = []
    @Published private(set) var pressedModifiers: Set<ModifierKey> = []
    @Published var credentialPrompt: CredentialPrompt?
    @Published private(set) var computerID: UUID?
    @Published var alert: AlertState?
    @Published private(set) var isClosing = false
    @Published private(set) var transfers: [FileTransfer] = []
    @Published var isShowingResolution = false
    @Published var isCustomizingToolbar = false
    @Published private(set) var lastRemoteText: String?
    let debugInfo = DebugInfoMonitor()
    private var lastRemoteClipboard: ClipboardContent?
    private var syncedClipboardChangeCount = UIPasteboard.general.changeCount
    private var readingClipboard = false
    private var decidedClipboardSharing = false

    let pipeline = DisplayPipeline()
    weak var screen: RemoteScreenUIView?
    var toolbarHandleFrame: CGRect?
    var toolbarBarFrame: CGRect?
    var lastSystemPointerLocation: CGPoint?
    private var pointerLockHasLocked = false
    private let store = ComputerStore.shared
    private var miceChanges: AnyCancellable?
    var onClose: () -> Void = {}

    private var session: LibsharingRemoteSession?
    private var sink: SessionEventSink?
    private var credentials: SessionCredentials?
    private var pressedKeys: [UIKeyboardHIDUsage: MacKey] = [:]
    private var repeatTask: Task<Void, Never>?
    private var capsLockEngaged = false
    private var capsLockPressedAt: TimeInterval?
    private var capsLockActsOnPress = false
    private var keyboardType = MacKeyboardType.ansi
    private(set) var pointerPosition = CGPoint.zero
    private var pictureSize = CGSize.zero
    private var pictureTurns = PictureTurns.none
    private(set) var pointerButtons: PointerButtons = []
    private var pointerModifiers: Set<ModifierKey> = []
    private var lastPointerMoveTime: TimeInterval = 0
    private var pointerMovedSinceTouch = false
    private var displayArea = CGSize.zero
    private var displayScale: CGFloat = 2
    private var screenSize = CGSize.zero
    private var screenRefreshRate = 60
    private let virtualDisplayRequests = VirtualDisplayRequests()
    private(set) var sceneInBackground = false
    private var activeWhenBackgrounded = false
    private var backgroundTask = UIBackgroundTaskIdentifier.invalid
    private var returnedFromBackground: TimeInterval?
    private var resumeCheck: Task<Void, Never>?
    private var restartsHighPerformanceOnReturn = false
    private var hasConnected = false
    private var microphone: MicrophoneSender?
    private var reconnectAttempts = 0
    private var reconnectTask: Task<Void, Never>?
    private var chosenDisplay: DisplaySelection?
    private var restoresDisplay = false
    private var assistCursorShown = false
    private var hasStartedHighPerformance = false
    private var highPerformanceStartPending = false
    private var awaitsVirtualDisplay = false {
        didSet { pipeline.setHoldsVideo(awaitsVirtualDisplay) }
    }
    private var awaitsResolutionChange = false {
        didSet {
            if awaitsResolutionChange != oldValue { pipeline.setWaitsForDisplay(awaitsResolutionChange) }
        }
    }
    private var highPerformanceAwaitsDisplay = false
    @Published private(set) var hasPicture = false
    private var isScrollGliding = false

    private var isRestoring = false
    private var server: ServerAuthentication?
    private var probeTask: Task<Void, Never>?
    private var passwordUpdate = PasswordUpdate.keep
    private var usedSavedPassword = false
    private var device: DeviceInfo?
    private var deviceType: DeviceType? {
        didSet {
            isTouchScreen = deviceType?.isTouchScreen ?? false
            isPhone = deviceType == .iPhone
        }
    }
    @Published private(set) var isTouchScreen = false
    @Published private var isPhone = false

    init(route: SessionRoute) {
        self.route = route
        title = route.name
        computerID = route.computerID
        server = route.server
        device = route.model.map { DeviceInfo(model: $0, enclosureColor: "") }
        serverType = route.serverType
        deviceType = route.deviceType ?? route.model.flatMap(DeviceType.init(model:))
        isTouchScreen = deviceType?.isTouchScreen ?? false
        isPhone = deviceType == .iPhone
        virtualDisplayRequests.onIdle = { [weak self] in self?.virtualDisplayDidSettle() }
        miceChanges = PointerLock.mice.objectWillChange.sink { [weak self] in self?.objectWillChange.send() }
        pipeline.setShownHandler { [weak self] in
            DispatchQueue.main.async {
                guard let self else { return }
                let shown = self.pipeline.currentPicture
                self.handle(picture: shown.picture, width: shown.width, height: shown.height, turns: shown.turns)
            }
        }
    }


    func start() {
        guard session == nil, probeTask == nil else { return }
        let stored = computerID.flatMap { store.stored($0) }
        if let stored {
            restore(stored.memory)
            device = device ?? stored.device
            serverType = stored.serverType
            deviceType = stored.deviceType
            keyboardSources = stored.keyboardSources.map(MacInputSource.init(id:))
        }
        if !decidedClipboardSharing {
            decidedClipboardSharing = true
            clipboardSharing = clipboardSharing && ClipboardAccess.shared.allowsReading
        }
        networkConfiguration = stored?.networkConfiguration ?? NetworkConfiguration()
        let username = route.username ?? stored?.username ?? ""
        let savedPassword = stored.flatMap { $0.username == username ? $0.password : nil } ?? ""
        passwordUpdate = .keep
        usedSavedPassword = false
        if let password = route.password, !password.isEmpty {
            connect(with: SessionCredentials(loginType: .account, username: username, password: password))
        } else if !savedPassword.isEmpty {
            usedSavedPassword = true
            connect(with: SessionCredentials(loginType: .account, username: username, password: savedPassword))
        } else {
            askForCredentials(username: username)
        }
    }

    private func askForCredentials(username: String, failure: SessionFailure? = nil) {
        phase = .connecting
        if let server {
            presentCredentialPrompt(for: server, username: username, failure: failure)
            return
        }
        probeTask = Task { [weak self, address = route.address] in
            let result = await ServerProber.probe(address)
            guard let self, !Task.isCancelled else { return }
            probeTask = nil
            guard phase == .connecting else { return }
            switch result {
            case .success(let server):
                self.server = server
                presentCredentialPrompt(for: server, username: username, failure: failure)
            case .failure(let failure):
                phase = .failed(failure)
            }
        }
    }

    private func presentCredentialPrompt(for server: ServerAuthentication, username: String, failure: SessionFailure?) {
        let form: CredentialPrompt.Form
        if let credentials = server.openCredentials, failure == nil {
            connect(with: credentials)
            return
        } else if server.account || server.requestPermission {
            form = .account(allowsRequest: server.requestPermission)
        } else if server.vncPassword {
            form = .password
        } else {
            phase = .failed(SessionFailure(.unsupported, message: "the server offers no sign-in method this app supports"))
            return
        }
        credentialPrompt = CredentialPrompt(
            form: form, username: username, remembers: usedSavedPassword, failed: failure != nil, errorMessage: failure?.message ?? "")
    }

    func submitCredentials(_ credentials: SessionCredentials, remember: Bool) {
        credentialPrompt = nil
        switch credentials.loginType {
        case .askForPermission, .none: passwordUpdate = .keep
        case .account, .vncPassword: passwordUpdate = remember ? .save(credentials.password) : .delete
        }
        usedSavedPassword = false
        connect(with: credentials)
    }

    func cancelCredentials() {
        credentialPrompt = nil
        close()
    }

    private func restore(_ memory: SessionMemory) {
        isRestoring = true
        quality = memory.quality
        mode = memory.observes ? .observe : .control
        clipboardSharing = memory.sharesClipboard
        usesDynamicResolution = memory.usesDynamicResolution
        resolutionScale = memory.resolutionScale
        chosenDisplay = memory.display
        hdrEnabled = memory.usesHDR && DecoderCapabilities.current.supportsHDR
        remoteAudio = memory.remoteAudio
        isRestoring = false
    }

    private var memory: SessionMemory {
        SessionMemory(
            quality: quality,
            observes: mode == .observe,
            sharesClipboard: clipboardSharing,
            usesDynamicResolution: usesDynamicResolution,
            resolutionScale: resolutionScale,
            display: chosenDisplay,
            usesHDR: hdrEnabled,
            remoteAudio: remoteAudio
        )
    }

    private func rememberState() {
        guard !isRestoring, let computerID else { return }
        save { try $0.remember(memory, for: computerID) }
    }

    private func captureThumbnail() {
        guard phase == .connected, let computerID, let thumbnail = pipeline.makeThumbnail() else { return }
        save { try $0.setThumbnail(thumbnail, for: computerID) }
    }

    private func recordConnection() {
        guard let credentials else { return }
        save { store in
            computerID = try store.record(
                ConnectionRecord(
                    computerID: computerID,
                    name: route.name,
                    address: route.address,
                    username: credentials.loginType == .account ? credentials.username : "",
                    password: passwordUpdate,
                    supportsHighPerformance: capabilities.contains(.highPerformance),
                    serverType: serverType ?? .vnc,
                    memory: memory,
                    device: device
                ))
            passwordUpdate = .keep
        }
        saveKeyboardSources()
    }

    private func save(_ body: (ComputerStore) throws -> Void) {
        do {
            try body(store)
        } catch {
            showError(String(localized: "Couldn’t Save “\(title)”"), error: error)
        }
    }

    private var canDecodeVideo: Bool {
        let decoder = DecoderCapabilities.current
        return decoder.supportsHEVC || decoder.supportsH264
    }

    var usesHighPerformance: Bool { serverType == .apple }

    private var mayUseHighPerformance: Bool { usesHighPerformance || (serverType == nil && canDecodeVideo) }

    private func settleServerType() {
        guard serverType == nil else { return }
        if info.isMac, capabilities.contains(.highPerformance), canDecodeVideo {
            serverType = .apple
            highPerformanceStartPending = true
        } else {
            serverType = .vnc
        }
    }

    fileprivate func handle(device: DeviceInfo) {
        self.device = device
        if let type = DeviceType(model: device.model) {
            deviceType = type
        }
        if let computerID {
            save { try $0.setDevice(device, for: computerID) }
        }
    }

    var touchScreenSize: CGSize? {
        guard let density = touchScreenDensity, info.framebufferWidth > 0, info.framebufferHeight > 0 else { return nil }
        return CGSize(width: Double(info.framebufferWidth) / density, height: Double(info.framebufferHeight) / density)
    }

    private var touchScreenDensity: Double? {
        guard isTouchScreen else { return nil }
        return isPhone && min(info.framebufferWidth, info.framebufferHeight) >= 1000 ? 3 : 2
    }

    var cursorPixelsPerPoint: Double {
        guard let density = touchScreenDensity else { return 1 }
        return density / info.framebufferPixelsPerCursorPixel
    }

    private func connect(with credentials: SessionCredentials) {
        self.credentials = credentials
        if let computerID, let computer = store.computer(computerID) {
            networkConfiguration = computer.networkConfiguration
        }
        hdrEnabled = hdrEnabled && networkSupportsHDR
        hasStartedHighPerformance = false
        highPerformanceStartPending = usesHighPerformance
        hasPicture = false
        pipeline.setExpectsVideo(mayUseHighPerformance)
        pipeline.setSchedulesPresentation(networkConfiguration.schedulesPresentation)
        let configuration = SessionConfiguration(
            address: route.address,
            displayName: route.name,
            clientName: credentials.username.isEmpty ? UIDevice.current.name : credentials.username,
            credentials: credentials,
            quality: quality,
            startsInObserveMode: mode == .observe,
            sharesClipboard: clipboardSharing,
            remoteAudio: remoteAudio,
            updateRate: screenRefreshRate,
            networkConfiguration: networkConfiguration
        )
        let session = LibsharingRemoteSession(configuration: configuration)
        let sink = SessionEventSink(controller: self, pipeline: pipeline)
        sink.session = session
        if sceneInBackground {
            sink.setSceneInBackground(true)
        }
        self.session = session
        self.sink = sink
        virtualDisplayRequests.reset { [weak self, weak session] mode in
            guard let self else { return }
            session?.setVirtualDisplay(
                mode, largestSide: VirtualDisplayMode.largestSide(onScreen: screenSize, screenScale: displayScale))
        }
        restoresDisplay = chosenDisplay != nil
        awaitsResolutionChange = false
        pipeline.holdPicture()
        phase = .connecting
        session.start(delegate: sink)
        // ScreenSharing 776.27 configures virtual displays before SetEncodings in stConfigureSession:
        // macOS 27.0.1 (26A434), arm64e UUID EC0B2C20-F816-3FCE-822D-F9403C09EB50,
        // calls at 0x1cc060514 and 0x1cc060590.
        if hdrEnabled {
            session.setHDR(true)
        }
        awaitsVirtualDisplay = usesDynamicResolution
        virtualDisplayRequests.open()
        highPerformanceAwaitsDisplay = mayUseHighPerformance
        requestVirtualDisplay()
        if !usesDynamicResolution {
            startHighPerformanceWithConnection()
        }
    }

    private func startHighPerformanceWithConnection() {
        guard highPerformanceAwaitsDisplay, virtualDisplayRequests.isOpening else { return }
        highPerformanceAwaitsDisplay = false
        session?.startHighPerformanceWithConnection()
    }

    func disconnect() {
        releaseAllKeys()
        teardown()
        phase = .disconnected
        endBackgroundTask()
    }

    func close() {
        isClosing = true
        disconnect()
        onClose()
    }

    func retry() {
        teardown()
        reconnectAttempts = 0
        if case .failed(let failure) = phase, failure.kind == .authenticationFailed {
            askForCredentials(username: credentials?.username ?? "", failure: failure)
            return
        }
        if let credentials {
            connect(with: credentials)
        } else {
            start()
        }
    }

    private func teardown() {
        captureThumbnail()
        assistCursorShown = false
        isScrollGliding = false
        reconnectTask?.cancel()
        reconnectTask = nil
        probeTask?.cancel()
        probeTask = nil
        resumeCheck?.cancel()
        resumeCheck = nil
        restartsHighPerformanceOnReturn = false
        microphone?.stop()
        microphone = nil
        sink?.detach()
        awaitsResolutionChange = false
        pipeline.holdPicture()
        session?.stop()
        session = nil
        sink = nil
        virtualDisplayRequests.reset(sender: nil)
        awaitsVirtualDisplay = false
        highPerformanceAwaitsDisplay = false
        abandonTransfers()
        updateIdleTimer()
    }


    func sceneDidEnterBackground() {
        guard !sceneInBackground else { return }
        sceneInBackground = true
        activeWhenBackgrounded = phase.isActive
        captureThumbnail()
        endResumeCheck()
        releaseAllKeys()
        stopScrollMomentum()
        updateMicrophone()
        sink?.setSceneInBackground(true)
        screen?.backgroundStateDidChange()
        if phase == .connected {
            beginBackgroundTask()
        }
    }

    func sceneDidBecomeActive() {
        guard sceneInBackground else {
            sendChangedClipboard()
            return
        }
        sceneInBackground = false
        returnedFromBackground = ProcessInfo.processInfo.systemUptime
        endBackgroundTask()
        sink?.setSceneInBackground(false)
        screen?.backgroundStateDidChange()
        updateMicrophone()
        let wasActive = activeWhenBackgrounded
        activeWhenBackgrounded = false
        let restartsHighPerformance = restartsHighPerformanceOnReturn
        restartsHighPerformanceOnReturn = false
        if phase == .connected {
            checkConnectionAfterBackground()
            if resumeCheck == nil {
                sendChangedClipboard()
            }
            if restartsHighPerformance {
                restartHighPerformance()
            }
        } else if wasActive, brokeInBackground {
            reconnectNow()
        }
    }

    private func beginBackgroundTask() {
        guard backgroundTask == .invalid else { return }
        backgroundTask = UIApplication.shared.beginBackgroundTask(withName: "Screen Sharing session") { [self] in
            endBackgroundTask()
        }
    }

    private func endBackgroundTask() {
        guard backgroundTask != .invalid else { return }
        UIApplication.shared.endBackgroundTask(backgroundTask)
        backgroundTask = .invalid
    }

    private static let resumeWindow: TimeInterval = 5

    private var returnedMomentsAgo: Bool {
        guard !sceneInBackground, let returnedFromBackground else { return false }
        return ProcessInfo.processInfo.systemUptime - returnedFromBackground < Self.resumeWindow
    }

    private var brokeInBackground: Bool {
        guard hasConnected, credentials != nil else { return false }
        switch phase {
        case .reconnecting:
            return reconnectAttempts > 0
        case .failed(let failure):
            return failure.kind == .connectionLost || failure.kind == .unreachable || failure.kind == .hostNotFound
        default:
            return false
        }
    }

    private func reconnectNow() {
        guard let credentials else { return }
        releaseAllKeys()
        teardown()
        reconnectAttempts = 0
        connect(with: credentials)
        phase = .reconnecting(attempt: 1)
    }

    private func checkConnectionAfterBackground() {
        guard let session, let sink else { return }
        if isVideo {
            session.requestKeyframe()
        } else {
            session.requestRefresh()
        }
        guard hasPicture else { return }
        sink.watchForLife()
        resumeCheck?.cancel()
        resumeCheck = Task { [weak self] in
            try? await Task.sleep(seconds: Self.resumeWindow)
            guard let self, !Task.isCancelled, self.phase == .connected else { return }
            self.resumeCheck = nil
            self.reconnectNow()
        }
    }

    fileprivate func endResumeCheck() {
        resumeCheck?.cancel()
        resumeCheck = nil
    }

    fileprivate func connectionResumed() {
        guard resumeCheck != nil else { return }
        endResumeCheck()
        sendChangedClipboard()
    }

    private func restartHighPerformance() {
        guard usesHighPerformance, phase == .connected, !isVideo else { return }
        if needsReconnectToRestartStream {
            reconnectToRestartStream()
        } else {
            highPerformanceStartPending = true
            startHighPerformanceIfDisplayReady()
        }
    }


    private static let reconnectLimit = 6

    fileprivate func handle(phase newPhase: SessionPhase) {
        endResumeCheck()
        var newPhase = newPhase
        var reconnecting = false
        if case .reconnecting = phase { reconnecting = true }
        if reconnecting, newPhase == .connecting || newPhase == .authenticating { return }
        if case .failed(let failure) = newPhase {
            let unreachable = failure.kind == .unreachable || failure.kind == .hostNotFound
            let retryable = failure.kind == .connectionLost || (reconnecting && unreachable)
            if retryable, hasConnected, reconnectAttempts < Self.reconnectLimit, let credentials {
                reconnectAttempts += 1
                let attempt = reconnectAttempts
                teardown()
                phase = .reconnecting(attempt: attempt)
                reconnectTask?.cancel()
                let delay: TimeInterval = attempt == 1 && returnedMomentsAgo ? 0 : TimeInterval(min(1 << (attempt - 1), 8))
                reconnectTask = Task { [weak self] in
                    try? await Task.sleep(seconds: delay)
                    guard let self, !Task.isCancelled, case .reconnecting = self.phase else { return }
                    self.connect(with: credentials)
                    self.phase = .reconnecting(attempt: attempt)
                }
                return
            }
            if reconnecting, unreachable {
                newPhase = .failed(SessionFailure(.connectionLost, message: failure.message))
            }
        }
        phase = newPhase
        switch newPhase {
        case .connected:
            reconnectAttempts = 0
            settleServerType()
            if !info.isMac {
                deviceType = nil
            } else if deviceType == nil {
                deviceType = .mac
            }
            if !hasConnected || passwordUpdate.isPending {
                hasConnected = true
                recordConnection()
            }
            pipeline.setExpectsVideo(usesHighPerformance)
            let sendsClipboard = sendChangedClipboard { [weak self] sent in
                guard let self, !sent else { return }
                session?.setClipboardSharing(clipboardSharing)
            }
            if !sendsClipboard {
                session?.setClipboardSharing(clipboardSharing)
            }
            if hdrEnabled {
                session?.setHDR(true)
            }
            let offersVirtualDisplay = capabilities.contains(.virtualDisplay)
            virtualDisplayRequests.connectionDidOpen(offersVirtualDisplay: offersVirtualDisplay)
            if !offersVirtualDisplay {
                awaitsVirtualDisplay = false
            }
            requestVirtualDisplay()
            startHighPerformanceIfDisplayReady()
        case .failed(let failure):
            releaseAllKeys()
            if failure.kind == .authenticationFailed, credentials?.loginType == .account || credentials?.loginType == .vncPassword {
                retry()
            }
        case .disconnected:
            releaseAllKeys()
        default:
            break
        }
        updateIdleTimer()
    }

    private func startHighPerformanceIfDisplayReady() {
        guard highPerformanceStartPending, usesHighPerformance, phase == .connected, virtualDisplayRequests.isIdle
        else { return }
        highPerformanceStartPending = false
        session?.startHighPerformance()
    }

    private func virtualDisplayDidSettle() {
        awaitsVirtualDisplay = false
        awaitsResolutionChange = false
        startHighPerformanceIfDisplayReady()
    }

    fileprivate func handle(screenWidth: Int, height: Int, video: Bool) {
        virtualDisplayRequests.framebufferDidChange(width: screenWidth, height: height)
        if isVideo != video {
            isVideo = video
            updateMicrophone()
        }
        if video {
            hasStartedHighPerformance = true
        }
    }

    private func handle(picture: ScreenPicture, width: Int, height: Int, turns: PictureTurns) {
        if self.picture != picture {
            self.picture = picture
        }
        if picture == .current, !hasPicture {
            hasPicture = true
        }
        let newPictureSize = CGSize(width: width, height: height)
        guard newPictureSize != pictureSize || turns != pictureTurns else { return }
        let serverPointer = newPictureSize == pictureSize ? serverPoint(pointerPosition) : nil
        pictureSize = newPictureSize
        pictureTurns = turns
        let size = turns.shownSize(of: newPictureSize)
        imageSize = size
        if let serverPointer {
            pointerPosition = shownPoint(serverPointer)
            pipeline.setCursorPosition(pointerPosition, visible: remotePointerVisible)
        } else if pointerPosition == .zero || pointerPosition.x >= size.width || pointerPosition.y >= size.height {
            pointerPosition = CGPoint(x: size.width / 2, y: size.height / 2)
            pipeline.setCursorPosition(pointerPosition, visible: remotePointerVisible)
        }
        screen?.imageSizeDidChange()
    }

    private func serverPoint(_ point: CGPoint) -> CGPoint {
        pictureTurns.picturePoint(point, inPictureOf: pictureSize)
    }

    private func shownPoint(_ point: CGPoint) -> CGPoint {
        pictureTurns.shownPoint(point, inPictureOf: pictureSize)
    }

    fileprivate func handle(displays: [RemoteDisplay], selection: DisplaySelection) {
        self.displays = displays
        displaySelection = selection
        let screen = pipeline.screenSize
        virtualDisplayRequests.displaysDidChange(displays, framebufferWidth: screen.width, framebufferHeight: screen.height)
        guard restoresDisplay, let chosen = chosenDisplay else { return }
        restoresDisplay = false
        let available =
            switch chosen {
            case .all: displays.count > 1
            case .display(let id): displays.contains { $0.id == id && !$0.isVirtual }
            }
        if available, chosen != selection {
            displaySelection = chosen
            session?.selectDisplay(chosen)
        }
    }

    fileprivate func handle(mode: SessionMode) {
        self.mode = mode
        if mode == .observe {
            releaseAllKeys()
        }
        updateAssistCursor()
    }

    fileprivate func handle(capabilities: SessionCapabilities) {
        self.capabilities = capabilities
    }

    fileprivate func handle(info: SessionInfo) {
        var updated = info
        updated.keyboardSource = self.info.keyboardSource ?? info.keyboardSource
        updated.secureInput = self.info.secureInput
        if self.info != updated {
            self.info = updated
        }
    }

    fileprivate func handleVideoFailure(_ failure: MediaFailure) {
        if usesHighPerformance, failure.kind == .silent || failure.kind == .network, sceneInBackground || returnedMomentsAgo {
            hasStartedHighPerformance = true
            if sceneInBackground {
                restartsHighPerformanceOnReturn = true
            } else {
                restartHighPerformance()
            }
            return
        }
        guard usesHighPerformance else { return }
        failHighPerformance(failure)
    }

    private func failHighPerformance(_ failure: MediaFailure) {
        releaseAllKeys()
        teardown()
        phase = .failed(SessionFailure(.highPerformance(failure.kind), message: failure.message))
        updateIdleTimer()
    }

    fileprivate func handle(clipboard content: ClipboardContent) {
        guard content != lastRemoteClipboard else { return }
        lastRemoteClipboard = content
        guard !content.isEmpty else { return }
        let items: [[String: Any]] = content.map { item in
            var converted: [String: Any] = [:]
            for (type, data) in item {
                if type == UTType.utf8PlainText.identifier, let text = String(data: data, encoding: .utf8) {
                    converted[type] = text
                } else {
                    converted[type] = data
                }
            }
            return converted
        }
        UIPasteboard.general.setItems(items)
        ClipboardAccess.shared.noteOwnWrite()
        syncedClipboardChangeCount = UIPasteboard.general.changeCount
        lastRemoteText = content.lazy.compactMap { $0[UTType.utf8PlainText.identifier].flatMap { String(data: $0, encoding: .utf8) } }.first
    }

    fileprivate func handle(cursorMovedTo point: CGPoint) {
        if isControlling, CACurrentMediaTime() - lastPointerMoveTime < 0.3 { return }
        pointerPosition = shownPoint(point)
        pipeline.setCursorPosition(pointerPosition, visible: remotePointerVisible)
    }

    fileprivate func handle(keyboardSource source: String?, secureInput: Bool) {
        info.keyboardSource = source
        info.secureInput = secureInput
        rememberKeyboardSource(source)
    }

    fileprivate func handle(event: SessionEvent) {
        switch event {
        case .bell:
            UINotificationFeedbackGenerator().notificationOccurred(.warning)
        case .displaysAsleep, .displaysAwake, .userSessionChanged, .sessionEnding, .controlAllowed, .busy:
            break
        case .pointerVisible(let visible):
            if remotePointerVisible != visible {
                remotePointerVisible = visible
            }
            pipeline.setCursorPosition(pointerPosition, visible: visible)
        }
    }


    static var downloadsFolder: URL {
        FileManager.default.urls(for: .documentDirectory, in: .userDomainMask)[0]
    }

    func uploadFiles(_ urls: [URL], to remoteFolder: String, temporary: Bool = false) {
        guard let session else {
            for url in urls {
                reportSendFailure(url.lastPathComponent, error: MessageError("not connected"))
                if temporary {
                    try? FileManager.default.removeItem(at: url)
                }
            }
            return
        }
        for url in urls {
            let scoped = !temporary && url.startAccessingSecurityScopedResource()
            do {
                let id = try session.uploadFile(url, remoteFolder: remoteFolder)
                transfers.append(
                    FileTransfer(
                        id: id, direction: .upload, name: url.lastPathComponent,
                        scopedURLs: scoped ? [url] : [], temporaryURLs: temporary ? [url] : []
                    ))
            } catch {
                if scoped { url.stopAccessingSecurityScopedResource() }
                if temporary { try? FileManager.default.removeItem(at: url) }
                reportSendFailure(url.lastPathComponent, error: error)
            }
        }
    }

    func downloadFile(_ remotePath: String) {
        let destination = Self.downloadsFolder
        do {
            guard let session else { throw MessageError("not connected") }
            let id = try session.downloadFile(remotePath, localFolder: destination)
            transfers.append(
                FileTransfer(
                    id: id, direction: .download, name: (remotePath as NSString).lastPathComponent, localFolder: destination
                ))
        } catch {
            showError(String(localized: "Couldn’t Get “\((remotePath as NSString).lastPathComponent)”"), error: error)
        }
    }

    func cancelTransfer(_ transfer: FileTransfer) {
        session?.cancelTransfer(transfer.id)
    }

    func clearFinishedTransfers() {
        transfers.removeAll { !$0.isActive }
    }

    private func abandonTransfers() {
        for index in transfers.indices where transfers[index].isActive {
            transfers[index].state = .failed(FileTransferError(.connection))
            transfers[index].scopedURLs.forEach { $0.stopAccessingSecurityScopedResource() }
            transfers[index].scopedURLs = []
            transfers[index].temporaryURLs.forEach { try? FileManager.default.removeItem(at: $0) }
            transfers[index].temporaryURLs = []
        }
    }

    private func update(_ id: Int64, _ body: (inout FileTransfer) -> Void) {
        guard let index = transfers.firstIndex(where: { $0.id == id }) else { return }
        body(&transfers[index])
    }

    fileprivate func handleTransferProgress(id: Int64, transferred: Int64, total: Int64, item: String?) {
        update(id) { transfer in
            transfer.transferred = transferred
            transfer.total = total
            transfer.currentItem = item
        }
    }

    fileprivate func handleTransferFinished(id: Int64, name: String?, error: FileTransferError?) {
        update(id) { transfer in
            if let name, !name.isEmpty { transfer.name = name }
            switch error {
            case nil:
                transfer.state = .finished
                transfer.transferred = max(transfer.transferred, transfer.total)
            case let error? where error.kind == .cancelled:
                transfer.state = .cancelled
            case let error?:
                transfer.state = .failed(error)
            }
            transfer.scopedURLs.forEach { $0.stopAccessingSecurityScopedResource() }
            transfer.scopedURLs = []
            transfer.temporaryURLs.forEach { try? FileManager.default.removeItem(at: $0) }
            transfer.temporaryURLs = []
        }
        guard let transfer = transfers.first(where: { $0.id == id }) else { return }
        if case .failed(let error) = transfer.state {
            showError(
                transfer.direction == .upload
                    ? String(localized: "Couldn’t Send “\(transfer.name)”") : String(localized: "Couldn’t Get “\(transfer.name)”"),
                error: error)
        }
    }

    func answerSessionChoice(_ choice: SessionChoice) {
        guard case .choosingSession(let prompt) = phase else { return }
        phase = choice == .cancel ? .disconnected : .authenticating
        session?.answerSessionChoice(prompt, choice: choice)
    }


    var isControlling: Bool { mode.controls && phase == .connected }
    var canTransferFiles: Bool { capabilities.contains(.fileTransfer) && phase == .connected }

    var loginName: String? {
        guard let username = credentials?.username, !username.isEmpty else { return nil }
        return username
    }

    func setMode(_ newMode: SessionMode) {
        guard newMode != mode else { return }
        if newMode == .observe {
            releaseAllKeys()
        }
        mode = newMode
        session?.setMode(newMode)
        updateAssistCursor()
        rememberState()
    }

    func selectDisplay(_ selection: DisplaySelection) {
        displaySelection = selection
        chosenDisplay = selection
        session?.selectDisplay(selection)
        rememberState()
    }

    func setQuality(_ newQuality: SessionQuality) {
        guard newQuality != quality else { return }
        quality = newQuality
        rememberState()
        session?.setQuality(newQuality)
    }

    private var needsReconnectToRestartStream: Bool {
        hasStartedHighPerformance && !usesDynamicResolution && phase == .connected
    }

    private func reconnectToRestartStream() {
        guard let credentials else { return }
        releaseAllKeys()
        teardown()
        connect(with: credentials)
        phase = .reconnecting(attempt: 1)
    }

    var networkSupportsHDR: Bool {
        networkConfiguration.chroma.pixelFormats(available: DecoderCapabilities.current.pixelFormats) & (1 << 5) != 0
    }

    var canUseHDR: Bool {
        usesHighPerformance && capabilities.contains(.highPerformance) && usesDynamicResolution
            && displaySupportsHDR && networkSupportsHDR
    }

    func setHDR(_ enabled: Bool) {
        guard !enabled || canUseHDR else { return }
        hdrEnabled = enabled
        session?.setHDR(enabled)
        rememberState()
    }

    fileprivate func hdrDecodingFailed(_ error: any Error) {
        guard hdrEnabled else { return }
        hdrEnabled = false
        session?.setHDR(false)
        rememberState()
        showError(String(localized: "This Device Can’t Show HDR"), error: error)
    }

    fileprivate func videoDecodingFailed(_ error: any Error) {
        guard usesHighPerformance else { return }
        failHighPerformance(MediaFailure(.undecodable, message: error.localizedDescription))
    }

    func setRemoteAudio(_ audio: RemoteAudio) {
        let changed = audio != remoteAudio
        remoteAudio = audio
        rememberState()
        if changed, isVideo, needsReconnectToRestartStream {
            reconnectToRestartStream()
            return
        }
        session?.setRemoteAudio(audio)
        updateMicrophone()
    }

    private func updateMicrophone() {
        let wanted = isVideo && remoteAudio == .toMac && phase == .connected && !sceneInBackground
        if !wanted {
            microphone?.stop()
            microphone = nil
            return
        }
        guard microphone == nil else { return }
        Task {
            let granted: Bool
            if #available(iOS 17, *) {
                granted = await AVAudioApplication.requestRecordPermission()
            } else {
                granted = await withCheckedContinuation { continuation in
                    AVAudioSession.sharedInstance().requestRecordPermission { @Sendable granted in
                        continuation.resume(returning: granted)
                    }
                }
            }
            guard granted else {
                alert = AlertState(String(localized: "Can’t Use the Microphone"), errorMessage: String(localized: "Microphone access is turned off in Settings."))
                return
            }
            guard microphone == nil, isVideo, remoteAudio == .toMac, !sceneInBackground else { return }
            let session = self.session
            do {
                let sender = try MicrophoneSender(send: { data in session?.sendMicrophoneAudio(data) })
                try sender.start()
                microphone = sender
            } catch {
                showError(String(localized: "Can’t Use the Microphone"), error: error)
            }
        }
    }

    func setClipboardSharing(_ enabled: Bool) {
        clipboardSharing = enabled
        rememberState()
        guard enabled, capabilities.contains(.clipboard), !readingClipboard else {
            session?.setClipboardSharing(enabled)
            return
        }
        guard !sendChangedClipboard(completion: { [weak self] _ in self?.shareClipboardAfterReading() }) else { return }
        readingClipboard = true
        Task {
            let explanation = await ClipboardAccess.shared.check()
            readingClipboard = false
            if let explanation {
                alert = explanation
            }
            shareClipboardAfterReading()
        }
    }

    private func shareClipboardAfterReading() {
        session?.setClipboardSharing(clipboardSharing)
    }

    func setUsesDynamicResolution(_ uses: Bool) {
        guard uses != usesDynamicResolution else { return }
        usesDynamicResolution = uses
        rememberState()
        if !uses, phase == .connected {
            reconnectNow()
        } else {
            requestVirtualDisplay(holdingPicture: true)
        }
    }

    func setResolutionScale(_ scale: Double) {
        guard scale != resolutionScale else { return }
        resolutionScale = scale
        rememberState()
        requestVirtualDisplay(holdingPicture: true)
    }

    var pictureMatchesWindow: Bool {
        !isTouchScreen && usesDynamicResolution && capabilities.contains(.virtualDisplay)
    }

    func displayAreaDidChange(_ area: CGSize, scale: CGFloat, screen: CGSize) {
        guard area.width > 0, area.height > 0 else { return }
        displayArea = area
        displayScale = scale
        screenSize = screen
        requestVirtualDisplay()
    }

    func screenRefreshRateDidChange(_ rate: Int) {
        guard rate > 0, rate != screenRefreshRate else { return }
        screenRefreshRate = rate
        session?.setUpdateRate(rate)
    }

    private func requestVirtualDisplay(holdingPicture: Bool = false) {
        guard virtualDisplayRequests.isOpening || phase == .connected && capabilities.contains(.virtualDisplay),
            usesDynamicResolution,
            let mode = VirtualDisplayMode(matching: displayArea, screenScale: displayScale, resolutionScale: resolutionScale)
        else { return }
        virtualDisplayRequests.request(mode)
        if holdingPicture, phase == .connected, !virtualDisplayRequests.isIdle {
            awaitsResolutionChange = true
        }
        startHighPerformanceWithConnection()
    }

    func sendClipboardToMac() {
        let content = Self.localClipboard()
        guard !content.isEmpty else { return }
        sendClipboard(content)
    }

    @discardableResult
    func sendChangedClipboard(completion: @escaping (Bool) -> Void = { _ in }) -> Bool {
        let changeCount = UIPasteboard.general.changeCount
        guard clipboardSharing, phase == .connected, capabilities.contains(.clipboard), !readingClipboard,
            changeCount != syncedClipboardChangeCount
        else { return false }
        syncedClipboardChangeCount = changeCount
        readingClipboard = true
        Task {
            let (content, explanation) = await ClipboardAccess.shared.read()
            readingClipboard = false
            if let explanation {
                alert = explanation
            }
            completion(!content.isEmpty && phase == .connected && sendClipboard(content, changeCount: changeCount))
        }
        return true
    }

    func pasteFromDevice() {
        let content = Self.localClipboard()
        guard !content.isEmpty, sendClipboard(content) else { return }
        textCursorMayHaveMoved()
        let commandHeld =
            lockedModifiers.contains(.command) || pressedModifiers.contains(.command)
            || pressedKeys.values.contains { $0.keysym == Keysym.commandLeft || $0.keysym == Keysym.commandRight }
        tap(MacKey(keysym: Keysym.character("v")), modifiers: commandHeld ? [] : [.command])
    }

    @discardableResult
    private func sendClipboard(_ content: ClipboardContent, changeCount: Int = UIPasteboard.general.changeCount) -> Bool {
        do {
            guard let session else { throw MessageError("not connected") }
            try session.sendClipboard(content)
            syncedClipboardChangeCount = changeCount
            lastRemoteClipboard = content
            return true
        } catch {
            showError(String(localized: "Couldn’t Send the Clipboard"), error: error)
            return false
        }
    }

    private static func localClipboard() -> ClipboardContent {
        ClipboardAccess.readClipboard().content
    }

    func getClipboardFromMac() {
        lastRemoteClipboard = nil
        do {
            guard let session else { throw MessageError("not connected") }
            try session.requestClipboard()
        } catch {
            showError(String(localized: "Couldn’t Receive the Clipboard"), error: error)
        }
    }

    fileprivate func audioFailed(_ error: any Error) {
        showError(String(localized: "Can’t Play the Mac’s Sound"), error: error)
    }

    func sendChord(_ chord: KeyChord) {
        guard isControlling else { return }
        textCursorMayHaveMoved()
        tap(chord.key, modifiers: Set(chord.modifiers))
    }

    func setScale(_ scale: ScaleMode) {
        screen?.applyScale(scale)
    }

    func setKeyboardVisible(_ visible: Bool) {
        screen?.setKeyboardVisible(visible)
    }

    func toggleKeyboard() {
        setKeyboardVisible(!isKeyboardVisible)
    }

    func keyboardVisibilityDidChange(_ visible: Bool) {
        isKeyboardVisible = visible
    }

    func setUsesHardwareKeyboard(_ uses: Bool) {
        if uses != usesHardwareKeyboard { usesHardwareKeyboard = uses }
    }

    func composingTextDidChange(_ text: String?) {
        if text != composingText { composingText = text }
    }

    func toggleToolbar() {
        withAnimation(.smoothCompat(duration: 0.25)) { isToolbarVisible.toggle() }
    }

    var canLockPointer: Bool {
        mode.controls && isFullScreen
    }

    func setPointerLock(_ on: Bool) {
        guard on != isPointerLockOn else { return }
        isPointerLockOn = on
        pointerLockHasLocked = false
        if on, isToolbarVisible { toggleToolbar() }
    }

    func setZoomMode(_ on: Bool) {
        guard on != isZoomModeOn else { return }
        isZoomModeOn = on
        if on, isToolbarVisible { toggleToolbar() }
    }

    var pointerLockCanResume: Bool {
        guard pointerLockHasLocked, !isToolbarDocked else { return true }
        guard let location = lastSystemPointerLocation else { return false }
        return [toolbarHandleFrame, toolbarBarFrame].contains { $0?.insetBy(dx: -16, dy: -16).contains(location) == true }
    }

    private func showError(_ failure: String, error: (any Error)? = nil) {
        alert = AlertState(failure, error: error)
    }

    func reportSendFailure(_ name: String, error: (any Error)?) {
        showError(String(localized: "Couldn’t Send “\(name)”"), error: error)
    }


    func movePointer(to point: CGPoint) {
        pointerMovedSinceTouch = true
        let clamped = clampToImage(point)
        pointerPosition = clamped
        lastPointerMoveTime = CACurrentMediaTime()
        pipeline.setCursorPosition(clamped, visible: true)
        guard isControlling else {
            updateAssistCursor()
            return
        }
        sendPointer(buttons: pointerButtons)
    }

    var touchesDirectly: Bool {
        isTouchScreen && isControlling
    }

    var hidesPointerForTouch: Bool {
        isTouchScreen && !pointerMovedSinceTouch
    }

    func moveTouch(to point: CGPoint) {
        pointerMovedSinceTouch = false
        let clamped = clampToImage(point)
        pointerPosition = clamped
        lastPointerMoveTime = CACurrentMediaTime()
        pipeline.setCursorPosition(clamped, visible: true)
        guard isControlling, !pointerButtons.isEmpty else { return }
        sendPointer(buttons: pointerButtons)
    }

    func pointerLeftScreen() {
        guard assistCursorShown else { return }
        assistCursorShown = false
        session?.hideAssistCursor()
    }

    private func updateAssistCursor() {
        let wanted = capabilities.contains(.assistCursor) && !mode.controls && phase == .connected
        if wanted {
            assistCursorShown = true
            let point = serverPoint(pointerPosition)
            session?.setAssistCursor(x: Int(point.x), y: Int(point.y), pointer: .highlight, pressed: !pointerButtons.isEmpty)
        } else {
            pointerLeftScreen()
        }
    }

    func setButtons(_ buttons: PointerButtons) {
        pointerButtons = buttons
        guard isControlling else {
            updateAssistCursor()
            return
        }
        sendPointer(buttons: buttons)
    }

    private func sendPointer(buttons: PointerButtons) {
        let point = serverPoint(pointerPosition)
        session?.sendPointer(x: Int(point.x.rounded(.down)), y: Int(point.y.rounded(.down)), buttons: buttons)
    }

    func press(_ button: PointerButtons) {
        if isControlling { textCursorMayHaveMoved() }
        if pointerButtons.isEmpty, isControlling {
            pointerModifiers = stickyModifiers.subtracting(lockedModifiers).subtracting(pressedModifiers)
            stickyModifiers.removeAll()
            for modifier in ModifierKey.allCases where pointerModifiers.contains(modifier) { send(modifier.macKey, down: true) }
        }
        setButtons(pointerButtons.union(button))
    }

    func release(_ button: PointerButtons) {
        setButtons(pointerButtons.subtracting(button))
        guard pointerButtons.isEmpty, !pointerModifiers.isEmpty else { return }
        for modifier in ModifierKey.allCases.reversed() where pointerModifiers.contains(modifier) { send(modifier.macKey, down: false) }
        pointerModifiers = []
    }

    func setPressedButtons(_ buttons: PointerButtons) {
        let released = pointerButtons.subtracting(buttons)
        let pressed = buttons.subtracting(pointerButtons)
        if !released.isEmpty { release(released) }
        if !pressed.isEmpty { press(pressed) }
    }

    func click(_ button: PointerButtons, count: Int = 1) {
        for _ in 0..<count {
            press(button)
            release(button)
        }
    }

    var sendsGesturesToMac: Bool {
        capabilities.contains(.gestures) && isControlling
    }

    func magnify(at point: CGPoint, by magnification: Double, phase: GesturePhase) {
        guard sendsGesturesToMac else { return }
        let point = serverPoint(point)
        session?.sendMagnify(x: Int(point.x), y: Int(point.y), magnification: magnification, phase: phase)
    }

    func rotate(at point: CGPoint, degrees: Double, phase: GesturePhase) {
        guard sendsGesturesToMac else { return }
        let point = serverPoint(point)
        session?.sendRotate(x: Int(point.x), y: Int(point.y), degrees: degrees, phase: phase)
    }

    func scroll(byPixels delta: CGPoint, phase: GesturePhase) {
        guard sendsGesturesToMac else { return }
        isScrollGliding = false
        let point = serverPoint(pointerPosition)
        let delta = pictureTurns.pictureVector(delta)
        session?.sendScrollPhase(x: Int(point.x), y: Int(point.y), deltaX: Double(delta.x), deltaY: Double(delta.y), phase: phase)
    }

    func startScrollMomentum(pixelsPerSecond velocity: CGPoint) {
        guard sendsGesturesToMac, hypot(velocity.x, velocity.y) > 50 else { return }
        isScrollGliding = true
        let point = serverPoint(pointerPosition)
        let velocity = pictureTurns.pictureVector(velocity)
        session?.startScrollMomentum(x: Int(point.x), y: Int(point.y), velocityX: Double(velocity.x), velocityY: Double(velocity.y))
    }

    func stopScrollMomentum() {
        guard isScrollGliding else { return }
        isScrollGliding = false
        session?.stopScrollMomentum()
    }

    func canPerform(_ command: SystemCommand) -> Bool {
        capabilities.contains(command.capability) || command.fallbackChord != nil
    }

    func perform(_ command: SystemCommand) {
        guard isControlling else { return }
        textCursorMayHaveMoved()
        if capabilities.contains(command.capability) {
            session?.sendSystemCommand(command)
        } else if let chord = command.fallbackChord {
            sendChord(chord)
        }
    }

    func scroll(byPixels delta: CGPoint) {
        guard isControlling else { return }
        let point = serverPoint(pointerPosition)
        let delta = pictureTurns.pictureVector(delta)
        session?.sendScroll(x: Int(point.x), y: Int(point.y), deltaX: Double(delta.x), deltaY: Double(delta.y))
    }

    private func clampToImage(_ point: CGPoint) -> CGPoint {
        guard imageSize.width > 0, imageSize.height > 0 else { return point }
        return CGPoint(x: min(max(point.x, 0), imageSize.width - 1), y: min(max(point.y, 0), imageSize.height - 1))
    }


    func handlePress(_ key: UIKey, down: Bool) -> Bool {
        if down { setUsesHardwareKeyboard(true) }
        let usage = key.keyCode
        if usage == .keyboardCapsLock {
            guard isControlling, capabilities.contains(.keyCodes) else { return false }
            if down { capsLockPressed() } else { capsLockReleased() }
            return true
        }
        if !down {
            guard let pressed = pressedKeys.removeValue(forKey: usage) else {
                return isControlling && macKey(for: key) != nil
            }
            send(pressed, down: false)
            return true
        }
        guard isControlling else { return false }
        keyboardType = keyboardType.learning(from: usage)
        guard let mapped = macKey(for: key) else { return false }
        if !mapped.isModifier { textCursorMayHaveMoved() }
        if info.secureInput, !Keysym.typesInSecureInput(mapped.keysym) {
            showSecureInputNotice()
            return true
        }
        if let previous = pressedKeys.removeValue(forKey: usage) {
            send(previous, down: false)
        }
        let sticky = mapped.isModifier ? [] : stickyModifiers.subtracting(heldModifiers(from: key.modifierFlags))
        if !sticky.isEmpty {
            stickyModifiers.removeAll()
            tap(mapped, modifiers: sticky)
            return true
        }
        pressedKeys[usage] = mapped
        send(mapped, down: true)
        if !mapped.isModifier {
            startRepeat(mapped, usage: usage)
        }
        return true
    }

    func replaceText(deleting count: Int, with text: String) -> Bool {
        guard isControlling else { return false }
        let asText = lockedModifiers.union(pressedModifiers).union(stickyModifiers).isSubset(of: [.shift])
        for _ in 0..<count { tapKey(.backspace) }
        let typed = insertText(text)
        return asText && typed
    }

    private func insertText(_ text: String) -> Bool {
        var typed = true
        var remaining = Substring(text)
        if !lockedModifiers.union(pressedModifiers).isSubset(of: [.shift]) {
            for character in remaining { typed = typeCharacter(character) && typed }
            return typed
        }
        if !stickyModifiers.isEmpty, let first = remaining.popFirst() {
            typed = typeCharacter(first)
        }
        var run = ""
        for character in remaining {
            let key: MacKey? =
                switch character {
                case "\n", "\r", "\r\n": .returnKey
                case "\t": .tab
                default: nil
                }
            guard let key else {
                run.append(character)
                continue
            }
            typed = typeText(run) && typed
            run = ""
            tap(key, modifiers: [])
        }
        return typeText(run) && typed
    }

    private func typeText(_ text: String) -> Bool {
        guard !text.isEmpty else { return true }
        if info.secureInput {
            let typed = text.filter(\.isASCII)
            if !typed.isEmpty { session?.sendText(typed) }
            guard typed.count == text.count else {
                showSecureInputNotice()
                return false
            }
            return true
        }
        // ScreensharingAgent recognizes Unicode keysyms only with high word 0x0100 and posts one UTF-16 unit.
        // macOS 27.0.1 (26A434), arm64e UUID DA1CD9BA-97A1-3758-B425-00FA34B428A3:
        // KeyMapDictionary_ConvertKeysym at 0x100035c7c; PostKeyEvent at 0x100027894.
        let inBasicPlane = text.unicodeScalars.allSatisfy { $0.value <= 0xffff }
        guard info.isMac else {
            let typed = String(String.UnicodeScalarView(text.unicodeScalars.filter { $0.value <= 0xffff }))
            if !typed.isEmpty { session?.sendText(typed) }
            if !inBasicPlane {
                alert = AlertState(title: String(localized: "Some Characters Can’t Be Typed on “\(title)”"))
            }
            return inBasicPlane
        }
        let macComposes = keyboardSource?.isInputMethod ?? capabilities.contains(.keyboardSource)
        if inBasicPlane, !macComposes {
            session?.sendText(text)
        } else {
            session?.pasteText(text)
        }
        return true
    }

    private func showSecureInputNotice() {
        alert = AlertState(title: String(localized: "Password Fields Take Only English Characters"))
    }

    private func typeCharacter(_ character: Character) -> Bool {
        let held = lockedModifiers.union(pressedModifiers)
        let modifiers = held.union(stickyModifiers)
        var key: MacKey
        var shift = false
        switch character {
        case "\n", "\r", "\r\n":
            key = .returnKey
        case "\t":
            key = .tab
        default:
            if modifiers.contains(.option), let keystroke = MacKey.usKeystroke(for: character) {
                (key, shift) = keystroke
            } else {
                var text = String(character)
                if modifiers.contains(.command) || modifiers.contains(.control) {
                    text = modifiers.contains(.shift) ? text.uppercased() : text.lowercased()
                }
                guard let scalar = text.unicodeScalars.first else { return false }
                key = MacKey(keysym: Keysym.character(scalar))
            }
        }
        if info.secureInput, !Keysym.typesInSecureInput(key.keysym) {
            if !stickyModifiers.isEmpty { stickyModifiers.removeAll() }
            showSecureInputNotice()
            return false
        }
        var pressing = stickyModifiers
        if shift { pressing.insert(.shift) }
        if !stickyModifiers.isEmpty { stickyModifiers.removeAll() }
        tap(key, modifiers: pressing.subtracting(held))
        return true
    }

    func typeKey(_ key: MacKey) {
        guard isControlling else { return }
        textCursorMayHaveMoved()
        tapKey(key)
    }

    private func tapKey(_ key: MacKey) {
        let sticky = stickyModifiers
        if !sticky.isEmpty { stickyModifiers.removeAll() }
        tap(key, modifiers: sticky)
    }

    private func textCursorMayHaveMoved() {
        screen?.forgetTypedText()
    }

    private func tap(_ key: MacKey, modifiers: Set<ModifierKey>) {
        for modifier in ModifierKey.allCases where modifiers.contains(modifier) { send(modifier.macKey, down: true) }
        send(key, down: true)
        send(key, down: false)
        for modifier in ModifierKey.allCases.reversed() where modifiers.contains(modifier) { send(modifier.macKey, down: false) }
    }

    private func setCapsLock(_ engaged: Bool) {
        guard engaged != capsLockEngaged, capabilities.contains(.keyCodes) else { return }
        capsLockEngaged = engaged
        send(.capsLock, down: engaged)
    }

    private static let capsLockHoldDuration: TimeInterval = 0.5


    private func capsLockPressed() {
        if capsLockPressedAt != nil {
            capsLockActsOnPress = true
        }
        capsLockPressedAt = ProcessInfo.processInfo.systemUptime
        if capsLockActsOnPress {
            capsLockTapped()
        }
    }

    private func capsLockReleased() {
        guard let pressedAt = capsLockPressedAt else { return }
        capsLockPressedAt = nil
        if capsLockActsOnPress { return }
        if ProcessInfo.processInfo.systemUptime - pressedAt >= Self.capsLockHoldDuration {
            setCapsLock(!capsLockEngaged)
        } else {
            capsLockTapped()
        }
    }

    private func capsLockTapped() {
        if capsLockEngaged {
            setCapsLock(false)
        } else if !info.secureInput, macHasOtherKeyboardSource {
            switchKeyboardSource()
        } else {
            setCapsLock(true)
        }
    }

    var keyboardSource: MacInputSource? {
        info.keyboardSource.map(MacInputSource.init(id:))
    }

    private var macHasOtherKeyboardSource: Bool {
        guard let current = keyboardSource else { return false }
        return current.isInputMethod || keyboardSources.contains { $0.id != current.id }
    }


    func switchKeyboardSource() {
        guard isControlling else { return }
        sendChord(.previousInputSource)
    }

    func selectKeyboardSource(_ source: MacInputSource) {
        guard isControlling, !info.secureInput, source.id != info.keyboardSource else { return }
        session?.selectKeyboardSource(source.id)
        if source.isInputMethod {
            sendChord(.previousInputSource)
            sendChord(.previousInputSource)
        }
    }

    private func rememberKeyboardSource(_ id: String?) {
        guard let id, !keyboardSources.contains(where: { $0.id == id }) else { return }
        keyboardSources = Array((keyboardSources + [MacInputSource(id: id)]).suffix(8))
        saveKeyboardSources()
    }

    private func saveKeyboardSources() {
        guard let computerID, !keyboardSources.isEmpty else { return }
        save { try $0.setKeyboardSources(keyboardSources.map(\.id), for: computerID) }
    }

    func toggleSticky(_ modifier: ModifierKey) {
        if lockedModifiers.contains(modifier) {
            setModifier(modifier, locked: false)
        } else if stickyModifiers.contains(modifier) {
            stickyModifiers.remove(modifier)
        } else if isControlling {
            stickyModifiers.insert(modifier)
        }
    }

    func setModifier(_ modifier: ModifierKey, locked: Bool) {
        guard locked != lockedModifiers.contains(modifier), !locked || isControlling else { return }
        stickyModifiers.remove(modifier)
        if locked {
            lockedModifiers.insert(modifier)
        } else {
            lockedModifiers.remove(modifier)
        }
        if !pressedModifiers.contains(modifier) {
            send(modifier.macKey, down: locked)
        }
    }

    func setModifier(_ modifier: ModifierKey, pressed: Bool) {
        guard pressed != pressedModifiers.contains(modifier), !pressed || isControlling else { return }
        stickyModifiers.remove(modifier)
        if pressed {
            pressedModifiers.insert(modifier)
        } else {
            pressedModifiers.remove(modifier)
        }
        if !lockedModifiers.contains(modifier) {
            send(modifier.macKey, down: pressed)
        }
    }

    func releaseAllKeys() {
        stopRepeat()
        let keys = pressedKeys.values.sorted { lhs, rhs in !lhs.isModifier && rhs.isModifier }
        pressedKeys.removeAll()
        for key in keys {
            send(key, down: false)
        }
        if capsLockEngaged {
            capsLockEngaged = false
            send(.capsLock, down: false)
        }
        capsLockPressedAt = nil
        if !pointerButtons.isEmpty {
            release(pointerButtons)
        }
        for modifier in lockedModifiers.union(pressedModifiers) {
            send(modifier.macKey, down: false)
        }
        lockedModifiers.removeAll()
        pressedModifiers.removeAll()
    }

    private func heldModifiers(from flags: UIKeyModifierFlags) -> Set<ModifierKey> {
        var result = Set<ModifierKey>()
        if flags.contains(.control) { result.insert(.control) }
        if flags.contains(.alternate) { result.insert(.option) }
        if flags.contains(.shift) { result.insert(.shift) }
        if flags.contains(.command) { result.insert(.command) }
        return result
    }

    private func macKey(for key: UIKey) -> MacKey? {
        let typed = keysym(for: key)
        guard let keyCode = MacKey.keyCode(for: key.keyCode, type: keyboardType) else {
            return typed.map { MacKey(keyCode: nil, keysym: $0) }
        }
        return MacKey(keyCode: keyCode, keysym: typed ?? 0)
    }

    private func keysym(for key: UIKey) -> UInt32? {
        if let special = Keysym.special(for: key.keyCode) { return special }
        let flags = key.modifierFlags
        let base = key.charactersIgnoringModifiers
        var text: String
        if flags.contains(.command) || flags.contains(.control) {
            if flags.contains(.shift) {
                let typed = key.characters
                if let scalar = typed.unicodeScalars.first, scalar.value >= 0x20, scalar.value != 0x7f {
                    text = typed.uppercased()
                } else {
                    text = base.uppercased()
                }
            } else {
                text = base.lowercased()
            }
        } else {
            let typed = key.characters
            if let scalar = typed.unicodeScalars.first, scalar.value >= 0x20, scalar.value != 0x7f {
                text = typed
            } else {
                text = base
            }
            if text.count == 1, text.first?.isLetter == true {
                let cased = flags.contains(.shift) != capsLockEngaged ? text.uppercased() : text.lowercased()
                if cased.count == 1 { text = cased }
            }
        }
        guard let scalar = text.unicodeScalars.first else { return nil }
        return Keysym.character(scalar)
    }

    private func startRepeat(_ key: MacKey, usage: UIKeyboardHIDUsage) {
        stopRepeat()
        repeatTask = Task { [weak self] in
            try? await Task.sleep(seconds: 0.45)
            while !Task.isCancelled {
                guard let self, self.pressedKeys[usage] == key else { return }
                self.send(key, down: true)
                try? await Task.sleep(seconds: 0.04)
            }
        }
    }

    private func stopRepeat() {
        repeatTask?.cancel()
        repeatTask = nil
    }

    private func send(_ key: MacKey, down: Bool) {
        session?.sendKey(key, keyboardType: keyboardType, down: down)
    }


    func sampleDebugInfo() {
        debugInfo.sample(phase == .connected ? session?.statistics() : nil)
    }


    private func updateIdleTimer() {
        SessionActivity.shared.set(self, active: phase.isActive)
    }
}

@MainActor
final class SessionActivity {
    static let shared = SessionActivity()
    private var active = Set<ObjectIdentifier>()

    func set(_ controller: AnyObject, active isActive: Bool) {
        if isActive {
            active.insert(ObjectIdentifier(controller))
        } else {
            active.remove(ObjectIdentifier(controller))
        }
        UIApplication.shared.isIdleTimerDisabled = !active.isEmpty
    }
}

nonisolated final class SessionEventSink: @unchecked Sendable {
    private let lock = NSLock()
    private weak var controller: SessionController?
    private let pipeline: DisplayPipeline
    private let debugInfo: DebugInfoMonitor
    private var detached = false
    private var decoder: VideoDecoder!
    private var audioPlayer: AudioPlayer?
    private var isVideo = false
    private var lastKeyframeRequest: CFAbsoluteTime = 0
    private var sceneInBackground = false
    private var awaitingLife = false
    weak var session: LibsharingRemoteSession?

    @MainActor
    init(controller: SessionController, pipeline: DisplayPipeline) {
        self.controller = controller
        self.pipeline = pipeline
        debugInfo = controller.debugInfo
        decoder = VideoDecoder(
            output: { [weak self, pipeline] frame in
                guard let self, !self.isDetached else { return }
                let timing = frame.timing
                pipeline.setVideoFrame(frame) { [weak self] picture, held in
                    self?.pictureReady(picture, arrival: timing.arrival, hold: timing.libraryHold + held, decode: timing.decode)
                }
                self.showedLife()
            },
            failure: { [weak self] in self?.decoderFailed() },
            unsupported: { [weak self] hdr, error in self?.onMain { hdr ? $0.hdrDecodingFailed(error) : $0.videoDecodingFailed(error) } }
        )
    }

    func detach() {
        lock.lock()
        detached = true
        let player = audioPlayer
        audioPlayer = nil
        lock.unlock()
        player?.stop()
        decoder.reset()
    }

    private var isDetached: Bool {
        lock.lock()
        defer { lock.unlock() }
        return detached
    }

    func setSceneInBackground(_ background: Bool) {
        decoder.setSuspended(background)
        lock.lock()
        sceneInBackground = background
        audioPlayer?.setInBackground(background)
        lock.unlock()
    }

    func watchForLife() {
        lock.lock()
        awaitingLife = true
        lock.unlock()
    }

    private func showedLife() {
        lock.lock()
        let awaited = awaitingLife
        awaitingLife = false
        lock.unlock()
        if awaited {
            onMain { $0.connectionResumed() }
        }
    }

    private func onMain(_ body: @escaping @MainActor (SessionController) -> Void) {
        DispatchQueue.main.async { [weak self] in
            guard let self, !self.isDetached, let controller = self.controller else { return }
            body(controller)
        }
    }

    private func decoderFailed() {
        let now = CFAbsoluteTimeGetCurrent()
        lock.lock()
        let shouldRequest = now - lastKeyframeRequest > 0.5
        if shouldRequest { lastKeyframeRequest = now }
        lock.unlock()
        if shouldRequest {
            session?.requestKeyframe()
        }
    }

    private func pictureReady(_ picture: Int, arrival: CFTimeInterval, hold: TimeInterval, decode: TimeInterval) {
        guard debugInfo.isRunning.load(ordering: .relaxed) else { return }
        onMain { $0.debugInfo.pictureReady(picture, arrival: arrival, hold: hold, decode: decode) }
    }

    func sessionPhaseChanged(_ phase: SessionPhase) {
        onMain { $0.handle(phase: phase) }
    }

    func sessionNeedsFramebuffer(width: Int, height: Int) -> Framebuffer? {
        guard !isDetached, let framebuffer = Framebuffer(width: width, height: height) else { return nil }
        pipeline.attachFramebuffer(framebuffer)
        lock.lock()
        let video = isVideo
        lock.unlock()
        if video {
            pipeline.beginVideo()
        }
        onMain { $0.handle(screenWidth: width, height: height, video: video) }
        return framebuffer
    }

    func sessionFramebufferUpdated(_ dirtyRect: CGRect) {
        guard !isDetached else { return }
        if let picture = pipeline.markDirty() {
            pictureReady(picture, arrival: CACurrentMediaTime(), hold: 0, decode: 0)
        }
        showedLife()
    }

    func sessionVideoFormatChanged(_ format: VideoFormat) {
        decoder.setFormat(format)
        lock.lock()
        let started = !isVideo
        isVideo = true
        lock.unlock()
        guard started else { return }
        pipeline.beginVideo()
        let size = pipeline.screenSize
        onMain { $0.handle(screenWidth: size.width, height: size.height, video: true) }
    }

    func sessionVideoStopped(failure: MediaFailure?) {
        decoder.reset()
        lock.lock()
        isVideo = false
        let player = audioPlayer
        audioPlayer = nil
        lock.unlock()
        player?.stop()
        pipeline.endVideo()
        let size = pipeline.screenSize
        onMain { controller in
            controller.handle(screenWidth: size.width, height: size.height, video: false)
            if let failure {
                controller.handleVideoFailure(failure)
            }
        }
    }

    func sessionReceivedVideo(_ unit: VideoAccessUnit) {
        decoder.decode(unit)
    }

    func sessionAudioFormatChanged(_ format: AudioFormat) {
        let player: AudioPlayer?
        do {
            player = try AudioPlayer(format: format) { [weak self] error in self?.onMain { $0.audioFailed(error) } }
        } catch {
            player = nil
            onMain { $0.audioFailed(error) }
        }
        lock.lock()
        let previous = audioPlayer
        audioPlayer = player
        if !sceneInBackground {
            player?.start()
        }
        lock.unlock()
        previous?.stop()
    }

    func sessionReceivedAudio(_ unit: AudioAccessUnit) {
        lock.lock()
        let player = audioPlayer
        lock.unlock()
        player?.enqueue(unit)
    }

    func sessionDisplaysChanged(_ displays: [RemoteDisplay], selection: DisplaySelection) {
        onMain { $0.handle(displays: displays, selection: selection) }
    }

    func sessionCursorChanged(_ cursor: CursorShape?) {
        pipeline.setCursor(cursor)
    }

    func sessionModeChanged(_ mode: SessionMode) {
        onMain { $0.handle(mode: mode) }
    }

    func sessionCapabilitiesChanged(_ capabilities: SessionCapabilities) {
        onMain { $0.handle(capabilities: capabilities) }
    }

    func sessionClipboardReceived(_ content: ClipboardContent) {
        onMain { $0.handle(clipboard: content) }
    }

    func sessionCursorMoved(x: Int, y: Int) {
        onMain { $0.handle(cursorMovedTo: CGPoint(x: x, y: y)) }
    }

    func sessionKeyboardSourceChanged(_ source: String?, secureInput: Bool) {
        onMain { $0.handle(keyboardSource: source, secureInput: secureInput) }
    }

    func sessionEvent(_ event: SessionEvent) {
        onMain { $0.handle(event: event) }
    }

    func sessionFileTransferProgress(id: Int64, transferred: Int64, total: Int64, item: String?) {
        onMain { $0.handleTransferProgress(id: id, transferred: transferred, total: total, item: item) }
    }

    func sessionFileTransferFinished(id: Int64, name: String?, error: FileTransferError?) {
        onMain { $0.handleTransferFinished(id: id, name: name, error: error) }
    }

    func sessionDeviceInfo(_ device: DeviceInfo) {
        onMain { $0.handle(device: device) }
    }

    func sessionInfoChanged(_ info: SessionInfo) {
        onMain { $0.handle(info: info) }
    }
}
