package display

import (
	"fmt"
	"net/netip"
	"time"
)

type StreamType string

const (
	Video StreamType = "video"
	Audio StreamType = "audio"
)

// In the iPad17,1 build 24B5089g reply, LocalSSRC is the
// RTP sender and RemoteSSRC is the offered receiver SSRC; SourcePort is
// also the port observed on the incoming RTP and RTCP packets.
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
	// (iPad17,1 build 24B5089g).
	SyncToken   uint64
	PixelFormat uint32
	RTCPTimeout time.Duration
}

func parseStartReply(output map[string]any) (StreamInfo, error) {
	connection := dictionary(output["connection"])
	config := dictionary(connection["streamConfig"])
	if config == nil {
		return StreamInfo{}, fmt.Errorf("start reply without connection.streamConfig: %v", output)
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
		return StreamInfo{}, fmt.Errorf("streamConfig without RemoteSSRC: %v", config)
	}
	if info.DeviceSSRC, ok = uint32Field(config, "LocalSSRC"); !ok {
		return StreamInfo{}, fmt.Errorf("streamConfig without LocalSSRC: %v", config)
	}
	info.DeviceAddress = addrPort(stringValue(config["SourceIP"]), intField(config, "SourcePort"))
	if !info.DeviceAddress.IsValid() {
		sender := dictionary(connection["sender"])
		info.DeviceAddress = addrPort(stringValue(sender["ipv6Address"]), intField(sender, "port"))
	}
	if !info.DeviceAddress.IsValid() {
		return StreamInfo{}, fmt.Errorf("streamConfig without the device's address: %v", config)
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
	var stopped []uint32
	for _, value := range array(output["stoppedStreams"]) {
		number, ok := uintValue(value)
		if ok && number <= 0xffffffff {
			stopped = append(stopped, uint32(number))
		}
	}
	return stopped
}
