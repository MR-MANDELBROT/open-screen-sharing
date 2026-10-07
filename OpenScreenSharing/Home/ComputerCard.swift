import SwiftUI

struct ComputerCard: View {
    let name: String
    let detail: String?
    let device: DeviceAppearance
    var thumbnail: Data? = nil
    var availability: ComputerAvailability? = nil

    private static let cornerRadius: CGFloat = 18

    @ScaledMetric(relativeTo: .headline) private var dotSize: CGFloat = 12
    @State private var isHovered = false

    private static var hoverShape: some Shape {
        RoundedRectangle(cornerRadius: cornerRadius).inset(by: -8)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            RoundedRectangle(cornerRadius: Self.cornerRadius)
                .fill(Color(uiColor: .quaternarySystemFill))
                .aspectRatio(16 / 10, contentMode: .fit)
                .overlay(alignment: .top) {
                    if let thumbnail {
                        ComputerThumbnail(data: thumbnail, dimmed: availability == .unavailable)
                    } else {
                        GeometryReader { proxy in
                            DeviceIcon(
                                device: device, size: CGSize(width: proxy.size.width * 0.7, height: proxy.size.height * 0.7),
                                dimmed: availability == .unavailable
                            )
                            .frame(maxWidth: .infinity, maxHeight: .infinity)
                        }
                    }
                }
                .clipShape(RoundedRectangle(cornerRadius: Self.cornerRadius))
                .availabilityDot(availability, size: dotSize, inset: Self.cornerRadius * (1 - 1 / 2.squareRoot()))
            ComputerLabel(name: name, detail: detail)
                .font(.headline)
        }
        .contentShape(Rectangle())
        .background {
            Self.hoverShape
                .fill(Color(uiColor: .quaternarySystemFill))
                .opacity(isHovered ? 1 : 0)
                .animation(.smoothCompat(duration: 0.2), value: isHovered)
        }
        .onHover { isHovered = $0 }
        .accessibilityElement(children: .combine)
        .accessibilityAddTraits(.isButton)
    }
}

struct ComputerRow: View {
    let name: String
    let detail: String?
    let device: DeviceAppearance
    var availability: ComputerAvailability? = nil

    @ScaledMetric(relativeTo: .body) private var iconSize: CGFloat = 36
    @ScaledMetric(relativeTo: .body) private var dotSize: CGFloat = 8

    var body: some View {
        HStack(spacing: 14) {
            DeviceIcon(device: device, size: CGSize(width: iconSize, height: iconSize), dimmed: availability == .unavailable)
                .availabilityDot(availability, size: dotSize, inset: dotSize / 4)
                .frame(width: iconSize, height: iconSize)
            ComputerLabel(name: name, detail: detail)
                .font(.body)
            Spacer(minLength: 0)
        }
        .padding(.vertical, 2)
        .contentShape(Rectangle())
        .accessibilityElement(children: .combine)
    }
}

private struct ComputerLabel: View {
    let name: String
    let detail: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(name)
                .lineLimit(1)
            if let detail {
                Text(detail)
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
            }
        }
    }
}

private struct AvailabilityDot: ViewModifier {
    let availability: ComputerAvailability?
    let size: CGFloat
    let inset: CGFloat

    func body(content: Content) -> some View {
        let ring = size / 4
        content
            .mask {
                ZStack(alignment: .bottomTrailing) {
                    Rectangle().padding(-size)
                    Circle()
                        .frame(width: size + ring * 2, height: size + ring * 2)
                        .padding([.bottom, .trailing], inset - size / 2 - ring)
                        .opacity(availability == nil ? 0 : 1)
                        .blendMode(.destinationOut)
                }
                .compositingGroup()
            }
            .overlay(alignment: .bottomTrailing) {
                Circle()
                    .fill(color)
                    .frame(width: size, height: size)
                    .padding([.bottom, .trailing], inset - size / 2)
                    .accessibilityLabel(availability == .available ? Text("Available") : Text("Not Available"))
                    .accessibilityHidden(availability == nil)
            }
    }

    private var color: Color {
        switch availability {
        case .available: .green
        case .unavailable: .gray
        case nil: .clear
        }
    }
}

private extension View {
    func availabilityDot(_ availability: ComputerAvailability?, size: CGFloat, inset: CGFloat) -> some View {
        modifier(AvailabilityDot(availability: availability, size: size, inset: inset))
    }
}

private struct ComputerThumbnail: View {
    let data: Data
    var dimmed = false

    @State private var image: UIImage?

    init(data: Data, dimmed: Bool = false) {
        self.data = data
        self.dimmed = dimmed
        _image = State(initialValue: UIImage(data: data))
    }

    var body: some View {
        Group {
            if let image {
                Image(uiImage: image)
                    .resizable()
                    .aspectRatio(contentMode: image.size.height > image.size.width ? .fit : .fill)
                    .saturation(dimmed ? 0 : 1)
                    .opacity(dimmed ? 0.5 : 1)
            }
        }
        .onChangeCompat(of: data) { image = UIImage(data: data) }
        .accessibilityHidden(true)
    }
}

extension Computer {
    var detailText: String {
        guard let lastConnected else { return address.description }
        let date = lastConnected.formatted(.relative(presentation: .named))
        return String(localized: "Last connected \(date)")
    }
}
