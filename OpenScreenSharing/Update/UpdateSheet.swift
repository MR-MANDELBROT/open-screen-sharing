#if os(macOS)
import SwiftUI

struct UpdateSheet: View {
    let update: AvailableUpdate

    @ObservedObject private var updates = UpdateManager.shared
    @Environment(\.dismiss) private var dismiss
    @Environment(\.openURL) private var openURL

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            VStack(alignment: .leading, spacing: 2) {
                Text("A New Build Is Available")
                    .font(.headline)
                Text(verbatim: "\(update.version) (\(update.build))")
                    .foregroundStyle(.secondary)
            }
            if !update.notes.isEmpty {
                ScrollView {
                    Text(notes)
                        .textSelection(.enabled)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
                .frame(maxHeight: 300)
            }
            switch updates.phase {
            case .idle:
                EmptyView()
            case .downloading:
                ProgressView(value: updates.progress)
            case .installing:
                ProgressView()
                    .progressViewStyle(.linear)
            }
            HStack {
                Button("View Release") { openURL(update.page) }
                Spacer()
                Group {
                    Button("Cancel", role: .cancel) { dismiss() }
                        .keyboardShortcut(.cancelAction)
                    Button("Update") {
                        Task { await updates.install(update) }
                    }
                    .keyboardShortcut(.defaultAction)
                }
                .disabled(updates.phase != .idle)
            }
        }
        .padding(20)
        .frame(width: 480)
        .interactiveDismissDisabled(updates.phase != .idle)
        .alert($updates.installFailure)
    }

    private var notes: AttributedString {
        (try? AttributedString(markdown: update.notes, options: .init(interpretedSyntax: .inlineOnlyPreservingWhitespace)))
            ?? AttributedString(update.notes)
    }
}

struct UpdatePresentation: ViewModifier {
    @ObservedObject private var updates = UpdateManager.shared

    func body(content: Content) -> some View {
        content
            .sheet(item: $updates.presented) { update in
                UpdateSheet(update: update)
            }
            .alert($updates.alert)
    }
}
#endif
