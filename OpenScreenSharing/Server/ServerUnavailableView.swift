import SwiftUI
import UIKit

struct ServerUnavailableView: View {
    private var isPad: Bool {
        UIDevice.current.userInterfaceIdiom == .pad
    }

    private var systemImage: String {
        guard #available(iOS 17.4, *) else { return "exclamationmark.triangle" }
        return isPad ? "ipad.badge.exclamationmark" : "iphone.badge.exclamationmark"
    }

    var body: some View {
        ContentUnavailableViewCompat {
            if isPad {
                Label("Unsupported iPadOS Version", systemImage: systemImage)
            } else {
                Label("Unsupported iOS Version", systemImage: systemImage)
            }
        } description: {
            if isPad {
                Text("Sharing this iPad’s screen needs iPadOS 27 or later.")
            } else {
                Text("Sharing this iPhone’s screen needs iOS 27 or later.")
            }
        }
        .navigationTitle("Server")
    }
}
