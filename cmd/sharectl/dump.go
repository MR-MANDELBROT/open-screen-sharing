package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
)

func newDumper(directory, encodings string) (func(rfb.RawRectangle), error) {
	err := os.MkdirAll(directory, 0o755)
	if err != nil {
		return nil, err
	}
	index, err := os.OpenFile(filepath.Join(directory, "index.tsv"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	_, err = fmt.Fprintln(index, "sequence\tencoding\tx\ty\twidth\theight\tbytes\tfile")
	if err != nil {
		return nil, err
	}
	wanted := make(map[string]bool)
	for _, name := range strings.Split(encodings, ",") {
		if name != "" {
			wanted[strings.TrimSpace(name)] = true
		}
	}
	return func(rectangle rfb.RawRectangle) {
		name := rectangle.Encoding.String()
		if len(wanted) > 0 && !wanted[name] && !wanted[fmt.Sprint(int32(rectangle.Encoding))] {
			return
		}
		file := fmt.Sprintf("%06d-%s-%dx%d-%dx%d.bin", rectangle.Sequence, name, rectangle.X, rectangle.Y, rectangle.Width, rectangle.Height)
		writeErr := os.WriteFile(filepath.Join(directory, file), rectangle.Payload, 0o644)
		if writeErr == nil {
			_, writeErr = fmt.Fprintf(index, "%d\t%s\t%d\t%d\t%d\t%d\t%d\t%s\n", rectangle.Sequence, name, rectangle.X, rectangle.Y,
				rectangle.Width, rectangle.Height, len(rectangle.Payload), file)
		}
		if writeErr != nil {
			fatal(fmt.Errorf("dump: %w", writeErr))
		}
	}, nil
}
