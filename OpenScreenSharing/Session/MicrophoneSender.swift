import AVFoundation
import Foundation

nonisolated final class MicrophoneSender: @unchecked Sendable {
    private static let sampleRate: Double = 48000
    private static let framesPerPacket: AVAudioFrameCount = 480

    private let engine = AVAudioEngine()
    private let queue = DispatchQueue(label: "io.nekohasekai.screensharing.microphone")
    private let send: @Sendable (Data) -> Void
    private let pcmFormat: AVAudioFormat
    private let encodedFormat: AVAudioFormat
    private let encoder: AVAudioConverter
    private var resampler: AVAudioConverter?
    private var pending: [Float] = []

    init(send: @escaping @Sendable (Data) -> Void) throws {
        self.send = send
        var description = AudioStreamBasicDescription(
            mSampleRate: Self.sampleRate, mFormatID: kAudioFormatMPEG4AAC_ELD, mFormatFlags: 0, mBytesPerPacket: 0,
            mFramesPerPacket: Self.framesPerPacket, mBytesPerFrame: 0, mChannelsPerFrame: 1, mBitsPerChannel: 0, mReserved: 0
        )
        guard let pcmFormat = AVAudioFormat(commonFormat: .pcmFormatFloat32, sampleRate: Self.sampleRate, channels: 1, interleaved: false),
            let encodedFormat = AVAudioFormat(streamDescription: &description)
        else { throw MessageError("AVAudioFormat rejected \(Int(Self.sampleRate)) Hz mono AAC-ELD") }
        guard let encoder = AVAudioConverter(from: pcmFormat, to: encodedFormat) else {
            throw MessageError("AVAudioConverter can't encode \(Int(Self.sampleRate)) Hz mono AAC-ELD")
        }
        self.pcmFormat = pcmFormat
        self.encodedFormat = encodedFormat
        self.encoder = encoder
    }

    func start() throws {
        #if os(iOS)
        let session = AVAudioSession.sharedInstance()
        try session.setCategory(.playAndRecord, mode: .voiceChat, options: [.defaultToSpeaker, .allowBluetoothHFP, .mixWithOthers])
        try session.setActive(true)
        #endif
        let input = engine.inputNode
        let inputFormat = input.outputFormat(forBus: 0)
        guard let resampler = AVAudioConverter(from: inputFormat, to: pcmFormat) else {
            throw MessageError("AVAudioConverter can't convert the microphone's \(inputFormat) to \(pcmFormat)")
        }
        self.resampler = resampler
        input.installTap(onBus: 0, bufferSize: 1024, format: inputFormat) { [weak self] buffer, _ in
            guard let self, let copy = buffer.copy() as? AVAudioPCMBuffer else { return }
            let boxed = BufferBox(buffer: copy)
            queue.async { self.process(boxed.buffer) }
        }
        engine.prepare()
        try engine.start()
    }

    func stop() {
        engine.inputNode.removeTap(onBus: 0)
        engine.stop()
        queue.async { [self] in pending.removeAll() }
    }

    private func process(_ buffer: AVAudioPCMBuffer) {
        guard let resampler else { return }
        let capacity = AVAudioFrameCount(Double(buffer.frameLength) * Self.sampleRate / buffer.format.sampleRate) + 32
        guard let resampled = AVAudioPCMBuffer(pcmFormat: pcmFormat, frameCapacity: capacity) else { return }
        var consumed = false
        var error: NSError?
        resampler.convert(to: resampled, error: &error) { _, status in
            if consumed {
                status.pointee = .noDataNow
                return nil
            }
            consumed = true
            status.pointee = .haveData
            return buffer
        }
        guard error == nil, let channel = resampled.floatChannelData?[0] else { return }
        pending.append(contentsOf: UnsafeBufferPointer(start: channel, count: Int(resampled.frameLength)))
        while pending.count >= Int(Self.framesPerPacket) {
            encodePacket(Array(pending.prefix(Int(Self.framesPerPacket))))
            pending.removeFirst(Int(Self.framesPerPacket))
        }
    }

    private func encodePacket(_ samples: [Float]) {
        guard let input = AVAudioPCMBuffer(pcmFormat: pcmFormat, frameCapacity: Self.framesPerPacket),
            let channel = input.floatChannelData?[0]
        else { return }
        samples.withUnsafeBufferPointer { channel.update(from: $0.baseAddress!, count: samples.count) }
        input.frameLength = Self.framesPerPacket
        let output = AVAudioCompressedBuffer(format: encodedFormat, packetCapacity: 1, maximumPacketSize: max(encoder.maximumOutputPacketSize, 1024))
        var consumed = false
        var error: NSError?
        let status = encoder.convert(to: output, error: &error) { _, inputStatus in
            if consumed {
                inputStatus.pointee = .noDataNow
                return nil
            }
            consumed = true
            inputStatus.pointee = .haveData
            return input
        }
        guard status != .error, output.byteLength > 0 else { return }
        send(Data(bytes: output.data, count: Int(output.byteLength)))
    }
}

nonisolated private final class BufferBox: @unchecked Sendable {
    let buffer: AVAudioPCMBuffer

    init(buffer: AVAudioPCMBuffer) {
        self.buffer = buffer
    }
}
