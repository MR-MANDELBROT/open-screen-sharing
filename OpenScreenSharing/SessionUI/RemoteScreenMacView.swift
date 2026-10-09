#if os(macOS)
import AppKit
import Carbon
import SwiftUI

struct RemoteScreenView: NSViewRepresentable {
    @ObservedObject var controller: SessionController

    func makeNSView(context: Context) -> RemoteScreenNSView {
        RemoteScreenNSView(controller: controller)
    }

    func updateNSView(_ view: RemoteScreenNSView, context: Context) {
        view.setAccessibilityValue(controller.mode == .control ? String(localized: "Controlling") : String(localized: "Observing"))
        view.refreshCursor()
    }
}

final class RemoteScreenNSView: NSView {
    private static let minimumZoom: CGFloat = 0.25
    private static let pointerMargin: CGFloat = 40
    private static let pointsPerLine: CGFloat = 10
    private static let wheelPointsPerZoomDoubling: CGFloat = 100
    private static let defaultPictureSize = CGSize(width: 1280, height: 800)
    private static let views = NSHashTable<RemoteScreenNSView>.weakObjects()

    private let controller: SessionController
    private let metalLayer = CAMetalLayer()
    private var renderer: ScreenRenderer?
    private var displayLink: CADisplayLink?
    private var presentationTimer: DispatchSourceTimer?
    private var presentationTick: CFTimeInterval = 0
    private var needsRender = true
    private var renderedThisFrame = false
    private var idleFrames = 0
    private var lastSnapshot: DisplayPipeline.Snapshot?
    private var lastPresentation: (epoch: Int, generation: Int, time: CFTimeInterval)?
    private var refreshRate = 0
    private var isOccluded = false

    private var zoom: CGFloat = 1
    private var viewportCenter: CGPoint?

    private var settleTask: Task<Void, Never>?
    private var reportedArea: CGSize?
    private var reportedScale: CGFloat?
    private var keyMonitor: Any?

    private var trackingArea: NSTrackingArea?
    private var systemPointerLocation: CGPoint?
    private var drawsCursorLocally = false
    private var remoteCursor: (image: CGImage, scale: CGFloat, cursor: NSCursor)?
    private var remoteCursorVisible = true
    private var clickedButtons: PointerButtons = []
    private var panStart: CGPoint?
    private var locksPointer = false
    private var isPointerLocked = false
    private var isPointerReleased = false
    private static let chordModifiers: NSEvent.ModifierFlags = [.control, .option, .shift, .command, .function]
    private var releaseChordFlags: NSEvent.ModifierFlags = []
    private var releaseChordSpoiled = false
    private var shapedImageSize: CGSize?
    private var hasSizedWindow = false
    private var keepsRememberedSize = false

    private var scrollStarted = false
    private var scrollUsesPhases = false
    private var scrollPansView = false
    private var scrollRemainder = CGPoint.zero
    private var scrollSamples: [(delta: CGPoint, time: TimeInterval)] = []
    private var magnifyGoesToMac = false
    private var rotateGoesToMac = false

    private static let hiddenCursor = NSCursor(image: NSImage(size: NSSize(width: 1, height: 1)), hotSpot: .zero)

    init(controller: SessionController) {
        self.controller = controller
        super.init(frame: .zero)
        wantsLayer = true
        layerContentsRedrawPolicy = .never
        metalLayer.backgroundColor = NSColor.black.cgColor
        guard let renderer = ScreenRenderer(layer: metalLayer) else {
            fatalError("The screen can't be drawn without Metal")
        }
        self.renderer = renderer
        controller.screen = self
        Self.views.add(self)
        setAccessibilityElement(true)
        setAccessibilityRole(.image)
        setAccessibilityLabel(String(localized: "Screen of \(controller.title)"))
        setAccessibilityCustomActions([
            NSAccessibilityCustomAction(name: String(localized: "Click")) { [weak self] in
                self?.controller.click(.primary)
                return self?.controller.isControlling ?? false
            },
            NSAccessibilityCustomAction(name: String(localized: "Right-Click")) { [weak self] in
                self?.controller.click(.secondary)
                return self?.controller.isControlling ?? false
            },
        ])
        controller.pipeline.setWakeHandler { [weak self] in
            DispatchQueue.main.async { self?.contentChanged() }
        }
    }

    required init?(coder: NSCoder) {
        fatalError()
    }

    deinit {
        MainActor.assumeIsolated {
            displayLink?.invalidate()
            presentationTimer?.cancel()
            controller.pipeline.setWakeHandler(nil)
            if let keyMonitor {
                NSEvent.removeMonitor(keyMonitor)
            }
            if isPointerLocked {
                CGAssociateMouseAndMouseCursorPosition(1)
                NSCursor.unhide()
            }
        }
    }

    override func makeBackingLayer() -> CALayer { metalLayer }

    override var wantsUpdateLayer: Bool { true }

    override func updateLayer() {}

    override var isFlipped: Bool { true }

    override var isOpaque: Bool { true }

    override var acceptsFirstResponder: Bool { true }

    private var backingScale: CGFloat {
        window?.backingScaleFactor ?? 2
    }


    override func viewWillMove(toWindow newWindow: NSWindow?) {
        super.viewWillMove(toWindow: newWindow)
        guard let window, newWindow !== window else { return }
        setPointerLocked(false)
        NotificationCenter.default.removeObserver(self, name: nil, object: window)
        DistributedNotificationCenter.default().removeObserver(self)
    }

    override func viewDidMoveToWindow() {
        super.viewDidMoveToWindow()
        displayLink?.invalidate()
        displayLink = nil
        presentationTimer?.cancel()
        presentationTimer = nil
        if let keyMonitor {
            NSEvent.removeMonitor(keyMonitor)
            self.keyMonitor = nil
        }
        settleTask?.cancel()
        settleTask = nil
        reportedArea = nil
        reportedScale = nil
        guard let window else {
            controller.releaseAllKeys()
            return
        }
        if !hasSizedWindow, !window.isVisible {
            window.alphaValue = 0
        }
        let center = NotificationCenter.default
        center.addObserver(self, selector: #selector(windowDidBecomeKey), name: NSWindow.didBecomeKeyNotification, object: window)
        center.addObserver(self, selector: #selector(windowDidResignKey), name: NSWindow.didResignKeyNotification, object: window)
        center.addObserver(self, selector: #selector(windowOcclusionDidChange), name: NSWindow.didChangeOcclusionStateNotification, object: window)
        center.addObserver(self, selector: #selector(windowScreenDidChange), name: NSWindow.didChangeScreenNotification, object: window)
        center.addObserver(self, selector: #selector(windowFullScreenDidChange), name: NSWindow.didEnterFullScreenNotification, object: window)
        center.addObserver(self, selector: #selector(windowFullScreenDidChange), name: NSWindow.didExitFullScreenNotification, object: window)
        DistributedNotificationCenter.default().addObserver(
            self, selector: #selector(keyboardSourceDidChange), name: NSNotification.Name(kTISNotifySelectedKeyboardInputSourceChanged as String),
            object: nil, suspensionBehavior: .deliverImmediately
        )
        let link = displayLink(target: DisplayLinkTarget(self), selector: #selector(DisplayLinkTarget.tick))
        link.add(to: .main, forMode: .common)
        displayLink = link
        keyMonitor = NSEvent.addLocalMonitorForEvents(matching: [.keyDown, .keyUp, .flagsChanged]) { [weak self] event in
            guard let self else { return event }
            if self.detectsReleaseChord(event), self.isPointerLocked {
                self.isPointerReleased = true
                self.updatePointerLock()
            }
            return self.capturesKey(event) && self.controller.handleKeyEvent(event) ? nil : event
        }
        isOccluded = !window.occlusionState.contains(.visible)
        refreshRate = 0
        updateDrawableSize()
        geometryDidChange()
        DispatchQueue.main.async { [weak self] in
            guard let self, let window = self.window else { return }
            self.sizeWindow()
            window.alphaValue = 1
            self.windowScreenDidChange()
            self.windowFullScreenDidChange()
            window.makeFirstResponder(self)
            if window.isKeyWindow { self.windowDidBecomeKey() }
        }
    }

    override func setFrameSize(_ newSize: NSSize) {
        super.setFrameSize(newSize)
        updateDrawableSize()
        geometryDidChange()
        if controller.scaleMode == .actualSize {
            applyScale(.actualSize)
        }
        clampCenter()
        setNeedsRender()
    }

    override func viewDidChangeBackingProperties() {
        super.viewDidChangeBackingProperties()
        updateDrawableSize()
        geometryDidChange()
        setNeedsRender()
    }

    override func viewDidEndLiveResize() {
        super.viewDidEndLiveResize()
        settleTask?.cancel()
        settleTask = nil
        reportArea()
    }

    override func updateTrackingAreas() {
        super.updateTrackingAreas()
        if let trackingArea {
            removeTrackingArea(trackingArea)
        }
        let area = NSTrackingArea(
            rect: .zero, options: [.mouseMoved, .mouseEnteredAndExited, .cursorUpdate, .activeInKeyWindow, .inVisibleRect], owner: self)
        addTrackingArea(area)
        trackingArea = area
    }

    private func updateDrawableSize() {
        let scale = backingScale
        metalLayer.contentsScale = scale
        metalLayer.drawableSize = CGSize(width: bounds.width * scale, height: bounds.height * scale)
    }

    private func geometryDidChange() {
        guard window != nil, bounds.width > 0, bounds.height > 0 else { return }
        settleTask?.cancel()
        settleTask = nil
        if inLiveResize { return }
        if bounds.size == reportedArea, backingScale == reportedScale { return }
        let delay: TimeInterval = reportedArea == nil ? 0.1 : 0.3
        settleTask = Task { [weak self] in
            try? await Task.sleep(seconds: delay)
            guard let self, !Task.isCancelled, !self.inLiveResize else { return }
            self.reportArea()
        }
    }

    private func reportArea() {
        guard let window, bounds.width > 0, bounds.height > 0 else { return }
        reportedArea = bounds.size
        reportedScale = window.backingScaleFactor
        controller.displayAreaDidChange(bounds.size, scale: window.backingScaleFactor, screen: window.screen?.frame.size ?? bounds.size)
        if hasSizedWindow, !window.styleMask.contains(.fullScreen) {
            controller.windowSizeDidChange(bounds.size)
        }
    }

    private func sizeWindow() {
        guard !hasSizedWindow, let window, !window.styleMask.contains(.fullScreen), window.contentLayoutRect.width > 0,
            window.contentLayoutRect.height > 0
        else { return }
        hasSizedWindow = true
        keepsRememberedSize = controller.windowSize != nil
        resizeWindow(toPicture: controller.windowSize ?? Self.defaultPictureSize, animate: false)
    }

    private func resizeWindow(toPicture size: CGSize, animate: Bool) {
        guard let window, let screen = window.screen, size.width > 0, size.height > 0 else { return }
        let content = window.contentLayoutRect.size
        let chrome = CGSize(width: window.frame.width - content.width, height: window.frame.height - content.height)
        let available = CGSize(width: screen.visibleFrame.width - chrome.width, height: screen.visibleFrame.height - chrome.height)
        let fit = min(1, available.width / size.width, available.height / size.height)
        let target = CGSize(width: (size.width * fit).rounded(), height: (size.height * fit).rounded())
        guard abs(target.width - content.width) > 1 || abs(target.height - content.height) > 1 else { return }
        var frame = window.frame
        frame.size = CGSize(width: target.width + chrome.width, height: target.height + chrome.height)
        frame.origin.y = window.frame.maxY - frame.height
        window.setFrame(window.constrainFrameRect(frame, to: screen), display: true, animate: animate)
    }

    static func focusWindow(showing route: SessionRoute) -> Bool {
        let shown = views.allObjects.first { view in
            if let id = route.computerID, let computerID = view.controller.computerID {
                return id == computerID
            }
            return view.controller.route.address.matches(route.address)
        }
        guard let shown, let window = shown.window else { return false }
        switch shown.controller.phase {
        case .disconnected, .failed:
            window.close()
            return false
        default:
            if window.isMiniaturized { window.deminiaturize(nil) }
            window.makeKeyAndOrderFront(nil)
            return true
        }
    }

    @objc private func windowDidBecomeKey() {
        guard window?.isKeyWindow == true else { return }
        controller.sceneDidBecomeActive()
        controller.syncCapsLock(NSEvent.modifierFlags.contains(.capsLock))
        updatePointerLock()
    }

    @objc private func windowDidResignKey() {
        if isPointerLocked { isPointerReleased = true }
        setPointerLocked(false)
        releaseChordFlags = []
        releaseChordSpoiled = false
        controller.releaseAllKeys()
        if !clickedButtons.isEmpty {
            clickedButtons = []
            controller.setPressedButtons([])
        }
        panStart = nil
        endScroll(velocity: .zero, cancelled: true)
        systemPointerLocation = nil
        controller.pointerLeftScreen()
        setNeedsRender()
    }

    @objc private func keyboardSourceDidChange() {
        guard let window, window.isKeyWindow, window.attachedSheet == nil, !(window.firstResponder is NSText),
            let source = TISCopyCurrentKeyboardInputSource()?.takeRetainedValue(),
            let id = TISGetInputSourceProperty(source, kTISPropertyInputSourceID)
        else { return }
        controller.followKeyboardSource(Unmanaged<CFString>.fromOpaque(id).takeUnretainedValue() as String)
    }

    @objc private func windowOcclusionDidChange() {
        guard let window else { return }
        isOccluded = !window.occlusionState.contains(.visible)
        if isOccluded {
            displayLink?.isPaused = true
        } else {
            setNeedsRender()
        }
    }

    @objc private func windowScreenDidChange() {
        guard let screen = window?.screen else { return }
        controller.displaySupportsHDR = screen.maximumPotentialExtendedDynamicRangeColorComponentValue > 1
        let rate = screen.maximumFramesPerSecond
        guard rate > 0, rate != refreshRate else { return }
        refreshRate = rate
        let maximum = Float(rate)
        displayLink?.preferredFrameRateRange = CAFrameRateRange(minimum: min(30, maximum), maximum: maximum, preferred: maximum)
        controller.screenRefreshRateDidChange(rate)
    }

    @objc private func windowFullScreenDidChange() {
        let fullScreen = window?.styleMask.contains(.fullScreen) ?? false
        if controller.isFullScreen != fullScreen {
            controller.isFullScreen = fullScreen
        }
    }


    fileprivate func tick() {
        renderedThisFrame = false
        guard !isOccluded else {
            displayLink?.isPaused = true
            return
        }
        if let displayLink, controller.pipeline.isSchedulingPresentation {
            if presentationTimer == nil {
                let timer = DispatchSource.makeTimerSource(queue: .main)
                timer.setEventHandler { [weak self] in
                    MainActor.assumeIsolated { self?.presentScheduledFrames() }
                }
                timer.activate()
                presentationTimer = timer
            }
            presentationTick = displayLink.targetTimestamp
            let midpoint = (displayLink.timestamp + displayLink.targetTimestamp) / 2
            presentationTimer?.schedule(deadline: .now() + max(midpoint - CACurrentMediaTime(), 0), leeway: .microseconds(100))
        }
        let snapshot = controller.pipeline.consume(through: displayLink?.timestamp ?? CACurrentMediaTime())
        if let snapshot {
            lastSnapshot = snapshot
        }
        guard snapshot != nil || needsRender else {
            idleFrames += 1
            if idleFrames > 30 { displayLink?.isPaused = true }
            return
        }
        idleFrames = 0
        renderedThisFrame = true
        needsRender = !render()
    }

    private func presentScheduledFrames() {
        guard !isOccluded, let snapshot = controller.pipeline.consume(through: presentationTick) else { return }
        lastSnapshot = snapshot
        idleFrames = 0
        needsRender = !render()
    }

    private func contentChanged() {
        wake()
        guard !renderedThisFrame, !isOccluded, let snapshot = controller.pipeline.consume(through: CACurrentMediaTime()) else { return }
        lastSnapshot = snapshot
        renderedThisFrame = true
        needsRender = !render()
    }

    private func wake() {
        guard !isOccluded else { return }
        idleFrames = 0
        displayLink?.isPaused = false
    }

    func backgroundStateDidChange() {
        setNeedsRender()
    }

    private func setNeedsRender() {
        needsRender = true
        wake()
    }

    private func render() -> Bool {
        guard let renderer, bounds.width > 0, bounds.height > 0 else { return false }
        let snapshot = lastSnapshot ?? controller.pipeline.snapshot()
        lastSnapshot = snapshot
        let pipeline = controller.pipeline
        let epoch = snapshot.presentationEpoch
        let generation = snapshot.pictureGeneration
        let run = controller.debugInfo.presentationRun
        guard snapshot.turns.shownSize(of: CGSize(width: snapshot.width, height: snapshot.height)) == controller.imageSize,
            pipeline.isCurrentPresentation(epoch: epoch)
        else { return false }
        let imageRect = self.imageRect
        let scale = displayScale
        let cursorScale = max(scale * controller.info.framebufferPixelsPerCursorPixel, 1 / controller.cursorPixelsPerPoint)
        updateRemoteCursor(snapshot.cursor, visible: snapshot.cursorVisible, scale: cursorScale)
        drawsCursorLocally = showsRemoteCursorLocally
        var cursorRect: CGRect?
        if let cursor = snapshot.cursor, snapshot.cursorVisible, controller.phase == .connected, !drawsCursorLocally {
            let origin = CGPoint(
                x: imageRect.minX + snapshot.cursorPosition.x * scale - cursor.hotspot.x * cursorScale,
                y: imageRect.minY + snapshot.cursorPosition.y * scale - cursor.hotspot.y * cursorScale
            )
            cursorRect = CGRect(
                origin: origin,
                size: CGSize(width: CGFloat(cursor.image.width) * cursorScale, height: CGFloat(cursor.image.height) * cursorScale))
        }
        return renderer.render(
            ScreenRenderer.Frame(
                pixelBuffer: snapshot.pixelBuffer,
                strips: snapshot.strips,
                stripHeight: snapshot.stripHeight,
                canvasHeight: snapshot.canvasHeight,
                imageRect: imageRect,
                turns: snapshot.turns,
                cursor: snapshot.cursor,
                cursorRect: cursorRect,
                viewSize: bounds.size,
                pixelsPerImagePixel: scale * backingScale,
                pixelsPerCursorPixel: cursorScale * backingScale
            ),
            in: metalLayer,
            presented: { [weak self, pipeline] time in
                DispatchQueue.main.async {
                    guard let self, let generation,
                        pipeline.isCurrentPresentation(epoch: epoch)
                    else { return }
                    if let previous = self.lastPresentation {
                        guard time > previous.time,
                            epoch != previous.epoch || generation > previous.generation
                        else { return }
                    }
                    self.lastPresentation = (epoch, generation, time)
                    if let run {
                        self.controller.debugInfo.picturePresented(generation, at: time, run: run)
                    }
                }
            },
            failed: { [weak self, pipeline] in
                DispatchQueue.main.async {
                    guard let self, pipeline.isCurrentPresentation(epoch: epoch) else { return }
                    self.setNeedsRender()
                }
            }
        )
    }

    func imageSizeDidChange() {
        if controller.scaleMode == .actualSize {
            applyScale(.actualSize)
        } else if controller.scaleMode == .fit {
            zoom = 1
            viewportCenter = nil
        }
        clampCenter()
        setNeedsRender()
        shapeWindow()
    }

    private func shapeWindow() {
        let size = controller.imageSize
        guard let window, size.width > 0, size.height > 0, size != shapedImageSize,
            controller.scaleMode == .fit, !controller.pictureMatchesWindow, !window.styleMask.contains(.fullScreen), !inLiveResize,
            bounds.width > 0, bounds.height > 0
        else { return }
        shapedImageSize = size
        guard !keepsRememberedSize else {
            keepsRememberedSize = false
            return
        }
        resizeWindow(toPicture: CGSize(width: bounds.width, height: bounds.width * size.height / size.width), animate: true)
    }


    private var imageSize: CGSize {
        let size = controller.imageSize
        return size.width > 0 && size.height > 0 ? size : CGSize(width: 16, height: 10)
    }

    private var fitScale: CGFloat {
        let scale = min(bounds.width / imageSize.width, bounds.height / imageSize.height)
        let devicePixels = scale * backingScale
        return devicePixels >= 1 && devicePixels < 1.01 ? 1 / backingScale : scale
    }

    var displayScale: CGFloat {
        fitScale * zoom
    }

    private var maximumZoom: CGFloat {
        max(4, (8 / backingScale) / max(fitScale, 0.0001))
    }

    private var actualSizeZoom: CGFloat {
        (1 / backingScale) / max(fitScale, 0.0001)
    }

    var imageRect: CGRect {
        let scale = displayScale
        let size = CGSize(width: imageSize.width * scale, height: imageSize.height * scale)
        var origin = CGPoint(x: bounds.midX - size.width / 2, y: bounds.midY - size.height / 2)
        if let center = viewportCenter {
            origin = CGPoint(x: bounds.midX - center.x * scale, y: bounds.midY - center.y * scale)
        }
        origin.x = (origin.x * backingScale).rounded() / backingScale
        origin.y = (origin.y * backingScale).rounded() / backingScale
        return CGRect(origin: origin, size: size)
    }

    private func clampCenter() {
        guard let center = viewportCenter else { return }
        let scale = displayScale
        func clamp(_ center: CGFloat, image: CGFloat, bounds: ClosedRange<CGFloat>) -> CGFloat {
            let size = image * scale
            let middle = (bounds.lowerBound + bounds.upperBound) / 2
            let origins: ClosedRange<CGFloat>
            if size > bounds.upperBound - bounds.lowerBound {
                origins = (bounds.upperBound - size)...bounds.lowerBound
            } else {
                origins = bounds.lowerBound...(bounds.upperBound - size)
            }
            let origin = min(max(middle - center * scale, origins.lowerBound), origins.upperBound)
            return (middle - origin) / scale
        }
        viewportCenter = CGPoint(
            x: clamp(center.x, image: imageSize.width, bounds: bounds.minX...bounds.maxX),
            y: clamp(center.y, image: imageSize.height, bounds: bounds.minY...bounds.maxY)
        )
    }

    private func currentCenter() -> CGPoint {
        let rect = imageRect
        let scale = displayScale
        return CGPoint(x: (bounds.midX - rect.minX) / scale, y: (bounds.midY - rect.minY) / scale)
    }

    func imagePoint(for viewPoint: CGPoint) -> CGPoint {
        let rect = imageRect
        let scale = displayScale
        return CGPoint(x: (viewPoint.x - rect.minX) / scale, y: (viewPoint.y - rect.minY) / scale)
    }

    func viewPoint(for imagePoint: CGPoint) -> CGPoint {
        let rect = imageRect
        let scale = displayScale
        return CGPoint(x: rect.minX + imagePoint.x * scale, y: rect.minY + imagePoint.y * scale)
    }

    func applyScale(_ scale: ScaleMode) {
        switch scale {
        case .fit:
            zoom = 1
            viewportCenter = nil
        case .actualSize:
            let anchor = controller.pointerPosition
            zoom = max(actualSizeZoom, 0.0001)
            viewportCenter = nil
            let centered = currentCenter()
            let scale = displayScale
            viewportCenter = CGPoint(
                x: imageSize.width * scale > bounds.width ? anchor.x : centered.x,
                y: imageSize.height * scale > bounds.height ? anchor.y : centered.y
            )
            clampCenter()
        case .custom:
            break
        }
        if controller.scaleMode != scale {
            controller.scaleMode = scale
        }
        setNeedsRender()
    }

    private func toggleZoom(at viewPoint: CGPoint) {
        if controller.scaleMode != .fit {
            applyScale(.fit)
        } else {
            setZoom(max(actualSizeZoom, 2), keeping: imagePoint(for: viewPoint), at: viewPoint)
        }
    }

    private func setZoom(_ newZoom: CGFloat, keeping imageAnchor: CGPoint, at viewAnchor: CGPoint) {
        zoom = min(max(newZoom, Self.minimumZoom), maximumZoom)
        let scale = displayScale
        viewportCenter = CGPoint(
            x: imageAnchor.x + (bounds.midX - viewAnchor.x) / scale,
            y: imageAnchor.y + (bounds.midY - viewAnchor.y) / scale
        )
        clampCenter()
        let mode: ScaleMode = abs(zoom - 1) < 0.01 ? .fit : (abs(zoom - actualSizeZoom) < 0.01 ? .actualSize : .custom)
        if controller.scaleMode != mode {
            controller.scaleMode = mode
        }
        setNeedsRender()
    }

    private func keepPointerVisible() {
        guard zoom > 1.01 else { return }
        let point = viewPoint(for: controller.pointerPosition)
        let inset = bounds.insetBy(dx: min(80, bounds.width / 4), dy: min(80, bounds.height / 4))
        var shift = CGPoint.zero
        if point.x < inset.minX { shift.x = point.x - inset.minX } else if point.x > inset.maxX { shift.x = point.x - inset.maxX }
        if point.y < inset.minY { shift.y = point.y - inset.minY } else if point.y > inset.maxY { shift.y = point.y - inset.maxY }
        guard shift != .zero else { return }
        let scale = displayScale
        let current = currentCenter()
        viewportCenter = CGPoint(x: current.x + shift.x / scale, y: current.y + shift.y / scale)
        clampCenter()
        setNeedsRender()
    }

    private func panViewport(by delta: CGPoint) {
        let scale = displayScale
        let current = currentCenter()
        viewportCenter = CGPoint(x: current.x - delta.x / scale, y: current.y - delta.y / scale)
        clampCenter()
        setNeedsRender()
    }


    private var showsRemoteCursorLocally: Bool {
        guard let systemPointerLocation, !isPointerLocked, !controller.isZoomModeOn, controller.isControlling,
            window?.isKeyWindow == true
        else { return false }
        return imageRect.contains(systemPointerLocation)
    }

    private func updateRemoteCursor(_ shape: CursorShape?, visible: Bool, scale: CGFloat) {
        var changed = visible != remoteCursorVisible
        remoteCursorVisible = visible
        if let shape {
            if shape.image !== remoteCursor?.image || scale != remoteCursor?.scale {
                let size = NSSize(width: CGFloat(shape.image.width) * scale, height: CGFloat(shape.image.height) * scale)
                let cursor = NSCursor(
                    image: NSImage(cgImage: shape.image, size: size),
                    hotSpot: NSPoint(x: shape.hotspot.x * scale, y: shape.hotspot.y * scale))
                remoteCursor = (shape.image, scale, cursor)
                changed = true
            }
        } else if remoteCursor != nil {
            remoteCursor = nil
            changed = true
        }
        if changed {
            refreshCursor()
        }
    }

    func refreshCursor() {
        guard systemPointerLocation != nil, !isPointerLocked else { return }
        if controller.isZoomModeOn {
            (panStart != nil ? NSCursor.closedHand : NSCursor.openHand).set()
        } else if showsRemoteCursorLocally {
            (remoteCursorVisible ? remoteCursor?.cursor ?? .arrow : Self.hiddenCursor).set()
        } else {
            NSCursor.arrow.set()
        }
        if showsRemoteCursorLocally != drawsCursorLocally {
            setNeedsRender()
        }
    }

    override func cursorUpdate(with event: NSEvent) {
        systemPointerLocation = convert(event.locationInWindow, from: nil)
        refreshCursor()
    }

    override func mouseEntered(with event: NSEvent) {
        systemPointerLocation = convert(event.locationInWindow, from: nil)
        refreshCursor()
    }

    override func mouseExited(with event: NSEvent) {
        guard !isPointerLocked else { return }
        systemPointerLocation = nil
        if clickedButtons.isEmpty {
            controller.pointerLeftScreen()
        }
        if drawsCursorLocally {
            setNeedsRender()
        }
    }

    override func mouseMoved(with event: NSEvent) {
        pointerMoved(event)
    }

    override func mouseDragged(with event: NSEvent) {
        pointerMoved(event)
    }

    override func rightMouseDragged(with event: NSEvent) {
        pointerMoved(event)
    }

    override func otherMouseDragged(with event: NSEvent) {
        pointerMoved(event)
    }

    private func pointerMoved(_ event: NSEvent) {
        if isPointerLocked {
            let scale = displayScale
            guard scale > 0 else { return }
            controller.movePointer(
                to: CGPoint(
                    x: controller.pointerPosition.x + event.deltaX / scale,
                    y: controller.pointerPosition.y + event.deltaY / scale
                ))
            keepPointerVisible()
            return
        }
        let location = convert(event.locationInWindow, from: nil)
        systemPointerLocation = location
        if let panStart {
            panViewport(by: CGPoint(x: location.x - panStart.x, y: location.y - panStart.y))
            self.panStart = location
            return
        }
        refreshCursor()
        guard !controller.isZoomModeOn else { return }
        if !clickedButtons.isEmpty || imageRect.insetBy(dx: -Self.pointerMargin, dy: -Self.pointerMargin).contains(location) {
            controller.movePointer(to: imagePoint(for: location))
        } else {
            controller.pointerLeftScreen()
        }
    }

    override func mouseDown(with event: NSEvent) {
        buttonChanged(.primary, down: true, event: event)
    }

    override func mouseUp(with event: NSEvent) {
        buttonChanged(.primary, down: false, event: event)
    }

    override func rightMouseDown(with event: NSEvent) {
        buttonChanged(.secondary, down: true, event: event)
    }

    override func rightMouseUp(with event: NSEvent) {
        buttonChanged(.secondary, down: false, event: event)
    }

    override func otherMouseDown(with event: NSEvent) {
        guard event.buttonNumber == 2 else { return }
        buttonChanged(.middle, down: true, event: event)
    }

    override func otherMouseUp(with event: NSEvent) {
        guard event.buttonNumber == 2 else { return }
        buttonChanged(.middle, down: false, event: event)
    }

    private func buttonChanged(_ button: PointerButtons, down: Bool, event: NSEvent) {
        let location = convert(event.locationInWindow, from: nil)
        guard down else {
            if button == .primary, panStart != nil {
                panStart = nil
                refreshCursor()
            }
            guard clickedButtons.remove(button) != nil else { return }
            controller.release(button)
            return
        }
        controller.stopScrollMomentum()
        window?.makeFirstResponder(self)
        controller.syncModifiers(event.modifierFlags)
        if !event.modifierFlags.intersection(Self.chordModifiers).isEmpty { releaseChordSpoiled = true }
        if isPointerReleased, locksPointer, imageRect.contains(location) {
            isPointerReleased = false
            updatePointerLock()
        }
        if controller.isZoomModeOn, !isPointerLocked {
            guard button == .primary else { return }
            if event.clickCount == 2 {
                toggleZoom(at: location)
            } else {
                panStart = location
                refreshCursor()
            }
            return
        }
        if !isPointerLocked {
            guard !clickedButtons.isEmpty || imageRect.insetBy(dx: -Self.pointerMargin, dy: -Self.pointerMargin).contains(location)
            else { return }
            controller.movePointer(to: imagePoint(for: location))
        }
        clickedButtons.insert(button)
        controller.press(button)
    }


    override func scrollWheel(with event: NSEvent) {
        let location = convert(event.locationInWindow, from: nil)
        guard event.hasPreciseScrollingDeltas else {
            let delta = CGPoint(x: event.scrollingDeltaX * Self.pointsPerLine, y: event.scrollingDeltaY * Self.pointsPerLine)
            if controller.isZoomModeOn {
                let factor = pow(2, delta.y / Self.wheelPointsPerZoomDoubling)
                setZoom(zoom * factor, keeping: imagePoint(for: location), at: location)
            } else {
                scrollRemainder = .zero
                sendScroll(delta)
            }
            return
        }
        let delta = CGPoint(x: event.scrollingDeltaX, y: event.scrollingDeltaY)
        if !event.momentumPhase.isEmpty {
            if !scrollUsesPhases, !scrollPansView {
                sendScroll(delta)
            }
            return
        }
        switch event.phase {
        case .mayBegin:
            controller.stopScrollMomentum()
        case .began:
            controller.stopScrollMomentum()
            scrollStarted = false
            scrollUsesPhases = controller.sendsGesturesToMac
            scrollPansView = controller.isZoomModeOn
            scrollRemainder = .zero
            scrollSamples = []
            scrolled(by: delta, at: event.timestamp)
        case .changed:
            scrolled(by: delta, at: event.timestamp)
        case .ended:
            let recent = scrollSamples.filter { event.timestamp - $0.time < 0.1 }
            var velocity = CGPoint.zero
            if recent.count > 1, let first = recent.first, let last = recent.last {
                let interval = max(last.time - first.time, 0.008)
                let distance = recent.dropFirst().reduce(CGPoint.zero) { CGPoint(x: $0.x + $1.delta.x, y: $0.y + $1.delta.y) }
                velocity = CGPoint(x: distance.x / interval, y: distance.y / interval)
            }
            endScroll(velocity: velocity, cancelled: false)
        case .cancelled:
            endScroll(velocity: .zero, cancelled: true)
        default:
            scrollUsesPhases = false
            scrollPansView = false
            sendScroll(delta)
        }
    }

    private func scrolled(by delta: CGPoint, at time: TimeInterval) {
        scrollSamples.append((delta, time))
        if scrollSamples.count > 8 { scrollSamples.removeFirst() }
        if scrollPansView {
            panViewport(by: delta)
        } else if scrollUsesPhases {
            let scale = displayScale
            controller.scroll(byPixels: CGPoint(x: delta.x / scale, y: delta.y / scale), phase: scrollStarted ? .changed : .began)
            scrollStarted = true
        } else {
            sendScroll(delta)
        }
    }

    private func endScroll(velocity: CGPoint, cancelled: Bool) {
        scrollSamples = []
        guard scrollStarted else { return }
        scrollStarted = false
        controller.scroll(byPixels: .zero, phase: cancelled ? .cancelled : .ended)
        if !cancelled {
            let scale = displayScale
            controller.startScrollMomentum(pixelsPerSecond: CGPoint(x: velocity.x / scale, y: velocity.y / scale))
        }
    }

    private func sendScroll(_ delta: CGPoint) {
        scrollRemainder.x += delta.x
        scrollRemainder.y += delta.y
        let sendX = scrollRemainder.x.rounded(.towardZero)
        let sendY = scrollRemainder.y.rounded(.towardZero)
        guard sendX != 0 || sendY != 0 else { return }
        scrollRemainder.x -= sendX
        scrollRemainder.y -= sendY
        let scale = displayScale
        controller.scroll(byPixels: CGPoint(x: sendX / scale, y: sendY / scale))
    }

    override func magnify(with event: NSEvent) {
        if event.phase == .began {
            magnifyGoesToMac = controller.sendsGesturesToMac && !controller.isZoomModeOn
        }
        if magnifyGoesToMac {
            guard let phase = GesturePhase(event.phase) else { return }
            controller.magnify(at: controller.pointerPosition, by: event.magnification, phase: phase)
            return
        }
        let location = convert(event.locationInWindow, from: nil)
        setZoom(zoom * (1 + event.magnification), keeping: imagePoint(for: location), at: location)
    }

    override func rotate(with event: NSEvent) {
        if event.phase == .began {
            rotateGoesToMac = controller.sendsGesturesToMac && !controller.isZoomModeOn
        }
        guard rotateGoesToMac, let phase = GesturePhase(event.phase) else { return }
        controller.rotate(at: controller.pointerPosition, degrees: Double(event.rotation), phase: phase)
    }

    override func smartMagnify(with event: NSEvent) {
        guard controller.isZoomModeOn || !controller.sendsGesturesToMac else { return }
        toggleZoom(at: convert(event.locationInWindow, from: nil))
    }


    func setLocksPointer(_ locks: Bool) {
        if !controller.isPointerLockOn { isPointerReleased = false }
        guard locks != locksPointer else { return }
        locksPointer = locks
        updatePointerLock()
    }

    private func updatePointerLock() {
        setPointerLocked(locksPointer && !isPointerReleased && window?.isKeyWindow == true && NSApp.isActive)
    }

    private func detectsReleaseChord(_ event: NSEvent) -> Bool {
        guard event.window === window else { return false }
        let flags = event.modifierFlags.intersection(Self.chordModifiers)
        guard event.type == .flagsChanged else {
            if event.type == .keyDown, !flags.isEmpty { releaseChordSpoiled = true }
            return false
        }
        let chord = KeyShortcuts.shared.pointerRelease
        let detected = releaseChordFlags == chord && flags.isStrictSubset(of: chord) && !releaseChordSpoiled
        releaseChordFlags = flags
        if flags.isEmpty {
            releaseChordSpoiled = false
        } else if detected || !flags.isSubset(of: chord) {
            releaseChordSpoiled = true
        }
        return detected
    }

    private func setPointerLocked(_ locked: Bool) {
        guard locked != isPointerLocked, let window else { return }
        isPointerLocked = locked
        if controller.isPointerLocked != locked {
            controller.isPointerLocked = locked
        }
        if locked {
            let location = convert(window.mouseLocationOutsideOfEventStream, from: nil)
            if imageRect.contains(location) {
                controller.movePointer(to: imagePoint(for: location))
            }
            warpCursor(to: CGPoint(x: bounds.midX, y: bounds.midY))
            CGAssociateMouseAndMouseCursorPosition(0)
            NSCursor.hide()
            systemPointerLocation = nil
        } else {
            CGAssociateMouseAndMouseCursorPosition(1)
            let point = viewPoint(for: controller.pointerPosition)
            if bounds.contains(point) {
                warpCursor(to: point)
            }
            NSCursor.unhide()
            if !clickedButtons.isEmpty {
                clickedButtons = []
                controller.setPressedButtons([])
            }
        }
        setNeedsRender()
    }

    private func warpCursor(to viewPoint: CGPoint) {
        guard let window, let primary = NSScreen.screens.first else { return }
        let point = window.convertPoint(toScreen: convert(viewPoint, to: nil))
        CGWarpMouseCursorPosition(CGPoint(x: point.x, y: primary.frame.maxY - point.y))
    }


    private func capturesKey(_ event: NSEvent) -> Bool {
        guard let window, event.window === window, window.attachedSheet == nil, !(window.firstResponder is NSText) else { return false }
        return event.type != .keyDown || !KeyShortcuts.shared.isAssigned(event)
    }

    override func becomeFirstResponder() -> Bool {
        let became = super.becomeFirstResponder()
        if became {
            controller.syncCapsLock(NSEvent.modifierFlags.contains(.capsLock))
        }
        return became
    }

    override func resignFirstResponder() -> Bool {
        let resigned = super.resignFirstResponder()
        if resigned {
            controller.releaseAllKeys()
        }
        return resigned
    }

    @objc func paste(_ sender: Any?) {
        controller.pasteFromDevice()
    }
}

extension RemoteScreenNSView: NSMenuItemValidation {
    func validateMenuItem(_ menuItem: NSMenuItem) -> Bool {
        if menuItem.action == #selector(paste(_:)) {
            return controller.isControlling && NSPasteboard.general.types?.isEmpty == false
        }
        return true
    }
}

private extension GesturePhase {
    init?(_ phase: NSEvent.Phase) {
        switch phase {
        case .mayBegin: self = .mayBegin
        case .began: self = .began
        case .changed: self = .changed
        case .ended: self = .ended
        case .cancelled: self = .cancelled
        default: return nil
        }
    }
}

private final class DisplayLinkTarget {
    weak var view: RemoteScreenNSView?

    init(_ view: RemoteScreenNSView) {
        self.view = view
    }

    @objc func tick() {
        view?.tick()
    }
}
#endif
