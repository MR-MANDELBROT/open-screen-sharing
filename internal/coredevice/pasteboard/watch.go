package pasteboard

import (
	"context"
	"errors"
	"time"

	E "github.com/sagernet/sing/common/exceptions"
)

const (
	// pymobiledevice3 uses this remote notification-proxy service and sends
	// ObserveNotification (pymobiledevice3 1146717,
	// pymobiledevice3/services/notification_proxy.py:25,76).
	notificationProxyService = "com.apple.mobile.notification_proxy.shim.remote"
	changedNotification      = "com.apple.pasteboard.notify.changed"

	writeReplyWait = time.Second

	reconnectDelay = 2 * time.Second
)

func (c *Client) Watch(ctx context.Context, changed func(Snapshot)) error {
	var last change
	started := false
	for {
		err := c.watch(ctx, &last, &started, changed)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		c.logger.Debug("pasteboard watch connection lost; reconnecting", "error", err)
		timer := time.NewTimer(reconnectDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (c *Client) watch(ctx context.Context, last *change, started *bool, changed func(Snapshot)) error {
	conn, err := c.device.DialShim(ctx, notificationProxyService)
	if err != nil {
		return E.Cause(err, "pasteboard")
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	err = conn.Send(map[string]any{"Command": "ObserveNotification", "Name": changedNotification})
	if err != nil {
		return E.Cause(err, "pasteboard: observe ", changedNotification)
	}
	if !*started {
		snapshot, err := c.pull(ctx, policyAllPromised)
		if err != nil && !errors.Is(err, errRefused) {
			return err
		}
		*last = snapshot.change
		*started = true
	} else {
		c.check(ctx, last, changed)
	}
	for {
		message, err := conn.Receive()
		if err != nil {
			return E.Cause(err, "pasteboard: receive notification")
		}
		switch message["Command"] {
		case "RelayNotification":
			if message["Name"] == changedNotification {
				c.check(ctx, last, changed)
			}
		case "ProxyDeath":
			return E.New("pasteboard: the notification proxy stopped")
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
		c.logger.Warn("read the pasteboard after a change", "error", err)
		return
	}
	c.waitWrites(ctx, writeReplyWait)
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
