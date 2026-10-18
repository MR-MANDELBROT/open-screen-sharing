package hid

import (
	"context"
	"time"

	E "github.com/sagernet/sing/common/exceptions"
)

type Button struct {
	UsagePage uint16
	Usage     uint16
	Hold      time.Duration
}

// Button usages and hold times match idevice (idevice d32c818,
// tools/src/hid.rs:19-28).
var (
	ButtonHome       = Button{UsagePage: 0x0c, Usage: 0x40, Hold: 80 * time.Millisecond}
	ButtonLock       = Button{UsagePage: 0x0c, Usage: 0x30, Hold: 200 * time.Millisecond}
	ButtonVolumeUp   = Button{UsagePage: 0x0c, Usage: 0xe9, Hold: 80 * time.Millisecond}
	ButtonVolumeDown = Button{UsagePage: 0x0c, Usage: 0xea, Hold: 80 * time.Millisecond}
	ButtonMute       = Button{UsagePage: 0x0c, Usage: 0xe2, Hold: 80 * time.Millisecond}
)

var (
	ButtonBrightnessUp   = Button{UsagePage: 0x0c, Usage: 0x6f}
	ButtonBrightnessDown = Button{UsagePage: 0x0c, Usage: 0x70}
)

// idevice maps IndigoButtonEvent Down/Up to 1/2 (idevice d32c818,
// idevice/src/services/core_device/hid.rs:23-35, 556).
const (
	buttonDown = 1
	buttonUp   = 2
)

func (c *Client) PressButton(ctx context.Context, button Button) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.sendButton(c.ctx, button, buttonDown); err != nil {
		return E.Errors(err, ctx.Err())
	}
	timer := time.NewTimer(button.Hold)
	select {
	case <-timer.C:
	case <-ctx.Done():
		timer.Stop()
	}
	releaseErr := c.sendButton(c.ctx, button, buttonUp)
	return E.Errors(ctx.Err(), releaseErr)
}

func (c *Client) releaseButtons(ctx context.Context) error {
	for button := range c.buttons {
		if err := c.sendButton(ctx, button, buttonUp); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) sendButton(ctx context.Context, button Button, state uint64) error {
	err := c.sendIndigo(ctx, "IndigoButtonEvent", buttonFeature, map[string]any{
		"state":     state,
		"usagePage": uint64(button.UsagePage),
		"usageCode": uint64(button.Usage),
	})
	if err != nil {
		return err
	}
	if state == buttonDown {
		if c.buttons == nil {
			c.buttons = make(map[Button]struct{})
		}
		c.buttons[button] = struct{}{}
	} else {
		delete(c.buttons, button)
	}
	return nil
}
