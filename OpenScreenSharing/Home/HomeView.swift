import SwiftUI

final class HomeActions {
    var newConnection: () -> Void = {}
}

enum HomeTab: Hashable {
    case connections
    case server
}

struct HomeView: View {
    @Environment(\.openSessionWindow) private var openSessionWindow
    @EnvironmentObject private var nearby: NearbyBrowser
    @EnvironmentObject private var availabilityMonitor: AvailabilityMonitor
    @Environment(\.scenePhase) private var scenePhase

    @State private var selection: HomeTab = .connections
    @State private var editing: Computer?
    @State private var setup: SetupRequest?
    @State private var pendingRoute: SessionRoute?
    @State private var activeSession: SessionRoute?
    @State private var actions = HomeActions()
    @State private var isWatchingConnections = false
    #if DEBUG || SIDELOAD
    @ObservedObject private var server = ServerController.shared
    #endif

    var body: some View {
        layout
            .fullScreenCover(item: $activeSession) { route in
                SessionView(route: route, hasOwnWindow: false) {
                    activeSession = nil
                }
            }
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
            .onDisappear { setWatchingConnections(false) }
            .onChangeCompat(of: scenePhase) { watchConnections() }
            .onChangeCompat(of: activeSession == nil) { watchConnections() }
            .onChangeCompat(of: selection) { watchConnections() }
            .focusedSceneValue(\.homeActions, actions)
    }

    @ViewBuilder
    private var layout: some View {
        if showsServerTab {
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
                }
            }
        } else {
            NavigationStackCompat {
                computersView
            }
        }
    }

    #if DEBUG || SIDELOAD
    @ViewBuilder
    private var serverView: some View {
        if ServerFeature.isAvailable {
            ServerView()
        } else {
            ServerUnavailableView()
        }
    }
    #endif

    private var showsServerTab: Bool {
        #if DEBUG || SIDELOAD
        ServerFeature.isShown
        #else
        false
        #endif
    }

    #if DEBUG || SIDELOAD
    private var isServerRunning: Bool {
        server.phase.isRunning
    }
    #endif

    private var computersView: ComputersView {
        ComputersView(
            connect: { route in connect(route) },
            openInNewWindow: openSessionWindow,
            edit: { computer in editing = computer },
            newConnection: newConnection
        )
    }

    private func appear() {
        watchConnections()
        actions.newConnection = newConnection
    }

    private func watchConnections() {
        setWatchingConnections(scenePhase != .background && activeSession == nil && selection != .server)
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
        if activeSession != nil, let openSessionWindow {
            openSessionWindow(route)
        } else {
            activeSession = route
        }
    }

    private func openPendingRoute() {
        guard let route = pendingRoute else { return }
        pendingRoute = nil
        connect(route)
    }

    private func newConnection() {
        setup = SetupRequest()
    }

    private func route(for target: ConnectionTarget) -> SessionRoute {
        var route: SessionRoute
        if let computer = ComputerStore.shared.computer(at: target.address, username: target.username) {
            route = SessionRoute(computer: computer)
            if let username = target.username {
                route.username = username
            }
        } else {
            let found = nearby.computer(matching: target.address)
            route = SessionRoute(
                name: found?.name ?? target.address.bonjourServiceName ?? target.address.host, address: target.address,
                username: target.username, model: found?.model)
        }
        route.password = target.password
        return route
    }

    private func open(_ url: URL) {
        guard let link = VNCLink(url: url) else { return }
        Task {
            var target = link.target
            if let serviceName = target.address.bonjourServiceName, let resolved = await ServiceResolver.resolve(name: serviceName) {
                target.address = resolved
            }
            var route = route(for: target)
            if route.computerID == nil, let serviceName = link.address.bonjourServiceName {
                route.name = serviceName
            }
            connect(route)
        }
    }
}
