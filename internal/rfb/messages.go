package rfb

import (
	"encoding/binary"
	"fmt"
	"math"
)

func setPixelFormatMessage(format PixelFormat) []byte {
	message := make([]byte, 20)
	format.encode(message[4:])
	return message
}

func setEncodingsMessage(encodings []Encoding) []byte {
	message := []byte{2, 0}
	message = binary.BigEndian.AppendUint16(message, uint16(len(encodings)))
	for _, encoding := range encodings {
		message = binary.BigEndian.AppendUint32(message, uint32(encoding))
	}
	return message
}

func framebufferUpdateRequestMessage(incremental bool, x, y, width, height int) []byte {
	message := []byte{3, boolByte(incremental)}
	message = binary.BigEndian.AppendUint16(message, uint16(x))
	message = binary.BigEndian.AppendUint16(message, uint16(y))
	message = binary.BigEndian.AppendUint16(message, uint16(width))
	message = binary.BigEndian.AppendUint16(message, uint16(height))
	return message
}

func keyEventMessage(down bool, keysym uint32) []byte {
	message := []byte{4, boolByte(down), 0, 0}
	return binary.BigEndian.AppendUint32(message, keysym)
}

func pointerEventMessage(buttons uint8, x, y int) []byte {
	message := []byte{5, buttons}
	message = binary.BigEndian.AppendUint16(message, uint16(clampInt(x, 0, math.MaxUint16)))
	return binary.BigEndian.AppendUint16(message, uint16(clampInt(y, 0, math.MaxUint16)))
}

func clientCutTextMessage(text []byte) []byte {
	message := []byte{6, 0, 0, 0}
	message = binary.BigEndian.AppendUint32(message, uint32(len(text)))
	return append(message, text...)
}

// Apple's RFBAutoFrameUpdate sends a 16-byte message 9 with the rectangle at byte 8.
// Rechecked in macOS 27.0.1 (26A434), ScreenSharing.framework arm64e UUID
// EC0B2C20-F816-3FCE-822D-F9403C09EB50, _RFBAutoFrameUpdate,
// construction at 0x1cc068668..0x1cc06871c; 16-byte write at 0x1cc0687a4..0x1cc0687b0.
func autoFramebufferUpdateMessage(intervalMicroseconds uint32, x, y, width, height int) []byte {
	message := []byte{0x09, 0}
	message = binary.BigEndian.AppendUint16(message, 1)
	message = binary.BigEndian.AppendUint32(message, intervalMicroseconds)
	message = binary.BigEndian.AppendUint16(message, uint16(x))
	message = binary.BigEndian.AppendUint16(message, uint16(y))
	message = binary.BigEndian.AppendUint16(message, uint16(width))
	return binary.BigEndian.AppendUint16(message, uint16(height))
}

func viewerInfoMessage() []byte {
	message := make([]byte, 66)
	message[0] = 0x21
	binary.BigEndian.PutUint16(message[2:], 62)
	binary.BigEndian.PutUint16(message[4:], 1)
	for i, value := range []uint32{2, 6, 1, 0, 26, 6, 2} {
		binary.BigEndian.PutUint32(message[6+i*4:], value)
	}
	bitmap := message[34:]
	bitmap[0] = 0xb0
	bitmap[2] = 0x0c
	bitmap[3] = 0x03
	bitmap[4] = 0x90
	bitmap[10] = 0x40
	return message
}

func setModeMessage(mode Mode) []byte {
	return binary.BigEndian.AppendUint16([]byte{0x0a, 0}, uint16(mode))
}

func autoPasteboardMessage(enabled bool) []byte {
	selector := byte(2)
	if enabled {
		selector = 1
	}
	return []byte{0x15, 0, 0, selector, 0, 0, 0, 0}
}

// Apple's RFBSetDisplay sends message 0x0d with the selector at byte 1 and a
// big-endian display ID at byte 4. Rechecked in macOS 27.0.1 (26A434),
// ScreenSharing.framework arm64e UUID EC0B2C20-F816-3FCE-822D-F9403C09EB50,
// _RFBSetDisplay, 0x1cc066c1c-0x1cc066c3c.
func setDisplayMessage(id uint32) []byte {
	message := []byte{0x0d, boolByte(id == 0), 0, 0}
	return binary.BigEndian.AppendUint32(message, id)
}

// SkyLight 1.600.0 reports MaximumSizeInPixels 16384×16384 via
// +[SLVirtualDisplay capabilities] on macOS 27.0.1 (26A434).
const VirtualDisplayLargestSide = 16384

func CheckVirtualDisplay(width, height int, density float64) error {
	if density != 1 && density != 2 {
		return fmt.Errorf("virtual display density %v; the Mac takes 1 or 2", density)
	}
	if width <= 0 || height <= 0 {
		return fmt.Errorf("virtual display of %dx%d points", width, height)
	}
	return nil
}

// Apple's message 0x1d uses a 0x9c-byte display header and 0x1c-byte modes.
// The viewer sets descriptor flag bit 0 for dynamic resolution and mode flag bit 0
// for HDR. The wire builder takes the per-axis maximum pixel dimensions across modes.
// Rechecked in macOS 27.0.1 (26A434), ScreenSharing.framework arm64e UUID
// EC0B2C20-F816-3FCE-822D-F9403C09EB50: _RFBSetDisplayConfiguration at
// 0x1cc148bd8..0x1cc148bf0, 0x1cc148d48..0x1cc148f24 and 0x1cc149000..0x1cc1490d4;
// -[SSSession stConfigureVirtualDisplaysWithDimensions:] at
// 0x1cc0d9410..0x1cc0d9420 and 0x1cc0d9664..0x1cc0d998c.
// ScreensharingAgent arm64e UUID DA1CD9BA-97A1-3758-B425-00FA34B428A3 forwards the
// received maximum to SLVirtualDisplayConfiguration as maximumSizeInPixels:
// -[SSAgentVirtualDisplay initVirtualDisplay:displayInfo:blankScreen:dynamicResolution:]
// loads at 0x1000025b0..0x1000025b4, initializer call at 0x10000274c.
func virtualDisplayMessage(width, height int, density float64, maximumWidth, maximumHeight int, refreshRate float64, hdr bool) ([]byte, error) {
	err := CheckVirtualDisplay(width, height, density)
	if err != nil {
		return nil, err
	}
	pixelWidth := int(float64(width) * density)
	pixelHeight := int(float64(height) * density)
	maximumWidth = max(min(maximumWidth, VirtualDisplayLargestSide), pixelWidth)
	maximumHeight = max(min(maximumHeight, VirtualDisplayLargestSide), pixelHeight)
	const (
		descriptorHead = 0x9c
		modeSize       = 0x1c
	)
	body := binary.BigEndian.AppendUint16(nil, 1)
	body = binary.BigEndian.AppendUint16(body, 1)
	body = binary.BigEndian.AppendUint32(body, 0)
	descriptor := binary.BigEndian.AppendUint16(nil, descriptorHead+modeSize)
	descriptor = append(descriptor, make([]byte, 0x7a-2)...)
	descriptor = binary.BigEndian.AppendUint32(descriptor, 1)
	descriptor = binary.BigEndian.AppendUint32(descriptor, 4)
	millimetres := func(points int) uint32 {
		return math.Float32bits(float32(points) / 132 * 25.4)
	}
	descriptor = binary.BigEndian.AppendUint32(descriptor, millimetres(width))
	descriptor = binary.BigEndian.AppendUint32(descriptor, millimetres(height))
	descriptor = binary.BigEndian.AppendUint32(descriptor, uint32(maximumWidth))
	descriptor = binary.BigEndian.AppendUint32(descriptor, uint32(maximumHeight))
	descriptor = binary.BigEndian.AppendUint16(descriptor, 0)
	descriptor = binary.BigEndian.AppendUint16(descriptor, 0)
	descriptor = binary.BigEndian.AppendUint32(descriptor, 7)
	descriptor = binary.BigEndian.AppendUint16(descriptor, 1)
	descriptor = binary.BigEndian.AppendUint32(descriptor, uint32(pixelWidth))
	descriptor = binary.BigEndian.AppendUint32(descriptor, uint32(pixelHeight))
	descriptor = binary.BigEndian.AppendUint32(descriptor, uint32(width))
	descriptor = binary.BigEndian.AppendUint32(descriptor, uint32(height))
	descriptor = binary.BigEndian.AppendUint64(descriptor, math.Float64bits(refreshRate))
	descriptor = binary.BigEndian.AppendUint32(descriptor, uint32(boolByte(hdr)))
	body = append(body, descriptor...)
	message := []byte{0x1d, 0}
	message = binary.BigEndian.AppendUint16(message, uint16(len(body)))
	return append(message, body...), nil
}

func clampInt(value, low, high int) int {
	return min(max(value, low), high)
}
