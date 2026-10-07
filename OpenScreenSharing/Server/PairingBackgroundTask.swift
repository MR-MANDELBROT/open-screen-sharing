import BackgroundTasks
import Dispatch
import Foundation
import os

final class PairingBackgroundTask {
    private static let timeLimit = 300

    nonisolated private static let logger = Logger(subsystem: Bundle.main.bundleIdentifier ?? "OpenScreenSharing", category: "pairing")

    var onExpire: (() -> Void)?

    var isRunning: Bool { task != nil }

    // AltStore replaces CFBundleIdentifier without rewriting BGTaskSchedulerPermittedIdentifiers;
    // SideStore rewrites both (ResignAppOperation.swift, commits 56854e6 and 0dd743f).
    private let identifier = {
        let permitted = Bundle.main.object(forInfoDictionaryKey: "BGTaskSchedulerPermittedIdentifiers") as? [String]
        let prefix = permitted?.first { $0.hasSuffix(".pairing.*") }?.dropLast() ?? ""
        return String(prefix) + UUID().uuidString
    }()
    private var task: BGTask?
    private var isFinished = false
    private var ticker: Task<Void, Never>?
    private var pendingTitle: (title: String, subtitle: String)?

    func submit(title: String, subtitle: String) {
        guard #available(iOS 27, *) else { return }
        let registered = BGTaskScheduler.shared.register(forTaskWithIdentifier: identifier, using: .main) { @Sendable [weak self] task in
            let task = UncheckedSendable(value: task)
            MainActor.assumeIsolated {
                guard let self else {
                    task.value.setTaskCompleted(success: false)
                    return
                }
                self.launched(task.value)
            }
        }
        guard registered else {
            Self.logger.error("Couldn't register the pairing background task \(self.identifier, privacy: .public)")
            return
        }
        let identifier = identifier
        Task.detached {
            let request = BGContinuedProcessingTaskRequest(identifier: identifier, title: title, subtitle: subtitle)
            request.strategy = .fail
            do {
                try await BGTaskScheduler.shared.submitTaskRequest(request)
            } catch {
                Self.logger.error("Couldn't submit the pairing background task: \(error, privacy: .public)")
            }
        }
    }

    func update(title: String, subtitle: String) {
        if #available(iOS 26, *), let task = task as? BGContinuedProcessingTask {
            task.updateTitle(title, subtitle: subtitle)
        } else {
            pendingTitle = (title, subtitle)
        }
    }

    func finish(success: Bool) {
        guard !isFinished else { return }
        isFinished = true
        ticker?.cancel()
        ticker = nil
        onExpire = nil
        if let task {
            task.setTaskCompleted(success: success)
            self.task = nil
        } else {
            BGTaskScheduler.shared.cancel(taskRequestWithIdentifier: identifier)
        }
    }

    @available(iOS 26, *)
    private func launched(_ task: BGTask) {
        guard !isFinished, let task = task as? BGContinuedProcessingTask else {
            task.setTaskCompleted(success: false)
            return
        }
        self.task = task
        task.expirationHandler = { @Sendable [weak self] in
            DispatchQueue.main.async { self?.expire() }
        }
        if let pendingTitle {
            task.updateTitle(pendingTitle.title, subtitle: pendingTitle.subtitle)
            self.pendingTitle = nil
        }
        task.progress.totalUnitCount = Int64(Self.timeLimit)
        ticker = Task {
            for elapsed in 1...Self.timeLimit {
                try? await Task.sleep(seconds: 1)
                guard !Task.isCancelled else { return }
                task.progress.completedUnitCount = Int64(elapsed)
            }
            expire()
        }
    }

    private func expire() {
        guard !isFinished else { return }
        let onExpire = onExpire
        finish(success: false)
        onExpire?()
    }
}
