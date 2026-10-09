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
            Section("About") {
                Link(destination: Self.sourceCode) {
                    Label("Source Code", systemImage: "chevron.left.forwardslash.chevron.right")
                }
            }
        }
        #if os(macOS)
        .formStyle(.grouped)
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
