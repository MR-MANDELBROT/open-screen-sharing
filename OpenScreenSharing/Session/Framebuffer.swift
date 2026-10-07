import CoreGraphics
import CoreImage
import CoreVideo
import Foundation
import UIKit

nonisolated final class Framebuffer: @unchecked Sendable {
    let pixelBuffer: CVPixelBuffer
    let width: Int
    let height: Int
    let bytesPerRow: Int
    let baseAddress: UnsafeMutableRawPointer

    init?(width: Int, height: Int) {
        guard width > 0, height > 0 else { return nil }
        let attributes: [CFString: Any] = [
            kCVPixelBufferIOSurfacePropertiesKey: [CFString: Any](),
            kCVPixelBufferMetalCompatibilityKey: true,
            kCVPixelBufferCGImageCompatibilityKey: true,
            kCVPixelBufferCGBitmapContextCompatibilityKey: true,
        ]
        var buffer: CVPixelBuffer?
        let status = CVPixelBufferCreate(kCFAllocatorDefault, width, height, kCVPixelFormatType_32BGRA, attributes as CFDictionary, &buffer)
        guard status == kCVReturnSuccess, let buffer else { return nil }
        CVPixelBufferLockBaseAddress(buffer, [])
        guard let address = CVPixelBufferGetBaseAddress(buffer) else {
            CVPixelBufferUnlockBaseAddress(buffer, [])
            return nil
        }
        pixelBuffer = buffer
        self.width = width
        self.height = height
        bytesPerRow = CVPixelBufferGetBytesPerRow(buffer)
        baseAddress = address
        memset(address, 0, bytesPerRow * height)
    }

    deinit {
        CVPixelBufferUnlockBaseAddress(pixelBuffer, [])
    }
}

nonisolated enum PixelBufferImage {
    private static let context = CIContext(options: [.cacheIntermediates: false])
    private static let colorSpace = CGColorSpace(name: CGColorSpace.sRGB)

    static func makeImage(_ pixelBuffer: CVPixelBuffer, turns: PictureTurns) -> CGImage? {
        let image = CIImage(cvPixelBuffer: pixelBuffer).oriented(turns.imageOrientation)
        return context.createCGImage(image, from: image.extent, format: .BGRA8, colorSpace: colorSpace)
    }

    static func compose(strips: [CVPixelBuffer?], stripHeight: Int, canvasHeight: Int, turns: PictureTurns) -> CGImage? {
        guard let image = composed(strips: strips, stripHeight: stripHeight, canvasHeight: canvasHeight)?.oriented(turns.imageOrientation)
        else { return nil }
        return context.createCGImage(image, from: image.extent, format: .BGRA8, colorSpace: colorSpace)
    }

    static func makeThumbnail(_ pixelBuffer: CVPixelBuffer, turns: PictureTurns, maxWidth: CGFloat) -> Data? {
        thumbnail(of: CIImage(cvPixelBuffer: pixelBuffer).oriented(turns.imageOrientation), maxWidth: maxWidth)
    }

    static func makeThumbnail(strips: [CVPixelBuffer?], stripHeight: Int, canvasHeight: Int, turns: PictureTurns, maxWidth: CGFloat) -> Data? {
        guard let image = composed(strips: strips, stripHeight: stripHeight, canvasHeight: canvasHeight) else { return nil }
        return thumbnail(of: image.oriented(turns.imageOrientation), maxWidth: maxWidth)
    }

    private static func composed(strips: [CVPixelBuffer?], stripHeight: Int, canvasHeight: Int) -> CIImage? {
        guard let width = strips.compactMap({ $0 }).first.map(CVPixelBufferGetWidth), stripHeight > 0, canvasHeight > 0 else { return nil }
        var image = CIImage(color: .black).cropped(to: CGRect(x: 0, y: 0, width: width, height: canvasHeight))
        for (band, strip) in strips.enumerated() {
            guard let strip else { continue }
            let top = band * stripHeight
            let placed = CIImage(cvPixelBuffer: strip).transformed(by: CGAffineTransform(translationX: 0, y: CGFloat(canvasHeight - top - stripHeight)))
            image = placed.composited(over: image)
        }
        return image.cropped(to: CGRect(x: 0, y: 0, width: width, height: canvasHeight))
    }

    private static func thumbnail(of image: CIImage, maxWidth: CGFloat) -> Data? {
        guard image.extent.width > 0, image.extent.height > 0 else { return nil }
        let scale = min(1, maxWidth / image.extent.width)
        let scaled = image.applyingFilter("CILanczosScaleTransform", parameters: [kCIInputScaleKey: scale, kCIInputAspectRatioKey: 1])
        guard let cgImage = context.createCGImage(scaled, from: scaled.extent, format: .BGRA8, colorSpace: colorSpace) else { return nil }
        return UIImage(cgImage: cgImage).jpegData(compressionQuality: 0.8)
    }
}
