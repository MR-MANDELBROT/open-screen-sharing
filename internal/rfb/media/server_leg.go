package media

import (
	"encoding/binary"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"time"
)

const (
	pendingLimit      = 8
	pendingLifetime   = 5 * time.Second
	sentReportSlots   = 8
	maxServerOutputs  = 256
	legReadRetryDelay = 50 * time.Millisecond
)

type serverActive struct {
	ssrc         uint32
	receiverSSRC uint32
	generation   uint64
}

type serverOutput struct {
	srtp          srtpStream
	generation    uint64
	offset        uint16
	started       bool
	highest       uint64
	based         bool
	base          uint64
	deviceBase    uint16
	lastSequence  uint16
	lastTimestamp uint32
	lastSentAt    time.Time
	packets       uint32
	octets        uint32
}

type pendingDatagram struct {
	data []byte
	from netip.AddrPort
	at   time.Time
}

// ScreensharingAgent binds its local UDP socket to the server port number
// before connecting it (macOS 26A434 and 26B5101f).
type serverLeg struct {
	name      string
	conn      *net.UDPConn
	port      uint16
	clockRate float64
	logger    *slog.Logger

	access        sync.Mutex
	initialTarget netip.AddrPort
	target        netip.AddrPort
	latched       bool
	pending       []pendingDatagram
	clientKey     [masterKeyLength]byte
	serverKey     [masterKeyLength]byte
	receiver      *srtcpReceiver
	sender        *srtpSender
	reports       *srtcpSender
	outputs       map[uint32]*serverOutput
	active        *serverActive
	held          uint64
	lastHeard     time.Time
	sentReports   [sentReportSlots]sentReport
	sentNext      int
	deviceReports map[uint32]*deviceReport
	clientRTT     time.Duration
	timing        serverTiming

	packets       uint64
	octets        uint64
	clientReports uint64
	authFailures  uint64
	sendErrors    uint64
}

func newServerLeg(name string, conn *net.UDPConn, clockRate float64, initial netip.Addr, logger *slog.Logger) *serverLeg {
	port := uint16(conn.LocalAddr().(*net.UDPAddr).Port)
	leg := &serverLeg{
		name:          name,
		conn:          conn,
		port:          port,
		clockRate:     clockRate,
		logger:        logger,
		outputs:       make(map[uint32]*serverOutput),
		deviceReports: make(map[uint32]*deviceReport),
	}
	if initial.IsValid() {
		leg.initialTarget = netip.AddrPortFrom(initial, port)
		leg.target = leg.initialTarget
	}
	return leg
}

func (l *serverLeg) setKeys(stream *serverOfferStream, now time.Time) error {
	l.access.Lock()
	defer l.access.Unlock()
	if l.receiver != nil && l.clientKey == stream.clientKey && l.serverKey == stream.serverKey {
		l.checkPendingLocked(now)
		return nil
	}
	receiver, err := newSRTCPReceiver(stream.clientKey[:])
	if err != nil {
		return err
	}
	sender, err := newSRTPSender(stream.serverKey[:])
	if err != nil {
		return err
	}
	reports, err := newSRTCPSender(stream.serverKey[:])
	if err != nil {
		return err
	}
	l.clientKey, l.serverKey = stream.clientKey, stream.serverKey
	l.receiver, l.sender, l.reports = receiver, sender, reports
	clear(l.outputs)
	l.sentReports = [sentReportSlots]sentReport{}
	l.clientRTT = 0
	l.target, l.latched = l.initialTarget, false
	l.lastHeard = time.Time{}
	l.checkPendingLocked(now)
	return nil
}

func (l *serverLeg) checkPendingLocked(now time.Time) {
	pending := l.pending
	l.pending = nil
	for _, datagram := range pending {
		if now.Sub(datagram.at) > pendingLifetime {
			continue
		}
		_, err := l.receiver.unprotect(datagram.data)
		if err == nil {
			l.latchLocked(datagram.from, datagram.at)
		}
	}
}

func (l *serverLeg) keepPendingLocked(datagram []byte, from netip.AddrPort, now time.Time) {
	if len(l.pending) >= pendingLimit {
		l.pending = append(l.pending[:0], l.pending[1:]...)
	}
	l.pending = append(l.pending, pendingDatagram{data: append([]byte(nil), datagram...), from: from, at: now})
}

func (l *serverLeg) latchLocked(from netip.AddrPort, now time.Time) bool {
	from = netip.AddrPortFrom(from.Addr().Unmap(), from.Port())
	l.lastHeard = now
	if l.latched && l.target == from {
		return false
	}
	previous := l.target
	l.target, l.latched = from, true
	l.logger.Debug("media client address", "leg", l.name, "address", from, "previous", previous)
	held := l.held
	l.held = 0
	return held > 0
}

type serverLegSnapshot struct {
	packets       uint64
	octets        uint64
	held          uint64
	clientReports uint64
	authFailures  uint64
	sendErrors    uint64
	target        netip.AddrPort
}

func (l *serverLeg) snapshot() serverLegSnapshot {
	l.access.Lock()
	defer l.access.Unlock()
	return serverLegSnapshot{
		packets:       l.packets,
		octets:        l.octets,
		held:          l.held,
		clientReports: l.clientReports,
		authFailures:  l.authFailures,
		sendErrors:    l.sendErrors,
		target:        l.target,
	}
}

func (l *serverLeg) lastHeardAt() time.Time {
	l.access.Lock()
	defer l.access.Unlock()
	return l.lastHeard
}

func (l *serverLeg) currentTarget() netip.AddrPort {
	l.access.Lock()
	defer l.access.Unlock()
	return l.target
}

func (l *serverLeg) activate(active *serverActive) {
	l.access.Lock()
	l.active = active
	l.held = 0
	l.startTimingLocked(time.Now())
	l.access.Unlock()
}

func (l *serverLeg) deactivate(generation uint64) {
	l.access.Lock()
	if l.active != nil && l.active.generation == generation {
		l.active = nil
	}
	l.access.Unlock()
}

func (l *serverLeg) outputLocked(ssrc uint32, generation uint64, sequence uint16) *serverOutput {
	output := l.outputs[ssrc]
	if output == nil {
		if len(l.outputs) >= maxServerOutputs {
			return nil
		}
		output = &serverOutput{generation: generation}
		l.outputs[ssrc] = output
		return output
	}
	if output.generation != generation {
		output.generation = generation
		output.based = false
		if output.started {
			output.offset = output.lastSequence + 1 - sequence
		}
	}
	return output
}

func (l *serverLeg) protectLocked(packet []byte, header rtpPacket, generation uint64, now time.Time) ([]byte, netip.AddrPort, bool) {
	if l.active == nil || l.active.generation != generation {
		return nil, netip.AddrPort{}, false
	}
	if !l.target.IsValid() {
		l.held++
		return nil, netip.AddrPort{}, false
	}
	output := l.outputLocked(header.ssrc, generation, header.sequence)
	if output == nil {
		return nil, netip.AddrPort{}, false
	}
	sequence := header.sequence + output.offset
	binary.BigEndian.PutUint16(packet[2:], sequence)
	index := output.srtp.estimateIndex(sequence)
	if !output.srtp.replay.check(index) {
		return nil, netip.AddrPort{}, false
	}
	output.srtp.replay.accept(index)
	if !output.based {
		output.based = true
		output.base = index
		output.deviceBase = header.sequence
	}
	if !output.started || index > output.highest {
		output.highest = index
	}
	output.started = true
	output.lastSequence = sequence
	output.lastTimestamp = header.timestamp
	output.lastSentAt = now
	output.packets++
	output.octets += uint32(len(packet) - header.payloadOffset)
	l.packets++
	l.octets += uint64(len(packet))
	return l.sender.protect(packet, header.payloadOffset, index), l.target, true
}

func (l *serverLeg) send(datagram []byte, target netip.AddrPort) {
	_, err := l.conn.WriteToUDPAddrPort(datagram, target)
	if err == nil {
		return
	}
	if errors.Is(err, net.ErrClosed) {
		return
	}
	l.access.Lock()
	l.sendErrors++
	l.timing.interval.sendErrors++
	l.timing.interval.lastSendError = err
	l.access.Unlock()
}

func (l *serverLeg) receive(handle func(leg *serverLeg, datagram []byte, from netip.AddrPort, now time.Time)) {
	buffer := make([]byte, maxDatagram)
	for {
		n, from, err := l.conn.ReadFromUDPAddrPort(buffer)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			if isConnectionRefused(err) {
				continue
			}
			l.logger.Debug("media leg read", "leg", l.name, "error", err)
			time.Sleep(legReadRetryDelay)
			continue
		}
		if !isRTCP(buffer[:n]) {
			continue
		}
		handle(l, buffer[:n], from, time.Now())
	}
}
