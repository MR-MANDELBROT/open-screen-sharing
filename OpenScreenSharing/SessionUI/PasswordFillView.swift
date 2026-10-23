#if os(iOS)
import Perception
import SwiftUI

/// Fills a sign-in form on the Mac from the iPhone's password manager without the clipboard. Both fields offer
/// Password AutoFill (the key above the keyboard, confirmed with Face ID), and the buttons type their contents as key
/// presses. The password is typed only while the Mac reports a focused password field (secure input), and the sheet
/// forgets both fields once the password is typed or the sheet closes. The sheet leaves the screen above it usable,
/// so the fields on the Mac can be clicked while it stays open.
struct PasswordFillView: View {
    let controller: SessionController

    @State private var username = ""
    @State private var password = ""
    @State private var pressesReturn = true
    @State private var problem: String.LocalizationValue?
    @FocusState private var focusedField: Field?
    @Environment(\.dismiss) private var dismiss

    enum Field {
        case username
        case password
    }

    var body: some View {
        WithPerceptionTracking {
            NavigationStackCompat {
                SheetForm(Text("Fill Password")) {
                    Section {
                        TextField("User Name", text: $username)
                            .textContentType(.username)
                            .textInputAutocapitalization(.never)
                            .autocorrectionDisabled()
                            .focused($focusedField, equals: .username)
                            .submitLabel(.next)
                            .onSubmit { focusedField = .password }
                        SecureField("Password", text: $password)
                            .textContentType(.password)
                            .focused($focusedField, equals: .password)
                            .submitLabel(.done)
                            .onSubmit { focusedField = nil }
                    } footer: {
                        Text("Tap the key above the keyboard to fill from your passwords. Nothing is copied to the clipboard.")
                    }
                    Section {
                        Button("Type User Name") { typeUsername(pressTab: false) }
                        Button("Type User Name and Tab") { typeUsername(pressTab: true) }
                    } footer: {
                        Text("Click the user name field on the Mac first.")
                    }
                    .disabled(username.isEmpty || !controller.isControlling)
                    Section {
                        Toggle("Press Return After Password", isOn: $pressesReturn)
                        Button("Type Password", action: typePassword)
                            .disabled(password.isEmpty || !controller.isControlling || !controller.isPasswordFieldFocused)
                    } footer: {
                        status
                    }
                }
                .toolbar {
                    ToolbarItem(placement: .cancellationAction) {
                        Button("Cancel", role: .cancel, action: close)
                    }
                }
            }
            .phonePresentationDetents([.medium, .large])
            .backgroundInteractionUpThroughMedium()
            .onAppear {
                Task { focusedField = .username }
            }
            .onDisappear(perform: forget)
        }
    }

    @ViewBuilder
    private var status: some View {
        if let problem {
            Text(String(localized: problem))
                .foregroundStyle(.red)
        } else if controller.isPasswordFieldFocused {
            Label("The Mac reports a focused password field.", systemImage: "checkmark.shield")
                .foregroundStyle(.green)
        } else {
            Text("Click the password field on the Mac. The password is typed only into a field the Mac reports as a password field.")
        }
    }

    private func typeUsername(pressTab: Bool) {
        problem = controller.fillUsername(username, pressTab: pressTab) ? nil : "Some characters couldn’t be typed."
    }

    private func typePassword() {
        switch controller.fillPassword(password, pressReturn: pressesReturn) {
        case .typed:
            close()
        case .notControlling:
            problem = "The Mac isn’t being controlled."
        case .noPasswordField:
            problem = "The Mac no longer reports a password field. Click it and try again."
        case .modifiersHeld:
            problem = "Release the modifier keys first."
        case .unsupportedCharacters:
            problem = "Password fields on the Mac take only English characters. Nothing was typed."
        case .failed:
            problem = "The password couldn’t be sent."
        }
    }

    private func close() {
        forget()
        dismiss()
    }

    private func forget() {
        username = ""
        password = ""
        problem = nil
    }
}

private extension View {
    /// Keeps the session screen above a medium sheet usable, so that the fields on the Mac can be clicked meanwhile.
    @ViewBuilder
    func backgroundInteractionUpThroughMedium() -> some View {
        if #available(iOS 16.4, *) {
            presentationBackgroundInteraction(.enabled(upThrough: .medium))
        } else {
            self
        }
    }
}
#endif
