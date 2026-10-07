package rfb

import (
	"bytes"
	"compress/flate"
	"errors"
	"fmt"
	"io"
)

const zlibWindow = 32 * 1024

type zlibStream struct {
	started  bool
	inflater io.ReadCloser
	history  []byte
	output   []byte
	input    bytes.Reader
}

func (z *zlibStream) inflate(compressed []byte, limit int) ([]byte, error) {
	if !z.started {
		if len(compressed) < 2 {
			return nil, errors.New("zlib stream header missing")
		}
		header := uint16(compressed[0])<<8 | uint16(compressed[1])
		if compressed[0]&0x0f != 8 || header%31 != 0 || compressed[1]&0x20 != 0 {
			return nil, fmt.Errorf("zlib stream header %x", compressed[:2])
		}
		compressed = compressed[2:]
		z.started = true
	}
	z.input.Reset(compressed)
	if z.inflater == nil {
		z.inflater = flate.NewReaderDict(&z.input, z.history)
	} else {
		err := z.inflater.(flate.Resetter).Reset(&z.input, z.history)
		if err != nil {
			return nil, err
		}
	}
	z.output = z.output[:0]
	for {
		if len(z.output) == cap(z.output) {
			z.output = append(z.output, make([]byte, max(64*1024, len(z.output)))...)[:len(z.output)]
		}
		n, err := z.inflater.Read(z.output[len(z.output):cap(z.output)])
		z.output = z.output[:len(z.output)+n]
		if len(z.output) > limit {
			return nil, fmt.Errorf("rectangle inflates past %d bytes", limit)
		}
		if err == nil {
			continue
		}
		if (errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF)) && z.input.Len() == 0 {
			break
		}
		return nil, err
	}
	if len(z.output) >= zlibWindow {
		z.history = append(z.history[:0], z.output[len(z.output)-zlibWindow:]...)
	} else {
		keep := min(len(z.history), zlibWindow-len(z.output))
		copy(z.history, z.history[len(z.history)-keep:])
		z.history = append(z.history[:keep], z.output...)
	}
	return z.output, nil
}

func inflateIndependent(compressed []byte, limit int) ([]byte, error) {
	if len(compressed) < 2 {
		return nil, errors.New("zlib data too short")
	}
	inflater := flate.NewReader(bytes.NewReader(compressed[2:]))
	defer inflater.Close()
	var output bytes.Buffer
	_, err := io.Copy(&output, io.LimitReader(inflater, int64(limit)+1))
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, err
	}
	if output.Len() > limit {
		return nil, fmt.Errorf("zlib data inflates past %d bytes", limit)
	}
	return output.Bytes(), nil
}
