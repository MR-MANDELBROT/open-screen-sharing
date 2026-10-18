package pasteboard

import (
	"context"
	"errors"

	"github.com/nekohasekai/open-screen-sharing/internal/coredevice/rsd"
	E "github.com/sagernet/sing/common/exceptions"
)

const (
	// pymobiledevice3 uses this remote notification-proxy service and sends
	// ObserveNotification (pymobiledevice3 1146717,
	// pymobiledevice3/services/notification_proxy.py:25,76).
	notificationProxyService = "com.apple.mobile.notification_proxy.shim.remote"
	changedNotification      = "com.apple.pasteboard.notify.changed"
)

func (c *Client) Watch(ctx context.Context, changed func(Snapshot)) error {
	ctx, cancel := c.operationContext(ctx)
	defer cancel()
	var last change
	for redialed := false; ; redialed = true {
		conn, err := c.device.DialShim(ctx, notificationProxyService)
		if err != nil {
			if ctx.Err() != nil {
				return context.Cause(ctx)
			}
			return E.Cause(err, "pasteboard")
		}
		delivered, err := c.watch(ctx, conn, &last, redialed, changed)
		conn.Close()
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		if redialed && !delivered {
			return err
		}
		c.logger.Debug("pasteboard notification proxy connection ended; reconnecting", "error", err)
	}
}

func (c *Client) watch(ctx context.Context, conn *rsd.PlistConn, last *change, redialed bool, changed func(Snapshot)) (bool, error) {
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	err := conn.Send(map[string]any{"Command": "ObserveNotification", "Name": changedNotification})
	if err != nil {
		return false, E.Cause(err, "pasteboard: observe ", changedNotification)
	}
	if redialed {
		c.check(ctx, last, changed)
	} else {
		snapshot, err := c.pull(ctx, policyAllPromised)
		if err != nil && !errors.Is(err, errRefused) && ctx.Err() == nil {
			c.logger.Warn("read the pasteboard", "error", err)
		}
		*last = snapshot.change
	}
	delivered := false
	for {
		message, err := conn.Receive()
		if err != nil {
			return delivered, E.Cause(err, "pasteboard: receive notification")
		}
		switch message["Command"] {
		case "RelayNotification":
			if message["Name"] == changedNotification {
				delivered = true
				c.check(ctx, last, changed)
			}
		case "ProxyDeath":
			return delivered, E.New("pasteboard: the notification proxy stopped")
		}
	}
}

func (c *Client) check(ctx context.Context, last *change, changed func(Snapshot)) {
	snapshot, err := c.Read(ctx)
	if errors.Is(err, errRefused) {
		c.logger.Debug("the device refused to share a pasteboard change")
		return
	}
	if err != nil {
		if ctx.Err() == nil {
			c.logger.Warn("read the pasteboard after a change", "error", err)
		}
		return
	}
	if snapshot.change.equal(*last) {
		return
	}
	*last = snapshot.change
	switch {
	case snapshot.Empty(), c.isOwn(snapshot):
	case snapshot.Concealed():
		c.logger.Debug("not sharing a pasteboard change marked as concealed")
	default:
		changed(snapshot)
	}
}
