package display

import (
	"fmt"
	"math"
	"net/netip"
	"time"

	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
)

type StreamType string

const (
	Video StreamType = "video"
	Audio StreamType = "audio"
)

// In the start reply, LocalSSRC is the RTP sender and RemoteSSRC is the
// offered receiver SSRC; SourcePort is also the port observed on the
// incoming RTP and RTCP packets (observed on iPad17,1, build 24B5089g).
type StreamInfo struct {
	Type          StreamType
	SSRC          uint32
	DeviceSSRC    uint32
	DeviceAddress netip.AddrPort
	Width, Height int
	FrameRate     int
	TilesPerFrame int
	LTRP          bool
	// The shared-session audio/video probe returned a nonzero
	// SyncStreamToken on video; the video-only probe returned zero
	// (observed on iPad17,1, build 24B5089g).
	SyncToken   uint64
	PixelFormat uint32
	RTCPTimeout time.Duration
}

func parseStartReply(output map[string]any) (StreamInfo, error) {
	connection := dictionary(output["connection"])
	config := dictionary(connection["streamConfig"])
	if config == nil {
		return StreamInfo{}, E.New("display: start reply without connection.streamConfig: ", fmt.Sprint(output))
	}
	info := StreamInfo{
		Type:          StreamType(stringValue(connection["type"])),
		Width:         intField(config, "CustomWidth"),
		Height:        intField(config, "CustomHeight"),
		FrameRate:     intField(config, "Framerate"),
		TilesPerFrame: max(intField(config, "TilesPerFrame"), 1),
	}
	var ok bool
	if info.SSRC, ok = uint32Field(config, "RemoteSSRC"); !ok {
		return StreamInfo{}, E.New("display: streamConfig without RemoteSSRC: ", fmt.Sprint(config))
	}
	if info.DeviceSSRC, ok = uint32Field(config, "LocalSSRC"); !ok {
		return StreamInfo{}, E.New("display: streamConfig without LocalSSRC: ", fmt.Sprint(config))
	}
	info.DeviceAddress = addrPort(stringValue(config["SourceIP"]), intField(config, "SourcePort"))
	if !info.DeviceAddress.IsValid() {
		sender := dictionary(connection["sender"])
		info.DeviceAddress = addrPort(stringValue(sender["ipv6Address"]), intField(sender, "port"))
	}
	if !info.DeviceAddress.IsValid() {
		return StreamInfo{}, E.New("display: streamConfig without the device's address: ", fmt.Sprint(config))
	}
	info.LTRP, _ = boolValue(config["IsltrpEnabled"])
	info.SyncToken, _ = uintValue(config["SyncStreamToken"])
	info.PixelFormat, _ = uint32Field(config, "PixelFormat")
	if seconds, ok := floatValue(config["RTCPTimeoutInterval"]); ok {
		info.RTCPTimeout = time.Duration(seconds * float64(time.Second))
	}
	return info, nil
}

func parseStopReply(output map[string]any) []uint32 {
	return common.FilterIsInstance(array(output["stoppedStreams"]), func(value any) (uint32, bool) {
		number, ok := uintValue(value)
		return uint32(number), ok && number <= math.MaxUint32
	})
}
