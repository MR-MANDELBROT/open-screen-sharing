import Combine
import Foundation
import Network

nonisolated enum ComputerAvailability: Hashable, Sendable {
    case available
    case unavailable
}

/// With its blacklist enabled, TigerVNC counts banner-only connections until authentication succeeds
/// (tigervnc 086c692, common/rfb/Blacklist.cxx:52-89, common/rfb/VNCServerST.cxx:141-161,
/// common/rfb/VNCServerST.cxx:741-745).
final class AvailabilityMonitor: ObservableObject {
    @Published private(set) var availability: [ComputerAddress: ComputerAvailability] = [:]

    private let nearby: NearbyBrowser
    private var servers: [ComputerAddress: RemoteProtocol] = [:]
    private var probes: [ComputerAddress: Task<Void, Never>] = [:]
    private var activeClients = 0
    private var pingedServers: Set<String>
    private var nearbyChanges: AnyCancellable?
    private static let pingedServersKey = "availabilityPingedServers"

    init(nearby: NearbyBrowser) {
        self.nearby = nearby
        pingedServers = Set(PreferenceStore.shared.value([String].self, for: Self.pingedServersKey) ?? [])
        nearbyChanges = nearby.objectWillChange.sink { [weak self] in self?.objectWillChange.send() }
    }

    func availability(of address: ComputerAddress) -> ComputerAvailability? {
        servers[address].flatMap { nearby.computer(matching: address, remoteProtocol: $0) } != nil ? .available : availability[address]
    }

    func start() {
        activeClients += 1
        guard activeClients == 1 else { return }
        for address in servers.keys {
            probe(address)
        }
    }

    func stop() {
        activeClients = max(0, activeClients - 1)
        guard activeClients == 0 else { return }
        probes.values.forEach { $0.cancel() }
        probes.removeAll()
    }

    func update(servers: [ComputerAddress: RemoteProtocol]) {
        for address in self.servers.keys where servers[address] == nil {
            probes[address]?.cancel()
            probes[address] = nil
            availability[address] = nil
        }
        let changed = servers.keys.filter { self.servers[$0] != servers[$0] }
        self.servers = servers
        guard activeClients > 0 else { return }
        for address in changed {
            availability[address] = nil
            probe(address)
        }
    }

    private func probe(_ address: ComputerAddress) {
        probes[address]?.cancel()
        probes[address] = Task {
            try? await Task.sleep(seconds: 1)
            while !Task.isCancelled {
                let result = await check(address)
                guard !Task.isCancelled else { return }
                if availability[address] != result {
                    availability[address] = result
                }
                try? await Task.sleep(seconds: 1)
            }
        }
    }

    private func check(_ address: ComputerAddress) async -> ComputerAvailability {
        if servers[address] == .rdp {
            return await AvailabilityProbe.check(address, remoteProtocol: .rdp) == .nothing ? .unavailable : .available
        }
        if nearby.computer(matching: address, remoteProtocol: .vnc) != nil {
            return .available
        }
        if address.isLocal, nearby.state == .browsing, nearby.host(matching: address) == nil {
            return .unavailable
        }
        if pingedServers.contains(address.serverKey) {
            return await PingProbe.check(address.host) ? .available : .unavailable
        }
        switch await AvailabilityProbe.check(address, remoteProtocol: .vnc) {
        case .mac:
            return .available
        case .otherServer:
            rememberPingedServer(address)
            return .available
        case .nothing:
            return .unavailable
        }
    }

    private func rememberPingedServer(_ address: ComputerAddress) {
        guard pingedServers.insert(address.serverKey).inserted else { return }
        PreferenceStore.shared.set(pingedServers.sorted(), for: Self.pingedServersKey)
    }
}

extension ComputerAddress {
    fileprivate var isLocal: Bool {
        if bonjourServiceName != nil { return true }
        let lowered = host.lowercased()
        return lowered.hasSuffix(".local") || lowered.hasSuffix(".local.")
    }

    fileprivate var serverKey: String {
        "\(host.lowercased()):\(port)"
    }
}

nonisolated enum AvailabilityProbe {
    enum Answer: Sendable {
        /// Apple's Screen Sharing, which announces RFB 3.889 (screensharingd 756.36.5.2, macOS 27.0.1 26A434,
        /// UUID 44908B06-F788-3822-ABC4-2285001F6A8D, sub_10002fcb4 0x10003061c..0x10003062c).
        case mac
        case otherServer
        case nothing
    }

    private static let queue = DispatchQueue(label: "io.nekohasekai.screensharing.availability", qos: .utility)
    private static let timeout: TimeInterval = 3

    static func check(_ address: ComputerAddress, remoteProtocol: RemoteProtocol) async -> Answer {
        let attempt = Attempt(address: address, remoteProtocol: remoteProtocol)
        return await withTaskCancellationHandler {
            await withCheckedContinuation { continuation in
                attempt.start(continuation)
            }
        } onCancel: {
            attempt.finish(.nothing)
        }
    }

    private static func answer(for banner: Data?) -> Answer {
        guard let banner, banner.starts(with: "RFB ".utf8) else { return .nothing }
        return banner.starts(with: "RFB 003.889".utf8) ? .mac : .otherServer
    }

    private final class Attempt: @unchecked Sendable {
        private let lock = NSLock()
        private let connection: NWConnection
        private let remoteProtocol: RemoteProtocol
        private var continuation: CheckedContinuation<Answer, Never>?

        init(address: ComputerAddress, remoteProtocol: RemoteProtocol) {
            self.remoteProtocol = remoteProtocol
            let tcp = NWProtocolTCP.Options()
            tcp.connectionTimeout = Int(AvailabilityProbe.timeout)
            connection = NWConnection(
                host: NWEndpoint.Host(address.host),
                port: NWEndpoint.Port(integerLiteral: UInt16(clamping: address.port)),
                using: NWParameters(tls: nil, tcp: tcp)
            )
        }

        func start(_ continuation: CheckedContinuation<Answer, Never>) {
            lock.withLock { self.continuation = continuation }
            connection.stateUpdateHandler = { [self] state in
                switch state {
                case .ready where remoteProtocol == .rdp:
                    finish(.otherServer)
                case .ready:
                    connection.receive(minimumIncompleteLength: 12, maximumLength: 12) { [self] data, _, _, _ in
                        finish(AvailabilityProbe.answer(for: data))
                    }
                case .waiting, .failed, .cancelled:
                    finish(.nothing)
                default:
                    break
                }
            }
            connection.start(queue: AvailabilityProbe.queue)
            AvailabilityProbe.queue.asyncAfter(deadline: .now() + AvailabilityProbe.timeout) { [self] in
                finish(.nothing)
            }
        }

        func finish(_ answer: Answer) {
            let continuation = lock.withLock {
                defer { self.continuation = nil }
                return self.continuation
            }
            guard let continuation else { return }
            connection.cancel()
            continuation.resume(returning: answer)
        }
    }
}

nonisolated enum PingProbe {
    private static let timeout: TimeInterval = 3
    private static let identifier = UInt16.random(in: 1...UInt16.max)
    private static let sequence = LockedState(initialState: UInt16(0))

    static func check(_ host: String) async -> Bool {
        await withCheckedContinuation { continuation in
            DispatchQueue.global(qos: .utility).async {
                continuation.resume(returning: ping(host))
            }
        }
    }

    private static func ping(_ host: String) -> Bool {
        var hints = addrinfo()
        hints.ai_socktype = SOCK_DGRAM
        var list: UnsafeMutablePointer<addrinfo>?
        guard getaddrinfo(host, nil, &hints, &list) == 0, let list else { return false }
        defer { freeaddrinfo(list) }
        var entry: UnsafeMutablePointer<addrinfo>? = list
        while let current = entry {
            if current.pointee.ai_family == AF_INET || current.pointee.ai_family == AF_INET6, ping(current.pointee) {
                return true
            }
            entry = current.pointee.ai_next
        }
        return false
    }

    private static func ping(_ entry: addrinfo) -> Bool {
        let isIPv6 = entry.ai_family == AF_INET6
        let socket = Darwin.socket(entry.ai_family, SOCK_DGRAM, isIPv6 ? IPPROTO_ICMPV6 : IPPROTO_ICMP)
        guard socket >= 0 else { return false }
        defer { close(socket) }
        var wait = timeval(tv_sec: Int(timeout), tv_usec: 0)
        setsockopt(socket, SOL_SOCKET, SO_RCVTIMEO, &wait, socklen_t(MemoryLayout<timeval>.size))

        let sequence = Self.sequence.withLock { value in
            value &+= 1
            return value
        }
        var request = [UInt8](repeating: 0, count: 64)
        request[0] = isIPv6 ? 128 : 8
        request[4] = UInt8(identifier >> 8)
        request[5] = UInt8(identifier & 0xff)
        request[6] = UInt8(sequence >> 8)
        request[7] = UInt8(sequence & 0xff)
        for index in 8..<request.count {
            request[index] = UInt8(truncatingIfNeeded: index)
        }
        if !isIPv6 {
            let sum = checksum(request)
            request[2] = UInt8(sum >> 8)
            request[3] = UInt8(sum & 0xff)
        }
        guard sendto(socket, request, request.count, 0, entry.ai_addr, entry.ai_addrlen) == request.count else { return false }

        let deadline = Date().addingTimeInterval(timeout)
        var reply = [UInt8](repeating: 0, count: 2048)
        while Date() < deadline {
            let received = recv(socket, &reply, reply.count, 0)
            guard received > 0 else { return false }
            var offset = 0
            if !isIPv6 {
                guard received >= 20 else { continue }
                offset = Int(reply[0] & 0x0f) * 4
            }
            guard received >= offset + 8, reply[offset] == (isIPv6 ? 129 : 0) else { continue }
            let replyIdentifier = UInt16(reply[offset + 4]) << 8 | UInt16(reply[offset + 5])
            let replySequence = UInt16(reply[offset + 6]) << 8 | UInt16(reply[offset + 7])
            if replyIdentifier == identifier, replySequence == sequence {
                return true
            }
        }
        return false
    }

    private static func checksum(_ bytes: [UInt8]) -> UInt16 {
        var sum: UInt32 = 0
        var index = 0
        while index + 1 < bytes.count {
            sum += UInt32(bytes[index]) << 8 | UInt32(bytes[index + 1])
            index += 2
        }
        if index < bytes.count {
            sum += UInt32(bytes[index]) << 8
        }
        while sum >> 16 != 0 {
            sum = (sum & 0xffff) + (sum >> 16)
        }
        return ~UInt16(sum)
    }
}
