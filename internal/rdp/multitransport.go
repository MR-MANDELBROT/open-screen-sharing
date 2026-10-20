package rdp

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"io"
	"net"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nekohasekai/open-screen-sharing/internal/rdp/rdpeudp"

	E "github.com/sagernet/sing/common/exceptions"
)

const (
	multitransportReliableUDP = 0x0001
	multitransportSoftSync    = 0x0200

	securityTransportRequest  = 0x0002
	securityTransportResponse = 0x0004

	multitransportSuccess = 0x00000000
	multitransportAbort   = 0x80004004
	multitransportTimeout = 10 * time.Second
	tunnelCloseTimeout    = 5 * time.Second
	multitransportCookie  = 16

	tunnelCreateRequest   = 0x0
	tunnelCreateResponse  = 0x1
	tunnelData            = 0x2
	tunnelHeaderLength    = 4
	tunnelSubheaderLength = 2
)

type tunnel struct {
	conn    *tls.Conn
	udp     *rdpeudp.Conn
	access  sync.Mutex
	read    atomic.Uint64
	written atomic.Uint64
}

func (t *tunnel) write(action byte, subheaders []byte, payload []byte) error {
	b := binary.LittleEndian.AppendUint16([]byte{action}, uint16(len(payload)))
	b = append(b, byte(tunnelHeaderLength+len(subheaders)))
	b = append(b, subheaders...)
	b = append(b, payload...)
	t.access.Lock()
	defer t.access.Unlock()
	n, err := t.conn.Write(b)
	t.written.Add(uint64(n))
	return err
}

func (t *tunnel) receive() (action byte, subheaders []byte, payload []byte, err error) {
	var header [tunnelHeaderLength]byte
	_, err = io.ReadFull(t.conn, header[:])
	if err != nil {
		return
	}
	headerLength := int(header[3])
	if headerLength < tunnelHeaderLength {
		err = E.New("rdp: tunnel header declares ", headerLength, " bytes")
		return
	}
	body := make([]byte, headerLength-tunnelHeaderLength+int(binary.LittleEndian.Uint16(header[1:])))
	_, err = io.ReadFull(t.conn, body)
	if err != nil {
		return
	}
	t.read.Add(uint64(tunnelHeaderLength + len(body)))
	return header[0] & 0x0f, body[:headerLength-tunnelHeaderLength], body[headerLength-tunnelHeaderLength:], nil
}

func (t *tunnel) create(requestID uint32, cookie []byte) error {
	request := binary.LittleEndian.AppendUint32(nil, requestID)
	request = binary.LittleEndian.AppendUint32(request, 0)
	err := t.write(tunnelCreateRequest, nil, append(request, cookie...))
	if err != nil {
		return err
	}
	action, _, response, err := t.receive()
	if err != nil {
		return err
	}
	if action != tunnelCreateResponse || len(response) < 4 {
		return E.New("rdp: tunnel action ", action, " of ", len(response), " bytes is not a create response")
	}
	if result := binary.LittleEndian.Uint32(response); result != multitransportSuccess {
		return E.New("rdp: the server rejected the tunnel with 0x", strconv.FormatUint(uint64(result), 16))
	}
	return nil
}

func (c *Client) receiveMultitransport(r *reader) error {
	requestID := r.u32()
	protocol := r.u16()
	r.take(2)
	cookie := r.take(multitransportCookie)
	if r.err != nil {
		return E.Cause(r.err, "rdp: parse multitransport request")
	}
	if protocol != multitransportReliableUDP || c.mcs.multitransport&multitransportSoftSync == 0 {
		c.logger.Debug("decline multitransport request", "protocol", protocol, "server_flags", c.mcs.multitransport)
		return c.respondMultitransport(requestID, multitransportAbort)
	}
	cookie = slices.Clone(cookie)
	c.tunnels.Go(func() { c.runTunnel(requestID, cookie) })
	return nil
}

func (c *Client) respondMultitransport(requestID uint32, result uint32) error {
	b := binary.LittleEndian.AppendUint16(nil, securityTransportResponse)
	b = binary.LittleEndian.AppendUint16(b, 0)
	b = binary.LittleEndian.AppendUint32(b, requestID)
	return c.mcs.send(c.mcs.message, binary.LittleEndian.AppendUint32(b, result))
}

func (c *Client) runTunnel(requestID uint32, cookie []byte) {
	t, err := c.dialTunnel(requestID, cookie)
	if err != nil {
		if c.ctx.Err() == nil {
			c.logger.Info("continue over TCP without the UDP transport", "error", err)
			_ = c.respondMultitransport(requestID, multitransportAbort)
		}
		return
	}
	context.AfterFunc(c.ctx, func() { _ = t.conn.Close() })
	c.dynamic.access.Lock()
	c.dynamic.tunnel = func(data []byte) error { return t.write(tunnelData, nil, data) }
	c.dynamic.access.Unlock()
	c.tunnel.Store(t)
	err = c.respondMultitransport(requestID, multitransportSuccess)
	if err != nil {
		return
	}
	c.logger.Debug("open the UDP tunnel", "rtt", t.udp.RoundTrip())
	payloads := make(chan []byte, receiveQueue)
	c.tunnels.Go(func() {
		defer close(payloads)
		c.failTunnel(c.readTunnel(t, payloads))
	})
	for payload := range payloads {
		select {
		case <-c.dynamic.switched:
		case <-c.ctx.Done():
			return
		}
		c.processAccess.Lock()
		err := c.dynamic.process(payload, true)
		c.processAccess.Unlock()
		if err != nil {
			c.failTunnel(err)
			return
		}
	}
}

func (c *Client) failTunnel(err error) {
	if E.IsClosed(err) {
		select {
		case <-c.ctx.Done():
			return
		case <-time.After(tunnelCloseTimeout):
		}
	}
	if c.ctx.Err() != nil {
		return
	}
	err = E.Cause(err, "rdp: receive from the UDP tunnel")
	c.lifecycle.Lock()
	c.tunnelErr = err
	c.lifecycle.Unlock()
	c.abort(err)
}

func (c *Client) dialTunnel(requestID uint32, cookie []byte) (*tunnel, error) {
	ctx, cancel := context.WithTimeout(c.ctx, multitransportTimeout)
	defer cancel()
	conn, err := rdpeudp.Dial(ctx, c.transport.tcp.RemoteAddr().String(), cookie, c.logger)
	if err != nil {
		return nil, E.Cause(err, "rdp: connect the UDP transport")
	}
	host, _, _ := net.SplitHostPort(c.address)
	t := &tunnel{udp: conn, conn: tls.Client(conn, &tls.Config{
		MinVersion: tls.VersionTLS12,
		// Windows 11 26200 drops a packet that arrives right behind the first data packet of a UDP
		// connection, and a TLS 1.3 ClientHello of crypto/tls takes two packets, so the tunnel would
		// wait a retransmission timeout before the handshake continues.
		MaxVersion:         tls.VersionTLS12,
		ServerName:         host,
		InsecureSkipVerify: true,
		// rdpbase.dll of Windows 11 26200 serves the tunnel with the certificate of the TCP listener.
		VerifyConnection: func(state tls.ConnectionState) error {
			if !bytes.Equal(state.PeerCertificates[0].Raw, c.transport.certificate.Raw) {
				return ErrCertificateChanged
			}
			return nil
		},
	})}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	err = t.conn.HandshakeContext(ctx)
	if err != nil {
		err = E.Cause(err, "rdp: perform the UDP TLS handshake")
	} else {
		err = t.create(requestID, cookie)
	}
	if !stop() && err == nil {
		err = ctx.Err()
	}
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return t, nil
}

func (c *Client) readTunnel(t *tunnel, payloads chan<- []byte) error {
	// rdpbase.dll of Windows 11 26200 sends and reads a whole auto-detect PDU as the subheader, so
	// subHeaderLength and subHeaderType are its headerLength and headerTypeId.
	detector := &autodetect{logger: c.logger, roundTrip: &c.roundTrip, respond: func(response []byte) error {
		return t.write(tunnelData, response, nil)
	}}
	// mstscax.dll of Windows 11 26200 counts only tunnel payloads toward the bandwidth byteCount.
	var received uint64
	for {
		action, subheaders, payload, err := t.receive()
		if err != nil {
			return err
		}
		arrival := time.Now()
		if action != tunnelData {
			return E.New("rdp: unexpected tunnel action ", action)
		}
		received += uint64(len(payload))
		for len(subheaders) > 0 {
			length := int(subheaders[0])
			if length < tunnelSubheaderLength || length > len(subheaders) {
				return E.New("rdp: tunnel subheader declares ", length, " of ", len(subheaders), " bytes")
			}
			err = detector.receive(&reader{data: subheaders[:length]}, arrival, received)
			if err != nil {
				return err
			}
			subheaders = subheaders[length:]
		}
		if len(payload) == 0 {
			continue
		}
		select {
		case payloads <- payload:
		case <-c.ctx.Done():
			return c.ctx.Err()
		}
	}
}
