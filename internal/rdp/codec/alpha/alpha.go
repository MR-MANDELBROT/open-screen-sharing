package alpha

import (
	"encoding/binary"
	"strconv"

	"github.com/nekohasekai/open-screen-sharing/internal/bgra"

	E "github.com/sagernet/sing/common/exceptions"
)

const (
	signature   = 0x414C
	headerSize  = 4
	runLength16 = 0xFF
	runLength32 = 0xFFFF
)

func Decode(src []byte, width, height int, dst *bgra.Image) error {
	if len(src) < headerSize {
		return E.New("alpha: truncated header: ", len(src), " of 4 bytes")
	}
	if value := binary.LittleEndian.Uint16(src); value != signature {
		return E.New("alpha: invalid signature 0x", strconv.FormatUint(uint64(value), 16))
	}
	compressed := binary.LittleEndian.Uint16(src[2:]) != 0
	data := src[headerSize:]
	outWidth, outHeight := min(width, dst.Width), min(height, dst.Height)

	if !compressed {
		if len(data)/width < height {
			return E.New("alpha: uncompressed ", width, "x", height, " plane of ", len(data), " bytes")
		}
		if outWidth <= 0 {
			return nil
		}
		for y := range outHeight {
			row := data[y*width:][:outWidth]
			out := dst.Pix[y*dst.Stride:][:outWidth*4]
			for x, value := range row {
				out[x*4+3] = value
			}
		}
		return nil
	}

	x, y := 0, 0
	for y < height {
		if len(data) < 2 {
			return E.New("alpha: truncated run at ", x, ",", y)
		}
		value, count := data[0], int(data[1])
		data = data[2:]
		if count == runLength16 {
			if len(data) < 2 {
				return E.New("alpha: truncated 16-bit run length at ", x, ",", y)
			}
			count = int(binary.LittleEndian.Uint16(data))
			data = data[2:]
			if count == runLength32 {
				if len(data) < 4 {
					return E.New("alpha: truncated 32-bit run length at ", x, ",", y)
				}
				count = int(binary.LittleEndian.Uint32(data))
				data = data[4:]
			}
		}
		for count > 0 && y < height {
			n := min(count, width-x)
			if y < outHeight && x < outWidth {
				out := dst.Pix[y*dst.Stride+x*4:][:min(n, outWidth-x)*4]
				for i := 3; i < len(out); i += 4 {
					out[i] = value
				}
			}
			count -= n
			x += n
			if x == width {
				x = 0
				y++
			}
		}
	}
	return nil
}
