package libsharing

import (
	"errors"
	"time"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb/media"
)

const (
	CodecHEVC int32 = 1
	CodecH264 int32 = 2
)

const (
	DecoderCodecHEVC int32 = 1 << 0
	DecoderCodecH264 int32 = 1 << 1
)

const (
	MediaErrorNone int32 = 0

	MediaErrorBusy int32 = 1

	MediaErrorRefused int32 = 2

	MediaErrorSilent int32 = 3

	MediaErrorNetwork int32 = 4
)

func classifyMediaError(err error) int32 {
	var serverError *media.ServerError
	switch {
	case err == nil:
		return MediaErrorNone
	case errors.As(err, &serverError):
		if serverError.Type == 1 && serverError.SubCode == 1 {
			return MediaErrorBusy
		}
		return MediaErrorRefused
	case errors.Is(err, media.ErrStreamSilent):
		return MediaErrorSilent
	}
	return MediaErrorNetwork
}

const (
	AudioFromMac int32 = 0

	AudioOff int32 = 1

	AudioToMac int32 = 2
)

const (
	VideoModeAutomatic int32 = 0
	// AVConference mode 5 uses CoreDeviceScreenSharing settings on macOS
	// 27.0.1 (26A434) and 27.2 (26B5101f).
	VideoModeCoreDevice int32 = 5
	// AVConference mode 7 uses RemoteDesktopScreenSharing settings on
	// macOS 27.0.1 (26A434) and 27.2 (26B5101f).
	VideoModeRemoteDesktop int32 = 7
	// AVConference mode 9 uses NearbyScreenSharing settings on macOS
	// 27.0.1 (26A434) and 27.2 (26B5101f).
	VideoModeNearby int32 = 9
)

type HighPerformanceOptions struct {
	SupportedCodecs int32

	PixelFormats int32

	Audio int32

	FrameRate60 bool

	CursorInVideo bool

	Bands int32

	VideoMode int32

	MaximumBitrate int64
}

func NewHighPerformanceOptions() *HighPerformanceOptions {
	return &HighPerformanceOptions{SupportedCodecs: DecoderCodecHEVC, Audio: AudioFromMac, FrameRate60: true, Bands: 1, MaximumBitrate: 60_000_000}
}

const (
	ColorPrimariesBT709  int32 = 1
	ColorPrimariesBT2020 int32 = 9
	ColorPrimariesP3D65  int32 = 12
	TransferBT709        int32 = 1
	TransferSRGB         int32 = 13
	TransferPQ           int32 = 16
	TransferHLG          int32 = 18
	MatrixBT709          int32 = 1
	MatrixBT2020         int32 = 9
)

type VideoFormat struct {
	Codec int32

	Bands int32

	Width  int32
	Height int32

	BitDepth       int32
	ChromaFormat   int32
	FullRange      bool
	ColorPrimaries int32
	Transfer       int32
	Matrix         int32

	HDR           bool
	parameterSets [][]byte
}

func (f *VideoFormat) Len() int32 {
	return int32(len(f.parameterSets))
}

func (f *VideoFormat) Get(index int32) []byte {
	if index < 0 || int(index) >= len(f.parameterSets) {
		return nil
	}
	return f.parameterSets[index]
}

type VideoAccessUnit struct {
	Data []byte

	Timestamp int64
	ClockRate int32

	Sync  bool
	Band  int32
	Bands int32

	FrameID int64

	DecodeOrder int64

	FrameEnd      bool
	ChangedBands  int32
	Discontinuity bool

	Turns int32

	HoldNanoseconds int64
	SpanNanoseconds int64
}

type AudioFormat struct {
	FormatID            int64
	SampleRate          int32
	Channels            int32
	FramesPerPacket     int32
	AudioSpecificConfig []byte
	MagicCookie         []byte
}

type AudioPacket struct {
	Data []byte

	Timestamp int64

	Lost int32
}

func (s *Session) StartHighPerformance(options *HighPerformanceOptions) error {
	offer, err := offerOptions(options)
	if err != nil {
		return err
	}
	client, kept := s.pending(func(setup *sessionSetup) {
		setup.highPerformance = &offer
	})
	if kept {
		return nil
	}
	if client == nil {
		return errors.New("not connected")
	}
	return client.StartMediaStream(offer, &mediaEvents{session: s})
}

func offerOptions(options *HighPerformanceOptions) (media.OfferOptions, error) {
	if options == nil {
		options = NewHighPerformanceOptions()
	}
	offer := media.OfferOptions{
		FrameRate60:    options.FrameRate60,
		CursorInVideo:  options.CursorInVideo,
		Audio:          media.AudioMode(options.Audio),
		TilesPerFrame:  int(options.Bands),
		VideoMode:      media.VideoMode(options.VideoMode),
		PixelFormats:   media.PixelFormatsSDR420,
		MaximumBitrate: options.MaximumBitrate,
	}
	if options.PixelFormats != 0 {
		if options.PixelFormats < 0 || options.PixelFormats&^0xff != 0 {
			return offer, errors.New("invalid decoder pixel formats")
		}
		offer.PixelFormats = media.PixelFormats(options.PixelFormats)
	}
	if options.Bands == 0 {
		offer.TilesPerFrame = 1
	}
	if options.SupportedCodecs == 0 || options.SupportedCodecs&^(DecoderCodecHEVC|DecoderCodecH264) != 0 {
		return offer, errors.New("High Performance needs a supported hardware decoder")
	}
	if options.SupportedCodecs&DecoderCodecHEVC != 0 {
		offer.Codecs = append(offer.Codecs, media.CodecHEVC)
	}
	if options.SupportedCodecs&DecoderCodecH264 != 0 {
		offer.Codecs = append(offer.Codecs, media.CodecH264)
	}
	if len(offer.Codecs) == 1 && offer.Codecs[0] == media.CodecH264 {
		offer.PixelFormats &= media.PixelFormatsSDR420
		if offer.PixelFormats == 0 {
			offer.PixelFormats = media.PixelFormatsSDR420
		}
	}
	return offer, nil
}

func (s *Session) RestartHighPerformance(options *HighPerformanceOptions) error {
	client, err := s.connected()
	if err != nil {
		return err
	}
	go func() {
		_ = client.StopMediaStream()
		err := s.StartHighPerformance(options)
		if err != nil {
			s.handler.OnMediaStopped(classifyMediaError(err), err.Error())
		}
	}()
	return nil
}

func (s *Session) SetHDR(enabled bool) error {
	client, kept := s.pending(func(setup *sessionSetup) {
		setup.hdr = &enabled
	})
	if kept {
		return nil
	}
	if client == nil {
		return errors.New("not connected")
	}
	return client.SetHDR(enabled)
}

func newAudioFormat(format media.AudioFormat) *AudioFormat {
	return &AudioFormat{
		FormatID:            int64(format.FormatID),
		SampleRate:          int32(format.SampleRate),
		Channels:            int32(format.Channels),
		FramesPerPacket:     int32(format.FramesPerPacket),
		AudioSpecificConfig: format.AudioSpecificConfig,
		MagicCookie:         format.MagicCookie,
	}
}

func (s *Session) mediaStream() (*media.Stream, error) {
	client, err := s.connected()
	if err != nil {
		return nil, err
	}
	stream := client.MediaStream()
	if stream == nil {
		return nil, errors.New("the High Performance stream is not running")
	}
	return stream, nil
}

func (s *Session) RequestKeyframe() error {
	stream, err := s.mediaStream()
	if err != nil {
		return err
	}
	stream.RequestKeyframe()
	return nil
}

func (s *Session) SendMicrophoneAudio(data []byte) error {
	stream, err := s.mediaStream()
	if err != nil {
		return err
	}
	return stream.SendAudio(data)
}

type mediaEvents struct {
	session *Session
}

func (e *mediaEvents) VideoFormat(format media.VideoFormat) {
	codec := CodecHEVC
	if format.Codec == media.CodecH264 {
		codec = CodecH264
	}
	color := format.Color
	e.session.handler.OnVideoFormat(&VideoFormat{
		Codec:          codec,
		Bands:          int32(format.Bands),
		Width:          int32(format.Width),
		Height:         int32(format.Height),
		BitDepth:       int32(color.BitDepth),
		ChromaFormat:   int32(color.ChromaFormat),
		FullRange:      color.FullRange,
		ColorPrimaries: int32(color.Primaries),
		Transfer:       int32(color.Transfer),
		Matrix:         int32(color.Matrix),
		HDR:            color.HDR(),
		parameterSets:  format.ParameterSets,
	})
}

func (e *mediaEvents) VideoAccessUnit(unit media.VideoAccessUnit) {
	var hold, span time.Duration
	if !unit.CompletedAt.IsZero() {
		hold = max(time.Since(unit.CompletedAt), 0)
		if !unit.ReceivedAt.IsZero() {
			span = max(unit.CompletedAt.Sub(unit.ReceivedAt), 0)
		}
	}
	e.session.handler.OnVideoAccessUnit(&VideoAccessUnit{
		Data:            unit.Data,
		Timestamp:       unit.Timestamp,
		ClockRate:       int32(unit.ClockRate),
		Sync:            unit.Sync,
		Band:            int32(unit.Band),
		Bands:           int32(unit.Bands),
		FrameID:         unit.FrameID,
		DecodeOrder:     unit.DecodeOrder,
		FrameEnd:        unit.FrameEnd,
		ChangedBands:    int32(unit.ChangedBands),
		Discontinuity:   unit.Discontinuity,
		Turns:           int32(unit.Turns),
		HoldNanoseconds: int64(hold),
		SpanNanoseconds: int64(span),
	})
}

func (e *mediaEvents) AudioFormat(format media.AudioFormat) {
	e.session.handler.OnAudioFormat(newAudioFormat(format))
}

func (e *mediaEvents) AudioPacket(packet media.AudioPacket) {
	e.session.handler.OnAudioPacket(&AudioPacket{Data: packet.Data, Timestamp: packet.Timestamp, Lost: int32(packet.Lost)})
}

func (e *mediaEvents) StreamStopped(err error) {
	message := ""
	if err != nil {
		message = err.Error()
	}
	e.session.handler.OnMediaStopped(classifyMediaError(err), message)
}
