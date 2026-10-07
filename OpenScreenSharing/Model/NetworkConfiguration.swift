nonisolated enum NetworkQuality: String, Codable, CaseIterable, Identifiable, Sendable {
    case localNetwork
    /// The mode-5 reply from iPad17,1 on iPadOS 27.2 (24B5099f) sets 6 Mbit/s maximum and 0.333 minimum.
    case internet

    var id: Self { self }
}

nonisolated enum ChromaSampling: Sendable {
    case yuv420
    case yuv444

    // AVConference mode 7 picks 444f from {420f, 444f}, and 420f from {420f},
    // on macOS 27.0.1 (26A434) and 27.2 (26B5101f).
    func pixelFormats(available: Int32) -> Int32 {
        switch self {
        case .yuv420:
            return available & 0x07
        case .yuv444:
            return available
        }
    }
}

nonisolated enum MaximumFrameRate: Int, Codable, CaseIterable, Identifiable, Sendable {
    case thirty = 30
    case sixty = 60

    var id: Self { self }
}

nonisolated struct NetworkConfiguration: Equatable, Sendable {
    var quality: NetworkQuality = .localNetwork
    var internetMaximumFrameRate: MaximumFrameRate = .sixty

    var usesCoreDevice: Bool { quality == .internet }

    var bands: Int { usesCoreDevice ? 1 : 4 }

    var maximumFrameRate: MaximumFrameRate {
        quality == .localNetwork ? .sixty : internetMaximumFrameRate
    }

    /// Mode 5 negotiated 4:2:0 on iPad17,1 / iPadOS 27.2 (24B5099f).
    var chroma: ChromaSampling { usesCoreDevice ? .yuv420 : .yuv444 }
}
