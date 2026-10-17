package main

import (
	"fmt"
	"log/slog"
	"sync"

	"github.com/nekohasekai/open-screen-sharing/internal/bgra"
	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
)

type handler struct {
	logger  *slog.Logger
	session rfb.SessionChoice

	access     sync.Mutex
	cursor     *rfb.Cursor
	updates    int
	pixels     int64
	clipboards chan *rfb.Clipboard

	drag         *rfb.RemoteDrag
	dropID       uint32
	dropPaths    []string
	destinations chan dropDestination
}

func newHandler(logger *slog.Logger, session rfb.SessionChoice) *handler {
	return &handler{
		logger:     logger,
		session:    session,
		clipboards: make(chan *rfb.Clipboard, 16),

		destinations: make(chan dropDestination, 16),
	}
}

func (h *handler) StateChanged(state rfb.State) {
	h.logger.Info("state", "state", state.String())
}

func (h *handler) ResizeFramebuffer(width, height int) (*bgra.Image, error) {
	h.logger.Info("framebuffer", "width", width, "height", height)
	return nil, nil
}

func (h *handler) FramebufferUpdated(update rfb.Update) {
	h.access.Lock()
	h.updates++
	for _, rectangle := range update.Rectangles {
		h.pixels += int64(rectangle.Width * rectangle.Height)
	}
	h.access.Unlock()
	h.logger.Debug("update", "dirty", update.Dirty.String(), "rectangles", len(update.Rectangles))
}

func (h *handler) CursorChanged(cursor *rfb.Cursor) {
	h.access.Lock()
	h.cursor = cursor
	h.access.Unlock()
	h.logger.Debug("cursor", "id", cursor.CacheID, "size", fmt.Sprint(cursor.Width, "x", cursor.Height),
		"hotspot", fmt.Sprint(cursor.HotspotX, ",", cursor.HotspotY))
}

func (h *handler) CursorMoved(x, y int) {
	h.logger.Debug("cursor moved", "x", x, "y", y)
}

func (h *handler) DisplaysChanged(layout *rfb.DisplayLayout) {
	h.logger.Info("displays", "version", layout.Version, "desktop", fmt.Sprint(layout.DesktopWidth, "x", layout.DesktopHeight),
		"framebuffer", fmt.Sprint(layout.FramebufferWidth, "x", layout.FramebufferHeight),
		"current", fmt.Sprintf("%#x", layout.Current), "state", fmt.Sprintf("%#x", layout.SessionState))
	for _, display := range layout.Displays {
		h.logger.Info("display", "id", display.ID, "density", display.Density, "scale", display.Scale,
			"logical", display.Logical.String(), "backing", display.Backing.String(), "flags", fmt.Sprintf("%#x", display.Flags))
	}
}

func (h *handler) ClipboardReceived(clipboard *rfb.Clipboard) {
	for i, item := range clipboard.Items {
		for _, flavor := range item.Flavors {
			h.logger.Info("clipboard flavor", "item", i, "type", flavor.Type, "bytes", len(flavor.Data),
				"tags", fmt.Sprint(flavor.Tags), "promise", flavor.Promise)
		}
	}
	text, found := clipboard.Text()
	if found {
		h.logger.Info("clipboard text", "text", text)
	}
	select {
	case h.clipboards <- clipboard:
	default:
	}
}

func (h *handler) ClipboardFailed(problem rfb.ClipboardProblem, err error) {
	h.logger.Warn("clipboard", "problem", int(problem), "error", err)
}

func (h *handler) Bell() {
	h.logger.Info("bell")
}

func (h *handler) KeyboardSourceChanged(source rfb.KeyboardSource) {
	h.logger.Info("keyboard source", "id", source.ID, "secure", source.SecureInput)
}

func (h *handler) StatusReceived(status rfb.Status) {
	h.logger.Debug("status", "status", status.String())
}

func (h *handler) DeviceInfoReceived(info rfb.DeviceInfo) {
	h.logger.Info("device", "model", info.Model, "color", info.DeviceColor, "enclosure", info.EnclosureColor,
		"dynamic-drag", info.DynamicDrag)
}

func (h *handler) SystemCommandsChanged(commands []rfb.SystemCommand) {
	h.logger.Info("system commands", "commands", fmt.Sprint(commands))
}

func (h *handler) ChooseSession(request rfb.SessionRequest) (rfb.SessionChoice, error) {
	h.logger.Info("session selection", "console", request.ConsoleUser, "choices", fmt.Sprint(request.Choices), "answer", h.session.String())
	return h.session, nil
}
