import Foundation

nonisolated struct MacInputSource: Hashable, Sendable, Identifiable {
    let id: String

    private static let layoutPrefix = "com.apple.keylayout."

    var isInputMethod: Bool { !id.hasPrefix(Self.layoutPrefix) }

    var title: String {
        if let name = Self.names[id] {
            return String(localized: name.title)
        }
        if let name = Self.thirdPartyNames.first(where: { id.hasPrefix($0.prefix) }) {
            return String(localized: name.title)
        }
        if id.hasPrefix(Self.layoutPrefix) {
            return String(id.dropFirst(Self.layoutPrefix.count)).replacingOccurrences(of: "-", with: " – ")
        }
        return id.split(separator: ".").last.map(String.init) ?? id
    }

    var label: String? {
        Self.names[id]?.label ?? Self.thirdPartyNames.first(where: { id.hasPrefix($0.prefix) })?.label
    }

    private static let names: [String: (title: String.LocalizationValue, label: String)] = [
        "com.apple.keylayout.ABC": ("ABC", "ABC"),
        "com.apple.keylayout.US": ("U.S.", "US"),
        "com.apple.inputmethod.SCIM.ITABC": ("Pinyin – Simplified", "拼"),
        "com.apple.inputmethod.SCIM.Shuangpin": ("Shuangpin – Simplified", "双"),
        "com.apple.inputmethod.SCIM.WBX": ("Wubi – Simplified", "五"),
        "com.apple.inputmethod.SCIM.WBH": ("Stroke – Simplified", "画"),
        "com.apple.inputmethod.TCIM.Pinyin": ("Pinyin – Traditional", "拼"),
        "com.apple.inputmethod.TCIM.Shuangpin": ("Shuangpin – Traditional", "雙"),
        "com.apple.inputmethod.TCIM.Zhuyin": ("Zhuyin – Traditional", "注"),
        "com.apple.inputmethod.TCIM.ZhuyinEten": ("Zhuyin Eten – Traditional", "注"),
        "com.apple.inputmethod.TCIM.Cangjie": ("Cangjie – Traditional", "倉"),
        "com.apple.inputmethod.TCIM.Jianyi": ("Sucheng – Traditional", "速"),
        "com.apple.inputmethod.TCIM.WBH": ("Stroke – Traditional", "畫"),
        "com.apple.inputmethod.TYIM.Phonetic": ("Phonetic – Cantonese", "粵"),
        "com.apple.inputmethod.TYIM.Cangjie": ("Cangjie – Cantonese", "倉"),
        "com.apple.inputmethod.TYIM.Sucheng": ("Sucheng – Cantonese", "速"),
        "com.apple.inputmethod.TYIM.Stroke": ("Stroke – Cantonese", "畫"),
        "com.apple.inputmethod.Kotoeri.RomajiTyping.Japanese": ("Hiragana", "あ"),
        "com.apple.inputmethod.Kotoeri.RomajiTyping.Japanese.Katakana": ("Katakana", "ア"),
        "com.apple.inputmethod.Kotoeri.RomajiTyping.Roman": ("Romaji", "A"),
        "com.apple.inputmethod.Kotoeri.KanaTyping.Japanese": ("Hiragana", "あ"),
        "com.apple.inputmethod.Kotoeri.KanaTyping.Japanese.Katakana": ("Katakana", "ア"),
        "com.apple.inputmethod.Kotoeri.KanaTyping.Roman": ("Romaji", "A"),
        "com.apple.inputmethod.Korean.2SetKorean": ("2-Set Korean", "한"),
        "com.apple.inputmethod.Korean.3SetKorean": ("3-Set Korean", "한"),
        "com.apple.inputmethod.Korean.390Sebulshik": ("3-Set Korean (390)", "한"),
    ]

    private static let thirdPartyNames: [(prefix: String, title: String.LocalizationValue, label: String?)] = [
        ("com.sogou.inputmethod.", "Sogou Pinyin", "搜"),
        ("com.tencent.inputmethod.wetype.", "WeType", "微"),
        ("im.rime.inputmethod.", "Squirrel", nil),
    ]
}
