package rfb

import (
	"crypto/aes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"slices"
)

type ViewerEvent interface {
	viewerEvent()
}

type ViewerPointer struct {
	X, Y    int
	Buttons uint8
}

// ScreenSharing’s keycode table maps Option to Meta (0xffe7/0xffe8) and
// Command to Alt (0xffe9/0xffea). The legacy key-event path also reads
// preference overrides (macOS 27.0.1 26A434 and 27.2 26B5101f,
// _RFBPostKeyEventCore and _ConvertKeycodeToX11Keysym).
type ViewerKey struct {
	Down         bool
	Keysym       uint32
	HasKeyCode   bool
	KeyCode      uint16
	KeyboardType KeyboardType
}

type ViewerSystemCommand struct {
	Command SystemCommand
}

type ViewerScroll struct {
	X, Y           int
	DeltaX, DeltaY float64
	LinesX, LinesY int
	Continuous     bool
	Phase          GesturePhase
	Momentum       uint32
}

type GestureKind uint16

const (
	GestureKindBegin        GestureKind = 1
	GestureKindEnd          GestureKind = 2
	GestureKindMagnify      GestureKind = 3
	GestureKindSmartMagnify GestureKind = 4
	GestureKindRotate       GestureKind = 5
	GestureKindSwipe        GestureKind = 6
	GestureKindLookup       GestureKind = 7
	GestureKindScroll       GestureKind = 8
)

type ViewerGesture struct {
	Kind           GestureKind
	X, Y           int
	Phase          GesturePhase
	Amount         float64
	DeltaX, DeltaY float64
	Direction      SwipeDirection
}

type ViewerClipboard struct {
	Clipboard *Clipboard
}

type ViewerClipboardSharing struct {
	Enabled bool
}

type ViewerMode struct {
	Mode Mode
}

func (ViewerPointer) viewerEvent()          {}
func (ViewerKey) viewerEvent()              {}
func (ViewerSystemCommand) viewerEvent()    {}
func (ViewerScroll) viewerEvent()           {}
func (ViewerGesture) viewerEvent()          {}
func (ViewerClipboard) viewerEvent()        {}
func (ViewerClipboardSharing) viewerEvent() {}
func (ViewerMode) viewerEvent()             {}

const (
	clientSetPixelFormat  = 0x00
	clientSetEncodings    = 0x02
	clientUpdateRequest   = 0x03
	clientKeyEvent        = 0x04
	clientPointerEvent    = 0x05
	clientCutText         = 0x06
	clientSetScale        = 0x08
	clientAutoUpdate      = 0x09
	clientSetMode         = 0x0a
	clientFetchPasteboard = 0x0b
	clientSetCurtain      = 0x0c
	clientSetDisplay      = 0x0d
	clientEncryptedEvent  = 0x10
	clientSetEncryption   = 0x12
	clientAutoPasteboard  = 0x15
	clientAssistCursor    = 0x16
	clientEvent           = 0x17
	clientKeyboardSource  = 0x1a
	clientMediaStream     = 0x1c
	clientDisplayConfig   = 0x1d
	clientPasteboard      = 0x1f
	clientViewerInfo      = 0x21
	clientMessageLimit    = 1 << 16
	pasteboardHeaderSize  = 16
)

func readClientMessage(source *stream) ([]byte, error) {
	messageType, err := source.u8()
	if err != nil {
		return nil, err
	}
	fixed := func(size int) ([]byte, error) {
		message := make([]byte, size)
		message[0] = messageType
		return message, source.readFull(message[1:])
	}
	counted := func(headerSize int, length func(header []byte) int, limit int) ([]byte, error) {
		header, err := fixed(headerSize)
		if err != nil {
			return nil, err
		}
		size := length(header)
		if size < 0 || size > limit {
			return nil, fmt.Errorf("client message %#02x of %d bytes", messageType, size)
		}
		message := header
		for remaining := size; remaining > 0; {
			piece := min(remaining, clientMessageLimit)
			start := len(message)
			message = slices.Grow(message, piece)[:start+piece]
			err = source.readFull(message[start:])
			if err != nil {
				return nil, err
			}
			remaining -= piece
		}
		return message, nil
	}
	u16At := func(offset int) func([]byte) int {
		return func(header []byte) int { return int(binary.BigEndian.Uint16(header[offset:])) }
	}
	switch messageType {
	case clientSetPixelFormat:
		return fixed(20)
	case clientSetEncodings:
		return counted(4, func(header []byte) int { return 4 * int(binary.BigEndian.Uint16(header[2:])) }, clientMessageLimit*4)
	case clientUpdateRequest, clientSetScale:
		return fixed(10)
	case clientKeyEvent, clientFetchPasteboard, clientSetDisplay, clientAutoPasteboard:
		return fixed(8)
	case clientPointerEvent:
		return fixed(6)
	case clientCutText:
		return counted(8, func(header []byte) int {
			length := int32(binary.BigEndian.Uint32(header[4:]))
			if length < 0 {
				length = -length
			}
			return int(length)
		}, maximumPasteboard)
	case clientAutoUpdate:
		return fixed(16)
	case clientSetMode:
		return fixed(4)
	case clientSetCurtain:
		return counted(6, u16At(4), clientMessageLimit)
	case clientEncryptedEvent:
		return fixed(18)
	case clientSetEncryption:
		return counted(8, func(header []byte) int { return 4 * int(binary.BigEndian.Uint16(header[6:])) }, clientMessageLimit*4)
	case clientAssistCursor, clientEvent, clientKeyboardSource, clientMediaStream, clientDisplayConfig, clientViewerInfo:
		return counted(4, u16At(2), clientMessageLimit)
	case clientPasteboard:
		return counted(pasteboardHeaderSize, func(header []byte) int { return int(binary.BigEndian.Uint32(header[12:])) }, maximumPasteboard)
	default:
		return nil, fmt.Errorf("unknown client message type %#02x", messageType)
	}
}

func position(data []byte) (int, int) {
	return int(binary.BigEndian.Uint16(data)), int(binary.BigEndian.Uint16(data[2:]))
}

// ScreensharingAgent on macOS 27.0.1 (26A434) passes each wire-mask bit
// index to CGEventCreateMouseEvent: bit 1 is right and bit 2 is middle
// (arm64e UUID DA1CD9BA-97A1-3758-B425-00FA34B428A3,
// 0x100023280..0x1000232f8, 0x1000233d0..0x1000233dc).
func unswapButtons(wire uint8) uint8 {
	buttons := wire & ButtonLeft
	if wire&0x02 != 0 {
		buttons |= ButtonRight
	}
	if wire&0x04 != 0 {
		buttons |= ButtonMiddle
	}
	return buttons
}

func parsePointerEvent(message []byte) ViewerEvent {
	x, y := position(message[2:])
	wire := message[1]
	wheel := wire & (wheelUp | wheelDown | wheelLeft | wheelRight)
	if wheel == 0 {
		return ViewerPointer{X: x, Y: y, Buttons: unswapButtons(wire)}
	}
	scroll := ViewerScroll{X: x, Y: y}
	if wheel&wheelUp != 0 {
		scroll.LinesY++
	}
	if wheel&wheelDown != 0 {
		scroll.LinesY--
	}
	if wheel&wheelLeft != 0 {
		scroll.LinesX++
	}
	if wheel&wheelRight != 0 {
		scroll.LinesX--
	}
	scroll.DeltaX, scroll.DeltaY = float64(scroll.LinesX*10), float64(scroll.LinesY*10)
	return scroll
}

// ScreenSharing 776.27 (macOS 27.0.1, 26A434) also sends pointer events
// in message 0x10: plaintext byte 0 is zero, byte 10 is 0xff, byte 11
// is the button mask, and bytes 12..15 hold big-endian x/y coordinates
// (_RFBPostX11KeyAndMouseCore, 0x1cc0717a8..0x1cc0718d8,
// 0x1cc071a1c..0x1cc071a20, 0x1cc07245c..0x1cc0724d0; arm64e UUID
// EC0B2C20-F816-3FCE-822D-F9403C09EB50).
func parseEncryptedEvent(key [16]byte, message []byte) (ViewerEvent, error) {
	flags := message[1]
	block := message[2:18]
	err := decryptECB(key[:], block)
	if err != nil {
		return nil, err
	}
	if block[0] != 0xff && block[0] != 0 {
		return nil, errors.New("the event does not decrypt with the session key")
	}
	if block[0] == 0 && block[10] == 0xff {
		return parsePointerEvent(append([]byte{clientPointerEvent}, block[11:16]...)), nil
	}
	keysym := binary.BigEndian.Uint32(block[2:])
	if flags == 0 && keysym >= uint32(SystemCommandLaunchpad) && keysym <= uint32(SystemCommandAppWindows) {
		return ViewerSystemCommand{Command: SystemCommand(keysym)}, nil
	}
	return ViewerKey{
		Down:         block[1] != 0,
		Keysym:       keysym,
		HasKeyCode:   flags&keyFlagKeyCodeValid != 0,
		KeyCode:      binary.BigEndian.Uint16(block[14:]),
		KeyboardType: KeyboardType(binary.BigEndian.Uint16(block[12:])),
	}, nil
}

func parseEventMessage(message []byte) (ViewerEvent, error) {
	if len(message) < 8 {
		return nil, errors.New("short event message")
	}
	subtype := binary.BigEndian.Uint16(message[6:])
	body := message[8:]
	need := func(size int) error {
		if len(body) < size {
			return fmt.Errorf("event %d of %d bytes", subtype, len(body))
		}
		return nil
	}
	be32 := func(offset int) uint32 { return binary.BigEndian.Uint32(body[offset:]) }
	switch subtype {
	case eventGestureStart, eventGestureEnd:
		if err := need(8); err != nil {
			return nil, err
		}
		x, y := position(body[4:])
		return ViewerGesture{Kind: GestureKind(subtype), X: x, Y: y}, nil
	case eventGestureMagnify, eventGestureRotate:
		if err := need(28); err != nil {
			return nil, err
		}
		x, y := position(body[8:])
		return ViewerGesture{
			Kind:   GestureKind(subtype),
			X:      x,
			Y:      y,
			Amount: math.Float64frombits(binary.BigEndian.Uint64(body)),
			Phase:  GesturePhase(binary.BigEndian.Uint64(body[12:])),
		}, nil
	case eventGestureSmartMagnify, eventGestureLookup:
		if err := need(4); err != nil {
			return nil, err
		}
		x, y := position(body)
		return ViewerGesture{Kind: GestureKind(subtype), X: x, Y: y}, nil
	case eventGestureSwipe:
		if err := need(44); err != nil {
			return nil, err
		}
		x, y := position(body[40:])
		progress := math.Float32frombits(be32(8))
		direction := SwipeBack
		switch {
		case be32(0) == 2 && progress < 0:
			direction = SwipeUp
		case be32(0) == 2:
			direction = SwipeDown
		case progress < 0:
			direction = SwipeForward
		}
		return ViewerGesture{Kind: GestureKindSwipe, X: x, Y: y, Direction: direction}, nil
	case eventGestureScroll:
		if err := need(28); err != nil {
			return nil, err
		}
		x, y := position(body[24:])
		return ViewerGesture{
			Kind:   GestureKindScroll,
			X:      x,
			Y:      y,
			DeltaX: float64(math.Float32frombits(be32(0))),
			DeltaY: float64(math.Float32frombits(be32(4))),
			Phase:  GesturePhase(be32(16)),
		}, nil
	case eventScrollWheel:
		if err := need(50); err != nil {
			return nil, err
		}
		x, y := position(body[46:])
		return ViewerScroll{
			X:          x,
			Y:          y,
			LinesX:     int(int16(binary.BigEndian.Uint16(body[0:]))),
			LinesY:     int(int16(binary.BigEndian.Uint16(body[2:]))),
			DeltaX:     float64(int32(be32(18))),
			DeltaY:     float64(int32(be32(22))),
			Phase:      GesturePhase(be32(30)),
			Momentum:   be32(34),
			Continuous: be32(42)&2 != 0,
		}, nil
	default:
		return nil, nil
	}
}

func parseClientPasteboard(message []byte) (*Clipboard, error) {
	promise := message[2]&1 != 0
	uncompressed := binary.BigEndian.Uint32(message[8:])
	if uncompressed > maximumPasteboard {
		return nil, fmt.Errorf("pasteboard of %d bytes", uncompressed)
	}
	archive, err := inflatePasteboard(message[pasteboardHeaderSize:], uncompressed)
	if err != nil {
		return nil, fmt.Errorf("pasteboard: %w", err)
	}
	clipboard, err := parsePasteboardArchive(archive)
	if err != nil {
		return nil, err
	}
	clipboard.Promise = promise
	return clipboard, nil
}

func parseEncodings(message []byte) []Encoding {
	count := int(binary.BigEndian.Uint16(message[2:]))
	encodings := make([]Encoding, 0, count)
	for i := range count {
		encodings = append(encodings, Encoding(int32(binary.BigEndian.Uint32(message[4+4*i:]))))
	}
	return encodings
}

func decryptECB(key []byte, data []byte) error {
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	for offset := 0; offset+16 <= len(data); offset += 16 {
		block.Decrypt(data[offset:offset+16], data[offset:offset+16])
	}
	return nil
}
