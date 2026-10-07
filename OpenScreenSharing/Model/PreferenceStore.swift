import Foundation
import GRDB

final class PreferenceStore {
    static let shared = PreferenceStore(database: AppDatabase.shared)

    private nonisolated struct Item: Codable, FetchableRecord, PersistableRecord {
        static let databaseTableName = "preferences"

        var name: String
        var data: Data
    }

    private let database: DatabaseQueue

    private init(database: DatabaseQueue) {
        self.database = database
    }

    func value<T: Decodable>(_ type: T.Type, for name: String) -> T? {
        guard let item = try? database.read({ try Item.fetchOne($0, key: name) }) else { return nil }
        return try? JSONDecoder().decode(type, from: item.data)
    }

    func set(_ value: some Encodable, for name: String) {
        guard let data = try? JSONEncoder().encode(value) else { return }
        try? database.write { try Item(name: name, data: data).save($0) }
    }

    func remove(_ name: String) {
        _ = try? database.write { try Item.deleteOne($0, key: name) }
    }
}
