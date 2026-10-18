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

	E "github.com/sagernet/sing/common/exceptions"
)

type Source interface {
	Open(ctx context.Context, name string) (io.ReadCloser, error)
	Size(ctx context.Context, name string) (int64, error)
	String() string
}

const manifestLimit = 16 << 20

func Import(ctx context.Context, src Source, dir string, progress func(received, total int64)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return E.Cause(err, "imagemounter")
	}
	counter := &progressCounter{ctx: ctx, report: progress}
	manifestData, err := readManifest(ctx, src, counter)
	if err != nil {
		return err
	}
	manifest, err := parseManifest(manifestData)
	if err != nil {
		return err
	}
	var staged []*stagedVariant
	defer func() {
		for _, variant := range staged {
			os.RemoveAll(variant.staging)
		}
	}()
	var missing error
	for _, variant := range []Variant{Cryptex, Personalized} {
		if err := ctx.Err(); err != nil {
			return err
		}
		identity, err := variant.identity(manifest)
		if err != nil {
			missing = E.Errors(missing, E.Cause(err, "import ", variant))
			continue
		}
		stagedVariant, err := stageVariant(ctx, src, dir, variant, identity, counter)
		var notInSource *notInSourceError
		switch {
		case err == nil:
			staged = append(staged, stagedVariant)
		case errors.As(err, &notInSource):
			missing = E.Errors(missing, E.Cause(err, "import ", variant))
		default:
			return err
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
	imported := false
	for _, variant := range staged {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := finishVariant(ctx, src, dir, variant, manifestData, counter)
		var notInSource *notInSourceError
		switch {
		case err == nil:
			imported = true
		case errors.As(err, &notInSource):
			missing = E.Errors(missing, E.Cause(err, "import ", variant.variant))
		default:
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	counter.flush()
	if !imported {
		return E.Cause(missing, "imagemounter: ", src, " holds no usable Developer Disk Image")
	}
	return nil
}

func Present(dir string, variant Variant) (bool, error) {
	dir = filepath.Join(dir, variant.String())
	manifest, err := loadManifest(context.Background(), dir)
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

var ErrNoManifest = E.New("imagemounter: no " + manifestName)

func readManifest(ctx context.Context, src Source, counter *progressCounter) ([]byte, error) {
	file, err := src.Open(ctx, manifestName)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, E.Extend(ErrNoManifest, src)
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	stop := context.AfterFunc(ctx, func() { file.Close() })
	defer stop()
	var data bytes.Buffer
	_, err = io.Copy(io.MultiWriter(&data, counter), io.LimitReader(&contextReader{ctx: ctx, reader: file}, manifestLimit))
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, E.Cause(err, "imagemounter: read ", manifestName)
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	staging, err := os.MkdirTemp(dir, "."+variant.String()+".*")
	if err != nil {
		return nil, E.Cause(err, "imagemounter")
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
		if err := ctx.Err(); err != nil {
			os.RemoveAll(staging)
			return nil, err
		}
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
	err := writeFile(ctx, filepath.Join(variant.staging, manifestName), func(file *os.File) error {
		_, err := file.Write(manifestData)
		return err
	})
	if err != nil {
		return err
	}
	if err = os.Chmod(variant.staging, 0o755); err != nil {
		return E.Cause(err, "imagemounter")
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	target := filepath.Join(dir, variant.variant.String())
	if err = os.RemoveAll(target); err != nil {
		return E.Cause(err, "imagemounter")
	}
	if err = os.Rename(variant.staging, target); err != nil {
		return E.Cause(err, "imagemounter")
	}
	return nil
}

func copyEntry(ctx context.Context, src Source, staging string, entry sourceFile, counter *progressCounter) error {
	file := resolvedFile{key: entry.key, path: filepath.Join(staging, fixedNames[entry.key]), digest: entry.digest}
	var mismatch error
	for _, name := range entry.names {
		if err := ctx.Err(); err != nil {
			return err
		}
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
	stop := context.AfterFunc(ctx, func() { input.Close() })
	defer stop()
	return writeFile(ctx, file.path, func(output *os.File) error {
		hash := sha512.New384()
		_, err := io.Copy(io.MultiWriter(output, hash, counter), &contextReader{ctx: ctx, reader: input})
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return E.Cause(err, "imagemounter: read ", name, " from ", src)
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
	if now := time.Now(); now.Sub(c.reported) >= 100*time.Millisecond {
		c.reported = now
		c.report(c.received, c.total)
	}
	return len(data), nil
}

func (c *progressCounter) setTotal(total int64) {
	c.total = total
	c.flush()
}

func (c *progressCounter) flush() {
	c.reported = time.Now()
	c.report(c.received, c.total)
}
