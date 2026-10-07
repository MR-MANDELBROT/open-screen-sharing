import SwiftUI

struct ComputersView: View {
    let connect: (SessionRoute) -> Void
    let openInNewWindow: ((SessionRoute) -> Void)?
    let edit: (Computer) -> Void
    let newConnection: () -> Void

    @EnvironmentObject private var availabilityMonitor: AvailabilityMonitor
    @Environment(\.horizontalSizeClass) private var sizeClass
    @ObservedObject private var store = ComputerStore.shared
    @ObservedObject private var settings = AppSettings.shared
    @State private var computerToDelete: Computer?
    @State private var alert: AlertState?
    @State private var margin: CGFloat = 16

    private var orderedComputers: [Computer] {
        let computers = store.computers
        let positions = Dictionary(settings.computerOrder.enumerated().map { ($1, $0) }, uniquingKeysWith: min)
        return computers.enumerated()
            .sorted { (positions[$0.element.id] ?? .max, $0.offset) < (positions[$1.element.id] ?? .max, $1.offset) }
            .map(\.element)
    }

    var body: some View {
        content
            .background(NavigationBarMarginReader(margin: $margin))
            .navigationTitle("Connections")
            .primaryActionsCompat {
                Button(action: newConnection) { Label("New Connection", systemImage: "plus") }
            } trailing: {
                Menu {
                    Picker(selection: $settings.computerLayout) {
                        Label("Icons", systemImage: "square.grid.2x2").tag(ComputerLayout.grid)
                        Label("List", systemImage: "list.bullet").tag(ComputerLayout.list)
                    } label: {
                        Text("View As")
                    }
                    .pickerStyle(.inline)
                } label: {
                    Label("View As", systemImage: settings.computerLayout == .grid ? "square.grid.2x2" : "list.bullet")
                }
            }
            .confirmationDialog(
                "Delete “\(computerToDelete?.displayName ?? "")”?",
                isPresented: Binding(get: { computerToDelete != nil }, set: { if !$0 { computerToDelete = nil } }),
                titleVisibility: .visible
            ) {
                Button("Delete", role: .destructive) {
                    if let computer = computerToDelete { delete(computer) }
                    computerToDelete = nil
                }
            }
            .alert($alert)
            .onChangeCompat(of: store.computers.map(\.address), initial: true) { addresses in
                availabilityMonitor.update(addresses: Set(addresses))
            }
    }

    @ViewBuilder
    private var content: some View {
        let shown = orderedComputers
        if shown.isEmpty {
            ContentUnavailableViewCompat {
                Label("No Connections", systemImage: "desktopcomputer")
            } description: {
                Text("Tap \(Image(systemName: "plus")) to add a connection.")
            }
        } else if settings.computerLayout == .list {
            List {
                ForEach(shown) { row($0) }
                    .onMove { move(from: $0, to: $1) }
            }
            .listStyle(.insetGrouped)
        } else if #available(iOS 27, *) {
            cardGrid {
                ForEach(shown) { card($0) }
                    .reorderable()
            }
            .reorderContainer(for: Computer.self) { move($0) }
        } else {
            cardGrid {
                ForEach(shown) { card($0) }
            }
        }
    }

    private func cardGrid(@ViewBuilder cards: () -> some View) -> some View {
        ScrollView {
            LazyVGrid(columns: gridColumns, alignment: .leading, spacing: 24, content: cards)
                .padding(.horizontal, margin)
                .padding(.vertical, 12)
        }
    }

    private func move(from source: IndexSet, to destination: Int) {
        var shown = orderedComputers
        shown.move(fromOffsets: source, toOffset: destination)
        settings.computerOrder = shown.map(\.id)
    }

    @available(iOS 27, *)
    private func move(_ difference: ReorderDifference<UUID, ReorderableSingleCollectionIdentifier>) {
        var shown = orderedComputers
        let moved = shown.filter { difference.sources.contains($0.id) }
        shown.removeAll { difference.sources.contains($0.id) }
        let index = switch difference.destination.position {
        case .before(let id): shown.firstIndex { $0.id == id } ?? shown.endIndex
        case .end: shown.endIndex
        }
        shown.insert(contentsOf: moved, at: index)
        settings.computerOrder = shown.map(\.id)
    }

    private var gridColumns: [GridItem] {
        if sizeClass == .compact {
            return [GridItem(.flexible(), alignment: .top)]
        }
        return [GridItem(.adaptive(minimum: 340), spacing: 20, alignment: .top)]
    }

    private func card(_ computer: Computer) -> some View {
        Button {
            connect(SessionRoute(computer: computer))
        } label: {
            ComputerCard(
                name: computer.displayName, detail: computer.detailText, device: computer.device,
                thumbnail: computer.thumbnail, availability: availabilityMonitor.availability(of: computer.address))
        }
        .buttonStyle(.plain)
        .hoverEffectDisabledCompat()
        .contextMenu { menu(computer) }
    }

    private func row(_ computer: Computer) -> some View {
        Button {
            connect(SessionRoute(computer: computer))
        } label: {
            ComputerRow(
                name: computer.displayName, detail: computer.detailText, device: computer.device,
                availability: availabilityMonitor.availability(of: computer.address))
        }
        .foregroundStyle(.primary)
        .contextMenu { menu(computer) }
        .swipeActions(edge: .trailing) {
            Button(role: .destructive) {
                computerToDelete = computer
            } label: { Label("Delete", systemImage: "trash") }
        }
    }

    @ViewBuilder
    private func menu(_ computer: Computer) -> some View {
        if let openInNewWindow {
            Section {
                Button {
                    openInNewWindow(SessionRoute(computer: computer))
                } label: { Label("Open in New Window", systemImage: "macwindow.badge.plus") }
            }
        }
        Section {
            Button {
                edit(computer)
            } label: { Label("Edit", systemImage: "pencil") }
        }
        Section {
            Button(role: .destructive) {
                computerToDelete = computer
            } label: { Label("Delete", systemImage: "trash") }
        }
    }

    private func delete(_ computer: Computer) {
        let name = computer.displayName
        do {
            try ComputerStore.shared.delete(computer.id)
        } catch {
            alert = AlertState(String(localized: "Couldn’t Delete “\(name)”"), error: error)
        }
    }
}
