import Foundation
import GRDB
#if os(macOS) && (DEBUG || DEMO)
import AppKit
import ScreenCaptureKit
import UniformTypeIdentifiers
#endif

struct Demo {
    let database: DatabaseQueue
    let nearby: NearbyBrowser

    static let current: Demo? = {
        #if DEBUG || DEMO
        ProcessInfo.processInfo.arguments.contains("-Demo") ? Demo() : nil
        #else
        nil
        #endif
    }()
}

#if DEBUG || DEMO
extension Demo {
    private struct SavedComputer {
        var name: String
        var model: String
        var thumbnail: String
        var lastConnected: TimeInterval

        var host: String { name.replacingOccurrences(of: " ", with: "-") + ".local" }
    }

    private static let saved = [
        SavedComputer(name: "Mac mini", model: "Mac16,10", thumbnail: "Demo-Mac", lastConnected: 12 * 60),
    ]

    private init() {
        database = try! DatabaseQueue()
        try! AppDatabase.migrate(database)
        var nearby: [NearbyComputer] = []
        for (index, saved) in Self.saved.enumerated() {
            let address = ComputerAddress(host: saved.host, port: ComputerAddress.defaultPort)
            var computer = Computer(name: saved.name, address: address)
            computer.model = saved.model
            computer.thumbnail = try! Data(contentsOf: Bundle.main.url(forResource: saved.thumbnail, withExtension: "jpg")!)
            computer.lastConnected = Date(timeIntervalSinceNow: -saved.lastConnected)
            computer.createdAt = Date(timeIntervalSinceNow: TimeInterval(index - Self.saved.count))
            try! database.write { try computer.insert($0) }
            nearby.append(NearbyComputer(name: saved.name, hostName: saved.host, port: address.port, model: saved.model))
        }
        self.nearby = NearbyBrowser(fixed: nearby)
        #if os(macOS)
        if #available(macOS 14.4, *), let screenshot = UserDefaults.standard.string(forKey: "DemoScreenshot") {
            Task {
                do {
                    try await Self.takeScreenshot(named: screenshot)
                    exit(0)
                } catch {
                    print("screenshot: \(error)")
                    exit(1)
                }
            }
        }
        #endif
    }
}

#if os(macOS)
@available(macOS 14.4, *)
extension Demo {
    private static let screenshotWindowSize = NSSize(width: 1080, height: 675)
    private static let screenshotCanvasSize = CGSize(width: 1440, height: 900)

    private static func takeScreenshot(named name: String) async throws {
        var shown: NSWindow?
        while shown == nil {
            try await Task.sleep(seconds: 0.1)
            shown = NSApp.windows.first { $0.isVisible && $0.canBecomeMain }
        }
        let window = shown!
        let isDark = UserDefaults.standard.string(forKey: "AppleInterfaceStyle") == "Dark"
        NSApp.appearance = NSAppearance(named: isDark ? .darkAqua : .aqua)
        NSApp.activate(ignoringOtherApps: true)
        window.setContentSize(screenshotWindowSize)
        window.center()
        try await Task.sleep(seconds: 2)
        let content = try await SCShareableContent.currentProcess
        let captured = content.windows.first { $0.windowID == CGWindowID(window.windowNumber) }!
        let filter = SCContentFilter(desktopIndependentWindow: captured)
        let scale = CGFloat(filter.pointPixelScale)
        let configuration = SCStreamConfiguration()
        configuration.width = Int(filter.contentRect.width * scale)
        configuration.height = Int(filter.contentRect.height * scale)
        configuration.shouldBeOpaque = false
        configuration.showsCursor = false
        let image = try await SCScreenshotManager.captureImage(contentFilter: filter, configuration: configuration)
        let width = Int(screenshotCanvasSize.width * scale)
        let height = Int(screenshotCanvasSize.height * scale)
        let context = CGContext(
            data: nil, width: width, height: height, bitsPerComponent: 8, bytesPerRow: 0,
            space: CGColorSpace(name: CGColorSpace.sRGB)!, bitmapInfo: CGImageAlphaInfo.noneSkipLast.rawValue)!
        context.setFillColor(isDark ? .black : .white)
        context.fill(CGRect(x: 0, y: 0, width: width, height: height))
        context.setShadow(offset: CGSize(width: 0, height: -12 * scale), blur: 48 * scale, color: CGColor(gray: 0, alpha: 0.35))
        context.draw(image, in: CGRect(x: (width - image.width) / 2, y: (height - image.height) / 2, width: image.width, height: image.height))
        let url = FileManager.default.temporaryDirectory.appending(path: name + ".png")
        let destination = CGImageDestinationCreateWithURL(url as CFURL, UTType.png.identifier as CFString, 1, nil)!
        CGImageDestinationAddImage(destination, context.makeImage()!, nil)
        guard CGImageDestinationFinalize(destination) else {
            throw CocoaError(.fileWriteUnknown)
        }
        print(url.path)
    }
}
#endif
#endif
