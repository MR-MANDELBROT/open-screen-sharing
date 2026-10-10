package imagemounter

import (
	"bytes"
	"context"
	"crypto/sha512"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

type Source interface {
	Open(ctx context.Context, name string) (io.ReadCloser, error)
	Size(ctx context.Context, name string) (int64, error)
	String() string
}

const manifestLimit = 16 << 20

func Import(ctx context.Context, src Source, dir string, progress func(received, total int64)) ([]Variant, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("imagemounter: %w", err)
	}
	counter := &progressCounter{ctx: ctx, report: progress}
	manifestData, err := readManifest(ctx, src, counter)
	if err != nil {
		return nil, err
	}
	manifest, err := parseManifest(manifestData)
	if err != nil {
		return nil, err
	}
	var staged []*stagedVariant
	defer func() {
		for _, variant := range staged {
			os.RemoveAll(variant.staging)
		}
	}()
	var missing error
	for _, variant := range []Variant{Cryptex, Personalized} {
		identity, err := variant.identity(manifest)
		if err != nil {
			missing = errors.Join(missing, fmt.Errorf("%v: %w", variant, err))
			continue
		}
		stagedVariant, err := stageVariant(ctx, src, dir, variant, identity, counter)
		var notInSource *notInSourceError
		switch {
		case err == nil:
			staged = append(staged, stagedVariant)
		case errors.As(err, &notInSource):
			missing = errors.Join(missing, fmt.Errorf("%v: %w", variant, err))
		default:
			return nil, err
		}
	}
	total := counter.received
	for _, variant := range staged {
		if variant.image.size < 0 {
			total = 0
			break
		}
		total += variant.image.size
	}
	counter.setTotal(total)
	var imported []Variant
	for _, variant := range staged {
		err := finishVariant(ctx, src, dir, variant, manifestData, counter)
		var notInSource *notInSourceError
		switch {
		case err == nil:
			imported = append(imported, variant.variant)
		case errors.As(err, &notInSource):
			missing = errors.Join(missing, fmt.Errorf("%v: %w", variant.variant, err))
		default:
			return nil, err
		}
	}
	counter.flush()
	if len(imported) == 0 {
		return nil, fmt.Errorf("imagemounter: %s holds no usable Developer Disk Image: %w", src, missing)
	}
	return imported, nil
}

func Present(dir string, variant Variant) (bool, error) {
	dir = filepath.Join(dir, variant.String())
	manifest, err := loadManifest(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	identity, err := variant.identity(manifest)
	if err != nil {
		return false, err
	}
	for _, key := range variant.keys() {
		_, err = identity.resolve(dir, key)
		if errors.Is(err, errMissingFile) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
	}
	return true, nil
}

var ErrNoManifest = errors.New("imagemounter: no " + manifestName)

func readManifest(ctx context.Context, src Source, counter *progressCounter) ([]byte, error) {
	file, err := src.Open(ctx, manifestName)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w in %s", ErrNoManifest, src)
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var data bytes.Buffer
	if _, err = io.Copy(io.MultiWriter(&data, counter), io.LimitReader(file, manifestLimit)); err != nil {
		return nil, fmt.Errorf("imagemounter: read %s: %w", manifestName, err)
	}
	return data.Bytes(), nil
}

type notInSourceError struct {
	key string
	err error
}

func (e *notInSourceError) Error() string {
	if e.err != nil {
		return e.err.Error()
	}
	return fmt.Sprintf("imagemounter: no %s", e.key)
}

func (e *notInSourceError) Unwrap() error { return e.err }

type stagedVariant struct {
	variant Variant
	staging string
	image   sourceFile
}

type sourceFile struct {
	key    string
	digest []byte
	names  []string
	size   int64
}

func stageVariant(ctx context.Context, src Source, dir string, variant Variant, identity buildIdentity, counter *progressCounter) (*stagedVariant, error) {
	staging, err := os.MkdirTemp(dir, "."+variant.String()+".*")
	if err != nil {
		return nil, fmt.Errorf("imagemounter: %w", err)
	}
	keys := variant.keys()
	files := make([]sourceFile, 0, len(keys))
	for _, key := range keys {
		digest, err := identity.digest(key)
		if err != nil {
			os.RemoveAll(staging)
			return nil, &notInSourceError{key: key, err: err}
		}
		files = append(files, sourceFile{key: key, digest: digest, names: identity.names(key), size: -1})
	}
	for _, file := range files[:len(files)-1] {
		if err = copyEntry(ctx, src, staging, file, counter); err != nil {
			os.RemoveAll(staging)
			return nil, err
		}
	}
	image := files[len(files)-1]
	for index, name := range image.names {
		size, err := src.Size(ctx, name)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			os.RemoveAll(staging)
			return nil, err
		}
		image.names = image.names[index:]
		image.size = size
		return &stagedVariant{variant: variant, staging: staging, image: image}, nil
	}
	os.RemoveAll(staging)
	return nil, &notInSourceError{key: image.key}
}

func finishVariant(ctx context.Context, src Source, dir string, variant *stagedVariant, manifestData []byte, counter *progressCounter) error {
	if err := copyEntry(ctx, src, variant.staging, variant.image, counter); err != nil {
		return err
	}
	err := writeFile(filepath.Join(variant.staging, manifestName), func(file *os.File) error {
		_, err := file.Write(manifestData)
		return err
	})
	if err != nil {
		return err
	}
	if err = os.Chmod(variant.staging, 0o755); err != nil {
		return fmt.Errorf("imagemounter: %w", err)
	}
	target := filepath.Join(dir, variant.variant.String())
	if err = os.RemoveAll(target); err != nil {
		return fmt.Errorf("imagemounter: %w", err)
	}
	if err = os.Rename(variant.staging, target); err != nil {
		return fmt.Errorf("imagemounter: %w", err)
	}
	return nil
}

func copyEntry(ctx context.Context, src Source, staging string, entry sourceFile, counter *progressCounter) error {
	file := resolvedFile{key: entry.key, path: filepath.Join(staging, fixedNames[entry.key]), digest: entry.digest}
	var mismatch error
	for _, name := range entry.names {
		err := copyFile(ctx, src, name, file, counter)
		var mismatchErr *mismatchError
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if errors.As(err, &mismatchErr) {
			mismatch = err
			continue
		}
		return err
	}
	return &notInSourceError{key: entry.key, err: mismatch}
}

func copyFile(ctx context.Context, src Source, name string, file resolvedFile, counter *progressCounter) error {
	input, err := src.Open(ctx, name)
	if err != nil {
		return err
	}
	defer input.Close()
	return writeFile(file.path, func(output *os.File) error {
		hash := sha512.New384()
		if _, err := io.Copy(io.MultiWriter(output, hash, counter), input); err != nil {
			return fmt.Errorf("imagemounter: read %s from %s: %w", name, src, err)
		}
		if sum := hash.Sum(nil); !bytes.Equal(sum, file.digest) {
			return &mismatchError{path: name, sum: sum, want: file.digest}
		}
		return nil
	})
}

type progressCounter struct {
	ctx      context.Context
	received int64
	total    int64
	reported time.Time
	report   func(received, total int64)
}

func (c *progressCounter) Write(data []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	c.received += int64(len(data))
	if c.report != nil {
		if now := time.Now(); now.Sub(c.reported) >= 100*time.Millisecond {
			c.reported = now
			c.report(c.received, c.total)
		}
	}
	return len(data), nil
}

func (c *progressCounter) setTotal(total int64) {
	c.total = total
	c.flush()
}

func (c *progressCounter) flush() {
	if c.report != nil {
		c.reported = time.Now()
		c.report(c.received, c.total)
	}
}
