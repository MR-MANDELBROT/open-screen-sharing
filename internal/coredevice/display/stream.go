package display

import (
	"context"
	"encoding/binary"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nekohasekai/open-screen-sharing/internal/coredevice/tunnel"
	"github.com/nekohasekai/open-screen-sharing/internal/coredevice/xpc"
	E "github.com/sagernet/sing/common/exceptions"
)

const (
	maxDatagram      = 65536
	keyFrameInterval = time.Second
)

type Stream struct {
	client   *Client
	info     StreamInfo
	conn     *tunnel.UDPConn
	control  *xpc.Conn
	deviceIP netip.Addr

	first   []byte
	arrival time.Time

	keyFrameAccess sync.Mutex
	lastKeyFrame   time.Time
	keyFrameTimer  *time.Timer
	firSequence    uint8

	lastRTCP atomic.Int64

	done chan struct{}
}

func newStream(client *Client, info StreamInfo, conn *tunnel.UDPConn, control *xpc.Conn) *Stream {
	stream := &Stream{
		client:   client,
		info:     info,
		conn:     conn,
		control:  control,
		deviceIP: info.DeviceAddress.Addr().Unmap(),
		done:     make(chan struct{}),
	}
	stream.lastRTCP.Store(time.Now().UnixNano())
	go stream.watchControl()
	return stream
}

func (s *Stream) Info() StreamInfo {
	return s.info
}

func (s *Stream) watchControl() {
	for {
		message, err := s.control.Receive()
		if err != nil {
			select {
			case <-s.done:
			default:
				s.client.logger.Debug("display stream control connection ended", "ssrc", s.info.SSRC, "error", err)
			}
			return
		}
		s.client.logger.Debug("display stream control message", "ssrc", s.info.SSRC, "message", message)
	}
}

func (s *Stream) ReadPacket(ctx context.Context, buffer []byte) (n int, rtcp bool, err error) {
	if s.first != nil {
		n = copy(buffer, s.first)
		s.first = nil
		s.arrival = time.Now()
		return n, isRTCP(buffer[:n]), nil
	}
	for {
		n, from, arrived, err := s.conn.ReadFrom(ctx, buffer)
		if err != nil {
			return 0, false, err
		}
		if from.Addr() == s.deviceIP {
			s.arrival = arrived
			return n, isRTCP(buffer[:n]), nil
		}
	}
}

func (s *Stream) Arrival() time.Time {
	return s.arrival
}

func (s *Stream) waitFirstPacket(ctx context.Context) error {
	waitContext, cancel := context.WithTimeout(ctx, firstPacketTimeout)
	defer cancel()
	buffer := make([]byte, maxDatagram)
	n, _, err := s.ReadPacket(waitContext, buffer)
	if err != nil {
		if ctx.Err() == nil && waitContext.Err() != nil {
			return ErrNoMedia
		}
		return err
	}
	s.first = buffer[:n]
	return nil
}

func (s *Stream) WriteRTCP(packet []byte) error {
	_, err := s.conn.WriteTo(packet, s.info.DeviceAddress)
	if err != nil {
		return E.Cause(err, "write rtcp")
	}
	if len(packet) >= 2 && (packet[1] == 200 || packet[1] == 201 || packet[1] == 204) {
		s.lastRTCP.Store(time.Now().UnixNano())
	}
	return nil
}

func (s *Stream) RequestKeyFrame() error {
	s.keyFrameAccess.Lock()
	defer s.keyFrameAccess.Unlock()
	select {
	case <-s.done:
		return net.ErrClosed
	default:
	}
	if s.keyFrameTimer != nil {
		return nil
	}
	if wait := keyFrameInterval - time.Since(s.lastKeyFrame); wait > 0 {
		s.keyFrameTimer = time.AfterFunc(wait, s.sendDelayedKeyFrame)
		return nil
	}
	return s.sendKeyFrameLocked()
}

func (s *Stream) sendDelayedKeyFrame() {
	s.keyFrameAccess.Lock()
	defer s.keyFrameAccess.Unlock()
	s.keyFrameTimer = nil
	select {
	case <-s.done:
		return
	default:
	}
	err := s.sendKeyFrameLocked()
	if err != nil {
		s.client.logger.Debug("display stream keyframe request", "ssrc", s.info.SSRC, "error", err)
	}
}

func (s *Stream) sendKeyFrameLocked() error {
	s.lastKeyFrame = time.Now()
	s.firSequence++
	return s.WriteRTCP(keyFrameRequest(s.info.SSRC, s.info.DeviceSSRC, s.firSequence))
}

// pymobiledevice3 uses this empty-CNAME SDES in _build_rtcp_sdes
// (pymobiledevice3 1146717,
// pymobiledevice3/remote/core_device/screen_stream.py:1305-1322).
func keepaliveReport(ssrc uint32) []byte {
	report := binary.BigEndian.AppendUint32([]byte{0x80, 201, 0, 1}, ssrc)
	report = append(report, 0x81, 202, 0, 2)
	report = binary.BigEndian.AppendUint32(report, ssrc)
	return append(report, 1, 0, 0, 0)
}

func (s *Stream) startKeepalive() {
	timeout := rtcpTimeout
	if s.info.RTCPTimeout > 0 {
		timeout = min(timeout, s.info.RTCPTimeout)
	}
	interval := max(timeout/6, 500*time.Millisecond)
	report := keepaliveReport(s.info.SSRC)
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-s.done:
				return
			case now := <-ticker.C:
				if now.Sub(time.Unix(0, s.lastRTCP.Load())) < interval {
					continue
				}
				err := s.WriteRTCP(report)
				if err != nil {
					s.client.logger.Debug("display stream keepalive", "ssrc", s.info.SSRC, "error", err)
				}
			}
		}
	}()
}

func (s *Stream) Close() error {
	close(s.done)
	s.conn.CloseRead()
	ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
	defer cancel()
	stopped, err := s.client.stop(ctx, s.info.SSRC)
	_ = s.control.Close()
	_ = s.conn.Close()
	if err != nil {
		return E.Cause(err, "stop ", string(s.info.Type), " stream")
	}
	s.client.logger.Debug("display stream stopped", "ssrc", s.info.SSRC, "stopped", stopped)
	return nil
}
