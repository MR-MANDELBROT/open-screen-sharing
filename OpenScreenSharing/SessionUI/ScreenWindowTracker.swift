#if os(iOS)
import UIKit

final class ScreenWindowTracker {
    private unowned let view: UIView
    private let report: (_ area: CGSize, _ scale: CGFloat, _ screen: CGSize) -> Void
    private weak var scene: UIWindowScene?
    private var geometryObservation: NSKeyValueObservation?
    private var settleTask: Task<Void, Never>?
    private var reportedArea: CGSize?
    private var reportedScale: CGFloat?
    private var wasResizing = false
    private var restrictsSize = false
    private var replacedMinimumSize: CGSize?
    private var turnsLandscape = true

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
        let landscape = updateOrientation(scene)
        let resizing = scene.isInteractivelyResizingCompat
        let resizeEnded = wasResizing && !resizing
        wasResizing = resizing
        settleTask?.cancel()
        settleTask = nil
        if !resizing, area == reportedArea, view.traitCollection.displayScale == reportedScale {
            return
        }
        if resizeEnded {
            reportArea()
            return
        }
        let turning = landscape && !(scene.interfaceOrientationCompat.isLandscape && area.width > area.height)
        let delay: TimeInterval = turning ? 2 : reportedArea == nil && !resizing ? 0.1 : 0.3
        settleTask = Task { [weak self] in
            try? await Task.sleep(seconds: delay)
            guard let self, !Task.isCancelled else { return }
            if resizing {
                self.geometryDidChange()
            } else {
                self.reportArea()
            }
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
        settleTask?.cancel()
        settleTask = nil
        reportedArea = nil
        reportedScale = nil
        wasResizing = false
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

    @discardableResult
    private func updateOrientation(_ scene: UIWindowScene) -> Bool {
        guard let window = view.window else { return false }
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
        SessionOrientation.update(scene, landscape: landscape)
        return landscape
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

    static func update(_ scene: UIWindowScene, landscape: Bool) {
        guard landscapeScenes.contains(scene) != landscape else { return }
        if landscape {
            landscapeScenes.add(scene)
        } else {
            landscapeScenes.remove(scene)
        }
        guard #available(iOS 16, *) else {
            UIViewController.attemptRotationToDeviceOrientation()
            return
        }
        for window in scene.windows {
            var controller = window.rootViewController
            while let current = controller {
                current.setNeedsUpdateOfSupportedInterfaceOrientations()
                controller = current.presentedViewController
            }
        }
        if landscape {
            scene.requestGeometryUpdate(.iOS(interfaceOrientations: .landscape))
        }
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
