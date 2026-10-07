import SwiftUI
import UIKit

struct AlertState {
    struct Action {
        var label: String
        var role: ButtonRole?
        var perform: () -> Void
    }

    var title: String
    var message: String
    var actions: [Action]

    init(title: String, message: String = "", actions: [Action] = [Action(label: String(localized: "OK"), role: .cancel) {}]) {
        self.title = title
        self.message = message
        self.actions = actions
    }

    init(_ failure: String, error: (any Error)? = nil, closeLabel: String = String(localized: "OK"), onClose: @escaping () -> Void = {}) {
        self.init(failure, errorMessage: error?.localizedDescription ?? "", closeLabel: closeLabel, onClose: onClose)
    }

    init(_ failure: String, errorMessage: String, closeLabel: String = String(localized: "OK"), onClose: @escaping () -> Void = {}) {
        let message = errorMessage.trimmingCharacters(in: .whitespacesAndNewlines)
        var actions = [Action(label: closeLabel, role: .cancel, perform: onClose)]
        if !message.isEmpty {
            actions.insert(
                Action(label: String(localized: "Copy"), role: nil) {
                    UIPasteboard.general.string = "\(failure)\n\(message)"
                    ClipboardAccess.shared.noteOwnWrite()
                    onClose()
                }, at: 0)
        }
        self.init(title: failure, message: message, actions: actions)
    }
}

extension View {
    func alert(_ state: Binding<AlertState?>) -> some View {
        alert(
            state.wrappedValue?.title ?? "",
            isPresented: Binding(get: { state.wrappedValue != nil }, set: { if !$0 { state.wrappedValue = nil } }),
            presenting: state.wrappedValue
        ) { alert in
            ForEach(alert.actions.indices, id: \.self) { index in
                let action = alert.actions[index]
                Button(action.label, role: action.role, action: action.perform)
            }
        } message: { alert in
            if !alert.message.isEmpty {
                Text(alert.message)
            }
        }
    }
}

nonisolated struct MessageError: LocalizedError {
    let message: String

    init(_ message: String) {
        self.message = message
    }

    init(_ operation: String, status: OSStatus) {
        message = "\(operation): OSStatus \(status)"
    }

    var errorDescription: String? { message }
}
