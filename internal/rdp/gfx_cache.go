package rdp

import (
	"cmp"
	"encoding/binary"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/nekohasekai/open-screen-sharing/internal/bgra"

	"github.com/klauspost/compress/zstd"
	E "github.com/sagernet/sing/common/exceptions"
)

const (
	gfxCacheImportOffer  = 0x0010
	gfxMaxImportBytes    = 64 << 10
	gfxStoredEntryLength = 13
	gfxCacheFileMagic    = "OSGFXC01"

	// rdpserverbase.dll of Windows 11 26200 rejects a cache import offer of more than 5461 entries,
	// one less than MS-RDPEGFX allows.
	gfxMaxImportEntries = 5461
)

type gfxCacheEntry struct {
	image  *bgra.Image
	format byte
	// rdpserverbase.dll of Windows 11 26200 derives the key from the pixels, size and format of the
	// bitmap and the codec settings of the session, with no per-session secret, so it stays valid
	// across sessions to the same server.
	key  uint64
	used uint64
}

type gfxCacheImport struct {
	file    *os.File
	decoder *zstd.Decoder
	entries []*gfxCacheEntry
}

func readGFXCache(path string) (cache *gfxCacheImport, err error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	cache = &gfxCacheImport{file: file}
	defer func() {
		if err != nil {
			cache.close()
		}
	}()
	magic := make([]byte, len(gfxCacheFileMagic))
	_, err = io.ReadFull(file, magic)
	if err != nil {
		return nil, err
	}
	if string(magic) != gfxCacheFileMagic {
		return nil, E.New("unknown graphics cache format")
	}
	cache.decoder, err = zstd.NewReader(file, zstd.WithDecoderConcurrency(1))
	if err != nil {
		return nil, err
	}
	var header [4]byte
	_, err = io.ReadFull(cache.decoder, header[:])
	if err != nil {
		return nil, err
	}
	count := binary.LittleEndian.Uint32(header[:])
	if count > gfxMaxImportEntries {
		return nil, E.New("graphics cache of ", count, " entries exceeds ", gfxMaxImportEntries)
	}
	index := make([]byte, int(count)*gfxStoredEntryLength)
	_, err = io.ReadFull(cache.decoder, index)
	if err != nil {
		return nil, err
	}
	var total int
	r := &reader{data: index}
	cache.entries = make([]*gfxCacheEntry, 0, count)
	for range count {
		key, format, width, height := r.u64(), r.u8(), int(r.u16()), int(r.u16())
		total += width * height * gfxBytesPerPixel
		if width == 0 || height == 0 || width*height*gfxBytesPerPixel > gfxMaxImportBytes || key == 0 || !gfxValidFormat(format) || total > gfxMaxCacheBytes {
			return nil, E.New("invalid graphics cache entry ", width, "x", height, " in format ", format)
		}
		image := &bgra.Image{Stride: width * gfxBytesPerPixel, Width: width, Height: height}
		cache.entries = append(cache.entries, &gfxCacheEntry{image: image, format: format, key: key})
	}
	return cache, nil
}

func (c *gfxCacheImport) close() {
	if c.decoder != nil {
		c.decoder.Close()
	}
	_ = c.file.Close()
}

func writeGFXCache(path string, entries []*gfxCacheEntry) error {
	directory := filepath.Dir(path)
	err := os.MkdirAll(directory, 0o755)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(directory, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	err = writeGFXCacheEntries(file, entries)
	err = E.Errors(err, file.Close())
	if err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func writeGFXCacheEntries(file *os.File, entries []*gfxCacheEntry) error {
	_, err := file.WriteString(gfxCacheFileMagic)
	if err != nil {
		return err
	}
	encoder, err := zstd.NewWriter(file, zstd.WithEncoderLevel(zstd.SpeedFastest), zstd.WithEncoderConcurrency(1))
	if err != nil {
		return err
	}
	index := binary.LittleEndian.AppendUint32(nil, uint32(len(entries)))
	for _, entry := range entries {
		index = binary.LittleEndian.AppendUint64(index, entry.key)
		index = append(index, entry.format)
		index = binary.LittleEndian.AppendUint16(index, uint16(entry.image.Width))
		index = binary.LittleEndian.AppendUint16(index, uint16(entry.image.Height))
	}
	_, err = encoder.Write(index)
	for _, entry := range entries {
		if err != nil {
			break
		}
		_, err = encoder.Write(entry.image.Pix)
	}
	return E.Errors(err, encoder.Close())
}

func (p *rdpgfx) offerCache() error {
	if p.cachePath == "" {
		return nil
	}
	cache, err := readGFXCache(p.cachePath)
	if err != nil {
		if !os.IsNotExist(err) {
			p.g.logger.Warn("read the graphics cache", "error", err)
		}
		return nil
	}
	if len(cache.entries) == 0 {
		cache.close()
		return nil
	}
	p.imports = cache
	offer := binary.LittleEndian.AppendUint16(nil, uint16(len(cache.entries)))
	for _, entry := range cache.entries {
		offer = binary.LittleEndian.AppendUint64(offer, entry.key)
		offer = binary.LittleEndian.AppendUint32(offer, uint32(entry.image.Stride*entry.image.Height))
	}
	return p.send(gfxPacket(gfxCacheImportOffer, offer))
}

func (p *rdpgfx) importCache(r *reader) error {
	count := int(r.u16())
	if r.err != nil {
		return r.err
	}
	if count > r.remaining()/2 {
		return E.New("cache import reply lists ", count, " slots in ", r.remaining(), " bytes")
	}
	cache := p.imports
	p.imports = nil
	if cache == nil {
		if count > 0 {
			return E.New("unsolicited cache import reply with ", count, " entries")
		}
		return nil
	}
	defer cache.close()
	if count > len(cache.entries) {
		return E.New("cache import reply with ", count, " entries for ", len(cache.entries), " offered")
	}
	var imported int
	for _, entry := range cache.entries[:count] {
		slot := r.u16()
		if slot > gfxMaxCacheEntries {
			return E.New("invalid cache import slot ", slot)
		}
		size := entry.image.Stride * entry.image.Height
		// rdpserverbase.dll of Windows 11 26200 assigns slot 0 to an entry whose key it already holds.
		if slot == 0 {
			_, err := io.CopyN(io.Discard, cache.decoder, int64(size))
			if err != nil {
				return E.Cause(err, "read the graphics cache")
			}
			continue
		}
		entry.image.Pix = make([]byte, size)
		_, err := io.ReadFull(cache.decoder, entry.image.Pix)
		if err != nil {
			return E.Cause(err, "read the graphics cache")
		}
		err = p.storeCache(slot, entry)
		if err != nil {
			return err
		}
		imported++
	}
	p.g.logger.Debug("import the graphics cache", "offered", len(cache.entries), "imported", imported)
	return nil
}

func (p *rdpgfx) storeCache(slot uint16, entry *gfxCacheEntry) error {
	p.evictCache(slot)
	err := p.cacheMemory.reserve(len(entry.image.Pix))
	if err != nil {
		return err
	}
	p.cache[slot] = entry
	return nil
}

func (p *rdpgfx) evictCache(slot uint16) {
	if cached, found := p.cache[slot]; found {
		p.cacheMemory.release(len(cached.image.Pix))
		delete(p.cache, slot)
	}
}

func (p *rdpgfx) saveCache() {
	if p.cachePath == "" || !p.cacheChanged {
		return
	}
	entries := slices.SortedFunc(maps.Values(p.cache), func(a, b *gfxCacheEntry) int { return cmp.Compare(b.used, a.used) })
	// rdpserverbase.dll of Windows 11 26200 rejects the whole cache import offer when one entry has a
	// zero key or more than 64 KiB of pixels.
	entries = slices.DeleteFunc(entries, func(entry *gfxCacheEntry) bool {
		return entry.key == 0 || len(entry.image.Pix) > gfxMaxImportBytes
	})
	err := writeGFXCache(p.cachePath, entries[:min(len(entries), gfxMaxImportEntries)])
	if err != nil {
		p.g.logger.Warn("save the graphics cache", "error", err)
	}
}
