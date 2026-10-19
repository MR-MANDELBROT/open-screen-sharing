import CoreGraphics
import CoreVideo
import Metal
import QuartzCore

final class ScreenRenderer {
    private static let shaderSource = """
        #include <metal_stdlib>
        using namespace metal;

        struct QuadVertex {
            float4 position [[position]];
            float2 textureCoordinate;
        };

        vertex QuadVertex quadVertex(
            uint vertexID [[vertex_id]], constant float4 &rect [[buffer(0)]], constant float4 &textureRect [[buffer(1)]],
            constant uint &turns [[buffer(2)]]
        ) {
            float2 corner = float2(vertexID & 1, vertexID >> 1);
            float2 source = corner;
            for (uint turn = 0; turn < turns; turn++) {
                source = float2(source.y, 1 - source.x);
            }
            QuadVertex out;
            out.position = float4(mix(rect.xy, rect.zw, corner), 0, 1);
            out.textureCoordinate = mix(textureRect.xy, textureRect.zw, source);
            return out;
        }

        fragment float4 screenFragment(QuadVertex in [[stage_in]], texture2d<float> texture [[texture(0)]], sampler textureSampler [[sampler(0)]]) {
            return float4(texture.sample(textureSampler, in.textureCoordinate).rgb, 1);
        }

        fragment float4 cursorFragment(QuadVertex in [[stage_in]], texture2d<float> texture [[texture(0)]], sampler textureSampler [[sampler(0)]]) {
            return texture.sample(textureSampler, in.textureCoordinate);
        }

        fragment float4 cursorFragmentPQ(QuadVertex in [[stage_in]], texture2d<float> texture [[texture(0)]], sampler textureSampler [[sampler(0)]]) {
            float4 color = texture.sample(textureSampler, in.textureCoordinate);
            if (color.a <= 0) {
                return float4(0);
            }
            float3 encoded = color.rgb / color.a;
            float3 linear = select(pow((encoded + 0.055) / 1.055, 2.4), encoded / 12.92, encoded <= 0.04045);
            float3x3 sRGBToDisplayP3 = float3x3(
                float3(0.82246197, 0.03319420, 0.01708263), float3(0.17753803, 0.96680580, 0.07239744), float3(0, 0, 0.91051993));
            float3 scaled = pow(sRGBToDisplayP3 * linear * 0.01, 0.1593017578125);
            float3 pq = pow((0.8359375 + 18.8515625 * scaled) / (1 + 18.6875 * scaled), 78.84375);
            return float4(pq * color.a, color.a);
        }
        """

    private struct Pipelines {
        var screen: MTLRenderPipelineState
        var cursor: MTLRenderPipelineState
    }

    private let device: MTLDevice
    private let commandQueue: MTLCommandQueue
    private let standardPipelines: Pipelines
    private let hdrPipelines: Pipelines
    private var isHDR = false
    private var colorSpaceName: CFString?
    private let linearSampler: MTLSamplerState
    private let nearestSampler: MTLSamplerState
    private var textureCache: CVMetalTextureCache?
    private var cursorTexture: MTLTexture?
    private var cursorImage: CGImage?

    init(layer: CAMetalLayer) throws {
        guard let device = MTLCreateSystemDefaultDevice() else { throw MessageError("MTLCreateSystemDefaultDevice returned no device") }
        guard let commandQueue = device.makeCommandQueue() else { throw MessageError("MTLDevice returned no command queue") }
        let library = try device.makeLibrary(source: Self.shaderSource, options: nil)
        self.device = device
        self.commandQueue = commandQueue
        layer.device = device
        layer.pixelFormat = .bgra8Unorm
        layer.framebufferOnly = true
        layer.isOpaque = true

        func makePipelines(pixelFormat: MTLPixelFormat, cursorFunction: String) throws -> Pipelines {
            let descriptor = MTLRenderPipelineDescriptor()
            descriptor.vertexFunction = library.makeFunction(name: "quadVertex")
            descriptor.fragmentFunction = library.makeFunction(name: "screenFragment")
            descriptor.colorAttachments[0].pixelFormat = pixelFormat
            let screen = try device.makeRenderPipelineState(descriptor: descriptor)
            descriptor.fragmentFunction = library.makeFunction(name: cursorFunction)
            let attachment = descriptor.colorAttachments[0]!
            attachment.isBlendingEnabled = true
            attachment.sourceRGBBlendFactor = .one
            attachment.sourceAlphaBlendFactor = .one
            attachment.destinationRGBBlendFactor = .oneMinusSourceAlpha
            attachment.destinationAlphaBlendFactor = .oneMinusSourceAlpha
            let cursor = try device.makeRenderPipelineState(descriptor: descriptor)
            return Pipelines(screen: screen, cursor: cursor)
        }
        standardPipelines = try makePipelines(pixelFormat: .bgra8Unorm, cursorFunction: "cursorFragment")
        hdrPipelines = try makePipelines(pixelFormat: .rgba16Float, cursorFunction: "cursorFragmentPQ")

        let samplerDescriptor = MTLSamplerDescriptor()
        samplerDescriptor.minFilter = .linear
        samplerDescriptor.magFilter = .linear
        samplerDescriptor.sAddressMode = .clampToEdge
        samplerDescriptor.tAddressMode = .clampToEdge
        linearSampler = device.makeSamplerState(descriptor: samplerDescriptor)!
        samplerDescriptor.magFilter = .nearest
        nearestSampler = device.makeSamplerState(descriptor: samplerDescriptor)!
        let status = CVMetalTextureCacheCreate(nil, nil, device, nil, &textureCache)
        guard status == kCVReturnSuccess else { throw MessageError("CVMetalTextureCacheCreate", status: status) }
    }

    struct Frame {
        var pixelBuffer: CVPixelBuffer?
        var strips: [CVPixelBuffer?]
        var stripHeight: Int
        var canvasHeight: Int
        var imageRect: CGRect
        var turns: PictureTurns
        var cursor: CursorShape?
        var cursorRect: CGRect?
        var viewSize: CGSize
        var pixelsPerImagePixel: CGFloat
        var pixelsPerCursorPixel: CGFloat
    }

    func render(
        _ frame: Frame, in layer: CAMetalLayer,
        presented: @escaping @Sendable (CFTimeInterval) -> Void,
        failed: @escaping @Sendable () -> Void
    ) -> Bool {
        var retained: [CVMetalTexture] = []
        var images: [(texture: MTLTexture, rect: CGRect, textureRect: SIMD4<Float>)] = []
        if let pixelBuffer = frame.pixelBuffer {
            guard let texture = makeTexture(pixelBuffer, retained: &retained) else { return false }
            images.append((texture, frame.imageRect, SIMD4<Float>(0, 0, 1, 1)))
        } else if !frame.strips.isEmpty {
            guard frame.stripHeight > 0, frame.canvasHeight > 0,
                frame.canvasHeight <= frame.stripHeight * frame.strips.count
            else { return false }
            let canvasHeight = CGFloat(frame.canvasHeight)
            for (band, strip) in frame.strips.enumerated() {
                guard let strip, let texture = makeTexture(strip, retained: &retained) else { return false }
                let top = CGFloat(band * frame.stripHeight)
                let visible = min(CGFloat(frame.stripHeight), canvasHeight - top)
                guard visible > 0 else { continue }
                let unit = frame.turns.shownRect(ofUnit: CGRect(x: 0, y: top / canvasHeight, width: 1, height: visible / canvasHeight))
                let rect = CGRect(
                    x: frame.imageRect.minX + frame.imageRect.width * unit.minX,
                    y: frame.imageRect.minY + frame.imageRect.height * unit.minY,
                    width: frame.imageRect.width * unit.width,
                    height: frame.imageRect.height * unit.height
                )
                images.append((texture, rect, SIMD4<Float>(0, 0, 1, Float(visible / CGFloat(frame.stripHeight)))))
            }
        }
        var drawnCursor: (texture: MTLTexture, rect: CGRect)?
        if let cursor = frame.cursor, let cursorRect = frame.cursorRect {
            guard let texture = texture(for: cursor.image) else { return false }
            drawnCursor = (texture, cursorRect)
        }
        configure(layer, for: frame.pixelBuffer ?? frame.strips.compactMap { $0 }.first)
        let pipelines = isHDR ? hdrPipelines : standardPipelines
        guard let drawable = layer.nextDrawable(), let commandBuffer = commandQueue.makeCommandBuffer() else { return false }
        let pass = MTLRenderPassDescriptor()
        pass.colorAttachments[0].texture = drawable.texture
        pass.colorAttachments[0].loadAction = .clear
        pass.colorAttachments[0].storeAction = .store
        pass.colorAttachments[0].clearColor = MTLClearColor(red: 0, green: 0, blue: 0, alpha: 1)
        guard let encoder = commandBuffer.makeRenderCommandEncoder(descriptor: pass) else { return false }
        let sampler = self.sampler(pixelsPerTexel: frame.pixelsPerImagePixel)
        for image in images {
            draw(
                image.texture, in: image.rect, textureRect: image.textureRect, turns: frame.turns, viewSize: frame.viewSize,
                sampler: sampler, pipeline: pipelines.screen, encoder: encoder)
        }
        if let drawnCursor {
            encoder.setRenderPipelineState(pipelines.cursor)
            var rect = ndc(drawnCursor.rect, in: frame.viewSize)
            var textureRect = SIMD4<Float>(0, 0, 1, 1)
            var turns: UInt32 = 0
            encoder.setVertexBytes(&rect, length: MemoryLayout<SIMD4<Float>>.size, index: 0)
            encoder.setVertexBytes(&textureRect, length: MemoryLayout<SIMD4<Float>>.size, index: 1)
            encoder.setVertexBytes(&turns, length: MemoryLayout<UInt32>.size, index: 2)
            encoder.setFragmentTexture(drawnCursor.texture, index: 0)
            encoder.setFragmentSamplerState(self.sampler(pixelsPerTexel: frame.pixelsPerCursorPixel), index: 0)
            encoder.drawPrimitives(type: .triangleStrip, vertexStart: 0, vertexCount: 4)
        }
        encoder.endEncoding()
        let containsImage = !images.isEmpty
        let box = TextureRetainer(textures: retained)
        commandBuffer.addCompletedHandler { buffer in
            withExtendedLifetime(box) {}
            if buffer.status == .error {
                failed()
                return
            }
            #if targetEnvironment(simulator)
            if containsImage {
                presented(CACurrentMediaTime())
            }
            #endif
        }
        #if !targetEnvironment(simulator)
        drawable.addPresentedHandler { drawable in
            let time = drawable.presentedTime
            guard time.isFinite, time > 0 else {
                failed()
                return
            }
            if containsImage {
                presented(time)
            }
        }
        #endif
        commandBuffer.present(drawable)
        commandBuffer.commit()
        return true
    }

    private func configure(_ layer: CAMetalLayer, for pixelBuffer: CVPixelBuffer?) {
        let hdr = pixelBuffer.map { CVPixelBufferGetPixelFormatType($0) == kCVPixelFormatType_64RGBAHalf } ?? false
        if hdr != isHDR {
            isHDR = hdr
            layer.pixelFormat = hdr ? .rgba16Float : .bgra8Unorm
            if #available(iOS 16, *) {
                layer.wantsExtendedDynamicRangeContent = hdr
                layer.edrMetadata = hdr ? CAEDRMetadata.hdr10(minLuminance: 0.0005, maxLuminance: 1000, opticalOutputScale: 100) : nil
            }
        }
        let colorSpace: CGColorSpace?
        if hdr {
            colorSpace = CGColorSpace(name: CGColorSpace.displayP3_PQ)
        } else if let pixelBuffer, let attached = CVImageBufferGetColorSpace(pixelBuffer)?.takeUnretainedValue() {
            colorSpace = attached
        } else if let pixelBuffer, let attachments = CVBufferCopyAttachments(pixelBuffer, .shouldPropagate),
            let derived = CVImageBufferCreateColorSpaceFromAttachments(attachments)?.takeRetainedValue()
        {
            colorSpace = derived
        } else {
            colorSpace = CGColorSpace(name: CGColorSpace.sRGB)
        }
        let name = colorSpace?.name
        guard name != colorSpaceName || name == nil else { return }
        colorSpaceName = name
        layer.colorspace = colorSpace
    }

    private func draw(
        _ texture: MTLTexture, in rect: CGRect, textureRect: SIMD4<Float>, turns: PictureTurns, viewSize: CGSize, sampler: MTLSamplerState,
        pipeline: MTLRenderPipelineState, encoder: MTLRenderCommandEncoder
    ) {
        encoder.setRenderPipelineState(pipeline)
        var position = ndc(rect, in: viewSize)
        var coordinates = textureRect
        var turnCount = UInt32(turns.count)
        encoder.setVertexBytes(&position, length: MemoryLayout<SIMD4<Float>>.size, index: 0)
        encoder.setVertexBytes(&coordinates, length: MemoryLayout<SIMD4<Float>>.size, index: 1)
        encoder.setVertexBytes(&turnCount, length: MemoryLayout<UInt32>.size, index: 2)
        encoder.setFragmentTexture(texture, index: 0)
        encoder.setFragmentSamplerState(sampler, index: 0)
        encoder.drawPrimitives(type: .triangleStrip, vertexStart: 0, vertexCount: 4)
    }

    private func sampler(pixelsPerTexel: CGFloat) -> MTLSamplerState {
        pixelsPerTexel >= 2 || abs(pixelsPerTexel - 1) < 0.001 ? nearestSampler : linearSampler
    }

    private func ndc(_ rect: CGRect, in size: CGSize) -> SIMD4<Float> {
        SIMD4<Float>(
            Float(rect.minX / size.width * 2 - 1),
            Float(1 - rect.minY / size.height * 2),
            Float(rect.maxX / size.width * 2 - 1),
            Float(1 - rect.maxY / size.height * 2)
        )
    }

    private func makeTexture(_ pixelBuffer: CVPixelBuffer, retained: inout [CVMetalTexture]) -> MTLTexture? {
        guard let textureCache else { return nil }
        let pixelFormat: MTLPixelFormat
        switch CVPixelBufferGetPixelFormatType(pixelBuffer) {
        case kCVPixelFormatType_32BGRA: pixelFormat = .bgra8Unorm
        case kCVPixelFormatType_64RGBAHalf: pixelFormat = .rgba16Float
        default: return nil
        }
        var cvTexture: CVMetalTexture?
        let status = CVMetalTextureCacheCreateTextureFromImage(
            nil, textureCache, pixelBuffer, nil, pixelFormat,
            CVPixelBufferGetWidth(pixelBuffer), CVPixelBufferGetHeight(pixelBuffer), 0, &cvTexture
        )
        guard status == kCVReturnSuccess, let cvTexture, let texture = CVMetalTextureGetTexture(cvTexture) else { return nil }
        retained.append(cvTexture)
        return texture
    }

    private func texture(for image: CGImage) -> MTLTexture? {
        if image === cursorImage, let cursorTexture { return cursorTexture }
        let width = image.width
        let height = image.height
        let bytesPerRow = width * 4
        var pixels = [UInt8](repeating: 0, count: bytesPerRow * height)
        let drawn = pixels.withUnsafeMutableBytes { raw -> Bool in
            guard
                let context = CGContext(
                    data: raw.baseAddress, width: width, height: height, bitsPerComponent: 8, bytesPerRow: bytesPerRow,
                    space: CGColorSpace(name: CGColorSpace.sRGB)!,
                    bitmapInfo: CGImageAlphaInfo.premultipliedFirst.rawValue | CGBitmapInfo.byteOrder32Little.rawValue
                )
            else { return false }
            context.draw(image, in: CGRect(x: 0, y: 0, width: width, height: height))
            return true
        }
        guard drawn else { return nil }
        let descriptor = MTLTextureDescriptor.texture2DDescriptor(pixelFormat: .bgra8Unorm, width: width, height: height, mipmapped: false)
        descriptor.usage = .shaderRead
        guard let texture = device.makeTexture(descriptor: descriptor) else { return nil }
        texture.replace(region: MTLRegionMake2D(0, 0, width, height), mipmapLevel: 0, withBytes: pixels, bytesPerRow: bytesPerRow)
        cursorImage = image
        cursorTexture = texture
        return texture
    }
}

private nonisolated final class TextureRetainer: @unchecked Sendable {
    let textures: [CVMetalTexture]

    init(textures: [CVMetalTexture]) {
        self.textures = textures
    }
}

protocol RemoteScreen: AnyObject {
    var bounds: CGRect { get }
    var imageRect: CGRect { get }
    var displayScale: CGFloat { get }
    var isOccluded: Bool { get }
    func drawsCursor(_ shape: CursorShape?, visible: Bool, scale: CGFloat) -> Bool
}

final class ScreenPresenter {
    private unowned let screen: any RemoteScreen
    private unowned let controller: SessionController
    private let pipeline: DisplayPipeline
    private let layer: CAMetalLayer
    private var renderer: ScreenRenderer?
    private var displayLink: CADisplayLink?
    private var presentationTimer: DispatchSourceTimer?
    private var presentationTick: CFTimeInterval = 0
    private var needsRender = true
    private var renderedThisFrame = false
    private var idleFrames = 0
    private var lastSnapshot: DisplayPipeline.Snapshot?
    private var refreshRate = 0

    init(screen: any RemoteScreen, controller: SessionController, layer: CAMetalLayer) {
        self.screen = screen
        self.controller = controller
        pipeline = controller.pipeline
        self.layer = layer
        do {
            renderer = try ScreenRenderer(layer: layer)
        } catch {
            DispatchQueue.main.async { [controller] in controller.drawingFailed(error) }
        }
        pipeline.setWakeHandler { [weak self] in
            DispatchQueue.main.async { self?.contentChanged() }
        }
    }

    deinit {
        MainActor.assumeIsolated {
            displayLink?.invalidate()
            presentationTimer?.cancel()
            pipeline.setWakeHandler(nil)
        }
    }

    func start(_ makeLink: (_ target: Any, _ selector: Selector) -> CADisplayLink) {
        let link = makeLink(DisplayLinkTarget(self), #selector(DisplayLinkTarget.tick))
        link.add(to: .main, forMode: .common)
        displayLink = link
        refreshRate = 0
    }

    func stop() {
        displayLink?.invalidate()
        displayLink = nil
        presentationTimer?.cancel()
        presentationTimer = nil
    }

    func setRefreshRate(_ rate: Int) {
        guard rate > 0, rate != refreshRate else { return }
        refreshRate = rate
        let maximum = Float(rate)
        displayLink?.preferredFrameRateRange = CAFrameRateRange(minimum: min(30, maximum), maximum: maximum, preferred: maximum)
        controller.screenRefreshRateDidChange(rate)
    }

    func pause() {
        displayLink?.isPaused = true
    }

    func setNeedsRender() {
        needsRender = true
        wake()
    }

    fileprivate func tick() {
        renderedThisFrame = false
        guard !screen.isOccluded else {
            displayLink?.isPaused = true
            return
        }
        if let displayLink, pipeline.isSchedulingPresentation {
            if presentationTimer == nil {
                let timer = DispatchSource.makeTimerSource(queue: .main)
                timer.setEventHandler { [weak self] in
                    MainActor.assumeIsolated { self?.presentScheduledFrames() }
                }
                timer.activate()
                presentationTimer = timer
            }
            presentationTick = displayLink.targetTimestamp
            let midpoint = (displayLink.timestamp + displayLink.targetTimestamp) / 2
            presentationTimer?.schedule(deadline: .now() + max(midpoint - CACurrentMediaTime(), 0), leeway: .microseconds(100))
        }
        let snapshot = pipeline.consume(through: displayLink?.timestamp ?? CACurrentMediaTime())
        if let snapshot {
            lastSnapshot = snapshot
        }
        guard snapshot != nil || needsRender else {
            idleFrames += 1
            if idleFrames > 30 { displayLink?.isPaused = true }
            return
        }
        idleFrames = 0
        renderedThisFrame = true
        needsRender = !render()
    }

    private func presentScheduledFrames() {
        guard !screen.isOccluded, let snapshot = pipeline.consume(through: presentationTick) else { return }
        lastSnapshot = snapshot
        idleFrames = 0
        needsRender = !render()
    }

    private func contentChanged() {
        wake()
        guard !renderedThisFrame, !screen.isOccluded, let snapshot = pipeline.consume(through: CACurrentMediaTime()) else { return }
        lastSnapshot = snapshot
        renderedThisFrame = true
        needsRender = !render()
    }

    private func wake() {
        guard !screen.isOccluded else { return }
        idleFrames = 0
        displayLink?.isPaused = false
    }

    private func render() -> Bool {
        let viewSize = screen.bounds.size
        guard let renderer, viewSize.width > 0, viewSize.height > 0 else { return false }
        let snapshot = lastSnapshot ?? pipeline.snapshot()
        lastSnapshot = snapshot
        let epoch = snapshot.presentationEpoch
        let generation = snapshot.pictureGeneration
        let run = controller.debugInfo.presentationRun
        guard snapshot.turns.shownSize(of: CGSize(width: snapshot.width, height: snapshot.height)) == controller.imageSize,
            pipeline.isCurrentPresentation(epoch: epoch)
        else { return false }
        let imageRect = screen.imageRect
        let scale = screen.displayScale
        let resolutionScale = controller.pictureMatchesWindow ? controller.resolutionScale : 1
        let cursorScale = max(
            scale * controller.info.framebufferPixelsPerCursorPixel, 1 / (controller.cursorPixelsPerPoint * resolutionScale))
        let drawsCursor = screen.drawsCursor(snapshot.cursor, visible: snapshot.cursorVisible, scale: cursorScale)
        var cursorRect: CGRect?
        if let cursor = snapshot.cursor, snapshot.cursorVisible, controller.phase == .connected, drawsCursor {
            let origin = CGPoint(
                x: imageRect.minX + snapshot.cursorPosition.x * scale - cursor.hotspot.x * cursorScale,
                y: imageRect.minY + snapshot.cursorPosition.y * scale - cursor.hotspot.y * cursorScale
            )
            cursorRect = CGRect(
                origin: origin,
                size: CGSize(width: CGFloat(cursor.image.width) * cursorScale, height: CGFloat(cursor.image.height) * cursorScale))
        }
        return renderer.render(
            ScreenRenderer.Frame(
                pixelBuffer: snapshot.pixelBuffer,
                strips: snapshot.strips,
                stripHeight: snapshot.stripHeight,
                canvasHeight: snapshot.canvasHeight,
                imageRect: imageRect,
                turns: snapshot.turns,
                cursor: snapshot.cursor,
                cursorRect: cursorRect,
                viewSize: viewSize,
                pixelsPerImagePixel: scale * layer.contentsScale,
                pixelsPerCursorPixel: cursorScale * layer.contentsScale
            ),
            in: layer,
            presented: { [pipeline, debugInfo = controller.debugInfo] time in
                DispatchQueue.main.async {
                    guard let generation, let run, pipeline.isCurrentPresentation(epoch: epoch) else { return }
                    debugInfo.picturePresented(generation, at: time, run: run)
                }
            },
            failed: { [weak self, pipeline] in
                DispatchQueue.main.async {
                    guard let self, pipeline.isCurrentPresentation(epoch: epoch) else { return }
                    self.setNeedsRender()
                }
            }
        )
    }
}

private final class DisplayLinkTarget {
    weak var presenter: ScreenPresenter?

    init(_ presenter: ScreenPresenter) {
        self.presenter = presenter
    }

    @objc func tick() {
        presenter?.tick()
    }
}
