package rfb

import (
	"encoding/binary"
	"errors"
	"fmt"
)

type Framebuffer struct {
	Pix    []byte
	Stride int
	Width  int
	Height int
}

func NewFramebuffer(width, height int) *Framebuffer {
	return &Framebuffer{
		Pix:    make([]byte, width*height*4),
		Stride: width * 4,
		Width:  width,
		Height: height,
	}
}

func (f *Framebuffer) validate() error {
	if f.Width <= 0 || f.Height <= 0 {
		return fmt.Errorf("framebuffer size %dx%d", f.Width, f.Height)
	}
	if f.Stride < f.Width*4 {
		return fmt.Errorf("framebuffer stride %d is below %d", f.Stride, f.Width*4)
	}
	if len(f.Pix) < f.Stride*(f.Height-1)+f.Width*4 {
		return errors.New("framebuffer memory is smaller than its geometry")
	}
	return nil
}

func (f *Framebuffer) clip(x, y, width, height int) (int, int, int, int) {
	right := min(x+width, f.Width)
	bottom := min(y+height, f.Height)
	x = max(x, 0)
	y = max(y, 0)
	return x, y, max(right-x, 0), max(bottom-y, 0)
}

func (f *Framebuffer) contains(x, y, width, height int) bool {
	return x >= 0 && y >= 0 && width >= 0 && height >= 0 && x+width <= f.Width && y+height <= f.Height
}

func (f *Framebuffer) fill(x, y, width, height int, pixel uint32) {
	x, y, width, height = f.clip(x, y, width, height)
	if width == 0 || height == 0 {
		return
	}
	row := f.Pix[y*f.Stride+x*4 : y*f.Stride+x*4+width*4]
	for i := 0; i < width; i++ {
		binary.LittleEndian.PutUint32(row[i*4:], pixel)
	}
	for line := 1; line < height; line++ {
		offset := (y+line)*f.Stride + x*4
		copy(f.Pix[offset:offset+width*4], row)
	}
}

func (f *Framebuffer) copyRectangle(sourceX, sourceY, x, y, width, height int) {
	if !f.contains(sourceX, sourceY, width, height) || !f.contains(x, y, width, height) {
		return
	}
	if sourceY < y {
		for line := height - 1; line >= 0; line-- {
			source := (sourceY+line)*f.Stride + sourceX*4
			target := (y+line)*f.Stride + x*4
			copy(f.Pix[target:target+width*4], f.Pix[source:source+width*4])
		}
		return
	}
	for line := 0; line < height; line++ {
		source := (sourceY+line)*f.Stride + sourceX*4
		target := (y+line)*f.Stride + x*4
		copy(f.Pix[target:target+width*4], f.Pix[source:source+width*4])
	}
}

type PixelFormat struct {
	BitsPerPixel uint8
	Depth        uint8
	BigEndian    bool
	TrueColor    bool
	RedMax       uint16
	GreenMax     uint16
	BlueMax      uint16
	RedShift     uint8
	GreenShift   uint8
	BlueShift    uint8
}

var pixelFormatBGRA = PixelFormat{
	BitsPerPixel: 32,
	Depth:        24,
	TrueColor:    true,
	RedMax:       255,
	GreenMax:     255,
	BlueMax:      255,
	RedShift:     16,
	GreenShift:   8,
	BlueShift:    0,
}

func parsePixelFormat(data []byte) PixelFormat {
	return PixelFormat{
		BitsPerPixel: data[0],
		Depth:        data[1],
		BigEndian:    data[2] != 0,
		TrueColor:    data[3] != 0,
		RedMax:       binary.BigEndian.Uint16(data[4:]),
		GreenMax:     binary.BigEndian.Uint16(data[6:]),
		BlueMax:      binary.BigEndian.Uint16(data[8:]),
		RedShift:     data[10],
		GreenShift:   data[11],
		BlueShift:    data[12],
	}
}

func (p PixelFormat) encode(data []byte) {
	data[0] = p.BitsPerPixel
	data[1] = p.Depth
	data[2] = boolByte(p.BigEndian)
	data[3] = boolByte(p.TrueColor)
	binary.BigEndian.PutUint16(data[4:], p.RedMax)
	binary.BigEndian.PutUint16(data[6:], p.GreenMax)
	binary.BigEndian.PutUint16(data[8:], p.BlueMax)
	data[10] = p.RedShift
	data[11] = p.GreenShift
	data[12] = p.BlueShift
	data[13], data[14], data[15] = 0, 0, 0
}

func (p PixelFormat) String() string {
	return fmt.Sprintf("%dbpp depth %d big-endian=%t true-color=%t max %d/%d/%d shift %d/%d/%d",
		p.BitsPerPixel, p.Depth, p.BigEndian, p.TrueColor, p.RedMax, p.GreenMax, p.BlueMax,
		p.RedShift, p.GreenShift, p.BlueShift)
}

func boolByte(value bool) byte {
	if value {
		return 1
	}
	return 0
}
