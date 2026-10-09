import SwiftUI

struct ComputersView: View {
    let connect: (SessionRoute) -> Void
    let openInNewWindow: ((SessionRoute) -> Void)?
    let edit: (Computer) -> Void
    let newConnection: () -> Void

    @EnvironmentObject private var availabilityMonitor: AvailabilityMonitor
    @ObservedObject private var store = ComputerStore.shared
    @ObservedObject private var settings = AppSettings.shared
    @State private var computerToDelete: Computer?
    @State private var alert: AlertState?
    #if os(iOS)
    @Environment(\.horizontalSizeClass) private var sizeClass
    @State private var margin: CGFloat = 16
    #else
    @State private var selection: Set<Computer.ID> = []
    @State private var gridWidth: CGFloat = 0
    @FocusState private var focusedLayout: ComputerLayout?
    private let margin: CGFloat = 20
    private static let cardWidth: CGFloat = 220
    private static let cardSpacing: CGFloat = 20
    #endif

    private var orderedComputers: [Computer] {
        let computers = store.computers
        let positions = Dictionary(settings.computerOrder.enumerated().map { ($1, $0) }, uniquingKeysWith: min)
        return computers.enumerated()
            .sorted { (positions[$0.element.id] ?? .max, $0.offset) < (positions[$1.element.id] ?? .max, $1.offset) }
            .map(\.element)
    }

    var body: some View {
        content
            #if os(iOS)
            .background(NavigationBarMarginReader(margin: $margin))
            #endif
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
                #if os(iOS)
                Text("Tap \(Image(systemName: "plus")) to add a connection.")
                #else
                Text("Click \(Image(systemName: "plus")) to add a connection.")
                #endif
            }
        } else if settings.computerLayout == .list {
            #if os(iOS)
            List {
                ForEach(shown) { row($0) }
                    .onMove { move(from: $0, to: $1) }
            }
            .listStyle(.insetGrouped)
            #else
            List(selection: $selection) {
                ForEach(shown) { row($0) }
                    .onMove { move(from: $0, to: $1) }
            }
            .listStyle(.inset)
            .focused($focusedLayout, equals: .list)
            .onAppear { focusedLayout = .list }
            .contextMenu(forSelectionType: Computer.ID.self) { ids in
                if let computer = selectedComputer(in: ids) {
                    menu(computer)
                }
            } primaryAction: { ids in
                ids.compactMap { id in store.computers.first { $0.id == id } }.forEach { connect(SessionRoute(computer: $0)) }
            }
            .onDeleteCommand(perform: deleteSelection)
            #endif
        } else if #available(iOS 27, macOS 27, *) {
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
        #if os(iOS)
        ScrollView {
            LazyVGrid(columns: gridColumns, alignment: .leading, spacing: 24, content: cards)
                .padding(.horizontal, margin)
                .padding(.vertical, 12)
        }
        #else
        let cards = cards()
        return ScrollViewReader { proxy in
            ScrollView {
                LazyVGrid(columns: gridColumns, alignment: .leading, spacing: 24) { cards }
                    .padding(.horizontal, margin)
                    .padding(.vertical, 12)
            }
            .onChangeCompat(of: selection) { selection in
                if let id = selection.first {
                    proxy.scrollTo(id)
                }
            }
        }
        .background {
            Color.clear
                .contentShape(Rectangle())
                .onTapGesture {
                    selection = []
                    focusedLayout = .grid
                }
        }
        .onGeometryChangeCompat(for: CGFloat.self, of: { $0.size.width }) { gridWidth = $0 }
        .focusable(interactions: .edit)
        .focused($focusedLayout, equals: .grid)
        .onAppear { focusedLayout = .grid }
        .focusEffectDisabled()
        .onKeyPress(.return) {
            guard let computer = selectedComputer(in: selection) else { return .ignored }
            connect(SessionRoute(computer: computer))
            return .handled
        }
        .onMoveCommand(perform: moveSelection)
        .onDeleteCommand(perform: deleteSelection)
        #endif
    }

    private func move(from source: IndexSet, to destination: Int) {
        var shown = orderedComputers
        shown.move(fromOffsets: source, toOffset: destination)
        settings.computerOrder = shown.map(\.id)
    }

    @available(iOS 27, macOS 27, *)
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
        #if os(iOS)
        if sizeClass == .compact {
            return [GridItem(.flexible(), alignment: .top)]
        }
        return [GridItem(.adaptive(minimum: 340), spacing: 20, alignment: .top)]
        #else
        return [GridItem(.adaptive(minimum: Self.cardWidth), spacing: Self.cardSpacing, alignment: .top)]
        #endif
    }

    #if os(iOS)
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
    #else
    private func card(_ computer: Computer) -> some View {
        ComputerCard(
            name: computer.displayName, detail: computer.detailText, device: computer.device,
            thumbnail: computer.thumbnail, availability: availabilityMonitor.availability(of: computer.address),
            isSelected: selection.contains(computer.id))
        .onTapGesture(count: 2) { connect(SessionRoute(computer: computer)) }
        .simultaneousGesture(
            TapGesture().onEnded {
                selection = [computer.id]
                focusedLayout = .grid
            })
        .contextMenu { menu(computer) }
        .accessibilityAction { connect(SessionRoute(computer: computer)) }
    }

    private func row(_ computer: Computer) -> some View {
        ComputerRow(
            name: computer.displayName, detail: computer.detailText, device: computer.device,
            availability: availabilityMonitor.availability(of: computer.address))
    }

    private func selectedComputer(in ids: Set<Computer.ID>) -> Computer? {
        guard ids.count == 1 else { return nil }
        return store.computers.first { ids.contains($0.id) }
    }

    private func deleteSelection() {
        computerToDelete = selectedComputer(in: selection)
    }

    private func moveSelection(_ direction: MoveCommandDirection) {
        let shown = orderedComputers
        guard let first = shown.first else { return }
        guard let current = shown.firstIndex(where: { selection.contains($0.id) }) else {
            selection = [first.id]
            return
        }
        let columns = max(1, Int((gridWidth - margin * 2 + Self.cardSpacing) / (Self.cardWidth + Self.cardSpacing)))
        let target: Int
        switch direction {
        case .left: target = current - 1
        case .right: target = current + 1
        case .up: target = current - columns
        case .down: target = current + columns
        @unknown default: return
        }
        guard shown.indices.contains(target) else { return }
        selection = [shown[target].id]
    }
    #endif

    @ViewBuilder
    private func menu(_ computer: Computer) -> some View {
        #if os(macOS)
        Section {
            Button("Connect") {
                connect(SessionRoute(computer: computer))
            }
        }
        #endif
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
            } label: {
                #if os(iOS)
                Label("Edit", systemImage: "pencil")
                #else
                Label("Edit…", systemImage: "pencil")
                #endif
            }
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
