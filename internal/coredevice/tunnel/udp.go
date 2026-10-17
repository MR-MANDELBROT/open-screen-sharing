package tunnel

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"time"

	tun "github.com/sagernet/sing-tun"
	E "github.com/sagernet/sing/common/exceptions"
)

const (
	protocolUDP     = 17
	udpHeaderLength = 8
)

// sing-tun drops datagrams for a UDP socket once 212992 bytes are queued;
// on iOS, SetReadBuffer can raise that to at most 256 KiB
// (sing-tun v0.9.7-0.20261002083955-3f8acd9da65b, stack_go_udp_socket.go:26,
// 91; stack_go_udp_socket.go:172-174,
// 445; stack_go_profile_ios.go:7).
const udpQueueSize = 8 << 20

type UDPConn struct {
	tunnel *Tunnel
	conn   *tun.GoUDPConn
	port   uint16
	queue  *datagramQueue
}

func (t *Tunnel) ListenUDP(port uint16) (*UDPConn, error) {
	conn, err := t.stack.ListenUDP(netip.AddrPortFrom(t.parameters.ClientAddress, port))
	if err != nil {
		return nil, err
	}
	c := &UDPConn{
		tunnel: t,
		conn:   conn,
		port:   uint16(conn.LocalAddr().(*net.UDPAddr).Port),
		queue:  newDatagramQueue(udpQueueSize),
	}
	t.udpAccess.Lock()
	select {
	case <-t.done:
		t.udpAccess.Unlock()
		conn.Close()
		return nil, E.Cause(t.err, "tunnel closed")
	default:
	}
	t.udpConns[c.port] = c
	t.udpAccess.Unlock()
	go c.receiveFromStack()
	return c, nil
}

func (c *UDPConn) receiveFromStack() {
	buffer := make([]byte, 65536)
	for {
		n, source, err := c.conn.ReadFromUDPAddrPort(buffer)
		if err != nil {
			return
		}
		c.queue.push(buffer[:n], source, time.Now())
	}
}

func (c *UDPConn) Port() uint16 {
	return c.port
}

func (c *UDPConn) ReadFrom(ctx context.Context, buffer []byte) (n int, source netip.AddrPort, arrived time.Time, err error) {
	for {
		n, source, arrived, err = c.queue.pop(buffer)
		if !errors.Is(err, errQueueEmpty) {
			return n, source, arrived, err
		}
		select {
		case <-c.queue.ready:
		case <-c.queue.closed:
		case <-ctx.Done():
			return 0, netip.AddrPort{}, time.Time{}, ctx.Err()
		}
	}
}

func (c *UDPConn) WriteTo(buffer []byte, address netip.AddrPort) (int, error) {
	return c.conn.WriteToUDPAddrPort(buffer, address)
}

func (c *UDPConn) CloseRead() {
	c.queue.close(net.ErrClosed)
}

func (c *UDPConn) Close() error {
	t := c.tunnel
	t.udpAccess.Lock()
	delete(t.udpConns, c.port)
	t.udpAccess.Unlock()
	c.queue.close(net.ErrClosed)
	err := c.conn.Close()
	if dropped := c.queue.droppedCount(); dropped > 0 {
		t.logger.Debug("dropped datagrams that found the queue full",
			slog.Int("port", int(c.port)), slog.Uint64("count", dropped))
	}
	return err
}

func (t *Tunnel) deliverDirect(packet []byte, arrived time.Time) bool {
	if len(packet) < ipv6HeaderLength+udpHeaderLength || packet[6] != protocolUDP ||
		!bytes.Equal(packet[24:40], t.clientAddress[:]) {
		return false
	}
	udp := packet[ipv6HeaderLength:]
	t.udpAccess.RLock()
	c := t.udpConns[binary.BigEndian.Uint16(udp[2:])]
	t.udpAccess.RUnlock()
	if c == nil {
		return false
	}
	length := int(binary.BigEndian.Uint16(udp[4:]))
	if length < udpHeaderLength || length > len(udp) {
		return false
	}
	source := netip.AddrPortFrom(netip.AddrFrom16([16]byte(packet[8:24])).Unmap(), binary.BigEndian.Uint16(udp[0:]))
	c.queue.push(udp[udpHeaderLength:length], source, arrived)
	return true
}

func (t *Tunnel) closeUDP(err error) {
	t.udpAccess.Lock()
	defer t.udpAccess.Unlock()
	for _, c := range t.udpConns {
		c.queue.close(E.Cause(err, "tunnel closed"))
	}
}

var errQueueEmpty = E.New("queue empty")

const datagramHeaderLength = 2 + 16 + 2 + 8

type datagramQueue struct {
	access  sync.Mutex
	buffer  []byte
	head    int
	used    int
	epoch   time.Time
	dropped uint64
	err     error
	ready   chan struct{}
	closed  chan struct{}
}

func newDatagramQueue(size int) *datagramQueue {
	return &datagramQueue{
		buffer: make([]byte, size),
		epoch:  time.Now(),
		ready:  make(chan struct{}, 1),
		closed: make(chan struct{}),
	}
}

func (q *datagramQueue) push(datagram []byte, source netip.AddrPort, arrived time.Time) {
	size := datagramHeaderLength + len(datagram)
	q.access.Lock()
	if q.err != nil {
		q.access.Unlock()
		return
	}
	if q.used+size > len(q.buffer) {
		q.dropped++
		q.access.Unlock()
		return
	}
	var header [datagramHeaderLength]byte
	binary.BigEndian.PutUint16(header[0:], uint16(len(datagram)))
	address := source.Addr().As16()
	copy(header[2:], address[:])
	binary.BigEndian.PutUint16(header[18:], source.Port())
	binary.BigEndian.PutUint64(header[20:], uint64(arrived.Sub(q.epoch)))
	at := q.put((q.head+q.used)%len(q.buffer), header[:])
	q.put(at, datagram)
	wasEmpty := q.used == 0
	q.used += size
	q.access.Unlock()
	if wasEmpty {
		select {
		case q.ready <- struct{}{}:
		default:
		}
	}
}

func (q *datagramQueue) pop(buffer []byte) (int, netip.AddrPort, time.Time, error) {
	q.access.Lock()
	defer q.access.Unlock()
	if q.err != nil {
		return 0, netip.AddrPort{}, time.Time{}, q.err
	}
	if q.used == 0 {
		return 0, netip.AddrPort{}, time.Time{}, errQueueEmpty
	}
	var header [datagramHeaderLength]byte
	at := q.get(q.head, header[:])
	length := int(binary.BigEndian.Uint16(header[0:]))
	n := min(length, len(buffer))
	q.get(at, buffer[:n])
	q.used -= datagramHeaderLength + length
	if q.used == 0 {
		q.head = 0
	} else {
		q.head = (at + length) % len(q.buffer)
	}
	source := netip.AddrPortFrom(netip.AddrFrom16([16]byte(header[2:18])).Unmap(), binary.BigEndian.Uint16(header[18:]))
	arrived := q.epoch.Add(time.Duration(binary.BigEndian.Uint64(header[20:])))
	return n, source, arrived, nil
}

func (q *datagramQueue) put(at int, data []byte) int {
	n := copy(q.buffer[at:], data)
	copy(q.buffer, data[n:])
	return (at + len(data)) % len(q.buffer)
}

func (q *datagramQueue) get(at int, data []byte) int {
	n := copy(data, q.buffer[at:])
	copy(data[n:], q.buffer)
	return (at + len(data)) % len(q.buffer)
}

func (q *datagramQueue) close(err error) {
	q.access.Lock()
	defer q.access.Unlock()
	if q.err != nil {
		return
	}
	q.err = err
	q.used = 0
	close(q.closed)
}

func (q *datagramQueue) droppedCount() uint64 {
	q.access.Lock()
	defer q.access.Unlock()
	return q.dropped
}
