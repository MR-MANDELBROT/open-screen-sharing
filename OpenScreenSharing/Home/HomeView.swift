import Combine
import SwiftUI

final class HomeActions {
    var newConnection: () -> Void = {}
}

#if os(macOS)
final class HomeRequest: ObservableObject {
    enum Pending: Equatable {
        case newConnection
        case settings
    }

    static let shared = HomeRequest()
    @Published var pending: Pending?
}
#endif

enum HomeTab: Hashable {
    case connections
    case server
    case settings
}

struct HomeView: View {
    private struct LinkLookup {
        let name: String
        let task: Task<Void, Never>
    }

    @Environment(\.openSessionWindow) private var openSessionWindow
    @EnvironmentObject private var nearby: NearbyBrowser
    @EnvironmentObject private var availabilityMonitor: AvailabilityMonitor
    @Environment(\.scenePhase) private var scenePhase

    @State private var selection: HomeTab = .connections
    @State private var editing: Computer?
    @State private var setup: SetupRequest?
    @State private var pendingRoute: SessionRoute?
    @State private var activeSession: SessionController?
    @State private var alert: AlertState?
    @State private var actions = HomeActions()
    @State private var isWatchingConnections = false
    @State private var linkLookup: LinkLookup?
    #if os(iOS) && (DEBUG || SIDELOAD)
    @ObservedObject private var server = ServerController.shared
    #endif
    #if os(macOS)
    @ObservedObject private var homeRequest = HomeRequest.shared
    #endif

    var body: some View {
        layout
            .overlay {
                if let lookup = linkLookup {
                    ZStack {
                        Color.black.opacity(0.55)
                            .ignoresSafeArea()
                        VStack(spacing: 24) {
                            ProgressView("Looking for “\(lookup.name)”…")
                            Button("Cancel", role: .cancel) {
                                lookup.task.cancel()
                                linkLookup = nil
                            }
                            .keyboardShortcut(.cancelAction)
                            .glassButtonStyleCompat()
                            .controlSize(.large)
                        }
                        .padding()
                    }
                    .environment(\.colorScheme, .dark)
                }
            }
            #if os(iOS)
            .fullScreenCover(item: $activeSession) { controller in
                SessionView(controller: controller, hasOwnWindow: false) {
                    activeSession = nil
                }
                .modifier(StorageNoticeAlert())
            }
            .background {
                if let activeSession {
                    Color.clear.focusedSceneValue(\.sessionController, activeSession)
                }
            }
            #endif
            .modifier(StorageNoticeAlert())
            .alert($alert)
            .sheet(item: $editing) { computer in
                ComputerEditor(computer: computer)
            }
            .sheet(item: $setup, onDismiss: openPendingRoute) { _ in
                ConnectionSetupView { route in
                    pendingRoute = route
                }
            }
            .onOpenURL(perform: open)
            .onAppear(perform: appear)
            .onDisappear {
                linkLookup?.task.cancel()
                linkLookup = nil
                setWatchingConnections(false)
            }
            .onChangeCompat(of: scenePhase) {
                if scenePhase == .background {
                    linkLookup?.task.cancel()
                    linkLookup = nil
                }
                watchConnections()
            }
            .onChangeCompat(of: activeSession == nil) { watchConnections() }
            .onChangeCompat(of: selection) {
                linkLookup?.task.cancel()
                linkLookup = nil
                watchConnections()
            }
            .focusedSceneValue(\.homeActions, actions)
            #if os(macOS)
            .modifier(UpdatePresentation())
            .onChangeCompat(of: homeRequest.pending, initial: true) { pending in
                guard let pending else { return }
                homeRequest.pending = nil
                switch pending {
                case .newConnection: newConnection()
                case .settings: selection = .settings
                }
            }
            #endif
    }

    @ViewBuilder
    private var layout: some View {
        #if os(iOS)
        if #available(iOS 18, *) {
            TabView(selection: $selection) {
                Tab("Connections", systemImage: "desktopcomputer", value: HomeTab.connections) {
                    NavigationStackCompat {
                        computersView
                    }
                }
                #if DEBUG || SIDELOAD
                Tab("Server", systemImage: ServerFeature.systemImage, value: HomeTab.server) {
                    NavigationStackCompat {
                        serverView
                    }
                }
                .badge(isServerRunning ? Text(verbatim: "") : nil)
                .accessibilityValue(Text("Running"), isEnabled: isServerRunning)
                #endif
                Tab("Settings", systemImage: "gearshape", value: HomeTab.settings) {
                    NavigationStackCompat {
                        SettingsView()
                    }
                }
            }
            .tabViewStyle(.sidebarAdaptable)
        } else {
            TabView(selection: $selection) {
                NavigationStackCompat {
                    computersView
                }
                .tabItem { Label("Connections", systemImage: "desktopcomputer") }
                .tag(HomeTab.connections)
                #if DEBUG || SIDELOAD
                NavigationStackCompat {
                    serverView
                }
                .tabItem { Label("Server", systemImage: ServerFeature.systemImage) }
                .tag(HomeTab.server)
                .badge(isServerRunning ? Text(verbatim: "") : nil)
                .accessibilityValue(isServerRunning ? Text("Running") : Text(verbatim: ""))
                #endif
                NavigationStackCompat {
                    SettingsView()
                }
                .tabItem { Label("Settings", systemImage: "gearshape") }
                .tag(HomeTab.settings)
            }
        }
        #else
        NavigationSplitView {
            List(selection: Binding(get: { selection }, set: { if let page = $0 { selection = page } })) {
                Label("Connections", systemImage: "desktopcomputer")
                    .tag(HomeTab.connections)
                Label("Settings", systemImage: "gearshape")
                    .tag(HomeTab.settings)
            }
            .navigationSplitViewColumnWidth(min: 160, ideal: 180, max: 240)
        } detail: {
            NavigationStack {
                if selection == .settings {
                    SettingsView()
                } else {
                    computersView
                }
            }
            .id(selection)
        }
        #endif
    }

    #if os(iOS) && (DEBUG || SIDELOAD)
    @ViewBuilder
    private var serverView: some View {
        if ServerFeature.isAvailable {
            ServerView()
        } else {
            ServerUnavailableView()
        }
    }
    #endif

    #if os(iOS) && (DEBUG || SIDELOAD)
    private var isServerRunning: Bool {
        server.phase.isRunning
    }
    #endif

    private var computersView: ComputersView {
        #if os(iOS)
        let openInNewWindow = openSessionWindow
        #else
        let openInNewWindow: ((SessionRoute) -> Void)? = nil
        #endif
        return ComputersView(
            connect: { route in connect(route) },
            openInNewWindow: openInNewWindow,
            edit: { computer in editing = computer },
            newConnection: newConnection
        )
    }

    private func appear() {
        watchConnections()
        actions.newConnection = newConnection
    }

    private func watchConnections() {
        setWatchingConnections(scenePhase != .background && activeSession == nil && selection == .connections)
    }

    private func setWatchingConnections(_ watching: Bool) {
        guard watching != isWatchingConnections else { return }
        isWatchingConnections = watching
        if watching {
            nearby.start()
            availabilityMonitor.start()
        } else {
            availabilityMonitor.stop()
            nearby.stop()
        }
    }

    private func connect(_ route: SessionRoute) {
        linkLookup?.task.cancel()
        linkLookup = nil
        #if os(iOS)
        if activeSession != nil, let openSessionWindow {
            openSessionWindow(route)
        } else {
            activeSession = SessionController(route: route)
        }
        #else
        openSessionWindow?(route)
        #endif
    }

    private func openPendingRoute() {
        guard let route = pendingRoute else { return }
        pendingRoute = nil
        connect(route)
    }

    private func newConnection() {
        linkLookup?.task.cancel()
        linkLookup = nil
        selection = .connections
        setup = SetupRequest()
    }

    private func route(for link: RemoteLink, at address: ComputerAddress) -> SessionRoute {
        var route: SessionRoute
        if let computer = ComputerStore.shared.computer(at: address, remoteProtocol: link.remoteProtocol, username: link.username) {
            route = SessionRoute(computer: computer)
            if let username = link.username {
                route.username = username
            }
        } else {
            let found = nearby.computer(matching: address, remoteProtocol: link.remoteProtocol)
            route = SessionRoute(
                name: link.address.bonjourServiceName ?? found?.name ?? address.host, address: address,
                remoteProtocol: link.remoteProtocol, username: link.username, model: found?.model)
        }
        if let password = link.password {
            route.credentials = route.username.map { .account(username: $0, password: password) } ?? .vncPassword(password)
        }
        return route
    }

    private func open(_ url: URL) {
        linkLookup?.task.cancel()
        linkLookup = nil
        guard let link = RemoteLink(string: url.absoluteString) else {
            alert = AlertState(String(localized: "Couldn’t Open the Link"), errorMessage: String(localized: "“\(url.absoluteString)” isn’t a valid address."))
            return
        }
        guard let serviceName = link.address.bonjourServiceName else {
            connect(route(for: link, at: link.address))
            return
        }
        if let address = nearby.computers.first(where: { $0.remoteProtocol == .vnc && $0.name == serviceName })?.address {
            connect(route(for: link, at: address))
            return
        }
        let task = Task {
            do {
                let address = try await ServiceResolver.resolve(name: serviceName)
                try Task.checkCancellation()
                connect(route(for: link, at: address))
            } catch is CancellationError {
            } catch {
                guard !Task.isCancelled else { return }
                linkLookup = nil
                alert = AlertState(String(localized: "Couldn’t Open the Link"), error: error)
            }
        }
        linkLookup = LinkLookup(name: serviceName, task: task)
    }
}
