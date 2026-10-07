package tunnel

import (
	"bufio"
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"testing"
	"time"
)

var (
	testClient = netip.MustParseAddr("fd00::2")
	testDevice = netip.MustParseAddr("fd00::1")
)

func udpPacket(source, destination netip.AddrPort, payload []byte) []byte {
	length := 8 + len(payload)
	packet := make([]byte, ipv6HeaderLength+length)
	packet[0] = 0x60
	binary.BigEndian.PutUint16(packet[4:], uint16(length))
	packet[6] = 17
	packet[7] = 64
	sourceAddress, destinationAddress := source.Addr().As16(), destination.Addr().As16()
	copy(packet[8:], sourceAddress[:])
	copy(packet[24:], destinationAddress[:])
	udp := packet[ipv6HeaderLength:]
	binary.BigEndian.PutUint16(udp[0:], source.Port())
	binary.BigEndian.PutUint16(udp[2:], destination.Port())
	binary.BigEndian.PutUint16(udp[4:], uint16(length))
	copy(udp[8:], payload)
	var sum uint32
	add := func(data []byte) {
		for i := 0; i+1 < len(data); i += 2 {
			sum += uint32(binary.BigEndian.Uint16(data[i:]))
		}
		if len(data)%2 == 1 {
			sum += uint32(data[len(data)-1]) << 8
		}
	}
	add(packet[8:40])
	sum += uint32(length) + 17
	add(udp)
	for sum > 0xffff {
		sum = sum&0xffff + sum>>16
	}
	checksum := ^uint16(sum)
	if checksum == 0 {
		checksum = 0xffff
	}
	binary.BigEndian.PutUint16(udp[6:], checksum)
	return packet
}

type feedConn struct {
	packets []byte
	size    int
	credits chan struct{}
	closed  chan struct{}
	pending []byte
}

func newFeedConn() *feedConn {
	return &feedConn{credits: make(chan struct{}, 1024), closed: make(chan struct{})}
}

func (c *feedConn) setPacket(packet []byte) {
	c.size = len(packet)
	for range 64 {
		c.packets = append(c.packets, packet...)
	}
}

func (c *feedConn) Read(p []byte) (int, error) {
	if len(c.pending) == 0 {
		select {
		case <-c.credits:
		case <-c.closed:
			return 0, io.EOF
		}
		count := 1
	more:
		for count < 64 && (count+1)*c.size <= len(p) {
			select {
			case <-c.credits:
				count++
			default:
				break more
			}
		}
		c.pending = c.packets[:count*c.size]
	}
	n := copy(p, c.pending)
	c.pending = c.pending[n:]
	return n, nil
}

func (c *feedConn) Write(p []byte) (int, error)        { return len(p), nil }
func (c *feedConn) Close() error                       { close(c.closed); return nil }
func (c *feedConn) LocalAddr() net.Addr                { return nil }
func (c *feedConn) RemoteAddr() net.Addr               { return nil }
func (c *feedConn) SetDeadline(t time.Time) error      { return nil }
func (c *feedConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *feedConn) SetWriteDeadline(t time.Time) error { return nil }

func testTunnel(tb testing.TB, conn net.Conn) *Tunnel {
	tb.Helper()
	logger := slog.New(slog.DiscardHandler)
	reader := bufio.NewReaderSize(conn, 2*(ipv6HeaderLength+65535))
	tun, err := start(conn, reader, &Parameters{ClientAddress: testClient, MTU: 16000, ServerAddress: testDevice}, logger)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { tun.Close() })
	return tun
}

func BenchmarkReceiveVideo(b *testing.B) {
	benchmarkReceive(b, 64)
}

func BenchmarkReceiveOne(b *testing.B) {
	benchmarkReceive(b, 1)
}

func benchmarkReceive(b *testing.B, inFlight int) {
	feed := newFeedConn()
	tun := testTunnel(b, feed)
	conn, err := tun.ListenUDP(0)
	if err != nil {
		b.Fatal(err)
	}
	defer conn.Close()
	feed.setPacket(udpPacket(netip.AddrPortFrom(testDevice, 50000), netip.AddrPortFrom(testClient, conn.Port()), make([]byte, 1400)))
	ctx := context.Background()
	buffer := make([]byte, 65536)
	for range inFlight {
		feed.credits <- struct{}{}
	}
	b.SetBytes(1400)
	b.ReportAllocs()
	for b.Loop() {
		n, _, _, err := conn.ReadFrom(ctx, buffer)
		if err != nil || n != 1400 {
			b.Fatal(n, err)
		}
		feed.credits <- struct{}{}
	}
}
