package cursortheme

import (
	"bufio"
	"bytes"
	"encoding/json"
	"maps"
	"math"
	"path"
	"slices"
	"strings"
)

const scalableMaximumSize = 256

var scalableScales = []float64{1, 2, 3}

func parseKDEThemes(files map[string][]byte) []theme {
	roots := make(map[string]map[string]string)
	for name := range files {
		if path.Base(name) != "metadata.json" {
			continue
		}
		directory := path.Dir(name)
		container := path.Dir(directory)
		if path.Base(container) != "cursors_scalable" {
			continue
		}
		root := path.Dir(container)
		if roots[root] == nil {
			roots[root] = make(map[string]string)
		}
		roots[root][path.Base(directory)] = directory
	}
	var themes []theme
	for _, root := range slices.Sorted(maps.Keys(roots)) {
		cursors := resolveCursors(xcursorRoles, func(name string) []frame {
			directory, found := roots[root][name]
			if !found {
				return nil
			}
			return decodeKDECursor(files, directory)
		})
		if len(cursors) == 0 {
			continue
		}
		themes = append(themes, theme{root: root, name: indexThemeName(files, root), cursors: cursors})
	}
	return themes
}

// KWin renders a cursors_scalable entry for cursor size S at output scale k into round(round(default size *
// S / nominal_size) * k) pixels, where the default size comes from QtSvg, with the hotspot, given in default size
// units, at hotspot * S / nominal_size * k. QSvgRenderer keeps Qt::IgnoreAspectRatio, which stretches the view box over
// the image whatever the root preserveAspectRatio says (KWin v6.8.0, src/utils/svgcursorreader.cpp:124-143; qtbase
// v6.12.0, src/corelib/tools/qsize.h:72-73; qtsvg v6.12.0, src/svg/qsvgrenderer.cpp:300-316,
// src/svg/qsvgdocument.cpp:459-469, src/svg/qsvgdocument_p.h:113).
func decodeKDECursor(files map[string][]byte, directory string) []frame {
	var entries []struct {
		Filename    string  `json:"filename"`
		HotspotX    float64 `json:"hotspot_x"`
		HotspotY    float64 `json:"hotspot_y"`
		NominalSize float64 `json:"nominal_size"`
	}
	err := json.Unmarshal(files[path.Join(directory, "metadata.json")], &entries)
	if err != nil || len(entries) == 0 || entries[0].NominalSize <= 0 {
		return nil
	}
	entry := entries[0]
	document, err := parseSVG(files[path.Join(directory, entry.Filename)])
	if err != nil {
		return nil
	}
	document.root.attributes["preserveAspectRatio"] = "none"
	factor := xcursorNominalSize / entry.NominalSize
	var frames []frame
	for _, scale := range scalableScales {
		width := int(math.Round(math.Round(document.width*factor) * scale))
		height := int(math.Round(math.Round(document.height*factor) * scale))
		if width <= 0 || height <= 0 || width > scalableMaximumSize || height > scalableMaximumSize {
			continue
		}
		frames = append(frames, frame{
			image:    document.render(width, height).image(),
			hotspotX: int(math.Round(entry.HotspotX * factor * scale)),
			hotspotY: int(math.Round(entry.HotspotY * factor * scale)),
			scale:    scale,
		})
	}
	return frames
}

func indexThemeName(files map[string][]byte, root string) string {
	name := path.Base(root)
	if root == "." {
		name = ""
	}
	index, found := files[path.Join(root, "index.theme")]
	if !found {
		return name
	}
	scanner := bufio.NewScanner(bytes.NewReader(index))
	section := ""
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "[") {
			section = line
		} else if value, found := strings.CutPrefix(line, "Name="); found && section == "[Icon Theme]" && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return name
}
