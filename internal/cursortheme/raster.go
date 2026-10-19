package cursortheme

import (
	"image"
	"math"
	"slices"
)

type point struct {
	x, y float64
}

type matrix struct {
	a, b, c, d, e, f float64
}

var identityMatrix = matrix{a: 1, d: 1}

func (m matrix) multiply(n matrix) matrix {
	return matrix{
		a: m.a*n.a + m.c*n.b,
		b: m.b*n.a + m.d*n.b,
		c: m.a*n.c + m.c*n.d,
		d: m.b*n.c + m.d*n.d,
		e: m.a*n.e + m.c*n.f + m.e,
		f: m.b*n.e + m.d*n.f + m.f,
	}
}

func (m matrix) apply(p point) point {
	return point{m.a*p.x + m.c*p.y + m.e, m.b*p.x + m.d*p.y + m.f}
}

func (m matrix) invert() (matrix, bool) {
	determinant := m.a*m.d - m.b*m.c
	if determinant == 0 || math.IsNaN(determinant) || math.IsInf(determinant, 0) {
		return identityMatrix, false
	}
	inverse := matrix{a: m.d / determinant, b: -m.b / determinant, c: -m.c / determinant, d: m.a / determinant}
	inverse.e = -(inverse.a*m.e + inverse.c*m.f)
	inverse.f = -(inverse.b*m.e + inverse.d*m.f)
	return inverse, true
}

func (m matrix) scaleFactor() float64 {
	return max(math.Hypot(m.a, m.b), math.Hypot(m.c, m.d))
}

type contour struct {
	points []point
	closed bool
}

type pathBuilder struct {
	tolerance float64
	contours  []contour
	start     point
	current   point
	pending   bool
}

func (b *pathBuilder) moveTo(p point) {
	b.contours = append(b.contours, contour{points: []point{p}})
	b.start, b.current, b.pending = p, p, false
}

func (b *pathBuilder) lineTo(p point) {
	if len(b.contours) == 0 || b.pending {
		b.moveTo(b.current)
	}
	last := &b.contours[len(b.contours)-1]
	last.points = append(last.points, p)
	b.current = p
}

func (b *pathBuilder) close() {
	if len(b.contours) > 0 && !b.pending {
		b.contours[len(b.contours)-1].closed = true
	}
	b.current, b.pending = b.start, true
}

func (b *pathBuilder) segments(deviation float64) int {
	return min(max(int(math.Ceil(math.Sqrt(deviation/b.tolerance))), 1), 256)
}

func (b *pathBuilder) quadTo(control, end point) {
	start := b.current
	count := b.segments(0.25 * math.Hypot(start.x-2*control.x+end.x, start.y-2*control.y+end.y))
	for index := 1; index < count; index++ {
		t := float64(index) / float64(count)
		u := 1 - t
		b.lineTo(point{
			u*u*start.x + 2*u*t*control.x + t*t*end.x,
			u*u*start.y + 2*u*t*control.y + t*t*end.y,
		})
	}
	b.lineTo(end)
}

func (b *pathBuilder) cubicTo(control1, control2, end point) {
	start := b.current
	count := b.segments(0.75 * max(
		math.Hypot(start.x-2*control1.x+control2.x, start.y-2*control1.y+control2.y),
		math.Hypot(control1.x-2*control2.x+end.x, control1.y-2*control2.y+end.y),
	))
	for index := 1; index < count; index++ {
		t := float64(index) / float64(count)
		u := 1 - t
		b.lineTo(point{
			u*u*u*start.x + 3*u*u*t*control1.x + 3*u*t*t*control2.x + t*t*t*end.x,
			u*u*u*start.y + 3*u*u*t*control1.y + 3*u*t*t*control2.y + t*t*t*end.y,
		})
	}
	b.lineTo(end)
}

func (b *pathBuilder) arcTo(radiusX, radiusY, rotation float64, large, sweep bool, end point) {
	start := b.current
	radiusX, radiusY = math.Abs(radiusX), math.Abs(radiusY)
	if start == end {
		return
	}
	if radiusX == 0 || radiusY == 0 {
		b.lineTo(end)
		return
	}
	cos, sin := math.Cos(rotation*math.Pi/180), math.Sin(rotation*math.Pi/180)
	halfX, halfY := (start.x-end.x)/2, (start.y-end.y)/2
	x1 := cos*halfX + sin*halfY
	y1 := -sin*halfX + cos*halfY
	if lambda := x1*x1/(radiusX*radiusX) + y1*y1/(radiusY*radiusY); lambda > 1 {
		radiusX *= math.Sqrt(lambda)
		radiusY *= math.Sqrt(lambda)
	}
	numerator := radiusX*radiusX*radiusY*radiusY - radiusX*radiusX*y1*y1 - radiusY*radiusY*x1*x1
	denominator := radiusX*radiusX*y1*y1 + radiusY*radiusY*x1*x1
	coefficient := math.Sqrt(max(0, numerator/denominator))
	if large == sweep {
		coefficient = -coefficient
	}
	centerX1 := coefficient * radiusX * y1 / radiusY
	centerY1 := -coefficient * radiusY * x1 / radiusX
	centerX := cos*centerX1 - sin*centerY1 + (start.x+end.x)/2
	centerY := sin*centerX1 + cos*centerY1 + (start.y+end.y)/2
	theta := math.Atan2((y1-centerY1)/radiusY, (x1-centerX1)/radiusX)
	delta := math.Atan2((-y1-centerY1)/radiusY, (-x1-centerX1)/radiusX) - theta
	if sweep && delta < 0 {
		delta += 2 * math.Pi
	} else if !sweep && delta > 0 {
		delta -= 2 * math.Pi
	}
	count := arcSegments(max(radiusX, radiusY), math.Abs(delta), b.tolerance)
	for index := 1; index < count; index++ {
		angle := theta + delta*float64(index)/float64(count)
		x, y := radiusX*math.Cos(angle), radiusY*math.Sin(angle)
		b.lineTo(point{cos*x - sin*y + centerX, sin*x + cos*y + centerY})
	}
	b.lineTo(end)
}

func arcSegments(radius float64, angle float64, tolerance float64) int {
	step := math.Pi / 2
	if tolerance < radius {
		step = min(step, 2*math.Acos(1-tolerance/radius))
	}
	return min(max(int(math.Ceil(angle/step)), 1), 1024)
}

func signedArea(points []point) float64 {
	area := 0.0
	for index, current := range points {
		next := points[(index+1)%len(points)]
		area += current.x*next.y - next.x*current.y
	}
	return area
}

func dashContours(contours []contour, pattern []float64, offset float64) []contour {
	if len(pattern)%2 == 1 {
		pattern = append(pattern, pattern...)
	}
	total := 0.0
	for _, length := range pattern {
		if length < 0 {
			return contours
		}
		total += length
	}
	if total <= 0 {
		return contours
	}
	var dashes []contour
	for _, item := range contours {
		points := item.points
		if item.closed && len(points) > 1 {
			points = append(slices.Clip(points), points[0])
		}
		if len(points) < 2 {
			continue
		}
		index := 0
		remaining := math.Mod(offset, total)
		if remaining < 0 {
			remaining += total
		}
		for remaining >= pattern[index] {
			remaining -= pattern[index]
			index = (index + 1) % len(pattern)
		}
		remaining = pattern[index] - remaining
		var current []point
		if index%2 == 0 {
			current = []point{points[0]}
		}
		for segment := 1; segment < len(points); segment++ {
			from, to := points[segment-1], points[segment]
			length := math.Hypot(to.x-from.x, to.y-from.y)
			position := 0.0
			for length-position > remaining {
				position += remaining
				split := point{from.x + (to.x-from.x)*position/length, from.y + (to.y-from.y)*position/length}
				if index%2 == 0 {
					dashes = append(dashes, contour{points: append(current, split)})
					current = nil
				} else {
					current = []point{split}
				}
				index = (index + 1) % len(pattern)
				remaining = pattern[index]
			}
			remaining -= length - position
			if index%2 == 0 {
				current = append(current, to)
			}
		}
		if index%2 == 0 && len(current) > 1 {
			dashes = append(dashes, contour{points: current})
		}
	}
	return dashes
}

func strokeContours(contours []contour, width float64, lineCap string, lineJoin string, miterLimit float64, tolerance float64) []contour {
	half := width / 2
	var polygons []contour
	add := func(points ...point) {
		if signedArea(points) < 0 {
			slices.Reverse(points)
		}
		polygons = append(polygons, contour{points: points, closed: true})
	}
	circle := func(center point) {
		count := max(arcSegments(half, 2*math.Pi, tolerance), 8)
		points := make([]point, count)
		for index := range points {
			angle := 2 * math.Pi * float64(index) / float64(count)
			points[index] = point{center.x + half*math.Cos(angle), center.y + half*math.Sin(angle)}
		}
		add(points...)
	}
	normal := func(from, to point) (point, point) {
		length := math.Hypot(to.x-from.x, to.y-from.y)
		direction := point{(to.x - from.x) / length, (to.y - from.y) / length}
		return direction, point{-direction.y * half, direction.x * half}
	}
	for _, item := range contours {
		var points []point
		for _, current := range item.points {
			if len(points) == 0 || math.Hypot(current.x-points[len(points)-1].x, current.y-points[len(points)-1].y) > 1e-9 {
				points = append(points, current)
			}
		}
		if item.closed && len(points) > 1 && math.Hypot(points[0].x-points[len(points)-1].x, points[0].y-points[len(points)-1].y) <= 1e-9 {
			points = points[:len(points)-1]
		}
		switch {
		case len(points) == 0:
			continue
		case len(points) == 1:
			center := points[0]
			switch lineCap {
			case "round":
				circle(center)
			case "square":
				add(point{center.x - half, center.y - half}, point{center.x + half, center.y - half},
					point{center.x + half, center.y + half}, point{center.x - half, center.y + half})
			}
			continue
		}
		closed := item.closed && len(points) > 2
		count := len(points)
		segmentCount := count - 1
		if closed {
			segmentCount = count
		}
		for index := range segmentCount {
			from, to := points[index], points[(index+1)%count]
			_, offset := normal(from, to)
			add(point{from.x + offset.x, from.y + offset.y}, point{to.x + offset.x, to.y + offset.y},
				point{to.x - offset.x, to.y - offset.y}, point{from.x - offset.x, from.y - offset.y})
		}
		for index := range count {
			if !closed && (index == 0 || index == count-1) {
				continue
			}
			previous, current, next := points[(index+count-1)%count], points[index], points[(index+1)%count]
			incoming, incomingOffset := normal(previous, current)
			outgoing, outgoingOffset := normal(current, next)
			cross := incoming.x*outgoing.y - incoming.y*outgoing.x
			dot := incoming.x*outgoing.x + incoming.y*outgoing.y
			if math.Abs(cross) < 1e-12 && dot > 0 {
				continue
			}
			side := 1.0
			if cross > 0 {
				side = -1
			}
			outer1 := point{current.x + side*incomingOffset.x, current.y + side*incomingOffset.y}
			outer2 := point{current.x + side*outgoingOffset.x, current.y + side*outgoingOffset.y}
			join := lineJoin
			if dot > 0.99 {
				join = "miter"
			}
			switch join {
			case "round":
				circle(current)
			case "miter", "miter-clip", "arcs":
				ratio := 1 / math.Sqrt(max((1+dot)/2, 1e-12))
				bisector := point{outer1.x + outer2.x - 2*current.x, outer1.y + outer2.y - 2*current.y}
				length := math.Hypot(bisector.x, bisector.y)
				if ratio <= miterLimit && length > 0 {
					tip := point{current.x + bisector.x/length*half*ratio, current.y + bisector.y/length*half*ratio}
					add(current, outer1, tip, outer2)
				} else {
					add(current, outer1, outer2)
				}
			default:
				add(current, outer1, outer2)
			}
		}
		if closed {
			continue
		}
		for _, end := range [][2]point{{points[1], points[0]}, {points[count-2], points[count-1]}} {
			direction, offset := normal(end[0], end[1])
			tip := end[1]
			switch lineCap {
			case "round":
				circle(tip)
			case "square":
				extended := point{tip.x + direction.x*half, tip.y + direction.y*half}
				add(point{tip.x + offset.x, tip.y + offset.y}, point{extended.x + offset.x, extended.y + offset.y},
					point{extended.x - offset.x, extended.y - offset.y}, point{tip.x - offset.x, tip.y - offset.y})
			}
		}
	}
	return polygons
}

func rasterize(contours []contour, transform matrix, width int, height int, evenOdd bool) []float32 {
	stride := width + 1
	cells := make([]float32, stride*height)
	for _, item := range contours {
		for index, from := range item.points {
			to := item.points[(index+1)%len(item.points)]
			accumulateLine(cells, stride, height, transform.apply(from), transform.apply(to))
		}
	}
	coverage := make([]float32, width*height)
	for y := range height {
		sum := float32(0)
		for x := range width {
			sum += cells[y*stride+x]
			value := float32(math.Abs(float64(sum)))
			if evenOdd {
				value = float32(math.Mod(float64(value), 2))
				if value > 1 {
					value = 2 - value
				}
			} else {
				value = min(value, 1)
			}
			coverage[y*width+x] = value
		}
	}
	return coverage
}

func accumulateLine(cells []float32, stride int, height int, from point, to point) {
	if math.IsNaN(from.x+from.y+to.x+to.y) || math.IsInf(from.x+from.y+to.x+to.y, 0) {
		return
	}
	width := stride - 1
	direction := float32(1)
	ax, ay, bx, by := float32(from.x), float32(from.y), float32(to.x), float32(to.y)
	if ay > by {
		direction, ax, ay, bx, by = -1, bx, by, ax, ay
	}
	if by-ay <= 1e-6 || by <= 0 || ay >= float32(height) {
		return
	}
	slope := (bx - ax) / (by - ay)
	clamp := func(index int) int {
		return min(max(index, 0), width)
	}
	x := ax
	y := float32(math.Floor(float64(ay)))
	yMax := min(float32(math.Ceil(float64(by))), float32(height))
	for ; y < yMax; y++ {
		dy := min(y+1, by) - max(y, ay)
		xNext := x + dy*slope
		if y < 0 {
			x = xNext
			continue
		}
		row := cells[int(y)*stride : int(y)*stride+stride]
		d := dy * direction
		x0, x1 := min(x, xNext), max(x, xNext)
		x0i := int(math.Floor(float64(x0)))
		x0Floor := float32(x0i)
		x1i := int(math.Ceil(float64(x1)))
		x1Ceil := float32(x1i)
		if x1i <= x0i+1 {
			middle := 0.5*(x+xNext) - x0Floor
			row[clamp(x0i)] += d - d*middle
			row[clamp(x0i+1)] += d * middle
		} else {
			s := 1 / (x1 - x0)
			x0f := x0 - x0Floor
			a0 := 0.5 * s * (1 - x0f) * (1 - x0f)
			x1f := x1 - x1Ceil + 1
			am := 0.5 * s * x1f * x1f
			row[clamp(x0i)] += d * a0
			if x1i == x0i+2 {
				row[clamp(x0i+1)] += d * (1 - a0 - am)
			} else {
				a1 := s * (1.5 - x0f)
				row[clamp(x0i+1)] += d * (a1 - a0)
				low, high := x0i+2, x1i-1
				if low < 0 {
					row[0] += d * s * float32(max(min(high, 0)-low, 0))
					low = 0
				}
				for index := low; index < min(high, width); index++ {
					row[index] += d * s
				}
				a2 := a1 + s*float32(x1i-x0i-3)
				row[clamp(x1i-1)] += d * (1 - a2 - am)
			}
			row[clamp(x1i)] += d * am
		}
		x = xNext
	}
}

type layer struct {
	width  int
	height int
	pixels []float32
}

func newLayer(width int, height int) *layer {
	return &layer{width: width, height: height, pixels: make([]float32, width*height*4)}
}

func (l *layer) paint(coverage []float32, source paint) {
	for index, value := range coverage {
		if value <= 0 {
			continue
		}
		color := source.at(index%l.width, index/l.width)
		alpha := color[3] * value
		if alpha <= 0 {
			continue
		}
		pixel := l.pixels[index*4 : index*4+4]
		for channel := range pixel {
			pixel[channel] = color[channel]*value + pixel[channel]*(1-alpha)
		}
	}
}

func (l *layer) composite(source *layer, opacity float32) {
	for index := 0; index < len(l.pixels); index += 4 {
		alpha := source.pixels[index+3] * opacity
		if alpha <= 0 {
			continue
		}
		for channel := range 4 {
			l.pixels[index+channel] = source.pixels[index+channel]*opacity + l.pixels[index+channel]*(1-alpha)
		}
	}
}

func (l *layer) multiply(coverage []float32) {
	for index, value := range coverage {
		for channel := range 4 {
			l.pixels[index*4+channel] *= value
		}
	}
}

func (l *layer) blur(deviationX float64, deviationY float64) *layer {
	result := &layer{width: l.width, height: l.height, pixels: slices.Clone(l.pixels)}
	convolve := func(deviation float64, length int, lines int, step int, lineStep int) {
		if deviation <= 0 {
			return
		}
		radius := min(int(math.Ceil(deviation*3)), 256)
		kernel := make([]float32, radius*2+1)
		total := float32(0)
		for index := range kernel {
			distance := float64(index - radius)
			kernel[index] = float32(math.Exp(-distance * distance / (2 * deviation * deviation)))
			total += kernel[index]
		}
		source := slices.Clone(result.pixels)
		for line := range lines {
			for position := range length {
				var sum [4]float32
				for index, weight := range kernel {
					sample := position + index - radius
					if sample < 0 || sample >= length {
						continue
					}
					offset := (line*lineStep + sample*step) * 4
					for channel := range 4 {
						sum[channel] += source[offset+channel] * weight
					}
				}
				offset := (line*lineStep + position*step) * 4
				for channel := range 4 {
					result.pixels[offset+channel] = sum[channel] / total
				}
			}
		}
	}
	convolve(deviationX, l.width, l.height, 1, l.width)
	convolve(deviationY, l.height, l.width, l.width, 1)
	return result
}

func (l *layer) offset(dx float64, dy float64) *layer {
	result := newLayer(l.width, l.height)
	for y := range l.height {
		for x := range l.width {
			sourceX, sourceY := float64(x)-dx, float64(y)-dy
			x0, y0 := int(math.Floor(sourceX)), int(math.Floor(sourceY))
			fx, fy := float32(sourceX-float64(x0)), float32(sourceY-float64(y0))
			for _, sample := range [4]struct {
				x, y   int
				weight float32
			}{{x0, y0, (1 - fx) * (1 - fy)}, {x0 + 1, y0, fx * (1 - fy)}, {x0, y0 + 1, (1 - fx) * fy}, {x0 + 1, y0 + 1, fx * fy}} {
				if sample.x < 0 || sample.y < 0 || sample.x >= l.width || sample.y >= l.height || sample.weight == 0 {
					continue
				}
				for channel := range 4 {
					result.pixels[(y*l.width+x)*4+channel] += l.pixels[(sample.y*l.width+sample.x)*4+channel] * sample.weight
				}
			}
		}
	}
	return result
}

func (l *layer) image() *image.RGBA {
	result := image.NewRGBA(image.Rect(0, 0, l.width, l.height))
	for index := 0; index < len(l.pixels); index += 4 {
		alpha := min(max(l.pixels[index+3], 0), 1)
		for channel := range 3 {
			result.Pix[index+channel] = uint8(min(max(l.pixels[index+channel], 0), alpha)*255 + 0.5)
		}
		result.Pix[index+3] = uint8(alpha*255 + 0.5)
	}
	return result
}
