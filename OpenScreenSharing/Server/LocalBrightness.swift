import Libsharing
import UIKit
import notify
import os

final class LocalBrightness {
    nonisolated private static let logger = Logger(subsystem: Bundle.main.bundleIdentifier ?? "OpenScreenSharing", category: "server")
    private static let dimmedKey = "serverDimmedBrightness"
    nonisolated private static let displayStatusKey = "com.apple.iokit.hid.displayStatus"
    // Observed on iPhone18,2, iOS 27.2 24B5110a: brightness keys move by 1/16.
    nonisolated private static let steps: Float = 16
    // BKS reports the Always On level until CoreBrightness finishes its wake ramp.
    private static let settleTime: TimeInterval = 1.5
    private static let currentBrightness: (@convention(c) () -> Float)? = {
        guard let handle = dlopen("/System/Library/PrivateFrameworks/BackBoardServices.framework/BackBoardServices", RTLD_NOW),
              let symbol = dlsym(handle, "BKSDisplayBrightnessGetCurrent")
        else { return nil }
        return unsafeBitCast(symbol, to: (@convention(c) () -> Float).self)
    }()

    private nonisolated struct Dimmed: Codable {
        var brightness: Float
        var locks: Bool
    }

    private enum Pending {
        case dim(LibsharingDeviceServer, locks: Bool)
        case restore(LibsharingDeviceServer, locks: Bool, wake: Bool)
    }

    private nonisolated final class Operation: Sendable {
        private let cancelled = LockedState(initialState: false)
        var isCancelled: Bool { cancelled.withLock { $0 } }
        func cancel() { cancelled.withLock { $0 = true } }
    }

    private let store = PreferenceStore.shared
    private let queue: DispatchQueue
    private var watched = false
    private var pending: Pending?
    private var operation: Operation?
    private var task: Task<Void, Never>?
    private var userBrightness: Float?
    private var relocked = false
    private var settledAt = Date.distantPast
    private var displayToken = NOTIFY_TOKEN_INVALID

    init(queue: DispatchQueue) {
        self.queue = queue
        settledAt = Date().addingTimeInterval(Self.settleTime)
        notify_register_dispatch(Self.displayStatusKey, &displayToken, .main) { [weak self] _ in
            MainActor.assumeIsolated { self?.displayChanged() }
        }
        _ = Timer.scheduledTimer(withTimeInterval: 0.5, repeats: true) { [weak self] _ in
            MainActor.assumeIsolated { self?.sample() }
        }
    }

    func setWatched(_ watched: Bool, server: LibsharingDeviceServer?) {
        guard let server else {
            self.watched = false
            operation?.cancel()
            pending = nil
            return
        }
        guard watched != self.watched else { return }
        self.watched = watched
        if watched {
            let locks = !Self.isDisplayOn || (Self.state(of: "com.apple.springboard.lockstate") ?? 0) != 0
            start(.dim(server, locks: locks))
        } else {
            start(.restore(server, locks: true, wake: false))
        }
    }

    func restore(_ server: LibsharingDeviceServer, locks: Bool) {
        guard !watched else { return }
        start(.restore(server, locks: locks, wake: false))
    }

    // Keep the server usable until restoration finishes. If the display is off, briefly wake it
    // and put it back to sleep; otherwise recovery would be stranded after closing the server.
    func stop(_ server: LibsharingDeviceServer) -> Task<Void, Never> {
        watched = false
        return start(.restore(server, locks: false, wake: true))
    }

    @discardableResult
    private func start(_ action: Pending) -> Task<Void, Never> {
        operation?.cancel()
        pending = nil
        let previous = task
        let operation = Operation()
        self.operation = operation
        let task = Task {
            defer {
                if self.operation === operation { self.task = nil }
            }
            // An HID send only queues events. Finish observing an already-sent command before
            // another session sends more keys, even when its old operation has been cancelled.
            await previous?.value
            guard !operation.isCancelled else { return }
            do {
                switch action {
                case let .dim(server, locks):
                    try await dim(server, locks: locks, operation: operation)
                case let .restore(server, locks, wake):
                    try await restore(server, locks: locks, wake: wake, operation: operation)
                }
            } catch is CancellationError {
            } catch {
                Self.logger.error("Brightness: \(error.localizedDescription, privacy: .public)")
            }
        }
        self.task = task
        return task
    }

    private func dim(_ server: LibsharingDeviceServer, locks: Bool, operation: Operation) async throws {
        guard let currentBrightness = Self.currentBrightness else { return }
        defer {
            if !operation.isCancelled, !Self.isDisplayOn { pending = .dim(server, locks: locks) }
        }
        // A lock already handed to HID cannot be cancelled. The preceding operation observes
        // it finishing; if a viewer arrived meanwhile, undo that lock before dimming again.
        if relocked {
            _ = try await setDisplayOn(true, server: server, operation: operation)
            // A submitted wake can outlive this viewer. Observe it before a following restore
            // decides whether to relock, and keep compensation pending if the wake fails.
            guard await waitUntil(timeout: 3, { Self.isDisplayOn }) else { return }
            relocked = false
        }
        if store.value(Dimmed.self, for: Self.dimmedKey) == nil {
            let brightness: Float
            if Self.isDisplayOn, Date() >= settledAt {
                brightness = currentBrightness()
            } else if let userBrightness {
                brightness = userBrightness
            } else {
                guard Self.isDisplayOn else {
                    pending = .dim(server, locks: locks)
                    return
                }
                guard await waitUntil(timeout: 3, operation: operation, { Self.isDisplayOn && Date() >= self.settledAt }) else { return }
                brightness = currentBrightness()
            }
            guard !operation.isCancelled, brightness.isFinite,
                  store.set(Dimmed(brightness: min(1, max(0, brightness)), locks: locks), for: Self.dimmedKey) else { return }
            Self.logger.info("Saving brightness \(brightness, privacy: .public); locks \(locks, privacy: .public)")
        }
        for _ in 0..<2 {
            try await send(server, operation: operation) { try $0.adjustBrightness(-Int32(Self.steps)) }
            guard Self.isDisplayOn else {
                if !operation.isCancelled { pending = .dim(server, locks: locks) }
                return
            }
            if await confirmBrightness(0) {
                Self.logger.info("Brightness dimmed: confirmed zero")
                return
            }
            guard !operation.isCancelled else { return }
        }
        Self.logger.error("Brightness did not reach zero; keeping recovery brightness")
    }

    private func restore(_ server: LibsharingDeviceServer, locks: Bool, wake: Bool, operation: Operation) async throws {
        guard let dimmed = store.value(Dimmed.self, for: Self.dimmedKey), Self.currentBrightness != nil else { return }
        defer {
            if !wake, !operation.isCancelled, !Self.isDisplayOn, store.value(Dimmed.self, for: Self.dimmedKey) != nil {
                pending = .restore(server, locks: false, wake: false)
            }
        }
        let wasOff = !Self.isDisplayOn
        if wasOff {
            guard wake else {
                // The next wake belongs to the user, so it must not trigger another lock.
                pending = .restore(server, locks: false, wake: false)
                return
            }
            guard Self.state(of: Self.displayStatusKey) == 0 else { return }
            _ = try await setDisplayOn(true, server: server, operation: operation)
            guard await waitUntil(timeout: 3, operation: operation, { Self.isDisplayOn }) else {
                Self.logger.error("Display did not wake; keeping recovery brightness")
                return
            }
        }
        guard await waitUntil(timeout: 3, operation: operation, { Self.isDisplayOn && Date() >= self.settledAt }) else { return }
        let targetSteps = Int32((dimmed.brightness * Self.steps).rounded())
        let target = Float(targetSteps) / Self.steps
        Self.logger.info("Restoring brightness \(dimmed.brightness, privacy: .public) from zero; locks \(wasOff || (locks && dimmed.locks), privacy: .public)")
        // A cached BKS value can still precede an earlier dim. Reset and observe zero rather
        // than computing a delta from that value, then observe the target before dropping recovery.
        for _ in 0..<2 {
            try await send(server, operation: operation) { try $0.adjustBrightness(-Int32(Self.steps)) }
            guard await confirmBrightness(0) else {
                if operation.isCancelled { return }
                continue
            }
            guard !operation.isCancelled else { return }
            if targetSteps != 0 {
                try await send(server, operation: operation) { try $0.adjustBrightness(targetSteps) }
                guard await confirmBrightness(target) else {
                    if operation.isCancelled { return }
                    continue
                }
            }
            guard !operation.isCancelled, !watched else { return }
            Self.logger.info("Brightness restored: confirmed \(target, privacy: .public); relock \(wasOff || (locks && dimmed.locks), privacy: .public)")
            userBrightness = target
            if (wasOff || (locks && dimmed.locks)),
               try await setDisplayOn(false, server: server, operation: operation) {
                relocked = true
                if await waitUntil(timeout: 3, { !Self.isDisplayOn }) {
                    // Only a viewer overlapping this submitted lock needs compensation. A later
                    // session wakes through its video stream and must not send another power key.
                    relocked = operation.isCancelled && watched
                } else {
                    Self.logger.error("Display did not turn off after restoring brightness")
                    return
                }
            }
            guard !operation.isCancelled, !watched else { return }
            store.remove(Self.dimmedKey)
            return
        }
        Self.logger.error("Brightness restoration was not confirmed; keeping recovery brightness")
    }

    private func setDisplayOn(_ on: Bool, server: LibsharingDeviceServer, operation: Operation) async throws -> Bool {
        try await send(server, operation: operation) { server in
            // Display notifications lag the physical transition. Home can wake an already-awake
            // display without turning it off; using power here can undo a concurrent video wake.
            guard let state = Self.state(of: Self.displayStatusKey), (state != 0) != on else { return false }
            if on {
                try server.wakeScreen()
            } else {
                try server.lockScreen()
            }
            return true
        }
    }

    private func send<Result: Sendable>(_ server: LibsharingDeviceServer, operation: Operation,
                                       _ body: @escaping @Sendable (LibsharingDeviceServer) throws -> Result) async throws -> Result {
        let box = UncheckedSendable(value: server)
        return try await withCheckedThrowingContinuation { (continuation: CheckedContinuation<Result, Error>) in
            queue.async {
                guard !operation.isCancelled else {
                    continuation.resume(throwing: CancellationError())
                    return
                }
                do {
                    continuation.resume(returning: try body(box.value))
                } catch {
                    continuation.resume(throwing: error)
                }
            }
        }
    }

    private func confirmBrightness(_ target: Float) async -> Bool {
        // BKS reported 0.65 when backboardd committed 0.625 on the test device. Allow that
        // readback rounding for restored levels; zero must still be observed near zero.
        let tolerance: Float = target == 0 ? 0.01 : 0.0251
        var matchingSince: Date?
        return await waitUntil(timeout: 3) {
            guard Self.isDisplayOn, Date() >= self.settledAt,
                  let brightness = Self.currentBrightness?(), abs(brightness - target) < tolerance else {
                matchingSince = nil
                return false
            }
            let now = Date()
            if let matchingSince { return now.timeIntervalSince(matchingSince) >= 0.15 }
            matchingSince = now
            return false
        }
    }

    private func waitUntil(timeout: TimeInterval, operation: Operation? = nil, _ condition: () -> Bool) async -> Bool {
        let deadline = Date().addingTimeInterval(timeout)
        repeat {
            if operation?.isCancelled == true { return false }
            if condition() { return true }
            try? await Task.sleep(seconds: 0.05)
        } while Date() < deadline
        return false
    }

    private func displayChanged() {
        settledAt = Date().addingTimeInterval(Self.settleTime)
        guard Self.isDisplayOn, let pending else { return }
        start(pending)
    }

    private func sample() {
        guard !watched, task == nil, pending == nil, store.value(Dimmed.self, for: Self.dimmedKey) == nil,
              Date() >= settledAt, Self.isDisplayOn, let brightness = Self.currentBrightness?(), brightness.isFinite else { return }
        guard Self.isDisplayOn else { return }
        userBrightness = brightness
    }

    nonisolated private static var isDisplayOn: Bool {
        (state(of: displayStatusKey) ?? 0) != 0
    }

    nonisolated private static func state(of name: String) -> UInt64? {
        var token: Int32 = 0
        guard notify_register_check(name, &token) == NOTIFY_STATUS_OK else { return nil }
        defer { notify_cancel(token) }
        var state: UInt64 = 0
        guard notify_get_state(token, &state) == NOTIFY_STATUS_OK else { return nil }
        return state
    }
}
