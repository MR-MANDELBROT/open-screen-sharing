package cursortheme

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/bzip2"
	"compress/flate"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"io"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/bodgit/sevenzip"
	"github.com/klauspost/compress/zstd"
	"github.com/nwaples/rardecode/v2"
	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/ulikunitz/xz"
)

const (
	maximumFileSize  = 32 << 20
	maximumTotalSize = 512 << 20
	maximumDepth     = 8
)

type frame struct {
	image    image.Image
	hotspotX int
	hotspotY int
	scale    float64
}

type theme struct {
	root    string
	name    string
	cursors map[string][]frame
}

type Manifest struct {
	Name    string                     `json:"name"`
	Cursors map[string][]ManifestImage `json:"cursors"`
}

type ManifestImage struct {
	File     string  `json:"file"`
	Scale    float64 `json:"scale"`
	HotspotX int     `json:"hotspotX"`
	HotspotY int     `json:"hotspotY"`
}

func Import(source string, destination string) error {
	file, err := os.Open(source)
	if err != nil {
		return err
	}
	files, err := unpack(file, filepath.Base(source))
	file.Close()
	if err != nil {
		return err
	}
	var themes []theme
	for _, item := range slices.Concat(parseKDEThemes(files), parseHyprcursorThemes(files), parseXcursorThemes(files)) {
		index := slices.IndexFunc(themes, func(existing theme) bool {
			return existing.root == item.root
		})
		if index < 0 {
			themes = append(themes, item)
			continue
		}
		if themes[index].name == "" {
			themes[index].name = item.name
		}
		for role, frames := range item.cursors {
			if _, found := themes[index].cursors[role]; !found {
				themes[index].cursors[role] = frames
			}
		}
	}
	themes = common.Filter(slices.Concat(themes, parseCapeThemes(files), parseWindowsThemes(files)), func(item theme) bool {
		return len(item.cursors["default"]) > 0
	})
	if len(themes) == 0 {
		return E.New("cursor theme: no supported cursors found")
	}
	fallbackName := trimArchiveExtension(filepath.Base(source))
	if extension := path.Ext(fallbackName); slices.Contains([]string{".cur", ".ani", ".cape"}, strings.ToLower(extension)) {
		fallbackName = strings.TrimSuffix(fallbackName, extension)
	}
	for index, item := range themes {
		if item.name == "" {
			item.name = fallbackName
		}
		err = write(item, filepath.Join(destination, strconv.Itoa(index)))
		if err != nil {
			return err
		}
	}
	return nil
}

func write(item theme, directory string) error {
	err := os.MkdirAll(directory, 0o755)
	if err != nil {
		return err
	}
	manifest := Manifest{Name: item.name, Cursors: make(map[string][]ManifestImage)}
	for role, frames := range item.cursors {
		for index, frame := range frames {
			file := role + "-" + strconv.Itoa(index) + ".png"
			var buffer bytes.Buffer
			err = png.Encode(&buffer, frame.image)
			if err != nil {
				return E.Cause(err, "encode ", file)
			}
			err = os.WriteFile(filepath.Join(directory, file), buffer.Bytes(), 0o644)
			if err != nil {
				return err
			}
			manifest.Cursors[role] = append(manifest.Cursors[role], ManifestImage{
				File: file, Scale: frame.scale, HotspotX: frame.hotspotX, HotspotY: frame.hotspotY,
			})
		}
	}
	content, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(directory, "theme.json"), content, 0o644)
}

var (
	errTooLarge  = E.New("cursor theme: archive is too large")
	errEncrypted = E.New("cursor theme: archive is encrypted")
)

type unpacker struct {
	files   map[string][]byte
	links   map[string]string
	total   int
	failure error
}

type countingReader struct {
	io.Reader
	unpacker *unpacker
}

func (r countingReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.unpacker.total += n
	if r.unpacker.total > maximumTotalSize {
		return n, errTooLarge
	}
	return n, err
}

func unpack(reader io.Reader, name string) (map[string][]byte, error) {
	u := &unpacker{files: make(map[string][]byte), links: make(map[string]string)}
	err := u.fail(u.extract(countingReader{reader, u}, name, "", 0))
	if err != nil {
		return nil, err
	}
	if len(u.files) == 0 && u.failure != nil {
		return nil, u.failure
	}
	u.resolveLinks()
	return u.files, nil
}

func (u *unpacker) fail(err error) error {
	if err == nil || errors.Is(err, errTooLarge) {
		return err
	}
	if u.failure == nil {
		u.failure = err
	}
	return nil
}

func (u *unpacker) extract(reader io.Reader, name string, root string, depth int) error {
	if depth > maximumDepth {
		return E.New("cursor theme: ", name, " is nested too deeply")
	}
	buffered := bufio.NewReader(reader)
	header, _ := buffered.Peek(512)
	var (
		decompressor io.Reader
		err          error
	)
	switch {
	case bytes.HasPrefix(header, []byte{0x1f, 0x8b, 8}):
		decompressor, err = gzip.NewReader(buffered)
	case bytes.HasPrefix(header, []byte{0xfd, '7', 'z', 'X', 'Z', 0}):
		decompressor, err = xz.NewReader(buffered)
	case len(header) >= 10 && bytes.HasPrefix(header, []byte("BZh")) && string(header[4:10]) == "1AY&SY":
		decompressor = bzip2.NewReader(buffered)
	case bytes.HasPrefix(header, []byte{0x28, 0xb5, 0x2f, 0xfd}):
		var decoder *zstd.Decoder
		decoder, err = zstd.NewReader(buffered, zstd.WithDecoderConcurrency(1))
		if err == nil {
			defer decoder.Close()
		}
		decompressor = decoder
	}
	if err != nil {
		return E.Cause(err, "decompress ", name)
	}
	if decompressor != nil {
		return u.extract(countingReader{decompressor, u}, trimArchiveExtension(name), root, depth+1)
	}
	switch {
	case len(header) >= 262 && string(header[257:262]) == "ustar":
		return u.extractTar(buffered, root, depth)
	case bytes.HasPrefix(header, []byte("Rar!\x1a\x07")):
		return u.extractRar(buffered, root, depth)
	case bytes.HasPrefix(header, []byte("PK\x03\x04")), bytes.HasPrefix(header, []byte{'7', 'z', 0xbc, 0xaf, 0x27, 0x1c}):
		data, err := readAll(buffered, maximumTotalSize, name)
		if err != nil {
			return err
		}
		if data[0] == 'P' {
			return u.extractZip(data, root, depth)
		}
		return u.extract7z(data, root, depth)
	}
	return u.store(name, buffered)
}

func (u *unpacker) store(name string, reader io.Reader) error {
	content, err := readAll(reader, maximumFileSize, name)
	if err != nil {
		return err
	}
	u.files[name] = content
	return nil
}

func (u *unpacker) add(root string, name string, reader io.Reader, depth int) error {
	if strings.HasSuffix(name, "/") {
		return nil
	}
	name = cleanPath(name)
	if name == "" || strings.HasPrefix(name, "__MACOSX/") || strings.HasPrefix(path.Base(name), "._") {
		return nil
	}
	name = path.Join(root, name)
	root = trimArchiveExtension(name)
	if root == name {
		return u.store(name, reader)
	}
	return u.extract(reader, name, root, depth+1)
}

func (u *unpacker) extractTar(reader io.Reader, root string, depth int) error {
	archive := tar.NewReader(reader)
	for {
		header, err := archive.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return E.Cause(err, "read tar archive")
		}
		name := path.Join(root, cleanPath(header.Name))
		switch header.Typeflag {
		case tar.TypeReg:
			err = u.fail(u.add(root, header.Name, archive, depth))
			if err != nil {
				return err
			}
		case tar.TypeSymlink:
			u.links[name] = path.Join(path.Dir(name), header.Linkname)
		case tar.TypeLink:
			u.links[name] = path.Join(root, cleanPath(header.Linkname))
		}
	}
}

func (u *unpacker) extractRar(reader io.Reader, root string, depth int) error {
	archive, err := rardecode.NewReader(reader)
	if err != nil {
		return E.Cause(err, "read rar archive")
	}
	for {
		header, err := archive.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return E.Cause(err, "read rar archive")
		}
		name := path.Join(root, cleanPath(header.Name))
		switch header.LinkType {
		case rardecode.LinkTypeNone:
			if !header.IsDir {
				err = u.fail(u.add(root, header.Name, countingReader{archive, u}, depth))
				if err != nil {
					return err
				}
			}
		case rardecode.LinkTypeUnixSymlink, rardecode.LinkTypeWindowsSymlink:
			u.links[name] = path.Join(path.Dir(name), strings.ReplaceAll(header.LinkTarget, "\\", "/"))
		case rardecode.LinkTypeHardLink, rardecode.LinkTypeFileCopy:
			u.links[name] = path.Join(root, cleanPath(header.LinkTarget))
		}
	}
}

func (u *unpacker) extract7z(data []byte, root string, depth int) error {
	archive, err := sevenzip.NewReader(bytes.NewReader(data), int64(len(data)))
	if readError, isReadError := errors.AsType[*sevenzip.ReadError](err); isReadError && readError.Encrypted {
		return errEncrypted
	}
	if err != nil {
		return E.Cause(err, "read 7z archive")
	}
	for _, file := range archive.File {
		reader, err := file.Open()
		if err == nil {
			err = u.add(root, file.Name, countingReader{reader, u}, depth)
			reader.Close()
		}
		if readError, isReadError := errors.AsType[*sevenzip.ReadError](err); isReadError && readError.Encrypted {
			err = errEncrypted
		}
		err = u.fail(err)
		if err != nil {
			return err
		}
	}
	return nil
}

func (u *unpacker) extractZip(data []byte, root string, depth int) error {
	archive, _ := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if archive == nil {
		return u.extractZipEntries(data, root, depth)
	}
	for _, file := range archive.File {
		err := errEncrypted
		if file.Flags&1 == 0 {
			var reader io.ReadCloser
			reader, err = file.Open()
			if err == nil {
				err = u.add(root, file.Name, countingReader{reader, u}, depth)
				reader.Close()
			}
		}
		err = u.fail(err)
		if err != nil {
			return err
		}
	}
	return nil
}

func (u *unpacker) extractZipEntries(data []byte, root string, depth int) error {
	for len(data) >= 30 && bytes.HasPrefix(data, []byte("PK\x03\x04")) {
		flags := binary.LittleEndian.Uint16(data[6:])
		method := binary.LittleEndian.Uint16(data[8:])
		size := int(binary.LittleEndian.Uint32(data[18:]))
		nameEnd := 30 + int(binary.LittleEndian.Uint16(data[26:]))
		start := nameEnd + int(binary.LittleEndian.Uint16(data[28:]))
		if start > len(data) {
			break
		}
		body := data[start:]
		if flags&8 != 0 && method == zip.Store {
			size = -1
			for index := 0; ; index++ {
				found := bytes.Index(body[index:], []byte("PK\x07\x08"))
				if found < 0 {
					break
				}
				index += found
				if index+12 <= len(body) && int(binary.LittleEndian.Uint32(body[index+8:])) == index {
					size = index
					break
				}
			}
		} else if flags&8 != 0 {
			size = len(body)
		}
		if size < 0 || size > len(body) {
			break
		}
		reader := bytes.NewReader(body[:size])
		var err error
		switch {
		case method != zip.Store && method != zip.Deflate:
			err = zip.ErrAlgorithm
		case flags&1 != 0:
			err = errEncrypted
		default:
			var content io.Reader = reader
			if method == zip.Deflate {
				content = flate.NewReader(reader)
			}
			counted := countingReader{content, u}
			err = u.add(root, string(data[30:nameEnd]), counted, depth)
			_, drainErr := io.Copy(io.Discard, counted)
			if err == nil {
				err = drainErr
			}
		}
		err = u.fail(err)
		if err != nil {
			return err
		}
		if flags&8 == 0 {
			data = body[size:]
			continue
		}
		next := body[size-reader.Len():]
		data = nil
		for _, length := range []int{16, 12, 24, 20} {
			if len(next) >= length+4 && (bytes.HasPrefix(next[length:], []byte("PK\x03\x04")) || bytes.HasPrefix(next[length:], []byte("PK\x01\x02"))) {
				data = next[length:]
				break
			}
		}
	}
	if bytes.HasPrefix(data, []byte("PK\x01\x02")) || bytes.HasPrefix(data, []byte("PK\x05\x06")) {
		return nil
	}
	return E.Cause(io.ErrUnexpectedEOF, "read zip archive")
}

func (u *unpacker) resolveLinks() {
	names := slices.Sorted(maps.Keys(u.files))
	under := func(directory string) []string {
		start, _ := slices.BinarySearch(names, directory+"/")
		end := start
		for end < len(names) && strings.HasPrefix(names[end], directory+"/") {
			end++
		}
		return names[start:end]
	}
	for _, name := range names {
		content := u.files[name]
		if len(content) == 0 || len(content) > 1024 || !utf8.Valid(content) || bytes.ContainsFunc(content, unicode.IsControl) {
			continue
		}
		target := path.Join(path.Dir(name), string(content))
		_, isFile := u.files[target]
		_, isLink := u.links[target]
		if !strings.HasPrefix(name+"/", target+"/") && (isFile || isLink || len(under(target)) > 0) {
			u.links[name] = target
		}
	}
	for name := range u.links {
		delete(u.files, name)
	}
	for range 8 {
		names = slices.Sorted(maps.Keys(u.files))
		resolved := make(map[string][]byte)
		for name, target := range u.links {
			if content, found := u.files[target]; found {
				resolved[name] = content
			} else if !strings.HasPrefix(name+"/", target+"/") {
				for _, file := range under(target) {
					resolved[name+strings.TrimPrefix(file, target)] = u.files[file]
				}
			}
		}
		added := false
		for name, content := range resolved {
			if _, found := u.files[name]; !found {
				u.files[name] = content
				added = true
			}
		}
		if !added {
			break
		}
	}
}

func resolveCursors(roles map[string][]string, decode func(name string) []frame) map[string][]frame {
	cursors := make(map[string][]frame)
	for role, names := range roles {
		for _, name := range names {
			frames := decode(name)
			if len(frames) > 0 {
				cursors[role] = frames
				break
			}
		}
	}
	return cursors
}

func readAll(reader io.Reader, limit int, name string) ([]byte, error) {
	content, err := io.ReadAll(io.LimitReader(reader, int64(limit)+1))
	if err != nil {
		return nil, E.Cause(err, "read ", name)
	}
	if len(content) > limit {
		return nil, E.New("cursor theme: ", name, " is too large")
	}
	return content, nil
}

func trimArchiveExtension(name string) string {
	for {
		extension := path.Ext(name)
		if !slices.Contains([]string{".gz", ".tgz", ".xz", ".txz", ".bz2", ".tbz", ".tbz2", ".zst", ".tzst", ".tar", ".zip", ".7z", ".rar"}, strings.ToLower(extension)) {
			return name
		}
		name = strings.TrimSuffix(name, extension)
	}
}

func cleanPath(name string) string {
	name = path.Clean("/" + strings.ReplaceAll(name, "\\", "/"))
	return strings.TrimPrefix(name, "/")
}
