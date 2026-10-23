#if os(iOS)
import Perception
import SwiftUI

/// Fills a sign-in form on the Mac from the iPhone's password manager without the clipboard. A panel at the bottom of
/// the session keeps the Mac's screen in view and usable: its fields offer Password AutoFill (the key above the
/// keyboard, confirmed with Face ID), its buttons type their contents as key presses into the field selected on the
/// Mac, and Tab and Return are separate keys, so nothing is submitted on its own. The password is typed only while the
/// Mac reports secure input, which a focused password field turns on but other apps can too (see reportsSecureInput),
/// so the panel says so and the user checks the selected field on the screen above. The panel forgets the password as
/// soon as it is typed and both fields when it closes.
struct PasswordFillPanel: View {
    let controller: SessionController

    @State private var username = ""
    @State private var password = ""
    @State private var problem: String.LocalizationValue?
    @FocusState private var focusedField: Field?

    enum Field {
        case username
        case password
    }

    var body: some View {
        WithPerceptionTracking {
            VStack(alignment: .leading, spacing: 10) {
                HStack(spacing: 8) {
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
                        .onSubmit(returnKeyFocus)
                }
                .textFieldStyle(.roundedBorder)
                HStack(spacing: 8) {
                    Button(action: typeUsername) {
                        Label("User Name", systemImage: "person")
                    }
                    .disabled(username.isEmpty || !controller.isControlling)
                    Button { controller.typeKey(.tab) } label: {
                        Label("Tab", systemImage: "arrow.right.to.line")
                            .labelStyle(.iconOnly)
                    }
                    .disabled(!controller.isControlling)
                    Button(action: typePassword) {
                        Label("Password", systemImage: "key")
                    }
                    .disabled(password.isEmpty || !controller.isControlling || !controller.reportsSecureInput)
                    Button { controller.typeKey(.returnKey) } label: {
                        Label("Return", systemImage: "return")
                            .labelStyle(.iconOnly)
                    }
                    .disabled(!controller.isControlling)
                    Spacer(minLength: 0)
                    Button(role: .cancel, action: close) {
                        Label("Close", systemImage: "xmark")
                    }
                    .labelStyle(.iconOnly)
                }
                .buttonStyle(.bordered)
                // With the keyboard up a phone in landscape has little room above it; the hint waits until it's down.
                if focusedField == nil {
                    status
                        .font(.footnote)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
            .padding(12)
            .frame(maxWidth: 620)
            .dynamicTypeSize(...DynamicTypeSize.xLarge)
            .glassEffectCompat(in: RoundedRectangle(cornerRadius: 22, style: .continuous))
            .padding(.horizontal, 12)
            .padding(.bottom, 8)
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
        } else if username.isEmpty && password.isEmpty {
            Text("Tap a field here, then the key above the keyboard to fill from your passwords. Nothing goes through the clipboard.")
                .foregroundStyle(.secondary)
        } else if controller.reportsSecureInput {
            Label("The Mac reports secure input. Check that the password field is selected, then tap Password.", systemImage: "lock.shield")
                .foregroundStyle(.green)
        } else {
            Text("Select the field on the Mac above, then type. Password works only while the Mac reports secure input, as in a password field.")
                .foregroundStyle(.secondary)
        }
    }

    private func typeUsername() {
        switch controller.fillUsername(username) {
        case .typed: problem = nil
        case .unsupportedCharacters: problem = "This user name can’t be typed as key presses. Nothing was typed."
        case .notControlling, .noSecureInput: problem = "The Mac isn’t being controlled."
        case .failed: problem = "The keys couldn’t be sent."
        }
    }

    private func typePassword() {
        switch controller.fillPassword(password) {
        case .typed:
            password = ""
            problem = nil
        case .noSecureInput: problem = "The Mac doesn’t report secure input. Select the password field and try again."
        case .unsupportedCharacters: problem = "Password fields on the Mac take only English characters. Nothing was typed."
        case .notControlling: problem = "The Mac isn’t being controlled."
        case .failed: problem = "The keys couldn’t be sent."
        }
    }

    private func close() {
        forget()
        controller.isShowingPasswordFill = false
        returnKeyFocus()
    }

    private func returnKeyFocus() {
        focusedField = nil
        controller.returnKeyFocusToScreen()
    }

    private func forget() {
        username = ""
        password = ""
        problem = nil
    }
}
#endif
