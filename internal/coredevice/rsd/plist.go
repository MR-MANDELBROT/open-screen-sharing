package rsd

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"time"

	"howett.net/plist"
)

const maxPlistLength = 64 << 20

type PlistConn struct {
	conn net.Conn
}

func NewPlistConn(conn net.Conn) *PlistConn {
	return &PlistConn{conn: conn}
}

func (c *PlistConn) Send(message map[string]any) error {
	data, err := plist.Marshal(message, plist.XMLFormat)
	if err != nil {
		return fmt.Errorf("encode %v: %w", message, err)
	}
	frame := binary.BigEndian.AppendUint32(make([]byte, 0, 4+len(data)), uint32(len(data)))
	_, err = c.conn.Write(append(frame, data...))
	return err
}

func (c *PlistConn) Receive() (map[string]any, error) {
	header := make([]byte, 4)
	_, err := io.ReadFull(c.conn, header)
	if err != nil {
		return nil, err
	}
	length := binary.BigEndian.Uint32(header)
	if length > maxPlistLength {
		return nil, fmt.Errorf("message of %d bytes is too large", length)
	}
	data := make([]byte, length)
	_, err = io.ReadFull(c.conn, data)
	if err != nil {
		return nil, err
	}
	var message map[string]any
	_, err = plist.Unmarshal(data, &message)
	if err != nil {
		return nil, fmt.Errorf("decode message: %w", err)
	}
	return message, nil
}

func (c *PlistConn) Request(ctx context.Context, message map[string]any) (map[string]any, error) {
	stop := c.applyContext(ctx)
	defer stop()
	err := c.Send(message)
	if err == nil {
		var reply map[string]any
		reply, err = c.Receive()
		if err == nil {
			return reply, nil
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return nil, err
}

// pymobiledevice3 reads two replies after RSDCheckin: RSDCheckin first,
// then StartService, whose Error field reports a failure.
func (c *PlistConn) Checkin(ctx context.Context, label string) error {
	stop := c.applyContext(ctx)
	defer stop()
	err := c.checkin(label)
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func (c *PlistConn) checkin(label string) error {
	err := c.Send(map[string]any{"Label": label, "ProtocolVersion": "2", "Request": "RSDCheckin"})
	if err != nil {
		return fmt.Errorf("RSDCheckin: %w", err)
	}
	reply, err := c.Receive()
	if err != nil {
		return fmt.Errorf("RSDCheckin: %w", err)
	}
	if reply["Request"] != "RSDCheckin" {
		return fmt.Errorf("RSDCheckin: unexpected reply %v", reply)
	}
	reply, err = c.Receive()
	if err != nil {
		return fmt.Errorf("RSDCheckin: %w", err)
	}
	if reply["Request"] != "StartService" {
		return fmt.Errorf("RSDCheckin: unexpected reply %v", reply)
	}
	if message, failed := reply["Error"]; failed {
		return fmt.Errorf("RSDCheckin: service did not start: %v", message)
	}
	return nil
}

func (c *PlistConn) applyContext(ctx context.Context) func() {
	stopAfter := context.AfterFunc(ctx, func() {
		c.conn.SetDeadline(time.Unix(1, 0))
	})
	return func() {
		if stopAfter() || ctx.Err() == nil {
			c.conn.SetDeadline(time.Time{})
		}
	}
}

func (c *PlistConn) Write(data []byte) (int, error) {
	return c.conn.Write(data)
}

func (c *PlistConn) Close() error {
	return c.conn.Close()
}
