import SwiftUI

struct InlineProgress: View {
    let title: Text

    init(_ title: LocalizedStringKey) {
        self.title = Text(title)
    }

    init(verbatim title: String) {
        self.title = Text(verbatim: title)
    }

    var body: some View {
        HStack(spacing: 8) {
            ProgressView()
                .controlSize(.small)
            title
        }
        .foregroundStyle(.secondary)
    }
}
