package media

import (
	"bytes"
	"compress/zlib"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"time"
)

const (
	EncodingMediaStream = 1010

	ClientMessageMediaStream = 0x1c

	// ScreenSharing copies 46 bytes for each negotiated SRTP key
	// (macOS 26A434, RFBMediaStreamServerConfiguration).
	masterKeyLength = 46

	negotiatorModeRemoteMic                  = 4
	negotiatorModeCoreDeviceScreenSharing    = 5
	negotiatorModeRemoteDesktopScreenSharing = 7
	negotiatorModeRemoteDesktopSystemAudio   = 8
	negotiatorModeNearbyScreenSharing        = 9

	hdrModeStandard  = 0
	hdrModeReference = 3

	mediaStreamMessageVersion = 3
	messageFlagFrameRate60    = 1 << 0
	messageFlagNoCursor       = 1 << 2

	accessNetworkTypeViewer = 1

	// AVConference native mode-7 offers on macOS 27.0.1 (26A434) and 27.2
	// (26B5101f) carry blob version 2 and user agent "Viceroy 1.7.0".
	negotiatorUserAgent   = "Viceroy 1.7.0"
	negotiatorBlobVersion = 2

	// The same native mode-7 offers use transport 1, operations 1/2, format bitmap
	// 0xc3c3, and these codec FLS prefixes before the display-dependent AR/XR.
	videoRuleTransport       = 1
	videoRuleOperationEncode = 1
	videoRuleOperationDecode = 2
	screenFormats            = 0xc3c3

	featureListAspectRatios = "AR:16/9,5/8;XR:16/9,5/8;"
	featureListH264         = "FLS;MS:-1;LF:-1;LTR;CABAC;POS:0;EOD:1;HTS:2;RR:3;SW:4;" + featureListAspectRatios
	featureListHEVC         = "FLS;LF:-1;POS:5;EOD:1;HTS:2;RR:3;POSE:4;SW:0;" + featureListAspectRatios
	// AVConference _VCVideoParameterSets_DefaultSupportedSets returns 1 for PT 123
	// and 14 for PT 100 (macOS 27.0.1, 26A434, 0x1c1195f18); both native offers agree.
	parameterSetsH264 = 1
	parameterSetsHEVC = 14

	defaultDeviceModel          = "Mac14,3"
	defaultOSBuild              = "25G72"
	endpointAVConferenceVersion = "2215.5.1"
	endpointClientVersion       = 1
	minimumVideoBitrate         = 5_000_000
	maximumVideoBitrate         = 60_000_000
)

type AudioMode int

const (
	AudioFromServer AudioMode = iota
	AudioOff
	AudioToServer
)

// AVConference maps modes 5/7/9 to CoreDeviceScreenSharing, RemoteDesktopScreenSharing,
// and NearbyScreenSharing settings (macOS 27.0.1 26A434 and 27.2 26B5101f).
type VideoMode int

const (
	VideoModeAutomatic     VideoMode = 0
	VideoModeCoreDevice    VideoMode = negotiatorModeCoreDeviceScreenSharing
	VideoModeRemoteDesktop VideoMode = negotiatorModeRemoteDesktopScreenSharing
	VideoModeNearby        VideoMode = negotiatorModeNearbyScreenSharing
)

func (m VideoMode) String() string {
	switch m {
	case VideoModeAutomatic:
		return "automatic"
	case VideoModeCoreDevice:
		return "CoreDevice"
	case VideoModeRemoteDesktop:
		return "RemoteDesktop"
	case VideoModeNearby:
		return "Nearby"
	default:
		return fmt.Sprint("mode(", int(m), ")")
	}
}

type Codec int

const (
	CodecHEVC Codec = iota + 1
	CodecH264
)

func (c Codec) String() string {
	switch c {
	case CodecHEVC:
		return "HEVC"
	case CodecH264:
		return "H.264"
	default:
		return fmt.Sprint("codec(", int(c), ")")
	}
}

type PixelFormats uint32

const (
	PixelFormat420FullRange       PixelFormats = 1 << 0
	PixelFormat420VideoRange      PixelFormats = 1 << 1
	PixelFormat420TenBit          PixelFormats = 1 << 2
	PixelFormat444FullRange       PixelFormats = 1 << 3
	PixelFormat444VideoRange      PixelFormats = 1 << 4
	PixelFormat444TenBitFullRange PixelFormats = 1 << 5
	PixelFormat420Alpha           PixelFormats = 1 << 6
	PixelFormat444Alpha16         PixelFormats = 1 << 7

	PixelFormatsAll       PixelFormats = 0xff
	PixelFormatsSDR420    PixelFormats = PixelFormat420FullRange | PixelFormat420VideoRange
	PixelFormatsChroma420 PixelFormats = PixelFormat420FullRange | PixelFormat420VideoRange | PixelFormat420TenBit
)

type OfferOptions struct {
	Codecs              []Codec
	FrameRate60         bool
	DecoderMaxPixelRate int64
	MaximumFrameRate    int
	MaximumBitrate      int64
	CursorInVideo       bool
	PixelFormats        PixelFormats
	TilesPerFrame       int
	VideoMode           VideoMode
	Audio               AudioMode
	Width               int
	Height              int
	DeviceModel         string
	OSBuild             string
}

type Offer struct {
	options           OfferOptions
	videoMode         int64
	sessionID         [16]byte
	audioCallID       string
	videoCallID       string
	audioSend         [masterKeyLength]byte
	audioReceive      [masterKeyLength]byte
	videoSend         [masterKeyLength]byte
	videoReceive      [masterKeyLength]byte
	audioSSRC         uint32
	videoSSRC         uint32
	hdr               atomic.Bool
	decoderDimensions atomic.Uint64
}

func NewOffer(options OfferOptions) (*Offer, error) {
	if len(options.Codecs) == 0 {
		options.Codecs = []Codec{CodecHEVC}
	}
	for _, codec := range options.Codecs {
		if codec != CodecHEVC && codec != CodecH264 {
			return nil, fmt.Errorf("media: unsupported codec %v", codec)
		}
	}
	if options.Audio < AudioFromServer || options.Audio > AudioToServer {
		return nil, fmt.Errorf("media: unsupported audio mode %d", options.Audio)
	}
	if options.PixelFormats == 0 {
		options.PixelFormats = PixelFormatsSDR420
	}
	switch options.TilesPerFrame {
	case 0:
		options.TilesPerFrame = 1
	case 1, 4:
	default:
		return nil, fmt.Errorf("media: unsupported tiles per frame %d", options.TilesPerFrame)
	}
	if options.DecoderMaxPixelRate < 0 {
		return nil, fmt.Errorf("media: decoder maximum pixel rate must not be negative")
	}
	switch options.MaximumFrameRate {
	case 0, 30, 60:
	default:
		return nil, fmt.Errorf("media: unsupported maximum frame rate %d", options.MaximumFrameRate)
	}
	if options.MaximumBitrate == 0 {
		options.MaximumBitrate = maximumVideoBitrate
	}
	if options.MaximumBitrate < minimumVideoBitrate || options.MaximumBitrate > maximumVideoBitrate {
		return nil, fmt.Errorf("media: maximum bitrate must be between 5000000 and 60000000 bit/s")
	}
	if options.DeviceModel == "" {
		options.DeviceModel = defaultDeviceModel
	}
	if options.OSBuild == "" {
		options.OSBuild = defaultOSBuild
	}
	offer := &Offer{options: options, videoMode: negotiatorModeRemoteDesktopScreenSharing}
	switch options.VideoMode {
	case VideoModeAutomatic:
		if options.TilesPerFrame == 1 {
			offer.videoMode = negotiatorModeNearbyScreenSharing
		}
	case VideoModeNearby:
		if options.TilesPerFrame != 1 {
			return nil, errors.New("media: the Nearby profile sends one band")
		}
		offer.videoMode = negotiatorModeNearbyScreenSharing
	case VideoModeRemoteDesktop, VideoModeCoreDevice:
		offer.videoMode = int64(options.VideoMode)
	default:
		return nil, fmt.Errorf("media: unsupported video mode %d", options.VideoMode)
	}
	offer.UpdateVideoDimensions(options.Width, options.Height)
	for _, buffer := range [][]byte{
		offer.sessionID[:], offer.audioSend[:], offer.audioReceive[:],
		offer.videoSend[:], offer.videoReceive[:],
	} {
		_, err := rand.Read(buffer)
		if err != nil {
			return nil, err
		}
	}
	setUUIDVersion4(&offer.sessionID)
	for _, callID := range []*string{&offer.audioCallID, &offer.videoCallID} {
		var id [16]byte
		_, err := rand.Read(id[:])
		if err != nil {
			return nil, err
		}
		setUUIDVersion4(&id)
		*callID = uuidString(id)
	}
	offer.audioSSRC = randomSSRC()
	offer.videoSSRC = randomSSRC()
	return offer, nil
}

func (o *Offer) LegacyFallback() (*Offer, error) {
	if o.videoMode != negotiatorModeNearbyScreenSharing {
		return nil, nil
	}
	fallback, err := o.Renewed()
	if err != nil {
		return nil, err
	}
	fallback.videoMode = negotiatorModeRemoteDesktopScreenSharing
	return fallback, nil
}

func (o *Offer) Renewed() (*Offer, error) {
	renewed, err := NewOffer(o.options)
	if err != nil {
		return nil, err
	}
	renewed.videoMode = o.videoMode
	renewed.hdr.Store(o.hdr.Load())
	renewed.decoderDimensions.Store(o.decoderDimensions.Load())
	return renewed, nil
}

func setUUIDVersion4(id *[16]byte) {
	id[6] = id[6]&0x0f | 0x40
	id[8] = id[8]&0x3f | 0x80
}

func uuidString(id [16]byte) string {
	return strings.ToUpper(fmt.Sprintf("%x-%x-%x-%x-%x", id[0:4], id[4:6], id[6:8], id[8:10], id[10:16]))
}

func (o *Offer) UpdateVideoDimensions(width, height int) {
	if width <= 0 || height <= 0 || uint64(width) > 0xffffffff || uint64(height) > 0xffffffff {
		o.decoderDimensions.Store(0)
		return
	}
	o.decoderDimensions.Store(uint64(width)<<32 | uint64(height))
}

func Supports60FPS(decoderMaxPixelRate int64, width, height int) bool {
	if decoderMaxPixelRate <= 0 || width <= 0 || height <= 0 {
		return false
	}
	// ScreenSharing _GetHEVCDecoderMaxSupportedFrameRate, macOS 27.0.1 (26A434):
	// the single-display path divides MaxDecoderPixelRate by width * height * 3
	// and selects 60 FPS at a result of at least 60 (0x1cc0fbf9c–0x1cc0fc068).
	return uint64(decoderMaxPixelRate)/uint64(width)/uint64(height)/3 >= 60
}

func (o *Offer) requests60FPS() bool {
	if o.options.MaximumFrameRate == 30 {
		return false
	}
	if o.options.DecoderMaxPixelRate == 0 {
		return o.options.FrameRate60
	}
	dimensions := o.decoderDimensions.Load()
	return Supports60FPS(o.options.DecoderMaxPixelRate, int(dimensions>>32), int(dimensions&0xffffffff))
}

func randomSSRC() uint32 {
	var buffer [4]byte
	for {
		_, _ = rand.Read(buffer[:])
		value := binary.BigEndian.Uint32(buffer[:])
		if value != 0 {
			return value
		}
	}
}

func ntpTime(t time.Time) uint64 {
	const secondsFrom1900To1970 = 2208988800
	seconds := uint64(t.Unix() + secondsFrom1900To1970)
	fraction := uint64(t.Nanosecond()) << 32 / 1_000_000_000
	return seconds<<32 | fraction
}

func (o *Offer) Message() []byte {
	return o.message(time.Now())
}

func (o *Offer) message(now time.Time) []byte {
	created := ntpTime(now)
	audioMode := int64(negotiatorModeRemoteDesktopSystemAudio)
	if o.options.Audio != AudioFromServer {
		audioMode = negotiatorModeRemoteMic
	}
	audioOffer := o.streamOffer(audioMode, o.audioCallID, o.audioBlob(created))
	videoOffer := o.streamOffer(o.videoMode, o.videoCallID, o.videoBlob(created))
	// ScreenSharing _RFBMediaStreamServerConfiguration, macOS 27.0.1 (26A434),
	// 0x1cc14a050–0x1cc14a120: version 3, lengths at 0x0a/0x0c/0x0e, UUID at 0x14,
	// 46-byte audio keys at 0x24/0x52, audio blob at 0x80, then two 46-byte video
	// keys and the video blob. At 0x1cc14a458/0x1cc14a478, flag bits 0/2 select
	// 60 FPS/no cursor.
	message := make([]byte, 0x80, 0x80+len(audioOffer)+2*masterKeyLength+len(videoOffer))
	message[0] = ClientMessageMediaStream
	binary.BigEndian.PutUint16(message[4:], mediaStreamMessageVersion)
	var flags uint32
	if o.requests60FPS() {
		flags |= messageFlagFrameRate60
	}
	if !o.options.CursorInVideo {
		flags |= messageFlagNoCursor
	}
	binary.BigEndian.PutUint32(message[6:], flags)
	binary.BigEndian.PutUint16(message[0x0a:], uint16(len(audioOffer)))
	binary.BigEndian.PutUint16(message[0x0c:], uint16(len(videoOffer)))
	copy(message[0x14:], o.sessionID[:])
	copy(message[0x24:], o.audioSend[:])
	copy(message[0x52:], o.audioReceive[:])
	message = append(message, audioOffer...)
	message = append(message, o.videoSend[:]...)
	message = append(message, o.videoReceive[:]...)
	message = append(message, videoOffer...)
	binary.BigEndian.PutUint16(message[2:], uint16(len(message)-4))
	return message
}

func (o *Offer) streamOffer(mode int64, callID string, blob []byte) []byte {
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	_, _ = writer.Write(blob)
	_ = writer.Close()
	var endpoint protoMessage
	endpoint.addUint(1, 0)
	endpoint.addUint(2, endpointClientVersion)
	endpoint.addBytes(3, []byte(o.options.DeviceModel))
	endpoint.addBytes(4, []byte(endpointAVConferenceVersion))
	endpoint.addBytes(5, []byte(o.options.OSBuild))
	entries := []plistEntry{
		{"avcMediaStreamOptionRemoteEndpointInfo", endpoint.encode()},
		{"avcMediaStreamNegotiatorMode", mode},
		{"avcMediaStreamNegotiatorMediaBlob", compressed.Bytes()},
		{"avcMediaStreamOptionCallID", callID},
	}
	if mode == negotiatorModeNearbyScreenSharing {
		entries = append(entries, plistEntry{"avcMediaStreamNegotiatorDirection", int64(2)})
	}
	return encodeBinaryPlist(entries)
}

func (o *Offer) appendBlobTail(blob *protoMessage, created uint64, video bool) {
	blob.addBytes(6, []byte(negotiatorUserAgent))
	blob.addUint(8, 0)
	// The fixed field-9 entries below occur in native AVConference mode-7 offers
	// (macOS 27.0.1 26A434 and 27.2 26B5101f).
	for _, entry := range [][3]uint64{
		{4074, 0, 16384},
		{0, 20000000, 98304},
		{0, 40000000, 12288},
		{0, 100000000, 1048576},
		{0, 6000000, 131072},
		{1, 299, 0},
		{16, 4100, 0},
		{0, uint64(o.options.MaximumBitrate), 262144},
		{4, 6500, 0},
		{0, 75000000, 524288},
	} {
		if video && o.videoMode == negotiatorModeNearbyScreenSharing && entry[2] == 12288 {
			// AVConference bandwidthConfigurationWithArbiterMode:connectionType: returns
			// 4096 for arbiter 4 / connection 4 on macOS 27.0.1 (26A434) and 27.2 (26B5101f).
			entry[1], entry[2] = uint64(o.options.MaximumBitrate), 4096
		}
		var item protoMessage
		item.addUint(1, entry[0])
		item.addUint(2, entry[1])
		if entry[2] != 0 {
			item.addUint(3, entry[2])
		}
		blob.addMessage(9, item)
	}
	blob.addUint(13, created)
	blob.addUint(14, negotiatorBlobVersion)
	blob.addUint(16, 0)
	blob.addUint(18, accessNetworkTypeViewer)
}

func (o *Offer) audioBlob(created uint64) []byte {
	var stream protoMessage
	stream.addUint(1, uint64(o.audioSSRC))
	stream.addUint(2, 0)
	stream.addUint(3, 0)
	stream.addUint(4, 0x5e7f)
	stream.addUint(5, 0)
	stream.addUint(6, 0)
	var blob protoMessage
	blob.addUint(1, 1)
	blob.addUint(2, 1)
	blob.addMessage(3, stream)
	o.appendBlobTail(&blob, created, false)
	return blob.encode()
}

func (o *Offer) videoBlob(created uint64) []byte {
	var stream protoMessage
	stream.addUint(1, uint64(o.videoSSRC))
	stream.addUint(2, 0)
	for _, codec := range []Codec{CodecH264, CodecHEVC} {
		if !slices.Contains(o.options.Codecs, codec) {
			continue
		}
		var payload protoMessage
		var features string
		var parameterSets uint64
		switch codec {
		case CodecH264:
			payload.addUint(1, payloadTypeH264)
			features, parameterSets = featureListH264, parameterSetsH264
		case CodecHEVC:
			payload.addUint(1, payloadTypeHEVC)
			features, parameterSets = featureListHEVC, parameterSetsHEVC
		}
		for _, operation := range []uint64{videoRuleOperationEncode, videoRuleOperationDecode} {
			var rule protoMessage
			rule.addUint(1, videoRuleTransport)
			rule.addUint(2, operation)
			rule.addUint(3, screenFormats)
			rule.addUint(4, 0)
			payload.addMessage(2, rule)
		}
		payload.addBytes(3, []byte(features))
		payload.addUint(4, parameterSets)
		stream.addMessage(3, payload)
	}
	if o.options.Width > 0 && o.options.Height > 0 {
		stream.addUint(4, uint64(o.options.Width))
		stream.addUint(5, uint64(o.options.Height))
	}
	stream.addUint(6, uint64(o.options.TilesPerFrame))
	stream.addUint(7, 1)
	// AVConference storePixelFormatsInBitMap: maps bits 0–7 to 420f, 420v, x420,
	// 444f, 444v, xf44, v0a8, s4as (macOS 27.0.1 26A434 and 27.2 26B5101f).
	pixelFormats := o.options.PixelFormats
	hdrModes := uint64(1 << hdrModeStandard)
	if o.hdr.Load() && pixelFormats&PixelFormat444TenBitFullRange != 0 {
		// AVConference isPixelFormatValid:hdrMode: rejects 444f and 420f for HDR mode 3
		// (macOS 27.0.1 26A434 and 27.2 26B5101f).
		hdrModes |= 1 << hdrModeReference
	}
	stream.addUint(8, uint64(pixelFormats))
	stream.addUint(9, hdrModes)
	stream.addUint(12, 1)
	var blob protoMessage
	blob.addUint(1, 1)
	blob.addUint(2, 1)
	blob.addMessage(5, stream)
	o.appendBlobTail(&blob, created, true)
	return blob.encode()
}
