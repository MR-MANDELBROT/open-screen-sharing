import Foundation

nonisolated struct SessionRoute: Codable, Hashable, Identifiable, Sendable {
    var id = UUID()
    var computerID: UUID?
    var name: String
    var host: String
    var port: Int
    var username: String?
    var password: String?
    var model: String?
    var remoteProtocol: RemoteProtocol
    var usesHighPerformance: Bool?
    var deviceType: DeviceType?
    var server: ServerAuthentication?

    var address: ComputerAddress {
        ComputerAddress(host: host, port: port)
    }

    var knownPlatform: RemotePlatform? {
        if remoteProtocol == .rdp { return .pc }
        return (deviceType ?? model.flatMap(DeviceType.init(model:))).map { $0.isIOS ? .iOS : .mac }
    }

    init(computer: Computer) {
        computerID = computer.id
        name = computer.displayName
        host = computer.host
        port = computer.port
        username = computer.username.isEmpty ? nil : computer.username
        model = computer.model.isEmpty ? nil : computer.model
        remoteProtocol = computer.remoteProtocol
        usesHighPerformance = computer.usesHighPerformance
        deviceType = computer.usesHighPerformance ? computer.deviceType : nil
    }

    init(
        name: String, address: ComputerAddress, remoteProtocol: RemoteProtocol, username: String? = nil, password: String? = nil,
        model: String? = nil
    ) {
        self.name = name
        host = address.host
        port = address.port
        self.remoteProtocol = remoteProtocol
        self.username = username
        self.password = password
        self.model = model
        deviceType = model.flatMap(DeviceType.init(model:))
    }

    private enum CodingKeys: String, CodingKey {
        case id, computerID, name, host, port, username, model, remoteProtocol, usesHighPerformance, deviceType, server
    }
}

enum WindowPasswords {
    private static var passwords: [UUID: String] = [:]

    static func send(_ route: SessionRoute) -> SessionRoute {
        var route = route
        passwords[route.id] = route.password
        route.password = nil
        return route
    }

    static func receive(_ route: SessionRoute) -> SessionRoute {
        var route = route
        route.password = passwords.removeValue(forKey: route.id)
        return route
    }
}
