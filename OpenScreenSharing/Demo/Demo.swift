import Foundation
import GRDB

struct Demo {
    let database: DatabaseQueue
    let nearby: NearbyBrowser

    static let current: Demo? = {
        #if DEBUG
        ProcessInfo.processInfo.arguments.contains("-Demo") ? Demo() : nil
        #else
        nil
        #endif
    }()
}

#if DEBUG
extension Demo {
    private struct SavedComputer {
        var name: String
        var model: String
        var lastConnected: TimeInterval

        var host: String { name.replacingOccurrences(of: " ", with: "-") + ".local" }
    }

    private static let saved = [
        SavedComputer(name: "Mac mini", model: "Mac16,10", lastConnected: 12 * 60),
        SavedComputer(name: "iPhone", model: "iPhone18,2", lastConnected: 40 * 60),
        SavedComputer(name: "iPad Pro", model: "iPad16,5", lastConnected: 4 * 60 * 60),
    ]

    private init() {
        database = try! DatabaseQueue()
        try! AppDatabase.migrate(database)
        var nearby: [NearbyComputer] = []
        for (index, saved) in Self.saved.enumerated() {
            let address = ComputerAddress(host: saved.host, port: ComputerAddress.defaultPort)
            var computer = Computer(name: saved.name, address: address)
            computer.model = saved.model
            computer.lastConnected = Date(timeIntervalSinceNow: -saved.lastConnected)
            computer.createdAt = Date(timeIntervalSinceNow: TimeInterval(index - Self.saved.count))
            try! database.write { try computer.insert($0) }
            nearby.append(NearbyComputer(name: saved.name, hostName: saved.host, port: address.port, model: saved.model))
        }
        self.nearby = NearbyBrowser(fixed: nearby)
    }
}
#endif
