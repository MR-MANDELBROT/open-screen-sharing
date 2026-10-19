import Foundation

nonisolated enum MacCursor: Equatable, Sendable {
    case arrow
    case contextualMenu
    case pointingHand
    case openHand
    case closedHand
    case dragLink
    case dragCopy
    case operationNotAllowed
    case iBeam
    case verticalIBeam
    case crosshair
    case zoomIn
    case zoomOut
    case resize(ResizeDirections)

    nonisolated struct ResizeDirections: OptionSet, Hashable, Sendable {
        let rawValue: UInt8

        static let up = ResizeDirections(rawValue: 1 << 0)
        static let down = ResizeDirections(rawValue: 1 << 1)
        static let left = ResizeDirections(rawValue: 1 << 2)
        static let right = ResizeDirections(rawValue: 1 << 3)
        static let upLeft = ResizeDirections(rawValue: 1 << 4)
        static let upRight = ResizeDirections(rawValue: 1 << 5)
        static let downLeft = ResizeDirections(rawValue: 1 << 6)
        static let downRight = ResizeDirections(rawValue: 1 << 7)
    }

    // Cursor images screensharingd sends for the AppKit cursors of macOS 27.0.1 26A434
    // (SLSGetGlobalCursorData, forwarded unscaled).
    private static let known: [UInt64: MacCursor] = [
        0x550c_f3e1_2f82_e290: .arrow,
        0xa1d0_4e5d_7b36_38c1: .contextualMenu,
        0x794a_89c5_f925_811d: .pointingHand,
        0x5064_4df7_03aa_9698: .openHand,
        0xbee3_f9de_b429_0ac6: .closedHand,
        0xc45e_03a9_8e56_c597: .dragLink,
        0x404d_0f11_c243_6172: .dragCopy,
        0x95ad_88c4_cc9e_3d42: .operationNotAllowed,
        0x892a_7ece_621a_c8c5: .iBeam,
        0x9ae3_c16e_5e50_bde8: .verticalIBeam,
        0xbb6e_1a08_616b_a322: .crosshair,
        0x099f_7efb_ca83_ccba: .zoomIn,
        0x1343_3771_90e9_03aa: .zoomOut,
        0xd39b_94f8_38be_4e58: .resize(.left),
        0x42d8_bbc7_a95a_09ca: .resize(.right),
        0x95e8_7116_9704_c06d: .resize([.left, .right]),
        0xc462_9ef7_e72b_4692: .resize(.up),
        0xddef_b65d_0e8d_7aa3: .resize(.down),
        0x99fa_f011_b1d1_672a: .resize([.up, .down]),
        0xc272_dbe8_fb06_c3b0: .resize([.up, .down]),
        0x8d1c_c66a_b524_f521: .resize(.up),
        0x2168_3648_e46f_acfc: .resize(.down),
        0xbf87_b07e_efdd_81bf: .resize([.left, .right]),
        0x96f9_141f_df6a_48f0: .resize(.left),
        0x248f_9773_6835_ac0d: .resize(.right),
        0x36a3_3a21_5de7_2d90: .resize([.upLeft, .downRight]),
        0xe533_934d_212c_e831: .resize(.upLeft),
        0x1427_a75f_c243_7d9a: .resize(.downRight),
        0x8c24_bd91_dffa_9649: .resize([.upRight, .downLeft]),
        0x5764_40b0_b8e8_5e5b: .resize(.upRight),
        0x1653_1b30_a60c_ab6b: .resize(.downLeft),
        0x355b_7cb0_1599_7adc: .resize([.upLeft, .downRight]),
        0x6f4f_0072_c53c_6f0a: .resize([.upLeft, .downRight]),
        0xd739_435c_b04b_b4c2: .resize([.upRight, .downLeft]),
        0x7659_61df_f520_bfd4: .resize([.upRight, .downLeft]),
    ]

    init?(width: Int, height: Int, hotspotX: Int, hotspotY: Int, pixels: Data) {
        guard pixels.count >= width * height * 4 else { return nil }
        var hash: UInt64 = 0xcbf2_9ce4_8422_2325
        func mix(_ byte: UInt8) {
            hash ^= UInt64(byte)
            hash &*= 0x100_0000_01b3
        }
        for value in [width, height, hotspotX, hotspotY] {
            mix(UInt8(truncatingIfNeeded: value))
            mix(UInt8(truncatingIfNeeded: value >> 8))
        }
        pixels.withUnsafeBytes { bytes in
            for offset in stride(from: 0, to: width * height * 4, by: 4) {
                let alpha = bytes[offset + 3]
                mix(alpha)
                if alpha == 255 {
                    mix(Int(bytes[offset]) + 2 * Int(bytes[offset + 1]) + Int(bytes[offset + 2]) >= 512 ? 1 : 0)
                }
            }
        }
        guard let cursor = Self.known[hash] else { return nil }
        self = cursor
    }
}
