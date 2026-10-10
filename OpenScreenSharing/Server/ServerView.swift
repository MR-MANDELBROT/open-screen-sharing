import SwiftUI
import UIKit

struct ServerView: View {
    @State private var portText: String?
    @State private var isShowingPairing = false
    @State private var isShowingDiskImage = false
    @FocusState private var focusedField: Field?

    enum Field {
        case port
        case username
        case password
    }

    @ObservedObject private var controller = ServerController.shared
    @ObservedObject private var settings = ServerSettings.shared
    @ObservedObject private var pairing = PairingStore.shared
    @ObservedObject private var diskImage = DiskImageStore.shared

    var body: some View {
        Form {
            if pairing.isPaired {
                sharingSection
                listenSection
                authenticationSection
                backgroundSection
            }
            setupSection
        }
        .navigationTitle("Server")
        .alert(problemAlert)
        .sheet(isPresented: $isShowingPairing) {
            PairingSetupView()
        }
        .sheet(isPresented: $isShowingDiskImage) {
            DiskImageImportView()
        }
        .onAppear {
            controller.remotePairing.start()
            controller.startProbing()
        }
        .onDisappear {
            controller.remotePairing.stop()
            controller.stopProbing()
        }
        .onChangeCompat(of: focusedField) {
            if focusedField != .port {
                portText = nil
            }
        }
        .onChangeCompat(of: pairing.revision) {
            controller.remotePairing.refresh()
            if case .failed = controller.phase {
                controller.stop()
            }
        }
    }

    private var problemAlert: Binding<AlertState?> {
        Binding {
            if case .failed(let problem) = controller.phase {
                if problem.kind == .locationDenied {
                    return AlertState(
                        title: problem.title,
                        actions: [
                            AlertState.Action(label: String(localized: "Open Settings"), role: nil) {
                                if let url = URL(string: UIApplication.openSettingsURLString) {
                                    UIApplication.shared.open(url)
                                }
                            },
                            AlertState.Action(label: String(localized: "OK"), role: .cancel) {},
                        ])
                }
                return AlertState(problem.title, errorMessage: problem.message)
            }
            return nil
        } set: { state in
            guard state == nil else { return }
            if case .failed = controller.phase {
                controller.stop()
            }
        }
    }

    private var sharingBinding: Binding<Bool> {
        Binding {
            controller.isActive
        } set: { on in
            focusedField = nil
            if on {
                controller.start()
            } else {
                controller.stop()
            }
        }
    }

    private var sharingSection: some View {
        Section {
            Toggle("Enabled", isOn: sharingBinding)
                .disabled(!controller.isActive && !controller.canStart)
        } footer: {
            VStack(alignment: .leading, spacing: 4) {
                if !controller.isActive, controller.tunnelFound == false {
                    Text("No loopback address found.")
                }
                if let problem = phaseProblem {
                    Text(verbatim: problem.message.isEmpty ? problem.title : "\(problem.title): \(problem.message)")
                }
                if let problem = controller.viewerProblem {
                    Text(problem)
                }
                if let problem = controller.advertisingProblem {
                    Text(problem)
                }
            }
            .foregroundStyle(.red)
        }
    }

    private var phaseProblem: ServerProblem? {
        switch controller.phase {
        case .connecting(let problem), .running(let problem): problem
        default: nil
        }
    }

    private var listenSection: some View {
        Section("Listen") {
            LabeledContentCompat("Port") {
                TextField("Port", text: port, prompt: Text(verbatim: String(ServerSettings.defaultPort)))
                    .keyboardType(.numberPad)
                    .multilineTextAlignment(.trailing)
                    .foregroundStyle(isPortValid ? Color.primary : Color.red)
                    .focused($focusedField, equals: .port)
            }
        }
        .disabled(controller.isActive)
    }

    private var port: Binding<String> {
        Binding {
            portText ?? String(settings.port)
        } set: { text in
            let digits = String(text.filter { $0.isASCII && $0.isNumber }.prefix(5))
            portText = digits
            if let port = Self.parsedPort(digits) {
                settings.port = port
            }
        }
    }

    private var isPortValid: Bool {
        portText.map { Self.parsedPort($0) != nil } ?? true
    }

    private static func parsedPort(_ digits: String) -> Int? {
        guard !digits.isEmpty else { return ServerSettings.defaultPort }
        guard let port = Int(digits), ServerSettings.portRange.contains(port) else { return nil }
        return port
    }

    private var authenticationSection: some View {
        Section {
            Toggle("Enabled", isOn: $settings.authenticates)
            if settings.authenticates {
                LabeledContentCompat("Username") {
                    TextField("Username", text: $settings.username, prompt: Text("Required"))
                        .textContentType(.oneTimeCode)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                        .multilineTextAlignment(.trailing)
                        .focused($focusedField, equals: .username)
                        .submitLabel(.next)
                        .onSubmit { focusedField = .password }
                }
                LabeledContentCompat("Password") {
                    TextField("Password", text: $settings.password.masked { focusedField == .password }, prompt: Text("Required"))
                        .textContentType(.oneTimeCode)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                        .multilineTextAlignment(.trailing)
                        .focused($focusedField, equals: .password)
                        .submitLabel(.done)
                }
            }
        } header: {
            Text("Authentication")
        }
        .disabled(controller.isActive)
    }

    private var backgroundSection: some View {
        Section {
            Toggle("Enabled", isOn: $settings.runsInBackground)
            if settings.runsInBackground {
                Picker("Keep Running With", selection: $settings.backgroundMode) {
                    Text("Location Service").tag(ServerBackgroundMode.location)
                    Text("Audio Service").tag(ServerBackgroundMode.audio)
                }
                .pickerStyle(.menu)
            }
        } header: {
            Text("Background Service")
        } footer: {
            VStack(alignment: .leading, spacing: 4) {
                Text("Screen Sharing needs to stay running in the background to keep sharing this device’s screen.")
                if let problem = controller.backgroundProblem {
                    Text(problem)
                        .foregroundStyle(.red)
                }
            }
        }
        .onChangeCompat(of: settings.runsInBackground) { controller.updateBackground() }
        .onChangeCompat(of: settings.backgroundMode) { controller.updateBackground() }
    }

    private var setupSection: some View {
        Section {
            Button(pairing.isPaired ? "Pair Again" : "Pair") { isShowingPairing = true }
            Button(diskImage.isImported ? "Import Disk Image Again" : "Import Disk Image") { isShowingDiskImage = true }
        } header: {
            Text("Setup")
        } footer: {
            Text("Before sharing this device’s screen, pair and import the disk image.")
        }
        .disabled(controller.isActive)
    }
}
