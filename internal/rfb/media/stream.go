package media

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"sync"
	"time"
)

const (
	videoReceiveBuffer = 4 << 20
	audioReceiveBuffer = 256 << 10
	// AVConference startFeedbackSource sets a 50,000,000 ns timer interval:
	// macOS 27.0.1 (26A434), 0x1c1189bf0–0x1c1189c00;
	// macOS 27.2 (26B5101f), 0x1c0806298–0x1c08062a8.
	rateFeedbackInterval = 50 * time.Millisecond
	reportInterval       = time.Second
	keyframeInterval     = 250 * time.Millisecond
	keyframeAnswerLimit  = 2 * time.Second
	legSilenceLimit      = 48 * time.Second
	firstVideoLimit      = 8 * time.Second
	receiveTick          = 10 * time.Millisecond
	maxDatagram          = 65536

	drainQuiet   = 150 * time.Millisecond
	drainMinimum = 250 * time.Millisecond
	drainLimit   = time.Second
)

var (
	ErrStreamSilent       = errors.New("media: the server stopped sending on the video leg")
	ErrStreamNoVideo      = errors.New("media: no video arrived on the video leg")
	ErrKeyframeUnanswered = errors.New("media: the server did not answer keyframe requests")
)

type StreamConfig struct {
	Offer  *Offer
	Remote netip.Addr
	Local  netip.Addr
	Sink   Sink
}

type StreamStats struct {
	VideoPackets             uint64
	VideoPacketsExpected     uint64
	VideoBytes               uint64
	AudioBytes               uint64
	VideoAccessUnitsReceived uint64
	VideoAccessUnits         uint64
	VideoFrames              uint64
	VideoLosses              uint64
	VideoLatePackets         uint64
	VideoPlayoutDelay        time.Duration
	AudioPackets             uint64
	AudioLost                uint64
	AuthFailures             uint64
	KeyframeRequests         uint64
	SenderReports            uint64
	OneWayDelay              time.Duration
	Ports                    Ports
	Answer                   Answer
	LastVideoPacketAt        time.Time
}

type Stream struct {
	config StreamConfig

	access   sync.Mutex
	ports    *Ports
	answer   *Answer
	video    *leg
	audio    *leg
	closed   bool
	stopOnce sync.Once
	done     chan struct{}
	wg       sync.WaitGroup
	keyframe chan struct{}

	rate                rateFeedback
	microphone          *srtpSender
	microphoneIndex     uint64
	microphoneTimestamp uint32
	microphoneStarted   bool
	microphoneAccess    sync.Mutex
	microphonePacket    []byte
	lastKeyframeRequest time.Time
	keyframeRequestedAt time.Time
	stats               StreamStats
	receiveRTCP         []byte
	feedbackRTCP        []byte
}

type leg struct {
	conn        *net.UDPConn
	localSSRC   uint32
	srtp        *srtpReceiver
	reports     *srtcpReceiver
	sendAccess  sync.Mutex
	sender      *srtcpSender
	sendBuffer  []byte
	sources     map[uint32]*receptionStats
	lastHeardAt time.Time
}

func NewStream(config StreamConfig) *Stream {
	config.Remote = config.Remote.Unmap()
	config.Local = config.Local.Unmap()
	return &Stream{
		config:   config,
		done:     make(chan struct{}),
		keyframe: make(chan struct{}, 1),
		rate:     newRateFeedback(time.Now()),
	}
}

func (s *Stream) HandleRectangle(payload []byte) error {
	reply, err := parseReply(payload)
	if err == nil {
		switch message := reply.(type) {
		case *Ports:
			err = s.handlePorts(message)
		case *Answer:
			s.access.Lock()
			s.answer = message
			s.stats.Answer = *message
			s.rate.restart()
			s.stats.OneWayDelay = 0
			s.access.Unlock()
		case *ServerError:
			err = message
		}
	}
	if err != nil {
		s.stop(err)
	}
	return err
}

func (s *Stream) handlePorts(ports *Ports) error {
	s.config.Offer.hdr.Store(ports.Video.HDR)
	s.access.Lock()
	defer s.access.Unlock()
	s.stats.Ports = *ports
	if s.closed {
		return os.ErrClosed
	}
	if !ports.Video.Enabled || ports.Video.Port == 0 {
		return fmt.Errorf("media: server did not enable the video leg")
	}
	if s.ports != nil && s.ports.Video.Port == ports.Video.Port && s.ports.Audio.Port == ports.Audio.Port {
		s.ports = ports
		return nil
	}
	if s.ports != nil {
		return fmt.Errorf("media: server moved the media ports")
	}
	offer := s.config.Offer
	video, err := s.openLeg(ports.Video.Port, offer.videoSend[:], offer.videoReceive[:], offer.videoSSRC, videoReceiveBuffer)
	if err != nil {
		return err
	}
	var audio *leg
	if ports.Audio.Enabled && ports.Audio.Port != 0 {
		audio, err = s.openLeg(ports.Audio.Port, offer.audioSend[:], offer.audioReceive[:], offer.audioSSRC, audioReceiveBuffer)
		if err != nil {
			_ = video.conn.Close()
			return err
		}
	}
	if audio != nil && offer.options.Audio == AudioToServer {
		s.microphone, err = newSRTPSender(offer.audioSend[:])
		if err != nil {
			_ = video.conn.Close()
			_ = audio.conn.Close()
			return err
		}
		var random [6]byte
		_, _ = rand.Read(random[:])
		s.microphoneIndex = uint64(binary.BigEndian.Uint16(random[:]) & 0x7fff)
		s.microphoneTimestamp = binary.BigEndian.Uint32(random[2:])
	}
	s.ports = ports
	s.video = video
	s.audio = audio
	s.wg.Add(2)
	go s.receiveVideo(video)
	go s.feedback()
	if audio != nil {
		s.wg.Add(1)
		go s.receiveAudio(audio)
	}
	return nil
}

func (s *Stream) openLeg(port uint16, sendKey []byte, receiveKey []byte, localSSRC uint32, buffer int) (*leg, error) {
	srtp, err := newSRTPReceiver(receiveKey)
	if err != nil {
		return nil, err
	}
	reports, err := newSRTCPReceiver(receiveKey)
	if err != nil {
		return nil, err
	}
	sender, err := newSRTCPSender(sendKey)
	if err != nil {
		return nil, err
	}
	conn, err := dialLeg(s.config.Local, netip.AddrPortFrom(s.config.Remote, port), buffer)
	if err != nil {
		return nil, fmt.Errorf("media: open UDP port %d: %w", port, err)
	}
	return &leg{
		conn:        conn,
		localSSRC:   localSSRC,
		srtp:        srtp,
		reports:     reports,
		sender:      sender,
		sources:     make(map[uint32]*receptionStats),
		lastHeardAt: time.Now(),
	}, nil
}

func (l *leg) send(packet []byte) {
	l.sendAccess.Lock()
	defer l.sendAccess.Unlock()
	l.sendBuffer = l.sender.appendProtected(l.sendBuffer[:0], packet)
	_, _ = l.conn.Write(l.sendBuffer)
}

var ErrNoMicrophone = errors.New("media: the offer did not ask for AudioToServer, or the server has not named its ports")

func (s *Stream) SendAudio(data []byte) error {
	s.microphoneAccess.Lock()
	defer s.microphoneAccess.Unlock()
	s.access.Lock()
	if s.microphone == nil || s.closed {
		s.access.Unlock()
		return ErrNoMicrophone
	}
	if len(data) == 0 || len(data) > 0x1fff {
		s.access.Unlock()
		return fmt.Errorf("media: audio access unit of %d bytes", len(data))
	}
	packet := append(s.microphonePacket[:0], make([]byte, 12)...)
	packet[0] = 0x80
	packet[1] = payloadTypeAudio
	if !s.microphoneStarted {
		packet[1] |= 0x80
		s.microphoneStarted = true
	}
	binary.BigEndian.PutUint16(packet[2:], uint16(s.microphoneIndex))
	binary.BigEndian.PutUint32(packet[4:], s.microphoneTimestamp)
	binary.BigEndian.PutUint32(packet[8:], s.config.Offer.audioSSRC)
	packet = binary.BigEndian.AppendUint16(packet, 16)
	packet = binary.BigEndian.AppendUint16(packet, uint16(len(data)<<3))
	packet = append(packet, data...)
	protected := s.microphone.protect(packet, 12, s.microphoneIndex)
	s.microphonePacket = protected
	s.microphoneIndex++
	s.microphoneTimestamp += audioFramesPerPacket
	audio := s.audio
	s.access.Unlock()
	_, err := audio.conn.Write(protected)
	if err != nil && !isConnectionRefused(err) {
		return err
	}
	return nil
}

func (s *Stream) StopIfNotStarted() {
	s.access.Lock()
	waiting := s.ports == nil && !s.closed
	s.access.Unlock()
	if waiting {
		s.stop(ErrStreamNoVideo)
	}
}

func (s *Stream) RequestKeyframe() {
	select {
	case s.keyframe <- struct{}{}:
	default:
	}
}

func (s *Stream) Stats() StreamStats {
	s.access.Lock()
	defer s.access.Unlock()
	return s.stats
}

func (s *Stream) Close() error {
	s.stop(nil)
	s.wg.Wait()
	return nil
}

// An avconferenced crash on macOS 27.0.1 (26A434), 2026-10-05 11:12,
// shows stop waiting for a write lock in VTP_Close while VTPRecvProc is
// blocked in __recvmsg (AVConference 2235.63.5.2, UUID 93D8C8C0-8B9C-3A5E-A47D-01B5FAF73E1B).
func (s *Stream) Drain(closed time.Time) {
	for {
		s.access.Lock()
		video := s.video
		var heard time.Time
		if video != nil {
			heard = video.lastHeardAt
		}
		s.access.Unlock()
		if video == nil {
			return
		}
		now := time.Now()
		wait := max(drainMinimum-now.Sub(closed), drainQuiet-now.Sub(heard))
		wait = min(wait, drainLimit-now.Sub(closed))
		if wait <= 0 {
			return
		}
		select {
		case <-s.done:
			return
		case <-time.After(wait):
		}
	}
}

func (s *Stream) stop(cause error) {
	s.stopOnce.Do(func() {
		s.access.Lock()
		s.closed = true
		video, audio := s.video, s.audio
		s.access.Unlock()
		close(s.done)
		if video != nil {
			_ = video.conn.Close()
		}
		if audio != nil {
			_ = audio.conn.Close()
		}
		s.config.Sink.StreamStopped(cause)
	})
}

func (s *Stream) receiveVideo(video *leg) {
	defer s.wg.Done()
	opened := time.Now()
	var assembler *videoAssembler
	var appliedAnswer *Answer
	var unitsAtAnswer uint64
	reader := newLegReader(video.conn, readBatchSize)
	for {
		deadline := time.Now().Add(receiveTick)
		if assembler != nil {
			if due := assembler.nextDue(); due != 0 && due < deadline.UnixNano() {
				deadline = time.Unix(0, due)
			}
		}
		_ = video.conn.SetReadDeadline(deadline)
		datagrams, err := reader.read()
		now := time.Now()
		if err != nil {
			if s.isDone() {
				return
			}
			if !errors.Is(err, os.ErrDeadlineExceeded) && !isConnectionRefused(err) {
				s.stop(fmt.Errorf("media: video leg: %w", err))
				return
			}
		}
		for index := range max(len(datagrams), 1) {
			select {
			case <-s.keyframe:
				if assembler != nil {
					assembler.requestSync()
				}
				s.requestKeyframe(video, assembler)
			default:
			}
			s.applyVideoAnswer(assembler, &appliedAnswer, &unitsAtAnswer)
			if index < len(datagrams) && len(datagrams[index]) > 0 {
				s.handleVideoDatagram(video, &assembler, datagrams[index], now)
			}
			if assembler != nil {
				assembler.tick(now.UnixNano())
				if assembler.err != nil {
					s.stop(assembler.err)
					return
				}
				if order, pending := assembler.takeAcknowledgment(); pending {
					s.receiveRTCP = appendReferenceAcknowledgment(s.receiveRTCP[:0], video.localSSRC, order)
					video.send(s.receiveRTCP)
				}
				if assembler.wantsKeyframe() {
					s.requestKeyframe(video, assembler)
				}
				s.access.Lock()
				if s.answer == appliedAnswer && assembler.units > unitsAtAnswer {
					s.rate.videoArrived = true
				}
				if !assembler.awaitingSync {
					s.lastKeyframeRequest = time.Time{}
					s.keyframeRequestedAt = time.Time{}
				}
				unanswered := !s.keyframeRequestedAt.IsZero() && now.Sub(s.keyframeRequestedAt) > keyframeAnswerLimit
				s.stats.VideoAccessUnitsReceived = assembler.received
				s.stats.VideoAccessUnits = assembler.units
				s.stats.VideoFrames = assembler.frames
				s.stats.VideoLosses = assembler.losses
				s.stats.VideoLatePackets = assembler.latePackets()
				s.stats.VideoPlayoutDelay = time.Duration(assembler.playoutDelay)
				s.access.Unlock()
				if unanswered {
					s.stop(ErrKeyframeUnanswered)
					return
				}
			}
			heardAt := s.lastHeardAt(video)
			if firstVideoExpired(assembler, now.Sub(opened)) {
				s.stop(ErrStreamNoVideo)
				return
			}
			if now.Sub(heardAt) > legSilenceLimit {
				s.stop(ErrStreamSilent)
				return
			}
		}
	}
}

func firstVideoExpired(assembler *videoAssembler, elapsed time.Duration) bool {
	return elapsed > firstVideoLimit && (assembler == nil || assembler.units == 0)
}

func (s *Stream) applyVideoAnswer(assembler *videoAssembler, applied **Answer, unitsAtAnswer *uint64) {
	s.access.Lock()
	defer s.access.Unlock()
	if s.answer == nil || s.answer == *applied {
		return
	}
	*applied = s.answer
	*unitsAtAnswer = 0
	if assembler != nil {
		if s.answer.Video.SSRC != 0 {
			assembler.expectAnswer(s.answer.Video)
		}
		*unitsAtAnswer = assembler.units
	}
}

func (s *Stream) handleVideoDatagram(video *leg, assembler **videoAssembler, datagram []byte, now time.Time) {
	if isRTCP(datagram) {
		s.handleReports(video, datagram, now)
		return
	}
	packet, err := video.srtp.unprotect(datagram)
	if err != nil {
		if errors.Is(err, errAuthentication) {
			s.access.Lock()
			s.stats.AuthFailures++
			s.access.Unlock()
		}
		return
	}
	s.access.Lock()
	video.lastHeardAt = now
	s.stats.VideoPackets++
	s.stats.VideoBytes += uint64(len(datagram))
	s.stats.LastVideoPacketAt = now
	source := video.sources[packet.ssrc]
	if source == nil {
		if len(video.sources) >= maxTrackedVideoSSRC {
			clear(video.sources)
		}
		source = &receptionStats{}
		video.sources[packet.ssrc] = source
	}
	s.stats.VideoPacketsExpected += uint64(source.record(packet.sequence, packet.timestamp, videoClockRate, now))
	s.rate.record(packet.ssrc, packet.sequence, packet.timestamp, now)
	s.stats.OneWayDelay = time.Duration(s.rate.delay.value * float64(time.Second))
	s.access.Unlock()
	if *assembler == nil {
		codec := codecForPayloadType(int(packet.payloadType))
		if codec == 0 {
			return
		}
		// AVConference negotiateTilesPerFrame:negotiatedSettings: selects the smaller
		// of local tilesPerVideoFrame and remote tilesPerFrame (macOS 27.0.1 26A434,
		// 0x1c12305b4; macOS 27.2 26B5101f, 0x1c08a32ec).
		bands := s.config.Offer.options.TilesPerFrame
		s.access.Lock()
		answer := s.answer
		if s.answer != nil && s.answer.Video.TilesPerFrame >= 1 && s.answer.Video.TilesPerFrame <= bands {
			bands = s.answer.Video.TilesPerFrame
		}
		s.access.Unlock()
		*assembler = newVideoAssembler(codec, bands, s.config.Sink, func() {
			s.requestKeyframe(video, *assembler)
		})
		if answer != nil && answer.Video.SSRC != 0 {
			(*assembler).expectAnswer(answer.Video)
		}
	}
	(*assembler).push(packet, datagram[:len(datagram)-authTagLength], now.UnixNano())
}

func (s *Stream) requestKeyframe(video *leg, assembler *videoAssembler) {
	s.access.Lock()
	if assembler == nil && s.stats.VideoPackets == 0 {
		s.access.Unlock()
		return
	}
	if !s.lastKeyframeRequest.IsZero() && time.Since(s.lastKeyframeRequest) < keyframeInterval {
		s.access.Unlock()
		return
	}
	s.lastKeyframeRequest = time.Now()
	if s.keyframeRequestedAt.IsZero() {
		s.keyframeRequestedAt = s.lastKeyframeRequest
	}
	s.stats.KeyframeRequests++
	s.receiveRTCP = appendKeyframeRequest(s.receiveRTCP[:0], video.localSSRC, video.sources, s.lastKeyframeRequest)
	s.access.Unlock()
	video.send(s.receiveRTCP)
}

func (s *Stream) handleReports(target *leg, datagram []byte, now time.Time) {
	compound, err := target.reports.unprotect(datagram)
	if err != nil {
		return
	}
	s.access.Lock()
	defer s.access.Unlock()
	target.lastHeardAt = now
	s.stats.SenderReports++
	senderReports(compound, func(ssrc uint32, ntpMiddle uint32) {
		source := target.sources[ssrc]
		if source == nil {
			return
		}
		source.lastSenderReport = ntpMiddle
		source.senderReportAt = now
	})
}

func (s *Stream) receiveAudio(audio *leg) {
	defer s.wg.Done()
	receiver := &audioReceiver{sink: s.config.Sink}
	reader := newLegReader(audio.conn, 1)
	for {
		_ = audio.conn.SetReadDeadline(time.Now().Add(receiveTick))
		datagrams, err := reader.read()
		now := time.Now()
		if err != nil {
			if s.isDone() {
				return
			}
			if !errors.Is(err, os.ErrDeadlineExceeded) && !isConnectionRefused(err) {
				s.stop(fmt.Errorf("media: audio leg: %w", err))
				return
			}
		}
		for _, datagram := range datagrams {
			if isRTCP(datagram) {
				s.handleReports(audio, datagram, now)
			} else {
				packet, unprotectErr := audio.srtp.unprotect(datagram)
				if unprotectErr == nil {
					s.access.Lock()
					audio.lastHeardAt = now
					s.stats.AudioPackets++
					s.stats.AudioBytes += uint64(len(datagram))
					source := audio.sources[packet.ssrc]
					if source == nil {
						if len(audio.sources) >= maxTrackedVideoSSRC {
							clear(audio.sources)
						}
						source = &receptionStats{}
						audio.sources[packet.ssrc] = source
					}
					source.record(packet.sequence, packet.timestamp, audioSampleRate, now)
					s.access.Unlock()
					receiver.push(packet, now)
				}
			}
		}
		receiver.tick(now)
		s.access.Lock()
		s.stats.AudioLost = receiver.lost
		s.access.Unlock()
	}
}

func (s *Stream) lastHeardAt(target *leg) time.Time {
	s.access.Lock()
	defer s.access.Unlock()
	return target.lastHeardAt
}

func (s *Stream) isDone() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

func (s *Stream) feedback() {
	defer s.wg.Done()
	rateTicker := time.NewTicker(rateFeedbackInterval)
	defer rateTicker.Stop()
	s.sendReports()
	lastReport := time.Now()
	for {
		select {
		case <-s.done:
			return
		case now := <-rateTicker.C:
			var report bool
			s.feedbackRTCP, report = s.rateReport(s.feedbackRTCP[:0], now)
			if report {
				s.video.send(s.feedbackRTCP)
			}
			if now.Sub(lastReport) >= reportInterval {
				lastReport = now
				s.sendReports()
			}
		}
	}
}

func (s *Stream) rateReport(out []byte, now time.Time) ([]byte, bool) {
	s.access.Lock()
	defer s.access.Unlock()
	return s.rate.appendReport(out, s.video.localSSRC, now)
}

func (s *Stream) sendReports() {
	now := time.Now()
	for _, target := range []*leg{s.video, s.audio} {
		if target == nil {
			continue
		}
		s.access.Lock()
		s.feedbackRTCP = appendReceiverReport(s.feedbackRTCP[:0], target.localSSRC, target.sources, now)
		s.access.Unlock()
		target.send(s.feedbackRTCP)
	}
}
