package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
)

type dropDestination struct {
	id   uint32
	path string
}

func (h *handler) RemoteDragChanged(drag *rfb.RemoteDrag) {
	h.access.Lock()
	h.drag = drag
	h.access.Unlock()
	if drag == nil {
		h.logger.Info("remote drag ended")
		return
	}
	for i, item := range drag.Clipboard.Items {
		for _, flavor := range item.Flavors {
			h.logger.Debug("remote drag flavor", "item", i, "type", flavor.Type, "bytes", len(flavor.Data), "tags", fmt.Sprint(flavor.Tags))
		}
	}
	h.logger.Info("remote drag", "items", fmt.Sprint(drag.Items), "image", len(drag.Image))
}

func (h *handler) DropDestinationReceived(id uint32, path string) {
	h.logger.Info("drop destination", "id", id, "path", path)
	select {
	case h.destinations <- dropDestination{id: id, path: path}:
	default:
	}
}

func runDragAction(ctx context.Context, client *rfb.Client, h *handler, verb, argument string) (bool, error) {
	switch verb {
	case "drop-start":
		paths := strings.Split(argument, ",")
		named := make([]string, len(paths))
		for i, path := range paths {
			info, err := os.Stat(path)
			if err != nil {
				return true, err
			}
			named[i] = path
			if info.IsDir() {
				named[i] += "/"
			}
		}
		id, err := client.SendDrag(rfb.NewFileClipboard(named))
		if err != nil {
			return true, err
		}
		h.access.Lock()
		h.dropID, h.dropPaths = id, paths
		h.access.Unlock()
		h.logger.Info("drop started", "id", id)
		return true, nil
	case "drop-cancel":
		h.access.Lock()
		id := h.dropID
		h.access.Unlock()
		return true, client.CancelDrag(id)
	case "drop-finish":
		timeout, err := time.ParseDuration(argument)
		if err != nil {
			return true, err
		}
		return true, finishDrop(ctx, client, h, timeout)
	case "drag-request":
		return true, client.RequestDrag()
	case "drag-cancel":
		return true, client.CancelRemoteDrag()
	case "drag-get":
		h.access.Lock()
		drag := h.drag
		h.access.Unlock()
		if drag == nil || len(drag.Items) == 0 {
			return true, errors.New("no remote drag of files")
		}
		for _, item := range drag.Items {
			err := copyFile(ctx, client, h, false, item.Path, argument)
			if err != nil {
				return true, err
			}
		}
		return true, nil
	}
	return false, nil
}

func finishDrop(ctx context.Context, client *rfb.Client, h *handler, timeout time.Duration) error {
	h.access.Lock()
	id, paths := h.dropID, h.dropPaths
	h.access.Unlock()
	deadline := time.After(timeout)
	for {
		select {
		case destination := <-h.destinations:
			if destination.id != id {
				h.logger.Info("drop destination of another drag", "id", destination.id)
				continue
			}
			for _, path := range paths {
				err := copyFile(ctx, client, h, true, path, destination.path)
				if err != nil {
					return err
				}
			}
			return nil
		case <-deadline:
			return errors.New("no drop destination in time")
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
