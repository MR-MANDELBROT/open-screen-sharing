package media

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"sync"
	"sync/atomic"
	"time"

	E "github.com/sagernet/sing/common/exceptions"
)

const (
	serverOpenLimit        = 10 * time.Second
	serverKeyframeInterval = time.Second
	clientSilenceLimit     = 20 * time.Second
	maxSourceFailures      = 3
	videoSendBuffer        = 1 << 20
	serverReadBuffer       = 256 << 10
)

type ServerStreamConfig struct {
	Logger               *slog.Logger
	InitialTarget        netip.Addr
	AllowLoopbackTarget  bool
	VideoPort            uint16
	AudioPort            uint16
	DisableAudioLeg      bool
	ListenUDP            func(port uint16) (*net.UDPConn, error)
	OpenVideo            ServerVideoOpener
	OpenAudio            ServerAudioOpener
	Send                 func(payload []byte) error
	KeepExtensionProfile bool
	StripSliceTrailer    bool
}

type ServerStreamStats struct {
	Offers           uint64
	Answers          uint64
	Errors           uint64
	VideoPackets     uint64
	VideoBytes       uint64
	AudioPackets     uint64
	HeldPackets      uint64
	ClientReports    uint64
	AuthFailures     uint64
	SendErrors       uint64
	KeyframeRequests uint64
	VideoTarget      netip.AddrPort
	AudioTarget      netip.AddrPort
	Running          bool
}

type ServerStream struct {
	config ServerStreamConfig
	logger *slog.Logger
	video  *serverLeg
	audio  *serverLeg
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	access          sync.Mutex
	closed          bool
	generation      uint64
	offered         bool
	renegotiateNext bool
	quiet           bool
	cancelOpen      context.CancelFunc
	session         *serverSession
	failures        int
	stats           ServerStreamStats
}

type serverSession struct {
	generation   uint64
	ctx          context.Context
	cancel       context.CancelFunc
	video        ServerVideoSource
	audio        ServerAudioSource
	startedAt    time.Time
	lastKeyframe time.Time
	forwarded    atomic.Uint64
	wg           sync.WaitGroup
}

func (s *serverSession) close() {
	s.cancel()
	_ = s.video.Close()
	if s.audio != nil {
		_ = s.audio.Close()
	}
	s.wg.Wait()
}

func ListenServerStream(config ServerStreamConfig) (*ServerStream, error) {
	initial := config.InitialTarget.Unmap()
	if initial.IsLoopback() && !config.AllowLoopbackTarget {
		initial = netip.Addr{}
	}
	videoConn, err := config.ListenUDP(config.VideoPort)
	if err != nil {
		return nil, E.Cause(err, "media: listen on UDP port ", config.VideoPort)
	}
	_ = videoConn.SetReadBuffer(serverReadBuffer)
	_ = videoConn.SetWriteBuffer(videoSendBuffer)
	ctx, cancel := context.WithCancel(context.Background())
	s := &ServerStream{
		config: config,
		logger: config.Logger,
		ctx:    ctx,
		cancel: cancel,
		video:  newServerLeg("video", videoConn, videoClockRate, initial, config.Logger),
	}
	if !config.DisableAudioLeg {
		audioConn, err := config.ListenUDP(config.AudioPort)
		if err != nil {
			cancel()
			_ = videoConn.Close()
			return nil, E.Cause(err, "media: listen on UDP port ", config.AudioPort)
		}
		_ = audioConn.SetReadBuffer(serverReadBuffer)
		s.audio = newServerLeg("audio", audioConn, audioSampleRate, initial, config.Logger)
	}
	for _, leg := range s.legs() {
		s.wg.Go(func() { leg.receive(s.handleClientDatagram) })
	}
	s.wg.Go(s.housekeep)
	return s, nil
}

func (s *ServerStream) legs() []*serverLeg {
	if s.audio == nil {
		return []*serverLeg{s.video}
	}
	return []*serverLeg{s.video, s.audio}
}

func (s *ServerStream) Ports() (audio, video uint16) {
	if s.audio != nil {
		audio = s.audio.port
	}
	return audio, s.video.port
}

func (s *ServerStream) PortsMessage() []byte {
	audio, video := s.Ports()
	return portsMessage(audio, video)
}

func (s *ServerStream) HandleOffer(message []byte) error {
	offer, err := parseOffer(message)
	s.access.Lock()
	defer s.access.Unlock()
	s.stats.Offers++
	s.quiet = false
	if err != nil {
		s.logger.Warn("media offer", "error", err)
		s.stats.Errors++
		return s.config.Send(ErrorMessage(ServerErrorBadConfiguration, 0))
	}
	s.generation++
	generation := s.generation
	s.closeLaterLocked(s.detachLocked())
	now := time.Now()
	s.video.setKeys(&offer.Video, now)
	if s.audio != nil {
		s.audio.setKeys(&offer.Audio, now)
	}
	ctx, cancel := context.WithTimeout(s.ctx, serverOpenLimit)
	s.cancelOpen = cancel
	s.offered = true
	s.logger.Debug("media offer", "video_mode", offer.Video.Mode, "audio_mode", offer.Audio.Mode,
		"width", offer.Video.Width, "height", offer.Video.Height, "frame_rate_60", offer.FrameRate60,
		"pixel_formats", offer.Video.PixelFormats, "maximum_bitrate", offer.Video.MaximumBitrate)
	s.wg.Go(func() { s.open(ctx, cancel, generation, offer) })
	return nil
}

func (s *ServerStream) closeLaterLocked(session *serverSession) {
	if session != nil {
		s.wg.Go(session.close)
	}
}

func (s *ServerStream) open(ctx context.Context, cancel context.CancelFunc, generation uint64, offer *serverOffer) {
	defer cancel()
	video, err := s.config.OpenVideo(ctx, ServerVideoRequest{
		ClientSSRC:     offer.Video.SSRC,
		Mode:           VideoMode(offer.Video.Mode),
		TilesPerFrame:  offer.Video.TilesPerFrame,
		PixelFormats:   offer.Video.PixelFormats,
		MaximumBitrate: offer.Video.MaximumBitrate,
	})
	if err != nil {
		s.openFailed(generation, E.Cause(err, "open the device video"))
		return
	}
	var audio ServerAudioSource
	if s.audio != nil && offer.Audio.Present && offer.Audio.Mode == negotiatorModeRemoteDesktopSystemAudio {
		audio, err = s.config.OpenAudio()
		if err != nil {
			if !errors.Is(err, ErrAudioUnsupported) {
				s.logger.Warn("open the device sound", "error", err)
			}
			audio = nil
		}
	}
	info := video.Info()
	answer := serverAnswer{
		FrameRate60: info.FrameRate != 30,
		Video:       info,
		PixelFormat: answerPixelFormat(offer.Video.PixelFormats, info.PixelFormat),
	}
	var audioInfo ServerAudioInfo
	if audio != nil {
		audioInfo = audio.Info()
		answer.Audio = &audioInfo
	}
	payload, err := answerMessage(answer)
	closeSources := func() {
		_ = video.Close()
		if audio != nil {
			_ = audio.Close()
		}
	}
	if err != nil {
		closeSources()
		s.openFailed(generation, err)
		return
	}
	s.access.Lock()
	defer s.access.Unlock()
	if s.generation != generation {
		closeSources()
		return
	}
	s.cancelOpen = nil
	sessionContext, sessionCancel := context.WithCancel(s.ctx)
	session := &serverSession{
		generation: generation,
		ctx:        sessionContext,
		cancel:     sessionCancel,
		video:      video,
		audio:      audio,
		startedAt:  time.Now(),
	}
	s.session = session
	err = s.config.Send(payload)
	if err != nil {
		s.logger.Debug("send the media answer", "error", err)
	} else {
		s.stats.Answers++
	}
	s.video.activate(&serverActive{ssrc: info.SSRC, receiverSSRC: info.ReceiverSSRC, generation: generation})
	if audio != nil {
		s.audio.activate(&serverActive{ssrc: audioInfo.SSRC, receiverSSRC: audioInfo.ReceiverSSRC, generation: generation})
	}
	session.wg.Go(func() { s.forward(session, s.video, video.ReadPacket, video.PacketArrival) })
	if audio != nil {
		session.wg.Go(func() { s.forward(session, s.audio, audio.ReadPacket, nil) })
	}
	s.logger.Info("media stream started", "ssrc", info.SSRC, "width", info.Width, "height", info.Height,
		"sound", audio != nil, "target", s.video.currentTarget())
	if s.renegotiateNext {
		s.renegotiateNext = false
		previous, err := s.renegotiateLocked()
		s.closeLaterLocked(previous)
		if err != nil {
			s.logger.Debug("send the media ports", "error", err)
		}
	}
}

func (s *ServerStream) openFailed(generation uint64, err error) {
	s.access.Lock()
	defer s.access.Unlock()
	if s.generation != generation {
		return
	}
	s.cancelOpen = nil
	s.renegotiateNext = false
	s.stats.Errors++
	s.logger.Warn("media stream", "error", err)
	_ = s.config.Send(ErrorMessage(ServerErrorBadConfiguration, 0))
}

func (s *ServerStream) detachLocked() *serverSession {
	if s.cancelOpen != nil {
		s.cancelOpen()
		s.cancelOpen = nil
	}
	session := s.session
	s.session = nil
	if session == nil {
		return nil
	}
	for _, leg := range s.legs() {
		leg.deactivate()
	}
	session.cancel()
	return session
}

// ScreenSharing ignores a new ports message while its
// mediaStreamMessage1Received flag is set; it sets the flag after handling a
// ports message, and handling the answer clears it (ScreenSharing 776.27,
// macOS 27.0.1 26A434, UUID EC0B2C20-F816-3FCE-822D-F9403C09EB50,
// -[SSSession handleAVCMediaEncoding:] 0x1cc0e48dc..0x1cc0e495c,
// 0x1cc0e5c54..0x1cc0e5c5c, 0x1cc0e49d4..0x1cc0e49e8; ScreenSharing 786.3,
// macOS 27.2 26B5101f, UUID 21DF7A41-0D38-3DF3-B87A-8A6249686735,
// -[SSSession handleAVCMediaEncoding:] 0x22e52795c..0x22e5279dc,
// 0x22e528cd4..0x22e528cdc, 0x22e527a54..0x22e527a68).
func (s *ServerStream) Renegotiate() error {
	s.access.Lock()
	if s.closed {
		s.access.Unlock()
		return os.ErrClosed
	}
	if s.cancelOpen != nil {
		s.renegotiateNext = true
		s.access.Unlock()
		return nil
	}
	previous, err := s.renegotiateLocked()
	s.access.Unlock()
	if previous != nil {
		previous.close()
	}
	return err
}

func (s *ServerStream) renegotiateLocked() (*serverSession, error) {
	if !s.offered {
		return nil, nil
	}
	s.generation++
	s.quiet = false
	previous := s.detachLocked()
	return previous, s.config.Send(s.PortsMessage())
}

func (s *ServerStream) Close() error {
	s.access.Lock()
	s.closed = true
	s.generation++
	previous := s.detachLocked()
	s.access.Unlock()
	s.cancel()
	if previous != nil {
		previous.close()
	}
	for _, leg := range s.legs() {
		_ = leg.conn.Close()
	}
	s.wg.Wait()
	return nil
}

func (s *ServerStream) Stats() ServerStreamStats {
	s.access.Lock()
	stats := s.stats
	stats.Running = s.session != nil
	s.access.Unlock()
	video := s.video.snapshot()
	stats.VideoPackets, stats.VideoBytes = video.packets, video.octets
	stats.HeldPackets = video.held
	stats.ClientReports = video.clientReports
	stats.AuthFailures = video.authFailures
	stats.SendErrors = video.sendErrors
	stats.VideoTarget = video.target
	if s.audio != nil {
		audio := s.audio.snapshot()
		stats.AudioPackets = audio.packets
		stats.HeldPackets += audio.held
		stats.ClientReports += audio.clientReports
		stats.AuthFailures += audio.authFailures
		stats.SendErrors += audio.sendErrors
		stats.AudioTarget = audio.target
	}
	return stats
}

func (s *ServerStream) forward(session *serverSession, leg *serverLeg, read func(context.Context, []byte) (int, bool, error), arrival func() time.Time) {
	buffer := make([]byte, maxDatagram+bandPositionGrowth+authTagLength)
	isVideo := leg == s.video
	var positioner *bandPositioner
	if isVideo && !s.config.StripSliceTrailer {
		positioner = newBandPositioner(session.video.Info())
	}
	for {
		n, rtcp, err := read(session.ctx, buffer[:maxDatagram])
		if err != nil {
			s.sourceEnded(session, leg, err)
			return
		}
		now := time.Now()
		arrived := now
		if arrival != nil {
			arrived = now.Add(-now.Sub(arrival()))
		}
		if rtcp {
			leg.access.Lock()
			leg.recordDeviceRTCPLocked(buffer[:n], now, arrived)
			leg.access.Unlock()
			continue
		}
		packet := buffer[:n]
		header, err := parseRTPHeader(packet)
		if err != nil {
			continue
		}
		if isVideo {
			if s.config.StripSliceTrailer {
				packet = stripSliceTrailer(packet, header.payloadOffset)
			}
			if !s.config.KeepExtensionProfile {
				rewriteExtensionProfile(packet)
			}
			if positioner != nil {
				packet = positioner.rewrite(packet, header)
			}
		}
		leg.access.Lock()
		datagram, target, ok := leg.protectLocked(packet, header, session.generation, now)
		if ok && isVideo {
			leg.timePacketLocked(header, len(packet), arrived, now)
		}
		leg.access.Unlock()
		if !ok {
			continue
		}
		leg.send(datagram, target)
		session.forwarded.Add(1)
	}
}

func (s *ServerStream) sourceEnded(session *serverSession, leg *serverLeg, err error) {
	if session.ctx.Err() != nil {
		return
	}
	s.access.Lock()
	if s.session != session {
		s.access.Unlock()
		return
	}
	if leg != s.video {
		s.logger.Warn("device sound stopped", "error", err)
		s.audio.deactivate()
		s.access.Unlock()
		return
	}
	if session.forwarded.Load() > 0 {
		s.failures = 0
	}
	s.failures++
	s.logger.Warn("device video stopped", "error", err, "failures", s.failures)
	if s.failures > maxSourceFailures {
		s.failures = 0
		s.generation++
		previous := s.detachLocked()
		s.stats.Errors++
		_ = s.config.Send(ErrorMessage(ServerErrorBadConfiguration, 0))
		s.closeLaterLocked(previous)
		s.access.Unlock()
		return
	}
	previous, sendErr := s.renegotiateLocked()
	s.closeLaterLocked(previous)
	s.access.Unlock()
	if sendErr != nil {
		s.logger.Debug("send the media ports", "error", sendErr)
	}
}

func (s *ServerStream) handleClientDatagram(leg *serverLeg, datagram []byte, from netip.AddrPort, now time.Time) {
	leg.access.Lock()
	if leg.receiver == nil {
		leg.keepPendingLocked(datagram, from, now)
		leg.access.Unlock()
		return
	}
	packet, err := leg.receiver.unprotect(datagram)
	if err != nil {
		if errors.Is(err, errAuthentication) {
			leg.authFailures++
			leg.keepPendingLocked(datagram, from, now)
		}
		leg.access.Unlock()
		return
	}
	leg.clientReports++
	missed := leg.latchLocked(from, now)
	out, keyframe := leg.rewriteClientRTCPLocked(packet, now)
	leg.access.Unlock()
	if leg != s.video {
		keyframe = false
	} else if missed {
		keyframe = true
	}
	s.access.Lock()
	session := s.session
	if session == nil && s.quiet && leg == s.video && !s.closed {
		s.quiet = false
		s.logger.Info("media client is heard again", "address", from)
		previous, err := s.renegotiateLocked()
		s.closeLaterLocked(previous)
		s.access.Unlock()
		if err != nil {
			s.logger.Debug("send the media ports", "error", err)
		}
		return
	}
	if session != nil && keyframe {
		if now.Sub(session.lastKeyframe) >= serverKeyframeInterval {
			session.lastKeyframe = now
			s.stats.KeyframeRequests++
		} else {
			keyframe = false
		}
	}
	s.access.Unlock()
	if session == nil {
		return
	}
	if leg == s.video {
		if len(out) > 0 {
			_ = session.video.WriteRTCP(out)
		}
		if keyframe {
			err = session.video.RequestKeyFrame()
			if err != nil {
				s.logger.Debug("request a keyframe from the device", "error", err)
			}
		}
	} else if session.audio != nil && len(out) > 0 {
		_ = session.audio.WriteRTCP(out)
	}
}

func (s *ServerStream) housekeep() {
	ticker := time.NewTicker(reportInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case now := <-ticker.C:
			for _, leg := range s.legs() {
				leg.access.Lock()
				report, ok := leg.senderReportLocked(now)
				target := leg.target
				leg.access.Unlock()
				if ok {
					leg.send(report, target)
				}
			}
			s.video.access.Lock()
			timing, ok := s.video.takeTimingLocked(now)
			s.video.access.Unlock()
			if ok {
				s.logger.Info("media timing", timing...)
			}
			s.checkSilence(now)
		}
	}
}

func (s *ServerStream) checkSilence(now time.Time) {
	s.access.Lock()
	session := s.session
	if session == nil {
		s.access.Unlock()
		return
	}
	lastHeard := s.video.lastHeardAt()
	if lastHeard.Before(session.startedAt) {
		lastHeard = session.startedAt
	}
	if now.Sub(lastHeard) < clientSilenceLimit {
		s.access.Unlock()
		return
	}
	s.logger.Info("media client went quiet", "since", lastHeard, "target", s.video.currentTarget())
	s.generation++
	s.quiet = true
	previous := s.detachLocked()
	s.access.Unlock()
	if previous != nil {
		previous.close()
	}
}
