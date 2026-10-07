import Foundation
import Network

nonisolated struct ComputerAddress: Hashable, Sendable, CustomStringConvertible {
    static let defaultPort = 5900

    var host: String
    var port: Int

    init(host: String, port: Int = ComputerAddress.defaultPort) {
        self.host = host
        self.port = port
    }

    var description: String {
        if port == ComputerAddress.defaultPort {
            return host
        }
        return host.contains(":") ? "[\(host)]:\(port)" : "\(host):\(port)"
    }

    var bonjourServiceName: String? {
        let lowered = host.lowercased()
        for suffix in ["._rfb._tcp.local.", "._rfb._tcp.local"] where lowered.hasSuffix(suffix) {
            return String(host.dropLast(suffix.count))
        }
        return nil
    }

    func matches(_ other: ComputerAddress) -> Bool {
        port == other.port && Self.normalized(host) == Self.normalized(other.host)
    }

    private static func normalized(_ host: String) -> String {
        var value = host.lowercased()
        if value.hasSuffix(".") { value.removeLast() }
        return value
    }

    static func host(parsing text: String) -> String? {
        var host = text.trimmingCharacters(in: .whitespacesAndNewlines)
        if host.hasPrefix("["), host.hasSuffix("]") {
            host = String(host.dropFirst().dropLast())
            return IPv6Address(host) != nil ? host : nil
        }
        if host.contains(":") {
            return IPv6Address(host) != nil ? host : nil
        }
        return isHostName(host) ? host : nil
    }

    private static func isHostName(_ text: String) -> Bool {
        guard !text.isEmpty, text.count <= 253 else { return false }
        var labels = text.split(separator: ".", omittingEmptySubsequences: false)
        if labels.count > 1, labels.last?.isEmpty == true {
            labels.removeLast()
        }
        return labels.allSatisfy { label in
            !label.isEmpty && label.count <= 63 && label.first != "-" && label.last != "-"
                && label.allSatisfy { $0.isLetter || $0.isNumber || $0 == "-" || $0 == "_" }
        }
    }
}

nonisolated struct ConnectionTarget: Hashable, Sendable {
    var address: ComputerAddress
    var username: String?
    var password: String?

    init(address: ComputerAddress, username: String? = nil, password: String? = nil) {
        self.address = address
        self.username = username
        self.password = password
    }

    init?(parsing text: String) {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return nil }
        if trimmed.lowercased().hasPrefix("vnc://") {
            guard let link = VNCLink(url: URL(string: trimmed)) else { return nil }
            self = link.target
            return
        }
        var rest = Substring(trimmed)
        var username: String?
        if let at = rest.lastIndex(of: "@") {
            let name = rest[..<at]
            guard !name.isEmpty else { return nil }
            username = String(name)
            rest = rest[rest.index(after: at)...]
        }
        guard let address = Self.address(parsing: rest) else { return nil }
        self.init(address: address, username: username)
    }

    static func address(parsing text: Substring) -> ComputerAddress? {
        if text.hasPrefix("[") {
            guard let closing = text.firstIndex(of: "]") else { return nil }
            guard let host = ComputerAddress.host(parsing: String(text[...closing])) else { return nil }
            let suffix = text[text.index(after: closing)...]
            if suffix.isEmpty { return ComputerAddress(host: host) }
            guard suffix.hasPrefix(":"), let port = Int(suffix.dropFirst()), (1...65535).contains(port) else { return nil }
            return ComputerAddress(host: host, port: port)
        }
        let colons = text.count(where: { $0 == ":" })
        if colons == 1, let colon = text.firstIndex(of: ":") {
            guard let host = ComputerAddress.host(parsing: String(text[..<colon])),
                let port = Int(text[text.index(after: colon)...]), (1...65535).contains(port)
            else { return nil }
            return ComputerAddress(host: host, port: port)
        }
        guard let host = ComputerAddress.host(parsing: String(text)) else { return nil }
        return ComputerAddress(host: host)
    }
}

nonisolated struct VNCLink: Hashable, Sendable {
    var address: ComputerAddress
    var username: String?
    var password: String?

    var target: ConnectionTarget {
        ConnectionTarget(address: address, username: username, password: password)
    }

    init?(url: URL?) {
        guard let url, url.scheme?.lowercased() == "vnc",
            let components = URLComponents(url: url, resolvingAgainstBaseURL: false)
        else { return nil }
        let host = components.percentEncodedHost.flatMap { $0.removingPercentEncoding } ?? components.host
        guard let host, !host.isEmpty else { return nil }
        let unbracketed = host.hasPrefix("[") && host.hasSuffix("]") ? String(host.dropFirst().dropLast()) : host
        address = ComputerAddress(host: unbracketed, port: components.port ?? ComputerAddress.defaultPort)
        username = components.user.flatMap { $0.isEmpty ? nil : $0 }
        password = components.password.flatMap { $0.isEmpty ? nil : $0 }
    }
}
