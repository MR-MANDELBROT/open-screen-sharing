import Foundation
import UIKit

enum ServerFeature {
    static var isAvailable: Bool {
        guard #available(iOS 27, *) else { return false }
        return !ProcessInfo.processInfo.isiOSAppOnMac
    }

    static var systemImage: String {
        UIDevice.current.userInterfaceIdiom == .pad ? "ipad" : "iphone"
    }
}
