package media

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func testRTPPacket(sequence uint16, payload []byte) []byte {
	packet := make([]byte, 12, 12+len(payload)+authTagLength)
	packet[0] = 0x80
	packet[1] = 96
	binary.BigEndian.PutUint16(packet[2:], sequence)
	binary.BigEndian.PutUint32(packet[4:], 90000)
	binary.BigEndian.PutUint32(packet[8:], 0x1234)
	return append(packet, payload...)
}

func TestSRTPRoundTrip(t *testing.T) {
	master := bytes.Repeat([]byte{7}, 46)
	sender, err := newSRTPSender(master)
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := newSRTPReceiver(master)
	if err != nil {
		t.Fatal(err)
	}
	for _, index := range []uint64{1, 2, 0xffff, 0x10000, 0x10001} {
		payload := bytes.Repeat([]byte{byte(index)}, 1000)
		datagram := sender.protect(testRTPPacket(uint16(index), payload), 12, index)
		if bytes.Equal(datagram[12:12+len(payload)], payload) {
			t.Fatalf("index %d: payload sent in the clear", index)
		}
		packet, err := receiver.unprotect(datagram)
		if err != nil {
			t.Fatalf("index %d: %v", index, err)
		}
		if packet.index != index || !bytes.Equal(packet.payload, payload) {
			t.Fatalf("index %d: got index %d and %d bytes", index, packet.index, len(packet.payload))
		}
	}
	datagram := sender.protect(testRTPPacket(3, []byte("abc")), 12, 0x10003)
	datagram[len(datagram)-1] ^= 1
	_, err = receiver.unprotect(datagram)
	if err != errAuthentication {
		t.Fatalf("changed tag: %v", err)
	}
}

func TestSRTCPRoundTrip(t *testing.T) {
	master := bytes.Repeat([]byte{9}, 46)
	sender, err := newSRTCPSender(master)
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := newSRTCPReceiver(master)
	if err != nil {
		t.Fatal(err)
	}
	report := []byte{0x81, 201, 0, 7, 0, 0, 0x12, 0x34}
	report = append(report, bytes.Repeat([]byte{5}, 24)...)
	for range 3 {
		packet, err := receiver.unprotect(sender.protect(report))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(packet, report) {
			t.Fatalf("got % x, want % x", packet, report)
		}
	}
}

func TestSRTPAllocations(t *testing.T) {
	master := bytes.Repeat([]byte{7}, 46)
	sender, _ := newSRTPSender(master)
	receiver, _ := newSRTPReceiver(master)
	packet := testRTPPacket(0, make([]byte, 1400))
	buffer := make([]byte, len(packet)+authTagLength)
	var index uint64
	allocations := testing.AllocsPerRun(100, func() {
		index++
		copy(buffer, packet)
		binary.BigEndian.PutUint16(buffer[2:], uint16(index))
		datagram := sender.protect(buffer[:len(packet)], 12, index)
		_, err := receiver.unprotect(datagram)
		if err != nil {
			t.Fatal(err)
		}
	})
	if allocations > 2 {
		t.Fatalf("%v allocations for a packet sent and received, want 2", allocations)
	}
}
