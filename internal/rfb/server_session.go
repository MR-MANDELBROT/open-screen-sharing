package rfb

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb/media"
)

const (
	serverVersion          = "RFB 003.889\n"
	preludeLimit           = 32
	preludeByteLimit       = 64 << 10
	pasteboardFetchTimeout = 5 * time.Second
	layoutAnswerInterval   = time.Second
)

type ServerSession struct {
	server *Server
	id     uint64
	conn   net.Conn
	logger *slog.Logger
	stream *stream
	outbox *outbox
	writer *writer
	ctx    context.Context
	cancel context.CancelFunc

	userName      string
	wrapKey       [16]byte
	keys          recordKeys
	writerStarted bool
	writerDone    chan struct{}
	mode          Mode
	mediaWanted   bool
	standardTimer *time.Timer
	askedMedia    atomic.Bool

	access           sync.Mutex
	encodings        []Encoding
	clipboardSharing bool
	layoutSent       bool
	screen           ServerScreen
	viewerScale      float64
	framebuffer      [2]int
	format           PixelFormat
	cursorEncoding   Encoding
	cursorScale      float64
	modelSent        string
	commandsSent     bool
	media            *media.ServerStream
	lastLayoutAnswer time.Time
	ending           bool
	fetching         bool
	fetchAgain       bool
	fetchEcho        uint32
	viewerClipboard  *Clipboard
}

func newServerSession(server *Server, conn net.Conn, id uint64) *ServerSession {
	ctx, cancel := context.WithCancel(context.Background())
	return &ServerSession{
		server:     server,
		id:         id,
		conn:       conn,
		logger:     server.logger.With("viewer", conn.RemoteAddr().String(), "session", id),
		stream:     newStream(conn),
		outbox:     newOutbox(),
		writer:     &writer{conn: conn},
		ctx:        ctx,
		cancel:     cancel,
		writerDone: make(chan struct{}),
		mode:       ModeControl,
		format:     pixelFormatBGRA,
	}
}

func (s *ServerSession) ID() uint64 {
	return s.id
}

func (s *ServerSession) RemoteAddr() net.Addr {
	return s.conn.RemoteAddr()
}

func (s *ServerSession) MediaStats() (media.ServerStreamStats, bool) {
	s.access.Lock()
	stream := s.media
	s.access.Unlock()
	if stream == nil {
		return media.ServerStreamStats{}, false
	}
	return stream.Stats(), true
}

func (s *ServerSession) Close() error {
	return s.conn.Close()
}

func (s *ServerSession) run() {
	defer s.cancel()
	err := s.handshake()
	s.server.endHandshake()
	var held [][]byte
	if err == nil {
		s.conn.SetDeadline(time.Time{})
		held, err = s.prelude()
	}
	if err != nil {
		s.conn.Close()
		s.stopWriter()
		switch {
		case errors.Is(err, ErrAuthenticationFailed):
			s.logger.Info("viewer failed to sign in", "user", s.userName, "error", err)
		case hungUp(err):
			s.logger.Debug("viewer left during the handshake", "error", err)
		case errors.Is(err, os.ErrDeadlineExceeded):
			s.logger.Debug("viewer did not sign in in time", "error", err)
		default:
			s.logger.Warn("handshake", "error", err)
		}
		return
	}
	if !s.server.addSession(s) {
		s.conn.Close()
		s.stopWriter()
		return
	}
	s.logger.Info("viewer connected", "user", s.userName)
	s.server.handler.SessionStarted(s)
	for _, message := range held {
		if err == nil {
			err = s.dispatch(message)
		}
	}
	for err == nil {
		var message []byte
		message, err = readClientMessage(s.stream)
		if err == nil {
			err = s.dispatch(message)
		}
	}
	s.finish(err)
}

func (s *ServerSession) finish(err error) {
	if s.standardTimer != nil {
		s.standardTimer.Stop()
	}
	s.access.Lock()
	s.ending = true
	stream := s.media
	s.media = nil
	s.access.Unlock()
	if stream != nil {
		_ = stream.Close()
	}
	s.conn.Close()
	s.stopWriter()
	s.cancel()
	s.server.removeSession(s)
	if hungUp(err) {
		err = nil
	}
	s.logger.Info("viewer disconnected", "error", err)
	s.server.handler.SessionEnded(s, err)
}

func hungUp(err error) bool {
	return err == nil || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, net.ErrClosed) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE)
}

func (s *ServerSession) stopWriter() {
	s.outbox.close(nil)
	if s.writerStarted {
		<-s.writerDone
	}
}

func (s *ServerSession) handshake() error {
	options := &s.server.options
	s.conn.SetDeadline(time.Now().Add(handshakeTimeout))
	_, err := io.WriteString(s.conn, serverVersion)
	if err != nil {
		return err
	}
	banner, err := s.stream.bytes(12)
	if err != nil {
		return fmt.Errorf("read the viewer's version: %w", err)
	}
	var version ProtocolVersion
	_, err = fmt.Sscanf(string(banner), "RFB %03d.%03d\n", &version.Major, &version.Minor)
	if err != nil || !version.apple() {
		return fmt.Errorf("the viewer speaks %q, not Apple's 003.889", banner)
	}
	offered := []byte{1, byte(SecurityAppleSRP)}
	if options.NoAuthentication {
		offered = []byte{2, byte(SecurityAppleSRP), byte(SecurityAskControl)}
	}
	_, err = s.conn.Write(offered)
	if err != nil {
		return err
	}
	selected, err := s.stream.u8()
	if err != nil {
		return fmt.Errorf("read the security type: %w", err)
	}
	switch {
	case SecurityType(selected) == SecurityAppleSRP:
		err = s.signIn()
	case SecurityType(selected) == SecurityAskControl && options.NoAuthentication:
		err = s.askToShare()
	default:
		return fmt.Errorf("the viewer chose security type %v", SecurityType(selected))
	}
	if err != nil {
		return err
	}
	clientInit, err := s.stream.u8()
	if err != nil {
		return fmt.Errorf("read ClientInit: %w", err)
	}
	screen := s.server.handler.Screen()
	s.screen = screen
	s.framebuffer = [2]int{screen.Width, screen.Height}
	s.logger.Debug("client init", "flags", fmt.Sprintf("%#x", clientInit), "version", version.String())
	_, err = s.conn.Write(serverInitMessage(screen.Width, screen.Height, options.Flags, s.server.commands, options.Name))
	return err
}

func (s *ServerSession) readSRPMessage() ([]byte, error) {
	size, err := s.stream.u32()
	if err != nil {
		return nil, err
	}
	if size < 4 || size > srpMessageLimit {
		return nil, fmt.Errorf("SRP message of %d bytes", size)
	}
	message, err := s.stream.bytes(int(size))
	if err != nil {
		return nil, err
	}
	inner := int(binary.BigEndian.Uint32(message))
	if inner > len(message)-4 {
		return nil, fmt.Errorf("SRP message of %d bytes in %d", inner, len(message)-4)
	}
	return message[4 : 4+inner], nil
}

func (s *ServerSession) signIn() error {
	identity, err := s.readSRPMessage()
	if err != nil {
		return fmt.Errorf("read the SRP identity: %w", err)
	}
	reader := &fieldReader{data: identity}
	reader.u16()
	s.userName = string(reader.take(reader.u16()))
	exchange := s.server.verifier.start()
	_, err = s.conn.Write(exchange.challenge())
	if err != nil {
		return err
	}
	proof, err := s.readSRPMessage()
	if err != nil {
		return fmt.Errorf("read the SRP proof: %w", err)
	}
	serverProof, wrapKey, err := exchange.verify(proof)
	if err == nil && !s.server.acceptsUserName(s.userName) {
		err = errSRPUserName
	}
	if err != nil {
		timer := time.NewTimer(s.server.signInFailureWait(time.Now()))
		select {
		case <-timer.C:
		case <-s.server.done:
		}
		timer.Stop()
		answer := append(srpVerification(nil), 0, 0, 0, 1)
		_, _ = s.conn.Write(answer)
		return fmt.Errorf("%w: %w", ErrAuthenticationFailed, err)
	}
	s.wrapKey = wrapKey
	_, err = s.conn.Write(append(srpVerification(serverProof), 0, 0, 0, 0))
	return err
}

func (s *ServerSession) prelude() ([][]byte, error) {
	var held [][]byte
	heldBytes := 0
	for {
		message, err := readClientMessage(s.stream)
		if err != nil {
			return nil, err
		}
		if message[0] != clientSetEncryption {
			heldBytes += len(message)
			if len(held) >= preludeLimit || heldBytes > preludeByteLimit {
				return nil, errors.New("too much before encryption")
			}
			held = append(held, message)
			continue
		}
		command := binary.BigEndian.Uint16(message[2:])
		s.logger.Debug("set encryption", "command", command)
		switch command {
		case 1:
			if !s.server.options.RekeyAfterEncryptionStart && !s.writerStarted {
				err = s.startWriter()
			}
		case 2:
			if !s.writerStarted {
				err = s.startWriter()
			}
			if err == nil {
				err = s.stream.startRecords(s.keys)
			}
			if err == nil {
				return held, nil
			}
		}
		if err != nil {
			return nil, err
		}
	}
}

func (s *ServerSession) startWriter() error {
	copy(s.keys.key[:], randomBytes(16))
	copy(s.keys.iv[:], randomBytes(16))
	rekey, err := rekeyRectangle(s.wrapKey, s.keys)
	if err != nil {
		return err
	}
	_, err = s.conn.Write(rekey)
	if err != nil {
		return err
	}
	encoder := &recordEncoder{}
	err = encoder.rekey(s.keys)
	if err != nil {
		return err
	}
	s.writer.encoder = encoder
	s.writerStarted = true
	go func() {
		err := s.writer.run(s.outbox)
		if err != nil {
			s.conn.Close()
		}
		close(s.writerDone)
	}()
	return nil
}

func (s *ServerSession) send(message []byte) {
	err := s.outbox.push(message, false)
	if err != nil {
		s.logger.Debug("send", "error", err)
	}
}

func (s *ServerSession) emit(event ViewerEvent) {
	s.server.handler.HandleEvent(s, event)
}

func (s *ServerSession) emitInput(event ViewerEvent) {
	if s.mode != ModeObserve {
		s.emit(event)
	}
}

func (s *ServerSession) dispatch(message []byte) error {
	switch message[0] {
	case clientSetPixelFormat:
		format := parsePixelFormat(message[4:])
		s.access.Lock()
		s.format = format
		s.access.Unlock()
		s.logger.Debug("set pixel format", "format", format.String())
	case clientSetEncodings:
		s.setEncodings(parseEncodings(message))
	case clientUpdateRequest:
		if message[1] == 0 {
			err := s.outbox.pushWait(framebufferUpdate())
			if err != nil {
				s.logger.Debug("send", "error", err)
			}
		}
	case clientKeyEvent:
		s.emitInput(ViewerKey{Down: message[1] != 0, Keysym: binary.BigEndian.Uint32(message[4:])})
	case clientPointerEvent:
		s.emitInput(parsePointerEvent(message))
	case clientCutText:
		length := int32(binary.BigEndian.Uint32(message[4:]))
		if length >= 0 {
			s.receiveClipboard(NewTextClipboard(decodeLatin1(message[8:])))
		}
	case clientSetMode:
		s.mode = Mode(binary.BigEndian.Uint16(message[2:]))
		s.emit(ViewerMode{Mode: s.mode})
	case clientFetchPasteboard:
		s.fetchPasteboard(binary.BigEndian.Uint32(message[4:]))
	case clientEncryptedEvent:
		event, err := parseEncryptedEvent(s.keys.key, message)
		if err != nil {
			return fmt.Errorf("encrypted input event: %w", err)
		}
		s.emitInput(event)
	case clientSetScale:
		s.setScale(math.Float64frombits(binary.BigEndian.Uint64(message[2:])))
	case clientAutoPasteboard:
		enabled := message[3] == 1
		s.access.Lock()
		s.clipboardSharing = enabled
		s.access.Unlock()
		s.emit(ViewerClipboardSharing{Enabled: enabled})
	case clientEvent:
		event, err := parseEventMessage(message)
		if err != nil {
			s.logger.Debug("event message", "error", err)
			return nil
		}
		if event == nil {
			s.logger.Debug("event message", "version", binary.BigEndian.Uint16(message[4:]), "subtype", binary.BigEndian.Uint16(message[6:]))
			return nil
		}
		s.emitInput(event)
	case clientMediaStream:
		s.handleOffer(message)
	case clientDisplayConfig:
		s.logger.Debug("viewer asked for virtual displays, which the server cannot make", "bytes", len(message))
		s.answerDisplayRequest()
	case clientPasteboard:
		clipboard, err := parseClientPasteboard(message)
		if err != nil {
			s.logger.Warn("unreadable pasteboard", "error", err)
			return nil
		}
		s.receiveClipboard(clipboard)
	default:
		s.logger.Debug("client message", "type", fmt.Sprintf("%#02x", message[0]), "bytes", len(message))
	}
	return nil
}

func (s *ServerSession) declaredLocked(encoding Encoding) bool {
	return slices.Contains(s.encodings, encoding)
}

func (s *ServerSession) layoutLocked(screen ServerScreen) []serverRectangle {
	var rectangles []serverRectangle
	framebuffer := scaledFramebuffer(screen, s.viewerScale)
	if framebuffer != s.framebuffer && s.declaredLocked(EncodingDesktopSize) {
		rectangles = append(rectangles, newRectangle(0, 0, framebuffer[0], framebuffer[1], EncodingDesktopSize, nil))
	}
	if s.declaredLocked(EncodingDisplayLayout) {
		rectangles = append(rectangles, displayLayoutRectangle(screen, s.viewerScale, s.server.options.DisplayFlags))
	}
	s.screen = screen
	s.framebuffer = framebuffer
	return rectangles
}

func (s *ServerSession) setScale(scale float64) {
	if !(scale > 0 && scale <= 1) {
		scale = 0
	}
	s.logger.Debug("viewer scale", "scale", scale)
	s.access.Lock()
	var rectangles []serverRectangle
	if scale != s.viewerScale {
		s.viewerScale = scale
		if s.layoutSent {
			rectangles = s.layoutLocked(s.screen)
		}
	}
	s.access.Unlock()
	if len(rectangles) > 0 {
		s.send(framebufferUpdate(rectangles...))
	}
}

func (s *ServerSession) deviceLocked(screen ServerScreen) []serverRectangle {
	var rectangles []serverRectangle
	model := s.server.options.DeviceModel
	if model == "" {
		model = screen.Model
	}
	switch {
	case !s.declaredLocked(EncodingDeviceInfo):
		s.modelSent = ""
	case model != "" && model != s.modelSent:
		s.modelSent = model
		rectangles = append(rectangles, deviceInfoRectangle(model))
	}
	commands := s.server.options.SystemCommands
	switch {
	case !s.declaredLocked(EncodingVendorKeysyms):
		s.commandsSent = false
	case len(commands) > 0 && !s.commandsSent:
		s.commandsSent = true
		rectangles = append(rectangles, vendorKeysymsRectangle(commands))
	}
	var encoding Encoding
	switch {
	case s.declaredLocked(EncodingCursorImage):
		encoding = EncodingCursorImage
	case s.declaredLocked(EncodingRichCursor):
		encoding = EncodingRichCursor
	}
	switch {
	case encoding == 0:
		s.cursorEncoding = 0
	case encoding != s.cursorEncoding || screen.Scale != s.cursorScale:
		rectangle, ok := cursorRectangle(pointerCursor(screen), encoding, s.format)
		if !ok {
			s.logger.Debug("no pointer in the viewer's pixel format", "format", s.format.String())
			break
		}
		s.cursorEncoding, s.cursorScale = encoding, screen.Scale
		rectangles = append(rectangles, rectangle)
	}
	return rectangles
}

func (s *ServerSession) setEncodings(encodings []Encoding) {
	screen := s.server.handler.Screen()
	s.access.Lock()
	s.encodings = encodings
	var rectangles []serverRectangle
	if !s.layoutSent && s.declaredLocked(EncodingDisplayLayout) {
		s.layoutSent = true
		rectangles = s.layoutLocked(screen)
	}
	rectangles = append(rectangles, s.deviceLocked(screen)...)
	wanted := s.declaredLocked(EncodingMediaStream)
	start := wanted && !s.mediaWanted
	stop := !wanted && s.mediaWanted
	s.mediaWanted = wanted
	if wanted {
		s.askedMedia.Store(true)
	} else if s.standardTimer == nil {
		s.standardTimer = time.AfterFunc(standardViewerLimit, s.letStandardViewerGo)
	}
	s.access.Unlock()
	s.logger.Debug("set encodings", "encodings", fmt.Sprint(encodings))
	if len(rectangles) > 0 {
		s.send(framebufferUpdate(rectangles...))
	}
	switch {
	case start:
		s.startMedia()
	case stop:
		s.stopMedia()
	}
}

func (s *ServerSession) letStandardViewerGo() {
	if s.askedMedia.Load() {
		return
	}
	s.logger.Info("viewer did not ask for High Performance, the only picture a device has")
	_ = s.conn.Close()
}

func (s *ServerSession) sendMedia(payload []byte) {
	s.send(mediaStreamUpdate(payload))
}

func (s *ServerSession) startMedia() {
	if !s.server.claimMedia(s) {
		s.logger.Info("media stream refused: another viewer has it")
		s.mediaWanted = false
		s.sendMedia(media.ErrorMessage(media.ServerErrorBusy, media.ServerErrorBusySubCode))
		return
	}
	config := s.server.options.Media
	config.Logger = s.logger
	config.OpenVideo = s.server.handler.OpenVideo
	config.OpenAudio = s.server.handler.OpenAudio
	config.Send = func(payload []byte) error {
		return s.outbox.push(mediaStreamUpdate(payload), false)
	}
	if address, isTCP := s.conn.RemoteAddr().(*net.TCPAddr); isTCP {
		config.InitialTarget = address.AddrPort().Addr()
	}
	stream, err := media.ListenServerStream(config)
	if err != nil {
		s.server.releaseMedia(s)
		s.mediaWanted = false
		s.logger.Warn("media stream", "error", err)
		s.sendMedia(media.ErrorMessage(media.ServerErrorBadConfiguration, 0))
		return
	}
	s.access.Lock()
	s.media = stream
	s.access.Unlock()
	audio, video := stream.Ports()
	s.logger.Info("media stream ports", "video", video, "audio", audio)
	s.sendMedia(stream.PortsMessage())
}

func (s *ServerSession) stopMedia() {
	s.access.Lock()
	stream := s.media
	s.media = nil
	s.access.Unlock()
	if stream == nil {
		return
	}
	_ = stream.Close()
	s.server.releaseMedia(s)
	s.logger.Info("media stream stopped")
}

func (s *ServerSession) handleOffer(message []byte) {
	s.access.Lock()
	stream := s.media
	s.access.Unlock()
	if stream == nil {
		if s.server.mediaTakenBy(s) {
			s.sendMedia(media.ErrorMessage(media.ServerErrorBusy, media.ServerErrorBusySubCode))
		} else {
			s.logger.Debug("media offer without a media stream")
			s.sendMedia(media.ErrorMessage(media.ServerErrorBadConfiguration, 0))
		}
		return
	}
	err := stream.HandleOffer(message)
	if err != nil {
		s.logger.Debug("media offer", "error", err)
	}
}

func (s *ServerSession) answerDisplayRequest() {
	now := time.Now()
	s.access.Lock()
	var rectangles []serverRectangle
	if s.layoutSent && now.Sub(s.lastLayoutAnswer) >= layoutAnswerInterval {
		s.lastLayoutAnswer = now
		rectangles = s.layoutLocked(s.screen)
	}
	s.access.Unlock()
	if len(rectangles) > 0 {
		s.send(framebufferUpdate(rectangles...))
	}
}

func (s *ServerSession) screenChanged(screen ServerScreen) {
	s.access.Lock()
	if s.ending {
		s.access.Unlock()
		return
	}
	resized := screen.Width != s.screen.Width || screen.Height != s.screen.Height
	rectangles := s.layoutLocked(screen)
	rectangles = append(rectangles, s.deviceLocked(screen)...)
	stream := s.media
	s.access.Unlock()
	if len(rectangles) > 0 {
		s.send(framebufferUpdate(rectangles...))
	}
	if resized && stream != nil {
		err := stream.Renegotiate()
		if err != nil {
			s.logger.Debug("restart the media stream", "error", err)
		}
	}
}

func (s *ServerSession) clipboardChanged() {
	s.access.Lock()
	sharing := s.clipboardSharing && !s.ending
	s.access.Unlock()
	if sharing {
		s.send(statusMessage(StatusPasteboardChanged))
	}
}

func (s *ServerSession) receiveClipboard(clipboard *Clipboard) {
	s.access.Lock()
	s.viewerClipboard = clipboard
	s.access.Unlock()
	s.emit(ViewerClipboard{Clipboard: clipboard})
}

func (s *ServerSession) fetchPasteboard(echo uint32) {
	s.access.Lock()
	s.fetchEcho = echo
	if s.fetching {
		s.fetchAgain = true
		s.access.Unlock()
		return
	}
	s.fetching = true
	s.access.Unlock()
	s.server.wg.Go(func() {
		for {
			s.access.Lock()
			echo := s.fetchEcho
			s.fetchAgain = false
			s.access.Unlock()
			s.answerPasteboard(echo)
			s.access.Lock()
			if !s.fetchAgain || s.ending {
				s.fetching = false
				s.access.Unlock()
				return
			}
			s.access.Unlock()
		}
	})
}

func (s *ServerSession) answerPasteboard(echo uint32) {
	ctx, cancel := context.WithTimeout(s.ctx, pasteboardFetchTimeout)
	defer cancel()
	clipboard, err := s.server.handler.Clipboard(ctx)
	var message []byte
	if err == nil {
		message, err = pasteboardMessage(clipboard, echo)
	}
	if err != nil {
		if s.ctx.Err() != nil {
			return
		}
		s.access.Lock()
		clipboard = s.viewerClipboard
		s.access.Unlock()
		if clipboard == nil {
			s.logger.Warn("the device clipboard is unavailable; the viewer's pasteboard request goes unanswered", "error", err)
			return
		}
		s.logger.Warn("the device clipboard is unavailable; answering the viewer with its last known pasteboard", "error", err)
		message, err = pasteboardMessage(clipboard, echo)
		if err != nil {
			s.logger.Warn("pasteboard", "error", err)
			return
		}
	}
	s.access.Lock()
	s.viewerClipboard = clipboard
	s.access.Unlock()
	err = s.outbox.pushWait(message)
	if err != nil {
		s.logger.Debug("send the pasteboard", "error", err)
	}
}
