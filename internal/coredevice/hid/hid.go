// dtuhidd serves the RemoteXPC services com.apple.coredevice.hid.indigo,
// whose features include remote.hid.button and remote.hid.digitizer, and
// com.apple.coredevice.hid.universalhidservice, whose feature is
// remote.universalhidservice (DDI 27A9266e,
// /Library/LaunchDaemons/com.apple.coredevice.dtuhidd.plist).
package hid

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/nekohasekai/open-screen-sharing/internal/coredevice"
	"github.com/nekohasekai/open-screen-sharing/internal/coredevice/xpc"
	E "github.com/sagernet/sing/common/exceptions"
)

const (
	universalService = "com.apple.coredevice.hid.universalhidservice"
	indigoService    = "com.apple.coredevice.hid.indigo"

	universalFeature = "com.apple.coredevice.feature.remote.universalhidservice"
	buttonFeature    = "com.apple.coredevice.feature.remote.hid.button"
	digitizerFeature = "com.apple.coredevice.feature.remote.hid.digitizer"

	// idevice sends at most pointOne and pointTwo in an IndigoDigitizerEvent
	// (idevice d32c818, idevice/src/services/core_device/hid.rs:601-632).
	MaxTouches = 2
)

// idevice waits 300 ms after starting the display stream before sending
// HID input (idevice d32c818, tools/src/hid.rs:429-449).
const StreamWarmup = 300 * time.Millisecond

const defaultRequestTimeout = 10 * time.Second

type Touch struct {
	X, Y float64
}

// idevice maps IndigoDigitizerEvent edges None/Top/Left/Bottom/Right to
// 0/1/2/3/4 (idevice d32c818, idevice/src/services/core_device/hid.rs:56-73, 624).
type Edge uint64

const (
	EdgeNone   Edge = 0
	EdgeTop    Edge = 1
	EdgeLeft   Edge = 2
	EdgeBottom Edge = 3
	EdgeRight  Edge = 4
)

// idevice maps IndigoDigitizerEvent Start/Position/End to 0/1/2 (idevice
// d32c818, idevice/src/services/core_device/hid.rs:39-52, 623).
const (
	digitizerStart    = 0
	digitizerPosition = 1
	digitizerEnd      = 2
)

type Client struct {
	device *coredevice.Device
	logger *slog.Logger

	access    sync.Mutex
	universal *xpc.Conn
	indigo    *xpc.Conn
	touches   []Touch
	edge      Edge
	keyboard  uint64
	pressed   [keyboardBitmapSize]byte
	buttons   map[Button]struct{}
	closed    bool
}

func New(device *coredevice.Device, logger *slog.Logger) *Client {
	return &Client{device: device, logger: logger}
}

func (c *Client) SetTouches(ctx context.Context, touches []Touch, edge Edge) error {
	if len(touches) > MaxTouches {
		return E.New("hid: ", len(touches), " touches, at most ", MaxTouches)
	}
	c.access.Lock()
	defer c.access.Unlock()
	return c.setTouches(ctx, touches, edge)
}

func (c *Client) LiftTouches(ctx context.Context, touches []Touch) error {
	if len(touches) > MaxTouches {
		return E.New("hid: ", len(touches), " touches, at most ", MaxTouches)
	}
	c.access.Lock()
	defer c.access.Unlock()
	return c.liftTouches(ctx, touches)
}

func (c *Client) setTouches(ctx context.Context, touches []Touch, edge Edge) error {
	eventType := uint64(digitizerPosition)
	switch {
	case len(touches) == 0:
		return c.liftTouches(ctx, nil)
	case len(c.touches) == 0:
		eventType = digitizerStart
		c.edge = edge
	}
	return c.sendTouches(ctx, eventType, touches)
}

func (c *Client) liftTouches(ctx context.Context, touches []Touch) error {
	if len(c.touches) == 0 {
		return nil
	}
	if len(touches) == 0 {
		touches = c.touches
	}
	return c.sendTouches(ctx, digitizerEnd, touches)
}

func (c *Client) sendTouches(ctx context.Context, eventType uint64, touches []Touch) error {
	payload := map[string]any{
		"pointOne":  digitizerPoint(touches[0]),
		"eventType": eventType,
		"edge":      uint64(c.edge),
		"target":    uint64(0),
	}
	if len(touches) > 1 {
		payload["pointTwo"] = digitizerPoint(touches[1])
	}
	if err := c.sendIndigo(ctx, "IndigoDigitizerEvent", digitizerFeature, payload); err != nil {
		return err
	}
	if eventType == digitizerEnd {
		c.touches = nil
	} else {
		c.touches = append(c.touches[:0], touches...)
	}
	return nil
}

func digitizerPoint(touch Touch) map[string]any {
	return map[string]any{"x": touch.X, "y": touch.Y}
}

// idevice sends Indigo events without a reply flag (idevice d32c818,
// idevice/src/services/core_device/hid.rs:524-541).
func (c *Client) sendIndigo(ctx context.Context, messageType, feature string, payload map[string]any) error {
	if c.closed {
		return E.New("hid: send ", messageType, ": client closed")
	}
	if c.indigo == nil {
		conn, err := c.device.DialXPC(ctx, indigoService)
		if err != nil {
			return E.Cause(err, "hid")
		}
		c.indigo = conn
	}
	err := c.indigo.Send(map[string]any{
		"messageType":       messageType,
		"featureIdentifier": feature,
		"payload":           payload,
	}, false)
	if err != nil {
		c.indigo.Close()
		c.indigo = nil
		return E.Cause(err, "hid: send ", messageType)
	}
	return nil
}

func (c *Client) ReleaseAll(ctx context.Context) error {
	c.access.Lock()
	defer c.access.Unlock()
	return c.releaseAll(ctx)
}

func (c *Client) releaseAll(ctx context.Context) error {
	return E.Errors(c.setTouches(ctx, nil, EdgeNone), c.releaseKeys(ctx), c.releaseButtons(ctx))
}

func (c *Client) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	c.access.Lock()
	defer c.access.Unlock()
	if c.closed {
		return nil
	}
	if err := c.releaseAll(ctx); err != nil {
		c.logger.Debug("release input on close", "error", err)
	}
	if err := c.removeKeyboard(); err != nil {
		c.logger.Debug("remove keyboard on close", "error", err)
	}
	c.closed = true
	c.closeUniversal()
	if c.indigo != nil {
		c.indigo.Close()
		c.indigo = nil
	}
	return nil
}

func (c *Client) sendReport(ctx context.Context, serviceID uint64, report []byte) error {
	conn, err := c.universalConn(ctx)
	if err != nil {
		return err
	}
	err = conn.Send(universalRequest(map[string]any{
		"send": map[string]any{"_0": report, "_1": serviceID},
	}), false)
	if err != nil {
		c.closeUniversal()
		return E.Cause(err, "hid: send report")
	}
	return nil
}

// The UniversalHID request envelope matches idevice (idevice d32c818,
// idevice/src/services/core_device/hid.rs:259-267).
func universalRequest(payload map[string]any) map[string]any {
	return map[string]any{
		"featureIdentifier": universalFeature,
		"messageType":       "Request",
		"payload":           payload,
	}
}

func (c *Client) universalConn(ctx context.Context) (*xpc.Conn, error) {
	if c.closed {
		return nil, E.New("hid: client closed")
	}
	if c.universal == nil {
		conn, err := c.device.DialXPC(ctx, universalService)
		if err != nil {
			return nil, E.Cause(err, "hid")
		}
		c.universal = conn
	}
	return c.universal, nil
}

// dtuhidd keeps the services created on a universalhidservice connection in a
// pool of that connection, and when the connection closes it resets their
// gestures and cancels them (dtuhidd 651.13.2, DDI 27A9266e, UUID
// C35A167F-1D19-3B5F-AAB9-687431EFC792, sub_10002fd74 0x10002fe7c..0x10002fe98,
// sub_1000129f4 0x1000129f4..0x100012a18, sub_100027f9c 0x1000281ac..0x100028250,
// sub_10003131c 0x10003131c..0x100031780).
func (c *Client) closeUniversal() {
	if c.universal == nil {
		return
	}
	c.universal.Close()
	c.universal = nil
	c.keyboard = 0
	c.pressed = [keyboardBitmapSize]byte{}
}

func uintValue(value any) (uint64, bool) {
	switch value := value.(type) {
	case uint64:
		return value, true
	case int64:
		return uint64(value), value >= 0
	}
	return 0, false
}
