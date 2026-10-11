import Combine
import Foundation
import Libsharing
import UIKit
import dnssd

final class PairingSession: ObservableObject {
    private static let timeLimit = 300

    enum State: Equatable {
        case idle
        case waiting
        case code(String)
        case failed(ServerProblem)
    }

    @Published private(set) var state: State = .idle

    let hostName: String = {
        let info = Bundle.main.localizedInfoDictionary ?? Bundle.main.infoDictionary
        return info?["CFBundleDisplayName"] as? String ?? "Screen Sharing"
    }()

    private var settingsPath: String {
        String(localized: "Privacy & Security › Developer Mode")
    }

    private var responder: LibsharingPairingResponder?
    private let advertiser = BonjourAdvertiser()
    private var continuedTask: ContinuedTask?
    private var ticker: Task<Void, Never>?
    private var backgroundTask: UIBackgroundTaskIdentifier = .invalid
    private var observers: [NSObjectProtocol] = []

    var isInProgress: Bool {
        switch state {
        case .waiting, .code: true
        default: false
        }
    }

    func start() {
        guard responder == nil else { return }
        LibsharingLog.installed
        let events = PairingEvents { [weak self] event in
            DispatchQueue.main.async { self?.handle(event) }
        }
        let store = PairingStore.shared
        let identity = store.device == nil ? nil : store.record
        var error: NSError?
        guard let responder = LibsharingNewPairingResponderWithRecord(hostName, identity, events, &error) else {
            state = .failed(ServerProblem(kind: .pairing, message: error?.localizedDescription ?? "libsharing returned no pairing responder"))
            return
        }
        self.responder = responder
        advertiser.onFailure = { [weak self] errorCode in
            self?.fail(ServerProblem(kind: .pairing, message: Self.publishingMessage(errorCode)))
        }
        var txt: [String: String] = [:]
        if let record = responder.txtRecord() {
            for index in 0..<record.len() {
                txt[record.key(index)] = record.value(index)
            }
        }
        let published = advertiser.start(
            name: responder.serviceName(), type: responder.serviceType(), port: Int(responder.port()), txt: txt)
        guard published == kDNSServiceErr_NoError else {
            fail(ServerProblem(kind: .pairing, message: Self.publishingMessage(published)))
            return
        }
        responder.start()
        state = .waiting
        let continuedTask = ContinuedTask(kind: "pairing")
        continuedTask.onExpire = { [weak self] in self?.backgroundTimeRanOut() }
        continuedTask.submit(title: String(localized: "Pair in Settings"), subtitle: settingsPath)
        self.continuedTask = continuedTask
        ticker = Task { [weak self] in
            for elapsed in 1...Self.timeLimit {
                try? await Task.sleep(seconds: 1)
                guard !Task.isCancelled else { return }
                continuedTask.setProgress(completed: Int64(elapsed), total: Int64(Self.timeLimit))
            }
            guard continuedTask.isRunning else { return }
            continuedTask.finish(success: false)
            self?.backgroundTimeRanOut()
        }
        watchAppState()
    }

    func acknowledgeFailure() {
        if case .failed = state {
            state = .idle
        }
    }

    func cancel() {
        guard responder != nil else { return }
        finish()
        state = .idle
    }

    private func handle(_ event: PairingEvent) {
        guard responder != nil else { return }
        switch event {
        case .pin(let pin):
            state = .code(pin)
            continuedTask?.update(title: String(localized: "Pairing Code: \(Self.grouped(pin))"), subtitle: settingsPath)
        case .paired(let record):
            do {
                try PairingStore.shared.save(record)
            } catch {
                fail(ServerProblem(kind: .pairing, message: error.localizedDescription))
                return
            }
            continuedTask?.update(title: String(localized: "Paired"), subtitle: String(localized: "Tap to return to Screen Sharing"))
            finish(succeeded: true)
            state = .idle
        case .failed(let errorCode, let message):
            fail(ServerProblem(code: errorCode, message: message))
        }
    }

    private func fail(_ problem: ServerProblem) {
        continuedTask?.update(title: problem.title, subtitle: problem.message)
        finish()
        state = .failed(problem)
    }

    private func finish(succeeded: Bool = false) {
        advertiser.stop()
        if let responder {
            let box = UncheckedSendable(value: responder)
            DispatchQueue.global(qos: .userInitiated).async { box.value.close() }
        }
        responder = nil
        for observer in observers {
            NotificationCenter.default.removeObserver(observer)
        }
        observers.removeAll()
        ticker?.cancel()
        ticker = nil
        continuedTask?.finish(success: succeeded)
        continuedTask = nil
        endBackgroundTask()
    }

    private func backgroundTimeRanOut() {
        guard isInProgress, UIApplication.shared.applicationState == .background else { return }
        fail(ServerProblem(kind: .pairing, message: String(localized: "Screen Sharing couldn’t keep waiting for Settings in the background.")))
    }

    static func grouped(_ pin: String) -> String {
        var groups: [Substring] = []
        var rest = Substring(pin)
        while !rest.isEmpty {
            groups.append(rest.prefix(3))
            rest = rest.dropFirst(3)
        }
        return groups.joined(separator: " ")
    }

    private static func publishingMessage(_ errorCode: DNSServiceErrorType) -> String {
        if errorCode == kDNSServiceErr_PolicyDenied {
            return String(localized: "Screen Sharing isn’t allowed to use the local network. Allow it in Settings › Privacy & Security › Local Network.")
        }
        return String(localized: "Couldn’t publish the pairing service (error \(errorCode)).")
    }

    private func watchAppState() {
        let center = NotificationCenter.default
        observers.append(
            center.addObserver(forName: UIApplication.didEnterBackgroundNotification, object: nil, queue: .main) { [weak self] _ in
                MainActor.assumeIsolated { self?.beginBackgroundTask() }
            })
        observers.append(
            center.addObserver(forName: UIApplication.willEnterForegroundNotification, object: nil, queue: .main) { [weak self] _ in
                MainActor.assumeIsolated { self?.endBackgroundTask() }
            })
    }

    private func beginBackgroundTask() {
        guard isInProgress, backgroundTask == .invalid else { return }
        backgroundTask = UIApplication.shared.beginBackgroundTask(withName: "Pairing") { [weak self] in
            guard let self else { return }
            endBackgroundTask()
            if continuedTask?.isRunning != true {
                backgroundTimeRanOut()
            }
        }
    }

    private func endBackgroundTask() {
        guard backgroundTask != .invalid else { return }
        UIApplication.shared.endBackgroundTask(backgroundTask)
        backgroundTask = .invalid
    }
}
