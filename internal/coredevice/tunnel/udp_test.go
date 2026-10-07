package tunnel

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"
)

func pipeTunnel(t *testing.T) (*Tunnel, net.Conn) {
	t.Helper()
	tunnelSide, deviceSide := net.Pipe()
	t.Cleanup(func() { deviceSide.Close() })
	return testTunnel(t, tunnelSide), deviceSide
}

func readFrom(t *testing.T, conn *UDPConn, buffer []byte) (int, netip.AddrPort) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	n, source, _, err := conn.ReadFrom(ctx, buffer)
	if err != nil {
		t.Fatal(err)
	}
	return n, source
}

func TestUDPConnReceives(t *testing.T) {
	tun, device := pipeTunnel(t)
	conn, err := tun.ListenUDP(0)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	source := netip.AddrPortFrom(testDevice, 50000)
	destination := netip.AddrPortFrom(testClient, conn.Port())
	var stream []byte
	for i := range 100 {
		stream = append(stream, udpPacket(source, destination, bytes.Repeat([]byte{byte(i)}, 1000+i))...)
	}
	go device.Write(stream)
	buffer := make([]byte, 65536)
	for i := range 100 {
		n, from := readFrom(t, conn, buffer)
		if from != source {
			t.Fatalf("datagram %d from %v, want %v", i, from, source)
		}
		if !bytes.Equal(buffer[:n], bytes.Repeat([]byte{byte(i)}, 1000+i)) {
			t.Fatalf("datagram %d: got %d bytes", i, n)
		}
	}
}

func TestUDPConnReceivesFragments(t *testing.T) {
	tun, device := pipeTunnel(t)
	conn, err := tun.ListenUDP(0)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	payload := make([]byte, 3000)
	for i := range payload {
		payload[i] = byte(i)
	}
	whole := udpPacket(netip.AddrPortFrom(testDevice, 50000), netip.AddrPortFrom(testClient, conn.Port()), payload)
	header, udp := whole[:ipv6HeaderLength], whole[ipv6HeaderLength:]
	fragment := func(offset int, data []byte, more bool) []byte {
		packet := append([]byte(nil), header...)
		packet[6] = 44
		binary.BigEndian.PutUint16(packet[4:], uint16(8+len(data)))
		word := uint16(offset)
		if more {
			word |= 1
		}
		packet = append(packet, protocolUDP, 0, 0, 0, 0, 0, 0, 0)
		binary.BigEndian.PutUint16(packet[ipv6HeaderLength+2:], word)
		binary.BigEndian.PutUint32(packet[ipv6HeaderLength+4:], 0x1234)
		return append(packet, data...)
	}
	go device.Write(append(fragment(0, udp[:1504], true), fragment(1504, udp[1504:], false)...))
	buffer := make([]byte, 65536)
	n, _ := readFrom(t, conn, buffer)
	if !bytes.Equal(buffer[:n], payload) {
		t.Fatalf("got %d bytes, want the %d sent", n, len(payload))
	}
}

func TestUDPConnSends(t *testing.T) {
	tun, device := pipeTunnel(t)
	conn, err := tun.ListenUDP(0)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	destination := netip.AddrPortFrom(testDevice, 50000)
	_, err = conn.WriteTo([]byte("report"), destination)
	if err != nil {
		t.Fatal(err)
	}
	device.SetReadDeadline(time.Now().Add(5 * time.Second))
	header := make([]byte, ipv6HeaderLength)
	_, err = io.ReadFull(device, header)
	if err != nil {
		t.Fatal(err)
	}
	body := make([]byte, binary.BigEndian.Uint16(header[4:]))
	_, err = io.ReadFull(device, body)
	if err != nil {
		t.Fatal(err)
	}
	if header[6] != protocolUDP || binary.BigEndian.Uint16(body[0:]) != conn.Port() ||
		binary.BigEndian.Uint16(body[2:]) != destination.Port() || string(body[udpHeaderLength:]) != "report" {
		t.Fatalf("sent % x % x", header, body)
	}
}

func TestUDPConnClose(t *testing.T) {
	tun, _ := pipeTunnel(t)
	conn, err := tun.ListenUDP(0)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, _, _, err := conn.ReadFrom(context.Background(), make([]byte, 100))
		result <- err
	}()
	conn.CloseRead()
	if err := <-result; !errors.Is(err, net.ErrClosed) {
		t.Fatalf("ReadFrom after CloseRead: %v", err)
	}
	conn.Close()

	conn, err = tun.ListenUDP(0)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	go func() {
		_, _, _, err := conn.ReadFrom(context.Background(), make([]byte, 100))
		result <- err
	}()
	tun.Close()
	if err := <-result; err == nil || errors.Is(err, errQueueEmpty) {
		t.Fatalf("ReadFrom after the tunnel closed: %v", err)
	}
	_, err = tun.ListenUDP(0)
	if err == nil {
		t.Fatal("ListenUDP succeeded on a closed tunnel")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	queue := newDatagramQueue(100)
	other := &UDPConn{queue: queue}
	_, _, _, err = other.ReadFrom(ctx, make([]byte, 100))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ReadFrom with an ended context: %v", err)
	}
}

func TestDatagramQueue(t *testing.T) {
	queue := newDatagramQueue(2*(datagramHeaderLength+25) + 10)
	source := netip.MustParseAddrPort("[fd00::1]:5000")
	arrived := time.Now()
	buffer := make([]byte, 100)
	for i := range 3 {
		queue.push(bytes.Repeat([]byte{byte(i)}, 25), source, arrived.Add(time.Duration(i)))
	}
	if queue.droppedCount() != 1 {
		t.Fatalf("dropped %d, want 1", queue.droppedCount())
	}
	n, from, at, err := queue.pop(buffer)
	if err != nil || from != source || !at.Equal(arrived) || !bytes.Equal(buffer[:n], bytes.Repeat([]byte{0}, 25)) {
		t.Fatalf("pop: %d %v %v %v", n, from, at, err)
	}
	queue.push(bytes.Repeat([]byte{3}, 25), source, arrived.Add(3))
	for _, want := range []byte{1, 3} {
		n, from, at, err := queue.pop(buffer)
		if err != nil || from != source || !at.Equal(arrived.Add(time.Duration(want))) || !bytes.Equal(buffer[:n], bytes.Repeat([]byte{want}, 25)) {
			t.Fatalf("pop %d: % x %v %v %v", want, buffer[:n], from, at, err)
		}
	}
	_, _, _, err = queue.pop(buffer)
	if err != errQueueEmpty {
		t.Fatalf("pop of an empty queue: %v", err)
	}
	queue.push([]byte("abcdef"), source, arrived)
	queue.push([]byte("ghi"), source, arrived)
	n, _, _, _ = queue.pop(buffer[:2])
	if string(buffer[:n]) != "ab" {
		t.Fatalf("short pop: %q", buffer[:n])
	}
	n, _, _, _ = queue.pop(buffer)
	if string(buffer[:n]) != "ghi" {
		t.Fatalf("pop after a short one: %q", buffer[:n])
	}
	queue.push([]byte("x"), source, arrived)
	queue.close(net.ErrClosed)
	_, _, _, err = queue.pop(buffer)
	if !errors.Is(err, net.ErrClosed) {
		t.Fatalf("pop after close: %v", err)
	}
}
