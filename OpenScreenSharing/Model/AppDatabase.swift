import Foundation
import GRDB

enum AppDatabase {
    private static let url: URL = {
        #if os(iOS)
        let directory = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
        #else
        let directory = FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: Bundle.main.object(forInfoDictionaryKey: "AppGroupIdentifier") as! String)!
        #endif
        return directory.appendingPathComponent("Screen Sharing.sqlite")
    }()

    static let shared: DatabaseQueue = Demo.current?.database ?? open()

    private static func open() -> DatabaseQueue {
        do {
            try FileManager.default.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
            let database = try DatabaseQueue(path: url.path)
            try migrate(database)
            return database
        } catch {
            fatalError("cannot open \(url.lastPathComponent): \(error)")
        }
    }

    static func migrate(_ database: DatabaseQueue) throws {
        var migrator = DatabaseMigrator()
        migrator.eraseDatabaseOnSchemaChange = true
        migrator.registerMigration("initialize") { db in
            try db.create(table: "computers") { t in
                t.primaryKey("id", .blob)
                t.column("name", .text).notNull()
                t.column("host", .text).notNull()
                t.column("port", .integer).notNull()
                t.column("username", .text).notNull()
                t.column("password", .text).notNull()
                t.column("authenticates", .boolean).notNull()
                t.column("model", .text).notNull()
                t.column("enclosureColor", .text).notNull()
                t.column("lastConnected", .datetime)
                t.column("thumbnail", .blob)
                t.column("createdAt", .datetime).notNull()
                t.column("supportsHighPerformance", .boolean).notNull()
                t.column("networkQuality", .text).notNull()
                t.column("internetMaximumFrameRate", .integer).notNull()
                t.column("serverType", .text).notNull()
                t.column("quality", .text).notNull()
                t.column("observes", .boolean).notNull()
                t.column("sharesClipboard", .boolean).notNull()
                t.column("usesDynamicResolution", .boolean).notNull()
                t.column("resolutionScale", .double).notNull()
                t.column("displayID", .integer).notNull()
                t.column("usesHDR", .boolean).notNull()
                t.column("remoteAudio", .text).notNull()
                t.column("keyboardSources", .text).notNull()
                t.column("windowWidth", .double).notNull()
                t.column("windowHeight", .double).notNull()
            }
            try db.create(table: "preferences") { t in
                t.primaryKey("name", .text)
                t.column("data", .blob).notNull()
            }
        }
        try migrator.migrate(database)
    }
}
