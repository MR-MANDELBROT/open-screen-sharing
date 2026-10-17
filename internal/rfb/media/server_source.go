package media

import (
	"context"
	"time"

	E "github.com/sagernet/sing/common/exceptions"
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
	PacketArrival() time.Time
	WriteRTCP(packet []byte) error
	RequestKeyFrame() error
	Close() error
}

type ServerVideoOpener func(ctx context.Context, request ServerVideoRequest) (ServerVideoSource, error)

var ErrAudioUnsupported = E.New("media: the server sends no sound")

// AVConference maps payload 101 to 48 kHz and 480 samples per frame
// (AVConference 2235.63.5.2, macOS 27.0.1 26A434, UUID
// 93D8C8C0-8B9C-3A5E-A47D-01B5FAF73E1B,
// +[VCPayloadUtils sampleRateForPayload:] 0x1c0eb35c4..0x1c0eb3644,
// +[VCPayloadUtils codecSamplesPerFrameForPayload:blockSize:sampleRate:] 0x1c0eb387c..0x1c0eb39ac;
// AVConference 2260.14.1, macOS 27.2 26B5101f, UUID
// EDFB99EF-2F5E-35DD-8E63-089CBD102EFE,
// +[VCPayloadUtils sampleRateForPayload:] 0x1c052dca4..0x1c052dd24,
// +[VCPayloadUtils codecSamplesPerFrameForPayload:blockSize:sampleRate:] 0x1c052df5c..0x1c052e08c).
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

type ServerAudioOpener func() (ServerAudioSource, error)
