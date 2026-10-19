package cursortheme

import (
	"bytes"
	"image"
	"image/draw"
	"image/png"
	"io"
	"maps"
	"math"
	"path"
	"slices"
	"strings"

	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
	"golang.org/x/image/tiff"
	"howett.net/plist"
)

// com.apple.cursor.N are CoreCursor numbers: 17-19 and 21-23 are NSCursor resizeLeft/Right/LeftRight and
// resizeUp/Down/UpDown, an arrow beside a bar (19 and 23 put the bar between two arrows), 27-38 are the window edge
// arrows, 39 is the four-way move arrows (-[NSCursor _coreCursorType] and CoreCursorCopyImages on macOS 27.0.1 26A434).
// Capes follow Mousecape's labels, which make 7 and 8 the crosshair although NSCursor crosshairCursor is 20 (Mousecape
// 184f87b, Mousecape/mousecloak/MCDefs.m:137-185); com.apple.coregraphics.Move is an 11 x 16 pt arrow despite its name
// (SkyLight 953.5.5, macOS 27.0.1 26A434, CursorAsset id 5).
var capeRoles = map[string][]string{
	"default":       {"com.apple.coregraphics.Arrow"},
	"context-menu":  {"com.apple.cursor.24", "com.apple.coregraphics.ArrowCtx"},
	"pointer":       {"com.apple.cursor.13"},
	"grab":          {"com.apple.cursor.12"},
	"grabbing":      {"com.apple.cursor.11"},
	"alias":         {"com.apple.cursor.2", "com.apple.coregraphics.Alias"},
	"copy":          {"com.apple.cursor.5", "com.apple.coregraphics.Copy"},
	"not-allowed":   {"com.apple.cursor.3"},
	"text":          {"com.apple.coregraphics.IBeam", "com.apple.coregraphics.IBeamXOR"},
	"vertical-text": {"com.apple.cursor.26"},
	"crosshair":     {"com.apple.cursor.7", "com.apple.cursor.8"},
	"zoom-in":       {"com.apple.cursor.42"},
	"zoom-out":      {"com.apple.cursor.43"},
	"n-resize":      {"com.apple.cursor.31", "com.apple.cursor.21"},
	"s-resize":      {"com.apple.cursor.36", "com.apple.cursor.22"},
	"e-resize":      {"com.apple.cursor.27", "com.apple.cursor.18"},
	"w-resize":      {"com.apple.cursor.38", "com.apple.cursor.17"},
	"ne-resize":     {"com.apple.cursor.29"},
	"nw-resize":     {"com.apple.cursor.33"},
	"se-resize":     {"com.apple.cursor.35"},
	"sw-resize":     {"com.apple.cursor.37"},
	"ns-resize":     {"com.apple.cursor.32", "com.apple.cursor.23"},
	"ew-resize":     {"com.apple.cursor.28", "com.apple.cursor.19"},
	"nesw-resize":   {"com.apple.cursor.30"},
	"nwse-resize":   {"com.apple.cursor.34"},
	"col-resize":    {"com.apple.cursor.19", "com.apple.cursor.28"},
	"row-resize":    {"com.apple.cursor.23", "com.apple.cursor.32"},
	"move":          {"com.apple.cursor.39"},
	"all-scroll":    {"com.apple.cursor.39"},
	"help":          {"com.apple.cursor.40"},
	"progress":      {"com.apple.cursor.4"},
	"wait":          {"com.apple.coregraphics.Wait"},
}

type capeFile struct {
	Name    string                `plist:"CapeName"`
	Cursors map[string]capeCursor `plist:"Cursors"`
}

type capeCursor struct {
	FrameCount      capeNumber `plist:"FrameCount"`
	HotSpotX        capeNumber `plist:"HotSpotX"`
	HotSpotY        capeNumber `plist:"HotSpotY"`
	PointsWide      capeNumber `plist:"PointsWide"`
	PointsHigh      capeNumber `plist:"PointsHigh"`
	Representations [][]byte   `plist:"Representations"`
}

type capeNumber float64

func (number *capeNumber) UnmarshalPlist(unmarshal func(any) error) error {
	var value any
	err := unmarshal(&value)
	if err != nil {
		return err
	}
	switch value := value.(type) {
	case int64:
		*number = capeNumber(value)
	case uint64:
		*number = capeNumber(value)
	case float64:
		*number = capeNumber(value)
	case float32:
		*number = capeNumber(value)
	default:
		return E.New("cape: invalid number")
	}
	return nil
}

func parseCapeThemes(files map[string][]byte) []theme {
	var themes []theme
	for _, name := range slices.Sorted(maps.Keys(files)) {
		if !strings.EqualFold(path.Ext(name), ".cape") {
			continue
		}
		var file capeFile
		_, err := plist.Unmarshal(files[name], &file)
		if err != nil {
			continue
		}
		cursors := resolveCursors(capeRoles, func(identifier string) []frame {
			return decodeCapeCursor(file.Cursors[identifier])
		})
		if len(cursors) == 0 {
			continue
		}
		themeName := file.Name
		if themeName == "" {
			themeName = strings.TrimSuffix(path.Base(name), path.Ext(name))
		}
		themes = append(themes, theme{root: name, name: themeName, cursors: cursors})
	}
	return themes
}

// Each representation is one scale of a vertical strip of FrameCount frames, at pixelsWide / PointsWide in Mousecape
// (Mousecape 184f87b, Mousecape/Mousecape/src/models/MCCursor.m:108). WindowServer places the hotspot, given in points,
// at HotSpotX * pixelsWide / PointsWide and HotSpotY * frame pixel height / PointsHigh (SkyLight 953.5.5, macOS 27.0.1
// 26A434, UUID 8A3B348E-4637-3685-92D0-6CBC2F36A234, __CGXRegisterCursorImages 0x1872122d0..0x187212324).
func decodeCapeCursor(cursor capeCursor) []frame {
	if cursor.PointsWide <= 0 || cursor.PointsHigh <= 0 {
		return nil
	}
	count := max(int(cursor.FrameCount), 1)
	best := make(map[int]frame)
	for _, representation := range cursor.Representations {
		var decode func(io.Reader) (image.Image, error)
		var decodeConfig func(io.Reader) (image.Config, error)
		switch {
		case bytes.HasPrefix(representation, []byte("\x89PNG")):
			decode, decodeConfig = png.Decode, png.DecodeConfig
		case bytes.HasPrefix(representation, []byte("II*\x00")), bytes.HasPrefix(representation, []byte("MM\x00*")):
			decode, decodeConfig = tiff.Decode, tiff.DecodeConfig
		default:
			continue
		}
		config, err := decodeConfig(bytes.NewReader(representation))
		if err != nil || config.Width <= 0 || config.Height < count || config.Width*config.Height > maximumFileSize/4 {
			continue
		}
		width := config.Width
		if _, found := best[width]; found {
			continue
		}
		decoded, err := decode(bytes.NewReader(representation))
		if err != nil {
			continue
		}
		first := image.NewNRGBA(image.Rect(0, 0, width, decoded.Bounds().Dy()/count))
		draw.Draw(first, first.Bounds(), decoded, decoded.Bounds().Min, draw.Src)
		scale := float64(width) / float64(cursor.PointsWide)
		best[width] = frame{
			image:    first,
			hotspotX: int(math.Round(float64(cursor.HotSpotX) * scale)),
			hotspotY: int(math.Round(float64(cursor.HotSpotY) * float64(config.Height) / float64(count) / float64(cursor.PointsHigh))),
			scale:    scale,
		}
	}
	return common.Map(slices.Sorted(maps.Keys(best)), func(width int) frame {
		return best[width]
	})
}
