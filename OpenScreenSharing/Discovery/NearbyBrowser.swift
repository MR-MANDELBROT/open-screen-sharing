import Combine
import Foundation
import Network
import dnssd

struct NearbyComputer: Identifiable, Hashable {
    var id: String { "\(remoteProtocol.rawValue):\(name)" }
    var name: String
    var remoteProtocol = RemoteProtocol.vnc
    var hostName: String?
    var port: Int?
    var model: String?
    var addresses: Set<Data> = []

    var address: ComputerAddress? {
        guard let hostName, let port else { return nil }
        return ComputerAddress(host: hostName, port: port)
    }
}

final class NearbyBrowser: ObservableObject {
    enum State: Equatable {
        case idle
        case browsing
        case failed(String)
    }

    @Published private(set) var computers: [NearbyComputer] = []
    @Published private(set) var state: State = .idle
    @Published private(set) var hasSearched = false

    private var browser: NWBrowser?
    private var windowsBrowser: NWBrowser?
    private var resolvers: [String: ServiceResolver] = [:]
    private var windowsResolvers: [String: ServiceResolver] = [:]
    private var windowsProbes: [String: Task<Void, Never>] = [:]
    private var models: [String: String] = [:]
    private var serviceModels: [String: String] = [:]
    private var activeClients = 0
    private var restart: Task<Void, Never>?
    private var search: Task<Void, Never>?
    private var isReady = false
    private var hasResults = false
    private var isFixed = false

    /// NWBrowser can reach .ready before any results callback (observed on macOS 27.0.1 26A434).
    private static let searchTime: TimeInterval = 2

    #if DEBUG || DEMO
    convenience init(fixed computers: [NearbyComputer]) {
        self.init()
        self.computers = computers
        state = .browsing
        hasSearched = true
        isFixed = true
    }
    #endif

    func start() {
        guard !isFixed else { return }
        activeClients += 1
        guard browser == nil else { return }
        browse()
        search = Task {
            try? await Task.sleep(seconds: Self.searchTime)
            guard !Task.isCancelled else { return }
            hasSearched = true
        }
    }

    private func browse() {
        isReady = false
        hasResults = false
        let parameters = NWParameters.tcp
        parameters.includePeerToPeer = true
        let browser = NWBrowser(for: .bonjourWithTXTRecord(type: "_rfb._tcp", domain: "local."), using: parameters)
        browser.stateUpdateHandler = { [weak self] newState in
            Task { @MainActor in self?.handle(newState) }
        }
        browser.browseResultsChangedHandler = { [weak self] results, _ in
            var names: [String] = []
            var models: [String: String] = [:]
            for result in results {
                guard case let .service(name, _, _, _) = result.endpoint,
                    !result.interfaces.contains(where: { $0.type == .loopback })
                else { continue }
                names.append(name)
                if case let .bonjour(record) = result.metadata, let model = record["model"], !model.isEmpty {
                    models[name] = model
                }
            }
            Task { @MainActor in self?.update(names: names, models: models) }
        }
        browser.start(queue: .main)
        self.browser = browser
        browseWindows(using: parameters)
        state = .browsing
    }

    // Windows advertises Delivery Optimization's peer cache on _dosvc._tcp by default, never Remote Desktop
    // (observed on Windows 11 26100 and 26200).
    private func browseWindows(using parameters: NWParameters) {
        let browser = NWBrowser(for: .bonjour(type: "_dosvc._tcp", domain: "local."), using: parameters)
        browser.stateUpdateHandler = { [weak self] newState in
            Task { @MainActor in self?.handle(newState) }
        }
        browser.browseResultsChangedHandler = { [weak self] results, _ in
            let names = results.compactMap { result -> String? in
                guard case let .service(name, _, _, _) = result.endpoint,
                    !result.interfaces.contains(where: { $0.type == .loopback })
                else { return nil }
                return name
            }
            Task { @MainActor in self?.updateWindows(names: Set(names)) }
        }
        browser.start(queue: .main)
        windowsBrowser = browser
    }

    private func updateWindows(names: Set<String>) {
        for name in windowsResolvers.keys where !names.contains(name) {
            windowsResolvers.removeValue(forKey: name)?.cancel()
            windowsProbes.removeValue(forKey: name)?.cancel()
            computers.removeAll { $0.remoteProtocol == .rdp && $0.name == name }
        }
        for name in names where windowsResolvers[name] == nil {
            let resolver = ServiceResolver(name: name, type: "_dosvc._tcp") { [weak self] hostName, _ in
                self?.probeWindows(name: name, hostName: hostName)
            } addresses: { [weak self] addresses in
                guard let self, let index = computers.firstIndex(where: { $0.remoteProtocol == .rdp && $0.name == name }) else {
                    return
                }
                computers[index].addresses = addresses
            }
            windowsResolvers[name] = resolver
            resolver.start()
        }
    }

    private func probeWindows(name: String, hostName: String) {
        windowsProbes[name]?.cancel()
        windowsProbes[name] = Task {
            let address = ComputerAddress(host: hostName, port: RemoteProtocol.rdp.defaultPort)
            let answer = await AvailabilityProbe.check(address, remoteProtocol: .rdp)
            guard !Task.isCancelled else { return }
            windowsProbes[name] = nil
            let index = computers.firstIndex { $0.remoteProtocol == .rdp && $0.name == name }
            guard answer != .nothing else {
                if let index { computers.remove(at: index) }
                return
            }
            var computer = index.map { computers[$0] } ?? NearbyComputer(name: name, remoteProtocol: .rdp)
            computer.hostName = hostName
            computer.port = address.port
            if let index {
                computers[index] = computer
            } else {
                computers.append(computer)
                computers.sort { $0.name.localizedStandardCompare($1.name) == .orderedAscending }
            }
        }
    }

    func stop() {
        guard !isFixed else { return }
        activeClients = max(0, activeClients - 1)
        guard activeClients == 0 else { return }
        restart?.cancel()
        restart = nil
        search?.cancel()
        search = nil
        cancelBrowsing()
        state = .idle
        hasSearched = false
    }

    private func cancelBrowsing() {
        browser?.cancel()
        browser = nil
        windowsBrowser?.cancel()
        windowsBrowser = nil
        resolvers.values.forEach { $0.cancel() }
        resolvers.removeAll()
        windowsResolvers.values.forEach { $0.cancel() }
        windowsResolvers.removeAll()
        windowsProbes.values.forEach { $0.cancel() }
        windowsProbes.removeAll()
        models.removeAll()
    }

    private func restartBrowsing() {
        restart?.cancel()
        restart = Task {
            try? await Task.sleep(seconds: 1)
            guard !Task.isCancelled else { return }
            cancelBrowsing()
            browse()
            try? await Task.sleep(seconds: Self.searchTime)
            guard !Task.isCancelled, !hasResults else { return }
            update(names: [])
        }
    }

    func computer(matching address: ComputerAddress, remoteProtocol: RemoteProtocol) -> NearbyComputer? {
        computers.first {
            $0.remoteProtocol == remoteProtocol && ($0.port == nil || $0.port == address.port) && Self.matches($0, address)
        }
    }

    func host(matching address: ComputerAddress) -> NearbyComputer? {
        computers.first { Self.matches($0, address) }
    }

    private static func matches(_ computer: NearbyComputer, _ address: ComputerAddress) -> Bool {
        let host = normalized(address.host)
        if let hostName = computer.hostName, normalized(hostName) == host { return true }
        if let raw = rawAddress(of: address.host), computer.addresses.contains(raw) { return true }
        return normalized(computer.name) == host || address.bonjourServiceName == computer.name
    }

    private static func normalized(_ host: String) -> String {
        var value = host.lowercased()
        if value.hasSuffix(".") { value.removeLast() }
        if value.hasSuffix(".local") { value.removeLast(6) }
        return value
    }

    private static func rawAddress(of host: String) -> Data? {
        IPv4Address(host)?.rawValue ?? IPv6Address(host)?.rawValue
    }

    private func handle(_ newState: NWBrowser.State) {
        switch newState {
        case .failed(.dns(let code))
        where isReady && (code == kDNSServiceErr_DefunctConnection || code == kDNSServiceErr_ServiceNotRunning):
            restartBrowsing()
        case .failed(let error), .waiting(let error):
            state = .failed(error.localizedDescription)
        case .ready:
            isReady = true
            state = .browsing
        default:
            break
        }
    }

    private func update(model: String, for name: String) {
        guard models[name] != model else { return }
        models[name] = model
        guard let index = computers.firstIndex(where: { $0.remoteProtocol == .vnc && $0.name == name }) else { return }
        computers[index].model = self.model(of: name)
    }

    private func model(of name: String) -> String? {
        serviceModels[name] ?? models[name]
    }

    private func update(names: [String], models: [String: String] = [:]) {
        hasResults = true
        serviceModels = models
        let unique = Set(names)
        for name in resolvers.keys where !unique.contains(name) {
            resolvers[name]?.cancel()
            resolvers[name] = nil
            self.models[name] = nil
        }
        let macs = unique.map { name in
            var computer = computers.first { $0.remoteProtocol == .vnc && $0.name == name } ?? NearbyComputer(name: name)
            computer.model = model(of: name)
            return computer
        }
        computers = (macs + computers.filter { $0.remoteProtocol == .rdp }).sorted {
            $0.name.localizedStandardCompare($1.name) == .orderedAscending
        }
        for name in unique where resolvers[name] == nil {
            let resolver = ServiceResolver(name: name) { [weak self] hostName, port in
                guard let self, let index = computers.firstIndex(where: { $0.remoteProtocol == .vnc && $0.name == name }) else { return }
                computers[index].hostName = hostName
                computers[index].port = port
            } addresses: { [weak self] addresses in
                guard let self, let index = computers.firstIndex(where: { $0.remoteProtocol == .vnc && $0.name == name }) else { return }
                computers[index].addresses = addresses
            } model: { [weak self] model in
                self?.update(model: model, for: name)
            }
            resolvers[name] = resolver
            resolver.start()
        }
    }
}

final class ServiceResolver {
    private let name: String
    private let type: String
    private let resolved: (String, Int) -> Void
    private let addressesChanged: ((Set<Data>) -> Void)?
    private let modelChanged: ((String) -> Void)?
    private var reference: DNSServiceRef?
    private var addressReference: DNSServiceRef?
    private var modelReference: DNSServiceRef?
    private var addressHost: String?
    private var addresses: Set<Data> = []

    init(
        name: String, type: String = "_rfb._tcp", resolved: @escaping (String, Int) -> Void,
        addresses: ((Set<Data>) -> Void)? = nil, model: ((String) -> Void)? = nil
    ) {
        self.name = name
        self.type = type
        self.resolved = resolved
        addressesChanged = addresses
        modelChanged = model
    }

    func start() {
        let context = Unmanaged.passUnretained(self).toOpaque()
        var serviceReference: DNSServiceRef?
        let error = DNSServiceResolve(
            &serviceReference, 0, 0, name, type, "local.",
            { _, _, _, errorCode, _, hostTarget, port, _, _, context in
                guard errorCode == kDNSServiceErr_NoError, let hostTarget, let context else { return }
                let resolver = Unmanaged<ServiceResolver>.fromOpaque(context).takeUnretainedValue()
                let qualifiedHostName = String(cString: hostTarget)
                var hostName = qualifiedHostName
                if hostName.hasSuffix(".") { hostName.removeLast() }
                let hostPort = Int(UInt16(bigEndian: port))
                MainActor.assumeIsolated {
                    resolver.resolved(hostName, hostPort)
                    resolver.lookUpAddresses(of: qualifiedHostName)
                }
            }, context)
        guard error == kDNSServiceErr_NoError, let serviceReference else { return }
        reference = serviceReference
        DNSServiceSetDispatchQueue(serviceReference, .main)
        lookUpModel()
    }

    /// A Mac answered a named _device-info._tcp TXT query with its model while a service browse found nothing
    /// (observed on macOS 27.0.1 26A434).
    private func lookUpModel() {
        guard modelChanged != nil else { return }
        var fullName = [CChar](repeating: 0, count: Int(kDNSServiceMaxDomainName))
        guard DNSServiceConstructFullName(&fullName, name, "_device-info._tcp", "local.") == kDNSServiceErr_NoError else {
            return
        }
        let context = Unmanaged.passUnretained(self).toOpaque()
        var serviceReference: DNSServiceRef?
        let error = DNSServiceQueryRecord(
            &serviceReference, 0, 0, fullName, UInt16(kDNSServiceType_TXT), UInt16(kDNSServiceClass_IN),
            { _, flags, _, errorCode, _, _, _, length, data, _, context in
                guard errorCode == kDNSServiceErr_NoError, flags & DNSServiceFlags(kDNSServiceFlagsAdd) != 0, let data,
                    let context
                else { return }
                var valueLength: UInt8 = 0
                guard let value = TXTRecordGetValuePtr(length, data, "model", &valueLength), valueLength > 0 else { return }
                let model = String(decoding: UnsafeRawBufferPointer(start: value, count: Int(valueLength)), as: UTF8.self)
                let resolver = Unmanaged<ServiceResolver>.fromOpaque(context).takeUnretainedValue()
                MainActor.assumeIsolated {
                    resolver.modelChanged?(model)
                }
            }, context)
        guard error == kDNSServiceErr_NoError, let serviceReference else { return }
        modelReference = serviceReference
        DNSServiceSetDispatchQueue(serviceReference, .main)
    }

    private func lookUpAddresses(of host: String) {
        guard addressesChanged != nil, addressHost != host else { return }
        if let addressReference {
            DNSServiceRefDeallocate(addressReference)
        }
        addressReference = nil
        addressHost = host
        addresses = []
        let context = Unmanaged.passUnretained(self).toOpaque()
        var serviceReference: DNSServiceRef?
        let error = DNSServiceGetAddrInfo(
            &serviceReference, 0, 0, DNSServiceProtocol(kDNSServiceProtocol_IPv4 | kDNSServiceProtocol_IPv6), host,
            { _, flags, _, errorCode, _, address, _, context in
                guard errorCode == kDNSServiceErr_NoError, let address, let context,
                    let raw = ServiceResolver.rawAddress(address)
                else { return }
                let resolver = Unmanaged<ServiceResolver>.fromOpaque(context).takeUnretainedValue()
                let added = flags & DNSServiceFlags(kDNSServiceFlagsAdd) != 0
                let moreComing = flags & DNSServiceFlags(kDNSServiceFlagsMoreComing) != 0
                MainActor.assumeIsolated {
                    if added {
                        resolver.addresses.insert(raw)
                    } else {
                        resolver.addresses.remove(raw)
                    }
                    if !moreComing {
                        resolver.addressesChanged?(resolver.addresses)
                    }
                }
            }, context)
        guard error == kDNSServiceErr_NoError, let serviceReference else { return }
        addressReference = serviceReference
        DNSServiceSetDispatchQueue(serviceReference, .main)
    }

    nonisolated static func rawAddress(_ address: UnsafePointer<sockaddr>) -> Data? {
        switch Int32(address.pointee.sa_family) {
        case AF_INET:
            return address.withMemoryRebound(to: sockaddr_in.self, capacity: 1) { pointer in
                withUnsafeBytes(of: pointer.pointee.sin_addr) { Data($0) }
            }
        case AF_INET6:
            return address.withMemoryRebound(to: sockaddr_in6.self, capacity: 1) { pointer in
                withUnsafeBytes(of: pointer.pointee.sin6_addr) { Data($0) }
            }
        default:
            return nil
        }
    }

    func cancel() {
        if let reference {
            DNSServiceRefDeallocate(reference)
        }
        reference = nil
        if let addressReference {
            DNSServiceRefDeallocate(addressReference)
        }
        addressReference = nil
        if let modelReference {
            DNSServiceRefDeallocate(modelReference)
        }
        modelReference = nil
    }

    static func resolve(name: String) async -> ComputerAddress? {
        await withCheckedContinuation { continuation in
            var finished = false
            var resolver: ServiceResolver?
            resolver = ServiceResolver(name: name) { hostName, port in
                guard !finished else { return }
                finished = true
                resolver?.cancel()
                resolver = nil
                continuation.resume(returning: ComputerAddress(host: hostName, port: port))
            }
            resolver?.start()
            Task { @MainActor in
                try? await Task.sleep(seconds: 5)
                guard !finished else { return }
                finished = true
                resolver?.cancel()
                resolver = nil
                continuation.resume(returning: nil)
            }
        }
    }
}
