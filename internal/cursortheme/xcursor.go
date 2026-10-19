package cursortheme

import (
	"bytes"
	"encoding/binary"
	"image"
	"maps"
	"path"
	"slices"
)

const (
	xcursorImageType   = 0xfffd0002
	xcursorNominalSize = 24
)

var xcursorRoles = map[string][]string{
	"default":       {"default", "left_ptr", "arrow", "top_left_arrow"},
	"context-menu":  {"context-menu"},
	"pointer":       {"pointer", "hand2", "pointing_hand", "hand1", "hand", "e29285e634086352946a0e7090d73106", "9d800788f1b08800ae810202380a0822"},
	"grab":          {"grab", "openhand", "5aca4d189052212118709018842178c0"},
	"grabbing":      {"grabbing", "closedhand", "208530c400c041818281048008011002"},
	"alias":         {"alias", "dnd-link", "link", "3085a0e285430894940527032f8b26df", "640fb0e74195791501fd1ed57b41487f"},
	"copy":          {"copy", "dnd-copy", "1081e37283d90000800003c07f3ef6bf", "6407b0e94181790501fd1e167b474872"},
	"not-allowed":   {"not-allowed", "crossed_circle", "forbidden", "circle", "no-drop", "03b6e0fcb3499374a867c041f52298f0"},
	"text":          {"text", "xterm", "ibeam"},
	"vertical-text": {"vertical-text"},
	"crosshair":     {"crosshair", "cross", "tcross", "cross_reverse", "diamond_cross"},
	"zoom-in":       {"zoom-in"},
	"zoom-out":      {"zoom-out"},
	"n-resize":      {"n-resize", "top_side"},
	"s-resize":      {"s-resize", "bottom_side"},
	"e-resize":      {"e-resize", "right_side"},
	"w-resize":      {"w-resize", "left_side"},
	"ne-resize":     {"ne-resize", "top_right_corner"},
	"nw-resize":     {"nw-resize", "top_left_corner"},
	"se-resize":     {"se-resize", "bottom_right_corner"},
	"sw-resize":     {"sw-resize", "bottom_left_corner"},
	"ns-resize":     {"ns-resize", "sb_v_double_arrow", "v_double_arrow", "size_ver", "00008160000006810000408080010102"},
	"ew-resize":     {"ew-resize", "sb_h_double_arrow", "h_double_arrow", "size_hor", "028006030e0e7ebffc7f7070c0600140"},
	"nesw-resize":   {"nesw-resize", "fd_double_arrow", "size_bdiag", "fcf1c3c7cd4491d801f1e1c78f100000"},
	"nwse-resize":   {"nwse-resize", "bd_double_arrow", "size_fdiag", "c7088f0f3e6c8088236ef8e1e3e70000"},
	"col-resize":    {"col-resize", "split_h"},
	"row-resize":    {"row-resize", "split_v"},
	"move":          {"move", "fleur", "size_all", "4498f0e0c1937ffe01fd06f973665830", "9081237383d90e509aa00f00170e968f"},
	"all-scroll":    {"all-scroll"},
	"help":          {"help", "question_arrow", "whats_this", "5c6cd98b3f3ebcb1f9c7f1c204630408", "d9ce0ab605698f320427677b458ad60b"},
	"progress":      {"progress", "left_ptr_watch", "half-busy", "3ecb610c1bf2410f44200f48c40d3599", "00000000000000020006000e7e9ffc3f"},
	"wait":          {"wait", "watch"},
}

func parseXcursorThemes(files map[string][]byte) []theme {
	roots := make(map[string]map[string][]byte)
	for name, content := range files {
		directory := path.Dir(name)
		if path.Base(directory) != "cursors" || !bytes.HasPrefix(content, []byte("Xcur")) {
			continue
		}
		root := path.Dir(directory)
		if roots[root] == nil {
			roots[root] = make(map[string][]byte)
		}
		roots[root][path.Base(name)] = content
	}
	var themes []theme
	for _, root := range slices.Sorted(maps.Keys(roots)) {
		cursors := resolveCursors(xcursorRoles, func(name string) []frame {
			return decodeXcursor(roots[root][name])
		})
		if len(cursors) == 0 {
			continue
		}
		themes = append(themes, theme{root: root, name: indexThemeName(files, root), cursors: cursors})
	}
	return themes
}

func decodeXcursor(data []byte) []frame {
	if len(data) < 16 {
		return nil
	}
	headerSize := int(binary.LittleEndian.Uint32(data[4:]))
	count := int(binary.LittleEndian.Uint32(data[12:]))
	if headerSize < 16 || count > 0x10000 || headerSize+count*12 > len(data) {
		return nil
	}
	var frames []frame
	seen := make(map[uint32]bool)
	for index := range count {
		entry := data[headerSize+index*12:]
		size := binary.LittleEndian.Uint32(entry[4:])
		position := int(binary.LittleEndian.Uint32(entry[8:]))
		if binary.LittleEndian.Uint32(entry) != xcursorImageType || seen[size] || size == 0 || position < 0 || position+36 > len(data) {
			continue
		}
		chunk := data[position:]
		width := int(binary.LittleEndian.Uint32(chunk[16:]))
		height := int(binary.LittleEndian.Uint32(chunk[20:]))
		if width <= 0 || height <= 0 || width > 0x7fff || height > 0x7fff || position+36+width*height*4 > len(data) {
			continue
		}
		pixels := chunk[36 : 36+width*height*4]
		decoded := image.NewRGBA(image.Rect(0, 0, width, height))
		for offset := 0; offset < len(pixels); offset += 4 {
			decoded.Pix[offset] = pixels[offset+2]
			decoded.Pix[offset+1] = pixels[offset+1]
			decoded.Pix[offset+2] = pixels[offset]
			decoded.Pix[offset+3] = pixels[offset+3]
		}
		seen[size] = true
		frames = append(frames, frame{
			image:    decoded,
			hotspotX: int(binary.LittleEndian.Uint32(chunk[24:])),
			hotspotY: int(binary.LittleEndian.Uint32(chunk[28:])),
			scale:    float64(size) / xcursorNominalSize,
		})
	}
	return frames
}
