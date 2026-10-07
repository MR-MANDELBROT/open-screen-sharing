package media

import (
	"context"
	"errors"
	"time"
)

type ServerVideoRequest struct {
	ClientSSRC     uint32
	Mode           VideoMode
	TilesPerFrame  int
	PixelFormats   PixelFormats
	MaximumBitrate int64
}

type ServerVideoInfo struct {
	SSRC          uint32
	ReceiverSSRC  uint32
	Width         int
	Height        int
	LTRP          bool
	FrameRate     int
	PixelFormat   PixelFormats
	TilesPerFrame int
}

type ServerVideoSource interface {
	Info() ServerVideoInfo
	ReadPacket(ctx context.Context, buffer []byte) (n int, rtcp bool, err error)
	WriteRTCP(packet []byte) error
	RequestKeyFrame() error
	Close() error
}

type ServerPacketArrival interface {
	PacketArrival() time.Time
}

type ServerVideoOpener func(ctx context.Context, request ServerVideoRequest) (ServerVideoSource, error)

var ErrAudioUnsupported = errors.New("media: the server sends no sound")

// AVConference maps payload 101 to 48 kHz and 480 samples per frame
// (macOS 26A434 and 26B5101f).
type ServerAudioInfo struct {
	SSRC         uint32
	ReceiverSSRC uint32
}

type ServerAudioSource interface {
	Info() ServerAudioInfo
	ReadPacket(ctx context.Context, buffer []byte) (n int, rtcp bool, err error)
	WriteRTCP(packet []byte) error
	Close() error
}

type ServerAudioOpener func(ctx context.Context) (ServerAudioSource, error)
