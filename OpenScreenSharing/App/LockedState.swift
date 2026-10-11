import Foundation

nonisolated final class LockedState<State>: @unchecked Sendable {
    private let lock = NSLock()
    private var state: State

    init(initialState: State) {
        state = initialState
    }

    func withLock<R>(_ body: (inout State) throws -> R) rethrows -> R {
        lock.lock()
        defer { lock.unlock() }
        return try body(&state)
    }
}

nonisolated struct UncheckedSendable<Value>: @unchecked Sendable {
    let value: Value
}

extension Task where Success == Never, Failure == Never {
    nonisolated static func sleep(seconds: TimeInterval) async throws {
        try await sleep(nanoseconds: UInt64(seconds * 1_000_000_000))
    }
}
