package rfb

import (
	"fmt"
)

type Cursor struct {
	Width    int
	Height   int
	HotspotX int
	HotspotY int
	Pixels   []byte
	CacheID  uint32
}

const maximumCursorSize = 1024

// ScreenSharing 776.27 arm64e consumes only the ID for values <= 999; larger IDs
// carry a length, with zero reusing cached pixels. The inflated alpha plane follows
// the pixel plane. Evidence: HandleCursorImageWithAlphaUpdate at 0x1cc06c804,
// 0x1cc06cd84..0x1cc06cde4 and 0x1cc06cf44..0x1cc06cf70,
// UUID EC0B2C20-F816-3FCE-822D-F9403C09EB50.
func (c *Client) receiveCursorImage(rectangle Rectangle) error {
	id, err := c.stream.u32()
	if err != nil {
		return err
	}
	if id <= 999 {
		c.logger.Debug("system cursor", "id", id)
		return nil
	}
	length, err := c.stream.u32()
	if err != nil {
		return err
	}
	if length > 1<<20 {
		return fmt.Errorf("cursor image of %d bytes", length)
	}
	if length == 0 {
		cursor, found := c.cursors[id]
		if found {
			c.handler.CursorChanged(cursor)
		} else {
			c.logger.Debug("cursor select for an unknown id", "id", id)
		}
		return nil
	}
	compressed, err := c.stream.bytes(int(length))
	if err != nil {
		return err
	}
	width, height := rectangle.Width, rectangle.Height
	if width == 0 || height == 0 || width > maximumCursorSize || height > maximumCursorSize {
		c.logger.Warn("cursor image of unusable size", "width", width, "height", height)
		return nil
	}
	pixels := width * height
	raw, err := inflateIndependent(compressed, pixels*5)
	if err != nil || len(raw) != pixels*5 {
		c.logger.Warn("cursor image does not inflate", "id", id, "error", err, "bytes", len(raw))
		return nil
	}
	image := make([]byte, pixels*4)
	alpha := raw[pixels*4:]
	for i := 0; i < pixels; i++ {
		copy(image[i*4:i*4+3], raw[i*4:i*4+3])
		image[i*4+3] = alpha[i]
	}
	cursor := &Cursor{
		Width:    width,
		Height:   height,
		HotspotX: rectangle.X,
		HotspotY: rectangle.Y,
		Pixels:   image,
		CacheID:  id,
	}
	c.cursors[id] = cursor
	c.handler.CursorChanged(cursor)
	return nil
}

func (c *Client) receiveRichCursor(rectangle Rectangle) error {
	width, height := rectangle.Width, rectangle.Height
	pixels, err := c.stream.bytes(width * height * 4)
	if err != nil {
		return err
	}
	rowBytes := (width + 7) / 8
	mask, err := c.stream.bytes(rowBytes * height)
	if err != nil {
		return err
	}
	image := make([]byte, width*height*4)
	for line := 0; line < height; line++ {
		for column := 0; column < width; column++ {
			if mask[line*rowBytes+column/8]&(0x80>>(column%8)) == 0 {
				continue
			}
			offset := (line*width + column) * 4
			copy(image[offset:offset+3], pixels[offset:offset+3])
			image[offset+3] = 0xff
		}
	}
	c.handler.CursorChanged(&Cursor{
		Width:    width,
		Height:   height,
		HotspotX: rectangle.X,
		HotspotY: rectangle.Y,
		Pixels:   image,
	})
	return nil
}
