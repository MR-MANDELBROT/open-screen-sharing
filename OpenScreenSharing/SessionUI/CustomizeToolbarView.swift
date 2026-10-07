import SwiftUI

struct CustomizeToolbarView: View {
    @ObservedObject var controller: SessionController
    @ObservedObject private var layout: ToolbarLayout
    let slots: Int?

    @Environment(\.dismiss) private var dismiss
    @State private var isConfirmingReset = false

    init(controller: SessionController, slots: Int?) {
        self.controller = controller
        layout = .layout(for: controller)
        self.slots = slots
    }

    var body: some View {
        let plan = layout.plan(for: controller, slots: slots)
        let categories = SessionCommandCategory.allCases.filter { !layout.otherCommands(in: $0).isEmpty }
        NavigationStackCompat {
            List {
                Section {
                    ToolbarPreview(plan: plan)
                        .frame(maxWidth: .infinity)
                        .listRowBackground(Color.clear)
                        .listRowInsets(EdgeInsets())
                }
                Section {
                    ForEach(layout.bar, id: \.shownRowID) { command in
                        row(command, inMore: plan.overflow.contains(command))
                            .modifier(ReorderActions(layout: layout, command: command))
                    }
                    .onMove { source, destination in layout.move(fromOffsets: source, toOffset: destination) }
                    .onDelete { offsets in layout.remove(atOffsets: offsets) }
                } header: {
                    Text("Shown in Toolbar")
                }
                ForEach(categories) { category in
                    Section {
                        ForEach(layout.otherCommands(in: category), id: \.otherRowID) { command in
                            Button {
                                layout.add(command)
                            } label: {
                                HStack(spacing: 12) {
                                    Image(systemName: "plus.circle.fill")
                                        .symbolRenderingMode(.palette)
                                        .foregroundStyle(.white, .green)
                                        .font(.title2)
                                        .accessibilityHidden(true)
                                    row(command, inMore: false)
                                }
                                .contentShape(Rectangle())
                            }
                            .buttonStyle(.plain)
                            .accessibilityLabel(Text("Add \(String(localized: command.title)) to Toolbar"))
                            .accessibilityValue(notAvailableValue(command))
                            .deleteDisabled(true)
                            .moveDisabled(true)
                        }
                    } header: {
                        Text(String(localized: category.title))
                    }
                }
                Section {
                    Button("Reset to Default") {
                        if layout.isDefault {
                            layout.reset()
                        } else {
                            isConfirmingReset = true
                        }
                    }
                    .disabled(!layout.isCustomized)
                    .confirmationDialog("Reset Toolbar?", isPresented: $isConfirmingReset, titleVisibility: .visible) {
                        Button("Reset Toolbar", role: .destructive) { layout.reset() }
                        Button("Cancel", role: .cancel) {}
                    }
                }
            }
            .environment(\.editMode, .constant(.active))
            .animation(.default, value: layout.bar)
            .navigationTitle("Customize Toolbar")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    CloseButton { dismiss() }
                }
            }
        }
        .phonePresentationDetents([.medium, .large])
    }

    private func row(_ command: SessionCommand, inMore: Bool) -> some View {
        let offered = command.isOffered(by: controller)
        return HStack {
            Label(String(localized: command.title), systemImage: command.systemImage)
            Spacer(minLength: 8)
            if inMore {
                Text("In More")
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
            }
        }
        .foregroundStyle(offered ? .primary : .tertiary)
        .accessibilityValue(notAvailableValue(command))
    }

    private func notAvailableValue(_ command: SessionCommand) -> Text {
        command.isOffered(by: controller) ? Text(verbatim: "") : Text("Not Available")
    }
}

private extension SessionCommand {
    var shownRowID: String { "shown." + rawValue }
    var otherRowID: String { "other." + rawValue }
}

private struct ReorderActions: ViewModifier {
    let layout: ToolbarLayout
    let command: SessionCommand

    func body(content: Content) -> some View {
        if #available(iOS 16, *) {
            content.accessibilityActions {
                if command != layout.bar.first {
                    Button("Move Up") { layout.move(command, by: -1) }
                }
                if command != layout.bar.last {
                    Button("Move Down") { layout.move(command, by: 1) }
                }
                Button("Remove from Toolbar") { layout.remove(command) }
            }
        } else {
            content
                .accessibilityAction(named: Text("Move Up")) { layout.move(command, by: -1) }
                .accessibilityAction(named: Text("Move Down")) { layout.move(command, by: 1) }
                .accessibilityAction(named: Text("Remove from Toolbar")) { layout.remove(command) }
        }
    }
}

private struct ToolbarPreview: View {
    let plan: ToolbarPlan

    var body: some View {
        fittingBar
            .padding(.vertical, 20)
            .padding(.horizontal, 12)
            .frame(maxWidth: .infinity)
            .background {
                if #available(iOS 16, *) {
                    RoundedRectangle(cornerRadius: 20).fill(.black.gradient)
                } else {
                    RoundedRectangle(cornerRadius: 20).fill(.black)
                }
            }
            .environment(\.colorScheme, .dark)
            .allowsHitTesting(false)
            .accessibilityElement(children: .ignore)
            .accessibilityLabel(Text("Toolbar Preview"))
            .accessibilityValue(Text(plan.inBar.map { String(localized: $0.title) }.formatted(.list(type: .and))))
    }

    @ViewBuilder
    private var fittingBar: some View {
        if #available(iOS 16, *) {
            ViewThatFits(in: .horizontal) {
                bar(itemSize: 40)
                bar(itemSize: 32)
                bar(itemSize: 24)
            }
        } else {
            bar(itemSize: 32)
        }
    }

    private func bar(itemSize: CGFloat) -> some View {
        GlassEffectContainerCompat(spacing: 0) {
            HStack(spacing: ToolbarLayout.capsuleGroupSpacing) {
                icon("xmark", size: itemSize)
                    .glassEffectCompat(in: Circle())
                if !plan.inBar.isEmpty {
                    HStack(spacing: 0) {
                        ForEach(plan.inBar) { command in
                            icon(command.systemImage, size: itemSize)
                        }
                    }
                    .padding(.horizontal, ToolbarLayout.capsuleGroupPadding)
                    .glassEffectCompat(in: Capsule())
                }
                icon("ellipsis", size: itemSize)
                    .glassEffectCompat(in: Circle())
            }
        }
        .fixedSize()
    }

    private func icon(_ systemImage: String, size: CGFloat) -> some View {
        Image(systemName: systemImage)
            .font(.system(size: size * 0.4, weight: .medium))
            .foregroundStyle(.white)
            .frame(width: size, height: size)
    }
}
