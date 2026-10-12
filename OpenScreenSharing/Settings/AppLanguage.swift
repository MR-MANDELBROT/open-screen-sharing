import Foundation

enum AppLanguage {
    private static let key = "AppleLanguages"

    static var available: [String] {
        Bundle.main.localizations.filter { $0 != "Base" }.sorted { name(of: $0).localizedStandardCompare(name(of: $1)) == .orderedAscending }
    }

    static var selected: String? {
        guard let identifier = Bundle.main.bundleIdentifier,
            let current = (UserDefaults.standard.persistentDomain(forName: identifier)?[key] as? [String])?.first
        else { return nil }
        return Bundle.preferredLocalizations(from: available, forPreferences: [current]).first
    }

    static func select(_ code: String?) {
        if let code {
            UserDefaults.standard.set([code], forKey: key)
        } else {
            UserDefaults.standard.removeObject(forKey: key)
        }
        #if os(macOS)
        // AppKit keeps a left-to-right layout for an app-only AppleLanguages override until these are set
        // (AppKit 2775.10.103.1, macOS 27.0.1 26A434, UUID 2688AFBC-5C39-3132-BCF5-8D00D32D7615,
        // _NSUseRTLForSystemUIDefaultValueFunction 0x184f34658..0x184f346c4; UIFoundation 1056, macOS 27.0.1 26A434,
        // UUID 4BC0E1C7-F192-387E-A05D-DB35F4C46F09, +[NSParagraphStyle _defaultWritingDirection] 0x186510244..0x186510324).
        let rightToLeft = code.map { Locale.Language(identifier: $0).characterDirection == .rightToLeft } ?? false
        for direction in ["AppleTextDirection", "NSForceRightToLeftWritingDirection"] {
            if rightToLeft {
                UserDefaults.standard.set(true, forKey: direction)
            } else {
                UserDefaults.standard.removeObject(forKey: direction)
            }
        }
        #endif
    }

    static func systemDefaultName(in selected: String?) -> String {
        let system = UserDefaults.standard.persistentDomain(forName: UserDefaults.globalDomain)?[key] as? [String] ?? Locale.preferredLanguages
        guard #available(iOS 16, *), let code = selected ?? Bundle.preferredLocalizations(from: available, forPreferences: system).first else {
            return String(localized: "System Default")
        }
        return String(localized: LocalizedStringResource("System Default", locale: Locale(identifier: code)))
    }

    static func name(of code: String) -> String {
        Locale(identifier: code).localizedString(forIdentifier: code) ?? code
    }
}
