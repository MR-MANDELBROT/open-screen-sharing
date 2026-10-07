import Foundation
import dnssd

final class BonjourAdvertiser {
    var onRegister: ((String) -> Void)?
    var onFailure: ((DNSServiceErrorType) -> Void)?

    private var reference: DNSServiceRef?

    var isPublishing: Bool { reference != nil }

    @discardableResult
    func start(name: String, type: String, port: Int, txt: [String: String] = [:]) -> DNSServiceErrorType {
        stop()
        let name = Self.instanceName(name)
        let record = txt.reduce(into: Data()) { record, entry in
            let item = Data("\(entry.key)=\(entry.value)".utf8)
            record.append(UInt8(item.count))
            record.append(item)
        }
        let context = Unmanaged.passUnretained(self).toOpaque()
        var serviceReference: DNSServiceRef?
        let error = record.withUnsafeBytes { buffer in
            DNSServiceRegister(
                &serviceReference, 0, 0, name, type, nil, nil, UInt16(port).bigEndian, UInt16(buffer.count), buffer.baseAddress,
                { _, flags, errorCode, name, _, _, context in
                    guard let context else { return }
                    let advertiser = Unmanaged<BonjourAdvertiser>.fromOpaque(context).takeUnretainedValue()
                    let registered = name.map { String(cString: $0) }
                    let added = flags & DNSServiceFlags(kDNSServiceFlagsAdd) != 0
                    MainActor.assumeIsolated {
                        advertiser.registered(errorCode: errorCode, name: registered, added: added)
                    }
                }, context)
        }
        guard error == kDNSServiceErr_NoError, let serviceReference else { return error }
        reference = serviceReference
        DNSServiceSetDispatchQueue(serviceReference, .main)
        return error
    }

    func stop() {
        if let reference {
            DNSServiceRefDeallocate(reference)
        }
        reference = nil
    }

    private func registered(errorCode: DNSServiceErrorType, name: String?, added: Bool) {
        guard reference != nil else { return }
        guard errorCode == kDNSServiceErr_NoError else {
            stop()
            onFailure?(errorCode)
            return
        }
        guard added, let name else { return }
        onRegister?(name)
    }

    private static let maxNameLength = 63

    private static func instanceName(_ name: String) -> String {
        guard name.utf8.count > maxNameLength else { return name }
        var result = ""
        var length = 0
        for character in name {
            length += character.utf8.count
            guard length <= maxNameLength else { break }
            result.append(character)
        }
        while result.last?.isWhitespace == true {
            result.removeLast()
        }
        return result
    }
}
