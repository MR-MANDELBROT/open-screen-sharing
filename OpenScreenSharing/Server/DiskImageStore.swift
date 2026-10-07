import Combine
import Foundation
import Libsharing

nonisolated struct MacImportRequest: Sendable {
    var host: String
    var port: Int
    var username: String
    var password: String
    var privateKey: Data?
    var passphrase: String
}

nonisolated struct DiskImageImportProgress: Sendable {
    var received: Int64
    var total: Int64?

    var fraction: Double? {
        total.map { min(Double(received) / Double($0), 1) }
    }

    var localizedDescription: String {
        guard let total else {
            let formatter = ByteCountFormatter()
            formatter.countStyle = .file
            formatter.zeroPadsFractionDigits = true
            return formatter.string(fromByteCount: received)
        }
        let progress = Progress(totalUnitCount: total)
        progress.kind = .file
        progress.completedUnitCount = received
        return progress.localizedAdditionalDescription
    }
}

final class DiskImageStore: ObservableObject {
    static let shared = DiskImageStore()

    @Published private(set) var progress: DiskImageImportProgress?
    @Published private(set) var isImported: Bool

    private var current: LibsharingDiskImageImport?
    private var isCancelled = false

    var isImporting: Bool { progress != nil }

    init() {
        isImported = Self.directory.map { LibsharingDiskImageImported($0.path) } ?? false
    }

    static let directory: URL? = {
        var directory = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("DeveloperDiskImage", isDirectory: true)
        do {
            try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
            var values = URLResourceValues()
            values.isExcludedFromBackup = true
            try directory.setResourceValues(values)
        } catch {
            return nil
        }
        return directory
    }()

    static var defaultURL: String { LibsharingDefaultDiskImageURL() }

    func download(from url: String) async throws {
        try await run { try $0.download(url) }
    }

    func importFile(at url: URL) async throws {
        try await run { importer in
            try url.withPickedFile(options: .forUploading) { try importer.importFile($0.path) }
        }
    }

    func importFromMac(_ request: MacImportRequest) async throws {
        try await run { importer in
            let options = LibsharingMacImportOptions()
            options.host = request.host
            options.port = Int32(request.port)
            options.username = request.username
            options.password = request.password
            options.privateKey = request.privateKey
            options.passphrase = request.passphrase
            try importer.import(fromMac: options)
        }
    }

    func cancel() {
        guard let current else { return }
        isCancelled = true
        current.cancel()
    }

    private func run(_ body: @escaping @Sendable (LibsharingDiskImageImport) throws -> Void) async throws {
        guard current == nil else { throw CancellationError() }
        guard let directory = Self.directory else {
            throw MessageError("Couldn’t create the folder for the developer disk image")
        }
        LibsharingLog.installed
        let events = DiskImageImportEvents { [weak self] progress in
            DispatchQueue.main.async {
                guard let self, self.current != nil else { return }
                self.progress = progress
            }
        }
        var error: NSError?
        guard let importer = LibsharingNewDiskImageImport(directory.path, events, &error) else {
            throw error ?? MessageError("libsharing returned no disk image import")
        }
        current = importer
        isCancelled = false
        progress = DiskImageImportProgress(received: 0)
        defer {
            current = nil
            progress = nil
        }
        let box = UncheckedSendable(value: importer)
        do {
            try await withCheckedThrowingContinuation { (continuation: CheckedContinuation<Void, any Error>) in
                DispatchQueue.global(qos: .userInitiated).async {
                    do {
                        try body(box.value)
                        continuation.resume()
                    } catch {
                        continuation.resume(throwing: error)
                    }
                }
            }
        } catch {
            if isCancelled {
                throw CancellationError()
            }
            throw error
        }
        isImported = true
    }
}
