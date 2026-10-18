import MediaPlayer
import UIKit
import os

final class LocalVolume {
    nonisolated private static let logger = Logger(subsystem: Bundle.main.bundleIdentifier ?? "OpenScreenSharing", category: "server")
    private static let savedKey = "serverSavedVolume"

    private let store = PreferenceStore.shared
    private let systemVolume = SystemVolume()
    private lazy var volumeView = MPVolumeView(frame: CGRect(x: -100, y: -100, width: 1, height: 1))
    nonisolated(unsafe) private var fallbackObserver: (any NSObjectProtocol)?
    private var watched = false
    private var muteAttempt: MuteAttempt?

    private enum MuteAttempt {
        case initial
        case retry(Float)
    }

    init() {
        if systemVolume == nil {
            if let name = Self.fallbackAvailabilityNotification {
                fallbackObserver = NotificationCenter.default.addObserver(forName: name, object: nil, queue: .main) { [weak self] notification in
                    guard let changedSlider = notification.object as? UISlider else { return }
                    // 24A446 MediaPlayer 0x1a6e576b0 publishes availability before volume
                    // in one main callback; 0x1a71a3ca4 posts this notification. This ordering
                    // was also observed on 24B5110a.
                    DispatchQueue.main.async { [weak self, weak changedSlider] in
                        guard let self, let changedSlider, changedSlider === self.slider else { return }
                        self.update()
                    }
                }
            } else {
                Self.logger.error("MediaPlayer volume availability notification is unavailable")
            }
            _ = volumeView
        }
        // An already-ready volume controller sends no further availability event.
        DispatchQueue.main.async { [weak self] in
            self?.update()
        }
    }

    deinit {
        if let fallbackObserver { NotificationCenter.default.removeObserver(fallbackObserver) }
    }

    func setWatched(_ watched: Bool) {
        if watched != self.watched {
            self.watched = watched
            muteAttempt = watched ? .initial : nil
        }
        update()
    }

    private func update() {
        guard let volume = currentVolume else { return }
        let saved = store.value(Float.self, for: Self.savedKey)
        Self.logger.info("Watched \(self.watched, privacy: .public); volume \(volume, privacy: .public), saved \(String(describing: saved), privacy: .public)")
        if watched {
            guard let muteAttempt else { return }
            switch muteAttempt {
            case .initial:
                guard saved == nil, volume > 0 else {
                    self.muteAttempt = nil
                    return
                }
                guard store.set(volume, for: Self.savedKey) else { return }
            case let .retry(previous):
                guard volume == previous else {
                    self.muteAttempt = nil
                    return
                }
            }
            self.muteAttempt = setVolume(0) ? nil : .retry(volume)
        } else {
            guard let saved else { return }
            guard volume == 0 else {
                store.remove(Self.savedKey)
                return
            }
            if setVolume(saved) {
                store.remove(Self.savedKey)
            }
        }
    }

    private var currentVolume: Float? {
        if let systemVolume {
            guard let volume = systemVolume.value else {
                Self.logger.error("Couldn't read the media volume")
                return nil
            }
            return volume
        }
        // The slider's value writes through synchronously while its controller update is queued.
        guard fallbackVolume != nil, let value = slider?.value,
              value.isFinite, (0...1).contains(value)
        else { return nil }
        return value
    }

    private func setVolume(_ volume: Float) -> Bool {
        if let systemVolume {
            guard systemVolume.set(volume) else {
                Self.logger.error("Couldn't set the media volume to \(volume, privacy: .public)")
                return false
            }
            return true
        }
        guard fallbackVolume != nil, let slider else { return false }
        slider.value = volume
        return true
    }

    private var slider: UISlider? {
        guard let slider = volumeView.subviews.lazy.compactMap({ $0 as? UISlider }).first else {
            Self.logger.error("MPVolumeView has no slider to set the volume with")
            return nil
        }
        return slider
    }

    private static let fallbackAvailabilityNotification: Notification.Name? = {
        guard let handle = dlopen("/System/Library/Frameworks/MediaPlayer.framework/MediaPlayer", RTLD_NOW),
              let symbol = dlsym(handle, "MPVolumeSliderVolumeControlAvailabilityDidChangeNotification"),
              let name = symbol.assumingMemoryBound(to: Optional<NSString>.self).pointee
        else { return nil }
        return Notification.Name(name as String)
    }()

    // 24A446 MediaPlayer 0x1a6f4a82c reads the initialized route-capability flag.
    // UISlider.isEnabled alone is insufficient: it defaults to true before initialization.
    private var fallbackVolume: Float? {
        let controllerSelector = NSSelectorFromString("volumeController")
        let availableSelector = NSSelectorFromString("isVolumeControlAvailable")
        let valueSelector = NSSelectorFromString("volumeValue")
        guard let slider, slider.responds(to: controllerSelector),
              let controller = slider.perform(controllerSelector)?.takeUnretainedValue() as? NSObject,
              controller.responds(to: availableSelector), controller.responds(to: valueSelector)
        else { return nil }
        typealias IsAvailable = @convention(c) (AnyObject, Selector) -> Bool
        typealias GetValue = @convention(c) (AnyObject, Selector) -> Float
        let isAvailable = unsafeBitCast(controller.method(for: availableSelector), to: IsAvailable.self)
        guard isAvailable(controller, availableSelector) else { return nil }
        let getValue = unsafeBitCast(controller.method(for: valueSelector), to: GetValue.self)
        let value = getValue(controller, valueSelector)
        return value.isFinite && (0...1).contains(value) ? value : nil
    }

    // MPVolumeControllerSystemDataSource uses these methods for media volume.
    // 24A446 MediaExperience: 0x1ae9527a0 forwards to mode:nil / operation 12;
    // 0x1ae84cfbc sends synchronous XPC and 0x1ae84ccb0 waits for the daemon queue.
    // BOOL reports daemon OSStatus == 0, not notification delivery or HUD completion.
    private struct SystemVolume {
        private typealias GetVolume = @convention(c) (AnyObject, Selector, UnsafeMutablePointer<Float>, NSString) -> Bool
        private typealias SetVolume = @convention(c) (AnyObject, Selector, Float, NSString) -> Bool
        private let controller: NSObject
        private let getVolume: GetVolume
        private let setVolume: SetVolume
        private let getSelector = NSSelectorFromString("getVolume:forCategory:")
        private let setSelector = NSSelectorFromString("setVolumeTo:forCategory:")
        private let category = "Audio/Video" as NSString

        init?() {
            if NSClassFromString("AVSystemController") == nil {
                _ = dlopen("/System/Library/PrivateFrameworks/MediaExperience.framework/MediaExperience", RTLD_NOW)
            }
            let sharedSelector = NSSelectorFromString("sharedAVSystemController")
            guard let type = NSClassFromString("AVSystemController"),
                  let method = class_getClassMethod(type, sharedSelector)
            else { return nil }
            typealias SharedController = @convention(c) (AnyClass, Selector) -> Unmanaged<AnyObject>
            let shared = unsafeBitCast(method_getImplementation(method), to: SharedController.self)
            guard let controller = shared(type, sharedSelector).takeUnretainedValue() as? NSObject,
                  controller.responds(to: getSelector), controller.responds(to: setSelector)
            else { return nil }
            self.controller = controller
            getVolume = unsafeBitCast(controller.method(for: getSelector), to: GetVolume.self)
            setVolume = unsafeBitCast(controller.method(for: setSelector), to: SetVolume.self)
        }

        var value: Float? {
            var value: Float = 0
            guard getVolume(controller, getSelector, &value, category), value.isFinite, (0...1).contains(value) else { return nil }
            return value
        }

        func set(_ volume: Float) -> Bool {
            guard volume.isFinite, (0...1).contains(volume) else { return false }
            return setVolume(controller, setSelector, volume, category)
        }
    }
}
