import Combine
import Foundation
import Libsharing

struct PairedDevice {
    var name: String
    var canMatchService: Bool
}

final class PairingStore: ObservableObject {
    static let shared = PairingStore()

    private static let url = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
        .appendingPathComponent("PairingRecord.plist")

    @Published private(set) var record: Data?
    @Published private(set) var device: PairedDevice?
    @Published private(set) var revision = 0

    var isPaired: Bool { record != nil }

    init() {
        record = try? Data(contentsOf: Self.url)
        device = record.flatMap { try? Self.check($0) }
    }

    private static func check(_ record: Data) throws -> PairedDevice {
        LibsharingLog.installed
        var error: NSError?
        guard let info = LibsharingParsePairingRecord(record, &error) else {
            throw error ?? MessageError("libsharing returned no pairing record information")
        }
        return PairedDevice(name: info.deviceName, canMatchService: info.canMatchService)
    }

    func save(_ record: Data) throws {
        let device = try Self.check(record)
        var url = Self.url
        try record.write(to: url, options: .atomic)
        var values = URLResourceValues()
        values.isExcludedFromBackup = true
        try url.setResourceValues(values)
        self.record = record
        self.device = device
        revision += 1
    }
}
