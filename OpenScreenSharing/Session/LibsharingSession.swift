import CoreGraphics
import CoreMedia
import Foundation
import QuartzCore
import Libsharing
import os

nonisolated final class LibsharingRemoteSession: @unchecked Sendable {
    private let configuration: SessionConfiguration
    private let lock = NSLock()
    private var session: LibsharingSession?
    private var handler: LibsharingHandler?
    private var transfers: [Int64: LibsharingFileTransfer] = [:]
    private var remoteAudio: RemoteAudio
    private var highPerformanceRunning = false
    private var restartingHighPerformance = false
    private var supportsHighPerformance: Bool?

    init(configuration: SessionConfiguration) {
        self.configuration = configuration
        remoteAudio = configuration.remoteAudio
    }

    private var current: LibsharingSession? {
        lock.lock()
        defer { lock.unlock() }
        return session
    }

    private var currentHandler: LibsharingHandler? {
        lock.lock()
        defer { lock.unlock() }
        return handler
    }

    func start(delegate: SessionEventSink) {
        LibsharingLog.installed
        guard let options = LibsharingNewSessionOptions() else {
            delegate.sessionPhaseChanged(.failed(SessionFailure(.other, message: "libsharing returned no session options")))
            return
        }
        options.host = configuration.address.host
        options.port = Int32(configuration.address.port)
        options.username = configuration.credentials.username
        options.password = configuration.credentials.password
        switch configuration.credentials.loginType {
        case .account:
            options.loginType = configuration.credentials.username.isEmpty ? LibsharingLoginVNCPassword : LibsharingLoginAccount
        case .askForPermission:
            options.loginType = configuration.startsInObserveMode ? LibsharingLoginRequestObserve : LibsharingLoginRequestControl
        case .vncPassword:
            options.loginType = LibsharingLoginVNCPassword
        case .none:
            options.loginType = LibsharingLoginNone
        }
        options.quality = Self.quality(configuration.quality)
        options.mode = configuration.startsInObserveMode ? LibsharingModeObserve : LibsharingModeControl
        options.sharedClipboard = configuration.sharesClipboard
        options.clientName = configuration.clientName
        options.decoderMaxPixelRate = DecoderCapabilities.current.maximumPixelRate
        options.maximumFrameRate = Int32(configuration.networkConfiguration.maximumFrameRate.rawValue)
        options.updateRate = Int32(configuration.updateRate)

        let handler = LibsharingHandler(delegate: delegate)
        handler.forwardsClipboard = configuration.sharesClipboard
        handler.onConnected = { [weak self] supportsHighPerformance in
            self?.connected(supportsHighPerformance: supportsHighPerformance)
        }
        handler.onTransferFinished = { [weak self] id in
            self?.transferFinished(id)
        }
        handler.onMediaStopped = { [weak self] in
            guard let self else { return }
            lock.lock()
            if restartingHighPerformance {
                restartingHighPerformance = false
            } else {
                highPerformanceRunning = false
            }
            lock.unlock()
        }
        var error: NSError?
        guard let session = LibsharingNewSession(options, handler, &error) else {
            delegate.sessionPhaseChanged(.failed(SessionFailure(.other, message: error?.localizedDescription ?? "libsharing returned no session")))
            return
        }
        lock.lock()
        self.session = session
        self.handler = handler
        lock.unlock()
        handler.session = session
        do {
            try session.connect()
        } catch {
            delegate.sessionPhaseChanged(.failed(SessionFailure(.unreachable, message: error.localizedDescription)))
        }
    }

    func stop() {
        lock.lock()
        let session = self.session
        lock.unlock()
        session?.disconnect()
    }

    func answerSessionChoice(_ prompt: SessionChoicePrompt, choice: SessionChoice) {
        let answer =
            switch choice {
            case .shareConsole: LibsharingSessionChoiceShareConsole
            case .ownSession: LibsharingSessionChoiceOwnSession
            case .cancel: LibsharingSessionChoiceCancel
            }
        try? current?.answerPrompt(prompt.id, answer: answer)
    }

    func sendPointer(x: Int, y: Int, buttons: PointerButtons) {
        var mask: Int32 = 0
        if buttons.contains(.primary) { mask |= LibsharingButtonLeft }
        if buttons.contains(.middle) { mask |= LibsharingButtonMiddle }
        if buttons.contains(.secondary) { mask |= LibsharingButtonRight }
        try? current?.sendPointer(Int32(x), y: Int32(y), buttons: mask)
    }

    func sendScroll(x: Int, y: Int, deltaX: Double, deltaY: Double) {
        let pointsPerPixel = currentHandler?.pointsPerPixel ?? 0.5
        try? current?.sendScroll(Int32(x), y: Int32(y), deltaX: deltaX * pointsPerPixel, deltaY: deltaY * pointsPerPixel)
    }

    func sendKey(_ key: MacKey, keyboardType: MacKeyboardType, down: Bool) {
        guard let keyCode = key.keyCode else {
            try? current?.sendKey(Int32(bitPattern: key.keysym), down: down)
            return
        }
        let type =
            switch keyboardType {
            case .ansi: LibsharingKeyboardANSI
            case .iso: LibsharingKeyboardISO
            case .jis: LibsharingKeyboardJIS
            }
        try? current?.sendKeyCode(Int32(keyCode), keyboardType: type, keysym: Int32(bitPattern: key.keysym), down: down)
    }

    func sendText(_ text: String) {
        try? current?.sendText(text)
    }

    func pasteText(_ text: String) throws {
        try current?.pasteText(text)
    }

    func selectKeyboardSource(_ id: String) {
        try? current?.selectKeyboardSource(id)
    }

    private static func phaseValue(_ phase: GesturePhase) -> Int32 {
        switch phase {
        case .mayBegin: LibsharingGesturePhaseMayBegin
        case .began: LibsharingGesturePhaseBegan
        case .changed: LibsharingGesturePhaseChanged
        case .ended: LibsharingGesturePhaseEnded
        case .cancelled: LibsharingGesturePhaseCancelled
        }
    }

    func sendMagnify(x: Int, y: Int, magnification: Double, phase: GesturePhase) {
        try? current?.sendMagnify(Int32(x), y: Int32(y), magnification: magnification, phase: Self.phaseValue(phase))
    }

    func sendRotate(x: Int, y: Int, degrees: Double, phase: GesturePhase) {
        try? current?.sendRotate(Int32(x), y: Int32(y), degrees: degrees, phase: Self.phaseValue(phase))
    }

    func sendSmartMagnify(x: Int, y: Int) {
        try? current?.sendSmartMagnify(Int32(x), y: Int32(y))
    }

    func sendLookup(x: Int, y: Int) {
        try? current?.sendLookup(Int32(x), y: Int32(y))
    }

    func sendSwipe(x: Int, y: Int, direction: SwipeDirection) {
        let value =
            switch direction {
            case .back: LibsharingSwipeBack
            case .forward: LibsharingSwipeForward
            case .up: LibsharingSwipeUp
            case .down: LibsharingSwipeDown
            }
        try? current?.sendSwipe(Int32(x), y: Int32(y), direction: value)
    }

    func sendScrollPhase(x: Int, y: Int, deltaX: Double, deltaY: Double, phase: GesturePhase) {
        let pointsPerPixel = currentHandler?.pointsPerPixel ?? 0.5
        try? current?.sendScrollPhase(
            Int32(x), y: Int32(y), deltaX: deltaX * pointsPerPixel, deltaY: deltaY * pointsPerPixel, phase: Self.phaseValue(phase))
    }

    func startScrollMomentum(x: Int, y: Int, velocityX: Double, velocityY: Double) {
        let pointsPerPixel = currentHandler?.pointsPerPixel ?? 0.5
        try? current?.startScrollMomentum(
            Int32(x), y: Int32(y), velocityX: velocityX * pointsPerPixel, velocityY: velocityY * pointsPerPixel)
    }

    func stopScrollMomentum() {
        current?.stopScrollMomentum()
    }

    func sendSystemCommand(_ command: SystemCommand) {
        let value =
            switch command {
            case .missionControl: LibsharingSystemCommandMissionControl
            case .appWindows: LibsharingSystemCommandAppWindows
            case .showDesktop: LibsharingSystemCommandShowDesktop
            case .launchpad: LibsharingSystemCommandLaunchpad
            }
        try? current?.sendSystemCommand(value)
    }

    func setAssistCursor(x: Int, y: Int, pointer: AssistPointer, pressed: Bool) {
        let value =
            switch pointer {
            case .highlight: LibsharingAssistPointerHighlight
            case .arrow: LibsharingAssistPointerArrow
            }
        try? current?.setAssistCursor(Int32(x), y: Int32(y), pointer: value, pressed: pressed)
    }

    func hideAssistCursor() {
        try? current?.hideAssistCursor()
    }

    func setMode(_ mode: SessionMode) {
        let value =
            switch mode {
            case .control: LibsharingModeControl
            case .observe: LibsharingModeObserve
            }
        try? current?.setMode(value)
    }

    func selectDisplay(_ selection: DisplaySelection) {
        switch selection {
        case .all: try? current?.selectDisplay(LibsharingAllDisplays)
        case .display(let id): try? current?.selectDisplay(id)
        }
    }

    func setQuality(_ quality: SessionQuality) {
        try? current?.setQuality(Self.quality(quality))
    }

    func startHighPerformance() {
        lock.lock()
        let supported = supportsHighPerformance
        lock.unlock()
        if supported == true {
            startStream()
        } else if supported == false {
            currentHandler?.reportMediaFailure(MediaFailure(.unsupported))
        }
    }

    func startHighPerformanceWithConnection() {
        startStream()
    }

    private func connected(supportsHighPerformance: Bool) {
        lock.lock()
        self.supportsHighPerformance = supportsHighPerformance
        if !supportsHighPerformance {
            highPerformanceRunning = false
        }
        lock.unlock()
    }

    private func startStream() {
        lock.lock()
        guard !highPerformanceRunning else {
            lock.unlock()
            return
        }
        highPerformanceRunning = true
        lock.unlock()
        guard let session = current else {
            lock.lock()
            highPerformanceRunning = false
            lock.unlock()
            return
        }
        let options: LibsharingHighPerformanceOptions
        do {
            options = try highPerformanceOptions()
        } catch {
            lock.lock()
            highPerformanceRunning = false
            lock.unlock()
            currentHandler?.reportMediaFailure(MediaFailure(.undecodable, message: error.localizedDescription))
            return
        }
        do {
            try session.startHighPerformance(options)
        } catch {
            lock.lock()
            highPerformanceRunning = false
            lock.unlock()
            currentHandler?.reportMediaFailure(MediaFailure(.other, message: error.localizedDescription))
        }
    }

    private func highPerformanceOptions() throws -> LibsharingHighPerformanceOptions {
        guard let options = LibsharingNewHighPerformanceOptions() else { throw MessageError("libsharing returned no High Performance options") }
        let capabilities = DecoderCapabilities.current
        guard capabilities.supportsHEVC || capabilities.supportsH264 else { throw MessageError("this device has no hardware decoder for HEVC or H.264") }
        options.supportedCodecs =
            (capabilities.supportsHEVC ? LibsharingDecoderCodecHEVC : 0)
            | (capabilities.supportsH264 ? LibsharingDecoderCodecH264 : 0)
        let network = configuration.networkConfiguration
        options.pixelFormats = network.chroma.pixelFormats(available: capabilities.pixelFormats)
        if network.chroma == .yuv444 && capabilities.supportsFullChroma {
            options.supportedCodecs = LibsharingDecoderCodecHEVC
        }
        lock.lock()
        let audio = remoteAudio
        lock.unlock()
        options.audio =
            switch audio {
            case .off: LibsharingAudioOff
            case .fromMac: LibsharingAudioFromMac
            case .toMac: LibsharingAudioToMac
            }
        options.frameRate60 = false
        options.cursorInVideo = false
        options.videoMode = network.usesCoreDevice ? LibsharingVideoModeCoreDevice : LibsharingVideoModeRemoteDesktop
        options.bands = Int32(network.bands)
        options.immediatePlayout = network.schedulesPresentation
        return options
    }

    func uploadFile(_ localURL: URL, remoteFolder: String) throws -> Int64 {
        guard let session = current else { throw MessageError("not connected") }
        let transfer = try session.uploadFile(localURL.path, remoteFolder: remoteFolder)
        let id = transfer.transferID()
        lock.lock()
        transfers[id] = transfer
        lock.unlock()
        return id
    }

    func downloadFile(_ remotePath: String, localFolder: URL) throws -> Int64 {
        guard let session = current else { throw MessageError("not connected") }
        let transfer = try session.downloadFile(remotePath, localFolder: localFolder.path)
        let id = transfer.transferID()
        lock.lock()
        transfers[id] = transfer
        lock.unlock()
        return id
    }

    private func transfer(_ id: Int64) -> LibsharingFileTransfer? {
        lock.lock()
        defer { lock.unlock() }
        return transfers[id]
    }

    func cancelTransfer(_ id: Int64) {
        try? transfer(id)?.cancel()
    }

    fileprivate func transferFinished(_ id: Int64) {
        lock.lock()
        transfers[id] = nil
        lock.unlock()
    }

    func setRemoteAudio(_ audio: RemoteAudio) {
        lock.lock()
        let changed = audio != remoteAudio
        remoteAudio = audio
        let running = highPerformanceRunning
        lock.unlock()
        guard changed, running, let session = current, let options = try? highPerformanceOptions() else { return }
        lock.lock()
        restartingHighPerformance = true
        lock.unlock()
        do {
            try session.restartHighPerformance(options)
        } catch {
            lock.lock()
            restartingHighPerformance = false
            lock.unlock()
        }
    }

    func sendMicrophoneAudio(_ data: Data) {
        try? current?.sendMicrophoneAudio(data)
    }

    func requestRefresh() {
        try? current?.requestRefresh()
    }

    func requestKeyframe() {
        try? current?.requestKeyframe()
    }

    func statistics() -> SessionStatistics? {
        guard let statistics = current?.statistics() else { return nil }
        return SessionStatistics(
            roundTripMilliseconds: statistics.roundTripMilliseconds,
            receivedBytes: statistics.receivedBytes,
            encoding: statistics.encoding.isEmpty ? nil : statistics.encoding,
            video: statistics.highPerformance
                ? VideoStreamStatistics(
                    packets: statistics.videoPackets,
                    expectedPackets: statistics.videoPacketsExpected,
                    keyframeRequests: statistics.keyframeRequests,
                    queuingDelayMilliseconds: statistics.queueDelayMilliseconds
                ) : nil
        )
    }

    func setVirtualDisplay(_ mode: VirtualDisplayMode, largestSide: Int) {
        try? current?.setVirtualDisplay(
            Int32(mode.width), height: Int32(mode.height), density: Double(mode.density),
            maximumWidth: Int32(largestSide), maximumHeight: Int32(largestSide))
    }

    func setHDR(_ enabled: Bool) {
        try? current?.setHDR(enabled)
    }

    func setUpdateRate(_ rate: Int) {
        try? current?.setUpdateRate(Int32(rate))
    }

    func setClipboardSharing(_ enabled: Bool) {
        currentHandler?.forwardsClipboard = enabled
        try? current?.setSharedClipboard(enabled)
    }

    func sendClipboard(_ content: ClipboardContent) throws {
        guard let session = current else { throw MessageError("not connected") }
        guard let clipboard = LibsharingNewClipboard() else { throw MessageError("libsharing returned no clipboard") }
        for item in content {
            guard let clipboardItem = LibsharingNewClipboardItem() else { throw MessageError("libsharing returned no clipboard item") }
            for (type, data) in item {
                clipboardItem.addFlavor(type, data: data)
            }
            clipboard.add(clipboardItem)
        }
        try session.setClipboard(clipboard)
    }

    func requestClipboard() throws {
        guard let session = current else { throw MessageError("not connected") }
        currentHandler?.forceNextClipboard = true
        do {
            try session.requestClipboard()
        } catch {
            currentHandler?.forceNextClipboard = false
            throw error
        }
    }

    private static func quality(_ quality: SessionQuality) -> Int32 {
        switch quality {
        case .adaptive: LibsharingQualityAdaptive
        case .full: LibsharingQualityFull
        }
    }
}

nonisolated private final class LibsharingHandler: NSObject, LibsharingSessionHandlerProtocol, @unchecked Sendable {
    private let lock = NSLock()
    private weak var delegate: SessionEventSink?
    weak var session: LibsharingSession?
    private var framebuffer: Framebuffer?
    private var framebufferFailure: String?
    private var info = SessionInfo()
    private var capabilities: SessionCapabilities = []
    private var clipboardForwarded = true
    private var clipboardForced = false
    private var screenPointsPerPixel = 0.5
    var onConnected: (@Sendable (Bool) -> Void)?
    var onMediaStopped: (@Sendable () -> Void)?
    var onTransferFinished: (@Sendable (Int64) -> Void)?
    private var supportsHighPerformance = false

    init(delegate: SessionEventSink) {
        self.delegate = delegate
    }

    private func locked<T>(_ body: () -> T) -> T {
        lock.lock()
        defer { lock.unlock() }
        return body()
    }

    var forwardsClipboard: Bool {
        get { locked { clipboardForwarded } }
        set { locked { clipboardForwarded = newValue } }
    }

    var forceNextClipboard: Bool {
        get { locked { clipboardForced } }
        set { locked { clipboardForced = newValue } }
    }

    var pointsPerPixel: Double {
        locked { screenPointsPerPixel }
    }

    private func updateInfo(_ body: (inout SessionInfo) -> Void) {
        lock.lock()
        defer { lock.unlock() }
        body(&info)
        delegate?.sessionInfoChanged(info)
    }

    func onStateChanged(_ state: Int32, errorCode: Int32, message: String?) {
        guard let delegate else { return }
        switch state {
        case LibsharingStateConnecting:
            delegate.sessionPhaseChanged(.connecting)
        case LibsharingStateAuthenticating:
            delegate.sessionPhaseChanged(.authenticating)
        case LibsharingStateWaitingForApproval:
            delegate.sessionPhaseChanged(.waitingForPermission)
        case LibsharingStateChoosingSession:
            break
        case LibsharingStateConnected:
            onConnected?(locked { supportsHighPerformance })
            delegate.sessionPhaseChanged(.connected)
        case LibsharingStateDisconnected:
            let text = locked { framebufferFailure } ?? message ?? ""
            switch errorCode {
            case LibsharingErrorNone, LibsharingErrorCancelled:
                delegate.sessionPhaseChanged(.disconnected)
            case LibsharingErrorAuthentication:
                delegate.sessionPhaseChanged(.failed(SessionFailure(.authenticationFailed, message: text)))
            case LibsharingErrorPermissionDenied:
                delegate.sessionPhaseChanged(.failed(SessionFailure(.permissionDenied, message: text)))
            case LibsharingErrorHostNotFound:
                delegate.sessionPhaseChanged(.failed(SessionFailure(.hostNotFound, message: text)))
            case LibsharingErrorOwnScreen:
                delegate.sessionPhaseChanged(.failed(SessionFailure(.ownScreen, message: text)))
            case LibsharingErrorNetwork:
                let started = locked { info.framebufferWidth > 0 }
                delegate.sessionPhaseChanged(.failed(SessionFailure(started ? .connectionLost : .unreachable, message: text)))
            case LibsharingErrorSessionEnded:
                delegate.sessionPhaseChanged(.failed(SessionFailure(.closedByServer)))
            case LibsharingErrorUnsupportedServer:
                delegate.sessionPhaseChanged(.failed(SessionFailure(.unsupported, message: text)))
            default:
                let started = locked { info.framebufferWidth > 0 }
                delegate.sessionPhaseChanged(.failed(SessionFailure(started ? .ended : .other, message: text)))
            }
        default:
            break
        }
    }

    func onServerInfo(_ serverInfo: LibsharingServerInfo?) {
        guard let serverInfo else { return }
        var capabilities: SessionCapabilities = []
        if !serverInfo.observeOnly { capabilities.insert(.control) }
        if serverInfo.supportsDisplaySelection { capabilities.insert(.displaySelection) }
        if serverInfo.supportsClipboard { capabilities.insert(.clipboard) }
        if serverInfo.supportsVirtualDisplay { capabilities.insert(.virtualDisplay) }
        if serverInfo.supportsKeyboardSource { capabilities.insert(.keyboardSource) }
        if serverInfo.supportsKeyCodes { capabilities.insert(.keyCodes) }
        if serverInfo.supportsHighPerformance { capabilities.insert(.highPerformance) }
        if serverInfo.supportsFileTransfer { capabilities.insert(.fileTransfer) }
        if serverInfo.supportsGestures { capabilities.insert(.gestures) }
        if serverInfo.supportsAssistCursor { capabilities.insert(.assistCursor) }
        if serverInfo.supportsMissionControl { capabilities.insert(.missionControl) }
        if serverInfo.supportsAppWindows { capabilities.insert(.appWindows) }
        if serverInfo.supportsShowDesktop { capabilities.insert(.showDesktop) }
        if serverInfo.supportsLaunchpad { capabilities.insert(.launchpad) }
        locked {
            supportsHighPerformance = serverInfo.supportsHighPerformance
            self.capabilities = capabilities
        }
        delegate?.sessionCapabilitiesChanged(capabilities)
        updateInfo { info in
            info.isMac = serverInfo.isMac
            info.framebufferWidth = Int(serverInfo.width)
            info.framebufferHeight = Int(serverInfo.height)
        }
        if serverInfo.observeOnly {
            delegate?.sessionModeChanged(.observe)
        }
    }

    func onFramebufferResize(_ width: Int32, height: Int32) {
        guard let session, let delegate else { return }
        let attached: Framebuffer?
        let failure: String?
        do {
            guard let framebuffer = try delegate.sessionNeedsFramebuffer(width: Int(width), height: Int(height)) else { return }
            try session.attachFramebuffer(
                Int64(Int(bitPattern: framebuffer.baseAddress)),
                width: width, height: height, bytesPerRow: Int32(framebuffer.bytesPerRow)
            )
            attached = framebuffer
            failure = nil
        } catch {
            let text = "attach a \(width)x\(height) framebuffer: \(error.localizedDescription)"
            LibsharingLog.logger.error("\(text, privacy: .public)")
            attached = nil
            failure = text
        }
        locked {
            self.framebuffer = attached
            framebufferFailure = failure
        }
        updateInfo { info in
            info.framebufferWidth = Int(width)
            info.framebufferHeight = Int(height)
        }
    }

    func onFramebufferUpdate(_ x: Int32, y: Int32, width: Int32, height: Int32) {
        delegate?.sessionFramebufferUpdated(CGRect(x: Int(x), y: Int(y), width: Int(width), height: Int(height)))
    }

    func onDisplays(_ displays: LibsharingDisplayList?) {
        guard let displays else { return }
        if displays.framebufferWidth > 0, displays.desktopWidth > 0 {
            let ratio = Double(displays.desktopWidth) / Double(displays.framebufferWidth)
            locked { screenPointsPerPixel = ratio }
            updateInfo { info in
                info.framebufferPixelsPerCursorPixel = 1 / ratio
            }
        }
        var result: [RemoteDisplay] = []
        var physicalIndex = 0
        for index in 0..<displays.len() {
            guard let display = displays.get(index) else { continue }
            let name: String
            if display.isVirtual {
                name = String(localized: "Virtual Display")
            } else if display.isMain {
                name = String(localized: "Main Display")
            } else {
                physicalIndex += 1
                name = String(localized: "Display \(physicalIndex + 1)")
            }
            result.append(
                RemoteDisplay(
                    id: display.displayID,
                    name: name,
                    width: Int(display.logicalWidth),
                    height: Int(display.logicalHeight),
                    isMain: display.isMain,
                    isVirtual: display.isVirtual
                ))
        }
        result.sort { lhs, rhs in lhs.isMain && !rhs.isMain }
        let selection: DisplaySelection = displays.current == LibsharingAllDisplays ? .all : .display(displays.current)
        delegate?.sessionDisplaysChanged(result, selection: selection)
    }

    func onCursorImage(_ cursor: LibsharingCursorImage?) {
        guard let cursor, cursor.width > 0, cursor.height > 0, let pixels = cursor.pixels,
            pixels.count >= Int(cursor.width) * Int(cursor.height) * 4,
            let provider = CGDataProvider(data: pixels as CFData),
            let image = CGImage(
                width: Int(cursor.width), height: Int(cursor.height), bitsPerComponent: 8, bitsPerPixel: 32,
                bytesPerRow: Int(cursor.width) * 4, space: CGColorSpace(name: CGColorSpace.sRGB)!,
                bitmapInfo: CGBitmapInfo(rawValue: CGBitmapInfo.byteOrder32Little.rawValue | CGImageAlphaInfo.premultipliedFirst.rawValue),
                provider: provider, decode: nil, shouldInterpolate: false, intent: .defaultIntent
            )
        else {
            delegate?.sessionCursorChanged(nil)
            return
        }
        delegate?.sessionCursorChanged(CursorShape(image: image, hotspot: CGPoint(x: Int(cursor.hotspotX), y: Int(cursor.hotspotY))))
    }

    func onCursorPosition(_ x: Int32, y: Int32) {
        delegate?.sessionCursorMoved(x: Int(x), y: Int(y))
    }

    func onClipboard(_ clipboard: LibsharingClipboard?) {
        guard let clipboard else { return }
        let forwarded = locked {
            let forwarded = clipboardForwarded || clipboardForced
            clipboardForced = false
            return forwarded
        }
        guard forwarded else { return }
        var content: ClipboardContent = []
        for index in 0..<clipboard.len() {
            guard let item = clipboard.get(index) else { continue }
            var flavors: [String: Data] = [:]
            for flavorIndex in 0..<item.len() {
                guard let flavor = item.get(flavorIndex), !flavor.promise, let data = flavor.data else { continue }
                flavors[flavor.type] = data
            }
            if !flavors.isEmpty {
                content.append(flavors)
            }
        }
        delegate?.sessionClipboardReceived(content)
    }

    func onKeyboardSource(_ sourceID: String?, secureInput: Bool) {
        delegate?.sessionKeyboardSourceChanged(sourceID, secureInput: secureInput)
    }

    func onPrompt(_ prompt: LibsharingPrompt?) {
        guard let prompt else { return }
        switch prompt.kind {
        case LibsharingPromptChooseSession:
            delegate?.sessionPhaseChanged(
                .choosingSession(
                    SessionChoicePrompt(
                        id: prompt.promptID,
                        consoleUser: prompt.consoleUser,
                        allowsShareConsole: prompt.allowsShareConsole,
                        allowsOwnSession: prompt.allowsOwnSession
                    )))
        default:
            try? session?.answerPrompt(prompt.promptID, answer: LibsharingSessionChoiceCancel)
        }
    }

    func onStatus(_ status: Int32) {
        switch status {
        case LibsharingStatusSessionEnding: delegate?.sessionEvent(.sessionEnding)
        case LibsharingStatusDisplaysSleep: delegate?.sessionEvent(.displaysAsleep)
        case LibsharingStatusDisplaysWake: delegate?.sessionEvent(.displaysAwake)
        case LibsharingStatusControlAllowed:
            let capabilities = locked {
                self.capabilities.insert(.control)
                return self.capabilities
            }
            delegate?.sessionCapabilitiesChanged(capabilities)
            delegate?.sessionEvent(.controlAllowed(true))
        case LibsharingStatusObserveOnly:
            let capabilities = locked {
                self.capabilities.remove(.control)
                return self.capabilities
            }
            delegate?.sessionCapabilitiesChanged(capabilities)
            delegate?.sessionEvent(.controlAllowed(false))
            delegate?.sessionModeChanged(.observe)
        case LibsharingStatusPointerHidden: delegate?.sessionEvent(.pointerVisible(false))
        case LibsharingStatusPointerShown: delegate?.sessionEvent(.pointerVisible(true))
        case LibsharingStatusBusyCursorStart: delegate?.sessionEvent(.busy(true))
        case LibsharingStatusBusyCursorEnd: delegate?.sessionEvent(.busy(false))
        case LibsharingStatusUserSessionChanged: delegate?.sessionEvent(.userSessionChanged)
        default: break
        }
    }

    func onBell() {
        delegate?.sessionEvent(.bell)
    }

    func onClipboardProblem(_ kind: Int32, message: String?) {
        if kind == LibsharingClipboardProblemNotRestored {
            delegate?.sessionEvent(.clipboardNotRestored(message ?? ""))
        } else {
            delegate?.sessionEvent(.clipboardUnreadable(message ?? ""))
        }
    }

    func onVideoFormat(_ format: LibsharingVideoFormat?) {
        guard let format else { return }
        var parameterSets: [Data] = []
        for index in 0..<format.len() {
            if let data = format.get(index) { parameterSets.append(data) }
        }
        delegate?.sessionVideoFormatChanged(
            VideoFormat(
                codec: format.codec == LibsharingCodecH264 ? .h264 : .hevc,
                parameterSets: parameterSets,
                bands: Int(max(1, format.bands)),
                canvasHeight: Int(format.height),
                isHDR: format.hdr
            ))
        let codec = format.codec == LibsharingCodecH264 ? "H.264" : "HEVC"
        let chroma = format.chromaFormat == 3 ? "4:4:4" : "4:2:0"
        let hdr = format.hdr
        updateInfo { $0.videoCodec = hdr ? "\(codec) \(chroma) HDR" : "\(codec) \(chroma)" }
    }

    func onVideoAccessUnit(_ unit: LibsharingVideoAccessUnit?) {
        guard let unit, let data = unit.data else { return }
        let hold = Double(max(unit.holdNanoseconds, 0)) / 1_000_000_000
        delegate?.sessionReceivedVideo(
            VideoAccessUnit(
                data: data,
                isSync: unit.sync,
                presentationTime: CMTime(value: unit.timestamp, timescale: max(1, unit.clockRate)),
                band: Int(unit.band),
                bands: Int(unit.bands),
                frameID: unit.frameID,
                frameEnd: unit.frameEnd,
                changedBands: Int(unit.changedBands),
                discontinuity: unit.discontinuity,
                turns: PictureTurns(Int(unit.turns)),
                arrival: CACurrentMediaTime() - hold,
                libraryHold: hold
            ))
    }

    func onAudioFormat(_ format: LibsharingAudioFormat?) {
        guard let format else { return }
        delegate?.sessionAudioFormatChanged(
            AudioFormat(
                sampleRate: Double(format.sampleRate),
                channels: UInt32(max(1, format.channels)),
                framesPerPacket: UInt32(max(1, format.framesPerPacket)),
                magicCookie: format.magicCookie ?? format.audioSpecificConfig ?? Data()
            ))
    }

    func onAudioPacket(_ packet: LibsharingAudioPacket?) {
        guard let packet, let data = packet.data else { return }
        delegate?.sessionReceivedAudio(AudioAccessUnit(data: data, timestamp: packet.timestamp, lost: Int(packet.lost)))
    }

    func onMediaStopped(_ errorCode: Int32, message: String?) {
        onMediaStopped?()
        updateInfo { $0.videoCodec = nil }
        let kind: MediaFailure.Kind? =
            switch errorCode {
            case LibsharingMediaErrorNone: nil
            case LibsharingMediaErrorBusy: .busy
            case LibsharingMediaErrorNetwork: .network
            case LibsharingMediaErrorRefused: .refused
            case LibsharingMediaErrorSilent: .silent
            default: .other
            }
        delegate?.sessionVideoStopped(failure: kind.map { MediaFailure($0, message: message ?? "") })
    }

    func onDeviceInfo(_ model: String?, deviceColor: String?, enclosureColor: String?) {
        func known(_ value: String?) -> String {
            guard let value, value.lowercased() != "unknown" else { return "" }
            return value
        }
        let model = known(model)
        guard !model.isEmpty else { return }
        let color = known(enclosureColor).isEmpty ? known(deviceColor) : known(enclosureColor)
        delegate?.sessionDeviceInfo(DeviceInfo(model: model, enclosureColor: color))
    }

    func onFileTransferProgress(_ transferID: Int64, transferred: Int64, total: Int64, item: String?) {
        delegate?.sessionFileTransferProgress(id: transferID, transferred: transferred, total: total, item: item)
    }

    func onFileTransferFinished(_ transferID: Int64, name: String?, errorCode: Int32, message: String?) {
        onTransferFinished?(transferID)
        let kind: FileTransferError.Kind? =
            switch errorCode {
            case LibsharingFileErrorNone: nil
            case LibsharingFileErrorCancelled: .cancelled
            case LibsharingFileErrorNotAllowed: .notAllowed
            case LibsharingFileErrorNotFound: .notFound
            case LibsharingFileErrorNotWritable: .notWritable
            case LibsharingFileErrorDiskFull: .diskFull
            case LibsharingFileErrorPermission: .permission
            case LibsharingFileErrorLocal: .local
            case LibsharingFileErrorConnection: .connection
            case LibsharingFileErrorObserveOnly: .observeOnly
            case LibsharingFileErrorUnsupported: .unsupported
            default: .failed
            }
        delegate?.sessionFileTransferFinished(id: transferID, name: name, error: kind.map { FileTransferError($0, message: message ?? "") })
    }

    func reportMediaFailure(_ failure: MediaFailure) {
        delegate?.sessionVideoStopped(failure: failure)
    }
}
