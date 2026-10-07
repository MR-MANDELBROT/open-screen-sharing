package rfb

import (
	"errors"
	"slices"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb/media"
)

type virtualDisplayRequest struct {
	width         int
	height        int
	density       float64
	maximumWidth  int
	maximumHeight int
}

// Without an explicit refresh-rate override, Apple's viewer asks
// GetHEVCDecoderMaxSupportedFrameRate and falls back to 60 Hz on failure.
// Rechecked in macOS 27.0.1 (26A434), ScreenSharing.framework arm64e UUID
// EC0B2C20-F816-3FCE-822D-F9403C09EB50,
// -[SSSession stConfigureVirtualDisplaysWithDimensions:], 0x1cc0d9888-0x1cc0d997c.
func (c *Client) displayConfiguration(request virtualDisplayRequest, hdr bool) ([]byte, error) {
	refreshRate := 60.0
	rate := c.options.DecoderMaxPixelRate
	pixelWidth := int(float64(request.width) * request.density)
	pixelHeight := int(float64(request.height) * request.density)
	if c.options.MaximumFrameRate == 30 || rate > 0 && !media.Supports60FPS(rate, pixelWidth, pixelHeight) {
		refreshRate = 30
	}
	return virtualDisplayMessage(request.width, request.height, request.density, request.maximumWidth, request.maximumHeight, refreshRate, hdr)
}

func (c *Client) SetVirtualDisplay(width, height int, density float64, maximumWidth, maximumHeight int) error {
	request := virtualDisplayRequest{width: width, height: height, density: density, maximumWidth: maximumWidth, maximumHeight: maximumHeight}
	c.access.Lock()
	hdr := c.hdr
	c.access.Unlock()
	message, err := c.displayConfiguration(request, hdr)
	if err != nil {
		return err
	}
	c.access.Lock()
	defer c.access.Unlock()
	c.virtualDisplay = &request
	return c.sendDisplayChangeLocked(message, false)
}

func (c *Client) SetHDR(enabled bool) error {
	if !c.info.Accepts(0x1d) {
		return errors.New("HDR needs a Mac that offers virtual displays")
	}
	c.access.Lock()
	changed := c.hdr != enabled
	c.hdr = enabled
	request := c.virtualDisplay
	c.access.Unlock()
	if !changed || request == nil {
		return nil
	}
	message, err := c.displayConfiguration(*request, enabled)
	if err != nil {
		return err
	}
	c.access.Lock()
	defer c.access.Unlock()
	return c.sendDisplayChangeLocked(message, false)
}

type heldDisplayChange struct {
	selection bool
	message   []byte
}

func (c *Client) holdsDisplayChangesLocked() bool {
	return c.preparing || c.media != nil && (c.media.starting || c.media.unanswered)
}

func (c *Client) sendDisplayChangeLocked(message []byte, selection bool) error {
	c.heldDisplay = slices.DeleteFunc(c.heldDisplay, func(held heldDisplayChange) bool {
		return held.selection == selection
	})
	if c.holdsDisplayChangesLocked() {
		c.heldDisplay = append(c.heldDisplay, heldDisplayChange{selection: selection, message: message})
		return nil
	}
	return c.send(message)
}

func (c *Client) releaseDisplayChangesLocked() error {
	held := c.heldDisplay
	c.heldDisplay = nil
	for _, change := range held {
		err := c.send(change.message)
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) HDR() bool {
	c.access.Lock()
	defer c.access.Unlock()
	return c.hdr
}
