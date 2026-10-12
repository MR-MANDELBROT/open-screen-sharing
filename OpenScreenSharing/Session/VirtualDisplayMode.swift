import CoreGraphics
import Foundation

nonisolated struct VirtualDisplayMode: Hashable, Sendable {
    var width: Int
    var height: Int
    var density: Int

    var pixelWidth: Int { width * density }
    var pixelHeight: Int { height * density }

    static let minimumLongSide: CGFloat = 800
    static let minimumShortSide: CGFloat = 525
    static let safeMinimumShortSide: CGFloat = 600

    static let minimumWindowPoints = CGSize(width: minimumLongSide * 0.75, height: minimumShortSide * 0.75)

    static let resolutionScaleRange: ClosedRange<Double> = 1...2
    static let resolutionScaleStep = 0.25
    static let resolutionScales = Array(
        stride(from: resolutionScaleRange.lowerBound, through: resolutionScaleRange.upperBound, by: resolutionScaleStep))
    static let defaultResolutionScale: Double = 1

    init(width: Int, height: Int, density: Int) {
        self.width = width
        self.height = height
        self.density = density
    }

    init?(matching area: CGSize, screenScale: CGFloat, resolutionScale: CGFloat = 1, minimumShortSide: CGFloat = VirtualDisplayMode.minimumShortSide) {
        guard area.width >= 1, area.height >= 1 else { return nil }
        // ScreenSharing's -[SSSessionView setDynamicSize:] requests two pixels per point (ScreenSharing 776.27,
        // macOS 27.0.1 26A434, UUID EC0B2C20-F816-3FCE-822D-F9403C09EB50, -[SSSessionView setDynamicSize:]
        // 0x1cc0a3788..0x1cc0a37f0).
        let density = screenScale >= 2 ? 2 : 1
        let points = CGSize(width: area.width * resolutionScale, height: area.height * resolutionScale)
        let grow = max(
            1, Self.minimumLongSide / max(points.width, points.height), minimumShortSide / min(points.width, points.height))
        self.init(
            width: Int((points.width * grow + 0.001).rounded(.down)),
            height: Int((points.height * grow + 0.001).rounded(.down)),
            density: density
        )
    }

    static func largestSide(onScreen screen: CGSize, screenScale: CGFloat) -> Int {
        guard
            let mode = VirtualDisplayMode(
                matching: screen, screenScale: screenScale, resolutionScale: resolutionScaleRange.upperBound)
        else { return 0 }
        return max(mode.pixelWidth, mode.pixelHeight)
    }

    var correction: VirtualDisplayMode? {
        let corrected = VirtualDisplayMode(
            matching: CGSize(width: width, height: height), screenScale: CGFloat(density),
            minimumShortSide: Self.safeMinimumShortSide)
        return corrected == self ? nil : corrected
    }
}

@MainActor
final class VirtualDisplayRequests {
    private var send: ((VirtualDisplayMode) -> Void)?
    private var sent: VirtualDisplayMode?
    private var waiting: VirtualDisplayMode?
    private var refused: Set<VirtualDisplayMode> = []
    private var isCorrection = false
    private var framebufferChanged = false
    private var answerTask: Task<Void, Never>?
    private(set) var isOpening = false
    var onIdle: (() -> Void)?

    var isIdle: Bool { answerTask == nil && waiting == nil && !isOpening }

    private static let answerTimeout: TimeInterval = 3

    func reset(sender: ((VirtualDisplayMode) -> Void)?) {
        answerTask?.cancel()
        answerTask = nil
        send = sender
        sent = nil
        waiting = nil
        refused = []
        isCorrection = false
        framebufferChanged = false
        isOpening = false
    }

    func open() {
        isOpening = true
    }

    func connectionDidOpen(offersVirtualDisplay: Bool) {
        guard isOpening else { return }
        isOpening = false
        guard offersVirtualDisplay else {
            sent = nil
            return
        }
        if sent != nil {
            awaitAnswer()
        }
    }

    func request(_ mode: VirtualDisplayMode) {
        if isOpening {
            guard mode != sent, let send else { return }
            sent = mode
            send(mode)
            return
        }
        if answerTask != nil {
            waiting = mode == sent ? nil : mode
            return
        }
        guard mode != sent, !refused.contains(mode) else { return }
        transmit(mode, correction: false)
    }

    func framebufferDidChange(width: Int, height: Int) {
        guard answerTask != nil, let sent else { return }
        guard sent.pixelWidth == width, sent.pixelHeight == height else {
            framebufferChanged = true
            return
        }
        answered()
    }

    func displaysDidChange(_ displays: [RemoteDisplay], framebufferWidth: Int, framebufferHeight: Int) {
        guard answerTask != nil, let sent, sent.pixelWidth == framebufferWidth, sent.pixelHeight == framebufferHeight,
            displays.contains(where: { $0.isVirtual && $0.width == sent.width && $0.height == sent.height })
        else { return }
        answered()
    }

    func stopWaiting() {
        guard let answerTask else { return }
        answerTask.cancel()
        self.answerTask = nil
        waiting = nil
        reportIdle()
    }

    private func answered() {
        answerTask?.cancel()
        answerTask = nil
        sendWaiting()
        reportIdle()
    }

    private func transmit(_ mode: VirtualDisplayMode, correction: Bool) {
        guard let send else { return }
        sent = mode
        waiting = nil
        isCorrection = correction
        framebufferChanged = false
        send(mode)
        awaitAnswer()
    }

    private func awaitAnswer() {
        answerTask = Task { [weak self] in
            try? await Task.sleep(seconds: Self.answerTimeout)
            guard !Task.isCancelled else { return }
            self?.timedOut()
        }
    }

    private func timedOut() {
        answerTask = nil
        defer { reportIdle() }
        guard let sent else { return }
        if framebufferChanged {
            refused.insert(sent)
        }
        if waiting != nil {
            sendWaiting()
        } else if framebufferChanged, !isCorrection, let correction = sent.correction, !refused.contains(correction) {
            transmit(correction, correction: true)
        }
    }

    private func reportIdle() {
        if isIdle {
            onIdle?()
        }
    }

    private func sendWaiting() {
        guard let next = waiting else { return }
        waiting = nil
        guard !refused.contains(next) else { return }
        transmit(next, correction: false)
    }
}
