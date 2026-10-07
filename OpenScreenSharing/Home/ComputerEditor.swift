import SwiftUI

struct ComputerEditor: View {
    let computer: Computer
    var setup: Setup?

    struct Setup {
        var password: String
        var server: ServerAuthentication?
        var connect: (SessionRoute) -> Void
    }

    @Environment(\.dismiss) private var dismiss
    @State private var name: String
    @State private var address: String
    @State private var username: String
    @State private var password: String
    @State private var serverType: ServerType
    @State private var networkConfiguration: NetworkConfiguration
    @State private var problem: Problem?
    @State private var alert: AlertState?
    @FocusState private var focusedField: Field?

    enum Field {
        case name
        case address
        case username
        case password
    }

    enum Problem {
        case emptyName
        case invalidAddress
    }

    init(computer: Computer, setup: Setup? = nil) {
        self.computer = computer
        self.setup = setup
        _name = State(initialValue: computer.displayName)
        _address = State(initialValue: computer.address.description)
        _username = State(initialValue: computer.username)
        _password = State(initialValue: setup?.password ?? computer.password)
        _serverType = State(initialValue: computer.serverType)
        _networkConfiguration = State(initialValue: computer.networkConfiguration)
    }

    var body: some View {
        NavigationStackCompat {
            Form {
                Section {
                    LabeledContentCompat("Name") {
                        TextField("Name", text: $name, prompt: Text(verbatim: ""))
                            .multilineTextAlignment(.trailing)
                            .focused($focusedField, equals: .name)
                            .submitLabel(setup == nil ? .next : .go)
                            .onSubmit {
                                if setup == nil {
                                    focusedField = .address
                                } else {
                                    save()
                                }
                            }
                    }
                    LabeledContentCompat("Server Address") {
                        TextField("Server Address", text: $address, prompt: Text(verbatim: ""))
                            .multilineTextAlignment(.trailing)
                            .keyboardType(.URL)
                            .textInputAutocapitalization(.never)
                            .autocorrectionDisabled()
                            .focused($focusedField, equals: .address)
                            .submitLabel(.next)
                            .onSubmit { focusedField = .username }
                            .disabled(setup != nil)
                    }
                    if setup == nil {
                        LabeledContentCompat("User Name") {
                            TextField("User Name", text: $username, prompt: Text(verbatim: ""))
                                .multilineTextAlignment(.trailing)
                                .textInputAutocapitalization(.never)
                                .autocorrectionDisabled()
                                .focused($focusedField, equals: .username)
                                .submitLabel(.next)
                                .onSubmit { focusedField = .password }
                        }
                        LabeledContentCompat("Password") {
                            TextField("Password", text: $password.masked { focusedField == .password }, prompt: Text(verbatim: ""))
                                .textContentType(.oneTimeCode)
                                .textInputAutocapitalization(.never)
                                .autocorrectionDisabled()
                                .focused($focusedField, equals: .password)
                                .multilineTextAlignment(.trailing)
                                .submitLabel(.done)
                                .onSubmit(save)
                        }
                    }
                    Picker("Server Type", selection: $serverType) {
                        Text(verbatim: "VNC").tag(ServerType.vnc)
                        Text(verbatim: "Apple VNC").tag(ServerType.apple)
                    }
                    .pickerStyle(.menu)
                    if serverType == .apple {
                        Picker("Network Quality", selection: $networkConfiguration.quality) {
                            Text("Local Network").tag(NetworkQuality.localNetwork)
                            Text("Internet").tag(NetworkQuality.internet)
                        }
                        .pickerStyle(.menu)
                        // iPad17,1 on iPadOS 27.2 (24B5099f) negotiated 60 fps in the mode-5 capture.
                        if networkConfiguration.quality == .internet, computer.deviceType == .mac {
                            Picker("Maximum Frame Rate", selection: $networkConfiguration.internetMaximumFrameRate) {
                                ForEach(MaximumFrameRate.allCases) { rate in
                                    Text("\(rate.rawValue) FPS").tag(rate)
                                }
                            }
                            .pickerStyle(.menu)
                        }
                    }
                } footer: {
                    switch problem {
                    case .emptyName: Text("Enter a name.").foregroundStyle(.red)
                    case .invalidAddress: Text("Enter a valid server address.").foregroundStyle(.red)
                    case nil: EmptyView()
                    }
                }
            }
            .navigationTitle(setup == nil ? Text("Edit") : Text("New Connection"))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel", role: .cancel) { dismiss() }
                }
                ToolbarItem(placement: .confirmationAction) {
                    if setup == nil {
                        Button("Save", role: .confirmCompat, action: save)
                    } else {
                        Button("Connect", role: .confirmCompat, action: save)
                            .keyboardShortcut(.defaultAction)
                    }
                }
            }
            .alert($alert)
        }
    }

    private func save() {
        let name = name.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !name.isEmpty else {
            problem = .emptyName
            focusedField = .name
            return
        }
        guard let address = ConnectionTarget.address(parsing: Substring(address.trimmingCharacters(in: .whitespacesAndNewlines))) else {
            problem = .invalidAddress
            focusedField = .address
            return
        }
        var computer = computer
        computer.name = name
        computer.address = address
        computer.username = username.trimmingCharacters(in: .whitespacesAndNewlines)
        computer.networkConfiguration = networkConfiguration
        computer.serverType = serverType
        computer.password = password
        do {
            try ComputerStore.shared.save(computer)
        } catch {
            alert = AlertState(String(localized: "Couldn’t Save “\(name)”"), error: error)
            return
        }
        if let setup {
            var route = SessionRoute(computer: computer)
            route.password = password.isEmpty ? nil : password
            route.server = setup.server
            setup.connect(route)
        }
        dismiss()
    }
}

struct NearbyDetailsView: View {
    let computer: NearbyComputer

    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStackCompat {
            Form {
                Section {
                    LabeledContentCompat("Server Address") {
                        if let host = computer.hostName {
                            Text(host)
                        } else {
                            InlineProgress("Looking Up…")
                        }
                    }
                    if let port = computer.port {
                        LabeledContentCompat("Port", value: String(port))
                    }
                }
            }
            .navigationTitle(computer.name)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    CloseButton { dismiss() }
                }
            }
        }
        .phonePresentationDetents([.medium])
    }
}
