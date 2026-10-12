import Combine
import SwiftUI
import os

final class StorageNotice: ObservableObject {
    static let shared = StorageNotice()

    nonisolated static let logger = Logger(subsystem: Bundle.main.bundleIdentifier ?? "OpenScreenSharing", category: "storage")

    @Published private(set) var alert: AlertState?
    private var queued: [AlertState] = []

    func report(_ failure: String, error: any Error) {
        Self.logger.error("\(failure, privacy: .public): \(error, privacy: .public)")
        let alert = AlertState(failure, error: error)
        guard !([self.alert].compactMap(\.self) + queued).contains(where: { $0.title == alert.title && $0.message == alert.message }) else { return }
        if self.alert == nil {
            self.alert = alert
        } else {
            queued.append(alert)
        }
    }

    fileprivate func dismiss() {
        alert = nil
        guard !queued.isEmpty else { return }
        DispatchQueue.main.async { [self] in
            guard alert == nil, !queued.isEmpty else { return }
            alert = queued.removeFirst()
        }
    }
}

struct StorageNoticeAlert: ViewModifier {
    @ObservedObject private var notice = StorageNotice.shared

    func body(content: Content) -> some View {
        content.alert(Binding(get: { notice.alert }, set: { if $0 == nil { notice.dismiss() } }))
    }
}
