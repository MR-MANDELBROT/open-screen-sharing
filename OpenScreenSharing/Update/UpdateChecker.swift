#if os(macOS)
import Foundation

nonisolated enum UpdateTrack: String, Codable, Sendable {
    case stable
    case nightly

    fileprivate var endpoint: URL {
        switch self {
        case .stable:
            URL(string: "https://api.github.com/repos/nekohasekai/open-screen-sharing/releases/latest")!
        case .nightly:
            URL(string: "https://api.github.com/repos/nekohasekai/open-screen-sharing/releases/tags/nightly")!
        }
    }
}

nonisolated struct AvailableUpdate: Identifiable, Sendable {
    let version: String
    let build: Int
    let notes: String
    let page: URL
    let package: URL
    let size: Int

    var id: Int { build }
}

nonisolated enum UpdateChecker {
    private static let currentBuild = Int(Bundle.main.object(forInfoDictionaryKey: "CFBundleVersion") as? String ?? "") ?? 0

    private static let downloads = AppDirectory.caches.appending(path: "Updates")

    private struct Release: Decodable {
        let htmlURL: URL
        let body: String?
        let assets: [Asset]

        enum CodingKeys: String, CodingKey {
            case htmlURL = "html_url"
            case body
            case assets
        }
    }

    private struct Asset: Decodable {
        let name: String
        let url: URL
        let size: Int
    }

    private struct Metadata: Decodable {
        let version: String
        let build: Int
    }

    static func check(track: UpdateTrack, token: String) async throws -> AvailableUpdate? {
        let (data, response) = try await URLSession.shared.data(for: request(track.endpoint, token: token, accept: "application/vnd.github+json"))
        if (response as? HTTPURLResponse)?.statusCode == 404 {
            return nil
        }
        try validate(response)
        let release = try JSONDecoder().decode(Release.self, from: data)
        guard let metadataAsset = release.assets.first(where: { $0.name == "metadata.json" }) else {
            return nil
        }
        let metadata = try await JSONDecoder().decode(Metadata.self, from: fetch(metadataAsset.url, token: token, accept: "application/octet-stream"))
        guard metadata.build > currentBuild, let package = release.assets.first(where: { $0.name == "OpenScreenSharing.pkg" }) else {
            return nil
        }
        return AvailableUpdate(
            version: metadata.version, build: metadata.build, notes: release.body ?? "", page: release.htmlURL, package: package.url, size: package.size)
    }

    static func download(_ update: AvailableUpdate, track: UpdateTrack, token: String, progress: @escaping @Sendable (Double) -> Void) async throws -> URL {
        let cached = downloads.appending(path: "Screen Sharing \(update.build).pkg")
        if (try? cached.resourceValues(forKeys: [.fileSizeKey]).fileSize) == update.size {
            progress(1)
            return cached
        }
        var update = update
        var (location, response) = try await URLSession.shared.download(
            for: request(update.package, token: token, accept: "application/octet-stream"), delegate: DownloadProgress(report: progress))
        defer { try? FileManager.default.removeItem(at: location) }
        if (response as? HTTPURLResponse)?.statusCode == 404, let latest = try await check(track: track, token: token) {
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
