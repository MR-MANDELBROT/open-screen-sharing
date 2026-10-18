package rdp

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"io"
	"net"
	"strconv"
	"sync/atomic"

	E "github.com/sagernet/sing/common/exceptions"
)

const (
	tpktVersion         = 3
	tpktHeaderLength    = 4
	x224DataLength      = 3
	x224LengthIndicator = 2
	x224Data            = 0xF0
	x224EndOfTransport  = 0x80
	x224Request         = 0xE0
	x224Confirm         = 0xD0
	maximumPacket       = 1 << 20

	fastPathActionMask     = 0x03
	fastPathEncryptionMask = 0xc0
	fastPathLengthLong     = 0x80
)

type transport struct {
	tcp     net.Conn
	conn    net.Conn
	reader  *bufio.Reader
	buffer  []byte
	written uint64
	read    atomic.Uint64
}

func newTransport(conn net.Conn) *transport {
	return &transport{tcp: conn, conn: conn, reader: bufio.NewReaderSize(conn, 64<<10), buffer: make([]byte, 0, 64<<10)}
}

func (t *transport) upgrade(config *tls.Config) (*x509.Certificate, error) {
	if t.reader.Buffered() > 0 {
		return nil, E.New("rdp: ", t.reader.Buffered(), " unexpected bytes before TLS handshake")
	}
	conn := tls.Client(t.conn, config)
	err := conn.Handshake()
	if err != nil {
		return nil, E.Cause(err, "rdp: perform TLS handshake")
	}
	t.conn = conn
	t.reader.Reset(conn)
	return conn.ConnectionState().PeerCertificates[0], nil
}

func (t *transport) write(packet []byte) error {
	n, err := t.conn.Write(packet)
	t.written += uint64(n)
	return err
}

func (t *transport) readPacket() (fastPath bool, header byte, payload []byte, err error) {
	header, err = t.reader.ReadByte()
	if err != nil {
		return
	}
	if header == tpktVersion {
		t.read.Add(tpktHeaderLength)
		var length [3]byte
		_, err = io.ReadFull(t.reader, length[:])
		if err != nil {
			return
		}
		size := int(binary.BigEndian.Uint16(length[1:]))
		if length[0] != 0 {
			err = E.New("rdp: TPKT reserved byte 0x", strconv.FormatUint(uint64(length[0]), 16))
			return
		}
		if size < tpktHeaderLength+x224DataLength {
			err = E.New("rdp: TPKT length ", size, " is shorter than the TPKT and X.224 headers")
			return
		}
		payload, err = t.fill(size - tpktHeaderLength)
		if err != nil {
			return
		}
		if payload[0] != x224LengthIndicator || payload[1] != x224Data || payload[2] != x224EndOfTransport {
			err = E.New("rdp: unexpected X.224 data header 0x", hex.EncodeToString(payload[:x224DataLength]))
			return
		}
		payload = payload[x224DataLength:]
		return
	}
	if header&fastPathActionMask != 0 {
		err = E.New("rdp: unexpected packet header 0x", strconv.FormatUint(uint64(header), 16))
		return
	}
	first, err := t.reader.ReadByte()
	if err != nil {
		return
	}
	size, used := int(first), 2
	if first&fastPathLengthLong != 0 {
		var second byte
		second, err = t.reader.ReadByte()
		if err != nil {
			return
		}
		size, used = int(first&^fastPathLengthLong)<<8|int(second), 3
	}
	if size < used {
		err = E.New("rdp: fast-path length ", size, " is shorter than its ", used, "-byte header")
		return
	}
	t.read.Add(uint64(used))
	payload, err = t.fill(size - used)
	fastPath = true
	return
}

func (t *transport) readX224() ([]byte, error) {
	var header [tpktHeaderLength]byte
	_, err := io.ReadFull(t.reader, header[:])
	if err != nil {
		return nil, err
	}
	t.read.Add(tpktHeaderLength)
	if string(header[:]) == "RFB " {
		return nil, ErrRFBServer
	}
	if header[0] != tpktVersion || header[1] != 0 {
		return nil, E.New("rdp: expected a TPKT header, got 0x", hex.EncodeToString(header[:]))
	}
	size := int(binary.BigEndian.Uint16(header[2:]))
	if size < tpktHeaderLength+x224DataLength {
		return nil, E.New("rdp: TPKT length ", size, " is shorter than the TPKT and X.224 headers")
	}
	return t.fill(size - tpktHeaderLength)
}

func (t *transport) fill(size int) ([]byte, error) {
	if size < 0 || size > maximumPacket {
		return nil, E.New("rdp: packet of ", size, " bytes exceeds the 1 MiB limit")
	}
	if cap(t.buffer) < size {
		t.buffer = make([]byte, size)
	}
	t.buffer = t.buffer[:size]
	n, err := io.ReadFull(t.reader, t.buffer)
	t.read.Add(uint64(n))
	if err != nil {
		return nil, err
	}
	return t.buffer, nil
}

func tpkt(payload []byte) []byte {
	packet := make([]byte, 0, tpktHeaderLength+x224DataLength+len(payload))
	packet = append(packet, tpktVersion, 0)
	packet = binary.BigEndian.AppendUint16(packet, uint16(tpktHeaderLength+x224DataLength+len(payload)))
	packet = append(packet, x224LengthIndicator, x224Data, x224EndOfTransport)
	return append(packet, payload...)
}
