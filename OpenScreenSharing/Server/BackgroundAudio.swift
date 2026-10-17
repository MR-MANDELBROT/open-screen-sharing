import AVFoundation
import UIKit
import os

final class BackgroundAudio {
    private(set) var isPlaying = false {
        didSet {
            guard isPlaying != oldValue else { return }
            onPlayingChange?()
        }
    }

    var onPlayingChange: (() -> Void)?
    var onFailure: ((any Error) -> Void)?

    nonisolated private static let logger = Logger(subsystem: Bundle.main.bundleIdentifier ?? "OpenScreenSharing", category: "server")

    private var engine: AVAudioEngine?
    private var engineObserver: (any NSObjectProtocol)?
    private var observers: [any NSObjectProtocol] = []

    func start() {
        guard observers.isEmpty else { return }
        watchSession()
        play()
    }

    func stop() {
        guard !observers.isEmpty else { return }
        for observer in observers {
            NotificationCenter.default.removeObserver(observer)
        }
        observers.removeAll()
        discardEngine()
        isPlaying = false
    }

    private func play() {
        guard !observers.isEmpty else { return }
        if engine?.isRunning == true {
            isPlaying = true
            return
        }
        discardEngine()
        do {
            let session = AVAudioSession.sharedInstance()
            if session.category != .playback && session.category != .playAndRecord {
                try session.setCategory(.playback, mode: .default, options: [.mixWithOthers])
            }
            try session.setActive(true)
            let engine = try Self.makeEngine()
            self.engine = engine
            let center = NotificationCenter.default
            engineObserver = center.addObserver(forName: .AVAudioEngineConfigurationChange, object: engine, queue: .main) { [weak self] _ in
                MainActor.assumeIsolated { self?.play() }
            }
            isPlaying = true
        } catch {
            Self.logger.error("Couldn't play the background sound: \(error, privacy: .public)")
            isPlaying = false
            onFailure?(error)
        }
    }

    private static func makeEngine() throws -> AVAudioEngine {
        let engine = AVAudioEngine()
        let player = AVAudioPlayerNode()
        guard let format = AVAudioFormat(standardFormatWithSampleRate: 44100, channels: 1),
            let silence = AVAudioPCMBuffer(pcmFormat: format, frameCapacity: 44100),
            let samples = silence.floatChannelData
        else { throw MessageError("AVAudioPCMBuffer couldn't hold a second of silence") }
        silence.frameLength = silence.frameCapacity
        samples[0].update(repeating: 0, count: Int(silence.frameLength))
        engine.attach(player)
        engine.connect(player, to: engine.mainMixerNode, format: format)
        try engine.start()
        player.scheduleBuffer(silence, at: nil, options: .loops)
        player.play()
        return engine
    }

    private func discardEngine() {
        if let engineObserver {
            NotificationCenter.default.removeObserver(engineObserver)
        }
        engineObserver = nil
        engine?.stop()
        engine = nil
    }

    private func watchSession() {
        let center = NotificationCenter.default
        let session = AVAudioSession.sharedInstance()
        observers.append(
            center.addObserver(forName: AVAudioSession.interruptionNotification, object: session, queue: .main) { [weak self] notification in
                let began = (notification.userInfo?[AVAudioSessionInterruptionTypeKey] as? UInt)
                    .flatMap(AVAudioSession.InterruptionType.init) == .began
                MainActor.assumeIsolated {
                    guard let self else { return }
                    if began {
                        self.isPlaying = false
                    } else {
                        self.play()
                    }
                }
            })
        observers.append(
            center.addObserver(forName: AVAudioSession.mediaServicesWereResetNotification, object: session, queue: .main) { [weak self] _ in
                MainActor.assumeIsolated {
                    guard let self else { return }
                    self.discardEngine()
                    self.play()
                }
            })
        observers.append(
            center.addObserver(forName: UIApplication.didBecomeActiveNotification, object: nil, queue: .main) { [weak self] _ in
                MainActor.assumeIsolated { self?.play() }
            })
    }
}
