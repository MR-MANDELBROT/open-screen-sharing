package cursortheme

import (
	"bytes"
	"encoding/xml"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"

	E "github.com/sagernet/sing/common/exceptions"
)

const (
	svgMaximumDepth = 32
	svgBudget       = 20000
)

var svgNamedColors = map[string][3]uint8{
	"black": {0, 0, 0}, "white": {255, 255, 255}, "red": {255, 0, 0}, "lime": {0, 255, 0}, "green": {0, 128, 0},
	"blue": {0, 0, 255}, "yellow": {255, 255, 0}, "cyan": {0, 255, 255}, "aqua": {0, 255, 255},
	"magenta": {255, 0, 255}, "fuchsia": {255, 0, 255}, "gray": {128, 128, 128}, "grey": {128, 128, 128},
	"silver": {192, 192, 192}, "maroon": {128, 0, 0}, "olive": {128, 128, 0}, "navy": {0, 0, 128},
	"purple": {128, 0, 128}, "teal": {0, 128, 128}, "orange": {255, 165, 0}, "darkgray": {169, 169, 169},
	"darkgrey": {169, 169, 169}, "lightgray": {211, 211, 211}, "lightgrey": {211, 211, 211},
	"dimgray": {105, 105, 105}, "dimgrey": {105, 105, 105}, "whitesmoke": {245, 245, 245},
}

type svgNode struct {
	name       string
	attributes map[string]string
	children   []*svgNode
	text       string
}

type svgDocument struct {
	root    *svgNode
	ids     map[string]*svgNode
	width   float64
	height  float64
	viewBox [4]float64
}

type svgStyle struct {
	fill          string
	stroke        string
	fillOpacity   float64
	strokeOpacity float64
	fillRule      string
	clipRule      string
	strokeWidth   float64
	lineCap       string
	lineJoin      string
	miterLimit    float64
	dashArray     string
	dashOffset    float64
	color         string
	visibility    string
}

var svgDefaultStyle = svgStyle{
	fill: "black", stroke: "none", fillOpacity: 1, strokeOpacity: 1, fillRule: "nonzero", clipRule: "nonzero",
	strokeWidth: 1, lineCap: "butt", lineJoin: "miter", miterLimit: 4, dashArray: "none", color: "black",
	visibility: "visible",
}

type svgRenderer struct {
	document *svgDocument
	width    int
	height   int
	budget   int
}

type paint struct {
	color    [4]float32
	gradient *gradient
}

type gradient struct {
	radial  bool
	start   point
	end     point
	radius  float64
	inverse matrix
	spread  string
	offsets []float64
	colors  [][4]float32
}

type bounds struct {
	minX, minY, maxX, maxY float64
}

func parseSVG(data []byte) (*svgDocument, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	decoder.Strict = false
	decoder.CharsetReader = func(_ string, input io.Reader) (io.Reader, error) {
		return input, nil
	}
	document := &svgDocument{ids: make(map[string]*svgNode)}
	var stack []*svgNode
	var styleSheets []string
	var space string
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, E.Cause(err, "parse svg")
		}
		switch token := token.(type) {
		case xml.StartElement:
			var parent *svgNode
			if len(stack) > 0 {
				parent = stack[len(stack)-1]
			}
			if len(stack) > 0 && (parent == nil || token.Name.Space != "" && token.Name.Space != space) {
				stack = append(stack, nil)
				continue
			}
			node := &svgNode{name: token.Name.Local, attributes: make(map[string]string)}
			for _, attribute := range token.Attr {
				if attribute.Name.Space == "" || attribute.Name.Space == "http://www.w3.org/1999/xlink" || attribute.Name.Space == "xlink" {
					node.attributes[attribute.Name.Local] = attribute.Value
				}
			}
			if id := node.attributes["id"]; id != "" {
				if _, found := document.ids[id]; !found {
					document.ids[id] = node
				}
			}
			if parent != nil {
				parent.children = append(parent.children, node)
			} else if document.root == nil && node.name == "svg" {
				document.root = node
				space = token.Name.Space
			} else {
				node = nil
			}
			stack = append(stack, node)
		case xml.EndElement:
			if len(stack) == 0 {
				continue
			}
			if node := stack[len(stack)-1]; node != nil && node.name == "style" {
				styleSheets = append(styleSheets, node.text)
			}
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) > 0 && stack[len(stack)-1] != nil && stack[len(stack)-1].name == "style" {
				stack[len(stack)-1].text += string(token)
			}
		}
	}
	if document.root == nil {
		return nil, E.New("svg: missing svg element")
	}
	applyStyleSheets(document.root, parseStyleSheets(styleSheets))
	viewBox := parseNumbers(document.root.attributes["viewBox"])
	hasViewBox := len(viewBox) == 4 && viewBox[2] > 0 && viewBox[3] > 0
	if hasViewBox {
		document.viewBox = [4]float64(viewBox)
	}
	// QtSvg (which KWin renders cursors with) reads the root width and height with only the units %, px, pc, pt, mm,
	// cm and in, as 0 when the number runs into an e (em, ex), converts only mm, cm and in (at 90 dpi), and truncates
	// them to integers. Without a view box it uses the untruncated width and height, both times 1.25 when the height is
	// in pt. Its default size is the rounded view box size when either truncated dimension is missing or below 1, and
	// otherwise resolves a percentage against the view box size (qtsvg v6.12.0, src/svg/qsvghandler.cpp:3093-3118,
	// src/svg/qsvgdocument_p.h:130-154; qtbase v6.12.0, src/gui/util/qguisvg.cpp:86-99, 133-137, 151-220).
	rootLength := func(name string) (float64, string, bool) {
		value := strings.TrimSpace(document.root.attributes[name])
		if value == "" {
			return 0, "", false
		}
		unit := "px"
		for _, suffix := range []string{"%", "px", "pc", "pt", "mm", "cm", "in"} {
			if number, found := strings.CutSuffix(value, suffix); found {
				value, unit = number, suffix
				break
			}
		}
		scanner := svgScanner{input: value}
		number, _ := scanner.number()
		if rest := value[scanner.position:]; strings.HasPrefix(rest, "e") || strings.HasPrefix(rest, "E") {
			number = 0
		}
		switch unit {
		case "mm":
			number *= 3.543307
		case "cm":
			number *= 35.43307
		case "in":
			number *= 90
		}
		return number, unit, true
	}
	width, widthUnit, hasWidth := rootLength("width")
	height, heightUnit, hasHeight := rootLength("height")
	if !hasViewBox {
		scale := 1.0
		if heightUnit == "pt" {
			scale = 1.25
		}
		if width > 0 && height > 0 {
			document.viewBox = [4]float64{0, 0, width * scale, height * scale}
		} else {
			document.viewBox = [4]float64{0, 0, 100, 100}
		}
	}
	document.width, document.height = math.Trunc(width), math.Trunc(height)
	if !hasWidth || !hasHeight || document.width < 1 || document.height < 1 {
		document.width, document.height = math.Round(document.viewBox[2]), math.Round(document.viewBox[3])
	} else {
		if widthUnit == "%" {
			document.width = math.Round(0.01 * document.width * document.viewBox[2])
		}
		if heightUnit == "%" {
			document.height = math.Round(0.01 * document.height * document.viewBox[3])
		}
	}
	if document.width <= 0 || document.height <= 0 {
		return nil, E.New("svg: invalid size")
	}
	return document, nil
}

type styleRule struct {
	selector    string
	specificity int
	properties  [][2]string
}

func parseStyleSheets(sheets []string) []styleRule {
	var rules []styleRule
	for _, sheet := range sheets {
		for {
			start := strings.Index(sheet, "/*")
			if start < 0 {
				break
			}
			end := strings.Index(sheet[start+2:], "*/")
			if end < 0 {
				sheet = sheet[:start]
				break
			}
			sheet = sheet[:start] + sheet[start+2+end+2:]
		}
		for {
			open := strings.Index(sheet, "{")
			if open < 0 {
				break
			}
			selectors := strings.TrimSpace(sheet[:open])
			depth, end := 0, -1
			for index := open; index < len(sheet); index++ {
				if sheet[index] == '{' {
					depth++
				} else if sheet[index] == '}' {
					depth--
					if depth == 0 {
						end = index
						break
					}
				}
			}
			if end < 0 {
				break
			}
			body := sheet[open+1 : end]
			sheet = sheet[end+1:]
			if strings.HasPrefix(selectors, "@") {
				continue
			}
			properties := parseDeclarations(body)
			for _, selector := range strings.Split(selectors, ",") {
				selector = strings.TrimSpace(selector)
				if selector == "" || strings.ContainsAny(selector, " >+~[:") {
					continue
				}
				specificity := 0
				for _, character := range selector {
					switch character {
					case '#':
						specificity += 100
					case '.':
						specificity += 10
					}
				}
				if selector[0] != '#' && selector[0] != '.' && selector[0] != '*' {
					specificity++
				}
				rules = append(rules, styleRule{selector: selector, specificity: specificity, properties: properties})
			}
		}
	}
	slices.SortStableFunc(rules, func(left, right styleRule) int {
		return left.specificity - right.specificity
	})
	return rules
}

func parseDeclarations(body string) [][2]string {
	var properties [][2]string
	for _, declaration := range strings.Split(body, ";") {
		name, value, found := strings.Cut(declaration, ":")
		if !found {
			continue
		}
		value = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(value), "!important"))
		properties = append(properties, [2]string{strings.ToLower(strings.TrimSpace(name)), value})
	}
	return properties
}

func applyStyleSheets(node *svgNode, rules []styleRule) {
	classes := strings.Fields(node.attributes["class"])
	for _, rule := range rules {
		matched := rule.selector == "*"
		if !matched {
			rest := rule.selector
			name := rest
			if index := strings.IndexAny(rest, ".#"); index >= 0 {
				name, rest = rest[:index], rest[index:]
			} else {
				rest = ""
			}
			matched = name == "" || name == node.name
			for matched && rest != "" {
				kind := rest[0]
				rest = rest[1:]
				value := rest
				if index := strings.IndexAny(rest, ".#"); index >= 0 {
					value, rest = rest[:index], rest[index:]
				} else {
					rest = ""
				}
				if kind == '#' {
					matched = node.attributes["id"] == value
				} else {
					matched = slices.Contains(classes, value)
				}
			}
		}
		if matched {
			for _, property := range rule.properties {
				node.attributes[property[0]] = property[1]
			}
		}
	}
	for _, property := range parseDeclarations(node.attributes["style"]) {
		node.attributes[property[0]] = property[1]
	}
	for _, child := range node.children {
		applyStyleSheets(child, rules)
	}
}

func parseNumbers(value string) []float64 {
	scanner := svgScanner{input: value}
	var numbers []float64
	for {
		number, ok := scanner.number()
		if !ok {
			return numbers
		}
		numbers = append(numbers, number)
	}
}

// QtSvg and librsvg's C API, which libhyprcursor renders with, convert pt, mm, cm and in at 90 dpi (qtbase v6.12.0,
// src/gui/util/qguisvg.cpp:192-220; librsvg 2.63.3, librsvg-c/src/dpi.rs:14-15, 31-45).
func parseLength(value string, reference float64) (float64, bool) {
	value = strings.TrimSpace(value)
	scanner := svgScanner{input: value}
	number, ok := scanner.number()
	if !ok {
		return 0, false
	}
	switch strings.ToLower(strings.TrimSpace(value[scanner.position:])) {
	case "", "px", "pc":
		return number, true
	case "pt":
		return number * 1.25, true
	case "mm":
		return number * 3.543307, true
	case "cm":
		return number * 35.43307, true
	case "in":
		return number * 90, true
	case "em":
		return number * 16, true
	case "ex":
		return number * 8, true
	case "%":
		return number * reference / 100, true
	}
	return number, true
}

func parseColor(value string) ([4]float32, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "transparent" {
		return [4]float32{}, true
	}
	if named, found := svgNamedColors[value]; found {
		return [4]float32{float32(named[0]) / 255, float32(named[1]) / 255, float32(named[2]) / 255, 1}, true
	}
	if hex, found := strings.CutPrefix(value, "#"); found {
		digits := make([]float32, 0, 8)
		for _, character := range hex {
			digit, err := strconv.ParseUint(string(character), 16, 8)
			if err != nil {
				return [4]float32{}, false
			}
			digits = append(digits, float32(digit))
		}
		switch len(digits) {
		case 3, 4:
			color := [4]float32{digits[0] / 15, digits[1] / 15, digits[2] / 15, 1}
			if len(digits) == 4 {
				color[3] = digits[3] / 15
			}
			return color, true
		case 6, 8:
			color := [4]float32{(digits[0]*16 + digits[1]) / 255, (digits[2]*16 + digits[3]) / 255, (digits[4]*16 + digits[5]) / 255, 1}
			if len(digits) == 8 {
				color[3] = (digits[6]*16 + digits[7]) / 255
			}
			return color, true
		}
		return [4]float32{}, false
	}
	if strings.HasPrefix(value, "rgb") {
		open, end := strings.Index(value, "("), strings.LastIndex(value, ")")
		if open < 0 || end < open {
			return [4]float32{}, false
		}
		fields := strings.FieldsFunc(value[open+1:end], func(character rune) bool {
			return character == ',' || character == ' ' || character == '/' || character == '\t'
		})
		if len(fields) < 3 {
			return [4]float32{}, false
		}
		color := [4]float32{0, 0, 0, 1}
		for index, field := range fields[:min(len(fields), 4)] {
			scale := float64(255)
			if index == 3 {
				scale = 1
			}
			number, ok := parseLength(field, scale)
			if !ok {
				return [4]float32{}, false
			}
			color[index] = float32(min(max(number/scale, 0), 1))
		}
		return color, true
	}
	return [4]float32{}, false
}

func parseOpacity(value string, fallback float64) float64 {
	number, ok := parseLength(value, 1)
	if !ok {
		return fallback
	}
	return min(max(number, 0), 1)
}

func parseTransform(value string) matrix {
	result := identityMatrix
	for {
		open := strings.Index(value, "(")
		end := strings.Index(value, ")")
		if open < 0 || end < open {
			return result
		}
		name := strings.Trim(strings.TrimSpace(value[:open]), ",")
		name = strings.TrimSpace(name)
		arguments := parseNumbers(value[open+1 : end])
		value = value[end+1:]
		argument := func(index int, fallback float64) float64 {
			if index < len(arguments) {
				return arguments[index]
			}
			return fallback
		}
		var transform matrix
		switch name {
		case "matrix":
			if len(arguments) != 6 {
				continue
			}
			transform = matrix{arguments[0], arguments[1], arguments[2], arguments[3], arguments[4], arguments[5]}
		case "translate":
			transform = matrix{a: 1, d: 1, e: argument(0, 0), f: argument(1, 0)}
		case "scale":
			transform = matrix{a: argument(0, 1), d: argument(1, argument(0, 1))}
		case "rotate":
			angle := argument(0, 0) * math.Pi / 180
			cos, sin := math.Cos(angle), math.Sin(angle)
			centerX, centerY := argument(1, 0), argument(2, 0)
			transform = matrix{a: 1, d: 1, e: centerX, f: centerY}.
				multiply(matrix{a: cos, b: sin, c: -sin, d: cos}).
				multiply(matrix{a: 1, d: 1, e: -centerX, f: -centerY})
		case "skewX":
			transform = matrix{a: 1, c: math.Tan(argument(0, 0) * math.Pi / 180), d: 1}
		case "skewY":
			transform = matrix{a: 1, b: math.Tan(argument(0, 0) * math.Pi / 180), d: 1}
		default:
			continue
		}
		result = result.multiply(transform)
	}
}

type svgScanner struct {
	input    string
	position int
}

func (s *svgScanner) skip() {
	for s.position < len(s.input) && strings.IndexByte(" \t\r\n,", s.input[s.position]) >= 0 {
		s.position++
	}
}

func (s *svgScanner) number() (float64, bool) {
	s.skip()
	start := s.position
	if s.position < len(s.input) && (s.input[s.position] == '+' || s.input[s.position] == '-') {
		s.position++
	}
	digits := 0
	for s.position < len(s.input) && s.input[s.position] >= '0' && s.input[s.position] <= '9' {
		s.position++
		digits++
	}
	if s.position < len(s.input) && s.input[s.position] == '.' {
		s.position++
		for s.position < len(s.input) && s.input[s.position] >= '0' && s.input[s.position] <= '9' {
			s.position++
			digits++
		}
	}
	if digits == 0 {
		s.position = start
		return 0, false
	}
	if s.position < len(s.input) && (s.input[s.position] == 'e' || s.input[s.position] == 'E') {
		exponent := s.position + 1
		if exponent < len(s.input) && (s.input[exponent] == '+' || s.input[exponent] == '-') {
			exponent++
		}
		if exponent < len(s.input) && s.input[exponent] >= '0' && s.input[exponent] <= '9' {
			for exponent < len(s.input) && s.input[exponent] >= '0' && s.input[exponent] <= '9' {
				exponent++
			}
			s.position = exponent
		}
	}
	number, err := strconv.ParseFloat(s.input[start:s.position], 64)
	if err != nil {
		s.position = start
		return 0, false
	}
	return number, true
}

func (s *svgScanner) flag() (bool, bool) {
	s.skip()
	if s.position < len(s.input) && (s.input[s.position] == '0' || s.input[s.position] == '1') {
		s.position++
		return s.input[s.position-1] == '1', true
	}
	return false, false
}

func buildPath(data string, builder *pathBuilder) {
	scanner := svgScanner{input: data}
	var command byte
	var control point
	var previous byte
	numbers := func(count int) ([]float64, bool) {
		values := make([]float64, count)
		for index := range values {
			value, ok := scanner.number()
			if !ok {
				return nil, false
			}
			values[index] = value
		}
		return values, true
	}
	for {
		scanner.skip()
		if scanner.position >= len(data) {
			return
		}
		if character := data[scanner.position]; character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z' {
			command = character
			scanner.position++
		} else if command == 0 {
			return
		}
		relative := command >= 'a'
		current := builder.current
		offset := func(x, y float64) point {
			if relative {
				return point{current.x + x, current.y + y}
			}
			return point{x, y}
		}
		upper := command &^ 0x20
		switch upper {
		case 'Z':
			builder.close()
			command, previous = 0, 'Z'
			continue
		case 'M', 'L', 'T':
			values, ok := numbers(2)
			if !ok {
				return
			}
			target := offset(values[0], values[1])
			switch upper {
			case 'M':
				builder.moveTo(target)
				if relative {
					command = 'l'
				} else {
					command = 'L'
				}
			case 'L':
				builder.lineTo(target)
			case 'T':
				reflected := current
				if previous == 'Q' || previous == 'T' {
					reflected = point{2*current.x - control.x, 2*current.y - control.y}
				}
				builder.quadTo(reflected, target)
				control = reflected
			}
		case 'H', 'V':
			values, ok := numbers(1)
			if !ok {
				return
			}
			target := current
			switch {
			case upper == 'H' && relative:
				target.x += values[0]
			case upper == 'H':
				target.x = values[0]
			case relative:
				target.y += values[0]
			default:
				target.y = values[0]
			}
			builder.lineTo(target)
		case 'C', 'S':
			count := 6
			if upper == 'S' {
				count = 4
			}
			values, ok := numbers(count)
			if !ok {
				return
			}
			first := current
			if upper == 'C' {
				first = offset(values[0], values[1])
				values = values[2:]
			} else if previous == 'C' || previous == 'S' {
				first = point{2*current.x - control.x, 2*current.y - control.y}
			}
			control = offset(values[0], values[1])
			builder.cubicTo(first, control, offset(values[2], values[3]))
		case 'Q':
			values, ok := numbers(4)
			if !ok {
				return
			}
			control = offset(values[0], values[1])
			builder.quadTo(control, offset(values[2], values[3]))
		case 'A':
			values, ok := numbers(3)
			if !ok {
				return
			}
			large, ok := scanner.flag()
			if !ok {
				return
			}
			sweep, ok := scanner.flag()
			if !ok {
				return
			}
			target, ok := numbers(2)
			if !ok {
				return
			}
			builder.arcTo(values[0], values[1], values[2], large, sweep, offset(target[0], target[1]))
		default:
			return
		}
		previous = upper
	}
}

func (document *svgDocument) render(width int, height int) *layer {
	renderer := &svgRenderer{document: document, width: width, height: height, budget: svgBudget}
	viewBox := document.viewBox
	scaleX, scaleY := float64(width)/viewBox[2], float64(height)/viewBox[3]
	translateX, translateY := 0.0, 0.0
	aspect := strings.Fields(document.root.attributes["preserveAspectRatio"])
	if len(aspect) == 0 || aspect[0] != "none" {
		align := "xMidYMid"
		if len(aspect) > 0 {
			align = aspect[0]
		}
		scale := min(scaleX, scaleY)
		if len(aspect) > 1 && aspect[1] == "slice" {
			scale = max(scaleX, scaleY)
		}
		scaleX, scaleY = scale, scale
		switch {
		case strings.HasPrefix(align, "xMid"):
			translateX = (float64(width) - viewBox[2]*scale) / 2
		case strings.HasPrefix(align, "xMax"):
			translateX = float64(width) - viewBox[2]*scale
		}
		switch {
		case strings.HasSuffix(align, "YMid"):
			translateY = (float64(height) - viewBox[3]*scale) / 2
		case strings.HasSuffix(align, "YMax"):
			translateY = float64(height) - viewBox[3]*scale
		}
	}
	base := matrix{a: scaleX, d: scaleY, e: translateX - viewBox[0]*scaleX, f: translateY - viewBox[1]*scaleY}
	target := newLayer(width, height)
	renderer.draw(document.root, base, svgDefaultStyle, target, 0)
	return target
}

func (r *svgRenderer) reference(value string) *svgNode {
	value = strings.TrimSpace(value)
	if inner, found := strings.CutPrefix(value, "url("); found {
		end := strings.Index(inner, ")")
		if end < 0 {
			return nil
		}
		value = strings.Trim(strings.TrimSpace(inner[:end]), `"'`)
	}
	id, found := strings.CutPrefix(value, "#")
	if !found {
		return nil
	}
	return r.document.ids[id]
}

func nodeLength(node *svgNode, name string, reference float64) float64 {
	value, _ := parseLength(node.attributes[name], reference)
	return value
}

func (r *svgRenderer) diagonal() float64 {
	return math.Hypot(r.document.viewBox[2], r.document.viewBox[3]) / math.Sqrt2
}

func (r *svgRenderer) inherit(style svgStyle, node *svgNode) svgStyle {
	value := func(name string) (string, bool) {
		value := strings.TrimSpace(node.attributes[name])
		return value, value != "" && value != "inherit"
	}
	for name, field := range map[string]*string{
		"fill": &style.fill, "stroke": &style.stroke, "fill-rule": &style.fillRule, "clip-rule": &style.clipRule,
		"stroke-linecap": &style.lineCap, "stroke-linejoin": &style.lineJoin, "stroke-dasharray": &style.dashArray,
		"color": &style.color, "visibility": &style.visibility,
	} {
		if text, ok := value(name); ok {
			*field = text
		}
	}
	if text, ok := value("fill-opacity"); ok {
		style.fillOpacity = parseOpacity(text, style.fillOpacity)
	}
	if text, ok := value("stroke-opacity"); ok {
		style.strokeOpacity = parseOpacity(text, style.strokeOpacity)
	}
	if text, ok := value("stroke-width"); ok {
		if width, ok := parseLength(text, r.diagonal()); ok && width >= 0 {
			style.strokeWidth = width
		}
	}
	if text, ok := value("stroke-miterlimit"); ok {
		if limit, ok := parseLength(text, 0); ok && limit >= 1 {
			style.miterLimit = limit
		}
	}
	if text, ok := value("stroke-dashoffset"); ok {
		style.dashOffset, _ = parseLength(text, r.diagonal())
	}
	return style
}

func (r *svgRenderer) draw(node *svgNode, transform matrix, style svgStyle, target *layer, depth int) {
	if depth > svgMaximumDepth || r.budget <= 0 || strings.TrimSpace(node.attributes["display"]) == "none" {
		return
	}
	r.budget--
	style = r.inherit(style, node)
	transform = transform.multiply(parseTransform(node.attributes["transform"]))
	opacity := parseOpacity(node.attributes["opacity"], 1)
	filter := r.reference(node.attributes["filter"])
	clip := r.reference(node.attributes["clip-path"])
	mask := r.reference(node.attributes["mask"])
	if filter != nil && filter.name != "filter" {
		filter = nil
	}
	if clip != nil && clip.name != "clipPath" {
		clip = nil
	}
	if mask != nil && mask.name != "mask" {
		mask = nil
	}
	destination := target
	isolated := opacity < 1 || filter != nil || clip != nil || mask != nil
	if isolated {
		destination = newLayer(r.width, r.height)
	}
	switch node.name {
	case "svg", "g", "a", "switch":
		if node.name == "svg" && node != r.document.root {
			transform = transform.multiply(matrix{a: 1, d: 1, e: nodeLength(node, "x", r.document.viewBox[2]), f: nodeLength(node, "y", r.document.viewBox[3])})
		}
		for _, child := range node.children {
			if child.name != "symbol" {
				r.draw(child, transform, style, destination, depth+1)
			}
		}
	case "use":
		if referenced := r.reference(node.attributes["href"]); referenced != nil && referenced != node {
			offset := matrix{a: 1, d: 1, e: nodeLength(node, "x", r.document.viewBox[2]), f: nodeLength(node, "y", r.document.viewBox[3])}
			if referenced.name == "symbol" {
				symbol := *referenced
				symbol.name = "g"
				referenced = &symbol
			}
			r.draw(referenced, transform.multiply(offset), style, destination, depth+1)
		}
	case "path", "rect", "circle", "ellipse", "line", "polyline", "polygon":
		r.drawShape(node, transform, style, destination)
	}
	if !isolated {
		return
	}
	if filter != nil {
		destination = r.applyFilter(filter, destination, transform, style)
	}
	if clip != nil {
		destination.multiply(r.clipCoverage(clip, node, transform, depth))
	}
	if mask != nil {
		r.applyMask(mask, node, destination, transform, depth)
	}
	target.composite(destination, float32(opacity))
}

func (r *svgRenderer) geometry(node *svgNode, tolerance float64) []contour {
	builder := pathBuilder{tolerance: tolerance}
	width, height := r.document.viewBox[2], r.document.viewBox[3]
	ellipse := func(centerX, centerY, radiusX, radiusY float64) {
		if radiusX <= 0 || radiusY <= 0 {
			return
		}
		builder.moveTo(point{centerX + radiusX, centerY})
		builder.arcTo(radiusX, radiusY, 0, false, true, point{centerX - radiusX, centerY})
		builder.arcTo(radiusX, radiusY, 0, false, true, point{centerX + radiusX, centerY})
		builder.close()
	}
	switch node.name {
	case "path":
		buildPath(node.attributes["d"], &builder)
	case "rect":
		x, y := nodeLength(node, "x", width), nodeLength(node, "y", height)
		w, h := nodeLength(node, "width", width), nodeLength(node, "height", height)
		if w <= 0 || h <= 0 {
			return nil
		}
		radiusX, hasX := parseLength(node.attributes["rx"], width)
		radiusY, hasY := parseLength(node.attributes["ry"], height)
		if !hasX {
			radiusX = radiusY
		}
		if !hasY {
			radiusY = radiusX
		}
		radiusX, radiusY = min(max(radiusX, 0), w/2), min(max(radiusY, 0), h/2)
		if radiusX == 0 || radiusY == 0 {
			builder.moveTo(point{x, y})
			builder.lineTo(point{x + w, y})
			builder.lineTo(point{x + w, y + h})
			builder.lineTo(point{x, y + h})
			builder.close()
			break
		}
		builder.moveTo(point{x + radiusX, y})
		builder.lineTo(point{x + w - radiusX, y})
		builder.arcTo(radiusX, radiusY, 0, false, true, point{x + w, y + radiusY})
		builder.lineTo(point{x + w, y + h - radiusY})
		builder.arcTo(radiusX, radiusY, 0, false, true, point{x + w - radiusX, y + h})
		builder.lineTo(point{x + radiusX, y + h})
		builder.arcTo(radiusX, radiusY, 0, false, true, point{x, y + h - radiusY})
		builder.lineTo(point{x, y + radiusY})
		builder.arcTo(radiusX, radiusY, 0, false, true, point{x + radiusX, y})
		builder.close()
	case "circle":
		radius := nodeLength(node, "r", r.diagonal())
		ellipse(nodeLength(node, "cx", width), nodeLength(node, "cy", height), radius, radius)
	case "ellipse":
		radiusX, hasX := parseLength(node.attributes["rx"], width)
		radiusY, hasY := parseLength(node.attributes["ry"], height)
		if !hasX {
			radiusX = radiusY
		}
		if !hasY {
			radiusY = radiusX
		}
		ellipse(nodeLength(node, "cx", width), nodeLength(node, "cy", height), radiusX, radiusY)
	case "line":
		builder.moveTo(point{nodeLength(node, "x1", width), nodeLength(node, "y1", height)})
		builder.lineTo(point{nodeLength(node, "x2", width), nodeLength(node, "y2", height)})
	case "polyline", "polygon":
		numbers := parseNumbers(node.attributes["points"])
		for index := 0; index+1 < len(numbers); index += 2 {
			if index == 0 {
				builder.moveTo(point{numbers[0], numbers[1]})
			} else {
				builder.lineTo(point{numbers[index], numbers[index+1]})
			}
		}
		if node.name == "polygon" {
			builder.close()
		}
	}
	return builder.contours
}

func contourBounds(contours []contour, transform matrix) (bounds, bool) {
	result := bounds{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
	for _, item := range contours {
		for _, original := range item.points {
			p := transform.apply(original)
			result = bounds{min(result.minX, p.x), min(result.minY, p.y), max(result.maxX, p.x), max(result.maxY, p.y)}
		}
	}
	return result, result.minX <= result.maxX
}

func (r *svgRenderer) bounds(node *svgNode, transform matrix, depth int) (bounds, bool) {
	if depth > svgMaximumDepth || r.budget <= 0 || strings.TrimSpace(node.attributes["display"]) == "none" {
		return bounds{}, false
	}
	r.budget--
	switch node.name {
	case "path", "rect", "circle", "ellipse", "line", "polyline", "polygon":
		return contourBounds(r.geometry(node, 0.1/max(transform.scaleFactor(), 1e-9)), transform)
	case "use":
		if referenced := r.reference(node.attributes["href"]); referenced != nil && referenced != node {
			offset := matrix{a: 1, d: 1, e: nodeLength(node, "x", r.document.viewBox[2]), f: nodeLength(node, "y", r.document.viewBox[3])}
			return r.bounds(referenced, transform.multiply(offset).multiply(parseTransform(referenced.attributes["transform"])), depth+1)
		}
	case "svg", "g", "a", "switch", "symbol":
		result, found := bounds{}, false
		for _, child := range node.children {
			if child.name == "symbol" {
				continue
			}
			if item, ok := r.bounds(child, transform.multiply(parseTransform(child.attributes["transform"])), depth+1); ok {
				if !found {
					result, found = item, true
				} else {
					result = bounds{min(result.minX, item.minX), min(result.minY, item.minY), max(result.maxX, item.maxX), max(result.maxY, item.maxY)}
				}
			}
		}
		return result, found
	}
	return bounds{}, false
}

func (r *svgRenderer) boundingBoxMatrix(node *svgNode, depth int) (matrix, bool) {
	box, ok := r.bounds(node, identityMatrix, depth)
	if !ok || box.maxX <= box.minX || box.maxY <= box.minY {
		return identityMatrix, false
	}
	return matrix{a: box.maxX - box.minX, d: box.maxY - box.minY, e: box.minX, f: box.minY}, true
}

func (r *svgRenderer) drawShape(node *svgNode, transform matrix, style svgStyle, target *layer) {
	if style.visibility != "visible" {
		return
	}
	tolerance := 0.1 / max(transform.scaleFactor(), 1e-9)
	contours := r.geometry(node, tolerance)
	if len(contours) == 0 {
		return
	}
	box, _ := contourBounds(contours, identityMatrix)
	if source, ok := r.paint(style.fill, style.fillOpacity, style, box, transform); ok {
		target.paint(rasterize(contours, transform, r.width, r.height, style.fillRule == "evenodd"), source)
	}
	if style.strokeWidth <= 0 {
		return
	}
	source, ok := r.paint(style.stroke, style.strokeOpacity, style, box, transform)
	if !ok {
		return
	}
	stroked := contours
	if style.dashArray != "none" {
		var pattern []float64
		for _, field := range strings.FieldsFunc(style.dashArray, func(character rune) bool {
			return character == ',' || character == ' '
		}) {
			length, ok := parseLength(field, r.diagonal())
			if !ok {
				pattern = nil
				break
			}
			pattern = append(pattern, length)
		}
		if len(pattern) > 0 {
			stroked = dashContours(contours, pattern, style.dashOffset)
		}
	}
	polygons := strokeContours(stroked, style.strokeWidth, style.lineCap, style.lineJoin, style.miterLimit, tolerance)
	target.paint(rasterize(polygons, transform, r.width, r.height, false), source)
}

func (r *svgRenderer) paint(value string, opacity float64, style svgStyle, box bounds, transform matrix) (paint, bool) {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "url(") {
		end := strings.Index(value, ")")
		if end < 0 {
			return paint{}, false
		}
		if server := r.reference(value[:end+1]); server != nil {
			return r.gradient(server, opacity, box, transform)
		}
		value = strings.TrimSpace(value[end+1:])
	}
	if strings.EqualFold(value, "currentColor") {
		value = style.color
	}
	color, ok := parseColor(value)
	if !ok {
		return paint{}, false
	}
	alpha := color[3] * float32(opacity)
	return paint{color: [4]float32{color[0] * alpha, color[1] * alpha, color[2] * alpha, alpha}}, true
}

func (r *svgRenderer) gradient(node *svgNode, opacity float64, box bounds, transform matrix) (paint, bool) {
	if node.name != "linearGradient" && node.name != "radialGradient" {
		return paint{}, false
	}
	chain := []*svgNode{node}
	for current := node; len(chain) < 8; {
		current = r.reference(current.attributes["href"])
		if current == nil || current.name != "linearGradient" && current.name != "radialGradient" || slices.Contains(chain, current) {
			break
		}
		chain = append(chain, current)
	}
	attribute := func(name string) string {
		for _, item := range chain {
			if value := strings.TrimSpace(item.attributes[name]); value != "" {
				return value
			}
		}
		return ""
	}
	var stops []*svgNode
	for _, item := range chain {
		for _, child := range item.children {
			if child.name == "stop" {
				stops = append(stops, child)
			}
		}
		if len(stops) > 0 {
			break
		}
	}
	result := &gradient{radial: node.name == "radialGradient", spread: attribute("spreadMethod")}
	for _, stop := range stops {
		offset := min(max(parseOpacity(stop.attributes["offset"], 0), 0), 1)
		if len(result.offsets) > 0 {
			offset = max(offset, result.offsets[len(result.offsets)-1])
		}
		colorValue := strings.TrimSpace(stop.attributes["stop-color"])
		if colorValue == "" {
			colorValue = "black"
		} else if strings.EqualFold(colorValue, "currentColor") {
			colorValue = strings.TrimSpace(stop.attributes["color"])
		}
		color, ok := parseColor(colorValue)
		if !ok {
			color = [4]float32{0, 0, 0, 1}
		}
		alpha := color[3] * float32(parseOpacity(stop.attributes["stop-opacity"], 1)*opacity)
		result.offsets = append(result.offsets, offset)
		result.colors = append(result.colors, [4]float32{color[0] * alpha, color[1] * alpha, color[2] * alpha, alpha})
	}
	switch len(result.colors) {
	case 0:
		return paint{}, false
	case 1:
		return paint{color: result.colors[0]}, true
	}
	space := transform
	objectBounding := attribute("gradientUnits") != "userSpaceOnUse"
	if objectBounding {
		if box.maxX <= box.minX || box.maxY <= box.minY {
			return paint{}, false
		}
		space = space.multiply(matrix{a: box.maxX - box.minX, d: box.maxY - box.minY, e: box.minX, f: box.minY})
	}
	space = space.multiply(parseTransform(attribute("gradientTransform")))
	coordinate := func(name string, fallback string, reference float64) float64 {
		value := attribute(name)
		if value == "" {
			value = fallback
		}
		if objectBounding {
			if number, found := strings.CutSuffix(value, "%"); found {
				parsed, _ := parseLength(number, 0)
				return parsed / 100
			}
		}
		parsed, _ := parseLength(value, reference)
		return parsed
	}
	width, height := r.document.viewBox[2], r.document.viewBox[3]
	if result.radial {
		result.end = point{coordinate("cx", "50%", width), coordinate("cy", "50%", height)}
		result.radius = coordinate("r", "50%", r.diagonal())
		result.start = result.end
		if attribute("fx") != "" {
			result.start.x = coordinate("fx", "50%", width)
		}
		if attribute("fy") != "" {
			result.start.y = coordinate("fy", "50%", height)
		}
		if distance := math.Hypot(result.start.x-result.end.x, result.start.y-result.end.y); distance > result.radius*0.99 && distance > 0 {
			ratio := result.radius * 0.99 / distance
			result.start = point{result.end.x + (result.start.x-result.end.x)*ratio, result.end.y + (result.start.y-result.end.y)*ratio}
		}
	} else {
		result.start = point{coordinate("x1", "0%", width), coordinate("y1", "0%", height)}
		result.end = point{coordinate("x2", "100%", width), coordinate("y2", "0%", height)}
	}
	inverse, ok := space.invert()
	if !ok {
		return paint{}, false
	}
	result.inverse = inverse
	return paint{gradient: result}, true
}

func (p paint) at(x int, y int) [4]float32 {
	if p.gradient == nil {
		return p.color
	}
	g := p.gradient
	position := g.inverse.apply(point{float64(x) + 0.5, float64(y) + 0.5})
	var t float64
	if g.radial {
		if g.radius <= 0 {
			return g.colors[len(g.colors)-1]
		}
		dx, dy := position.x-g.start.x, position.y-g.start.y
		cx, cy := g.end.x-g.start.x, g.end.y-g.start.y
		a := cx*cx + cy*cy - g.radius*g.radius
		b := dx*cx + dy*cy
		c := dx*dx + dy*dy
		if a == 0 {
			t = c / (2 * b)
		} else {
			t = (b - math.Sqrt(max(b*b-a*c, 0))) / a
		}
	} else {
		dx, dy := g.end.x-g.start.x, g.end.y-g.start.y
		length := dx*dx + dy*dy
		if length == 0 {
			return g.colors[len(g.colors)-1]
		}
		t = ((position.x-g.start.x)*dx + (position.y-g.start.y)*dy) / length
	}
	switch g.spread {
	case "reflect":
		t = math.Mod(math.Abs(t), 2)
		if t > 1 {
			t = 2 - t
		}
	case "repeat":
		t -= math.Floor(t)
	default:
		t = min(max(t, 0), 1)
	}
	if t <= g.offsets[0] {
		return g.colors[0]
	}
	for index := 1; index < len(g.offsets); index++ {
		if t > g.offsets[index] {
			continue
		}
		span := g.offsets[index] - g.offsets[index-1]
		if span <= 0 {
			return g.colors[index]
		}
		fraction := float32((t - g.offsets[index-1]) / span)
		var color [4]float32
		for channel := range color {
			color[channel] = g.colors[index-1][channel]*(1-fraction) + g.colors[index][channel]*fraction
		}
		return color
	}
	return g.colors[len(g.colors)-1]
}

func (r *svgRenderer) clipCoverage(clip *svgNode, node *svgNode, transform matrix, depth int) []float32 {
	coverage := make([]float32, r.width*r.height)
	if clip.attributes["clipPathUnits"] == "objectBoundingBox" {
		box, ok := r.boundingBoxMatrix(node, depth)
		if !ok {
			return coverage
		}
		transform = transform.multiply(box)
	}
	transform = transform.multiply(parseTransform(clip.attributes["transform"]))
	style := r.inherit(svgDefaultStyle, clip)
	for _, child := range clip.children {
		shape, shapeTransform := child, transform
		if child.name == "use" {
			shape = r.reference(child.attributes["href"])
			if shape == nil {
				continue
			}
			shapeTransform = shapeTransform.multiply(parseTransform(child.attributes["transform"])).
				multiply(matrix{a: 1, d: 1, e: nodeLength(child, "x", r.document.viewBox[2]), f: nodeLength(child, "y", r.document.viewBox[3])})
		}
		if strings.TrimSpace(shape.attributes["display"]) == "none" || strings.TrimSpace(child.attributes["display"]) == "none" {
			continue
		}
		switch shape.name {
		case "path", "rect", "circle", "ellipse", "line", "polyline", "polygon":
		default:
			continue
		}
		shapeStyle := r.inherit(r.inherit(style, child), shape)
		if shapeStyle.visibility != "visible" {
			continue
		}
		shapeTransform = shapeTransform.multiply(parseTransform(shape.attributes["transform"]))
		contours := r.geometry(shape, 0.1/max(shapeTransform.scaleFactor(), 1e-9))
		for index, value := range rasterize(contours, shapeTransform, r.width, r.height, shapeStyle.clipRule == "evenodd") {
			coverage[index] = max(coverage[index], value)
		}
	}
	return coverage
}

func (r *svgRenderer) applyMask(mask *svgNode, node *svgNode, target *layer, transform matrix, depth int) {
	if mask.attributes["maskContentUnits"] == "objectBoundingBox" {
		box, ok := r.boundingBoxMatrix(node, depth)
		if !ok {
			target.multiply(make([]float32, r.width*r.height))
			return
		}
		transform = transform.multiply(box)
	}
	content := newLayer(r.width, r.height)
	style := r.inherit(svgDefaultStyle, mask)
	for _, child := range mask.children {
		r.draw(child, transform, style, content, depth+1)
	}
	alphaOnly := strings.TrimSpace(mask.attributes["mask-type"]) == "alpha"
	coverage := make([]float32, r.width*r.height)
	for index := range coverage {
		pixel := content.pixels[index*4 : index*4+4]
		if alphaOnly {
			coverage[index] = pixel[3]
		} else {
			coverage[index] = 0.2125*pixel[0] + 0.7154*pixel[1] + 0.0721*pixel[2]
		}
	}
	target.multiply(coverage)
}

func (r *svgRenderer) applyFilter(filter *svgNode, source *layer, transform matrix, style svgStyle) *layer {
	results := make(map[string]*layer)
	last := source
	var sourceAlpha *layer
	input := func(name string) *layer {
		switch strings.TrimSpace(name) {
		case "":
			return last
		case "SourceGraphic":
			return source
		case "SourceAlpha":
			if sourceAlpha == nil {
				sourceAlpha = newLayer(r.width, r.height)
				for index := 3; index < len(source.pixels); index += 4 {
					sourceAlpha.pixels[index] = source.pixels[index]
				}
			}
			return sourceAlpha
		}
		if result, found := results[strings.TrimSpace(name)]; found {
			return result
		}
		return newLayer(r.width, r.height)
	}
	scaleX, scaleY := math.Hypot(transform.a, transform.b), math.Hypot(transform.c, transform.d)
	flood := func(primitive *svgNode) [4]float32 {
		value := strings.TrimSpace(primitive.attributes["flood-color"])
		if value == "" {
			value = "black"
		} else if strings.EqualFold(value, "currentColor") {
			value = style.color
		}
		color, ok := parseColor(value)
		if !ok {
			color = [4]float32{0, 0, 0, 1}
		}
		alpha := color[3] * float32(parseOpacity(primitive.attributes["flood-opacity"], 1))
		return [4]float32{color[0] * alpha, color[1] * alpha, color[2] * alpha, alpha}
	}
	deviation := func(primitive *svgNode) (float64, float64) {
		values := parseNumbers(primitive.attributes["stdDeviation"])
		switch len(values) {
		case 0:
			return 0, 0
		case 1:
			return max(values[0], 0) * scaleX, max(values[0], 0) * scaleY
		}
		return max(values[0], 0) * scaleX, max(values[1], 0) * scaleY
	}
	shift := func(primitive *svgNode, fallback float64) (float64, float64) {
		dx, ok := parseLength(primitive.attributes["dx"], 0)
		if !ok {
			dx = fallback
		}
		dy, ok := parseLength(primitive.attributes["dy"], 0)
		if !ok {
			dy = fallback
		}
		return transform.a*dx + transform.c*dy, transform.b*dx + transform.d*dy
	}
	for _, primitive := range filter.children {
		in := input(primitive.attributes["in"])
		var output *layer
		switch primitive.name {
		case "feGaussianBlur":
			output = in.blur(deviation(primitive))
		case "feOffset":
			output = in.offset(shift(primitive, 0))
		case "feFlood":
			output = newLayer(r.width, r.height)
			color := flood(primitive)
			for index := 0; index < len(output.pixels); index += 4 {
				copy(output.pixels[index:index+4], color[:])
			}
		case "feColorMatrix":
			output = colorMatrix(in, strings.TrimSpace(primitive.attributes["type"]), parseNumbers(primitive.attributes["values"]))
		case "feComposite":
			values := []float64{0, 0, 0, 0}
			for index, name := range []string{"k1", "k2", "k3", "k4"} {
				values[index], _ = parseLength(primitive.attributes[name], 0)
			}
			output = compositeLayers(in, input(primitive.attributes["in2"]), strings.TrimSpace(primitive.attributes["operator"]), values)
		case "feBlend":
			output = blendLayers(in, input(primitive.attributes["in2"]), strings.TrimSpace(primitive.attributes["mode"]))
		case "feMerge":
			output = newLayer(r.width, r.height)
			for _, node := range primitive.children {
				if node.name == "feMergeNode" {
					output.composite(input(node.attributes["in"]), 1)
				}
			}
		case "feDropShadow":
			color := flood(primitive)
			output = newLayer(r.width, r.height)
			for index := 3; index < len(in.pixels); index += 4 {
				for channel := range 4 {
					output.pixels[index-3+channel] = color[channel] * in.pixels[index]
				}
			}
			deviationX, deviationY := deviation(primitive)
			if _, found := primitive.attributes["stdDeviation"]; !found {
				deviationX, deviationY = 2*scaleX, 2*scaleY
			}
			output = output.blur(deviationX, deviationY).offset(shift(primitive, 2))
			output.composite(in, 1)
		default:
			output = in
		}
		if name := strings.TrimSpace(primitive.attributes["result"]); name != "" {
			results[name] = output
		}
		last = output
	}
	return last
}

func colorMatrix(source *layer, kind string, values []float64) *layer {
	var coefficients [20]float64
	switch kind {
	case "saturate":
		s := 1.0
		if len(values) > 0 {
			s = values[0]
		}
		coefficients = [20]float64{
			0.213 + 0.787*s, 0.715 - 0.715*s, 0.072 - 0.072*s, 0, 0,
			0.213 - 0.213*s, 0.715 + 0.285*s, 0.072 - 0.072*s, 0, 0,
			0.213 - 0.213*s, 0.715 - 0.715*s, 0.072 + 0.928*s, 0, 0,
			0, 0, 0, 1, 0,
		}
	case "hueRotate":
		angle := 0.0
		if len(values) > 0 {
			angle = values[0] * math.Pi / 180
		}
		cos, sin := math.Cos(angle), math.Sin(angle)
		coefficients = [20]float64{
			0.213 + cos*0.787 - sin*0.213, 0.715 - cos*0.715 - sin*0.715, 0.072 - cos*0.072 + sin*0.928, 0, 0,
			0.213 - cos*0.213 + sin*0.143, 0.715 + cos*0.285 + sin*0.140, 0.072 - cos*0.072 - sin*0.283, 0, 0,
			0.213 - cos*0.213 - sin*0.787, 0.715 - cos*0.715 + sin*0.715, 0.072 + cos*0.928 + sin*0.072, 0, 0,
			0, 0, 0, 1, 0,
		}
	case "luminanceToAlpha":
		coefficients = [20]float64{15: 0.2125, 16: 0.7154, 17: 0.0721}
	default:
		if len(values) != 20 {
			return source
		}
		coefficients = [20]float64(values)
	}
	result := newLayer(source.width, source.height)
	for index := 0; index < len(source.pixels); index += 4 {
		pixel := source.pixels[index : index+4]
		var color [4]float64
		if pixel[3] > 0 {
			color = [4]float64{float64(pixel[0] / pixel[3]), float64(pixel[1] / pixel[3]), float64(pixel[2] / pixel[3]), float64(pixel[3])}
		}
		var output [4]float64
		for row := range 4 {
			value := coefficients[row*5+4]
			for column := range 4 {
				value += coefficients[row*5+column] * color[column]
			}
			output[row] = min(max(value, 0), 1)
		}
		for channel := range 3 {
			result.pixels[index+channel] = float32(output[channel] * output[3])
		}
		result.pixels[index+3] = float32(output[3])
	}
	return result
}

func compositeLayers(top *layer, bottom *layer, operator string, k []float64) *layer {
	result := newLayer(top.width, top.height)
	for index := 0; index < len(top.pixels); index += 4 {
		a, b := top.pixels[index:index+4], bottom.pixels[index:index+4]
		alphaA, alphaB := a[3], b[3]
		for channel := range 4 {
			var value float32
			switch operator {
			case "in":
				value = a[channel] * alphaB
			case "out":
				value = a[channel] * (1 - alphaB)
			case "atop":
				value = a[channel]*alphaB + b[channel]*(1-alphaA)
			case "xor":
				value = a[channel]*(1-alphaB) + b[channel]*(1-alphaA)
			case "arithmetic":
				value = float32(k[0])*a[channel]*b[channel] + float32(k[1])*a[channel] + float32(k[2])*b[channel] + float32(k[3])
			default:
				value = a[channel] + b[channel]*(1-alphaA)
			}
			result.pixels[index+channel] = min(max(value, 0), 1)
		}
		for channel := range 3 {
			result.pixels[index+channel] = min(result.pixels[index+channel], result.pixels[index+3])
		}
	}
	return result
}

func blendLayers(top *layer, bottom *layer, mode string) *layer {
	result := newLayer(top.width, top.height)
	for index := 0; index < len(top.pixels); index += 4 {
		s, b := top.pixels[index:index+4], bottom.pixels[index:index+4]
		for channel := range 3 {
			cs, cb := s[channel], b[channel]
			var value float32
			switch mode {
			case "multiply":
				value = cs*(1-b[3]) + cb*(1-s[3]) + cs*cb
			case "screen":
				value = cs + cb - cs*cb
			case "darken":
				value = min(cs*b[3], cb*s[3]) + cs*(1-b[3]) + cb*(1-s[3])
			case "lighten":
				value = max(cs*b[3], cb*s[3]) + cs*(1-b[3]) + cb*(1-s[3])
			default:
				value = cs + cb*(1-s[3])
			}
			result.pixels[index+channel] = value
		}
		result.pixels[index+3] = s[3] + b[3] - s[3]*b[3]
	}
	return result
}
