// DDI 27A9266e ships dtuhidd with the RemoteXPC services
// com.apple.coredevice.hid.indigo and universalhidservice; its launchd
// manifest advertises button/digitizer and universal-HID features.
package hid

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/nekohasekai/open-screen-sharing/internal/coredevice"
	"github.com/nekohasekai/open-screen-sharing/internal/coredevice/xpc"
)

const (
	universalService = "com.apple.coredevice.hid.universalhidservice"
	indigoService    = "com.apple.coredevice.hid.indigo"

	universalFeature = "com.apple.coredevice.feature.remote.universalhidservice"
	buttonFeature    = "com.apple.coredevice.feature.remote.hid.button"
	digitizerFeature = "com.apple.coredevice.feature.remote.hid.digitizer"

	// idevice d32c818 sends at most pointOne and pointTwo in an
	// IndigoDigitizerEvent (idevice/src/services/core_device/hid.rs:601-633).
	MaxTouches = 2
)

// idevice d32c818 waits 300 ms after starting the display stream before
// sending HID input (tools/src/hid.rs:429-449).
const StreamWarmup = 300 * time.Millisecond

const defaultRequestTimeout = 10 * time.Second

type Touch struct {
	X, Y float64
}

// idevice d32c818 maps IndigoDigitizerEvent edges None/Top/Left/Bottom/Right
// to 0/1/2/3/4 (idevice/src/services/core_device/hid.rs:56-73).
type Edge uint64

const (
	EdgeNone   Edge = 0
	EdgeTop    Edge = 1
	EdgeLeft   Edge = 2
	EdgeBottom Edge = 3
	EdgeRight  Edge = 4
)

// idevice d32c818 maps IndigoDigitizerEvent Start/Position/End to 0/1/2
// (idevice/src/services/core_device/hid.rs:39-52).
const (
	digitizerStart    = 0
	digitizerPosition = 1
	digitizerEnd      = 2
)

type Client struct {
	device *coredevice.Device
	logger *slog.Logger

	access        sync.Mutex
	universal     *xpc.Conn
	indigo        *xpc.Conn
	touches       []Touch
	edge          Edge
	keyboard      uint64
	pressed       [keyboardBitmapSize]byte
	staleKeyboard uint64
	buttons       map[Button]struct{}
	closed        bool
}

var errClosed = errors.New("hid: client closed")

func New(device *coredevice.Device, logger *slog.Logger) *Client {
	return &Client{device: device, logger: logger}
}

func (c *Client) SetTouches(ctx context.Context, touches []Touch, edge Edge) error {
	if len(touches) > MaxTouches {
		return fmt.Errorf("hid: %d touches, at most %d", len(touches), MaxTouches)
	}
	c.access.Lock()
	defer c.access.Unlock()
	return c.setTouches(ctx, touches, edge)
}

func (c *Client) LiftTouches(ctx context.Context, touches []Touch) error {
	if len(touches) > MaxTouches {
		return fmt.Errorf("hid: %d touches, at most %d", len(touches), MaxTouches)
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

// idevice d32c818 sends Indigo events without a reply flag
// (idevice/src/services/core_device/hid.rs:525-543).
func (c *Client) sendIndigo(ctx context.Context, messageType, feature string, payload map[string]any) error {
	if c.closed {
		return errClosed
	}
	if c.indigo == nil {
		conn, err := c.device.DialXPC(ctx, indigoService)
		if err != nil {
			return fmt.Errorf("hid: %w", err)
		}
		c.indigo = conn
	}
	err := c.indigo.Send(map[string]any{
		"messageType":       messageType,
		"isBarrier":         false,
		"featureIdentifier": feature,
		"payload":           payload,
	}, false)
	if err != nil {
		c.indigo.Close()
		c.indigo = nil
		return fmt.Errorf("hid: send %s: %w", messageType, err)
	}
	return nil
}

func (c *Client) ReleaseAll(ctx context.Context) error {
	c.access.Lock()
	defer c.access.Unlock()
	return c.releaseAll(ctx)
}

func (c *Client) releaseAll(ctx context.Context) error {
	return errors.Join(c.setTouches(ctx, nil, EdgeNone), c.releaseKeys(ctx), c.releaseButtons(ctx))
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
	if err := c.removeKeyboard(ctx); err != nil {
		c.logger.Debug("remove keyboard on close", "error", err)
	}
	c.closed = true
	c.closeUniversal()
	c.staleKeyboard = 0
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
		return fmt.Errorf("hid: send report: %w", err)
	}
	return nil
}

// The UniversalHID request envelope matches idevice d32c818,
// idevice/src/services/core_device/hid.rs:259-267.
func universalRequest(payload map[string]any) map[string]any {
	return map[string]any{
		"featureIdentifier": universalFeature,
		"messageType":       "Request",
		"payload":           payload,
	}
}

func (c *Client) universalConn(ctx context.Context) (*xpc.Conn, error) {
	if c.closed {
		return nil, errClosed
	}
	if c.universal == nil {
		conn, err := c.device.DialXPC(ctx, universalService)
		if err != nil {
			return nil, fmt.Errorf("hid: %w", err)
		}
		if c.staleKeyboard != 0 {
			if err = removeService(conn, c.staleKeyboard); err != nil {
				conn.Close()
				return nil, fmt.Errorf("hid: remove keyboard of the last connection: %w", err)
			}
			c.logger.Debug("removed keyboard of the last connection", "service", c.staleKeyboard)
			c.staleKeyboard = 0
		}
		c.universal = conn
	}
	return c.universal, nil
}

func (c *Client) closeUniversal() {
	if c.universal == nil {
		return
	}
	c.universal.Close()
	c.universal = nil
	if c.keyboard != 0 {
		c.staleKeyboard = c.keyboard
	}
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
