package rfb

import (
	"encoding/binary"
	"fmt"
)

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
