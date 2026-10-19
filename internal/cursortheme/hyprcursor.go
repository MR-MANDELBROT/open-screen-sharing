package cursortheme

import (
	"archive/zip"
	"bytes"
	"image/png"
	"maps"
	"math"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/sagernet/sing/common"
)

type hyprcursorShape struct {
	archive     *zip.Reader
	hotspotX    float32
	hotspotY    float32
	nominalSize float32
	images      []hyprcursorImage
	overrides   []string
}

type hyprcursorImage struct {
	size int
	file string
}

func parseHyprcursorThemes(files map[string][]byte) []theme {
	manifests := make(map[string][]byte)
	for name, content := range files {
		switch path.Base(name) {
		case "manifest.hl":
			manifests[path.Dir(name)] = content
		case "manifest.toml":
			if _, found := files[path.Join(path.Dir(name), "manifest.hl")]; !found {
				manifests[path.Dir(name)] = content
			}
		}
	}
	var themes []theme
	for _, root := range slices.Sorted(maps.Keys(manifests)) {
		manifest := parseHyprcursorConfig(manifests[root])
		if len(manifest["cursors_directory"]) == 0 || manifest["cursors_directory"][0] == "" {
			continue
		}
		directory := path.Join(root, manifest["cursors_directory"][0])
		shapes := make(map[string]*hyprcursorShape)
		overrides := make(map[string]*hyprcursorShape)
		for _, name := range slices.Sorted(maps.Keys(files)) {
			if path.Dir(name) != directory {
				continue
			}
			shape := parseHyprcursorShape(files[name])
			if shape == nil {
				continue
			}
			shapes[strings.TrimSuffix(path.Base(name), path.Ext(name))] = shape
			for _, override := range shape.overrides {
				if _, found := overrides[override]; !found {
					overrides[override] = shape
				}
			}
		}
		cursors := resolveCursors(xcursorRoles, func(name string) []frame {
			shape, found := shapes[name]
			if !found {
				shape, found = overrides[name]
			}
			if !found {
				return nil
			}
			return shape.decode()
		})
		if len(cursors) == 0 {
			continue
		}
		name := path.Base(root)
		if root == "." {
			name = ""
		}
		if values := manifest["name"]; len(values) > 0 && values[0] != "" {
			name = values[0]
		}
		themes = append(themes, theme{root: root, name: name, cursors: cursors})
	}
	return themes
}

func parseHyprcursorConfig(content []byte) map[string][]string {
	values := make(map[string][]string)
	for _, line := range strings.Split(string(content), "\n") {
		key, value, found := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !found || key == "" || strings.HasPrefix(key, "#") || strings.HasPrefix(key, "[") {
			continue
		}
		value = strings.TrimSpace(value)
		if value != "" && (value[0] == '"' || value[0] == '\'') {
			if end := strings.IndexByte(value[1:], value[0]); end >= 0 {
				value = value[1 : end+1]
			}
		} else {
			var builder strings.Builder
			for index := 0; index < len(value); index++ {
				if value[index] == '#' {
					if index+1 < len(value) && value[index+1] == '#' {
						builder.WriteByte('#')
						index++
						continue
					}
					break
				}
				builder.WriteByte(value[index])
			}
			value = strings.TrimSpace(builder.String())
		}
		values[key] = append(values[key], value)
	}
	return values
}

func parseHyprcursorShape(content []byte) *hyprcursorShape {
	if !bytes.HasPrefix(content, []byte("PK\x03\x04")) {
		return nil
	}
	archive, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return nil
	}
	meta := readZipFile(archive, "meta.hl")
	if meta == nil {
		meta = readZipFile(archive, "meta.toml")
	}
	if meta == nil {
		return nil
	}
	values := parseHyprcursorConfig(meta)
	number := func(name string, fallback float32) float32 {
		if items := values[name]; len(items) > 0 {
			if value, err := strconv.ParseFloat(items[len(items)-1], 32); err == nil {
				return float32(value)
			}
		}
		return fallback
	}
	// libhyprcursor reads these as floats, defaulting nominal_size to 1 and clamping it to [0.1, 2] (hyprcursor v0.1.13,
	// libhyprcursor/meta.cpp:144-146, 156-158, 168-170).
	shape := &hyprcursorShape{
		archive:     archive,
		hotspotX:    number("hotspot_x", 0),
		hotspotY:    number("hotspot_y", 0),
		nominalSize: min(max(number("nominal_size", 1), 0.1), 2),
	}
	for _, value := range values["define_size"] {
		for _, item := range strings.Split(value, ";") {
			fields := strings.Split(item, ",")
			if len(fields) < 2 {
				continue
			}
			size, _ := strconv.Atoi(strings.TrimSpace(fields[0]))
			shape.images = append(shape.images, hyprcursorImage{size: size, file: strings.TrimSpace(fields[1])})
		}
	}
	for _, value := range values["define_override"] {
		for _, item := range strings.Split(value, ";") {
			if item = strings.TrimSpace(item); item != "" {
				shape.overrides = append(shape.overrides, item)
			}
		}
	}
	return shape
}

func readZipFile(archive *zip.Reader, name string) []byte {
	file := common.Find(archive.File, func(file *zip.File) bool {
		return file.Name == name
	})
	if file == nil {
		return nil
	}
	reader, err := file.Open()
	if err != nil {
		return nil
	}
	content, _ := readAll(reader, maximumFileSize, name)
	reader.Close()
	return content
}

// libhyprcursor renders a shape for cursor size S into a round(S / nominal_size) pixel square, with the hotspot at
// round(side * hotspot), both computed in float; librsvg fits an SVG's view box to that square whatever its declared
// size, which only stands in for a missing view box (hyprcursor v0.1.13, libhyprcursor/hyprcursor.cpp:369-370, 540,
// 566-569; librsvg 2.63.3, rsvg/src/structure.rs:358-372).
func (s *hyprcursorShape) decode() []frame {
	if len(s.images) == 0 {
		return nil
	}
	if strings.HasSuffix(s.images[0].file, ".svg") {
		document, err := parseSVG(readZipFile(s.archive, s.images[0].file))
		if err != nil {
			return nil
		}
		var frames []frame
		for _, scale := range scalableScales {
			side := int(math.Round(float64(float32(xcursorNominalSize*scale) / s.nominalSize)))
			if side <= 0 || side > scalableMaximumSize {
				continue
			}
			frames = append(frames, frame{
				image:    document.render(side, side).image(),
				hotspotX: int(math.Round(float64(s.hotspotX * float32(side)))),
				hotspotY: int(math.Round(float64(s.hotspotY * float32(side)))),
				scale:    scale,
			})
		}
		return frames
	}
	var frames []frame
	seen := make(map[int]bool)
	for _, item := range s.images {
		if seen[item.size] || item.size <= 0 || strings.HasSuffix(item.file, ".svg") {
			continue
		}
		content := readZipFile(s.archive, item.file)
		config, err := png.DecodeConfig(bytes.NewReader(content))
		if err != nil || config.Width*config.Height > maximumFileSize/4 {
			continue
		}
		decoded, err := png.Decode(bytes.NewReader(content))
		if err != nil {
			continue
		}
		seen[item.size] = true
		frames = append(frames, frame{
			image:    decoded,
			hotspotX: int(math.Round(float64(s.hotspotX * float32(item.size)))),
			hotspotY: int(math.Round(float64(s.hotspotY * float32(item.size)))),
			scale:    float64(item.size) * float64(s.nominalSize) / xcursorNominalSize,
		})
	}
	return frames
}
