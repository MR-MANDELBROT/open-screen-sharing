package media

// In macOS 26A434, SSSession processes remote video orientation changes
// only when isAppleIDAddress and connectingToIOSdevice are both true.
const (
	appleExtensionProfile = 0x9000
	appleExtensionMask    = 0xf000
	orientationShift      = 8
	orientationMask       = 0x3 << orientationShift
)

var orientationTurns = [4]int{
	0, // portrait
	2, // portrait upside down
	3, // landscape left
	1, // landscape right
}

var turnsOrientation = [4]uint16{0, 3, 1, 2}

const macTurnsOffset = 1

func pictureTurns(profile uint16) int {
	if profile&appleExtensionMask != appleExtensionProfile {
		return 0
	}
	orientation := (profile & orientationMask) >> orientationShift
	return (orientationTurns[orientation] - macTurnsOffset) & 3
}

func macExtensionProfile(profile uint16) uint16 {
	orientation := (profile & orientationMask) >> orientationShift
	turns := orientationTurns[orientation]
	return profile&^orientationMask | turnsOrientation[(turns+macTurnsOffset)&3]<<orientationShift
}
