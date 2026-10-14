import Foundation

nonisolated enum AppDirectory {
    #if os(macOS)
    static let group = FileManager.default.containerURL(
        forSecurityApplicationGroupIdentifier: Bundle.main.object(forInfoDictionaryKey: "AppGroupIdentifier") as! String)!
    static let caches = group.appendingPathComponent("Library/Caches", isDirectory: true)
    #else
    static let caches = FileManager.default.urls(for: .cachesDirectory, in: .userDomainMask)[0]
    #endif

    static func cacheSize() -> Int64 {
        guard let enumerator = FileManager.default.enumerator(at: caches, includingPropertiesForKeys: [.totalFileAllocatedSizeKey]) else {
            return 0
        }
        var size: Int64 = 0
        for case let url as URL in enumerator {
            size += Int64((try? url.resourceValues(forKeys: [.totalFileAllocatedSizeKey]).totalFileAllocatedSize) ?? 0)
        }
        return size
    }

    static func clearCaches() throws {
        for item in try FileManager.default.contentsOfDirectory(at: caches, includingPropertiesForKeys: nil) {
            try FileManager.default.removeItem(at: item)
        }
    }
}
