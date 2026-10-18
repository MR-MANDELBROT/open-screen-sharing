#if os(iOS)
import UIKit

final class ScreenWindowTracker {
    private unowned let view: UIView
    private let report: (_ area: CGSize, _ scale: CGFloat, _ screen: CGSize) -> Void
    private weak var scene: UIWindowScene?
    private var geometryObservation: NSKeyValueObservation?
    private var reportTask: Task<Void, Never>?
    private var reportedArea: CGSize?
    private var reportedScale: CGFloat?
    private var restrictsSize = false
    private var replacedMinimumSize: CGSize?
    private var turnsLandscape = true
    private var awaitsLandscape = false

    init(view: UIView, report: @escaping (_ area: CGSize, _ scale: CGFloat, _ screen: CGSize) -> Void) {
        self.view = view
        self.report = report
    }

    var screenArea: CGRect? {
        guard let window = view.window else { return nil }
        var area = window.bounds
        if view.traitCollection.userInterfaceIdiom == .phone {
            let safeArea = window.safeAreaInsets
            var insets = UIEdgeInsets.zero
            switch window.windowScene?.interfaceOrientationCompat {
            case .landscapeLeft, .landscapeRight:
                insets.left = safeArea.left
                insets.right = safeArea.right
            case .portraitUpsideDown: insets.bottom = safeArea.bottom
            default: insets.top = safeArea.top
            }
            area = area.inset(by: insets)
        }
        let visible = view.convert(area, from: window).intersection(view.bounds)
        return visible.isNull || visible.isEmpty ? nil : visible
    }

    func windowDidChange() {
        let newScene = view.window?.windowScene
        if newScene !== scene {
            detach()
            scene = newScene
            if let newScene {
                if #available(iOS 16, *) {
                    geometryObservation = newScene.observe(\.effectiveGeometry) { [weak self] _, _ in
                        MainActor.assumeIsolated { self?.geometryDidChange() }
                    }
                }
                applySizeRestriction()
            }
        }
        geometryDidChange()
    }

    func geometryDidChange() {
        guard let scene, let area = screenArea?.size, area.width > 0, area.height > 0 else { return }
        updateOrientation(scene)
        guard !awaitsLandscape, !scene.isInteractivelyResizingCompat else {
            reportTask?.cancel()
            reportTask = nil
            return
        }
        guard reportTask == nil else { return }
        reportTask = Task { [weak self] in
            guard let self, !Task.isCancelled else { return }
            self.reportTask = nil
            self.reportArea()
        }
    }

    func setTurnsLandscape(_ turns: Bool) {
        guard turns != turnsLandscape else { return }
        turnsLandscape = turns
        geometryDidChange()
    }

    func setMatchesWindow(_ matches: Bool) {
        guard matches != restrictsSize else { return }
        restrictsSize = matches
        applySizeRestriction()
    }

    func detach() {
        geometryObservation = nil
        reportTask?.cancel()
        reportTask = nil
        reportedArea = nil
        reportedScale = nil
        awaitsLandscape = false
        guard let scene else { return }
        if let replacedMinimumSize {
            scene.sizeRestrictions?.minimumSize = replacedMinimumSize
            self.replacedMinimumSize = nil
        }
        SessionOrientation.update(scene, landscape: false)
        self.scene = nil
    }

    private func reportArea() {
        guard let scene, !scene.isInteractivelyResizingCompat, let area = screenArea?.size else { return }
        let scale = view.traitCollection.displayScale
        guard area != reportedArea || scale != reportedScale else { return }
        reportedArea = area
        reportedScale = scale
        report(area, scale, scene.screen.bounds.size)
    }

    private func applySizeRestriction() {
        guard let restrictions = scene?.sizeRestrictions else { return }
        if restrictsSize {
            if replacedMinimumSize == nil {
                replacedMinimumSize = restrictions.minimumSize
            }
            restrictions.minimumSize = VirtualDisplayMode.minimumWindowPoints
        } else if let replacedMinimumSize {
            restrictions.minimumSize = replacedMinimumSize
            self.replacedMinimumSize = nil
        }
    }

    private func updateOrientation(_ scene: UIWindowScene) {
        guard let window = view.window else { return }
        let landscape: Bool
        if !turnsLandscape {
            landscape = false
        } else if view.traitCollection.userInterfaceIdiom == .phone {
            landscape = true
        } else {
            let screen = scene.screen.bounds.size
            let size = window.bounds.size
            landscape =
                (abs(size.width - screen.width) < 1 && abs(size.height - screen.height) < 1)
                || (abs(size.width - screen.height) < 1 && abs(size.height - screen.width) < 1)
        }
        let requested = SessionOrientation.update(scene, landscape: landscape) { [weak self] in
            guard let self, self.awaitsLandscape else { return }
            self.awaitsLandscape = false
            self.geometryDidChange()
        }
        awaitsLandscape = landscape && (requested || awaitsLandscape) && !scene.interfaceOrientationCompat.isLandscape
    }
}

enum SessionOrientation {
    private static let landscapeScenes = NSHashTable<UIWindowScene>.weakObjects()

    static func supportedOrientations(for scene: UIWindowScene?) -> UIInterfaceOrientationMask {
        if let scene, landscapeScenes.contains(scene) {
            return .landscape
        }
        return UIDevice.current.userInterfaceIdiom == .pad ? .all : .allButUpsideDown
    }

    @discardableResult
    static func update(_ scene: UIWindowScene, landscape: Bool, refused: @escaping @MainActor @Sendable () -> Void = {}) -> Bool {
        guard landscapeScenes.contains(scene) != landscape else { return false }
        if landscape {
            landscapeScenes.add(scene)
        } else {
            landscapeScenes.remove(scene)
        }
        guard #available(iOS 16, *) else {
            UIViewController.attemptRotationToDeviceOrientation()
            return false
        }
        for window in scene.windows {
            var controller = window.rootViewController
            while let current = controller {
                current.setNeedsUpdateOfSupportedInterfaceOrientations()
                controller = current.presentedViewController
            }
        }
        guard landscape else { return false }
        scene.requestGeometryUpdate(.iOS(interfaceOrientations: .landscape)) { _ in
            Task { @MainActor in refused() }
        }
        return true
    }
}

extension UIWindowScene {
    fileprivate var interfaceOrientationCompat: UIInterfaceOrientation {
        if #available(iOS 16, *) {
            effectiveGeometry.interfaceOrientation
        } else {
            interfaceOrientation
        }
    }

    fileprivate var isInteractivelyResizingCompat: Bool {
        if #available(iOS 26, *) {
            effectiveGeometry.isInteractivelyResizing
        } else {
            false
        }
    }
}

final class AppDelegate: NSObject, UIApplicationDelegate {
    func application(_ application: UIApplication, supportedInterfaceOrientationsFor window: UIWindow?) -> UIInterfaceOrientationMask {
        SessionOrientation.supportedOrientations(for: window?.windowScene)
    }
}
#endif
