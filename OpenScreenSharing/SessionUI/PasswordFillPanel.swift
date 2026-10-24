#if os(iOS)
import Perception
import SwiftUI

/// Fills a sign-in form on the Mac from the iPhone's password manager without the clipboard, about as quickly as a Mac
/// fills one itself. Selecting a password field on the Mac opens the panel (SessionController.secureInputSwitchedOn)
/// with a field focused, so the Passwords key above the keyboard offers the saved logins at once. iOS can't be told
/// which website the Mac shows, so the login is picked there and confirmed with Face ID.
///
/// Fill moves only forward from the field selected on the Mac (SessionController.fillCredentials): from the user name
/// field it types the name, Tab and the password once the Mac reports secure input for the next field; from a field
/// with secure input it types only the password. The Mac reports secure input, not a password field, so the panel
/// says only that, and Sign In After Filling presses Return only after the name and Tab led to secure input. After a
/// name alone, as on a first page that asks only for the name, the next Fill types the password once secure input
/// has switched on since. Closing the panel stops a fill and forgets both fields.
struct PasswordFillPanel: View {
    let controller: SessionController

    @AppStorage("PasswordFillSignsIn") private var signsIn = false
    @State private var userName = ""
    @State private var password = ""
    @State private var userNameTyped = false
    /// SessionController.secureInputSwitches when the user name went out.
    @State private var switchesAtUserName = 0
    @State private var fillTask: Task<Void, Never>?
    @State private var problem: String.LocalizationValue?
    @FocusState private var focusedField: Field?

    enum Field {
        case userName
        case password
    }

    var body: some View {
        WithPerceptionTracking {
            GlassEffectContainerCompat(spacing: 8) {
                VStack(alignment: .leading, spacing: 8) {
                    status
                        .font(.footnote.weight(.medium))
                        .lineLimit(3)
                        .fixedSize(horizontal: false, vertical: true)
                        .padding(.horizontal, 14)
                        .padding(.vertical, 8)
                        .glassEffectCompat(in: RoundedRectangle(cornerRadius: 18, style: .continuous))
                    HStack(spacing: 8) {
                        TextField("User Name", text: $userName)
                            .textContentType(.username)
                            .textInputAutocapitalization(.never)
                            .autocorrectionDisabled()
                            .focused($focusedField, equals: .userName)
                            .submitLabel(.next)
                            .onSubmit { focusedField = .password }
                            .modifier(GlassField())
                        SecureField("Password", text: $password)
                            .textContentType(.password)
                            .focused($focusedField, equals: .password)
                            .submitLabel(.done)
                            .onSubmit(fill)
                            .modifier(GlassField())
                    }
                    HStack(spacing: 8) {
                        Button(action: fill) {
                            Label(String(localized: fillTitle), systemImage: "key.fill")
                                .labelStyle(.titleAndIcon)
                                .frame(maxWidth: .infinity)
                        }
                        .prominentGlassButtonStyleCompat()
                        .disabled(!canFill)
                        Button { controller.typeFillKey(.returnKey) } label: {
                            Label("Return", systemImage: "return")
                        }
                        .disabled(!controller.isControlling || isFilling)
                        Menu {
                            Toggle("Sign In After Filling", isOn: $signsIn)
                            Button("Tab") { controller.typeFillKey(.tab) }
                                .disabled(!controller.isControlling)
                        } label: {
                            Label("More", systemImage: "ellipsis")
                        }
                        .disabled(isFilling)
                        // Close also stops a running fill.
                        Button(role: .cancel, action: close) {
                            Label("Close", systemImage: "xmark")
                        }
                    }
                    .labelStyle(.iconOnly)
                    .glassButtonStyleCompat()
                    .controlSize(.large)
                }
            }
            .frame(maxWidth: 620)
            .dynamicTypeSize(...DynamicTypeSize.xLarge)
            .padding(.horizontal, 12)
            .padding(.bottom, 8)
            .onAppear(perform: focusAsAsked)
            .onChangeCompat(of: controller.passwordFillFocusRequests, focusAsAsked)
            // Another login or an edited name starts over.
            .onChangeCompat(of: userName) {
                guard !isFilling else { return }
                userNameTyped = false
                problem = nil
            }
            .onDisappear {
                fillTask?.cancel()
                fillTask = nil
                forget()
            }
        }
    }

    private var isFilling: Bool { fillTask != nil }

    @ViewBuilder
    private var status: some View {
        if let problem {
            Label(String(localized: problem), systemImage: "exclamationmark.triangle.fill")
                .foregroundStyle(.orange)
        } else if isFilling {
            Label("Filling…", systemImage: "ellipsis.circle")
                .foregroundStyle(.secondary)
        } else if controller.reportsSecureInput {
            Label("The Mac reports secure input. Check that the password field is selected.", systemImage: "lock.shield.fill")
        } else if userNameTyped {
            Label("User name typed. Continue on the Mac, then tap Fill Password at the password field.", systemImage: "arrow.forward.circle.fill")
                .foregroundStyle(.secondary)
        } else {
            Label("Select the empty user name field on the Mac, or the password field if the name is already there.", systemImage: "cursorarrow.click")
                .foregroundStyle(.secondary)
        }
    }

    private var fillTitle: String.LocalizationValue {
        controller.reportsSecureInput ? "Fill Password" : "Fill"
    }

    private var canFill: Bool {
        guard controller.isControlling, !isFilling else { return false }
        return controller.reportsSecureInput ? !password.isEmpty : !userName.isEmpty && !userNameTyped
    }

    private func focusAsAsked() {
        let field: Field? =
            switch controller.passwordFillFocus {
            case .none: nil
            case .userName: .userName
            case .password: .password
            }
        guard let field else { return }
        Task { focusedField = field }
    }

    private func fill() {
        guard canFill else { return }
        problem = nil
        focusedField = nil
        // After the user name, only a field the Mac switched secure input on for since then takes the password.
        if userNameTyped, controller.secureInputSwitches == switchesAtUserName {
            problem = "Select the password field on the Mac, then tap Fill Password."
            return
        }
        let name = userName
        let secret = password
        fillTask = Task {
            let result = await controller.fillCredentials(userName: name, password: secret)
            guard !Task.isCancelled else { return }
            fillTask = nil
            if controller.fillTypedUserName { noteUserNameTyped() }
            switch result {
            case .typedAfterFieldChange:
                if signsIn { controller.typeFillKey(.returnKey) }
                close()
            case .typed where controller.fillTypedUserName:
                break
            case .typed:
                close()
            case .passwordFieldNotReached, .cancelled:
                break
            default:
                show(result)
            }
        }
    }

    private func noteUserNameTyped() {
        userNameTyped = true
        switchesAtUserName = controller.fillSwitchesAtUserName
    }

    private func show(_ result: SessionController.CredentialFillResult) {
        switch result {
        case .typed, .typedAfterFieldChange, .passwordFieldNotReached, .cancelled: problem = nil
        case .noSecureInput: problem = "Select the user name field on the Mac, then tap Fill."
        case .unsupportedCharacters:
            problem = "Only English characters can be typed into a Mac password field, and a user name only as key presses. Nothing was typed."
        case .notControlling: problem = "The Mac isn’t being controlled."
        case .failed: problem = "The keys couldn’t be sent."
        }
    }

    private func close() {
        fillTask?.cancel()
        fillTask = nil
        forget()
        controller.isShowingPasswordFill = false
        focusedField = nil
        controller.returnKeyFocusToScreen()
    }

    private func forget() {
        userName = ""
        password = ""
        userNameTyped = false
        problem = nil
    }
}

/// A field of the fill panel: a capsule of Liquid Glass like the toolbar's controls.
private struct GlassField: ViewModifier {
    func body(content: Content) -> some View {
        content
            .textFieldStyle(.plain)
            .padding(.horizontal, 16)
            .padding(.vertical, 12)
            .glassEffectCompat(in: Capsule())
    }
}
#endif
