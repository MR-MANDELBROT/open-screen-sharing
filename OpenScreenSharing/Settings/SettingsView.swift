import SwiftUI
#if os(macOS)
import AppKit
#endif

struct SettingsView: View {
    private static let sourceCode = URL(string: "https://github.com/nekohasekai/open-screen-sharing")!

    @State private var language = AppLanguage.selected
    @State private var alert: AlertState?

    var body: some View {
        Form {
            Section("App") {
                Picker(selection: $language) {
                    Text(verbatim: AppLanguage.systemDefaultName(in: language)).tag(String?.none)
                    ForEach(AppLanguage.available, id: \.self) { code in
                        Text(verbatim: AppLanguage.name(of: code)).tag(String?.some(code))
                    }
                } label: {
                    Label("Language", systemImage: "globe")
                }
                .onChangeCompat(of: language) { language in
                    AppLanguage.select(language)
                    #if os(macOS)
                    let actions = [
                        AlertState.Action(label: String(localized: "Later"), role: .cancel) {},
                        AlertState.Action(label: String(localized: "Restart"), role: nil, isDefault: true, perform: relaunch),
                    ]
                    #else
                    let actions = [AlertState.Action(label: String(localized: "OK"), role: .cancel, isDefault: true) {}]
                    #endif
                    alert = AlertState(
                        title: String(localized: "Restart Required"),
                        message: String(localized: "The new language takes effect after you restart the app."),
                        actions: actions
                    )
                }
                NavigationLink {
                    KeyboardShortcutsView()
                } label: {
                    Label("Keyboard Shortcuts", systemImage: "keyboard")
                }
            }
            #if os(macOS)
            UpdatesSection()
            #endif
            Section("About") {
                Link(destination: Self.sourceCode) {
                    Label("Source Code", systemImage: "chevron.left.forwardslash.chevron.right")
                }
            }
        }
        #if os(macOS)
        .formStyle(.grouped)
        .unboundedFormWidth()
        #endif
        .navigationTitle("Settings")
        .alert($alert)
    }

    #if os(macOS)
    private func relaunch() {
        Task {
            do {
                let configuration = NSWorkspace.OpenConfiguration()
                configuration.createsNewApplicationInstance = true
                _ = try await NSWorkspace.shared.openApplication(at: Bundle.main.bundleURL, configuration: configuration)
                NSApp.terminate(nil)
            } catch {
                alert = AlertState(String(localized: "Couldn’t Restart the App"), error: error)
            }
        }
    }
    #endif
}

#if os(macOS)
private struct UpdatesSection: View {
    @ObservedObject private var updates = UpdateManager.shared
    @State private var isEditingToken = false

    var body: some View {
        Section("Updates") {
            Toggle(isOn: $updates.checksAutomatically) {
                Label("Check for Updates Automatically", systemImage: "clock.arrow.circlepath")
            }
            LabeledContentCompat {
                Button("Set") { isEditingToken = true }
                    .sheet(isPresented: $isEditingToken) {
                        GitHubTokenSheet()
                    }
            } label: {
                Label("GitHub Token", systemImage: "key")
            }
            Button {
                Task { await updates.check(manually: true) }
            } label: {
                if updates.isChecking {
                    Label {
                        Text("Checking…")
                    } icon: {
                        ProgressView()
                            .controlSize(.small)
                    }
                } else {
                    Label("Check for Updates…", systemImage: "arrow.triangle.2.circlepath")
                }
            }
            .formRowButtonStyleCompat()
            .disabled(updates.isChecking)
        }
    }
}

private struct GitHubTokenSheet: View {
    private static let newToken = URL(string: "https://github.com/settings/personal-access-tokens/new")!

    @ObservedObject private var updates = UpdateManager.shared
    @Environment(\.dismiss) private var dismiss
    @State private var token = UpdateManager.shared.githubToken
    @State private var isUsable = false

    var body: some View {
        Form {
            Section {
                LabeledContentCompat("GitHub Token") {
                    TextField("GitHub Token", text: $token, prompt: Text("Optional"))
                        .multilineTextAlignment(.trailing)
                        .frame(minWidth: 0, idealWidth: 0, maxWidth: .infinity)
                        .autocorrectionDisabled()
                        .onSubmit(save)
                        .onChangeCompat(of: token) { newValue in
                            token = newValue.filter { !$0.isNewline }
                        }
                }
            } footer: {
                HStack {
                    Link("Generate a Token", destination: Self.newToken)
                    Spacer()
                    if isUsable {
                        Image(systemName: "checkmark.circle.fill")
                            .foregroundStyle(.green)
                    }
                }
            }
        }
        .formStyle(.grouped)
        .scrollContentBackground(.hidden)
        .frame(width: 420)
        .fixedSize(horizontal: false, vertical: true)
        .toolbar {
            if !updates.githubToken.isEmpty {
                ToolbarItem(placement: .destructiveAction) {
                    Button("Delete", role: .destructive) {
                        updates.githubToken = ""
                        dismiss()
                    }
                }
            }
            ToolbarItem(placement: .cancellationAction) {
                Button("Cancel", role: .cancel) { dismiss() }
            }
            ToolbarItem(placement: .confirmationAction) {
                Button("Save", role: .confirmCompat, action: save)
            }
        }
        .task(id: trimmedToken) {
            isUsable = false
            guard !trimmedToken.isEmpty else { return }
            do {
                try await Task.sleep(for: .milliseconds(500))
                _ = try await UpdateChecker.check(token: trimmedToken)
                isUsable = true
            } catch {}
        }
    }

    private var trimmedToken: String {
        token.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    private func save() {
        updates.githubToken = trimmedToken
        dismiss()
    }
}
#endif
