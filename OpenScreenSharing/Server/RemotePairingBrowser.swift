import Combine
import Foundation
import Libsharing
import Network

// The _remotepairing._tcp authTag authenticates the service identifier with the pairing record’s altIRK
// (idevice d32c818, idevice/src/remote_pairing/peer_device.rs:33-45, idevice/src/remote_pairing/peer_device.rs:129-140).
final class RemotePairingBrowser: ObservableObject {
    static let serviceType = "_remotepairing._tcp"

    @Published private(set) var port: Int?
    var onPortChange: ((Int) -> Void)?

    private var browser: NWBrowser?
    private var resolvers: [String: ServiceResolver] = [:]
    private var services: [String: Service] = [:]
    private var activeClients = 0
    private var restart: Task<Void, Never>?
    private var isReady = false

    private struct Service {
        var txt = NWTXTRecord()
        var port: Int?
        var addresses: Set<Data> = []
    }

    func start() {
        activeClients += 1
        guard browser == nil, restart == nil else { return }
        browse()
    }

    func stop() {
        activeClients = max(0, activeClients - 1)
        guard activeClients == 0 else { return }
        restart?.cancel()
        restart = nil
        cancelBrowsing()
    }

    func waitForPort(timeout: TimeInterval) async {
        let deadline = ProcessInfo.processInfo.systemUptime + timeout
        while port == nil, ProcessInfo.processInfo.systemUptime < deadline, !Task.isCancelled {
            try? await Task.sleep(seconds: 0.1)
        }
    }

    private func browse() {
        isReady = false
        let browser = NWBrowser(for: .bonjourWithTXTRecord(type: Self.serviceType, domain: "local."), using: .tcp)
        browser.stateUpdateHandler = { [weak self, weak browser] state in
            let source = browser
            Task { @MainActor in
                guard let self, self.isCurrent(source) else { return }
                self.handle(state)
            }
        }
        browser.browseResultsChangedHandler = { [weak self, weak browser] results, _ in
            var found: [String: NWTXTRecord] = [:]
            for result in results {
                guard case let .service(name, _, _, _) = result.endpoint else { continue }
                found[name] = if case let .bonjour(txt) = result.metadata { txt } else { NWTXTRecord() }
            }
            let source = browser
            Task { @MainActor in
                guard let self, self.isCurrent(source) else { return }
                self.update(found)
            }
        }
        browser.start(queue: .main)
        self.browser = browser
    }

    private func isCurrent(_ source: NWBrowser?) -> Bool {
        source != nil && source === browser
    }

    private func cancelBrowsing() {
        browser?.cancel()
        browser = nil
        resolvers.values.forEach { $0.cancel() }
        resolvers.removeAll()
        services.removeAll()
    }

    private func handle(_ state: NWBrowser.State) {
        switch state {
        case .ready:
            isReady = true
        case .failed where isReady:
            retry()
        default:
            break
        }
    }

    private func retry() {
        guard activeClients > 0, restart == nil else { return }
        cancelBrowsing()
        restart = Task {
            try? await Task.sleep(seconds: 2)
            guard !Task.isCancelled else { return }
            restart = nil
            guard activeClients > 0 else { return }
            browse()
        }
    }

    private func update(_ found: [String: NWTXTRecord]) {
        for name in resolvers.keys where found[name] == nil {
            resolvers[name]?.cancel()
            resolvers[name] = nil
            services[name] = nil
        }
        for (name, txt) in found {
            services[name, default: Service()].txt = txt
            guard resolvers[name] == nil else { continue }
            let resolver = ServiceResolver(name: name, type: Self.serviceType) { [weak self] _, port in
                self?.services[name]?.port = port
                self?.refresh()
            } addresses: { [weak self] addresses in
                self?.services[name]?.addresses = addresses
                self?.refresh()
            }
            resolvers[name] = resolver
            resolver.start()
        }
        refresh()
    }

    func refresh() {
        let localAddresses = Self.localAddresses()
        let found = services
            .filter { _, service in
                service.port != nil
                    && (Self.isPairedDevice(service.txt) || service.addresses.contains { localAddresses.contains(Self.comparable($0)) })
            }
            .min { $0.key < $1.key }?.value.port
        guard let found, found != port else { return }
        port = found
        onPortChange?(found)
    }

    private static func isPairedDevice(_ txt: NWTXTRecord) -> Bool {
        let pairing = PairingStore.shared
        guard pairing.device?.canMatchService == true, let record = pairing.record,
            let identifier = txt["identifier"], let authTag = txt["authTag"]
        else { return false }
        return LibsharingPairingRecordMatchesService(record, identifier, authTag)
    }

    private static func localAddresses() -> Set<Data> {
        var list: UnsafeMutablePointer<ifaddrs>?
        guard getifaddrs(&list) == 0, let first = list else { return [] }
        defer { freeifaddrs(list) }
        var result: Set<Data> = []
        for entry in sequence(first: first, next: { $0.pointee.ifa_next }) {
            guard Int32(entry.pointee.ifa_flags) & IFF_UP != 0, let address = entry.pointee.ifa_addr,
                let raw = ServiceResolver.rawAddress(UnsafePointer(address))
            else { continue }
            result.insert(comparable(raw))
        }
        return result
    }

    private static func comparable(_ address: Data) -> Data {
        guard address.count == 16, address[0] == 0xfe, address[1] & 0xc0 == 0x80 else { return address }
        var address = address
        address[2] = 0
        address[3] = 0
        return address
    }
}
