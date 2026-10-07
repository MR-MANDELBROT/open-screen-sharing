package deviceserver

import (
	"context"
	"math"
	"time"

	"github.com/nekohasekai/open-screen-sharing/internal/coredevice/hid"
	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
	"github.com/sagernet/sing/common"
)

const (
	longPressHold = 650 * time.Millisecond
	lookupHold    = 700 * time.Millisecond
	wheelMerge    = 30 * time.Millisecond
	wheelGain     = 4.0
	touchSlop     = 10.0
	edgeRegion    = 20.0
	edgeInset     = 24.0
	dragSteps     = 6
	dragStep      = 10 * time.Millisecond
	dragHold      = 40 * time.Millisecond
	tapHold       = 40 * time.Millisecond
	doubleTapGap  = 80 * time.Millisecond
	pinchGap      = 200.0
	swipeFraction = 0.4
	swipeSteps    = 10
	swipeStep     = 12 * time.Millisecond
	homeGap       = 150 * time.Millisecond
	flickWindow   = 150 * time.Millisecond
	flickSpeed    = 500.0
	flickSteps    = 3
	flickStep     = 17 * time.Millisecond
)

func sleep(ctx context.Context, duration time.Duration) error {
	if duration <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type point struct {
	X, Y float64
}

type touchMapper struct {
	input    *hid.Client
	geometry screenGeometry

	finger *buttonFinger
	scroll *scrollFinger
	pinch  *pinchFingers
}

type trailPoint struct {
	at   point
	time time.Time
}

type movingTouch struct {
	at    point
	trail []trailPoint
}

func (t *movingTouch) move(at point) {
	now := time.Now()
	start := 0
	for start+1 < len(t.trail) && now.Sub(t.trail[start+1].time) >= flickWindow {
		start++
	}
	t.at = at
	t.trail = append(t.trail[start:], trailPoint{at, now})
}

type buttonFinger struct {
	movingTouch
	button uint8
	downAt time.Time
}

type scrollFinger struct {
	movingTouch
	start point
}

type pinchFingers struct {
	center point
	radius float64
	scale  float64
	angle  float64
	kinds  map[rfb.GestureKind]bool
}

func (m *touchMapper) reset() {
	m.finger = nil
	m.scroll = nil
	m.pinch = nil
}

func (m *touchMapper) busy() bool {
	return m.finger != nil || m.scroll != nil || m.pinch != nil
}

func (m *touchMapper) set(ctx context.Context, points ...point) error {
	return m.setFrom(ctx, hid.EdgeNone, points...)
}

func (m *touchMapper) setFrom(ctx context.Context, edge hid.Edge, points ...point) error {
	return m.input.SetTouches(ctx, m.touches(points), edge)
}

func (m *touchMapper) lift(ctx context.Context, points ...point) error {
	return m.input.LiftTouches(ctx, m.touches(points))
}

func (m *touchMapper) touches(points []point) []hid.Touch {
	return common.Map(points, func(at point) hid.Touch {
		return touchPoint(m.geometry, at)
	})
}

func (m *touchMapper) endOthers(ctx context.Context) error {
	if m.scroll == nil && m.pinch == nil {
		return nil
	}
	m.scroll, m.pinch = nil, nil
	return m.lift(ctx)
}

func (m *touchMapper) pointer(ctx context.Context, event rfb.ViewerPointer) error {
	at := point{float64(event.X), float64(event.Y)}
	held := event.Buttons & (rfb.ButtonLeft | rfb.ButtonRight)
	switch {
	case m.finger == nil:
		if held == 0 {
			return nil
		}
		if err := m.endOthers(ctx); err != nil {
			return err
		}
		button := rfb.ButtonLeft
		if held&rfb.ButtonLeft == 0 {
			button = rfb.ButtonRight
		}
		m.finger = &buttonFinger{button: button, downAt: time.Now()}
		m.finger.move(at)
		return m.setFrom(ctx, touchEdge(m.geometry, at), at)
	case held&m.finger.button != 0:
		if at == m.finger.at {
			return nil
		}
		m.finger.move(at)
		return m.set(ctx, at)
	default:
		finger := m.finger
		m.finger = nil
		if at != finger.at {
			if err := m.set(ctx, at); err != nil {
				return err
			}
		}
		if finger.button == rfb.ButtonRight {
			if err := sleep(ctx, longPressHold-time.Since(finger.downAt)); err != nil {
				return m.liftAfter(err)
			}
			return m.lift(ctx)
		}
		finger.move(at)
		return m.flick(ctx, finger.trail, point{}, point{float64(m.geometry.Width - 1), float64(m.geometry.Height - 1)})
	}
}

func (m *touchMapper) flick(ctx context.Context, trail []trailPoint, low, high point) error {
	origin, last := trail[0], trail[len(trail)-1]
	dx, dy := last.at.X-origin.at.X, last.at.Y-origin.at.Y
	seconds := max(last.time.Sub(origin.time), flickStep).Seconds()
	distance := math.Hypot(dx, dy)
	if distance < m.geometry.points(touchSlop) || distance/seconds < m.geometry.points(flickSpeed) {
		return m.lift(ctx)
	}
	for index := 1; ; index++ {
		if err := sleep(ctx, flickStep); err != nil {
			return m.liftAfter(err)
		}
		fraction := float64(index) * flickStep.Seconds() / seconds
		at := point{clamp(last.at.X+dx*fraction, low.X, high.X), clamp(last.at.Y+dy*fraction, low.Y, high.Y)}
		if index == flickSteps {
			return m.lift(ctx, at)
		}
		if err := m.set(ctx, at); err != nil {
			return err
		}
	}
}

func (m *touchMapper) liftAfter(err error) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = m.lift(ctx)
	return err
}

func (m *touchMapper) inset() (left, top, right, bottom float64) {
	margin := m.geometry.points(edgeInset)
	right = float64(m.geometry.Width-1) - margin
	bottom = float64(m.geometry.Height-1) - margin
	if right < margin {
		margin, right = 0, float64(m.geometry.Width-1)
	}
	if bottom < margin {
		bottom = float64(m.geometry.Height - 1)
		return margin, 0, right, bottom
	}
	return margin, margin, right, bottom
}

func (m *touchMapper) wheel(ctx context.Context, x, y int, deltaX, deltaY float64) error {
	if m.finger != nil || m.pinch != nil {
		return nil
	}
	if m.scroll != nil {
		if err := m.endOthers(ctx); err != nil {
			return err
		}
	}
	dx := m.geometry.points(deltaX * wheelGain)
	dy := m.geometry.points(deltaY * wheelGain)
	length := math.Hypot(dx, dy)
	if length == 0 {
		return nil
	}
	total := length + m.geometry.points(touchSlop)
	ux, uy := dx/length, dy/length
	left, top, right, bottom := m.inset()
	start := point{clamp(float64(x), left, right), clamp(float64(y), top, bottom)}
	start.X, total = fitDrag(start.X, ux, total, left, right)
	start.Y, total = fitDrag(start.Y, uy, total, top, bottom)
	end := point{start.X + ux*total, start.Y + uy*total}
	return m.drag(ctx, hid.EdgeNone, start, end, dragSteps, dragStep, dragHold)
}

func fitDrag(start, direction, length, low, high float64) (float64, float64) {
	switch {
	case direction > 0:
		if start+direction*length > high {
			start = max(low, high-direction*length)
			length = min(length, (high-start)/direction)
		}
	case direction < 0:
		if start+direction*length < low {
			start = min(high, low-direction*length)
			length = min(length, (low-start)/direction)
		}
	}
	return start, max(length, 0)
}

func (m *touchMapper) drag(ctx context.Context, edge hid.Edge, from, to point, steps int, step, hold time.Duration) error {
	if err := m.setFrom(ctx, edge, from); err != nil {
		return err
	}
	for index := 1; index <= steps; index++ {
		if err := sleep(ctx, step); err != nil {
			return m.liftAfter(err)
		}
		fraction := float64(index) / float64(steps)
		at := point{from.X + (to.X-from.X)*fraction, from.Y + (to.Y-from.Y)*fraction}
		if err := m.set(ctx, at); err != nil {
			return err
		}
	}
	if err := sleep(ctx, hold); err != nil {
		return m.liftAfter(err)
	}
	return m.lift(ctx)
}

func (m *touchMapper) continuousScroll(ctx context.Context, event rfb.ViewerScroll) error {
	if m.finger != nil || m.pinch != nil {
		return nil
	}
	left, top, right, bottom := m.inset()
	switch {
	case event.Phase&(rfb.PhaseEnded|rfb.PhaseCancelled) != 0:
		scroll := m.scroll
		if scroll == nil {
			return nil
		}
		m.scroll = nil
		if event.Phase&rfb.PhaseCancelled != 0 {
			return m.lift(ctx)
		}
		scroll.move(scroll.at)
		return m.flick(ctx, scroll.trail, point{left, top}, point{right, bottom})
	case event.Phase&(rfb.PhaseBegan|rfb.PhaseChanged) == 0:
		return nil
	}
	if m.scroll == nil {
		start := point{clamp(float64(event.X), left, right), clamp(float64(event.Y), top, bottom)}
		m.scroll = &scrollFinger{start: start}
		m.scroll.move(start)
		if err := m.set(ctx, start); err != nil {
			return err
		}
	}
	dx, dy := m.geometry.viewerPoints(event.DeltaX), m.geometry.viewerPoints(event.DeltaY)
	if dx == 0 && dy == 0 {
		return nil
	}
	next := point{m.scroll.at.X + dx, m.scroll.at.Y + dy}
	clamped := point{clamp(next.X, left, right), clamp(next.Y, top, bottom)}
	m.scroll.move(clamped)
	if err := m.set(ctx, clamped); err != nil {
		return err
	}
	if clamped != next {
		if err := m.lift(ctx); err != nil {
			return err
		}
		m.scroll.trail = nil
		m.scroll.move(m.scroll.start)
		return m.set(ctx, m.scroll.start)
	}
	return nil
}

func (m *touchMapper) gesture(ctx context.Context, event rfb.ViewerGesture) error {
	switch event.Kind {
	case rfb.GestureKindEnd:
		if m.pinch == nil {
			return nil
		}
		m.pinch = nil
		return m.lift(ctx)
	case rfb.GestureKindMagnify, rfb.GestureKindRotate:
		return m.pinchGesture(ctx, event)
	}
	if m.busy() {
		return nil
	}
	at := point{float64(event.X), float64(event.Y)}
	switch event.Kind {
	case rfb.GestureKindSmartMagnify:
		for index := range 2 {
			if index > 0 {
				if err := sleep(ctx, doubleTapGap); err != nil {
					return err
				}
			}
			if err := m.tap(ctx, at, tapHold); err != nil {
				return err
			}
		}
		return nil
	case rfb.GestureKindLookup:
		return m.tap(ctx, at, lookupHold)
	case rfb.GestureKindSwipe:
		width := float64(m.geometry.Width - 1)
		y := clamp(at.Y, 0, float64(m.geometry.Height-1))
		switch event.Direction {
		case rfb.SwipeBack:
			from := point{0, y}
			return m.drag(ctx, touchEdge(m.geometry, from), from, point{width * swipeFraction, y}, swipeSteps, swipeStep, 0)
		case rfb.SwipeForward:
			from := point{width, y}
			return m.drag(ctx, touchEdge(m.geometry, from), from, point{width * (1 - swipeFraction), y}, swipeSteps, swipeStep, 0)
		}
	}
	return nil
}

func (m *touchMapper) tap(ctx context.Context, at point, hold time.Duration) error {
	if err := m.set(ctx, at); err != nil {
		return err
	}
	if err := sleep(ctx, hold); err != nil {
		return m.liftAfter(err)
	}
	return m.lift(ctx)
}

func (m *touchMapper) pinchGesture(ctx context.Context, event rfb.ViewerGesture) error {
	if m.finger != nil || m.scroll != nil {
		return nil
	}
	finished := event.Phase&(rfb.PhaseEnded|rfb.PhaseCancelled) != 0
	if m.pinch == nil {
		if finished || event.Phase&(rfb.PhaseBegan|rfb.PhaseChanged) == 0 {
			return nil
		}
		radius := m.geometry.points(pinchGap) / 2
		radius = min(radius, float64(m.geometry.Width-1)/3, float64(m.geometry.Height-1)/3)
		center := point{
			clamp(float64(event.X), radius, float64(m.geometry.Width-1)-radius),
			clamp(float64(event.Y), radius, float64(m.geometry.Height-1)-radius),
		}
		m.pinch = &pinchFingers{center: center, radius: radius, scale: 1, kinds: make(map[rfb.GestureKind]bool)}
	}
	pinch := m.pinch
	if finished {
		delete(pinch.kinds, event.Kind)
		if len(pinch.kinds) > 0 {
			return nil
		}
		m.pinch = nil
		return m.lift(ctx)
	}
	pinch.kinds[event.Kind] = true
	if event.Kind == rfb.GestureKindMagnify {
		pinch.scale = clamp(pinch.scale*(1+event.Amount), 0.2, 5)
	} else {
		pinch.angle += event.Amount
	}
	return m.set(ctx, pinch.fingers(m.geometry)...)
}

func (p *pinchFingers) fingers(geometry screenGeometry) []point {
	radians := p.angle * math.Pi / 180
	dx := p.radius * p.scale * math.Cos(radians)
	dy := -p.radius * p.scale * math.Sin(radians)
	width, height := float64(geometry.Width-1), float64(geometry.Height-1)
	return []point{
		{clamp(p.center.X-dx, 0, width), clamp(p.center.Y-dy, 0, height)},
		{clamp(p.center.X+dx, 0, width), clamp(p.center.Y+dy, 0, height)},
	}
}

func (m *touchMapper) systemCommand(ctx context.Context, command rfb.SystemCommand) error {
	switch command {
	case rfb.SystemCommandShowDesktop, rfb.SystemCommandLaunchpad:
		return m.input.PressButton(ctx, hid.ButtonHome)
	case rfb.SystemCommandMissionControl, rfb.SystemCommandAppWindows:
		if err := m.input.PressButton(ctx, hid.ButtonHome); err != nil {
			return err
		}
		if err := sleep(ctx, homeGap); err != nil {
			return err
		}
		return m.input.PressButton(ctx, hid.ButtonHome)
	}
	return nil
}

func clamp(value, low, high float64) float64 {
	return min(max(value, low), high)
}
