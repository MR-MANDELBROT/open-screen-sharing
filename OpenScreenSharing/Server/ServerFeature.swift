import Foundation
import UIKit

enum ServerFeature {
    static var isShown: Bool {
        !ProcessInfo.processInfo.isiOSAppOnMac
    }

    static var isAvailable: Bool {
        guard #available(iOS 27, *) else { return false }
        return isShown
    }

    static var systemImage: String {
        UIDevice.current.userInterfaceIdiom == .pad ? "ipad" : "iphone"
    }
}
