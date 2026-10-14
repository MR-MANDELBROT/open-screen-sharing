package ctl

import (
	"image"
	"image/png"
	"os"
	"strconv"
	"strings"

	"github.com/nekohasekai/open-screen-sharing/internal/bgra"
	E "github.com/sagernet/sing/common/exceptions"
)

func ParseNumbers[T any](value string, count int, parse func(string) (T, error)) ([]T, error) {
	parts := strings.Split(value, ",")
	if len(parts) < count {
		return nil, E.New(strconv.Quote(value), " needs ", count, " numbers")
	}
	numbers := make([]T, len(parts))
	for i, part := range parts {
		number, err := parse(strings.TrimSpace(part))
		if err != nil {
			return nil, err
		}
		numbers[i] = number
	}
	return numbers, nil
}

func ParseSize(value string) (int, int, error) {
	widthText, heightText, found := strings.Cut(value, "x")
	if !found {
		return 0, 0, E.New("size ", strconv.Quote(value), " is not WxH")
	}
	width, err := strconv.Atoi(widthText)
	if err != nil {
		return 0, 0, err
	}
	height, err := strconv.Atoi(heightText)
	if err != nil {
		return 0, 0, err
	}
	return width, height, nil
}

func Picture(frame *bgra.Image) *image.NRGBA {
	picture := image.NewNRGBA(image.Rect(0, 0, frame.Width, frame.Height))
	for y := range frame.Height {
		source := frame.Pix[y*frame.Stride : y*frame.Stride+frame.Width*4]
		target := picture.Pix[y*picture.Stride : y*picture.Stride+frame.Width*4]
		for x := 0; x < len(source); x += 4 {
			target[x], target[x+1], target[x+2], target[x+3] = source[x+2], source[x+1], source[x], 0xff
		}
	}
	return picture
}

func WritePNG(path string, picture image.Image) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	err = png.Encode(file, picture)
	closeErr := file.Close()
	return E.Errors(err, closeErr)
}
