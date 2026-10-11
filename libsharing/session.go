package libsharing

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
	"github.com/nekohasekai/open-screen-sharing/internal/rfb/media"
)

type SessionHandler interface {
	OnStateChanged(state int32, errorCode int32, message string)
	OnServerInfo(info *ServerInfo)

	OnFramebufferResize(width int32, height int32)

	OnFramebufferUpdate(x int32, y int32, width int32, height int32)
	OnCursorImage(cursor *CursorImage)
	OnCursorPosition(x int32, y int32)
	OnDisplays(displays *DisplayList)
	OnClipboard(clipboard *Clipboard)
	OnClipboardProblem(kind int32, message string)
	OnBell()
	OnKeyboardSource(sourceID string, secureInput bool)

	OnStatus(status int32)

	OnPrompt(prompt *Prompt)

	OnVideoFormat(format *VideoFormat)
	OnVideoAccessUnit(unit *VideoAccessUnit)
	OnAudioFormat(format *AudioFormat)
	OnAudioPacket(packet *AudioPacket)

	OnMediaStopped(errorCode int32, message string)

	OnFileTransferProgress(transferID int64, transferred int64, total int64, item string)

	OnFileTransferFinished(transferID int64, name string, errorCode int32, message string)

	OnDropDestination(dragID int64, path string)
	OnRemoteDrag(drag *RemoteDrag)

	OnDeviceInfo(model string, deviceColor string, enclosureColor string)
}

type Session struct {
	options SessionOptions
	handler SessionHandler

	access     sync.Mutex
	client     *rfb.Client
	cancel     context.CancelFunc
	started    bool
	resizing   bool
	attached   *rfb.Framebuffer
	prompts    map[int64]chan int32
	nextPrompt int64
	ending     atomic.Bool
	finished   bool
	setup      sessionSetup

	resizeWidth  int
	resizeHeight int

	pictureEncoding atomic.Int64

	updateRate int32
}

func NewSession(options *SessionOptions, handler SessionHandler) (*Session, error) {
	if options == nil || handler == nil {
		return nil, errors.New("options and handler are required")
	}
	if options.Host == "" {
		return nil, errors.New("host is required")
	}
	if options.Port <= 0 || options.Port > 65535 {
		return nil, fmt.Errorf("port %d", options.Port)
	}
	if options.UpdateRate < 0 {
		return nil, fmt.Errorf("update rate %d", options.UpdateRate)
	}
	return &Session{options: *options, handler: handler, prompts: make(map[int64]chan int32), updateRate: options.UpdateRate}, nil
}

func (s *Session) Connect() error {
	options, err := rfbOptions(&s.options)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.access.Lock()
	if s.started {
		s.access.Unlock()
		cancel()
		return errors.New("session already started")
	}
	s.started = true
	s.cancel = cancel
	s.access.Unlock()
	go s.run(ctx, options)
	return nil
}

func rfbOptions(o *SessionOptions) (rfb.Options, error) {
	options := rfb.Options{
		Address:             net.JoinHostPort(o.Host, strconv.Itoa(int(o.Port))),
		Username:            o.Username,
		Password:            o.Password,
		ClientName:          o.ClientName,
		Quality:             rfb.Quality(o.Quality),
		Mode:                rfb.Mode(o.Mode),
		Clipboard:           o.SharedClipboard,
		DisplayID:           uint32(o.DisplayID),
		DecoderMaxPixelRate: o.DecoderMaxPixelRate,
		MaximumFrameRate:    int(o.MaximumFrameRate),
		AutomaticUpdates:    true,
		UpdateInterval:      updateInterval(o.UpdateRate),
		Dialect:             rfb.Dialect(o.Dialect),
		Logger:              newLogger(),

		AllowUnverifiedLogin: o.AllowUnverifiedLogin,
	}
	switch o.LoginType {
	case LoginAccount:
		options.SecurityTypes = rfb.SecurityAccount
	case LoginRequestControl:
		options.SecurityTypes = rfb.SecurityAskPermission
	case LoginRequestObserve:
		options.SecurityTypes = rfb.SecurityObserveRequest
	case LoginVNCPassword:
		options.SecurityTypes = []rfb.SecurityType{rfb.SecurityVNCPassword}
	case LoginNone:
		options.SecurityTypes = []rfb.SecurityType{rfb.SecurityNone}
	default:
		return options, fmt.Errorf("login type %d", o.LoginType)
	}
	if options.Quality < rfb.QualityAdaptive || options.Quality > rfb.QualityLow {
		return options, fmt.Errorf("quality %d", o.Quality)
	}
	if options.Mode < rfb.ModeObserve || options.Mode > rfb.ModeExclusiveControl {
		return options, fmt.Errorf("mode %d", o.Mode)
	}
	switch options.MaximumFrameRate {
	case 0, 30, 60:
	default:
		return options, fmt.Errorf("maximum frame rate %d", o.MaximumFrameRate)
	}
	return options, nil
}

func (s *Session) run(ctx context.Context, options rfb.Options) {
	err := checkOwnScreen(ctx, s.options.Host, s.options.Port)
	if err != nil {
		s.finish(err)
		return
	}
	events := &sessionEvents{session: s}
	options.Prepare = s.prepare
	client, err := rfb.Dial(ctx, options, events)
	if err != nil {
		s.finish(err)
		return
	}
	s.access.Lock()
	cancelled := ctx.Err() != nil
	if !cancelled {
		s.client = client
	}
	interval := updateInterval(s.updateRate)
	late := s.setup
	s.setup = sessionSetup{}
	s.access.Unlock()
	if cancelled {
		client.Close()
		s.finish(context.Canceled)
		return
	}

	if interval != options.UpdateInterval {
		client.SetUpdateInterval(interval)
	}
	s.apply(client, late)
	s.handler.OnServerInfo(newServerInfo(client.Info()))
	s.handler.OnStateChanged(StateConnected, ErrorNone, "")
	err = client.Run()
	if err == nil && ctx.Err() != nil {
		err = context.Canceled
	}
	s.finish(err)
}

type sessionSetup struct {
	hdr             *bool
	display         *virtualDisplay
	highPerformance *media.OfferOptions
}

type virtualDisplay struct {
	width         int32
	height        int32
	density       float64
	maximumWidth  int32
	maximumHeight int32
}

func (s *Session) pending(keep func(*sessionSetup)) (*rfb.Client, bool) {
	s.access.Lock()
	defer s.access.Unlock()
	if s.client == nil && !s.finished {
		keep(&s.setup)
		return nil, true
	}
	return s.client, false
}

func (s *Session) prepare(client *rfb.Client) error {
	s.access.Lock()
	setup := s.setup
	s.setup = sessionSetup{}
	s.access.Unlock()
	s.apply(client, setup)
	return nil
}

func (s *Session) apply(client *rfb.Client, setup sessionSetup) {
	info := newServerInfo(client.Info())
	if setup.hdr != nil && info.SupportsVirtualDisplay {
		err := client.SetHDR(*setup.hdr)
		if err != nil {
			client.Logger().Warn("apply the HDR setting made before connecting", "error", err)
		}
	}
	if display := setup.display; display != nil && info.SupportsVirtualDisplay {
		err := client.SetVirtualDisplay(int(display.width), int(display.height), display.density, int(display.maximumWidth), int(display.maximumHeight))
		if err != nil {
			client.Logger().Warn("apply the virtual display requested before connecting", "error", err)
		}
	}
	if setup.highPerformance != nil && info.SupportsHighPerformance && client.MediaStream() == nil {
		err := client.StartMediaStream(*setup.highPerformance, &mediaEvents{session: s})
		if err != nil {
			s.handler.OnMediaStopped(classifyMediaError(err), err.Error())
		}
	}
}

func (s *Session) finish(err error) {
	s.access.Lock()
	s.finished = true
	s.setup = sessionSetup{}
	s.client = nil
	s.attached = nil
	s.cancelPromptsLocked()
	s.access.Unlock()
	code, message := classifyError(err)
	if s.ending.Load() && (code == ErrorNetwork || code == ErrorProtocol) {
		code = ErrorSessionEnded
	}
	s.handler.OnStateChanged(StateDisconnected, code, message)
}

func classifyError(err error) (int32, string) {
	var ownScreen *ownScreenError
	switch {
	case err == nil:
		return ErrorNone, ""
	case errors.Is(err, context.Canceled), errors.Is(err, rfb.ErrSessionCancelled):
		return ErrorCancelled, err.Error()
	case errors.Is(err, rfb.ErrAuthenticationFailed):
		return ErrorAuthentication, err.Error()
	case errors.Is(err, rfb.ErrPermissionDenied):
		return ErrorPermissionDenied, err.Error()
	case errors.Is(err, rfb.ErrUnsupportedServer):
		return ErrorUnsupportedServer, err.Error()
	case errors.Is(err, rfb.ErrUnverifiedServer):
		return ErrorUnverifiedServer, err.Error()
	case errors.As(err, &ownScreen):
		return ErrorOwnScreen, err.Error()
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return ErrorNetwork, err.Error()
	}
	var dnsError *net.DNSError
	if errors.As(err, &dnsError) && dnsError.IsNotFound {
		return ErrorHostNotFound, err.Error()
	}
	var networkError net.Error
	var operationError *net.OpError
	if errors.As(err, &networkError) || errors.As(err, &operationError) {
		return ErrorNetwork, err.Error()
	}
	return ErrorProtocol, err.Error()
}

func (s *Session) cancelPromptsLocked() {
	for id, answer := range s.prompts {
		close(answer)
		delete(s.prompts, id)
	}
}

func (s *Session) Disconnect() {
	s.access.Lock()
	cancel, client := s.cancel, s.client
	s.cancelPromptsLocked()
	s.access.Unlock()
	if cancel != nil {
		cancel()
	}
	if client != nil {
		go client.Close()
	}
}

func (s *Session) AttachFramebuffer(address int64, width int32, height int32, bytesPerRow int32) error {
	if address == 0 || width <= 0 || height <= 0 || bytesPerRow < width*4 {
		return fmt.Errorf("framebuffer %dx%d with %d bytes per row", width, height, bytesPerRow)
	}
	length := int(bytesPerRow) * int(height)

	base := *(*unsafe.Pointer)(unsafe.Pointer(&address))
	framebuffer := &rfb.Framebuffer{
		Pix:    unsafe.Slice((*byte)(base), length),
		Stride: int(bytesPerRow),
		Width:  int(width),
		Height: int(height),
	}
	s.access.Lock()
	if s.resizing {
		if framebuffer.Width != s.resizeWidth || framebuffer.Height != s.resizeHeight {
			s.access.Unlock()
			return fmt.Errorf("framebuffer is %dx%d, the screen is being resized to %dx%d", width, height, s.resizeWidth, s.resizeHeight)
		}
		s.attached = framebuffer
		s.access.Unlock()
		return nil
	}
	client := s.client
	s.access.Unlock()
	if client == nil {
		return errors.New("not connected")
	}
	return client.SetFramebuffer(framebuffer)
}

func (s *Session) connected() (*rfb.Client, error) {
	s.access.Lock()
	defer s.access.Unlock()
	if s.client == nil {
		return nil, errors.New("not connected")
	}
	return s.client, nil
}

func (s *Session) SendPointer(x int32, y int32, buttons int32) error {
	client, err := s.connected()
	if err != nil {
		return err
	}
	return client.SendPointer(int(x), int(y), uint8(buttons))
}

func (s *Session) SendScroll(x int32, y int32, deltaX float64, deltaY float64) error {
	client, err := s.connected()
	if err != nil {
		return err
	}
	return client.SendScroll(int(x), int(y), deltaX, deltaY)
}

func (s *Session) SendKey(keysym int32, down bool) error {
	client, err := s.connected()
	if err != nil {
		return err
	}
	return client.SendKey(uint32(keysym), down)
}

func (s *Session) SendKeyCode(keyCode int32, keyboardType int32, keysym int32, down bool) error {
	client, err := s.connected()
	if err != nil {
		return err
	}
	return client.SendKeyCode(uint16(keyCode), rfb.KeyboardType(keyboardType), uint32(keysym), down)
}

func (s *Session) SendText(text string) error {
	client, err := s.connected()
	if err != nil {
		return err
	}
	return client.SendText(text)
}

func (s *Session) PasteText(text string) error {
	client, err := s.connected()
	if err != nil {
		return err
	}
	return client.PasteText(text)
}

func (s *Session) SetClipboard(clipboard *Clipboard) error {
	client, err := s.connected()
	if err != nil {
		return err
	}
	if clipboard == nil {
		clipboard = NewClipboard()
	}
	return client.SetClipboard(clipboard.toRFB())
}

func (s *Session) SetSharedClipboard(enabled bool) error {
	client, err := s.connected()
	if err != nil {
		return err
	}
	return client.SetSharedClipboard(enabled)
}

func (s *Session) RequestClipboard() error {
	client, err := s.connected()
	if err != nil {
		return err
	}
	return client.RequestClipboard()
}

func (s *Session) SetMode(mode int32) error {
	if rfb.Mode(mode) < rfb.ModeObserve || rfb.Mode(mode) > rfb.ModeExclusiveControl {
		return fmt.Errorf("mode %d", mode)
	}
	client, err := s.connected()
	if err != nil {
		return err
	}
	return client.SetMode(rfb.Mode(mode))
}

func (s *Session) SelectDisplay(displayID int64) error {
	client, err := s.connected()
	if err != nil {
		return err
	}
	return client.SelectDisplay(uint32(displayID))
}

func (s *Session) SetQuality(quality int32) error {
	if rfb.Quality(quality) < rfb.QualityAdaptive || rfb.Quality(quality) > rfb.QualityLow {
		return fmt.Errorf("quality %d", quality)
	}
	client, err := s.connected()
	if err != nil {
		return err
	}
	return client.SetQuality(rfb.Quality(quality))
}

func (s *Session) SetUpdateRate(rate int32) error {
	if rate < 0 {
		return fmt.Errorf("update rate %d", rate)
	}
	s.access.Lock()
	s.updateRate = rate
	client := s.client
	s.access.Unlock()
	if client == nil {
		return nil
	}
	return client.SetUpdateInterval(updateInterval(rate))
}

func updateInterval(rate int32) time.Duration {
	if rate <= 0 {
		return 0
	}
	return time.Second / time.Duration(rate)
}

func (s *Session) SetScale(factor float64) error {
	client, err := s.connected()
	if err != nil {
		return err
	}
	return client.SetScale(factor)
}

func (s *Session) SetVirtualDisplay(width int32, height int32, density float64, maximumWidth int32, maximumHeight int32) error {
	err := rfb.CheckVirtualDisplay(int(width), int(height), density)
	if err != nil {
		return err
	}
	client, kept := s.pending(func(setup *sessionSetup) {
		setup.display = &virtualDisplay{width: width, height: height, density: density, maximumWidth: maximumWidth, maximumHeight: maximumHeight}
	})
	if kept {
		return nil
	}
	if client == nil {
		return errors.New("not connected")
	}
	return client.SetVirtualDisplay(int(width), int(height), density, int(maximumWidth), int(maximumHeight))
}

func (s *Session) SelectKeyboardSource(sourceID string) error {
	client, err := s.connected()
	if err != nil {
		return err
	}
	return client.SelectKeyboardSource(sourceID)
}

func (s *Session) RequestRefresh() error {
	client, err := s.connected()
	if err != nil {
		return err
	}
	return client.RequestRefresh()
}

func (s *Session) AnswerPrompt(promptID int64, answer int32) error {
	s.access.Lock()
	channel, found := s.prompts[promptID]
	delete(s.prompts, promptID)
	s.access.Unlock()
	if !found {
		return fmt.Errorf("no prompt %d", promptID)
	}
	channel <- answer
	return nil
}

type sessionEvents struct {
	session *Session
}

func (e *sessionEvents) StateChanged(state rfb.State) {
	switch state {
	case rfb.StateConnecting:
		e.session.handler.OnStateChanged(StateConnecting, ErrorNone, "")
	case rfb.StateAuthenticating:
		e.session.handler.OnStateChanged(StateAuthenticating, ErrorNone, "")
	case rfb.StateWaitingForApproval:
		e.session.handler.OnStateChanged(StateWaitingForApproval, ErrorNone, "")
	case rfb.StateChoosingSession:
		e.session.handler.OnStateChanged(StateChoosingSession, ErrorNone, "")
	}
}

func (e *sessionEvents) ResizeFramebuffer(width, height int) (*rfb.Framebuffer, error) {
	s := e.session
	s.access.Lock()
	s.resizing = true
	s.resizeWidth, s.resizeHeight = width, height
	s.attached = nil
	s.access.Unlock()
	s.handler.OnFramebufferResize(int32(width), int32(height))
	s.access.Lock()
	framebuffer := s.attached
	s.resizing = false
	s.attached = nil
	s.access.Unlock()

	if framebuffer == nil {
		return nil, fmt.Errorf("no %dx%d framebuffer was attached during OnFramebufferResize", width, height)
	}
	return framebuffer, nil
}

func (e *sessionEvents) FramebufferUpdated(update rfb.Update) {
	var encoding rfb.Encoding
	var largest int
	for _, rectangle := range update.Rectangles {
		area := rectangle.Width * rectangle.Height
		if area > largest {
			encoding, largest = rectangle.Encoding, area
		}
	}
	if largest > 0 {
		e.session.pictureEncoding.Store(int64(encoding) + 1)
	}
	dirty := update.Dirty
	e.session.handler.OnFramebufferUpdate(int32(dirty.X), int32(dirty.Y), int32(dirty.Width), int32(dirty.Height))
}

func (e *sessionEvents) CursorChanged(cursor *rfb.Cursor) {
	e.session.handler.OnCursorImage(&CursorImage{
		Width:    int32(cursor.Width),
		Height:   int32(cursor.Height),
		HotspotX: int32(cursor.HotspotX),
		HotspotY: int32(cursor.HotspotY),
		Pixels:   cursor.Pixels,
	})
}

func (e *sessionEvents) CursorMoved(x, y int) {
	e.session.handler.OnCursorPosition(int32(x), int32(y))
}

func (e *sessionEvents) DisplaysChanged(layout *rfb.DisplayLayout) {
	e.session.handler.OnDisplays(newDisplayList(layout))
}

func (e *sessionEvents) ClipboardReceived(clipboard *rfb.Clipboard) {
	e.session.handler.OnClipboard(newClipboard(clipboard))
}

func (e *sessionEvents) ClipboardFailed(problem rfb.ClipboardProblem, err error) {
	e.session.handler.OnClipboardProblem(int32(problem), err.Error())
}

func (e *sessionEvents) Bell() {
	e.session.handler.OnBell()
}

func (e *sessionEvents) KeyboardSourceChanged(source rfb.KeyboardSource) {
	e.session.handler.OnKeyboardSource(source.ID, source.SecureInput)
}

func (e *sessionEvents) StatusReceived(status rfb.Status) {
	switch status {
	case rfb.StatusHeartbeat, rfb.StatusPasteboardChanged, rfb.StatusPasteboardNeedsData:
		return
	case rfb.StatusSessionEnding:
		e.session.ending.Store(true)
	}
	e.session.handler.OnStatus(int32(status))
}

func (e *sessionEvents) DeviceInfoReceived(info rfb.DeviceInfo) {
	e.session.handler.OnDeviceInfo(info.Model, info.DeviceColor, info.EnclosureColor)
}

func (e *sessionEvents) RemoteDragChanged(drag *rfb.RemoteDrag) {
	if drag == nil {
		e.session.handler.OnRemoteDrag(nil)
		return
	}
	e.session.handler.OnRemoteDrag(&RemoteDrag{Image: drag.Image, items: drag.Items})
}

func (e *sessionEvents) DropDestinationReceived(id uint32, path string) {
	e.session.handler.OnDropDestination(int64(id), path)
}

func (e *sessionEvents) ChooseSession(request rfb.SessionRequest) (rfb.SessionChoice, error) {
	s := e.session
	prompt := &Prompt{Kind: PromptChooseSession, ConsoleUser: request.ConsoleUser}
	for _, choice := range request.Choices {
		switch choice {
		case rfb.SessionShareConsole:
			prompt.AllowsShareConsole = true
		case rfb.SessionOwn:
			prompt.AllowsOwnSession = true
		}
	}
	answer := make(chan int32, 1)
	s.access.Lock()
	s.nextPrompt++
	prompt.PromptID = s.nextPrompt
	s.prompts[prompt.PromptID] = answer
	s.access.Unlock()
	s.handler.OnPrompt(prompt)
	choice, open := <-answer
	if !open {
		return rfb.SessionCancel, rfb.ErrSessionCancelled
	}
	switch choice {
	case SessionChoiceShareConsole:
		return rfb.SessionShareConsole, nil
	case SessionChoiceOwnSession:
		return rfb.SessionOwn, nil
	default:
		return rfb.SessionCancel, rfb.ErrSessionCancelled
	}
}
