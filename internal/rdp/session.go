package rdp

import (
	"encoding/binary"
	"log/slog"
	"sync/atomic"

	E "github.com/sagernet/sing/common/exceptions"
)

const (
	securityInfoPacket        = 0x0040
	securityLicensePacket     = 0x0080
	securityRedirectionPacket = 0x0400

	shareControlHeaderLength = 6
	shareProtocolVersion     = 0x10
	shareTypeMask            = 0x0f
	shareDemandActive        = 0x1
	shareConfirmActive       = 0x3
	shareDeactivateAll       = 0x6
	shareData                = 0x7
	shareRedirect            = 0xa
	shareFlowMarker          = 0x8000
	shareFlowPDULength       = 6
	shareStreamLow           = 1
	shareServerChannel       = 1002

	shareDataHeaderLength  = 12
	packetCompressed       = 0x20
	packetCompressionFlags = 0xe0

	pduUpdate             = 2
	pduControl            = 20
	pduPointer            = 27
	pduInput              = 28
	pduSynchronize        = 31
	pduRefreshRect        = 33
	pduPlaySound          = 34
	pduSuppressOutput     = 35
	pduSaveSessionInfo    = 38
	pduFontList           = 39
	pduFontMap            = 40
	pduKeyboardIndicators = 41
	pduSetErrorInfo       = 47
	pduFrameAcknowledge   = 56

	controlRequestControl = 1
	controlCooperate      = 4
	synchronizeMessage    = 1
	fontListFirstLast     = 0x0003
	fontListEntrySize     = 50

	infoMouse              = 0x00000001
	infoDisableCtrlAltDel  = 0x00000002
	infoAutoLogon          = 0x00000008
	infoUnicode            = 0x00000010
	infoMaximizeShell      = 0x00000020
	infoLogonNotify        = 0x00000040
	infoCompression        = 0x00000080
	infoEnableWindowsKey   = 0x00000100
	infoCompressionRDP61   = 0x00000600
	infoRemoteConsoleAudio = 0x00002000
	infoLogonErrors        = 0x00010000
	infoMouseHasWheel      = 0x00020000
	addressFamilyINET      = 2
	timeZoneInformationLen = 172

	sessionInfoLogon         = 0
	sessionInfoLogonLong     = 1
	sessionInfoPlainNotify   = 2
	sessionInfoExtended      = 3
	logonExtendedCookie      = 0x00000001
	logonExtendedErrors      = 0x00000002
	logonMessageNoPermission = 0xfffffffa
	logonMessageTerminate    = 0xfffffffd
)

const clientDirectory = "C:\\Windows\\System32\\mstscax.dll"

type session struct {
	mcs      *mcsLayer
	graphics *graphics
	input    *inputQueue
	display  *displayChannel
	events   Events
	logger   *slog.Logger
	changed  func()
	license  *licenseState

	keyboardLayout uint32

	shareID    atomic.Uint32
	active     atomic.Bool
	suppressed atomic.Bool
	loggedOn   atomic.Bool
	inputFlags atomic.Uint32
	serverCaps map[uint16][]byte
	errorInfo  uint32
}

func clientInfo(opts Options) []byte {
	b := binary.LittleEndian.AppendUint32(nil, securityInfoPacket)
	b = binary.LittleEndian.AppendUint32(b, 0)
	flags := uint32(infoMouse | infoDisableCtrlAltDel | infoUnicode | infoMaximizeShell | infoLogonNotify |
		infoCompression | infoEnableWindowsKey | infoCompressionRDP61 | infoLogonErrors | infoMouseHasWheel)
	if opts.Username != "" {
		flags |= infoAutoLogon
	}
	if !opts.Audio {
		flags |= infoRemoteConsoleAudio
	}
	b = binary.LittleEndian.AppendUint32(b, flags)
	fields := [][]byte{utf16le(nil, opts.Domain), utf16le(nil, opts.Username), utf16le(nil, opts.Password), nil, nil}
	for _, s := range fields {
		b = binary.LittleEndian.AppendUint16(b, uint16(len(s)))
	}
	for _, s := range fields {
		b = append(b, s...)
		b = binary.LittleEndian.AppendUint16(b, 0)
	}
	b = binary.LittleEndian.AppendUint16(b, addressFamilyINET)
	for _, value := range []string{"0.0.0.0", clientDirectory} {
		field := binary.LittleEndian.AppendUint16(utf16le(nil, value), 0)
		b = binary.LittleEndian.AppendUint16(b, uint16(len(field)))
		b = append(b, field...)
	}
	b = zeros(b, timeZoneInformationLen)
	b = binary.LittleEndian.AppendUint32(b, 0)
	b = binary.LittleEndian.AppendUint32(b, 0)
	return binary.LittleEndian.AppendUint16(b, 0)
}

func (s *session) sharePacket(kind uint16, payload []byte) ([]byte, error) {
	b := binary.LittleEndian.AppendUint16(nil, uint16(len(payload)+shareControlHeaderLength))
	b = binary.LittleEndian.AppendUint16(b, shareProtocolVersion|kind)
	b = binary.LittleEndian.AppendUint16(b, s.mcs.userID)
	return s.mcs.packet(s.mcs.global, append(b, payload...))
}

func (s *session) dataPacket(kind byte, payload []byte) ([]byte, error) {
	b := binary.LittleEndian.AppendUint32(nil, s.shareID.Load())
	b = append(b, 0, shareStreamLow)
	b = binary.LittleEndian.AppendUint16(b, uint16(len(payload)))
	b = append(b, kind, 0)
	b = binary.LittleEndian.AppendUint16(b, 0)
	return s.sharePacket(shareData, append(b, payload...))
}

func (s *session) sendData(kind byte, payload []byte) error {
	packet, err := s.dataPacket(kind, payload)
	if err != nil {
		return err
	}
	return s.mcs.outbox.Push(packet, false)
}

func (s *session) sendInput(events []inputEvent, motion bool) error {
	if !s.active.Load() {
		return nil
	}
	var packets []byte
	for len(events) > 0 {
		count := min(len(events), maximumInputEvents)
		packet, err := s.dataPacket(pduInput, encodeInputEvents(events[:count]))
		if err != nil {
			return err
		}
		packets = append(packets, packet...)
		events = events[count:]
	}
	return s.mcs.outbox.Push(packets, motion)
}

func (s *session) receive(payload []byte) error {
	if len(payload) < 4 {
		return E.New("rdp: truncated security header: ", len(payload), " of 4 bytes")
	}
	flags := binary.LittleEndian.Uint16(payload)
	if binary.LittleEndian.Uint16(payload[2:]) == 0 {
		switch {
		case flags&securityLicensePacket != 0:
			response, err := s.license.receive(payload[4:])
			if err != nil || response == nil {
				return err
			}
			return s.mcs.send(s.mcs.global, append(binary.LittleEndian.AppendUint32(nil, securityLicensePacket), response...))
		case flags&securityRedirectionPacket != 0:
			return E.New("rdp: server requested session redirection")
		}
	}
	r := &reader{data: payload}
	for r.remaining() > 0 && r.err == nil {
		length := int(r.u16())
		if length == shareFlowMarker {
			r.take(shareFlowPDULength)
			continue
		}
		if length < shareControlHeaderLength {
			return E.New("rdp: share control PDU declares ", length, " bytes, less than its ", shareControlHeaderLength, "-byte header")
		}
		kind := r.u16() & shareTypeMask
		source := r.u16()
		body := r.take(length - shareControlHeaderLength)
		if r.err != nil {
			return r.err
		}
		var err error
		switch kind {
		case shareDemandActive:
			err = s.demandActive(source, body)
		case shareDeactivateAll:
			s.active.Store(false)
		case shareData:
			err = s.receiveData(body)
		case shareRedirect:
			err = E.New("rdp: server requested session redirection")
		}
		if err != nil {
			return err
		}
	}
	return r.err
}

func (s *session) demandActive(source uint16, payload []byte) error {
	r := &reader{data: payload}
	shareID := r.u32()
	descriptorLength := int(r.u16())
	combinedLength := int(r.u16())
	r.take(descriptorLength)
	if combinedLength < 4 {
		return E.New("rdp: Demand Active capabilities declare ", combinedLength, " bytes, less than their 4-byte header")
	}
	caps := r.sub(combinedLength)
	count := int(caps.u16())
	caps.take(2)
	width, height := int(s.graphics.width.Load()), int(s.graphics.height.Load())
	serverCaps := make(map[uint16][]byte, count)
	for range count {
		kind := caps.u16()
		length := int(caps.u16())
		if length < 4 {
			return E.New("rdp: capability set ", kind, " declares ", length, " bytes, less than its 4-byte header")
		}
		data := caps.take(length - 4)
		if caps.err != nil {
			return caps.err
		}
		serverCaps[kind] = append([]byte(nil), data...)
		switch kind {
		case capabilityBitmap:
			if len(data) < 12 {
				return E.New("rdp: truncated bitmap capability set: ", len(data), " of 12 bytes")
			}
			width = int(binary.LittleEndian.Uint16(data[8:]))
			height = int(binary.LittleEndian.Uint16(data[10:]))
		case capabilityInput:
			if len(data) < 2 {
				return E.New("rdp: truncated input capability set: ", len(data), " of 2 bytes")
			}
			s.inputFlags.Store(uint32(binary.LittleEndian.Uint16(data)))
		}
	}
	if r.err != nil {
		return r.err
	}
	s.active.Store(false)
	s.shareID.Store(shareID)
	s.serverCaps = serverCaps
	err := s.graphics.resize(width, height)
	if err != nil {
		return err
	}
	s.changed()
	countCaps, capabilities := s.capabilities()
	sourceDescriptor := []byte("MSTSC\x00")
	b := binary.LittleEndian.AppendUint32(nil, shareID)
	b = binary.LittleEndian.AppendUint16(b, shareServerChannel)
	b = binary.LittleEndian.AppendUint16(b, uint16(len(sourceDescriptor)))
	b = binary.LittleEndian.AppendUint16(b, uint16(len(capabilities)+4))
	b = append(b, sourceDescriptor...)
	b = binary.LittleEndian.AppendUint16(b, countCaps)
	b = binary.LittleEndian.AppendUint16(b, 0)
	b = append(b, capabilities...)
	packet, err := s.sharePacket(shareConfirmActive, b)
	if err != nil {
		return err
	}
	synchronize, err := s.dataPacket(pduSynchronize, uint16s(synchronizeMessage, source))
	if err != nil {
		return err
	}
	packet = append(packet, synchronize...)
	for _, action := range []uint16{controlCooperate, controlRequestControl} {
		control, err := s.dataPacket(pduControl, binary.LittleEndian.AppendUint32(uint16s(action, 0), 0))
		if err != nil {
			return err
		}
		packet = append(packet, control...)
	}
	synchronizeLocks, err := s.dataPacket(pduInput, encodeInputEvents(s.input.reactivate(s.inputFlags.Load()&inputFlagUnicode != 0)))
	if err != nil {
		return err
	}
	packet = append(packet, synchronizeLocks...)
	fonts, err := s.dataPacket(pduFontList, uint16s(0, 0, fontListFirstLast, fontListEntrySize))
	if err != nil {
		return err
	}
	return s.mcs.outbox.Push(append(packet, fonts...), false)
}

func (s *session) receiveData(payload []byte) error {
	r := &reader{data: payload}
	r.take(8)
	kind := r.u8()
	compressedType := r.u8()
	compressedLength := int(r.u16())
	data := r.rest()
	if r.err != nil {
		return r.err
	}
	if compressedType&packetCompressionFlags != 0 {
		if compressedType&packetCompressed != 0 && compressedLength != 0 && compressedLength != len(data)+shareControlHeaderLength+shareDataHeaderLength {
			return E.New("rdp: compressed share data declares ", compressedLength, " bytes, got ", len(data)+shareControlHeaderLength+shareDataHeaderLength)
		}
		var err error
		data, err = s.mcs.bulk.Decompress(data, compressedType)
		if err != nil {
			return E.Cause(err, "rdp: decompress share data")
		}
	}
	switch kind {
	case pduUpdate:
		return s.graphics.processSlowPathUpdate(data)
	case pduPointer:
		s.graphics.processPointer(data)
	case pduFontMap:
		s.active.Store(true)
		if s.suppressed.Load() {
			return s.sendData(pduSuppressOutput, []byte{suppressDisplayUpdates, 0, 0, 0})
		}
	case pduSaveSessionInfo:
		return s.receiveSessionInfo(data)
	case pduSetErrorInfo:
		if len(data) < 4 {
			return E.New("rdp: truncated Set Error Info PDU: ", len(data), " of 4 bytes")
		}
		s.errorInfo = binary.LittleEndian.Uint32(data)
		if s.errorInfo != 0 {
			s.logger.Debug("server error info", "code", s.errorInfo)
		}
	case pduKeyboardIndicators:
		if len(data) < 4 {
			return E.New("rdp: truncated Set Keyboard Indicators PDU: ", len(data), " of 4 bytes")
		}
		locks := binary.LittleEndian.Uint16(data[2:])
		s.input.setLocks(lockKeys(locks) & (scrollLock | numLock | capsLock | kanaLock))
		s.logger.Debug("keyboard indicators", "locks", locks)
	case pduPlaySound:
		s.events.Bell()
	}
	return nil
}

func (s *session) receiveSessionInfo(payload []byte) error {
	r := &reader{data: payload}
	switch r.u32() {
	case sessionInfoLogon, sessionInfoLogonLong, sessionInfoPlainNotify:
		if !s.loggedOn.Swap(true) {
			s.display.loggedOn()
			s.changed()
		}
	case sessionInfoExtended:
		r.take(2)
		fields := r.u32()
		if fields&logonExtendedCookie != 0 {
			r.take(int(r.u32()))
		}
		if fields&logonExtendedErrors == 0 {
			return nil
		}
		r.take(4)
		notification, data := r.u32(), r.u32()
		if r.err != nil {
			s.logger.Debug("parse logon notification", "error", r.err)
			return nil
		}
		switch notification {
		case logonMessageTerminate:
			return E.Extend(ErrSessionEnded, "Windows ended the sign-in")
		case logonMessageNoPermission:
			return ErrRemoteLogonDenied
		}
		s.logger.Debug("logon notification", "type", notification, "data", data)
	}
	return nil
}
