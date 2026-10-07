import SwiftUI

@main
enum OpenScreenSharingMain {
    static func main() {
        if #available(iOS 17, *) {
            MultiWindowApp.main()
        } else {
            SingleWindowApp.main()
        }
    }
}

final class AppLaunch {
    let nearby: NearbyBrowser
    let availabilityMonitor: AvailabilityMonitor

    init() {
        Task.detached(priority: .userInitiated) { _ = DecoderCapabilities.current }
        _ = AppDatabase.shared
        nearby = Demo.current?.nearby ?? NearbyBrowser()
        availabilityMonitor = AvailabilityMonitor(nearby: nearby)
        #if DEBUG || SIDELOAD
        BadgeDots.install()
        if ServerFeature.isAvailable {
            ServerController.shared.resume()
        }
        #endif
    }
}

struct HomeScene: Scene {
    let launch: AppLaunch

    var body: some Scene {
        WindowGroup {
            HomeView()
                .providesSessionWindows()
                .environmentObject(launch.nearby)
                .environmentObject(launch.availabilityMonitor)
        }
        .commands {
            AppCommands()
        }
    }
}

struct SingleWindowApp: App {
    @UIApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate
    private let launch = AppLaunch()

    var body: some Scene {
        HomeScene(launch: launch)
    }
}

@available(iOS 17, *)
struct MultiWindowApp: App {
    @UIApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate
    private let launch = AppLaunch()

    var body: some Scene {
        HomeScene(launch: launch)

        WindowGroup(for: SessionRoute.self) { $route in
            SessionWindow(route: route)
                .environmentObject(launch.nearby)
        }
    }
}

@available(iOS 17, *)
struct SessionWindow: View {
    let route: SessionRoute?
    @Environment(\.dismissWindow) private var dismissWindow

    var body: some View {
        if let route {
            SessionView(route: WindowPasswords.receive(route)) {
                dismissWindow()
            }
        } else {
            ContentUnavailableView("No Connection Open", systemImage: "desktopcomputer")
        }
    }
}

nonisolated private struct OpenSessionWindowKey: EnvironmentKey {
    static let defaultValue: (@MainActor (SessionRoute) -> Void)? = nil
}

nonisolated extension EnvironmentValues {
    var openSessionWindow: (@MainActor (SessionRoute) -> Void)? {
        get { self[OpenSessionWindowKey.self] }
        set { self[OpenSessionWindowKey.self] = newValue }
    }
}

@available(iOS 17, *)
private struct SessionWindowOpener: ViewModifier {
    @Environment(\.openWindow) private var openWindow
    @Environment(\.supportsMultipleWindows) private var supportsMultipleWindows

    func body(content: Content) -> some View {
        content.environment(\.openSessionWindow, supportsMultipleWindows ? open : nil)
    }

    private func open(_ route: SessionRoute) {
        openWindow(value: WindowPasswords.send(route))
    }
}

extension View {
    @ViewBuilder
    func providesSessionWindows() -> some View {
        if #available(iOS 17, *) {
            modifier(SessionWindowOpener())
        } else {
            self
        }
    }
}
