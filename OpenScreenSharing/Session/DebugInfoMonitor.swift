import Atomics
import Foundation
import Perception
import QuartzCore

@Perceptible
final class DebugInfoMonitor {
    struct Figures: Equatable {
        var readyPerSecond: Double?
        var shownPerSecond: Double?
        var bitsPerSecond: Double?
        var roundTripMilliseconds: Double?
        var encoding: String?
        var toScreenMilliseconds: Double?
        var holdMilliseconds: Double?
        var decodeMilliseconds: Double?
        var video: Video?
    }

    struct Video: Equatable {
        var loss: Double?
        var keyframeRequests: Int64
        var queuingDelayMilliseconds: Double
    }

    private struct Ready {
        var picture: Int
        var arrival: CFTimeInterval
        var hold: TimeInterval
        var decode: TimeInterval
    }

    private struct Screened {
        var toScreen: TimeInterval
        var hold: TimeInterval
        var decode: TimeInterval
    }

    static let graphLength = 120
    private static let maxWaiting = 64

    private(set) var figures: Figures?
    private(set) var toScreen: [Double] = []

    nonisolated let isRunning = ManagedAtomic<Bool>(false)
    @PerceptionIgnored private var run = 0
    @PerceptionIgnored private var startedAt: CFTimeInterval = 0
    @PerceptionIgnored private var lastPicture: Int?
    @PerceptionIgnored private var lastPresentation: CFTimeInterval?
    @PerceptionIgnored private var waiting: [Ready] = []
    @PerceptionIgnored private var ready = 0
    @PerceptionIgnored private var shown = 0
    @PerceptionIgnored private var screened: [Screened] = []
    @PerceptionIgnored private var windowStart: CFTimeInterval = 0
    @PerceptionIgnored private var previous: SessionStatistics?

    var presentationRun: Int? { isRunning.load(ordering: .relaxed) ? run : nil }

    func start() {
        isRunning.store(true, ordering: .relaxed)
        run &+= 1
        startedAt = CACurrentMediaTime()
        windowStart = startedAt
        toScreen = []
        figures = nil
        lastPicture = nil
        lastPresentation = nil
        waiting = []
        ready = 0
        shown = 0
        screened = []
        previous = nil
    }

    func stop() {
        isRunning.store(false, ordering: .relaxed)
        run &+= 1
        waiting = []
    }

    func pictureReady(_ picture: Int, arrival: CFTimeInterval, hold: TimeInterval, decode: TimeInterval) {
        guard isRunning.load(ordering: .relaxed) else { return }
        if let lastPicture, picture <= lastPicture { return }
        ready += 1
        if waiting.count == Self.maxWaiting {
            waiting.removeFirst()
        }
        waiting.append(Ready(picture: picture, arrival: arrival, hold: hold, decode: decode))
    }

    func picturePresented(_ picture: Int, at time: CFTimeInterval, run: Int) {
        guard isRunning.load(ordering: .relaxed), run == self.run, time.isFinite, time > 0, time >= startedAt else { return }
        if let lastPicture, picture <= lastPicture { return }
        if let lastPresentation, time <= lastPresentation { return }
        lastPicture = picture
        lastPresentation = time
        shown += 1
        var kept: [Ready] = []
        var took: [Double] = []
        for waited in waiting {
            guard waited.picture <= picture else {
                kept.append(waited)
                continue
            }
            let delay = max(time - waited.arrival, 0)
            screened.append(Screened(toScreen: delay, hold: waited.hold, decode: waited.decode))
            took.append(delay)
        }
        waiting = kept
        guard !took.isEmpty else { return }
        toScreen = Array((toScreen + took).suffix(Self.graphLength))
    }

    func sample(_ statistics: SessionStatistics?) {
        let now = CACurrentMediaTime()
        defer {
            previous = statistics
            windowStart = now
            ready = 0
            shown = 0
            screened.removeAll(keepingCapacity: true)
        }
        guard let statistics else {
            figures = nil
            return
        }
        let elapsed = now - windowStart
        let measured = previous != nil && elapsed > 0
        func rate(_ count: Int64, since earlier: Int64?) -> Double? {
            guard measured else { return nil }
            let added = earlier.map { count >= $0 ? count - $0 : count } ?? count
            return Double(added) / elapsed
        }
        func milliseconds(_ values: [TimeInterval]) -> Double? {
            values.isEmpty ? nil : values.reduce(0, +) / Double(values.count) * 1000
        }
        var video: Video?
        if let stream = statistics.video {
            var expected = stream.expectedPackets
            var packets = stream.packets
            if let earlier = previous?.video, expected >= earlier.expectedPackets, packets >= earlier.packets {
                expected -= earlier.expectedPackets
                packets -= earlier.packets
            }
            video = Video(
                loss: expected > 0 ? Double(max(expected - packets, 0)) / Double(expected) : nil,
                keyframeRequests: stream.keyframeRequests,
                queuingDelayMilliseconds: stream.queuingDelayMilliseconds
            )
        }
        figures = Figures(
            readyPerSecond: measured ? Double(ready) / elapsed : nil,
            shownPerSecond: measured ? Double(shown) / elapsed : nil,
            bitsPerSecond: rate(statistics.receivedBytes, since: previous?.receivedBytes).map { $0 * 8 },
            roundTripMilliseconds: statistics.roundTripMilliseconds,
            encoding: statistics.encoding,
            toScreenMilliseconds: milliseconds(screened.map(\.toScreen)),
            holdMilliseconds: video == nil ? nil : milliseconds(screened.map(\.hold)),
            decodeMilliseconds: video == nil ? nil : milliseconds(screened.map(\.decode)),
            video: video
        )
    }
}
