package media

// SSSession handles a remote video attributes change, including its
// orientation, only when the address isAppleIDAddress and the connection
// options have connectingToIOSdevice; otherwise it returns at once
// (ScreenSharing 776.27, macOS 27.0.1 26A434, UUID
// EC0B2C20-F816-3FCE-822D-F9403C09EB50,
// -[SSSession remoteVideoClient:remoteVideoAttributesDidChange:] 0x1cc0de894..0x1cc0de8b0;
// ScreenSharing 786.3, macOS 27.2 26B5101f, UUID
// 21DF7A41-0D38-3DF3-B87A-8A6249686735,
// -[SSSession remoteVideoClient:remoteVideoAttributesDidChange:] 0x22e521914..0x22e521930).
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
