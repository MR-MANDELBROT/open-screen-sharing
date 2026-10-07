package rfb

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"math"
)

const (
	pointerDiameter  = 20.0
	pointerEdge      = 1.0
	pointerFillGray  = 0x40
	pointerFillAlpha = 0.5
	pointerEdgeGray  = 0xff
	pointerEdgeAlpha = 0.9
	// ScreenSharing 776.27 (macOS 27.0.1, 26A434) treats cursor IDs above
	// 999 as custom images (_HandleCursorImageWithAlphaUpdate, 0x1cc06c804).
	pointerCursorID = 1000
)

func pointerCursor(screen ServerScreen) *Cursor {
	scale := max(screen.Scale, 1) / screen.Density()
	radius := pointerDiameter * scale / 2
	edge := pointerEdge * scale
	size := 2*int(math.Ceil(radius)) + 3
	center := float64(size) / 2
	pixels := make([]byte, size*size*4)
	for y := range size {
		for x := range size {
			distance := math.Hypot(float64(x)+0.5-center, float64(y)+0.5-center)
			inside := coverage(radius - edge - distance)
			fill := inside * pointerFillAlpha
			ring := (coverage(radius-distance) - inside) * pointerEdgeAlpha
			gray := byte(math.Round(fill*pointerFillGray + ring*pointerEdgeGray))
			offset := (y*size + x) * 4
			pixels[offset], pixels[offset+1], pixels[offset+2] = gray, gray, gray
			pixels[offset+3] = byte(math.Round((fill + ring) * 255))
		}
	}
	return &Cursor{
		Width:    size,
		Height:   size,
		HotspotX: size / 2,
		HotspotY: size / 2,
		Pixels:   pixels,
		CacheID:  pointerCursorID,
	}
}

func coverage(distance float64) float64 {
	return min(max(distance+0.5, 0), 1)
}

func cursorRectangle(cursor *Cursor, encoding Encoding, format PixelFormat) (serverRectangle, bool) {
	if !format.TrueColor || format.BitsPerPixel != 32 || format.RedMax != 255 || format.GreenMax != 255 || format.BlueMax != 255 {
		return serverRectangle{}, false
	}
	if encoding == EncodingCursorImage {
		return cursorImageRectangle(cursor, format), true
	}
	return richCursorRectangle(cursor, format), true
}

func cursorColor(cursor *Cursor, format PixelFormat, straight bool) []byte {
	count := cursor.Width * cursor.Height
	color := make([]byte, 0, count*4)
	for i := range count {
		pixel := cursor.Pixels[i*4 : i*4+4]
		blue, green, red, alpha := uint32(pixel[0]), uint32(pixel[1]), uint32(pixel[2]), uint32(pixel[3])
		if straight && alpha > 0 {
			red, green, blue = red*255/alpha, green*255/alpha, blue*255/alpha
		}
		value := red<<format.RedShift | green<<format.GreenShift | blue<<format.BlueShift
		if format.BigEndian {
			color = binary.BigEndian.AppendUint32(color, value)
		} else {
			color = binary.LittleEndian.AppendUint32(color, value)
		}
	}
	return color
}

// ScreenSharing 776.27 (macOS 27.0.1, 26A434) reads encoding 1104 as a
// cursor ID and compressed image; the inflated color plane is followed by
// one alpha byte per pixel (_HandleCursorImageWithAlphaUpdate,
// 0x1cc06c754..0x1cc06c804, 0x1cc06cda8..0x1cc06cf64).
func cursorImageRectangle(cursor *Cursor, format PixelFormat) serverRectangle {
	raw := cursorColor(cursor, format, false)
	for i := range cursor.Width * cursor.Height {
		raw = append(raw, cursor.Pixels[i*4+3])
	}
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	_, _ = writer.Write(raw)
	// ScreenSharing 776.27 (macOS 27.0.1, 26A434) calls inflate with
	// Z_SYNC_FLUSH and accepts only Z_OK, including rejecting Z_STREAM_END
	// (InflateCursorImage, 0x1cc163500..0x1cc163508; arm64e UUID
	// EC0B2C20-F816-3FCE-822D-F9403C09EB50).
	_ = writer.Flush()
	payload := binary.BigEndian.AppendUint32(nil, cursor.CacheID)
	payload = binary.BigEndian.AppendUint32(payload, uint32(compressed.Len()))
	payload = append(payload, compressed.Bytes()...)
	return newRectangle(cursor.HotspotX, cursor.HotspotY, cursor.Width, cursor.Height, EncodingCursorImage, payload)
}

func richCursorRectangle(cursor *Cursor, format PixelFormat) serverRectangle {
	payload := cursorColor(cursor, format, true)
	rowBytes := (cursor.Width + 7) / 8
	mask := make([]byte, rowBytes*cursor.Height)
	for y := range cursor.Height {
		for x := range cursor.Width {
			if cursor.Pixels[(y*cursor.Width+x)*4+3] >= 0x80 {
				mask[y*rowBytes+x/8] |= 0x80 >> (x % 8)
			}
		}
	}
	payload = append(payload, mask...)
	return newRectangle(cursor.HotspotX, cursor.HotspotY, cursor.Width, cursor.Height, EncodingRichCursor, payload)
}
