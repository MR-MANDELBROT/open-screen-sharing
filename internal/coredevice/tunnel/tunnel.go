package tunnel

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"time"

	tun "github.com/sagernet/sing-tun"
	"github.com/sagernet/sing/common/buf"
)

// idevice sends CDTunnel JSON after this magic and a big-endian uint16
// length (idevice/src/tunnel.rs).
const magic = "CDTunnel"

// idevice requests MTU 16000; go-ios requests 1280
// (idevice/src/tunnel.rs; ios/tunnel/tunnel.go).
const requestedMTU = 16000

const ipv6HeaderLength = 40

type Parameters struct {
	ClientAddress netip.Addr
	MTU           int
	ServerAddress netip.Addr
	ServerRSDPort uint16
}

type handshakeResponse struct {
	ClientParameters struct {
		Address string `json:"address"`
		MTU     int    `json:"mtu"`
	} `json:"clientParameters"`
	ServerAddress string `json:"serverAddress"`
	ServerRSDPort uint16 `json:"serverRSDPort"`
}

func handshake(conn net.Conn, reader *bufio.Reader) (*Parameters, error) {
	request, err := json.Marshal(map[string]any{"type": "clientHandshakeRequest", "mtu": requestedMTU})
	if err != nil {
		return nil, err
	}
	frame := append([]byte(magic), 0, 0)
	binary.BigEndian.PutUint16(frame[len(magic):], uint16(len(request)))
	_, err = conn.Write(append(frame, request...))
	if err != nil {
		return nil, fmt.Errorf("send CDTunnel request: %w", err)
	}
	header := make([]byte, len(magic)+2)
	_, err = io.ReadFull(reader, header)
	if err != nil {
		return nil, fmt.Errorf("read CDTunnel response: %w", err)
	}
	if string(header[:len(magic)]) != magic {
		return nil, fmt.Errorf("bad CDTunnel magic %q", header[:len(magic)])
	}
	body := make([]byte, binary.BigEndian.Uint16(header[len(magic):]))
	_, err = io.ReadFull(reader, body)
	if err != nil {
		return nil, fmt.Errorf("read CDTunnel response: %w", err)
	}
	var response handshakeResponse
	err = json.Unmarshal(body, &response)
	if err != nil {
		return nil, fmt.Errorf("parse CDTunnel response %s: %w", body, err)
	}
	clientAddress, err := netip.ParseAddr(response.ClientParameters.Address)
	if err != nil {
		return nil, fmt.Errorf("CDTunnel client address: %w", err)
	}
	serverAddress, err := netip.ParseAddr(response.ServerAddress)
	if err != nil {
		return nil, fmt.Errorf("CDTunnel server address: %w", err)
	}
	parameters := &Parameters{
		ClientAddress: clientAddress,
		MTU:           response.ClientParameters.MTU,
		ServerAddress: serverAddress,
		ServerRSDPort: response.ServerRSDPort,
	}
	if parameters.MTU <= ipv6HeaderLength || parameters.MTU > 65535 {
		parameters.MTU = 1280
	}
	return parameters, nil
}

const batchSize = 64

// sing-tun MemoryTun.WritePackets skips packets larger than 65535 bytes
// without incrementing its accepted count (tun_memory.go).
const maxPacket = 65535

const maxWrite = 256 << 10

const retryDelay = time.Millisecond

type Tunnel struct {
	conn          net.Conn
	reader        *bufio.Reader
	parameters    *Parameters
	clientAddress [16]byte
	logger        *slog.Logger
	device        *tun.MemoryTun
	stack         *tun.Go
	udpAccess     sync.RWMutex
	udpConns      map[uint16]*UDPConn
	// Only writePackets uses it; sing-tun calls that outbound callback serially
	// from one goroutine (tun_memory_queue.go, OutboundQueue.loopHandle).
	writeBuffer []byte
	done        chan struct{}
	closeOnce   sync.Once
	err         error
}

func Open(ctx context.Context, conn net.Conn, logger *slog.Logger) (*Tunnel, error) {
	reader := bufio.NewReaderSize(conn, 2*(ipv6HeaderLength+65535))
	stopAfter := context.AfterFunc(ctx, func() {
		conn.SetDeadline(time.Unix(1, 0))
	})
	parameters, err := handshake(conn, reader)
	if !stopAfter() || ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		conn.Close()
		return nil, err
	}
	conn.SetDeadline(time.Time{})
	logger.Debug("tunnel parameters",
		slog.String("client", parameters.ClientAddress.String()),
		slog.String("server", parameters.ServerAddress.String()),
		slog.Int("rsd_port", int(parameters.ServerRSDPort)),
		slog.Int("mtu", parameters.MTU))
	return start(conn, reader, parameters, logger)
}

func start(conn net.Conn, reader *bufio.Reader, parameters *Parameters, logger *slog.Logger) (*Tunnel, error) {
	t := &Tunnel{
		conn:          conn,
		reader:        reader,
		parameters:    parameters,
		clientAddress: parameters.ClientAddress.As16(),
		logger:        logger,
		udpConns:      make(map[uint16]*UDPConn),
		done:          make(chan struct{}),
	}
	t.device = tun.NewMemoryTun(tun.MemoryTunOptions{
		MTU:       parameters.MTU,
		BatchSize: batchSize,
		Outbound:  t.writePackets,
	})
	var err error
	t.stack, err = tun.NewGo(tun.StackOptions{
		Context:    context.Background(),
		Tun:        t.device,
		TunOptions: tun.Options{MTU: uint32(parameters.MTU)},
		Logger:     singLogger{logger},
	})
	if err == nil {
		err = t.stack.Start()
	}
	if err != nil {
		t.device.Close()
		conn.Close()
		return nil, fmt.Errorf("start network stack: %w", err)
	}
	go t.readLoop()
	return t, nil
}

func (t *Tunnel) Parameters() *Parameters {
	return t.parameters
}

// sing-tun waits for its outbound callback; a full data queue returns
// errGoTransmitBlocked (tun_memory_queue.go).
func (t *Tunnel) writePackets(packets []*buf.Buffer) {
	defer buf.ReleaseMulti(packets)
	data := t.writeBuffer[:0]
	for _, packet := range packets {
		if len(data) > 0 && len(data)+packet.Len() > maxWrite {
			if !t.write(data) {
				return
			}
			data = data[:0]
		}
		data = append(data, packet.Bytes()...)
	}
	if len(data) > 0 {
		t.write(data)
	}
	if cap(data) <= 2*maxWrite {
		t.writeBuffer = data[:0]
	}
}

func (t *Tunnel) write(data []byte) bool {
	_, err := t.conn.Write(data)
	if err != nil {
		t.fail(fmt.Errorf("write to tunnel: %w", err))
		return false
	}
	return true
}

func (t *Tunnel) readLoop() {
	packets := make([][]byte, 0, batchSize)
	lengths := make([]int, 0, batchSize)
	retry := time.NewTimer(retryDelay)
	retry.Stop()
	var tooLong uint64
	for {
		length, err := t.peekPacket(0, true)
		if err != nil {
			t.fail(err)
			return
		}
		total := length
		lengths = append(lengths[:0], length)
		for len(lengths) < batchSize {
			length, err = t.peekPacket(total, false)
			if err != nil || length == 0 {
				break
			}
			lengths = append(lengths, length)
			total += length
		}
		data, _ := t.reader.Peek(total)
		arrived := time.Now()
		packets = packets[:0]
		offset := 0
		for _, length := range lengths {
			packet := data[offset : offset+length]
			offset += length
			if t.deliverDirect(packet, arrived) {
				continue
			}
			if length > maxPacket {
				tooLong++
				t.logger.Debug("dropped a packet too long for the network stack",
					slog.Int("length", length), slog.Uint64("total", tooLong))
				continue
			}
			packets = append(packets, packet)
		}
		err = t.deliver(packets, retry)
		if err != nil {
			t.fail(err)
			return
		}
		t.reader.Discard(total)
	}
}

func (t *Tunnel) peekPacket(offset int, wait bool) (int, error) {
	if !wait && t.reader.Buffered() < offset+ipv6HeaderLength {
		return 0, nil
	}
	data, err := t.reader.Peek(offset + ipv6HeaderLength)
	if err != nil {
		return 0, fmt.Errorf("read from tunnel: %w", err)
	}
	header := data[offset:]
	if header[0]>>4 != 6 {
		return 0, fmt.Errorf("read from tunnel: not an IPv6 packet (version %d)", header[0]>>4)
	}
	length := ipv6HeaderLength + int(binary.BigEndian.Uint16(header[4:6]))
	if !wait && t.reader.Buffered() < offset+length {
		return 0, nil
	}
	_, err = t.reader.Peek(offset + length)
	if err != nil {
		return 0, fmt.Errorf("read from tunnel: %w", err)
	}
	return length, nil
}

// sing-tun MemoryTun.WritePackets copies accepted packets and reports
// their count; a full queue accepts none (tun_memory.go).
func (t *Tunnel) deliver(packets [][]byte, retry *time.Timer) error {
	for len(packets) > 0 {
		accepted, err := t.device.WritePackets(packets)
		if err != nil {
			return err
		}
		packets = packets[accepted:]
		if len(packets) == 0 {
			return nil
		}
		retry.Reset(retryDelay)
		select {
		case <-t.done:
			retry.Stop()
			return net.ErrClosed
		case <-retry.C:
		}
	}
	return nil
}

func (t *Tunnel) fail(err error) {
	t.closeOnce.Do(func() {
		t.logger.Debug("tunnel closed", slog.Any("error", err))
		t.err = err
		close(t.done)
		t.closeUDP(err)
		t.conn.Close()
		t.stack.Close()
		t.device.Close()
	})
}

func (t *Tunnel) DialTCP(ctx context.Context, port uint16) (net.Conn, error) {
	conn, err := t.stack.DialTCP(ctx, t.parameters.ClientAddress, netip.AddrPortFrom(t.parameters.ServerAddress, port))
	if err != nil {
		return nil, err
	}
	return conn, nil
}

func (t *Tunnel) Done() <-chan struct{} {
	return t.done
}

func (t *Tunnel) Err() error {
	select {
	case <-t.done:
		return t.err
	default:
		return nil
	}
}

func (t *Tunnel) Close() error {
	t.fail(net.ErrClosed)
	return nil
}

type singLogger struct {
	logger *slog.Logger
}

func (l singLogger) Trace(args ...any) {}
func (l singLogger) Debug(args ...any) { l.logger.Debug(fmt.Sprint(args...)) }
func (l singLogger) Info(args ...any)  { l.logger.Debug(fmt.Sprint(args...)) }
func (l singLogger) Warn(args ...any)  { l.logger.Warn(fmt.Sprint(args...)) }
func (l singLogger) Error(args ...any) { l.logger.Error(fmt.Sprint(args...)) }
func (l singLogger) Fatal(args ...any) { l.logger.Error(fmt.Sprint(args...)) }
func (l singLogger) Panic(args ...any) { l.logger.Error(fmt.Sprint(args...)) }
