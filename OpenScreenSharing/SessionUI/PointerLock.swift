#if os(iOS)
import Combine
import GameController
import ObjectiveC
import SwiftUI
import UIKit

protocol PointerLockReceiver: AnyObject {
    func lockedMouseMoved(by delta: CGPoint)
    func lockedMouseButton(_ button: PointerButtons, pressed: Bool)
}

enum PointerLock {
    private static let requests = NSHashTable<UIWindowScene>.weakObjects()
    private static let receivers = NSHashTable<AnyObject>.weakObjects()
    private static var isInstalled = false

    static func request(_ locked: Bool, in scene: UIWindowScene) {
        install()
        guard locked != requests.contains(scene) else { return }
        if locked {
            requests.add(scene)
        } else {
            requests.remove(scene)
        }
        update(scene)
    }

    static func addReceiver(_ receiver: PointerLockReceiver) {
        install()
        receivers.add(receiver)
    }

    static func isLocked(_ scene: UIWindowScene?) -> Bool {
        scene?.pointerLockState?.isLocked == true
    }

    static var hasMouse: Bool {
        install()
        return mice.isConnected
    }

    static let mice = ConnectedMice()

    private static func prefersLock(_ scene: UIWindowScene) -> Bool {
        requests.contains(scene) && !GCMouse.mice().isEmpty
    }

    private static func update(_ scene: UIWindowScene) {
        for window in scene.windows {
            var controller = window.rootViewController
            while let current = controller {
                current.setNeedsUpdateOfPrefersPointerLocked()
                controller = current.presentedViewController
            }
        }
    }

    private static func install() {
        guard !isInstalled else { return }
        isInstalled = true
        let selector = #selector(getter: UIViewController.prefersPointerLocked)
        if let method = class_getInstanceMethod(UIViewController.self, selector) {
            typealias Getter = @convention(c) (UIViewController, Selector) -> Bool
            let original = unsafeBitCast(method_getImplementation(method), to: Getter.self)
            let getter: @convention(block) (UIViewController) -> Bool = { controller in
                if let scene = controller.viewIfLoaded?.window?.windowScene, prefersLock(scene) { return true }
                return original(controller, selector)
            }
            method_setImplementation(method, imp_implementationWithBlock(getter))
        }
        GCMouse.mice().forEach(attach)
        NotificationCenter.default.addObserver(forName: .GCMouseDidConnect, object: nil, queue: .main) { _ in
            MainActor.assumeIsolated {
                GCMouse.mice().forEach(attach)
                mice.isConnected = !GCMouse.mice().isEmpty
                requests.allObjects.forEach(update)
            }
        }
        NotificationCenter.default.addObserver(forName: .GCMouseDidDisconnect, object: nil, queue: .main) { _ in
            MainActor.assumeIsolated {
                mice.isConnected = !GCMouse.mice().isEmpty
                requests.allObjects.forEach(update)
            }
        }
    }

    private static func attach(_ mouse: GCMouse) {
        guard let input = mouse.mouseInput else { return }
        input.mouseMovedHandler = { _, deltaX, deltaY in
            MainActor.assumeIsolated {
                forward { $0.lockedMouseMoved(by: CGPoint(x: CGFloat(deltaX), y: -CGFloat(deltaY))) }
            }
        }
        let buttons: [(GCControllerButtonInput?, PointerButtons)] = [
            (input.leftButton, .primary), (input.rightButton, .secondary), (input.middleButton, .middle),
        ]
        for case (let input?, let button) in buttons {
            input.pressedChangedHandler = { _, _, pressed in
                MainActor.assumeIsolated {
                    forward { $0.lockedMouseButton(button, pressed: pressed) }
                }
            }
        }
    }

    private static func forward(_ event: (PointerLockReceiver) -> Void) {
        for case let receiver as PointerLockReceiver in receivers.allObjects {
            event(receiver)
        }
    }
}

final class ConnectedMice: ObservableObject {
    @Published fileprivate(set) var isConnected = !GCMouse.mice().isEmpty
}

extension View {
    func requestsPointerLock(_ locked: Bool) -> some View {
        background {
            PointerLockRequest(locked: locked)
        }
    }
}

private struct PointerLockRequest: UIViewRepresentable {
    let locked: Bool

    func makeUIView(context: Context) -> RequestView {
        RequestView()
    }

    func updateUIView(_ view: RequestView, context: Context) {
        view.locked = locked
    }

    final class RequestView: UIView {
        private weak var scene: UIWindowScene?

        var locked = false {
            didSet {
                if locked != oldValue { report() }
            }
        }

        override init(frame: CGRect) {
            super.init(frame: frame)
            isUserInteractionEnabled = false
        }

        required init?(coder: NSCoder) {
            fatalError()
        }

        override func didMoveToWindow() {
            super.didMoveToWindow()
            report()
        }

        private func report() {
            let current = window?.windowScene
            if let scene, scene !== current {
                PointerLock.request(false, in: scene)
            }
            scene = current
            if let current {
                PointerLock.request(locked, in: current)
            }
        }
    }
}
#endif
