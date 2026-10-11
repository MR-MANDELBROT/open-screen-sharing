import ImageIO
import UIKit
import UniformTypeIdentifiers
import XCTest

final class ScreenshotTests: XCTestCase {
    private let locales = ProcessInfo.processInfo.environment["SCREENSHOT_LOCALES"]?.split(separator: ",").map(String.init) ?? ["en_US"]

    override func setUpWithError() throws {
        continueAfterFailure = false
    }

    @MainActor
    func testConnections() throws {
        XCUIDevice.shared.orientation = UIDevice.current.userInterfaceIdiom == .pad ? .landscapeLeft : .portrait
        for locale in locales {
            let app = launch(in: locale)
            XCTAssertTrue(app.buttons.matching(NSPredicate(format: "label CONTAINS %@", "Mac mini")).firstMatch.waitForExistence(timeout: 10))
            attachScreenshot(of: app, named: "01-Connections", in: locale)
        }
    }

    @MainActor
    private func launch(in locale: String) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchArguments = ["-Demo", "-AppleLanguages", "(\(language(of: locale)))", "-AppleLocale", locale]
        app.launch()
        return app
    }

    private func language(of locale: String) -> String {
        String(locale.prefix { $0 != "_" })
    }

    @MainActor
    private func attachScreenshot(of app: XCUIApplication, named name: String, in locale: String) {
        let window = app.windows.firstMatch.frame.size
        // XCUIScreen.main stays the outer display of an unfolded iPhone Duo.
        let image = XCUIScreen.screens.lazy.map { $0.screenshot().image }.first {
            abs($0.size.width - window.width) < 1 && abs($0.size.height - window.height) < 1
        } ?? XCUIScreen.main.screenshot().image
        let width = Int(image.size.width * image.scale)
        let height = Int(image.size.height * image.scale)
        let context = CGContext(
            data: nil, width: width, height: height, bitsPerComponent: 8, bytesPerRow: 0,
            space: CGColorSpace(name: CGColorSpace.sRGB)!,
            bitmapInfo: CGImageAlphaInfo.noneSkipFirst.rawValue | CGBitmapInfo.byteOrder32Little.rawValue)!
        context.translateBy(x: 0, y: CGFloat(height))
        context.scaleBy(x: image.scale, y: -image.scale)
        UIGraphicsPushContext(context)
        image.draw(at: .zero)
        UIGraphicsPopContext()
        let data = NSMutableData()
        let destination = CGImageDestinationCreateWithData(data, UTType.png.identifier as CFString, 1, nil)!
        CGImageDestinationAddImage(destination, context.makeImage()!, nil)
        XCTAssertTrue(CGImageDestinationFinalize(destination))
        let attachment = XCTAttachment(data: data as Data, uniformTypeIdentifier: UTType.png.identifier)
        attachment.name = language(of: locale) + "_" + name
        attachment.lifetime = .keepAlways
        add(attachment)
    }
}
