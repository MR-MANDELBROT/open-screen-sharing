package deviceserver

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/nekohasekai/open-screen-sharing/internal/coredevice/pasteboard"
	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
	E "github.com/sagernet/sing/common/exceptions"
)

var clipboardImageTypes = []string{
	pasteboard.TypePNG, pasteboard.TypeJPEG, pasteboard.TypeHEIC, pasteboard.TypeTIFF,
	pasteboard.TypeGIF, pasteboard.TypeWebP,
}

type clipboardBridge struct {
	server *Server
	logger *slog.Logger

	access    sync.Mutex
	sharers   map[*rfb.ServerSession]bool
	watched   *attachment
	stopWatch context.CancelFunc
	latest    *rfb.Clipboard
	writing   *rfb.Clipboard
}

func newClipboardBridge(server *Server, logger *slog.Logger) *clipboardBridge {
	return &clipboardBridge{server: server, logger: logger, sharers: make(map[*rfb.ServerSession]bool)}
}

func (c *clipboardBridge) enabled() bool {
	return c.server.options.SharedClipboard
}

func (c *clipboardBridge) sharing(session *rfb.ServerSession, enabled bool) {
	if !c.enabled() {
		return
	}
	c.access.Lock()
	if enabled {
		c.sharers[session] = true
	} else {
		delete(c.sharers, session)
	}
	c.access.Unlock()
	c.update()
}

func (c *clipboardBridge) update() {
	if !c.enabled() {
		return
	}
	a := c.server.currentDevice()
	c.access.Lock()
	defer c.access.Unlock()
	want := a != nil && len(c.sharers) > 0
	if want && c.watched == a {
		return
	}
	if c.stopWatch != nil {
		c.stopWatch()
		c.stopWatch = nil
		c.watched = nil
		c.latest = nil
	}
	if !want {
		return
	}
	ctx, cancel := context.WithCancel(a.ctx)
	c.stopWatch = cancel
	c.watched = a
	c.logger.Debug("watching the device's clipboard")
	go func() {
		err := a.pasteboard.Watch(ctx, func(snapshot pasteboard.Snapshot) {
			clipboard := clipboardOf(snapshot)
			c.access.Lock()
			current := c.watched == a && ctx.Err() == nil
			if current {
				c.latest = clipboard
			}
			c.access.Unlock()
			if current {
				c.server.rfb.ClipboardChanged()
			}
		})
		if err != nil && !errors.Is(err, context.Canceled) {
			c.logger.Debug("watch the device's clipboard", "error", err)
		}
	}()
}

func (c *clipboardBridge) read(ctx context.Context) (*rfb.Clipboard, error) {
	if !c.enabled() {
		return &rfb.Clipboard{}, nil
	}
	c.access.Lock()
	latest := c.latest
	c.access.Unlock()
	if latest != nil {
		return latest, nil
	}
	a := c.server.currentDevice()
	if a == nil {
		return nil, E.New("the device is not connected")
	}
	snapshot, err := a.pasteboard.Read(ctx)
	if err != nil {
		return nil, err
	}
	clipboard := clipboardOf(snapshot)
	c.access.Lock()
	if c.watched == a && c.latest == nil {
		c.latest = clipboard
	}
	c.access.Unlock()
	return clipboard, nil
}

func (c *clipboardBridge) viewerClipboard(clipboard *rfb.Clipboard) {
	if !c.enabled() {
		return
	}
	c.access.Lock()
	c.writing = clipboard
	c.access.Unlock()
	c.server.input.postClipboard(clipboard)
}

func (c *clipboardBridge) written(a *attachment, clipboard *rfb.Clipboard, err error) {
	if err != nil {
		c.logger.Warn("write the clipboard to the device", "error", err)
		return
	}
	c.access.Lock()
	current := c.writing == clipboard
	if current {
		c.writing = nil
	}
	announce := current && c.watched == a
	if announce {
		c.latest = clipboard
	}
	c.access.Unlock()
	if announce {
		c.server.rfb.ClipboardChanged()
	}
}

func (w *inputWorker) writeClipboard(ctx context.Context, a *attachment, clipboard *rfb.Clipboard) {
	ctx, cancel := context.WithTimeout(ctx, inputTimeout)
	defer cancel()
	board := a.pasteboard
	written := func(err error) {
		w.server.clip.written(a, clipboard, err)
	}
	if text, found := clipboard.Text(); found {
		if err := board.WriteText(ctx, text, written); err != nil {
			written(err)
		}
		return
	}
	for _, imageType := range clipboardImageTypes {
		for _, item := range clipboard.Items {
			for _, flavor := range item.Flavors {
				if flavor.Type == imageType && !flavor.Promise && len(flavor.Data) > 0 {
					if err := board.WriteImage(ctx, imageType, flavor.Data, written); err != nil {
						written(err)
					}
					return
				}
			}
		}
	}
}

func clipboardOf(snapshot pasteboard.Snapshot) *rfb.Clipboard {
	if snapshot.Concealed() {
		return &rfb.Clipboard{}
	}
	var item rfb.ClipboardItem
	if text, found := snapshot.Text(); found {
		item.Flavors = append(item.Flavors, rfb.ClipboardFlavor{Type: rfb.TypeUTF8PlainText, Data: []byte(text)})
	}
	if imageType, data, found := snapshot.Image(); found {
		item.Flavors = append(item.Flavors, rfb.ClipboardFlavor{Type: imageType, Data: data})
	}
	if len(item.Flavors) == 0 {
		return &rfb.Clipboard{}
	}
	return &rfb.Clipboard{Items: []rfb.ClipboardItem{item}}
}
