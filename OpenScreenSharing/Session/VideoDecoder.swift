import CoreMedia
import CoreVideo
import Foundation
import QuartzCore
import VideoToolbox

nonisolated final class VideoDecoder: @unchecked Sendable {
    private let queue = DispatchQueue(label: "io.nekohasekai.screensharing.video-decoder")
    private let queueKey = DispatchSpecificKey<UInt8>()
    private var session: VTDecompressionSession?
    private var formatDescription: CMVideoFormatDescription?
    private var codec: VideoCodec?
    private var parameterSets: [Data] = []
    private var waitingForSync = true
    private var canvasHeight = 0
    private var bands = 1
    private var isHDR = false
    private var suspended = false
    private var generation: UInt64 = 0
    private var nextTicket: UInt64 = 0
    private var inFlight: [UInt64: Submission] = [:]
    private var pending: [PendingFrame] = []
    private var base: [CVPixelBuffer?] = []
    private var baselineStartedAt: CFTimeInterval?
    private var lastSubmittedFrameID: Int64?
    private var lastSubmissionEnded = true
    private var timeout: DispatchWorkItem?
    private var timeoutID: UInt64 = 0
    private let completions = LockedState(initialState: [Completion]())
    private let output: @Sendable (DecodedVideoFrame) -> Void
    private let failure: @Sendable () -> Void
    private let unsupported: @Sendable (_ hdr: Bool, _ error: any Error) -> Void

    private static let maxInFlight = 8
    private static let maxPendingFrames = 4
    private static let assemblyTimeout: CFTimeInterval = 0.5

    private struct Submission {
        var frameID: Int64
        var band: Int
        var received: CFTimeInterval
    }

    private struct PendingFrame {
        var id: Int64
        var startedAt: CFTimeInterval
        var arrival: CFTimeInterval
        var hold: TimeInterval
        var capture: CFTimeInterval
        var turns: PictureTurns
        var completedAt: CFTimeInterval?
        var submittedMask = 0
        var ended = false
        var remaining = 0
        var latestTickets: [UInt64?]
        var strips: [CVPixelBuffer?]
    }

    private struct Completion: @unchecked Sendable {
        var generation: UInt64
        var ticket: UInt64
        var status: OSStatus
        var pixelBuffer: CVPixelBuffer?
    }

    init(
        output: @escaping @Sendable (DecodedVideoFrame) -> Void,
        failure: @escaping @Sendable () -> Void,
        unsupported: @escaping @Sendable (_ hdr: Bool, _ error: any Error) -> Void
    ) {
        self.output = output
        self.failure = failure
        self.unsupported = unsupported
        queue.setSpecific(key: queueKey, value: 1)
    }

    deinit {
        timeout?.cancel()
        if let session {
            VTDecompressionSessionInvalidate(session)
        }
    }

    private func serialized(_ body: () -> Void) {
        if DispatchQueue.getSpecific(key: queueKey) != nil {
            body()
        } else {
            queue.sync(execute: body)
        }
    }

    func reset() {
        serialized {
            invalidateSession()
            formatDescription = nil
            parameterSets = []
            codec = nil
        }
    }

    func setFormat(_ format: VideoFormat) {
        serialized {
            guard format.codec != codec || format.parameterSets != parameterSets || format.isHDR != isHDR
                || format.bands != bands || format.canvasHeight != canvasHeight || formatDescription == nil
            else { return }
            invalidateSession()
            formatDescription = nil
            parameterSets = format.parameterSets
            codec = format.codec
            canvasHeight = format.canvasHeight
            bands = format.bands
            isHDR = format.isHDR
            do {
                guard bands == 1 || bands == 4, bands == 1 || canvasHeight > 0 else {
                    throw MessageError("the video stream has an invalid band layout")
                }
                formatDescription = try Self.makeFormatDescription(format)
            } catch {
                unsupported(isHDR, error)
            }
        }
    }

    func setSuspended(_ suspended: Bool) {
        serialized {
            guard suspended != self.suspended else { return }
            self.suspended = suspended
            if suspended {
                invalidateSession()
            }
        }
    }

    func decode(_ unit: VideoAccessUnit) {
        serialized { submit(unit) }
    }

    private func submit(_ unit: VideoAccessUnit) {
        guard !suspended else { return }
        if unit.discontinuity {
            invalidateSession()
        }
        if waitingForSync {
            guard unit.isSync else {
                if unit.discontinuity { failure() }
                return
            }
        }
        guard let formatDescription else { return }
        let fullMask = (1 << bands) - 1
        guard unit.bands == bands, unit.band >= 0, unit.band < bands,
            unit.frameID > 0, unit.changedBands > 0, unit.changedBands & ~fullMask == 0,
            unit.changedBands & (1 << unit.band) != 0
        else {
            recover()
            return
        }
        if session == nil {
            do {
                try createSession()
            } catch {
                unsupported(isHDR, error)
                return
            }
        }
        guard let session else { return }
        let epoch = generation
        if inFlight.count >= Self.maxInFlight {
            VTDecompressionSessionWaitForAsynchronousFrames(session)
            drainCompletions()
            guard generation == epoch else { return }
            guard inFlight.count < Self.maxInFlight else {
                recover()
                return
            }
        }
        guard let sample = Self.makeSampleBuffer(unit.data, time: unit.presentationTime, format: formatDescription) else {
            recover()
            return
        }
        let received = CACurrentMediaTime()
        if lastSubmittedFrameID != unit.frameID {
            if let previous = lastSubmittedFrameID {
                guard lastSubmissionEnded, previous < Int64.max, unit.frameID == previous + 1 else {
                    recover()
                    return
                }
            }
            guard pending.count < Self.maxPendingFrames else {
                recover()
                return
            }
            pending.append(PendingFrame(
                id: unit.frameID, startedAt: received, arrival: unit.arrival, hold: unit.libraryHold, capture: unit.presentationTime.seconds,
                turns: unit.turns,
                latestTickets: Array(repeating: nil, count: bands), strips: Array(repeating: nil, count: bands)))
            lastSubmittedFrameID = unit.frameID
            lastSubmissionEnded = false
        }
        guard !lastSubmissionEnded, let index = pending.indices.last, pending[index].id == unit.frameID else {
            recover()
            return
        }
        pending[index].submittedMask |= 1 << unit.band
        pending[index].arrival = max(pending[index].arrival, unit.arrival)
        pending[index].hold = max(pending[index].hold, unit.libraryHold)
        guard unit.changedBands == pending[index].submittedMask else {
            recover()
            return
        }
        nextTicket &+= 1
        let ticket = nextTicket
        pending[index].remaining += 1
        pending[index].latestTickets[unit.band] = ticket
        pending[index].ended = unit.frameEnd
        lastSubmissionEnded = unit.frameEnd
        inFlight[ticket] = Submission(frameID: unit.frameID, band: unit.band, received: received)
        if waitingForSync {
            waitingForSync = false
            base = Array(repeating: nil, count: bands)
            baselineStartedAt = received
        }
        scheduleTimeout()
        let status = VTDecompressionSessionDecodeFrame(session, sampleBuffer: sample, flags: [._EnableAsynchronousDecompression], infoFlagsOut: nil) {
            [weak self, completions, queue] status, _, imageBuffer, _, _ in
            let completion = Completion(generation: epoch, ticket: ticket, status: status, pixelBuffer: imageBuffer)
            completions.withLock { $0.append(completion) }
            queue.async { [weak self] in self?.drainCompletions() }
        }
        if status != noErr {
            recover()
        }
    }

    private func drainCompletions() {
        let ready = completions.withLock { values in
            let ready = values
            values.removeAll(keepingCapacity: true)
            return ready
        }
        for completion in ready {
            guard completion.generation == generation, let submission = inFlight.removeValue(forKey: completion.ticket) else { continue }
            guard completion.status == noErr, let pixelBuffer = completion.pixelBuffer,
                let index = pending.firstIndex(where: { $0.id == submission.frameID })
            else {
                recover()
                continue
            }
            pending[index].remaining -= 1
            pending[index].completedAt = CACurrentMediaTime()
            if pending[index].latestTickets[submission.band] == completion.ticket {
                pending[index].strips[submission.band] = pixelBuffer
            }
            guard completion.generation == generation else { continue }
            publishCompletedFrames()
        }
    }

    private func publishCompletedFrames() {
        while let first = pending.first, first.ended, first.remaining == 0 {
            let epoch = generation
            for band in 0..<bands where first.submittedMask & (1 << band) != 0 {
                guard let strip = first.strips[band] else {
                    recover()
                    return
                }
                base[band] = strip
            }
            pending.removeFirst()
            let strips = base.compactMap { $0 }
            if strips.count == bands {
                let width = CVPixelBufferGetWidth(strips[0])
                let height = CVPixelBufferGetHeight(strips[0])
                guard width > 0, height > 0,
                    strips.allSatisfy({ CVPixelBufferGetWidth($0) == width && CVPixelBufferGetHeight($0) == height })
                else {
                    recover()
                    return
                }
                baselineStartedAt = nil
                let frame = DecodedVideoFrame(
                    pixelBuffer: bands == 1 ? strips[0] : nil,
                    strips: bands == 1 ? [] : strips,
                    canvasHeight: canvasHeight,
                    turns: first.turns,
                    timing: FrameTiming(
                        arrival: first.arrival, libraryHold: first.hold,
                        decode: max((first.completedAt ?? CACurrentMediaTime()) - first.startedAt, 0), capture: first.capture))
                output(frame)
                guard generation == epoch else { return }
            }
        }
        scheduleTimeout()
    }

    private func scheduleTimeout() {
        timeout?.cancel()
        timeoutID &+= 1
        let id = timeoutID
        let starts = [pending.first?.startedAt, baselineStartedAt].compactMap { $0 }
        guard let startedAt = starts.min() else {
            timeout = nil
            return
        }
        let item = DispatchWorkItem { [weak self] in
            guard let self, timeoutID == id else { return }
            recover()
        }
        timeout = item
        queue.asyncAfter(deadline: .now() + max(0, startedAt + Self.assemblyTimeout - CACurrentMediaTime()), execute: item)
    }

    private func recover() {
        invalidateSession()
        failure()
    }

    private func invalidateSession() {
        generation &+= 1
        timeoutID &+= 1
        timeout?.cancel()
        timeout = nil
        pending = []
        base = []
        baselineStartedAt = nil
        lastSubmittedFrameID = nil
        lastSubmissionEnded = true
        inFlight = [:]
        completions.withLock { $0.removeAll(keepingCapacity: true) }
        waitingForSync = true
        if let session {
            VTDecompressionSessionInvalidate(session)
        }
        session = nil
    }

    private func createSession() throws {
        guard let formatDescription else { return }
        let attributes: [CFString: Any] = [
            kCVPixelBufferPixelFormatTypeKey: isHDR ? kCVPixelFormatType_64RGBAHalf : kCVPixelFormatType_32BGRA,
            kCVPixelBufferIOSurfacePropertiesKey: [CFString: Any](),
            kCVPixelBufferMetalCompatibilityKey: true,
        ]
        var created: VTDecompressionSession?
        let status = VTDecompressionSessionCreate(
            allocator: nil, formatDescription: formatDescription, decoderSpecification: nil,
            imageBufferAttributes: attributes as CFDictionary, outputCallback: nil, decompressionSessionOut: &created
        )
        guard status == noErr, let created else { throw MessageError("VTDecompressionSessionCreate", status: status) }
        VTSessionSetProperty(created, key: kVTDecompressionPropertyKey_RealTime, value: kCFBooleanTrue)
        session = created
    }

    private static func makeFormatDescription(_ format: VideoFormat) throws -> CMVideoFormatDescription {
        let buffers = format.parameterSets.map { [UInt8]($0) }
        guard !buffers.isEmpty else { throw MessageError("the \(format.codec.rawValue.uppercased()) stream came without parameter sets") }
        let copies = buffers.map { buffer -> UnsafeMutablePointer<UInt8> in
            let pointer = UnsafeMutablePointer<UInt8>.allocate(capacity: max(buffer.count, 1))
            pointer.initialize(from: buffer, count: buffer.count)
            return pointer
        }
        defer { copies.forEach { $0.deallocate() } }
        let pointers = copies.map { UnsafePointer($0) }
        let sizes = buffers.map(\.count)
        var description: CMVideoFormatDescription?
        let status: OSStatus
        let operation: String
        switch format.codec {
        case .h264:
            operation = "CMVideoFormatDescriptionCreateFromH264ParameterSets"
            status = CMVideoFormatDescriptionCreateFromH264ParameterSets(
                allocator: nil, parameterSetCount: pointers.count, parameterSetPointers: pointers,
                parameterSetSizes: sizes, nalUnitHeaderLength: 4, formatDescriptionOut: &description
            )
        case .hevc:
            operation = "CMVideoFormatDescriptionCreateFromHEVCParameterSets"
            status = CMVideoFormatDescriptionCreateFromHEVCParameterSets(
                allocator: nil, parameterSetCount: pointers.count, parameterSetPointers: pointers,
                parameterSetSizes: sizes, nalUnitHeaderLength: 4, extensions: nil, formatDescriptionOut: &description
            )
        }
        guard status == noErr, let description else { throw MessageError(operation, status: status) }
        return description
    }

    private static func makeSampleBuffer(_ payload: Data, time: CMTime, format: CMVideoFormatDescription) -> CMSampleBuffer? {
        guard !payload.isEmpty else { return nil }
        var blockBuffer: CMBlockBuffer?
        let count = payload.count
        guard
            CMBlockBufferCreateWithMemoryBlock(
                allocator: nil, memoryBlock: nil, blockLength: count, blockAllocator: nil, customBlockSource: nil,
                offsetToData: 0, dataLength: count, flags: 0, blockBufferOut: &blockBuffer
            ) == noErr, let blockBuffer
        else { return nil }
        let copied = payload.withUnsafeBytes { raw in
            CMBlockBufferReplaceDataBytes(with: raw.baseAddress!, blockBuffer: blockBuffer, offsetIntoDestination: 0, dataLength: count)
        }
        guard copied == noErr else { return nil }
        var timing = CMSampleTimingInfo(duration: .invalid, presentationTimeStamp: time, decodeTimeStamp: .invalid)
        var sampleSize = count
        var sampleBuffer: CMSampleBuffer?
        guard
            CMSampleBufferCreateReady(
                allocator: nil, dataBuffer: blockBuffer, formatDescription: format, sampleCount: 1,
                sampleTimingEntryCount: 1, sampleTimingArray: &timing, sampleSizeEntryCount: 1,
                sampleSizeArray: &sampleSize, sampleBufferOut: &sampleBuffer
            ) == noErr
        else { return nil }
        return sampleBuffer
    }
}

nonisolated extension VideoDecoder {
    static let decodesFullChromaInHardware: Bool = {
        let format = VideoFormat(
            codec: .hevc,
            parameterSets: [
                "40010c01ffff0408000003009e2800000300001eba0240",
                "4201010408000003009e2800000300001e90041020b2dd49265780b4040000030004000003000420",
                "4401c172860c0224",
            ].map(hexData),
            bands: 1
        )
        guard let description = try? makeFormatDescription(format),
            let sample = makeSampleBuffer(hexData("0000000c2801af78ebaefbfe7557cb4f"), time: .zero, format: description)
        else { return false }
        var created: VTDecompressionSession?
        guard
            VTDecompressionSessionCreate(
                allocator: nil, formatDescription: description, decoderSpecification: DecoderCapabilities.hardwareSpecification as CFDictionary,
                imageBufferAttributes: nil, outputCallback: nil, decompressionSessionOut: &created
            ) == noErr, let session = created
        else { return false }
        defer { VTDecompressionSessionInvalidate(session) }
        let decoded = LockedState(initialState: false)
        let status = VTDecompressionSessionDecodeFrame(session, sampleBuffer: sample, flags: [], infoFlagsOut: nil) { status, _, imageBuffer, _, _ in
            let succeeded = status == noErr && imageBuffer != nil
            decoded.withLock { $0 = succeeded }
        }
        VTDecompressionSessionWaitForAsynchronousFrames(session)
        return status == noErr && decoded.withLock { $0 }
    }()

    private static func hexData(_ hex: String) -> Data {
        var data = Data(capacity: hex.count / 2)
        var index = hex.startIndex
        while index < hex.endIndex {
            let next = hex.index(index, offsetBy: 2)
            data.append(UInt8(hex[index..<next], radix: 16)!)
            index = next
        }
        return data
    }
}
