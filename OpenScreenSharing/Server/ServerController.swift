import Combine
import Foundation
import Libsharing
import UIKit

final class ServerController: ObservableObject {
    static let shared = ServerController()

    enum Phase: Equatable {
        case stopped
        case starting
        case connecting(ServerProblem?)
        case preparing
        case running(ServerProblem?)
        case failed(ServerProblem)

        var isActive: Bool {
            switch self {
            case .stopped, .failed: false
            case .starting, .connecting, .preparing, .running: true
            }
        }

        var isRunning: Bool {
            if case .running = self { true } else { false }
        }
    }

    @Published private(set) var phase: Phase = .stopped {
        didSet {
            guard phase != oldValue else { return }
            phaseChanged()
        }
    }

    @Published private(set) var advertisedName: String?
    @Published private(set) var tunnelFound: Bool?

    let remotePairing = RemotePairingBrowser()
    let pairingSession = PairingSession()
    private(set) lazy var location: BackgroundLocation = {
        let location = BackgroundLocation()
        location.onUpdatingChange = { [weak self] in
            self?.updateIdleTimer()
        }
        location.onDenied = { [weak self] in
            self?.fail(ServerProblem(kind: .locationDenied, message: ""))
        }
        return location
    }()
    private lazy var audio: BackgroundAudio = {
        let audio = BackgroundAudio()
        audio.onPlayingChange = { [weak self] in
            self?.updateIdleTimer()
        }
        return audio
    }()

    private lazy var volume = LocalVolume()
    private var viewers: Set<Int64> = []

    private var server: LibsharingDeviceServer?
    private var generation = 0
    private var startTask: Task<Void, Never>?
    private var probeTask: Task<Void, Never>?
    private var isBrowsing = false
    private let advertiser = BonjourAdvertiser()
    nonisolated private static let queue = DispatchQueue(label: "io.nekohasekai.screensharing.server", qos: .userInitiated)

    private var settings: ServerSettings { .shared }
    private var pairing: PairingStore { .shared }

    init() {
        remotePairing.onPortChange = { [weak self] port in
            self?.server?.setRemotePairingPort(Int32(port))
        }
        advertiser.onRegister = { [weak self] name in
            self?.advertisedName = name
        }
        advertiser.onFailure = { [weak self] _ in
            self?.advertisedName = nil
        }
    }

    var isActive: Bool { phase.isActive }

    var canStart: Bool {
        pairing.isPaired && settings.isComplete && tunnelFound != false && !DiskImageStore.shared.isImporting
    }

    func startProbing() {
        guard probeTask == nil else { return }
        probeTask = Task {
            while !Task.isCancelled {
                if !phase.isActive {
                    let port = remotePairing.port ?? 0
                    let found = await Task.detached(priority: .utility) {
                        !LibsharingDetectLoopbackAddress(Int32(port)).isEmpty
                    }.value
                    guard !Task.isCancelled else { return }
                    if tunnelFound != found {
                        tunnelFound = found
                    }
                }
                try? await Task.sleep(seconds: 2)
            }
        }
    }

    func stopProbing() {
        probeTask?.cancel()
        probeTask = nil
    }

    private var name: String {
        if let name = pairing.device?.name, !name.isEmpty {
            return name
        }
        return UIDevice.current.name
    }

    func resume() {
        volume.restore()
        guard settings.isEnabled else { return }
        start()
    }

    func start() {
        guard !phase.isActive, canStart, let record = pairing.record else { return }
        if settings.runsInBackground, settings.backgroundMode == .location, location.isDenied {
            fail(ServerProblem(kind: .locationDenied, message: ""))
            return
        }
        settings.isEnabled = true
        generation += 1
        let generation = generation
        phase = .starting
        setBrowsing(true)
        startTask = Task {
            await remotePairing.waitForPort(timeout: 2)
            guard generation == self.generation else { return }
            await launch(record: record, generation: generation)
        }
    }

    func stop() {
        settings.isEnabled = false
        guard phase != .stopped else { return }
        generation += 1
        startTask?.cancel()
        startTask = nil
        closeServer()
        phase = .stopped
    }

    private func launch(record: Data, generation: Int) async {
        let options = LibsharingDeviceServerOptions()
        options.pairingRecord = record
        options.remotePairingPort = Int32(remotePairing.port ?? 0)
        options.port = Int32(settings.port)
        options.noAuthentication = !settings.authenticates
        options.username = settings.effectiveUsername
        options.password = settings.password
        options.diskImageDirectory = DiskImageStore.directory?.path ?? ""
        let events = DeviceServerEvents(
            state: { [weak self] change in
                DispatchQueue.main.async { self?.handle(change, generation: generation) }
            },
            viewer: { [weak self] viewerID, connected in
                DispatchQueue.main.async {
                    guard let self, generation == self.generation else { return }
                    if connected {
                        self.viewers.insert(viewerID)
                    } else {
                        self.viewers.remove(viewerID)
                    }
                    self.volume.setWatched(!self.viewers.isEmpty)
                }
            })
        let request = UncheckedSendable(value: options)
        let result = await withCheckedContinuation { continuation in
            Self.queue.async {
                continuation.resume(returning: Self.makeServer(request.value, events: events))
            }
        }
        switch result {
        case .success(let box):
            guard generation == self.generation else {
                Self.queue.async { box.value.close() }
                return
            }
            if let port = remotePairing.port, Int32(port) != options.remotePairingPort {
                box.value.setRemotePairingPort(Int32(port))
            }
            do {
                try box.value.start()
            } catch {
                Self.queue.async { box.value.close() }
                fail(ServerProblem(kind: .other, message: error.localizedDescription))
                return
            }
            server = box.value
        case .failure(let error):
            guard generation == self.generation else { return }
            fail(ServerProblem(kind: .other, message: error.message))
        }
    }

    nonisolated private static func makeServer(
        _ options: LibsharingDeviceServerOptions, events: DeviceServerEvents
    ) -> Result<UncheckedSendable<LibsharingDeviceServer>, MessageError> {
        LibsharingLog.installed
        var error: NSError?
        guard let server = LibsharingNewDeviceServer(options, events, &error) else {
            return .failure(MessageError(error?.localizedDescription ?? "libsharing returned no server"))
        }
        return .success(UncheckedSendable(value: server))
    }

    private func handle(_ change: DeviceServerStateChange, generation: Int) {
        guard generation == self.generation else { return }
        let problem = change.errorCode == LibsharingDeviceErrorNone ? nil : ServerProblem(code: change.errorCode, message: change.message)
        switch change.state {
        case LibsharingDeviceServerConnecting:
            phase = .connecting(problem)
        case LibsharingDeviceServerPreparing:
            phase = .preparing
        case LibsharingDeviceServerRunning:
            phase = .running(problem)
        case LibsharingDeviceServerFailed:
            fail(problem ?? ServerProblem(kind: .other, message: change.message))
        case LibsharingDeviceServerStopped:
            self.generation += 1
            server = nil
            settings.isEnabled = false
            phase = problem.map(Phase.failed) ?? .stopped
        default:
            break
        }
    }

    private func fail(_ problem: ServerProblem) {
        settings.isEnabled = false
        generation += 1
        startTask?.cancel()
        startTask = nil
        closeServer()
        phase = .failed(problem)
    }

    private func closeServer() {
        guard let server else { return }
        self.server = nil
        let box = UncheckedSendable(value: server)
        Self.queue.async { box.value.close() }
    }

    private func phaseChanged() {
        if !phase.isActive {
            viewers.removeAll()
            volume.restore()
        }
        updateBackground()
        setBrowsing(phase.isActive)
        updateAdvertisement()
    }

    func updateBackground() {
        let mode = phase.isActive && settings.runsInBackground ? settings.backgroundMode : nil
        if mode == .location {
            location.start()
        } else {
            location.stop()
        }
        if mode == .audio {
            audio.start()
        } else {
            audio.stop()
        }
        updateIdleTimer()
    }

    private func updateIdleTimer() {
        SessionActivity.shared.set(self, active: phase.isActive && !location.isUpdating && !audio.isPlaying)
    }

    private func setBrowsing(_ browsing: Bool) {
        guard browsing != isBrowsing else { return }
        isBrowsing = browsing
        if browsing {
            remotePairing.start()
        } else {
            remotePairing.stop()
        }
    }

    private func updateAdvertisement() {
        guard phase.isRunning else {
            advertiser.stop()
            advertisedName = nil
            return
        }
        guard !advertiser.isPublishing else { return }
        advertiser.start(name: name, type: "_rfb._tcp", port: settings.port, txt: ["model": Self.model])
    }

    private static let model: String = {
        var system = utsname()
        uname(&system)
        return withUnsafeBytes(of: &system.machine) { String(decoding: $0.prefix { $0 != 0 }, as: UTF8.self) }
    }()
}
