import Darwin
import Libsharing
import UIKit
import notify
import os

final class LocalBrightness {
    nonisolated private static let logger = Logger(subsystem: Bundle.main.bundleIdentifier ?? "OpenScreenSharing", category: "server")
    private static let dimmedKey = "serverDimmedBrightness"
    nonisolated private static let displayStatusKey = "com.apple.iokit.hid.displayStatus"
    private static let brightnessChangedKey = "UIBacklightLevelChangedNotification"
    // backboardd 24B5110a quantizes unentitled brightness reads to 0.05.
    // RE: /Volumes/Solidigm/RE/24B5110a/backboardd/brightness-api-contract.json
    private static let readbackTolerance: Float = 0.0251
    // backboardd 24B5110a commits an implicit write 1.0 s after the last one and acknowledges
    // nothing; Power before that left the next wake at zero on iPad17,1, whose commit was
    // logged 1.11 s after the write.
    // RE: /Volumes/Solidigm/RE/24B5110a/backboardd/brightness-immediate-path-audit.json
    // RE: /Volumes/Solidigm/RE/24B5110a/backboardd/brightness-late-commit-device-evidence.json
    private static let commitDelay: TimeInterval = 1.5
    // BackBoardServices 24A446/24B5089g initializes the public getter's result to 1
    // and discards this MIG return code. Its server port is framework-owned.
    nonisolated private static let currentBrightness: (@Sendable () -> Float?)? = {
        typealias ServerPort = @convention(c) () -> UInt32
        typealias ReadBrightness = @convention(c) (UInt32, UnsafeMutablePointer<Float>) -> Int32
        guard let handle = dlopen("/System/Library/PrivateFrameworks/BackBoardServices.framework/BackBoardServices", RTLD_NOW),
              let port = dlsym(handle, "BKSHIDServicesServerPort"),
              let read = dlsym(handle, "_BKSHIDGetCurrentDisplayBrightness")
        else { return nil }
        let serverPort = unsafeBitCast(port, to: ServerPort.self)
        let readBrightness = unsafeBitCast(read, to: ReadBrightness.self)
        return {
            let port = serverPort()
            guard port != 0, port != UInt32.max else { return nil }
            var value = Float.nan
            guard readBrightness(port, &value) == KERN_SUCCESS, value.isFinite else { return nil }
            return value
        }
    }()

    // This checked RPC permits applications and preserves backboardd's system target.
    // Raw Set/Restore require an entitlement, and their one-way result hides rejection.
    // RE: /Volumes/Solidigm/RE/24B5110a/backboardd/brightness-api-contract.json
    nonisolated private static let setTemporaryBrightness: (@Sendable (Float) -> Int32)? = {
        typealias ServerPort = @convention(c) () -> UInt32
        typealias SetBrightness = @convention(c) (UInt32, Float) -> Int32
        guard let handle = dlopen("/System/Library/PrivateFrameworks/BackBoardServices.framework/BackBoardServices", RTLD_NOW),
              let port = dlsym(handle, "BKSHIDServicesServerPort"),
              let set = dlsym(handle, "_BKSHIDSetDisplayBrightnessWithImplicitTransaction")
        else { return nil }
        let serverPort = unsafeBitCast(port, to: ServerPort.self)
        let setBrightness = unsafeBitCast(set, to: SetBrightness.self)
        return { value in
            let port = serverPort()
            guard port != 0, port != UInt32.max else { return KERN_FAILURE }
            return setBrightness(port, value)
        }
    }()

    nonisolated private static let backlight = Backlight(observing: true)
    nonisolated private static let backlightChanged = Notification.Name("OpenScreenSharing.backlightChanged")

    nonisolated static var backlightState: Int32 {
        guard backlight != nil, let state = currentDisplayState else { return -1 }
        return Int32(state.rawValue)
    }

    // BLSBacklightState values; BLSBacklightStateIsActive is true for ActiveOn and Dimmed
    // (BacklightServices 24A446, NSStringFromBLSBacklightState 0x22582161c,
    // BLSBacklightStateIsActive 0x2258259c4).
    private nonisolated enum DisplayState: Int {
        case off, inactiveOn, activeOn, dimmed

        var isOn: Bool { self == .activeOn || self == .dimmed }
    }

    private nonisolated struct Dimmed: Codable {
        var brightness: Float
        var locks: Bool
        var preserved: Bool?
        var temporary: Bool?
    }

    private enum Pending {
        case dim(LibsharingDeviceServer, locks: Bool)
        case restore(LibsharingDeviceServer, locks: Bool)
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
    private var confirmedDim: Pending?
    private var operation: Operation?
    private var task: Task<Void, Never>?
    private var dimMonitor: Task<Void, Never>?
    private var userBrightness: Float?
    private var lastBrightnessWrite = -TimeInterval.infinity
    private var displayToken = NOTIFY_TOKEN_INVALID
    private var brightnessToken = NOTIFY_TOKEN_INVALID
    private var backlightObserver: NSObjectProtocol?
    private var activeObserver: NSObjectProtocol?
    private var stateWaiter: CheckedContinuation<Void, Never>?

    init(queue: DispatchQueue) {
        self.queue = queue
        if Self.backlight?.canReadTransitions != true {
            Self.logger.error("Brightness control unavailable: display-transition state cannot be read")
        }
        notify_register_dispatch(Self.displayStatusKey, &displayToken, .main) { [weak self] _ in
            MainActor.assumeIsolated { self?.stateChanged() }
        }
        backlightObserver = NotificationCenter.default.addObserver(forName: Self.backlightChanged, object: nil, queue: .main) { [weak self] _ in
            MainActor.assumeIsolated { self?.stateChanged() }
        }
        // UIKit also uses this Darwin notification to reread BKS. On iPhone18,2 /
        // 24B5110a it fires for brightness-key changes while the app is in the background.
        notify_register_dispatch(Self.brightnessChangedKey, &brightnessToken, .main) { [weak self] _ in
            MainActor.assumeIsolated { self?.stateChanged() }
        }
        activeObserver = NotificationCenter.default.addObserver(forName: UIApplication.didBecomeActiveNotification, object: nil, queue: .main) { [weak self] _ in
            MainActor.assumeIsolated { self?.stateChanged() }
        }
        sample()
    }

    func setWatched(_ watched: Bool, server: LibsharingDeviceServer?) {
        guard let server else {
            self.watched = false
            dimMonitor?.cancel()
            dimMonitor = nil
            operation?.cancel()
            pending = nil
            confirmedDim = nil
            resumeStateWaiter()
            return
        }
        guard watched != self.watched else { return }
        self.watched = watched
        dimMonitor?.cancel()
        dimMonitor = nil
        if watched {
            // Implicit transactions suppress Darwin brightness notifications. A Home or
            // unlock ramp can return to the last notified value without another notification.
            dimMonitor = Task { [weak self] in
                while !Task.isCancelled {
                    do { try await Task.sleep(seconds: 0.25) } catch { return }
                    guard let self, self.watched, !Task.isCancelled else { return }
                    self.stateChanged()
                }
            }
            let locks = !isDisplayOn || (Self.state(of: "com.apple.springboard.lockstate") ?? 0) != 0
            start(.dim(server, locks: locks))
        } else {
            start(.restore(server, locks: true))
        }
    }

    func restore(_ server: LibsharingDeviceServer, locks: Bool) {
        guard !watched else { return }
        start(.restore(server, locks: locks))
    }

    // Keep the server usable until restoration finishes. If the display is off, briefly wake it
    // and put it back to sleep; otherwise recovery would be stranded after closing the server.
    func stop(_ server: LibsharingDeviceServer) -> Task<Void, Never> {
        watched = false
        dimMonitor?.cancel()
        dimMonitor = nil
        return start(.restore(server, locks: false))
    }

    @discardableResult
    private func start(_ action: Pending) -> Task<Void, Never> {
        operation?.cancel()
        pending = nil
        confirmedDim = nil
        resumeStateWaiter()
        let previous = task
        let operation = Operation()
        self.operation = operation
        let task = Task {
            defer {
                if self.operation === operation {
                    self.task = nil
                    self.sample()
                }
            }
            // An HID send only queues events. Finish observing an already-sent command before
            // another session sends more keys, even when its old operation has been cancelled.
            await previous?.value
            guard !operation.isCancelled else { return }
            do {
                switch action {
                case let .dim(server, locks):
                    try await dim(server, locks: locks, operation: operation)
                case let .restore(server, locks):
                    try await restore(server, locks: locks, operation: operation)
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
        guard Self.currentBrightness != nil, Self.setTemporaryBrightness != nil,
              Self.backlight?.canReadTransitions == true else { return }
        defer {
            if !operation.isCancelled, !self.isDisplayOn {
                pending = .dim(server, locks: locks)
            }
        }
        let saved = store.value(Dimmed.self, for: Self.dimmedKey)
        if let saved, saved.preserved == true {
            guard let brightness = readStableBrightness(), brightness > 0.01 else {
                confirmedDim = .dim(server, locks: saved.locks)
                return
            }
        }
        if saved == nil || saved?.preserved == true {
            var brightness = isDisplayOn ? nil : userBrightness
            if brightness == nil {
                guard isDisplayOn else {
                    pending = .dim(server, locks: locks)
                    return
                }
                guard await waitUntil(operation: operation, pollInterval: 0.05, {
                    guard let current = self.readStableBrightness() else { return false }
                    if let original = self.userBrightness,
                       abs(original - current) <= Self.readbackTolerance {
                        brightness = original
                    } else {
                        brightness = current
                    }
                    return true
                }) else { return }
            }
            guard !operation.isCancelled, let brightness,
                  store.set(Dimmed(brightness: min(1, max(0, brightness)), locks: saved?.locks ?? locks,
                                   preserved: brightness <= 0.01, temporary: true), for: Self.dimmedKey) else { return }
            Self.logger.info("Saving brightness \(brightness, privacy: .public); locks \(saved?.locks ?? locks, privacy: .public)")
        }
        guard let dimmed = store.value(Dimmed.self, for: Self.dimmedKey) else { return }
        if dimmed.preserved == true {
            confirmedDim = .dim(server, locks: dimmed.locks)
            return
        }
        guard dimmed.temporary == true else {
            Self.logger.error("Brightness: keeping recovery record from previous brightness-key control")
            return
        }
        guard isDisplayOn else {
            pending = .dim(server, locks: locks)
            return
        }
        // On 24B5110a, Home and unlock ramps can overwrite zero after BLS reports
        // ActiveOn/nontransition, without a Darwin notification under implicit suppression.
        // RE: /Volumes/Solidigm/RE/24B5110a/backboardd/brightness-cycle-recovery-device-evidence.json
        try await setBrightness(0, server: server, operation: operation)
        guard !operation.isCancelled else { return }
        if watched { confirmedDim = .dim(server, locks: locks) }
        Self.logger.info("Brightness dimmed temporarily")
    }

    private func restore(_ server: LibsharingDeviceServer, locks: Bool, operation: Operation) async throws {
        guard let dimmed = store.value(Dimmed.self, for: Self.dimmedKey),
              Self.currentBrightness != nil, Self.backlight?.canReadTransitions == true else { return }
        let shouldRelock = !isDisplayOn || (locks && dimmed.locks)
        guard dimmed.temporary == true else {
            Self.logger.error("Brightness: keeping recovery record from previous brightness-key control")
            return
        }
        if dimmed.preserved != true {
            if !isDisplayOn {
                _ = try await setDisplayOn(true, server: server, operation: operation)
            }
            guard await waitUntil(operation: operation, pollInterval: 0.05, { self.isBrightnessReadable }) else { return }
            // CoreBrightness 24B5110a CBAODModule backs up PendingDisplayNits when entering AOD
            // and writes it back when exiting, unless auto-brightness was on at entry or Domino
            // mode is on. Restore the target before sending Power: Off/wake cycles otherwise
            // repeatedly back up the temporary zero.
            // RE: /Volumes/Solidigm/RE/24B5110a/CoreBrightness/brightness-aod-backup-contract.json
            try await setBrightness(dimmed.brightness, server: server, operation: operation)
            guard await waitUntil(operation: operation, pollInterval: 0.05, {
                guard let brightness = self.readStableBrightness() else { return false }
                return abs(brightness - dimmed.brightness) < Self.readbackTolerance
            }), !operation.isCancelled else { return }
        }
        let committedAt = lastBrightnessWrite + Self.commitDelay
        guard await waitUntil(operation: operation, pollInterval: 0.05, {
            !self.isDisplayOn || ProcessInfo.processInfo.systemUptime >= committedAt
        }), !operation.isCancelled else { return }
        guard ProcessInfo.processInfo.systemUptime >= committedAt else {
            pending = .restore(server, locks: false)
            return
        }
        if shouldRelock {
            guard try await turnDisplayOff(server, operation: operation), !operation.isCancelled else { return }
        }
        guard !operation.isCancelled else { return }
        userBrightness = dimmed.brightness
        store.remove(Self.dimmedKey)
        Self.logger.info("Brightness restored before display sleep: \(dimmed.brightness, privacy: .public)")
    }

    private func setBrightness(_ value: Float, server: LibsharingDeviceServer, operation: Operation) async throws {
        guard let set = Self.setTemporaryBrightness else { throw MessageError("Brightness control unavailable") }
        let result = try await send(server, operation: operation) { _ in set(value) }
        lastBrightnessWrite = ProcessInfo.processInfo.systemUptime
        guard result == KERN_SUCCESS else { throw MessageError("Brightness request failed (\(result))") }
    }

    private func turnDisplayOff(_ server: LibsharingDeviceServer, operation: Operation) async throws -> Bool {
        let offCompletion = try await setDisplayOn(false, server: server, operation: operation)
        let completed = await waitUntil(operation: operation, pollInterval: 0.05) {
            if let offCompletion, Self.backlight?.offCompletion != offCompletion { return true }
            // HID displayStatus turns off before the CoreBrightness mode transition.
            return Self.backlight?.isTransitioning == false &&
                (Self.backlight?.state == DisplayState.off.rawValue || Self.backlight?.state == DisplayState.inactiveOn.rawValue)
        }
        if !completed { Self.logger.error("Display did not finish turning off") }
        return completed
    }

    private func setDisplayOn(_ on: Bool, server: LibsharingDeviceServer, operation: Operation) async throws -> UInt64? {
        try await send(server, operation: operation) { server in
            // Display notifications lag the physical transition. Home can wake an already-awake
            // display without turning it off; using power here can undo a concurrent video wake.
            guard let backlight = Self.backlight,
                  let state = Self.currentDisplayState, state.isOn != on else { return nil }
            let offCompletion = backlight.offCompletion
            if on {
                try server.wakeScreen()
            } else {
                try server.lockScreen()
            }
            return offCompletion
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

    private func waitUntil(operation: Operation, pollInterval: TimeInterval,
                           _ condition: () -> Bool) async -> Bool {
        let wakeTask = Task { [weak self] in
            while !Task.isCancelled {
                do { try await Task.sleep(seconds: pollInterval) } catch { return }
                guard !Task.isCancelled else { return }
                self?.resumeStateWaiter()
            }
        }
        defer { wakeTask.cancel() }
        while !operation.isCancelled {
            if condition() { return true }
            await withCheckedContinuation { stateWaiter = $0 }
        }
        return false
    }

    private func resumeStateWaiter() {
        let waiter = stateWaiter
        stateWaiter = nil
        waiter?.resume()
    }

    private func stateChanged() {
        resumeStateWaiter()
        sample()
        if watched, task == nil, pending == nil, let confirmedDim,
           let brightness = readStableBrightness(), brightness > 0.01 {
            start(confirmedDim)
        }
    }

    private func displayChanged() {
        guard isDisplayOn, let pending else { return }
        start(pending)
    }

    private func sample() {
        displayChanged()
        guard !watched, task == nil, pending == nil, store.value(Dimmed.self, for: Self.dimmedKey) == nil,
              let brightness = readStableBrightness() else { return }
        if UIApplication.shared.applicationState == .active {
            let precise = Float(UIScreen.main.brightness)
            if precise.isFinite, abs(precise - brightness) <= Self.readbackTolerance {
                userBrightness = precise
                return
            }
        }
        userBrightness = brightness
    }

    private func readStableBrightness() -> Float? {
        // BLS completion follows the CoreBrightness display-mode ramp. On iPhone18,2 /
        // 24B5110a, wake readback had reached the active level before that completion.
        guard isBrightnessReadable,
              let brightness = Self.currentBrightness?(), brightness.isFinite else { return nil }
        guard isBrightnessReadable else { return nil }
        return brightness
    }

    private var isBrightnessReadable: Bool {
        isDisplayOn && Self.backlight?.isTransitioning == false
    }

    private var isDisplayOn: Bool {
        Self.currentDisplayState?.isOn == true
    }

    nonisolated private static var currentDisplayState: DisplayState? {
        guard let hidState = state(of: displayStatusKey) else { return nil }
        if let backlight {
            guard let state = DisplayState(rawValue: backlight.state) else { return nil }
            // HID turns off before BLS leaves ActiveOn during the sleep animation.
            return hidState == 0 && state.isOn ? .off : state
        }
        return hidState == 0 ? .off : .activeOn
    }

    nonisolated private static func state(of name: String) -> UInt64? {
        var token: Int32 = 0
        guard notify_register_check(name, &token) == NOTIFY_STATUS_OK else { return nil }
        defer { notify_cancel(token) }
        var state: UInt64 = 0
        guard notify_get_state(token, &state) == NOTIFY_STATUS_OK else { return nil }
        return state
    }

    // BLS distinguishes inactive Always On from the active lock screen; HID displayStatus
    // only reports display availability.
    private nonisolated final class Backlight: NSObject, @unchecked Sendable {
        private typealias GetState = @convention(c) (AnyObject, Selector) -> Int
        private typealias GetTransitioning = @convention(c) (AnyObject, Selector) -> Bool
        private typealias Observe = @convention(c) (AnyObject, Selector, AnyObject) -> Void
        private let controller: NSObject
        private let getState: GetState
        private let getTransitioning: GetTransitioning?
        private let selector = NSSelectorFromString("backlightState")
        private let transitioningSelector = NSSelectorFromString("isTransitioning")
        private let completedOff = LockedState(initialState: UInt64(0))

        init?(observing: Bool) {
            _ = dlopen("/System/Library/PrivateFrameworks/BacklightServices.framework/BacklightServices", RTLD_NOW)
            let sharedSelector = NSSelectorFromString("sharedBacklight")
            guard let type = NSClassFromString("BLSBacklight"),
                  let sharedMethod = class_getClassMethod(type, sharedSelector),
                  let stateMethod = class_getInstanceMethod(type, selector) else { return nil }
            typealias SharedBacklight = @convention(c) (AnyClass, Selector) -> Unmanaged<AnyObject>
            let shared = unsafeBitCast(method_getImplementation(sharedMethod), to: SharedBacklight.self)
            guard let controller = shared(type, sharedSelector).takeUnretainedValue() as? NSObject else { return nil }
            self.controller = controller
            getState = unsafeBitCast(method_getImplementation(stateMethod), to: GetState.self)
            if let method = class_getInstanceMethod(type, transitioningSelector),
               method_getTypeEncoding(method).map({ String(cString: $0) }) == "B16@0:8" {
                getTransitioning = unsafeBitCast(method_getImplementation(method), to: GetTransitioning.self)
            } else {
                getTransitioning = nil
            }
            super.init()
            let addSelector = NSSelectorFromString("addObserver:")
            if observing, let method = class_getInstanceMethod(type, addSelector) {
                let addObserver = unsafeBitCast(method_getImplementation(method), to: Observe.self)
                addObserver(controller, addSelector, self)
            }
        }

        var state: Int { getState(controller, selector) }
        var canReadTransitions: Bool { getTransitioning != nil }
        var isTransitioning: Bool? { getTransitioning?(controller, transitioningSelector) }
        var offCompletion: UInt64 { completedOff.withLock { $0 } }

        @objc(backlight:didCompleteUpdateToState:forEvents:abortedEvents:)
        func backlight(_: AnyObject, didCompleteUpdateToState state: Int, forEvents _: AnyObject?, abortedEvents _: AnyObject?) {
            if state == DisplayState.off.rawValue || state == DisplayState.inactiveOn.rawValue {
                completedOff.withLock { $0 &+= 1 }
            }
            NotificationCenter.default.post(name: LocalBrightness.backlightChanged, object: self)
        }
    }
}
