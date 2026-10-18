package rsd

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"

	E "github.com/sagernet/sing/common/exceptions"
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
		return E.Cause(err, "encode ", fmt.Sprint(message))
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
		return nil, E.New("rsd: message of ", length, " bytes is too large, at most ", maxPlistLength)
	}
	data := make([]byte, length)
	_, err = io.ReadFull(c.conn, data)
	if err != nil {
		return nil, err
	}
	var message map[string]any
	_, err = plist.Unmarshal(data, &message)
	if err != nil {
		return nil, E.Cause(err, "decode message")
	}
	return message, nil
}

func (c *PlistConn) Request(ctx context.Context, message map[string]any) (result map[string]any, resultErr error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { c.conn.Close() })
	defer func() {
		if !stop() {
			c.conn.Close()
			result = nil
			resultErr = ctx.Err()
		}
	}()
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
// then StartService, whose Error field reports a failure (pymobiledevice3
// 9c33842, pymobiledevice3/remote/remote_service_discovery.py:437-447).
func (c *PlistConn) Checkin(ctx context.Context, label string) (err error) {
	if err = ctx.Err(); err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { c.conn.Close() })
	defer func() {
		if !stop() {
			c.conn.Close()
			err = ctx.Err()
		}
	}()
	err = c.checkin(label)
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func (c *PlistConn) checkin(label string) error {
	err := c.Send(map[string]any{"Label": label, "ProtocolVersion": "2", "Request": "RSDCheckin"})
	if err != nil {
		return E.Cause(err, "RSDCheckin")
	}
	reply, err := c.Receive()
	if err != nil {
		return E.Cause(err, "RSDCheckin")
	}
	if reply["Request"] != "RSDCheckin" {
		return E.New("RSDCheckin: unexpected reply ", fmt.Sprint(reply))
	}
	reply, err = c.Receive()
	if err != nil {
		return E.Cause(err, "RSDCheckin")
	}
	if reply["Request"] != "StartService" {
		return E.New("RSDCheckin: unexpected reply ", fmt.Sprint(reply))
	}
	if message, failed := reply["Error"]; failed {
		return E.New("RSDCheckin: service did not start: ", fmt.Sprint(message))
	}
	return nil
}

func (c *PlistConn) Write(data []byte) (int, error) {
	return c.conn.Write(data)
}

func (c *PlistConn) Close() error {
	return c.conn.Close()
}
