#if os(macOS)
import Foundation

nonisolated struct AvailableUpdate: Identifiable, Sendable {
    let build: Int
    let notes: String
    let package: URL
    let size: Int

    var id: Int { build }
}

nonisolated enum UpdateChecker {
    static let releasePage = URL(string: "https://github.com/nekohasekai/open-screen-sharing/releases/tag/nightly")!
    private static let currentBuild = Int(Bundle.main.object(forInfoDictionaryKey: "CFBundleVersion") as? String ?? "") ?? 0

    private static let releaseEndpoint = URL(string: "https://api.github.com/repos/nekohasekai/open-screen-sharing/releases/tags/nightly")!
    private static let downloads = AppDirectory.caches.appending(path: "Updates")

    private struct Release: Decodable {
        let body: String?
        let assets: [Asset]
    }

    private struct Asset: Decodable {
        let name: String
        let url: URL
        let size: Int
    }

    private struct Metadata: Decodable {
        let build: Int
    }

    static func check(token: String) async throws -> AvailableUpdate? {
        let release = try await JSONDecoder().decode(Release.self, from: fetch(releaseEndpoint, token: token, accept: "application/vnd.github+json"))
        guard let metadataAsset = release.assets.first(where: { $0.name == "metadata.json" }) else {
            return nil
        }
        let metadata = try await JSONDecoder().decode(Metadata.self, from: fetch(metadataAsset.url, token: token, accept: "application/octet-stream"))
        guard metadata.build > currentBuild, let package = release.assets.first(where: { $0.name == "OpenScreenSharing.pkg" }) else {
            return nil
        }
        return AvailableUpdate(build: metadata.build, notes: release.body ?? "", package: package.url, size: package.size)
    }

    static func download(_ update: AvailableUpdate, token: String, progress: @escaping @Sendable (Double) -> Void) async throws -> URL {
        let cached = downloads.appending(path: "Screen Sharing \(update.build).pkg")
        if (try? cached.resourceValues(forKeys: [.fileSizeKey]).fileSize) == update.size {
            progress(1)
            return cached
        }
        var update = update
        var (location, response) = try await URLSession.shared.download(
            for: request(update.package, token: token, accept: "application/octet-stream"), delegate: DownloadProgress(report: progress))
        defer { try? FileManager.default.removeItem(at: location) }
        if (response as? HTTPURLResponse)?.statusCode == 404, let latest = try await check(token: token) {
            try? FileManager.default.removeItem(at: location)
            update = latest
            (location, response) = try await URLSession.shared.download(
                for: request(update.package, token: token, accept: "application/octet-stream"), delegate: DownloadProgress(report: progress))
        }
        try validate(response)
        try? FileManager.default.removeItem(at: downloads)
        try FileManager.default.createDirectory(at: downloads, withIntermediateDirectories: true)
        let destination = downloads.appending(path: "Screen Sharing \(update.build).pkg")
        try FileManager.default.moveItem(at: location, to: destination)
        return destination
    }

    private static func request(_ url: URL, token: String, accept: String) -> URLRequest {
        var request = URLRequest(url: url, cachePolicy: .reloadIgnoringLocalCacheData)
        request.setValue(accept, forHTTPHeaderField: "Accept")
        if !token.isEmpty {
            request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        }
        return request
    }

    private static func fetch(_ url: URL, token: String, accept: String) async throws -> Data {
        let (data, response) = try await URLSession.shared.data(for: request(url, token: token, accept: accept))
        try validate(response)
        return data
    }

    private static func validate(_ response: URLResponse) throws {
        if let response = response as? HTTPURLResponse, response.statusCode != 200 {
            throw MessageError("HTTP \(response.statusCode): \(HTTPURLResponse.localizedString(forStatusCode: response.statusCode))")
        }
    }
}

nonisolated private final class DownloadProgress: NSObject, URLSessionTaskDelegate, Sendable {
    private let report: @Sendable (Double) -> Void
    private let observation = LockedState<NSKeyValueObservation?>(initialState: nil)

    init(report: @escaping @Sendable (Double) -> Void) {
        self.report = report
    }

    func urlSession(_ session: URLSession, didCreateTask task: URLSessionTask) {
        let report = report
        observation.withLock {
            $0 = task.progress.observe(\.fractionCompleted) { progress, _ in
                report(progress.fractionCompleted)
            }
        }
    }
}
#endif
