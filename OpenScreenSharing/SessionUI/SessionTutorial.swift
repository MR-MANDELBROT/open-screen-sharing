import SwiftUI

enum SessionTutorial: String, CaseIterable, Codable, Identifiable {
    case pressToDrag
    case pointerLock

    var id: Self { self }
}

#if os(iOS)
struct SessionTutorialView: View {
    let pages: [SessionTutorial]
    let onDone: () -> Void
    @ObservedObject private var settings = AppSettings.shared
    @State private var index = 0
    @Environment(\.horizontalSizeClass) private var horizontalSizeClass
    @Environment(\.verticalSizeClass) private var verticalSizeClass

    var body: some View {
        let page = pages[index]
        ZStack {
            Color.black.opacity(0.45)
                .ignoresSafeArea()
            Group {
                if horizontalSizeClass == .compact && verticalSizeClass == .regular {
                    VStack(alignment: .leading, spacing: 16) {
                        illustration(page)
                        text(page)
                        button(page)
                    }
                    .frame(width: 310)
                } else {
                    HStack(alignment: .top, spacing: 24) {
                        illustration(page)
                        VStack(alignment: .leading, spacing: 0) {
                            text(page)
                            Spacer(minLength: 16)
                            button(page)
                        }
                        .frame(width: 244)
                        .frame(maxHeight: .infinity)
                    }
                    .fixedSize(horizontal: false, vertical: true)
                }
            }
            .padding(20)
            .background(.regularMaterial, in: RoundedRectangle(cornerRadius: 30, style: .continuous))
            .dynamicTypeSize(...DynamicTypeSize.xxLarge)
            .padding(16)
        }
    }

    private func illustration(_ page: SessionTutorial) -> some View {
        VStack(spacing: 14) {
            Group {
                switch page {
                case .pressToDrag: PressToDragIllustration()
                case .pointerLock: PointerLockIllustration()
                }
            }
            .frame(width: 310, height: 210)
            .clipShape(RoundedRectangle(cornerRadius: 16, style: .continuous))
            .environment(\.colorScheme, .dark)
            .accessibilityHidden(true)
            .id(page)
            if pages.count > 1 {
                HStack(spacing: 8) {
                    ForEach(pages.indices, id: \.self) { i in
                        Circle()
                            .fill(i == index ? Color.primary : Color.secondary.opacity(0.4))
                            .frame(width: 7, height: 7)
                    }
                }
            }
        }
    }

    private func text(_ page: SessionTutorial) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            switch page {
            case .pressToDrag:
                Text("Press and Hold to Drag")
                    .font(.title2.bold())
                Text("Touch and hold anywhere on the screen until the pointer presses down, then move your finger to drag windows or select text.")
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
            case .pointerLock:
                Text("Pointer Lock")
                    .font(.title2.bold())
                Text(
                    "When using a mouse or trackpad, turn on \(String(localized: SessionCommandCategory.control.title)) › \(String(localized: SessionCommand.pointerLock.title)) so moving to the edge of the screen won’t open the Dock or Control Center."
                )
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
            }
        }
        .fixedSize(horizontal: false, vertical: true)
    }

    private func button(_ page: SessionTutorial) -> some View {
        let isLast = index == pages.count - 1
        return Button {
            settings.seenTutorials.insert(page)
            if isLast {
                onDone()
            } else {
                withAnimation { index += 1 }
            }
        } label: {
            (isLast ? Text("OK") : Text("Next Tip"))
                .font(.body.weight(.semibold))
                .frame(maxWidth: .infinity)
        }
        .buttonStyle(.borderedProminent)
        .buttonBorderShape(.capsule)
        .controlSize(.large)
    }
}

private enum TutorialPalette {
    static let wallpaper = Gradient(colors: [
        Color(red: 0x24 / 255, green: 0x47 / 255, blue: 0x6E / 255), Color(red: 0x5E / 255, green: 0x3B / 255, blue: 0x78 / 255),
    ])
    static let window = Color(red: 0xF5 / 255, green: 0xF5 / 255, blue: 0xF7 / 255)
    static let line = Color(red: 0xD1 / 255, green: 0xD1 / 255, blue: 0xD6 / 255)
    static let lights = [
        Color(red: 1, green: 0x5F / 255, blue: 0x57 / 255), Color(red: 0xFE / 255, green: 0xBC / 255, blue: 0x2E / 255),
        Color(red: 0x28 / 255, green: 0xC8 / 255, blue: 0x40 / 255),
    ]

    static func drawPointer(_ context: inout GraphicsContext, at center: CGPoint, radius: CGFloat, pressed: CGFloat) {
        let circle = Path(ellipseIn: CGRect(x: center.x - radius, y: center.y - radius, width: radius * 2, height: radius * 2))
        context.fill(circle, with: .color(Color(white: 0.28).opacity(0.62 + 0.2 * pressed)))
        context.stroke(circle, with: .color(.white.opacity(0.55)), lineWidth: 0.8)
    }
}

private struct PressToDragIllustration: View {
    private static let period = 5.2
    private static let windowOrigin = CGPoint(x: 34, y: 44)
    private static let fingerOrigin = CGPoint(x: 196, y: 148)
    private static let travel = CGPoint(x: 84, y: 8)
    private static let pointerOffset = CGPoint(x: 72, y: 10)
    private static let captions: [(start: Double, text: LocalizedStringKey)] = [
        (0, "Touch and Hold Anywhere"), (0.65, "Hold Still…"), (1.15, "Pointer Pressed"), (1.45, "Move to Drag"),
        (3.1, "Lift to Drop"), (4.6, "Touch and Hold Anywhere"),
    ]

    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var start = Date()

    var body: some View {
        TimelineView(.animation(minimumInterval: nil, paused: reduceMotion)) { timeline in
            let time = reduceMotion ? 2.2 : timeline.date.timeIntervalSince(start).truncatingRemainder(dividingBy: Self.period)
            Canvas { context, size in
                draw(in: &context, size: size, at: time)
            }
        }
    }

    private func draw(in context: inout GraphicsContext, size: CGSize, at t: Double) {
        func progress(_ from: Double, _ to: Double) -> CGFloat { CGFloat(min(1, max(0, (t - from) / (to - from)))) }
        func eased(_ x: CGFloat) -> CGFloat { x < 0.5 ? 4 * x * x * x : 1 - pow(-2 * x + 2, 3) / 2 }

        let appear = progress(0.5, 0.65)
        let hold = progress(0.65, 1.15)
        let move = eased(progress(1.45, 2.75))
        let lift = progress(3.1, 3.3)
        let press = progress(1.15, 1.25) - progress(3.1, 3.2)
        let reset = t >= 4.6
        let fade = reset ? progress(4.6, 4.9) : 1 - progress(4.3, 4.6)
        let offset =
            reset ? .zero : CGPoint(x: Self.travel.x * move, y: Self.travel.y * move - 12 * sin(.pi * move))

        context.fill(
            Path(CGRect(origin: .zero, size: size)),
            with: .linearGradient(TutorialPalette.wallpaper, startPoint: .zero, endPoint: CGPoint(x: size.width, y: size.height)))
        context.fill(Path(CGRect(x: 0, y: 0, width: size.width, height: 14)), with: .color(.white.opacity(0.2)))

        let window = CGPoint(x: Self.windowOrigin.x + offset.x, y: Self.windowOrigin.y + offset.y)
        var windowContext = context
        windowContext.opacity = fade
        windowContext.addFilter(.shadow(color: .black.opacity(0.3 + 0.2 * press), radius: 3 + 4 * press, x: 0, y: 3 + 6 * press))
        windowContext.drawLayer { layer in
            let frame = CGRect(x: window.x, y: window.y, width: 132, height: 88)
            let shape = Path(roundedRect: frame, cornerRadius: 8, style: .continuous)
            layer.clip(to: shape)
            layer.fill(shape, with: .color(TutorialPalette.window))
            let bar = (227 - 19 * press) / 255
            layer.fill(
                Path(CGRect(x: frame.minX, y: frame.minY, width: frame.width, height: 19)),
                with: .color(Color(red: bar, green: bar, blue: (232 - 16 * press) / 255)))
            for (i, color) in TutorialPalette.lights.enumerated() {
                let x = frame.minX + 11 + CGFloat(i) * 10
                layer.fill(Path(ellipseIn: CGRect(x: x - 3.3, y: frame.minY + 6.2, width: 6.6, height: 6.6)), with: .color(color))
            }
            for (y, width) in [(32.0, 90.0), (44, 108), (56, 70), (68, 96)] {
                layer.fill(
                    Path(roundedRect: CGRect(x: frame.minX + 12, y: frame.minY + y, width: width, height: 5), cornerRadius: 2.5),
                    with: .color(TutorialPalette.line))
            }
        }

        var pointerContext = context
        pointerContext.opacity = fade
        TutorialPalette.drawPointer(
            &pointerContext, at: CGPoint(x: window.x + Self.pointerOffset.x, y: window.y + Self.pointerOffset.y),
            radius: 7 * (1 - 0.15 * press), pressed: press)

        if t >= 0.5, t < 3.3 {
            var finger = context
            finger.opacity = min(appear, 1 - lift)
            let scale = t < 1 ? 1.25 - 0.25 * appear : 1 + 0.2 * lift
            let center = CGPoint(x: Self.fingerOrigin.x + offset.x, y: Self.fingerOrigin.y + offset.y)
            func circle(_ radius: CGFloat) -> Path {
                Path(ellipseIn: CGRect(x: center.x - radius, y: center.y - radius, width: radius * 2, height: radius * 2))
            }
            finger.stroke(circle(24 * scale), with: .color(.white.opacity(0.25)), lineWidth: 3.5)
            if hold > 0 {
                var ring = Path()
                ring.addArc(
                    center: center, radius: 24 * scale, startAngle: .degrees(-90), endAngle: .degrees(-90 + 360 * Double(hold)),
                    clockwise: false)
                finger.stroke(ring, with: .color(.accentColor), style: StrokeStyle(lineWidth: 3.5, lineCap: .round))
            }
            if t >= 1.15, t < 1.5 {
                let pulse = progress(1.15, 1.5)
                finger.stroke(circle(24 + 10 * pulse), with: .color(.accentColor.opacity(1 - pulse)), lineWidth: 2)
            }
            finger.fill(circle(17 * scale), with: .color(.white.opacity(0.35 * (1 - press))))
            finger.fill(circle(17 * scale), with: .color(.accentColor.opacity(0.6 * press)))
            finger.stroke(circle(17 * scale), with: .color(.white.opacity(0.9)), lineWidth: 1.5)
        }

        let index = Self.captions.lastIndex { t >= $0.start } ?? 0
        let caption = Self.captions[index]
        var alpha = index == 0 ? 1 : min(1, (t - caption.start) / 0.15)
        if t >= 4.3, t < 4.6 { alpha *= Double(1 - progress(4.3, 4.6)) }
        var captionContext = context
        captionContext.opacity = alpha
        let text = captionContext.resolve(Text(caption.text).font(.system(size: 12.5, weight: .medium)).foregroundColor(.white))
        let textSize = text.measure(in: size)
        let center = CGPoint(x: size.width / 2, y: 191)
        captionContext.fill(
            Path(
                roundedRect: CGRect(x: center.x - textSize.width / 2 - 13, y: center.y - 11, width: textSize.width + 26, height: 22),
                cornerRadius: 11),
            with: .color(.black.opacity(0.45)))
        captionContext.draw(text, at: center)
    }
}

private struct PointerLockIllustration: View {
    private static let toolbar: [SessionCommand] = [.audio, .pointerLock, .keyboard, .clipboard]

    var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: 6) {
                item("xmark")
                    .background(Circle().fill(Color(white: 0.24)))
                    .overlay(Circle().strokeBorder(.white.opacity(0.12)))
                HStack(spacing: 0) {
                    ForEach(Self.toolbar) { command in
                        if command == .pointerLock {
                            item(command.systemImage, highlighted: true)
                                .overlay(alignment: .top) {
                                    callout
                                        .fixedSize()
                                        .offset(y: 32)
                                }
                        } else {
                            item(command.systemImage)
                        }
                    }
                }
                .padding(.horizontal, 3)
                .background(Capsule().fill(Color(white: 0.24)))
                .overlay(Capsule().strokeBorder(.white.opacity(0.12)))
                item("ellipsis")
                    .background(Circle().fill(Color(white: 0.24)))
                    .overlay(Circle().strokeBorder(.white.opacity(0.12)))
            }
            .padding(.top, 12)
            .padding(.bottom, 27)
            HStack(spacing: 10) {
                tablet(locked: false)
                tablet(locked: true)
            }
            .padding(.top, 8)
            Spacer(minLength: 0)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(Color(white: 0.17))
    }

    private var callout: some View {
        VStack(spacing: 0) {
            Path { path in
                path.move(to: CGPoint(x: 0, y: 6))
                path.addLine(to: CGPoint(x: 5, y: 0))
                path.addLine(to: CGPoint(x: 10, y: 6))
                path.closeSubpath()
            }
            .fill(Color.accentColor)
            .frame(width: 10, height: 6)
            Text("Pointer Lock")
                .font(.system(size: 11.5, weight: .semibold))
                .foregroundStyle(.white)
                .padding(.horizontal, 10)
                .frame(height: 19)
                .background(Capsule().fill(Color.accentColor))
        }
    }

    private func item(_ systemImage: String, highlighted: Bool = false) -> some View {
        Image(systemName: systemImage)
            .font(.system(size: 14, weight: .medium))
            .foregroundStyle(highlighted ? Color.white : Color(white: 0.85))
            .frame(width: 32, height: 32)
            .background {
                if highlighted {
                    Circle().fill(Color.accentColor).padding(1)
                }
            }
    }

    private func tablet(locked: Bool) -> some View {
        VStack(spacing: 5) {
            Canvas { context, size in
                drawScreen(in: &context, size: size, locked: locked)
            }
            .frame(width: 132, height: 90)
            .clipShape(RoundedRectangle(cornerRadius: 7, style: .continuous))
            .padding(4)
            .background(RoundedRectangle(cornerRadius: 10, style: .continuous).fill(Color(white: 0.04)))
            .overlay(RoundedRectangle(cornerRadius: 10, style: .continuous).strokeBorder(Color(white: 0.28)))
            Label {
                locked ? Text("On") : Text("Off")
            } icon: {
                Image(systemName: locked ? "checkmark.circle.fill" : "xmark.circle.fill")
                    .symbolRenderingMode(.palette)
                    .foregroundStyle(.white, locked ? Color.green : Color.red)
            }
            .font(.system(size: 11.5))
            .foregroundStyle(.secondary)
        }
    }

    private func drawScreen(in context: inout GraphicsContext, size: CGSize, locked: Bool) {
        context.fill(
            Path(CGRect(origin: .zero, size: size)),
            with: .linearGradient(TutorialPalette.wallpaper, startPoint: .zero, endPoint: CGPoint(x: size.width, y: size.height)))
        context.fill(Path(CGRect(x: 0, y: 0, width: size.width, height: 6)), with: .color(.white.opacity(0.2)))
        let window = Path(roundedRect: CGRect(x: 16, y: 16, width: 62, height: 40), cornerRadius: 4)
        context.fill(window, with: .color(TutorialPalette.window))
        var bar = context
        bar.clip(to: window)
        bar.fill(Path(CGRect(x: 16, y: 16, width: 62, height: 9)), with: .color(Color(white: 0.89)))
        for (i, color) in TutorialPalette.lights.enumerated() {
            context.fill(Path(ellipseIn: CGRect(x: 19.4 + CGFloat(i) * 5, y: 18.9, width: 3.2, height: 3.2)), with: .color(color))
        }
        for i in 0..<3 {
            context.fill(
                Path(ellipseIn: CGRect(x: 64.7, y: 58.7 + CGFloat(i) * 6, width: 2.6, height: 2.6)), with: .color(.white.opacity(0.5)))
        }
        if locked {
            context.fill(
                Path(roundedRect: CGRect(x: 34, y: 78, width: 64, height: 11), cornerRadius: 3.5), with: .color(.white.opacity(0.28)))
            for (i, color) in [Color.cyan, .orange, .green, .purple, .red].enumerated() {
                context.fill(
                    Path(roundedRect: CGRect(x: 37.5 + CGFloat(i) * 12, y: 80, width: 9, height: 7.5), cornerRadius: 2), with: .color(color))
            }
            TutorialPalette.drawPointer(&context, at: CGPoint(x: 66, y: 81), radius: 4.5, pressed: 0)
        } else {
            let dock = Path(roundedRect: CGRect(x: 22, y: 64, width: 88, height: 21), cornerRadius: 7, style: .continuous)
            context.fill(dock, with: .color(Color(white: 0.95).opacity(0.72)))
            context.stroke(dock, with: .color(.white.opacity(0.6)), lineWidth: 0.6)
            for (i, color) in [Color.blue, .green, .orange, .pink, .indigo].enumerated() {
                context.fill(
                    Path(roundedRect: CGRect(x: 27 + CGFloat(i) * 16.5, y: 67.5, width: 12.5, height: 12.5), cornerRadius: 3.2, style: .continuous),
                    with: .color(color))
            }
            TutorialPalette.drawPointer(&context, at: CGPoint(x: 70, y: 76), radius: 5, pressed: 0)
        }
    }
}
#endif
