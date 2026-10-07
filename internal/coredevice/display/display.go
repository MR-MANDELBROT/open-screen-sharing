// On iPad17,1 build 24B5089g, the device sent unencrypted video RTP and
// RTCP from one UDP port. Starting a second stream of display 1 stopped
// the first stream after its final frame at 3.09 s.
package display

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math/rand/v2"
	"time"

	"github.com/google/uuid"
	"github.com/nekohasekai/open-screen-sharing/internal/coredevice"
)

const (
	serviceName = "com.apple.coredevice.displayservice"

	featureStart = "com.apple.coredevice.feature.startmediastream"
	actionStart  = "com.apple.coredevice.action.mediastreamstart"
	featureStop  = "com.apple.coredevice.feature.stopmediastream"
	actionStop   = "com.apple.coredevice.action.mediastreamstop"

	// The CoreDevice request fields match pymobiledevice3 1146717,
	// remote/core_device/display_service.py:40-45, 141-164.
	clientSupportedFeatures = uint64(140)
	accessNetworkType       = int64(1)
	transportProtocolType   = int64(2)

	// Display ID 1 produced the main-screen stream on iPad17,1
	// build 24B5089g.
	DefaultDisplayID = 1
	rtcpTimeout      = 30 * time.Second
	// The iPad17,1 build 24B5089g probe received its first IDR at 90.5 ms;
	// display ID 2 produced RTCP but no video RTP during its probe.
	firstPacketTimeout = 2 * time.Second

	stopTimeout = 5 * time.Second
)

var ErrNoMedia = errors.New("the device accepted the stream but sent no media; check that Xcode's Device Hub is not showing the device")

type Client struct {
	device           *coredevice.Device
	logger           *slog.Logger
	deviceIdentifier string
	turn             chan struct{}
}

func New(device *coredevice.Device, logger *slog.Logger) *Client {
	return &Client{
		device:           device,
		logger:           logger,
		deviceIdentifier: uuid.NewString(),
		turn:             make(chan struct{}, 1),
	}
}

func (c *Client) takeTurn(ctx context.Context) error {
	select {
	case c.turn <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) endTurn() {
	<-c.turn
}

func (c *Client) invoke(ctx context.Context, feature, action string, input map[string]any) (map[string]any, error) {
	err := c.takeTurn(ctx)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", feature, err)
	}
	defer c.endTurn()
	conn, err := c.device.DialXPC(ctx, serviceName)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return invoke(ctx, conn, c.deviceIdentifier, feature, action, input)
}

// The device returns stoppedStreams in the client-offered SSRC namespace
// (iPad17,1, iOS 27.2 build 24B5099f).
func (c *Client) stop(ctx context.Context, ssrc uint32) ([]uint32, error) {
	output, err := c.invoke(ctx, featureStop, actionStop, map[string]any{
		"stopAll":     false,
		"identifiers": []any{uint64(ssrc)},
	})
	if err != nil {
		return nil, err
	}
	return parseStopReply(output), nil
}

type StreamOptions struct {
	// The iPad17,1 build 24B5089g probe started audio then video with one
	// session UUID. Video returned a nonzero SyncStreamToken; the video-only
	// probe returned zero.
	SessionID uuid.UUID
	// The device echoes the offered SSRC as RemoteSSRC (iPad17,1 build
	// 24B5089g); the probe uses it as the RTCP sender SSRC.
	SSRC           uint32
	Mode           Mode
	MaximumBitrate uint64
	TilesPerFrame  int
	PixelFormats   uint32
}

func (o StreamOptions) videoMode() Mode {
	if o.Mode == 0 {
		return ModeRemoteDesktop
	}
	return o.Mode
}

func (c *Client) StartVideo(ctx context.Context, options StreamOptions) (*Stream, error) {
	return c.start(ctx, Video, options, map[string]any{
		"CoreDeviceVideoDisplayMode": map[string]any{"string": "DisplayByID"},
		"VideoStreamForDisplayID":    map[string]any{"int": int64(DefaultDisplayID)},
	})
}

func (c *Client) StartAudio(ctx context.Context, options StreamOptions) (*Stream, error) {
	return c.start(ctx, Audio, options, nil)
}

func startInput(streamType StreamType, sessionID uuid.UUID, offer []byte, receiverIP string, receiverPort int, senderIP string, extraOptions map[string]any) map[string]any {
	streamOptions := map[string]any{
		"AVCMediaStreamNegotiatorAccessNetworkType":     map[string]any{"int": accessNetworkType},
		"AVCMediaStreamNegotiatorTransportProtocolType": map[string]any{"int": transportProtocolType},
		"avcMediaStreamOptionClientSessionID":           map[string]any{"uuid": sessionID},
	}
	maps.Copy(streamOptions, extraOptions)
	return map[string]any{
		"clientSupportedFeatures": clientSupportedFeatures,
		"direction":               "output",
		"negotiatorOffer":         offer,
		"options":                 streamOptions,
		"receiverIP":              receiverIP,
		"receiverPort":            uint64(receiverPort),
		"senderIP":                senderIP,
		// The device answers with this value as RTCPTimeoutInterval: a probe
		// sent 3600 and got 3600 back (iPad17,1 build 24B5089g).
		"timeout": uint64(rtcpTimeout / time.Second),
		"type":    string(streamType),
	}
}

func (c *Client) start(ctx context.Context, streamType StreamType, options StreamOptions, extraOptions map[string]any) (*Stream, error) {
	for options.SSRC == 0 {
		options.SSRC = rand.Uint32()
	}
	conn, err := c.device.ListenUDP(0)
	if err != nil {
		return nil, fmt.Errorf("start %s: bind receiver: %w", streamType, err)
	}
	input := startInput(streamType, options.SessionID, encodeOffer(streamType, options, time.Now()),
		c.device.LocalAddress().String(), int(conn.Port()),
		c.device.Address().String(), extraOptions)

	err = c.takeTurn(ctx)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("start %s: %w", streamType, err)
	}
	began := time.Now()
	control, err := c.device.DialXPC(ctx, serviceName)
	if err != nil {
		c.endTurn()
		conn.Close()
		return nil, fmt.Errorf("start %s: %w", streamType, err)
	}
	connected := time.Now()
	output, err := invoke(ctx, control, c.deviceIdentifier, featureStart, actionStart, input)
	c.endTurn()
	answered := time.Now()
	if err != nil {
		control.Close()
		conn.Close()
		var deviceError *Error
		if !errors.As(err, &deviceError) {
			c.stopOffered(ctx, options.SSRC)
		}
		return nil, fmt.Errorf("start %s: %w", streamType, err)
	}
	info, err := parseStartReply(output)
	if err != nil {
		control.Close()
		conn.Close()
		c.stopOffered(ctx, options.SSRC)
		return nil, fmt.Errorf("start %s: %w", streamType, err)
	}
	stream := newStream(c, info, conn, control)
	c.logger.Debug("display stream started",
		"type", info.Type, "ssrc", info.SSRC, "device_ssrc", info.DeviceSSRC,
		"device", info.DeviceAddress, "width", info.Width, "height", info.Height,
		"tiles", info.TilesPerFrame, "ltrp", info.LTRP, "sync_token", info.SyncToken, "rtcp_timeout", info.RTCPTimeout,
		"connect_time", connected.Sub(began), "reply_time", answered.Sub(connected))
	if streamType == Video {
		err = stream.waitFirstPacket(ctx)
		if err != nil {
			stream.Close()
			return nil, fmt.Errorf("start %s: %w", streamType, err)
		}
	}
	stream.startKeepalive()
	return stream, nil
}

func (c *Client) stopOffered(ctx context.Context, ssrc uint32) {
	stopContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), stopTimeout)
	defer cancel()
	_, err := c.stop(stopContext, ssrc)
	if err != nil {
		c.logger.Debug("stop after a failed start", "ssrc", ssrc, "error", err)
	}
}
