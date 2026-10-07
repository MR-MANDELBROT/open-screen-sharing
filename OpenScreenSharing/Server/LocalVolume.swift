import MediaPlayer
import UIKit
import os

final class LocalVolume {
    nonisolated private static let logger = Logger(subsystem: Bundle.main.bundleIdentifier ?? "OpenScreenSharing", category: "server")
    private static let savedKey = "serverSavedVolume"

    private let store: PreferenceStore
    private let volumeView = MPVolumeView(frame: CGRect(x: -100, y: -100, width: 1, height: 1))
    private var isReady = false
    private var watched = false

    init(store: PreferenceStore = .shared) {
        self.store = store
        DispatchQueue.main.asyncAfter(deadline: .now() + 0.5) { [weak self] in
            guard let self else { return }
            isReady = true
            update()
        }
    }

    private var saved: Float? {
        get { store.value(Float.self, for: Self.savedKey) }
        set {
            if let newValue {
                store.set(newValue, for: Self.savedKey)
            } else {
                store.remove(Self.savedKey)
            }
        }
    }

    func setWatched(_ watched: Bool) {
        self.watched = watched
        update()
    }

    func restore() {
        setWatched(false)
    }

    private func update() {
        guard isReady, let slider else { return }
        let volume = slider.value
        Self.logger.info("Watched \(self.watched, privacy: .public); volume \(volume, privacy: .public), saved \(String(describing: self.saved), privacy: .public)")
        if watched {
            guard saved == nil, volume > 0 else { return }
            saved = volume
            slider.value = 0
        } else {
            guard let saved else { return }
            self.saved = nil
            guard volume == 0 else { return }
            slider.value = saved
        }
    }

    private var slider: UISlider? {
        guard let slider = volumeView.subviews.lazy.compactMap({ $0 as? UISlider }).first else {
            Self.logger.error("MPVolumeView has no slider to set the volume with")
            return nil
        }
        return slider
    }
}
