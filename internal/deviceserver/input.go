package deviceserver

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/nekohasekai/open-screen-sharing/internal/coredevice"
	"github.com/nekohasekai/open-screen-sharing/internal/coredevice/hid"
	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
)

const (
	inputQueueLimit = 4096
	inputTimeout    = 10 * time.Second
)

type inputItem struct {
	session *rfb.ServerSession
	event   rfb.ViewerEvent
	write   *rfb.Clipboard
	release bool
	moves   bool
}

type inputWorker struct {
	server *Server
	logger *slog.Logger
	done   chan struct{}

	access   sync.Mutex
	queue    []inputItem
	dropping bool
	buttons  map[*rfb.ServerSession]uint8
	wake     chan struct{}

	device     *coredevice.Device
	client     *hid.Client
	generation uint64
	touch      touchMapper
	keys       *keyboard
}

func newInputWorker(server *Server, logger *slog.Logger) *inputWorker {
	return &inputWorker{
		server:  server,
		logger:  logger,
		wake:    make(chan struct{}, 1),
		done:    make(chan struct{}),
		keys:    newKeyboard(),
		buttons: make(map[*rfb.ServerSession]uint8),
	}
}

func (w *inputWorker) push(item inputItem) {
	w.access.Lock()
	if len(w.queue) >= inputQueueLimit && item.event != nil {
		if !w.dropping {
			w.logger.Warn("the device takes input slower than the viewer sends it; dropping input")
		}
		w.dropping = true
		w.access.Unlock()
		return
	}
	w.dropping = false
	w.queue = append(w.queue, item)
	w.access.Unlock()
	w.signal()
}

func (w *inputWorker) signal() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *inputWorker) post(session *rfb.ServerSession, event rfb.ViewerEvent) {
	pointer, isPointer := event.(rfb.ViewerPointer)
	if !isPointer {
		w.push(inputItem{session: session, event: event})
		return
	}
	w.access.Lock()
	held, known := w.buttons[session]
	w.buttons[session] = pointer.Buttons
	moves := known && held == pointer.Buttons
	if count := len(w.queue); moves && count > 0 {
		last := &w.queue[count-1]
		if last.moves && last.session == session {
			last.event = pointer
			w.access.Unlock()
			return
		}
	}
	w.access.Unlock()
	w.push(inputItem{session: session, event: event, moves: moves})
}

func (w *inputWorker) postClipboard(clipboard *rfb.Clipboard) {
	w.push(inputItem{write: clipboard})
}

func (w *inputWorker) sessionEnded(session *rfb.ServerSession) {
	w.access.Lock()
	delete(w.buttons, session)
	w.access.Unlock()
	w.push(inputItem{session: session, release: true})
}

func (w *inputWorker) forget() {
	w.push(inputItem{})
}

func (w *inputWorker) run(ctx context.Context) {
	defer close(w.done)
	defer w.closeClient()
	for {
		item, ok := w.next(ctx)
		if !ok {
			return
		}
		w.handle(ctx, item)
	}
}

func (w *inputWorker) next(ctx context.Context) (inputItem, bool) {
	for {
		w.access.Lock()
		if len(w.queue) > 0 {
			item := w.queue[0]
			w.queue[0] = inputItem{}
			w.queue = w.queue[1:]
			w.access.Unlock()
			return item, true
		}
		w.access.Unlock()
		select {
		case <-ctx.Done():
			return inputItem{}, false
		case <-w.wake:
		}
	}
}

func (w *inputWorker) closeClient() {
	if w.client != nil {
		err := w.client.Close()
		if err != nil {
			w.logger.Debug("close the input client", "error", err)
		}
	}
	w.client = nil
	w.device = nil
	w.resetState()
	w.keys.capsLock = false
}

func (w *inputWorker) resetState() {
	w.touch.reset()
	w.keys.reset()
}

func (w *inputWorker) handle(ctx context.Context, item inputItem) {
	a := w.server.currentDevice()
	if w.client != nil && (a == nil || a.device != w.device) {
		w.closeClient()
	}
	if a == nil {
		return
	}
	switch {
	case item.write != nil:
		w.writeClipboard(ctx, a, item.write)
		return
	case item.release:
		if w.client != nil {
			releaseContext, cancel := context.WithTimeout(ctx, inputTimeout)
			err := w.client.ReleaseAll(releaseContext)
			cancel()
			if err != nil {
				w.logger.Debug("release input", "error", err)
			}
			w.resetState()
			if len(w.server.rfb.Sessions()) == 0 {
				w.closeClient()
			}
		}
		return
	case item.event == nil:
		return
	}
	running, wait, generation := w.server.streams.inputState()
	if !running {
		return
	}
	if sleep(ctx, wait) != nil {
		return
	}
	if w.client == nil {
		w.client = hid.New(a.device, w.logger)
		w.device = a.device
		w.generation = generation
	}
	eventContext, cancel := context.WithTimeout(ctx, inputTimeout)
	defer cancel()
	if generation != w.generation {
		w.generation = generation
		err := w.client.ReleaseAll(eventContext)
		if err != nil {
			w.logger.Debug("release input after the video restarted", "error", err)
		}
		w.resetState()
	}
	w.touch.input = w.client
	w.touch.geometry = w.server.geometry()
	if w.touch.geometry.Width < 2 || w.touch.geometry.Height < 2 {
		return
	}
	err := w.dispatch(eventContext, item)
	if err != nil && ctx.Err() == nil {
		w.logger.Debug("send input", "event", item.event, "error", err)
	}
}

func (w *inputWorker) dispatch(ctx context.Context, item inputItem) error {
	switch event := item.event.(type) {
	case rfb.ViewerPointer:
		return w.touch.pointer(ctx, event)
	case rfb.ViewerKey:
		return w.keys.handle(ctx, w.client, event)
	case rfb.ViewerScroll:
		if event.Momentum != 0 {
			return nil
		}
		if event.Continuous && event.Phase != rfb.PhaseNone {
			return w.touch.continuousScroll(ctx, event)
		}
		deltaX, deltaY := w.mergeWheel(ctx, item.session, event)
		return w.touch.wheel(ctx, event.X, event.Y, deltaX, deltaY)
	case rfb.ViewerGesture:
		if event.Kind == rfb.GestureKindScroll {
			return nil
		}
		return w.touch.gesture(ctx, event)
	case rfb.ViewerSystemCommand:
		return w.touch.systemCommand(ctx, event.Command)
	}
	return nil
}

func isWheel(event rfb.ViewerScroll) bool {
	return event.Momentum == 0 && (!event.Continuous || event.Phase == rfb.PhaseNone)
}

func wheelDelta(event rfb.ViewerScroll) (float64, float64) {
	deltaX, deltaY := event.DeltaX, event.DeltaY
	if deltaX == 0 && deltaY == 0 {
		deltaX, deltaY = float64(event.LinesX*10), float64(event.LinesY*10)
	}
	return deltaX, deltaY
}

func (w *inputWorker) mergeWheel(ctx context.Context, session *rfb.ServerSession, first rfb.ViewerScroll) (float64, float64) {
	deltaX, deltaY := wheelDelta(first)
	if sleep(ctx, wheelMerge) != nil {
		return deltaX, deltaY
	}
	w.access.Lock()
	defer w.access.Unlock()
	for len(w.queue) > 0 {
		next := w.queue[0]
		scroll, isScroll := next.event.(rfb.ViewerScroll)
		if !isScroll || next.session != session || !isWheel(scroll) {
			break
		}
		x, y := wheelDelta(scroll)
		deltaX += x
		deltaY += y
		w.queue[0] = inputItem{}
		w.queue = w.queue[1:]
	}
	return deltaX, deltaY
}
