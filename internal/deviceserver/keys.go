package deviceserver

import (
	"context"
	"slices"

	"github.com/nekohasekai/open-screen-sharing/internal/coredevice/hid"
	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
	E "github.com/sagernet/sing/common/exceptions"
)

const (
	usageReturn         = 0x28
	usageEscape         = 0x29
	usageBackspace      = 0x2a
	usageTab            = 0x2b
	usageSpace          = 0x2c
	usageBackslash      = 0x31
	usageGrave          = 0x35
	usageCapsLock       = 0x39
	usageInsert         = 0x49
	usageHome           = 0x4a
	usagePageUp         = 0x4b
	usageDeleteForward  = 0x4c
	usageEnd            = 0x4d
	usagePageDown       = 0x4e
	usageRight          = 0x4f
	usageLeft           = 0x50
	usageDown           = 0x51
	usageUp             = 0x52
	usageNumLock        = 0x53
	usageKeypadEnter    = 0x58
	usageNonUSBackslash = 0x64
	usageApplication    = 0x65
	usageLeftControl    = 0xe0
	usageLeftShift      = 0xe1
	usageLeftAlt        = 0xe2
	usageLeftGUI        = 0xe3
	usageRightControl   = 0xe4
	usageRightShift     = 0xe5
	usageRightAlt       = 0xe6
	usageRightGUI       = 0xe7
)

const (
	keyCodeISOSection = 10
	keyCodeGrave      = 50
	keyCodeH          = 4
	keyCodeL          = 37
	keyCodeCapsLock   = 57
	keyCodeVolumeUp   = 72
	keyCodeVolumeDown = 73
	keyCodeMute       = 74
	keyCodeDown       = 125
	keyCodeUp         = 126
)

var keyCodeUsages = func() map[uint16]uint8 {
	table := make(map[uint16]uint8)
	letters := []uint16{0, 11, 8, 2, 14, 3, 5, 4, 34, 38, 40, 37, 46, 45, 31, 35, 12, 15, 1, 17, 32, 9, 13, 7, 16, 6}
	for index, keyCode := range letters {
		table[keyCode] = uint8(0x04 + index)
	}
	digits := []uint16{18, 19, 20, 21, 23, 22, 26, 28, 25, 29}
	for index, keyCode := range digits {
		table[keyCode] = uint8(0x1e + index)
	}
	functionKeys := []uint16{122, 120, 99, 118, 96, 97, 98, 100, 101, 109, 103, 111}
	for index, keyCode := range functionKeys {
		table[keyCode] = uint8(0x3a + index)
	}
	laterFunctionKeys := []uint16{105, 107, 113, 106, 64, 79, 80, 90}
	for index, keyCode := range laterFunctionKeys {
		table[keyCode] = uint8(0x68 + index)
	}
	keypadDigits := []uint16{83, 84, 85, 86, 87, 88, 89, 91, 92}
	for index, keyCode := range keypadDigits {
		table[keyCode] = uint8(0x59 + index)
	}
	for keyCode, usage := range map[uint16]uint8{
		36: usageReturn, 53: usageEscape, 51: usageBackspace, 48: usageTab, 49: usageSpace,
		27: 0x2d, 24: 0x2e, 33: 0x2f, 30: 0x30, 42: usageBackslash, 41: 0x33, 39: 0x34,
		43: 0x36, 47: 0x37, 44: 0x38, keyCodeCapsLock: usageCapsLock,
		114: usageInsert, 115: usageHome, 116: usagePageUp, 117: usageDeleteForward, 119: usageEnd,
		121: usagePageDown, 124: usageRight, 123: usageLeft, 125: usageDown, 126: usageUp,
		71: usageNumLock, 75: 0x54, 67: 0x55, 78: 0x56, 69: 0x57, 76: usageKeypadEnter,
		82: 0x62, 65: 0x63, 81: 0x67, 110: usageApplication,
		95: 0x85, 93: 0x89, 94: 0x87, 102: 0x91, 104: 0x90,
		59: usageLeftControl, 56: usageLeftShift, 58: usageLeftAlt, 55: usageLeftGUI,
		62: usageRightControl, 60: usageRightShift, 61: usageRightAlt, 54: usageRightGUI,
	} {
		table[keyCode] = usage
	}
	return table
}()

func keyUsage(keyCode uint16, keyboard rfb.KeyboardType) (uint8, bool) {
	switch keyCode {
	case keyCodeISOSection:
		if keyboard == rfb.KeyboardISO {
			return usageGrave, true
		}
		return usageNonUSBackslash, true
	case keyCodeGrave:
		if keyboard == rfb.KeyboardISO {
			return usageNonUSBackslash, true
		}
		return usageGrave, true
	}
	usage, found := keyCodeUsages[keyCode]
	return usage, found
}

var keyCodeButtons = map[uint16]hid.Button{
	keyCodeVolumeUp:   hid.ButtonVolumeUp,
	keyCodeVolumeDown: hid.ButtonVolumeDown,
	keyCodeMute:       hid.ButtonMute,
}

var comboButtons = map[uint16]hid.Button{
	keyCodeH:    hid.ButtonHome,
	keyCodeL:    hid.ButtonLock,
	keyCodeUp:   hid.ButtonVolumeUp,
	keyCodeDown: hid.ButtonVolumeDown,
}

var keysymUsages = func() map[uint32]uint8 {
	table := map[uint32]uint8{
		0xff08: usageBackspace, 0xff09: usageTab, 0xff0d: usageReturn, 0xff1b: usageEscape,
		0xffff: usageDeleteForward, 0xff50: usageHome, 0xff51: usageLeft, 0xff52: usageUp,
		0xff53: usageRight, 0xff54: usageDown, 0xff55: usagePageUp, 0xff56: usagePageDown,
		0xff57: usageEnd, 0xff63: usageInsert, 0xff67: usageApplication, 0xff7f: usageNumLock,
		0xff8d: usageKeypadEnter, 0xffaa: 0x55, 0xffab: 0x57, 0xffac: 0x85, 0xffad: 0x56,
		0xffae: 0x63, 0xffaf: 0x54, 0xffbd: 0x67, 0xffb0: 0x62, 0xffe5: usageCapsLock,
		0xffe1: usageLeftShift, 0xffe2: usageRightShift, 0xffe3: usageLeftControl, 0xffe4: usageRightControl,
		// ScreenSharing maps Option to Meta (0xffe7/0xffe8) by default
		// (ScreenSharing 776.27, macOS 27.0.1 26A434, UUID EC0B2C20-F816-3FCE-822D-F9403C09EB50,
		// _MacVirtualKeycodeToKeysym 0x1e5a411e0, RFBPostKeyEventCore.optionKeyMapping 0x1e5a411dc;
		// ScreenSharing 786.3, macOS 27.2 26B5101f, UUID 21DF7A41-0D38-3DF3-B87A-8A6249686735,
		// _MacVirtualKeycodeToKeysym 0x272252840, RFBPostKeyEventCore.optionKeyMapping 0x27225283c).
		0xffe7: usageLeftAlt, 0xffe8: usageRightAlt, 0xffe9: usageLeftAlt, 0xffea: usageRightAlt,
		0xffeb: usageLeftGUI, 0xffec: usageRightGUI,
	}
	for index := range uint32(9) {
		table[0xffb1+index] = uint8(0x59 + index)
	}
	for index := range uint32(12) {
		table[0xffbe+index] = uint8(0x3a + index)
	}
	for index := range uint32(8) {
		table[0xffca+index] = uint8(0x68 + index)
	}
	return table
}()

var keysymButtons = map[uint32]hid.Button{
	0x1008ff11: hid.ButtonVolumeDown,
	0x1008ff12: hid.ButtonMute,
	0x1008ff13: hid.ButtonVolumeUp,
	0x1008ff2a: hid.ButtonLock,
}

type asciiKeystroke struct {
	usage uint8
	shift bool
}

var asciiKeystrokes = func() map[byte]asciiKeystroke {
	table := make(map[byte]asciiKeystroke)
	for index := range byte(26) {
		table['a'+index] = asciiKeystroke{usage: 0x04 + index}
		table['A'+index] = asciiKeystroke{usage: 0x04 + index, shift: true}
	}
	for index, character := range []byte("1234567890") {
		table[character] = asciiKeystroke{usage: 0x1e + byte(index)}
	}
	for index, character := range []byte("!@#$%^&*()") {
		table[character] = asciiKeystroke{usage: 0x1e + byte(index), shift: true}
	}
	plain := map[byte]uint8{
		' ': usageSpace, '-': 0x2d, '=': 0x2e, '[': 0x2f, ']': 0x30, '\\': usageBackslash,
		';': 0x33, '\'': 0x34, '`': usageGrave, ',': 0x36, '.': 0x37, '/': 0x38,
	}
	for character, usage := range plain {
		table[character] = asciiKeystroke{usage: usage}
	}
	shifted := map[byte]uint8{
		'_': 0x2d, '+': 0x2e, '{': 0x2f, '}': 0x30, '|': usageBackslash,
		':': 0x33, '"': 0x34, '~': usageGrave, '<': 0x36, '>': 0x37, '?': 0x38,
	}
	for character, usage := range shifted {
		table[character] = asciiKeystroke{usage: usage, shift: true}
	}
	return table
}()

func keysymKeystroke(keysym uint32) (asciiKeystroke, bool) {
	if usage, found := keysymUsages[keysym]; found {
		return asciiKeystroke{usage: usage}, true
	}
	if keysym&0xff000000 == 0x01000000 {
		keysym &^= 0x01000000
	}
	if keysym < 0x80 {
		keystroke, found := asciiKeystrokes[byte(keysym)]
		return keystroke, found
	}
	return asciiKeystroke{}, false
}

type keyID struct {
	keyCode bool
	value   uint32
}

type pressedKey struct {
	usage     uint8
	shift     bool
	swallowed bool
}

type keyboard struct {
	pressed  map[keyID]pressedKey
	held     map[uint8]int
	capsLock bool
}

func newKeyboard() *keyboard {
	return &keyboard{pressed: make(map[keyID]pressedKey), held: make(map[uint8]int)}
}

func (k *keyboard) reset() {
	clear(k.pressed)
	clear(k.held)
}

func (k *keyboard) holding(usages ...uint8) bool {
	return slices.ContainsFunc(usages, func(usage uint8) bool {
		return k.held[usage] > 0
	})
}

func (k *keyboard) comboHeld() bool {
	return k.holding(usageLeftControl, usageRightControl) && k.holding(usageLeftAlt, usageRightAlt) &&
		k.holding(usageLeftGUI, usageRightGUI)
}

func (k *keyboard) press(ctx context.Context, input *hid.Client, usage uint8) error {
	k.held[usage]++
	if k.held[usage] > 1 {
		return nil
	}
	return input.KeyDown(ctx, usage)
}

func (k *keyboard) release(ctx context.Context, input *hid.Client, usage uint8) error {
	if k.held[usage] == 0 {
		return nil
	}
	k.held[usage]--
	if k.held[usage] > 0 {
		return nil
	}
	delete(k.held, usage)
	return input.KeyUp(ctx, usage)
}

func (k *keyboard) handle(ctx context.Context, input *hid.Client, event rfb.ViewerKey) error {
	if event.HasKeyCode {
		if event.KeyCode == keyCodeCapsLock {
			return k.capsLockKey(ctx, input, event.Down)
		}
		return k.key(ctx, input, keyID{keyCode: true, value: uint32(event.KeyCode)}, event.Down, func() (pressedKey, *hid.Button, bool) {
			if button, found := keyCodeButtons[event.KeyCode]; found {
				return pressedKey{swallowed: true}, &button, true
			}
			if button, found := comboButtons[event.KeyCode]; found && k.comboHeld() {
				return pressedKey{swallowed: true}, &button, true
			}
			usage, found := keyUsage(event.KeyCode, event.KeyboardType)
			return pressedKey{usage: usage}, nil, found
		})
	}
	return k.key(ctx, input, keyID{value: event.Keysym}, event.Down, func() (pressedKey, *hid.Button, bool) {
		if button, found := keysymButtons[event.Keysym]; found {
			return pressedKey{swallowed: true}, &button, true
		}
		keystroke, found := keysymKeystroke(event.Keysym)
		if !found {
			return pressedKey{}, nil, false
		}
		addShift := keystroke.shift && !k.holding(usageLeftShift, usageRightShift)
		return pressedKey{usage: keystroke.usage, shift: addShift}, nil, true
	})
}

func (k *keyboard) key(ctx context.Context, input *hid.Client, id keyID, down bool, lookup func() (pressedKey, *hid.Button, bool)) error {
	if !down {
		pressed, found := k.pressed[id]
		if !found {
			return nil
		}
		delete(k.pressed, id)
		if pressed.swallowed {
			return nil
		}
		err := k.release(ctx, input, pressed.usage)
		if pressed.shift {
			err = E.Errors(err, k.release(ctx, input, usageLeftShift))
		}
		return err
	}
	if _, repeated := k.pressed[id]; repeated {
		return nil
	}
	pressed, button, found := lookup()
	if !found {
		return nil
	}
	k.pressed[id] = pressed
	if button != nil {
		return input.PressButton(ctx, *button)
	}
	if pressed.shift {
		if err := k.press(ctx, input, usageLeftShift); err != nil {
			return err
		}
	}
	return k.press(ctx, input, pressed.usage)
}

// ScreensharingAgent treats Caps Lock key-down/up as on/off, rather than toggling on each press
// (ScreensharingAgent 756.36.5.2, macOS 27.0.1 26A434, UUID DA1CD9BA-97A1-3758-B425-00FA34B428A3,
// sub_1000350fc 0x1000350fc..0x100035128, 0x1000351a4..0x1000351ac, 0x100035298..0x1000352a4;
// ScreensharingAgent 766.8, macOS 27.2 26B5101f, UUID D8791A89-6EDB-3A0A-A720-EC3F5FB1AD4F,
// sub_100035484 0x100035484..0x1000354b0, 0x10003552c..0x100035534, 0x100035620..0x10003562c).
func (k *keyboard) capsLockKey(ctx context.Context, input *hid.Client, on bool) error {
	if on == k.capsLock {
		return nil
	}
	if err := input.KeyDown(ctx, usageCapsLock); err != nil {
		return err
	}
	k.capsLock = on
	return input.KeyUp(ctx, usageCapsLock)
}
