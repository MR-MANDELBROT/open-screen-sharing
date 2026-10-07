package imagemounter

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
)

const manifestDepth = 3

func OpenFile(name string) (Source, io.Closer, error) {
	info, err := os.Stat(name)
	if err != nil {
		return nil, nil, fmt.Errorf("imagemounter: %w", err)
	}
	var fsys fs.FS
	var closer io.Closer = io.NopCloser(nil)
	if info.IsDir() {
		fsys = os.DirFS(name)
	} else {
		archive, err := zip.OpenReader(name)
		if err != nil {
			return nil, nil, fmt.Errorf("imagemounter: %s is neither a directory nor a ZIP archive: %w", name, err)
		}
		fsys, closer = archive, archive
	}
	root, err := findManifest(fsys)
	if err != nil {
		closer.Close()
		return nil, nil, fmt.Errorf("%w in %s", err, name)
	}
	sub, err := fs.Sub(fsys, root)
	if err != nil {
		closer.Close()
		return nil, nil, fmt.Errorf("imagemounter: %w", err)
	}
	return &fsSource{fsys: sub, name: path.Join(info.Name(), root)}, closer, nil
}

func findManifest(fsys fs.FS) (string, error) {
	level := []string{"."}
	for range manifestDepth + 1 {
		var next []string
		for _, dir := range level {
			if _, err := fs.Stat(fsys, path.Join(dir, manifestName)); err == nil {
				return dir, nil
			}
			entries, err := fs.ReadDir(fsys, dir)
			if err != nil {
				continue
			}
			for _, entry := range entries {
				if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") && entry.Name() != "__MACOSX" {
					next = append(next, path.Join(dir, entry.Name()))
				}
			}
		}
		level = next
	}
	return "", ErrNoManifest
}

type fsSource struct {
	fsys fs.FS
	name string
}

func (s *fsSource) String() string { return s.name }

func (s *fsSource) Size(ctx context.Context, name string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	info, err := fs.Stat(s.fsys, name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, err
		}
		return 0, fmt.Errorf("imagemounter: %w", err)
	}
	return info.Size(), nil
}

func (s *fsSource) Open(ctx context.Context, name string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := s.fsys.Open(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		return nil, fmt.Errorf("imagemounter: %w", err)
	}
	return file, nil
}
