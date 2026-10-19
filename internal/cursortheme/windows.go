package cursortheme

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"iter"
	"maps"
	"path"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/sagernet/sing/common"
)

const windowsNominalSize = 32

// Order of the cursor list in a scheme under HKCU\Control Panel\Cursors\Schemes (main.cpl 10.0.26100.8875,
// Windows 11 26200, PDB GUID D9D8274A-EA9D-3E6C-8B4B-F04607A7A22B, sub_180007c7c 0x180007cf8..0x180007d44).
var windowsSchemeRoles = []string{
	"default", "help", "progress", "wait", "crosshair", "text", "", "not-allowed", "ns-resize", "ew-resize", "nwse-resize",
	"nesw-resize", "move", "", "pointer", "", "",
}

// Value names under HKCU\Control Panel\Cursors, which RealWorld .crs schemes also use as section names (tkdnd
// 6efca37, demos/cursors/windows/aero-pantone-2016-1.crs); precisionhair, Pan (the open hand), Grabbing, Zoom-in and
// Zoom-out are written by clickgen-generated install.inf files (clickgen v2.2.5, src/clickgen/packer/windows.py:80-103;
// Bibata_Cursor ddb0927, configs/normal/win_rg.build.toml:42-46).
var windowsValueRoles = map[string]string{
	"arrow": "default", "help": "help", "appstarting": "progress", "wait": "wait", "crosshair": "crosshair",
	"precisionhair": "crosshair", "ibeam": "text", "no": "not-allowed", "sizens": "ns-resize", "sizewe": "ew-resize",
	"sizenwse": "nwse-resize", "sizenesw": "nesw-resize", "sizeall": "move", "hand": "pointer", "pan": "grab",
	"grabbing": "grabbing", "zoom-in": "zoom-in", "zoom-out": "zoom-out",
}

type windowsScheme struct {
	name  string
	roles map[string]string
}

type windowsSchemes struct {
	current windowsScheme
	listed  []windowsScheme
}

func (s *windowsSchemes) setValue(name string, value string) {
	if name == "" {
		s.current.name = value
		return
	}
	role := windowsValueRoles[strings.ToLower(name)]
	file := windowsFileName(value)
	if role == "" || file == "" {
		return
	}
	if s.current.roles == nil {
		s.current.roles = make(map[string]string)
	}
	s.current.roles[role] = file
}

func (s *windowsSchemes) addList(name string, list string) {
	scheme := windowsScheme{name: name, roles: make(map[string]string)}
	for index, reference := range strings.Split(list, ",") {
		if file := windowsFileName(reference); index < len(windowsSchemeRoles) && windowsSchemeRoles[index] != "" && file != "" {
			scheme.roles[windowsSchemeRoles[index]] = file
		}
	}
	s.listed = append(s.listed, scheme)
}

func (s *windowsSchemes) result() []windowsScheme {
	if len(s.current.roles) == 0 {
		return s.listed
	}
	index := slices.IndexFunc(s.listed, func(scheme windowsScheme) bool {
		return scheme.name == s.current.name
	})
	if index < 0 {
		return append(s.listed, s.current)
	}
	// clickgen writes the Schemes list without empty positions, which shifts every cursor after a missing one
	// (clickgen v2.2.5, src/clickgen/packer/windows.py:130-139).
	s.listed[index].roles = s.current.roles
	return s.listed
}

func parseWindowsThemes(files map[string][]byte) []theme {
	names := slices.Sorted(maps.Keys(files))
	cursorFiles := common.Filter(names, func(name string) bool {
		extension := strings.ToLower(path.Ext(name))
		return extension == ".cur" || extension == ".ani"
	})
	var themes []theme
	seen := make(map[string]bool)
	for _, name := range names {
		var schemes []windowsScheme
		switch strings.ToLower(path.Ext(name)) {
		case ".inf":
			schemes = parseWindowsInf(decodeWindowsText(files[name]))
		case ".reg":
			schemes = parseWindowsRegistry(decodeWindowsText(files[name]))
		case ".theme":
			schemes = parseWindowsProfile(decodeWindowsText(files[name]))
		case ".crs":
			schemes = parseWindowsProfile(decodeWindowsText(files[name]))
			for index := range schemes {
				schemes[index].name = strings.TrimSuffix(path.Base(name), path.Ext(name))
			}
		default:
			continue
		}
		directory := path.Dir(name)
		directoryName := ""
		if directory != "." {
			directoryName = path.Base(directory)
		}
		for _, scheme := range schemes {
			cursors := make(map[string][]frame)
			var assignments []string
			for role, file := range scheme.roles {
				file = resolveWindowsFile(cursorFiles, file, directory)
				if file == "" {
					continue
				}
				if frames := decodeWindowsCursor(files[file]); len(frames) > 0 {
					cursors[role] = frames
					assignments = append(assignments, role+"="+file)
				}
			}
			slices.Sort(assignments)
			key := strings.Join(assignments, "\n")
			if len(cursors) == 0 || seen[key] {
				continue
			}
			seen[key] = true
			themeName := common.Find([]string{strings.TrimSpace(scheme.name), directoryName}, func(name string) bool {
				return name != "" && utf8.ValidString(name)
			})
			themes = append(themes, theme{root: directory, name: themeName, cursors: cursors})
		}
	}
	if len(themes) == 0 && len(cursorFiles) == 1 {
		if frames := decodeWindowsCursor(files[cursorFiles[0]]); len(frames) > 0 {
			themes = append(themes, theme{root: path.Dir(cursorFiles[0]), cursors: map[string][]frame{"default": frames}})
		}
	}
	return themes
}

func windowsFileName(reference string) string {
	reference = strings.TrimSpace(reference)
	return windowsFold(reference[strings.LastIndexAny(reference, `\/`)+1:])
}

func resolveWindowsFile(cursorFiles []string, file string, directory string) string {
	var resolved string
	closeness := -1
	for _, name := range cursorFiles {
		if windowsFold(path.Base(name)) != file {
			continue
		}
		rank := 0
		switch {
		case path.Dir(name) == directory:
			rank = 2
		case directory == "." || strings.HasPrefix(name, directory+"/"):
			rank = 1
		}
		if rank > closeness {
			resolved, closeness = name, rank
		}
	}
	return resolved
}

func windowsFold(name string) string {
	folded := []byte(name)
	for index, character := range folded {
		if character >= 'A' && character <= 'Z' {
			folded[index] = character + 'a' - 'A'
		}
	}
	return string(folded)
}

func parseWindowsInf(content string) []windowsScheme {
	type entry struct {
		section string
		text    string
	}
	var entries []entry
	section := ""
	for _, text := range windowsLines(content, true) {
		if header, found := strings.CutPrefix(text, "["); found {
			section, _, _ = strings.Cut(header, "]")
			section = strings.ToLower(strings.TrimSpace(section))
			continue
		}
		entries = append(entries, entry{section, text})
	}
	values := make(map[string]string)
	for _, item := range entries {
		if item.section != "strings" && !strings.HasPrefix(item.section, "strings.") {
			continue
		}
		key, value, found := strings.Cut(item.text, "=")
		key = strings.ToLower(unquoteWindowsField(key))
		if _, exists := values[key]; found && (item.section == "strings" || !exists) {
			values[key] = unquoteWindowsField(value)
		}
	}
	expand := func(text string) string {
		var builder strings.Builder
		for {
			start := strings.IndexByte(text, '%')
			if start < 0 {
				break
			}
			end := strings.IndexByte(text[start+1:], '%')
			if end < 0 {
				break
			}
			builder.WriteString(text[:start])
			if key := text[start+1 : start+1+end]; key == "" {
				builder.WriteByte('%')
			} else {
				builder.WriteString(values[strings.ToLower(key)])
			}
			text = text[start+2+end:]
		}
		builder.WriteString(text)
		return builder.String()
	}
	var schemes windowsSchemes
	for _, item := range entries {
		fields := splitWindowsFields(item.text)
		if len(fields) < 5 || !strings.EqualFold(fields[0], "HKCU") {
			continue
		}
		switch key := expand(fields[1]); {
		case strings.EqualFold(key, `Control Panel\Cursors\Schemes`):
			schemes.addList(expand(fields[2]), expand(fields[4]))
		case strings.EqualFold(key, `Control Panel\Cursors`):
			schemes.setValue(expand(fields[2]), expand(fields[4]))
		}
	}
	return schemes.result()
}

func parseWindowsRegistry(content string) []windowsScheme {
	var schemes windowsSchemes
	key := ""
	for _, text := range windowsLines(content, false) {
		if header, found := strings.CutPrefix(text, "["); found {
			key, _, _ = strings.Cut(header, "]")
			continue
		}
		name, value, found := parseWindowsRegistryValue(text)
		switch {
		case !found:
		case strings.EqualFold(key, `HKEY_CURRENT_USER\Control Panel\Cursors\Schemes`):
			schemes.addList(name, value)
		case strings.EqualFold(key, `HKEY_CURRENT_USER\Control Panel\Cursors`):
			schemes.setValue(name, value)
		}
	}
	return schemes.result()
}

func parseWindowsRegistryValue(text string) (string, string, bool) {
	var name string
	if rest, found := strings.CutPrefix(text, "@"); found {
		text = rest
	} else {
		var found bool
		name, text, found = readWindowsRegistryString(text)
		if !found {
			return "", "", false
		}
	}
	text, found := strings.CutPrefix(strings.TrimSpace(text), "=")
	if !found {
		return "", "", false
	}
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, `"`) {
		value, _, found := readWindowsRegistryString(text)
		return name, value, found
	}
	data, found := strings.CutPrefix(strings.ToLower(text), "hex(2):")
	if !found {
		return "", "", false
	}
	var content []byte
	for _, item := range strings.Split(data, ",") {
		value, err := strconv.ParseUint(strings.TrimSpace(item), 16, 8)
		if err != nil {
			return "", "", false
		}
		content = append(content, byte(value))
	}
	value, _, _ := strings.Cut(decodeUTF16(content), "\x00")
	return name, value, true
}

func readWindowsRegistryString(text string) (string, string, bool) {
	if !strings.HasPrefix(text, `"`) {
		return "", "", false
	}
	var builder strings.Builder
	for index := 1; index < len(text); index++ {
		switch text[index] {
		case '\\':
			index++
			if index < len(text) {
				builder.WriteByte(text[index])
			}
		case '"':
			return builder.String(), text[index+1:], true
		default:
			builder.WriteByte(text[index])
		}
	}
	return "", "", false
}

func parseWindowsProfile(content string) []windowsScheme {
	var schemes windowsSchemes
	section := ""
	for _, text := range windowsLines(content, false) {
		if header, found := strings.CutPrefix(text, "["); found {
			section, _, _ = strings.Cut(header, "]")
			section = strings.TrimSpace(section)
			continue
		}
		key, value, found := strings.Cut(text, "=")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		switch {
		case !found:
		case strings.EqualFold(section, `Control Panel\Cursors`):
			if strings.EqualFold(key, "DefaultValue") {
				key = ""
			}
			schemes.setValue(key, value)
		case section != "" && strings.EqualFold(key, "Path"):
			schemes.setValue(section, value)
		}
	}
	return schemes.result()
}

func windowsLines(content string, inlineComments bool) []string {
	var lines []string
	pending := ""
	for _, raw := range strings.Split(content, "\n") {
		text := strings.TrimSpace(raw)
		if inlineComments {
			quoted := false
			for index := 0; index < len(text); index++ {
				if text[index] == '"' {
					quoted = !quoted
				} else if text[index] == ';' && !quoted {
					text = strings.TrimSpace(text[:index])
					break
				}
			}
		} else if strings.HasPrefix(text, ";") {
			text = ""
		}
		if continued, found := strings.CutSuffix(text, `\`); found {
			pending += continued
			continue
		}
		if text = pending + text; text != "" {
			lines = append(lines, text)
		}
		pending = ""
	}
	return lines
}

func splitWindowsFields(text string) []string {
	var fields []string
	quoted := false
	start := 0
	for index := 0; index < len(text); index++ {
		switch {
		case text[index] == '"':
			quoted = !quoted
		case text[index] == ',' && !quoted:
			fields = append(fields, unquoteWindowsField(text[start:index]))
			start = index + 1
		}
	}
	return append(fields, unquoteWindowsField(text[start:]))
}

func unquoteWindowsField(text string) string {
	text = strings.TrimSpace(text)
	var builder strings.Builder
	quoted := false
	for index := 0; index < len(text); index++ {
		switch {
		case text[index] == '"' && quoted && index+1 < len(text) && text[index+1] == '"':
			builder.WriteByte('"')
			index++
		case text[index] == '"':
			quoted = !quoted
		default:
			builder.WriteByte(text[index])
		}
	}
	return builder.String()
}

func decodeWindowsText(content []byte) string {
	switch {
	case bytes.HasPrefix(content, []byte{0xff, 0xfe}):
		return decodeUTF16(content[2:])
	case bytes.HasPrefix(content, []byte{0xef, 0xbb, 0xbf}):
		return string(content[3:])
	}
	return string(content)
}

func decodeUTF16(content []byte) string {
	units := make([]uint16, len(content)/2)
	for index := range units {
		units[index] = binary.LittleEndian.Uint16(content[index*2:])
	}
	return string(utf16.Decode(units))
}

func decodeWindowsCursor(data []byte) []frame {
	if !bytes.HasPrefix(data, []byte("RIFF")) {
		return decodeCursor(data)
	}
	if len(data) < 12 || string(data[8:12]) != "ACON" {
		return nil
	}
	var icons [][]byte
	var sequence []byte
	for identifier, payload := range riffChunks(data[12:]) {
		switch {
		case identifier == "seq ":
			sequence = payload
		case identifier == "LIST" && len(payload) >= 4 && string(payload[:4]) == "fram":
			for identifier, payload := range riffChunks(payload[4:]) {
				if identifier == "icon" {
					icons = append(icons, payload)
				}
			}
		}
	}
	if len(icons) == 0 {
		return nil
	}
	first := 0
	if len(sequence) >= 4 {
		if index := binary.LittleEndian.Uint32(sequence); index < uint32(len(icons)) {
			first = int(index)
		}
	}
	return decodeCursor(icons[first])
}

func riffChunks(data []byte) iter.Seq2[string, []byte] {
	return func(yield func(string, []byte) bool) {
		for len(data) >= 8 {
			size := uint64(binary.LittleEndian.Uint32(data[4:]))
			if size > uint64(len(data)-8) || !yield(string(data[:4]), data[8:8+size]) {
				return
			}
			data = data[min(8+size+size%2, uint64(len(data))):]
		}
	}
}

func decodeCursor(data []byte) []frame {
	if len(data) < 6 || binary.LittleEndian.Uint16(data) != 0 {
		return nil
	}
	if kind := binary.LittleEndian.Uint16(data[2:]); kind != 1 && kind != 2 {
		return nil
	}
	type candidate struct {
		frame
		depth int
	}
	best := make(map[int]candidate)
	for index := range int(binary.LittleEndian.Uint16(data[4:])) {
		start := 6 + index*16
		if start+16 > len(data) {
			break
		}
		entry := data[start : start+16]
		size := uint64(binary.LittleEndian.Uint32(entry[8:]))
		offset := uint64(binary.LittleEndian.Uint32(entry[12:]))
		if offset+size > uint64(len(data)) {
			continue
		}
		decoded, depth := decodeCursorImage(data[offset : offset+size])
		if decoded == nil {
			continue
		}
		width, height := decoded.Bounds().Dx(), decoded.Bounds().Dy()
		if previous, found := best[width]; found && previous.depth >= depth {
			continue
		}
		// user32 of Windows 11 26200 reads the hotspot from the directory entry whenever it loads a file as a cursor,
		// even when the header says icon.
		best[width] = candidate{frame{
			image:    decoded,
			hotspotX: min(int(binary.LittleEndian.Uint16(entry[4:])), width-1),
			hotspotY: min(int(binary.LittleEndian.Uint16(entry[6:])), height-1),
			scale:    float64(width) / windowsNominalSize,
		}, depth}
	}
	return common.Map(slices.Sorted(maps.Keys(best)), func(width int) frame {
		return best[width].frame
	})
}

func decodeCursorImage(data []byte) (image.Image, int) {
	if bytes.HasPrefix(data, []byte("\x89PNG")) {
		config, err := png.DecodeConfig(bytes.NewReader(data))
		if err != nil || config.Width > 1024 || config.Height > 1024 {
			return nil, 0
		}
		decoded, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, 0
		}
		return decoded, 32
	}
	if len(data) < 40 {
		return nil, 0
	}
	headerSize := int(binary.LittleEndian.Uint32(data))
	width := int(int32(binary.LittleEndian.Uint32(data[4:])))
	height := int(int32(binary.LittleEndian.Uint32(data[8:]))) / 2
	depth := int(binary.LittleEndian.Uint16(data[14:]))
	colorsUsed := int(binary.LittleEndian.Uint32(data[32:]))
	if headerSize < 40 || headerSize > len(data) || binary.LittleEndian.Uint32(data[16:]) != 0 || width <= 0 || width > 1024 ||
		height <= 0 || height > 1024 || !slices.Contains([]int{1, 4, 8, 24, 32}, depth) {
		return nil, 0
	}
	paletteSize := 0
	if depth <= 8 {
		paletteSize = 1 << depth
		if colorsUsed > 0 && colorsUsed < paletteSize {
			paletteSize = colorsUsed
		}
	}
	colorStart := headerSize + paletteSize*4
	stride := (width*depth + 31) / 32 * 4
	maskStart := colorStart + stride*height
	maskStride := (width + 31) / 32 * 4
	if maskStart+maskStride*height > len(data) {
		return nil, 0
	}
	hasAlpha := false
	if depth == 32 {
		for y := range height {
			row := data[colorStart+y*stride : colorStart+y*stride+width*4]
			for x := 3; x < len(row) && !hasAlpha; x += 4 {
				hasAlpha = row[x] != 0
			}
		}
	}
	decoded := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		row := data[colorStart+(height-1-y)*stride:]
		mask := data[maskStart+(height-1-y)*maskStride:]
		for x := range width {
			var pixel color.NRGBA
			switch depth {
			case 32:
				pixel = color.NRGBA{R: row[x*4+2], G: row[x*4+1], B: row[x*4], A: row[x*4+3]}
			case 24:
				pixel = color.NRGBA{R: row[x*3+2], G: row[x*3+1], B: row[x*3]}
			default:
				bit := x * depth
				if index := int(row[bit/8]>>(8-depth-bit%8)) & (1<<depth - 1); index < paletteSize {
					entry := data[headerSize+index*4:]
					pixel = color.NRGBA{R: entry[2], G: entry[1], B: entry[0]}
				}
			}
			if !hasAlpha {
				switch {
				case mask[x/8]>>(7-x%8)&1 == 0:
					pixel.A = 255
				case pixel.R|pixel.G|pixel.B == 0:
					pixel = color.NRGBA{}
				default:
					pixel = color.NRGBA{A: 255}
				}
			}
			decoded.SetNRGBA(x, y, pixel)
		}
	}
	return decoded, depth
}
