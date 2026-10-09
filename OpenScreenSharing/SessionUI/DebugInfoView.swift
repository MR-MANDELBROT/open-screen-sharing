import SwiftUI
#if os(iOS)
import UIKit
#endif

struct DebugInfoView: View {
    @ObservedObject var controller: SessionController

    @Environment(\.horizontalSizeClass) private var horizontalSizeClass
    @State private var figures: DebugInfoMonitor.Figures?

    static let pictureInterval = 1.0 / 60
    static let hitch = 0.05
    private static let slow = pictureInterval * 2
    private static let columns = 27
    private static let lossWarning = 0.01
    private static let dropWarning = 1.0

    private var fontSize: CGFloat { horizontalSizeClass == .compact ? 11 : 12 }

    private var width: CGFloat {
        #if os(iOS)
        let font = UIFont.monospacedSystemFont(ofSize: fontSize, weight: .regular)
        #else
        let font = NSFont.monospacedSystemFont(ofSize: fontSize, weight: .regular)
        #endif
        return ("0" as NSString).size(withAttributes: [.font: font]).width * CGFloat(Self.columns)
    }

    var body: some View {
        let video = figures?.video
        let isVideo = controller.isVideo
        VStack(spacing: 0) {
            pair {
                Text(verbatim: isVideo ? "Apple VNC" : "VNC")
                    .foregroundStyle(isVideo ? Color.green : .orange)
            } trailing: {
                Text(verbatim: (isVideo ? controller.info.videoCodec : figures?.encoding) ?? "—")
            }
            pair {
                Text(verbatim: imageSize)
            } trailing: {
                Text(verbatim: figures?.shownPerSecond.map { String(format: "%.0f fps", $0) } ?? "—")
                    .foregroundStyle(dropsFrames(figures) ? Color.orange : .white)
            }
            VStack(spacing: 0) {
                row("Bitrate", figures?.bitsPerSecond.map(Self.bitrate))
                if isVideo {
                    row(
                        "Loss / Keyframes", video?.loss.map { String(format: "%.1f%%", $0 * 100) },
                        video.map { "\($0.keyframeRequests)" },
                        firstColor: (video?.loss ?? 0) > Self.lossWarning ? .red : .white)
                    row(
                        "RTT / Queue", figures?.roundTripMilliseconds.map(Self.wholeMilliseconds),
                        video.map { Self.wholeMilliseconds($0.queuingDelayMilliseconds) + "ms" })
                } else {
                    row("RTT", figures?.roundTripMilliseconds.map { Self.wholeMilliseconds($0) + "ms" })
                }
                row(
                    "To Screen", figures?.toScreenMilliseconds.map(Self.milliseconds),
                    color: (figures?.toScreenMilliseconds ?? 0) > Self.slow * 1000 ? .orange : .white)
                if isVideo {
                    row(
                        "Buffer / Decode", figures?.holdMilliseconds.map(Self.wholeMilliseconds),
                        figures?.decodeMilliseconds.map { Self.wholeMilliseconds($0) + "ms" },
                        secondColor: (figures?.decodeMilliseconds ?? 0) > Self.pictureInterval * 1000 ? .orange : .white)
                }
            }
            .padding(.top, 4)
            ToScreenGraph(monitor: controller.debugInfo)
                .frame(height: fontSize * 3)
                .padding(.top, 2)
                .accessibilityHidden(true)
        }
        .font(.system(size: fontSize, weight: .regular, design: .monospaced))
        .monospacedDigit()
        .lineLimit(1)
        .foregroundStyle(.white)
        .frame(width: width)
        .padding(.horizontal, 8)
        .padding(.vertical, 6)
        .background(.black.opacity(0.8), in: RoundedRectangle(cornerRadius: 8))
        .environment(\.colorScheme, .dark)
        .accessibilityElement(children: .combine)
        .onReceive(controller.debugInfo.$figures) { figures = $0 }
        .task {
            let monitor = controller.debugInfo
            monitor.start()
            while !Task.isCancelled {
                controller.sampleDebugInfo()
                try? await Task.sleep(seconds: 1)
            }
            monitor.stop()
        }
    }

    private var imageSize: String {
        let size = controller.imageSize
        guard size.width > 0, size.height > 0 else { return "—" }
        return "\(Int(size.width))x\(Int(size.height))"
    }

    private func dropsFrames(_ figures: DebugInfoMonitor.Figures?) -> Bool {
        guard controller.isVideo, let ready = figures?.readyPerSecond, let shown = figures?.shownPerSecond else { return false }
        return ready - shown > Self.dropWarning
    }

    private func pair(@ViewBuilder leading: () -> some View, @ViewBuilder trailing: () -> some View) -> some View {
        HStack(spacing: 0) {
            leading()
            Spacer(minLength: 8)
            HStack(spacing: 0) {
                trailing()
            }
            .layoutPriority(1)
        }
    }

    private func row(_ label: String, _ value: String?, color: Color = .white) -> some View {
        pair {
            Text(verbatim: label)
        } trailing: {
            Text(verbatim: value ?? "—")
                .foregroundStyle(color)
        }
    }

    private func row(
        _ label: String, _ first: String?, _ second: String?, firstColor: Color = .white, secondColor: Color = .white
    ) -> some View {
        pair {
            Text(verbatim: label)
        } trailing: {
            Text(verbatim: first ?? "—")
                .foregroundStyle(firstColor)
            Text(verbatim: " / ")
            Text(verbatim: second ?? "—")
                .foregroundStyle(secondColor)
        }
    }

    private static func bitrate(_ bitsPerSecond: Double) -> String {
        bitsPerSecond >= 1_000_000 ? String(format: "%.1fMbps", bitsPerSecond / 1_000_000) : String(format: "%.0fkbps", bitsPerSecond / 1000)
    }

    private static func milliseconds(_ value: Double) -> String {
        value < 1000 ? String(format: "%.1fms", value) : String(format: "%.1fs", value / 1000)
    }

    private static func wholeMilliseconds(_ value: Double) -> String {
        value < 1 ? "<1" : String(format: "%.0f", value)
    }
}

private struct ToScreenGraph: View {
    @ObservedObject var monitor: DebugInfoMonitor

    @Environment(\.displayScale) private var displayScale

    private static let top = DebugInfoView.pictureInterval * 4
    private static let gridColumns = 6

    var body: some View {
        let values = monitor.toScreen
        Canvas { context, size in
            let pixel = 1 / displayScale
            var grid = Path()
            for column in 1..<Self.gridColumns {
                let x = size.width * CGFloat(column) / CGFloat(Self.gridColumns)
                grid.move(to: CGPoint(x: x, y: 0))
                grid.addLine(to: CGPoint(x: x, y: size.height))
            }
            let frameLine = (size.height - 1) * (1 - CGFloat(DebugInfoView.pictureInterval / Self.top))
            grid.move(to: CGPoint(x: 0, y: frameLine))
            grid.addLine(to: CGPoint(x: size.width, y: frameLine))
            context.stroke(grid, with: .color(.white.opacity(0.3)), lineWidth: pixel)
            var axes = Path()
            axes.move(to: CGPoint(x: pixel / 2, y: 0))
            axes.addLine(to: CGPoint(x: pixel / 2, y: size.height - pixel / 2))
            axes.addLine(to: CGPoint(x: size.width - pixel / 2, y: size.height - pixel / 2))
            axes.addLine(to: CGPoint(x: size.width - pixel / 2, y: 0))
            context.stroke(axes, with: .color(.white), lineWidth: pixel)

            guard values.count > 1 else { return }
            let step = size.width / CGFloat(DebugInfoMonitor.graphLength - 1)
            let start = CGFloat(DebugInfoMonitor.graphLength - values.count) * step
            func point(_ index: Int) -> CGPoint {
                let fraction = min(values[index] / Self.top, 1)
                return CGPoint(x: start + CGFloat(index) * step, y: (size.height - 1) * (1 - fraction))
            }
            var smooth = Path()
            var hitched = Path()
            for index in 1..<values.count {
                let hitches = max(values[index - 1], values[index]) > DebugInfoView.hitch
                if hitches {
                    hitched.move(to: point(index - 1))
                    hitched.addLine(to: point(index))
                } else {
                    smooth.move(to: point(index - 1))
                    smooth.addLine(to: point(index))
                }
            }
            let line = StrokeStyle(lineWidth: 1, lineCap: .round)
            context.stroke(smooth, with: .color(.blue), style: line)
            context.stroke(hitched, with: .color(.red), style: line)
        }
    }
}
