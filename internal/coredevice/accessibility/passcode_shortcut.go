package accessibility

import (
	"math"
	"strings"
)

func NumericPasscodeShortcutPoint(layout PasscodeLayout, deviceClass string) (LayoutPoint, bool) {
	if (deviceClass != "iPhone" && deviceClass != "iPad") || layout.Status != "ok" || !layout.SpringBoard || !layout.FrontmostConsistent ||
		layout.HitCode == nil || *layout.HitCode != 0 || layout.Keyboard == nil || layout.Hit == nil || layout.HitPoint == nil {
		return LayoutPoint{}, false
	}
	// SBUIPasscodeLockNumberPad's inner pad starts at y=0 and maps the initial
	// touch to a key by its location in that pad (24A446 SpringBoardUIServices
	// 0x1a7de09bc..0x1a7de09cc, -[SBUIPasscodeLockNumberPad
	// _numberPadTouchDownForEvent:] 0x1a7de2850..0x1a7de2874).
	if layout.Keyboard.Class != "SBUIPasscodeLockNumberPad" {
		return LayoutPoint{}, false
	}
	app, pad, button := layout.AppFrame, layout.Keyboard.Frame, layout.Hit.Frame
	if deviceClass == "iPad" {
		return iPadShortcutPoint(layout, app, pad, button)
	}
	if layout.Hit.Class != "SBUIButton" {
		return LayoutPoint{}, false
	}
	if !validShortcutFrame(app) || !validShortcutFrame(pad) || !validShortcutFrame(button) {
		return LayoutPoint{}, false
	}
	if app.X != 0 || app.Y != 0 || app.Width < 232 || app.Height < 332 || app.Height <= app.Width {
		return LayoutPoint{}, false
	}
	if pad.X != 0 || pad.Width != app.Width || pad.Y+pad.Height > app.Height ||
		button.X+button.Width > app.Width || button.Y+button.Height > app.Height {
		return LayoutPoint{}, false
	}

	// 24A446 SpringBoardUIServices: SBUIPasscodeBiometricAuthenticationView
	// updateConstraints 0x1a7daea48..0x1a7daeb04 fixes this button at 232x332;
	// the guide at 0x1a7dae8ec..0x1a7dae970 centers it at (.5W, .48H).
	// iPhone18,2 / 24B5110a reports (104,293,232,332) for (440,956), versus
	// ideal y=292.88.
	const frameRounding = 1.0
	if button.Width != 232 || button.Height != 332 ||
		math.Abs(button.X-(app.Width/2-116)) > frameRounding ||
		math.Abs(button.Y-(app.Height*0.48-166)) > frameRounding {
		return LayoutPoint{}, false
	}
	point := *layout.HitPoint
	if !finiteLayoutNumber(point.X) || !finiteLayoutNumber(point.Y) ||
		point.X-button.X <= frameRounding || button.X+button.Width-point.X <= frameRounding ||
		point.Y-button.Y <= frameRounding || button.Y+button.Height-point.Y <= frameRounding || pad.Y-point.Y <= frameRounding {
		return LayoutPoint{}, false
	}
	normalized := LayoutPoint{X: point.X / app.Width, Y: point.Y / app.Height}
	if !finiteLayoutNumber(normalized.X) || !finiteLayoutNumber(normalized.Y) ||
		normalized.X <= 0 || normalized.X >= 1 || normalized.Y <= 0 || normalized.Y >= 1 {
		return LayoutPoint{}, false
	}
	return normalized, true
}

func validShortcutFrame(frame *LayoutFrame) bool {
	return frame != nil && finiteLayoutNumber(frame.X) && finiteLayoutNumber(frame.Y) &&
		finiteLayoutNumber(frame.Width) && finiteLayoutNumber(frame.Height) &&
		frame.X >= 0 && frame.Y >= 0 && frame.Width > 0 && frame.Height > 0 &&
		finiteLayoutNumber(frame.X+frame.Width) && finiteLayoutNumber(frame.Y+frame.Height)
}

// iPad AX frames and hit points use the portrait-native screen whatever the
// interface orientation; AppFrame is the rotated one. The button that spans the
// screen below the status area is another one and does nothing.
func iPadShortcutPoint(layout PasscodeLayout, app, pad, button *LayoutFrame) (LayoutPoint, bool) {
	if !validShortcutFrame(app) || !validShortcutFrame(pad) || !validShortcutFrame(button) {
		return LayoutPoint{}, false
	}
	width, height := min(app.Width, app.Height), max(app.Width, app.Height)
	if !strings.HasSuffix(layout.Hit.Class, "Button") || button.Width*button.Height > width*height/4 {
		return LayoutPoint{}, false
	}
	point := *layout.HitPoint
	if !finiteLayoutNumber(point.X) || !finiteLayoutNumber(point.Y) ||
		button.X+button.Width > width || button.Y+button.Height > height ||
		point.X <= button.X || point.X >= button.X+button.Width || point.Y <= button.Y || point.Y >= button.Y+button.Height {
		return LayoutPoint{}, false
	}
	return LayoutPoint{X: point.X / width, Y: point.Y / height}, true
}
