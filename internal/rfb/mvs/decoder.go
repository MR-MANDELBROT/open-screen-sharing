package mvs

import (
	"errors"
	"fmt"
)

const (
	updatePartial      = 0
	updateFull         = 1
	updateQuantization = 2

	quantizationUpdateLength = 129
	cacheLastIndex           = 64999
	cacheChunkBits           = 10
)

type Target struct {
	Pix    []byte
	Stride int
	Width  int
	Height int
}

type tileState struct {
	generation uint32
	copySource int32
}

type tileBaseline struct {
	lumaCount uint8
	cbDC      int8
	crDC      int8
	luma      [64]int8
}

// ScreenSharing caches 64 Y, 15 Cr and 20 Cb bytes per entry (99 bytes total).
// Verified in _DecodeMVSUpdate on macOS 26A434 and 26B5101f, arm64e.
type cacheEntry struct {
	luma     [64]int8
	cb       [20]int8
	cr       [15]int8
	rendered uint32
	pixels   tilePixels
}

type tilePixels [8][32]byte

type tileScratch struct {
	lumaScan [64]int8
	luma     block
	cb       block
	cr       block
	lumaAC   bool
	cbAC     bool
	crAC     bool
	pixels   tilePixels
	lumaY    samples
	cbY      samples
	crY      samples
}

type Decoder struct {
	width      int
	height     int
	tilesWide  int
	tiles      []tileState
	baselines  []tileBaseline
	generation uint32

	luminanceQuantization   quantizationTable
	chrominanceQuantization quantizationTable
	quantizationGeneration  uint32

	cache              [][]cacheEntry
	cacheLength        int
	cacheWriteIndex    uint16
	lastCacheReference uint16

	scratch tileScratch
}

func NewDecoder() *Decoder {
	decoder := &Decoder{quantizationGeneration: 1}
	for i := range 64 {
		decoder.luminanceQuantization[i] = int32(defaultLuminanceQuantization[i])
		decoder.chrominanceQuantization[i] = int32(defaultChrominanceQuantization[i])
	}
	return decoder
}

func (d *Decoder) Reset(width, height int) {
	if width == d.width && height == d.height && d.tiles != nil {
		return
	}
	d.width = width
	d.height = height
	d.tilesWide = (width + 7) / 8
	count := d.tilesWide * ((height + 7) / 8)
	if cap(d.tiles) >= count && cap(d.tiles) <= count*2 {
		d.tiles = d.tiles[:count]
		d.baselines = d.baselines[:count]
		clear(d.baselines)
	} else {
		d.tiles = make([]tileState, count)
		d.baselines = make([]tileBaseline, count)
	}
	for i := range d.tiles {
		d.tiles[i] = tileState{copySource: -1}
	}
	d.generation = 0
}

func (d *Decoder) Decode(update []byte, x, y, width, height int, target *Target) error {
	if len(update) == 0 {
		return errors.New("mvs: empty update")
	}
	switch update[0] {
	case updateQuantization:
		if len(update) != quantizationUpdateLength {
			return fmt.Errorf("mvs: quantisation update has %d bytes", len(update))
		}
		for i := range 64 {
			d.luminanceQuantization[i] = int32(update[1+i])
			d.chrominanceQuantization[i] = int32(update[65+i])
		}
		d.quantizationGeneration++
		return nil
	case updatePartial, updateFull:
	default:
		return fmt.Errorf("mvs: unknown update type %d", update[0])
	}
	if width <= 0 || height <= 0 {
		return errors.New("mvs: empty rectangle")
	}
	if target.Width <= 0 || target.Height <= 0 || target.Stride < target.Width*4 ||
		len(target.Pix) < (target.Height-1)*target.Stride+target.Width*4 {
		return errors.New("mvs: invalid target")
	}
	if x < 0 || y < 0 || x+width > target.Width || y+height > target.Height {
		return fmt.Errorf("mvs: rectangle %dx%d at %d,%d outside %dx%d framebuffer", width, height, x, y, target.Width, target.Height)
	}
	d.Reset(target.Width, target.Height)
	if update[0] == updatePartial {
		return d.decodePartial(update, x, y, width, height, target)
	}
	return d.decodeFull(update, x, y, width, height, target)
}

func nextCacheIndex(index uint16) uint16 {
	if index == cacheLastIndex {
		return 1
	}
	return index + 1
}

func (d *Decoder) cachedEntry(index uint16) *cacheEntry {
	if index == 0 || int(index) >= d.cacheLength {
		return nil
	}
	d.lastCacheReference = index
	entry := &d.cache[index>>cacheChunkBits][index&(1<<cacheChunkBits-1)]
	if entry.rendered != d.quantizationGeneration {
		d.renderCacheEntry(entry)
		entry.rendered = d.quantizationGeneration
	}
	return entry
}

func (d *Decoder) insertCache(lossless bool) {
	index := nextCacheIndex(d.cacheWriteIndex)
	d.cacheWriteIndex = index
	chunk := int(index >> cacheChunkBits)
	if chunk == len(d.cache) {
		d.cache = append(d.cache, make([]cacheEntry, 1<<cacheChunkBits))
	}
	d.cacheLength = max(d.cacheLength, int(index)+1)
	entry := &d.cache[chunk][index&(1<<cacheChunkBits-1)]
	scratch := &d.scratch
	entry.luma = scratch.lumaScan
	for scan := range len(entry.cb) {
		entry.cb[scan] = int8(scratch.cb[zigzag[scan]])
	}
	for scan := range len(entry.cr) {
		entry.cr[scan] = int8(scratch.cr[zigzag[scan]])
	}
	if lossless {
		entry.pixels = scratch.pixels
		entry.rendered = d.quantizationGeneration
	} else {
		entry.rendered = 0
	}
}
