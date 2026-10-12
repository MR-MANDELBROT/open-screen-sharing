package media

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"reflect"

	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
)

const (
	replyPorts  = 1
	replyAnswer = 2
	replyError  = 3

	answerVersion              = 2
	answerEndpointModel        = "Mac14,3"
	answerEndpointAVConference = "2235.63.5.2"
	answerEndpointOSBuild      = "26A428"
	answerAudioPayloads        = 0x1600
	answerAudioSecondary       = 0x600
	answerVideoRuleFormats     = 128 // AVConference's native screen answer narrows 0xc3c3 to 128.
	answerFeatureString        = "FLS;POS:5;EOD:1;SW:1"
	defaultServerPixelFormat   = PixelFormat420FullRange
	serverErrorBodySize        = 16
	offerHeaderSize            = 0x80
	// AVConference caps negotiator mode 7 by the smaller of 60 Mbit/s and the
	// field-9 entry with bit 262144, and mode 9 by the smaller of 40 Mbit/s and
	// the entry with bit 4096: RemoteDesktop settings use operatingMode 9
	// (arbiter mode 5) on connection type 7, Nearby settings use operatingMode 8
	// (arbiter mode 4) on connection type 4, and the blob decoder reads bit 18 as
	// arbiter 5 on connection 7 and bit 12 as arbiter 4 on connection 4
	// (AVConference 2235.63.5.2, macOS 27.0.1 26A434, UUID
	// 93D8C8C0-8B9C-3A5E-A47D-01B5FAF73E1B,
	// -[AVCMediaStreamNegotiatorSettingsRemoteDesktopScreenSharing operatingMode] 0x1c133c4e8,
	// -[AVCMediaStreamNegotiatorSettingsRemoteDesktopScreenSharing maxBandwidth] 0x1c133c4fc,
	// -[AVCMediaStreamNegotiatorSettingsRemoteDesktopScreenSharing connectionType] 0x1c133c520,
	// -[AVCMediaStreamNegotiatorSettingsNearbyScreenSharing operatingMode] 0x1c0fb73b0,
	// -[AVCMediaStreamNegotiatorSettingsNearbyScreenSharing maxBandwidth] 0x1c0fb73b8,
	// -[AVCMediaStreamNegotiatorSettings connectionType] 0x1c11af800,
	// _VCBitrateArbiter_ModeFromOperatingMode 0x1c13b4ed0..0x1c13b4ee8, table 0x1c15ea260,
	// -[VCMediaNegotiationBlobBandwidthSettings(BandwidthSettings) newBandwidthConfigurations]
	// 0x1c1315f10..0x1c1315f2c, 0x1c1316110..0x1c131612c,
	// -[AVCMediaStreamNegotiator setupVideoStreamConfiguration:errorReason:]
	// 0x1c12e0f00..0x1c12e0f70, 0x1c12e12e0..0x1c12e1310; AVConference 2260.14.1,
	// macOS 27.2 26B5101f, UUID EDFB99EF-2F5E-35DD-8E63-089CBD102EFE,
	// -[AVCMediaStreamNegotiatorSettingsRemoteDesktopScreenSharing operatingMode] 0x1c09ad094,
	// -[AVCMediaStreamNegotiatorSettingsRemoteDesktopScreenSharing maxBandwidth] 0x1c09ad0a8,
	// -[AVCMediaStreamNegotiatorSettingsRemoteDesktopScreenSharing connectionType] 0x1c09ad0cc,
	// -[AVCMediaStreamNegotiatorSettingsNearbyScreenSharing operatingMode] 0x1c0632cd8,
	// -[AVCMediaStreamNegotiatorSettingsNearbyScreenSharing maxBandwidth] 0x1c0632ce0,
	// -[AVCMediaStreamNegotiatorSettings connectionType] 0x1c082bf4c,
	// _VCBitrateArbiter_ModeFromOperatingMode 0x1c0a262cc..0x1c0a262e4, table 0x1c0c675c0,
	// -[VCMediaNegotiationBlobBandwidthSettings(BandwidthSettings) newBandwidthConfigurations]
	// 0x1c0986788..0x1c09867a4, 0x1c0986988..0x1c09869a4,
	// -[AVCMediaStreamNegotiator setupVideoStreamConfiguration:errorReason:]
	// 0x1c0954cf0..0x1c0954d60, 0x1c09550d0..0x1c0955100).
	remoteDesktopBandwidthBit = 262144
	nearbyBandwidthBit        = 4096
	nearbyMaximumBitrate      = 40_000_000
)

const (
	ServerErrorBusy             uint32 = 1
	ServerErrorBusySubCode      uint32 = 1
	ServerErrorBadConfiguration uint32 = 2
)

// These field-9 bandwidth entries also occur in AVConference's native
// screen answers.
var (
	answerAudioBandwidth = [][3]uint64{
		{16, 4100, 0},
		{4074, 0, 16384},
		{0, 6000000, 131072},
		{0, 20000000, 98304},
		{1, 299, 0},
		{0, 75000000, 524288},
		{0, 40000000, 12288},
		{0, 100000000, 1048576},
		{4, 6500, 0},
		{0, 60000000, 262144},
	}
	answerVideoBandwidth = [][3]uint64{
		{0, 60000000, 262144},
		{16, 4100, 0},
		{4074, 0, 16384},
		{4, 6500, 0},
		{0, 40000000, 12288},
		{0, 20000000, 98304},
		{1, 299, 0},
		{0, 75000000, 524288},
		{0, 100000000, 1048576},
		{0, 6000000, 131072},
	}
)

func portsMessage(audioPort, videoPort uint16) []byte {
	body := make([]byte, 36)
	binary.BigEndian.PutUint16(body[0:], replyPorts)
	binary.BigEndian.PutUint16(body[2:], 1)
	leg := func(offset int, port uint16) {
		binary.BigEndian.PutUint16(body[offset:], port)
		if port != 0 {
			binary.BigEndian.PutUint32(body[offset+2:], 1)
		}
	}
	leg(8, audioPort)
	leg(14, videoPort)
	return append(binary.BigEndian.AppendUint16(nil, uint16(len(body))), body...)
}

func ErrorMessage(errorType, subCode uint32) []byte {
	body := make([]byte, serverErrorBodySize)
	binary.BigEndian.PutUint16(body[0:], replyError)
	binary.BigEndian.PutUint16(body[2:], answerVersion)
	binary.BigEndian.PutUint32(body[8:], errorType)
	binary.BigEndian.PutUint32(body[12:], subCode)
	return append(binary.BigEndian.AppendUint16(nil, uint16(len(body))), body...)
}

type serverAnswer struct {
	FrameRate60 bool
	Video       ServerVideoInfo
	Audio       *ServerAudioInfo
	PixelFormat PixelFormats
}

func answerMessage(answer serverAnswer) ([]byte, error) {
	if answer.Video.SSRC == 0 || answer.Video.Width <= 0 || answer.Video.Height <= 0 {
		return nil, E.New("media: the answer needs the video SSRC and size, got SSRC ", answer.Video.SSRC, " and ", answer.Video.Width, "x", answer.Video.Height)
	}
	var audio []byte
	if answer.Audio != nil {
		audio = answerPlist(answerAudioBlob(*answer.Audio))
	}
	video := answerPlist(answerVideoBlob(answer.Video, answer.PixelFormat))
	body := make([]byte, 18, 18+len(audio)+len(video))
	binary.BigEndian.PutUint16(body[0:], replyAnswer)
	binary.BigEndian.PutUint16(body[2:], answerVersion)
	if answer.FrameRate60 {
		binary.BigEndian.PutUint32(body[4:], messageFlagFrameRate60)
	}
	binary.BigEndian.PutUint16(body[8:], uint16(len(audio)))
	binary.BigEndian.PutUint16(body[10:], uint16(len(video)))
	body = append(body, audio...)
	body = append(body, video...)
	return append(binary.BigEndian.AppendUint16(nil, uint16(len(body))), body...), nil
}

func answerPlist(blob []byte) []byte {
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	_, _ = writer.Write(blob)
	_ = writer.Close()
	var endpoint protoMessage
	endpoint.addUint(1, 0)
	endpoint.addUint(2, endpointClientVersion)
	endpoint.addBytes(3, []byte(answerEndpointModel))
	endpoint.addBytes(4, []byte(answerEndpointAVConference))
	endpoint.addBytes(5, []byte(answerEndpointOSBuild))
	return encodeBinaryPlist([]plistEntry{
		{"avcMediaStreamNegotiatorMediaBlob", compressed.Bytes()},
		{"avcMediaStreamOptionRemoteEndpointInfo", endpoint.encode()},
	})
}

func appendAnswerTail(blob *protoMessage, bandwidth [][3]uint64) {
	blob.addBytes(6, []byte(negotiatorUserAgent))
	blob.addUint(8, 0)
	for _, entry := range bandwidth {
		var item protoMessage
		item.addUint(1, entry[0])
		item.addUint(2, entry[1])
		if entry[2] != 0 {
			item.addUint(3, entry[2])
		}
		blob.addMessage(9, item)
	}
	var tail protoMessage
	tail.addUint(1, 4)
	tail.addUint(4, 4)
	blob.addMessage(12, tail)
	blob.addUint(14, negotiatorBlobVersion)
	blob.addUint(16, 0)
}

func answerAudioBlob(info ServerAudioInfo) []byte {
	var stream protoMessage
	stream.addUint(1, uint64(info.SSRC))
	stream.addUint(2, 0)
	stream.addUint(3, 0)
	stream.addUint(4, answerAudioPayloads)
	stream.addUint(5, answerAudioSecondary)
	stream.addUint(6, 1)
	var blob protoMessage
	blob.addUint(1, 1)
	blob.addUint(2, 1)
	blob.addMessage(3, stream)
	appendAnswerTail(&blob, answerAudioBandwidth)
	return blob.encode()
}

func answerVideoBlob(info ServerVideoInfo, pixelFormat PixelFormats) []byte {
	// AVConference's native screen answer includes an empty audio stream record.
	var audio protoMessage
	for number := uint64(1); number <= 5; number++ {
		audio.addUint(number, 0)
	}
	audio.addUint(6, 1)
	var payload protoMessage
	payload.addUint(1, payloadTypeHEVC)
	for _, operation := range []uint64{videoRuleOperationEncode, videoRuleOperationDecode} {
		var rule protoMessage
		rule.addUint(1, videoRuleTransport)
		rule.addUint(2, operation)
		rule.addUint(3, answerVideoRuleFormats)
		rule.addUint(4, 0)
		payload.addMessage(2, rule)
	}
	// VideoProcessing requires an FLS EOD entry before CheckIfLastSubFrame
	// reads the end bit; EOD:1 selects bit 1. POS:5 makes control-byte bit 5
	// announce the position field (VideoProcessing 1395.65.1, macOS 27.0.1
	// 26A434, UUID D87079FD-5315-32B1-8022-E6144A69A8EE,
	// _VCPDecompressionSessionCheckIfLastSubFrame 0x1b5004328..0x1b500435c,
	// sub_1b50c7dec 0x1b50c7dec..0x1b50c8038, sub_1b50c95c4 0x1b50c95c4..0x1b50c9958;
	// VideoProcessing 1415.6.1, macOS 27.2 26B5101f, UUID
	// A6FC3A01-8834-335F-A877-FDACE2E1AECC,
	// _VCPDecompressionSessionCheckIfLastSubFrame 0x1cb62d0ac..0x1cb62d0e0,
	// sub_1cb6f0970 0x1cb6f0970..0x1cb6f0bbc, sub_1cb6f2148 0x1cb6f2148..0x1cb6f24dc).
	payload.addBytes(3, []byte(answerFeatureString))
	payload.addUint(4, parameterSetsHEVC)
	var stream protoMessage
	stream.addUint(1, uint64(info.SSRC))
	// With RTCP-FB off, AVConference RTPSendRTCP takes a legacy FIR path that
	// uses RTCP packet type 192 (AVConference 2235.63.5.2, macOS 27.0.1 26A434,
	// UUID 93D8C8C0-8B9C-3A5E-A47D-01B5FAF73E1B, _RTPSetRTCPFB 0x1c0eeda74,
	// _RTPSendRTCP 0x1c0eba7cc..0x1c0eba84c; AVConference 2260.14.1, macOS 27.2
	// 26B5101f, UUID EDFB99EF-2F5E-35DD-8E63-089CBD102EFE,
	// _RTPSetRTCPFB 0x1c05680d0, _RTPSendRTCP 0x1c0534eac..0x1c0534f2c).
	stream.addUint(2, 0)
	stream.addMessage(3, payload)
	stream.addUint(4, uint64(info.Width))
	stream.addUint(5, uint64(info.Height))
	stream.addUint(6, uint64(max(info.TilesPerFrame, 1)))
	stream.addUint(7, boolUint(info.LTRP))
	stream.addUint(8, uint64(pixelFormat))
	stream.addUint(9, 1<<hdrModeStandard)
	stream.addUint(12, 1)
	var blob protoMessage
	blob.addUint(1, 1)
	blob.addUint(2, 1)
	blob.addMessage(3, audio)
	blob.addMessage(5, stream)
	appendAnswerTail(&blob, answerVideoBandwidth)
	return blob.encode()
}

func boolUint(value bool) uint64 {
	if value {
		return 1
	}
	return 0
}

type serverOffer struct {
	FrameRate60 bool
	Audio       serverOfferStream
	Video       serverOfferStream
}

type serverOfferStream struct {
	Present bool
	// AVConference modes 5/7/9 select CoreDevice/RemoteDesktop/Nearby screen
	// settings; mode 8 selects RemoteDesktop system audio and mode 4 selects
	// remote mic (AVConference 2235.63.5.2, macOS 27.0.1 26A434, UUID
	// 93D8C8C0-8B9C-3A5E-A47D-01B5FAF73E1B,
	// +[AVCMediaStreamNegotiatorSettings negotiatorSettingsForMode:deviceRole:options:errorString:] 0x1c11af820..0x1c11af8e0,
	// class table 0x1e1c9b9f0; AVConference 2260.14.1, macOS 27.2 26B5101f, UUID
	// EDFB99EF-2F5E-35DD-8E63-089CBD102EFE,
	// +[AVCMediaStreamNegotiatorSettings negotiatorSettingsForMode:deviceRole:options:errorString:] 0x1c082bf6c..0x1c082c02c,
	// class table 0x1e18e2f80).
	Mode           int
	SSRC           uint32
	Width          int
	Height         int
	PixelFormats   PixelFormats
	TilesPerFrame  int
	MaximumBitrate int64

	clientKey [masterKeyLength]byte
	serverKey [masterKeyLength]byte
}

func parseOffer(message []byte) (*serverOffer, error) {
	if len(message) < offerHeaderSize {
		return nil, E.New("media: offer of ", len(message), " bytes, shorter than its ", offerHeaderSize, "-byte header")
	}
	offer := &serverOffer{FrameRate60: binary.BigEndian.Uint32(message[6:])&messageFlagFrameRate60 != 0}
	audioLength := int(binary.BigEndian.Uint16(message[0x0a:]))
	videoLength := int(binary.BigEndian.Uint16(message[0x0c:]))
	audioEnd := offerHeaderSize + audioLength
	videoKeys := audioEnd
	videoStart := videoKeys + 2*masterKeyLength
	if len(message) < videoStart+videoLength {
		return nil, E.New("media: offer of ", len(message), " bytes, shorter than the ", videoStart+videoLength, " bytes its streams need")
	}
	copy(offer.Audio.clientKey[:], message[0x24:])
	copy(offer.Audio.serverKey[:], message[0x52:])
	copy(offer.Video.clientKey[:], message[videoKeys:])
	copy(offer.Video.serverKey[:], message[videoKeys+masterKeyLength:])
	var err error
	if audioLength > 0 {
		err = parseOfferStream(&offer.Audio, message[offerHeaderSize:audioEnd], 3)
		if err != nil {
			return nil, E.Cause(err, "parse audio offer")
		}
	}
	if videoLength == 0 {
		return nil, E.New("media: the offer has no video")
	}
	err = parseOfferStream(&offer.Video, message[videoStart:videoStart+videoLength], 5)
	if err != nil {
		return nil, E.Cause(err, "parse video offer")
	}
	if !offer.Video.Present {
		return nil, E.New("media: the video offer names no stream")
	}
	return offer, nil
}

func parseOfferStream(stream *serverOfferStream, data []byte, streamField uint64) error {
	value, err := decodeBinaryPlist(data)
	if err != nil {
		return err
	}
	dictionary, isDictionary := value.(map[string]any)
	if !isDictionary {
		return E.New("media: property list is ", reflect.TypeOf(value), ", not a dictionary")
	}
	mode, _ := dictionary["avcMediaStreamNegotiatorMode"].(int64)
	stream.Mode = int(mode)
	compressed, isData := dictionary["avcMediaStreamNegotiatorMediaBlob"].([]byte)
	if !isData {
		return E.New("media: no avcMediaStreamNegotiatorMediaBlob")
	}
	blob, err := parseNegotiatorBlob(compressed)
	if err != nil {
		return err
	}
	streams := blob.messages(streamField)
	if len(streams) == 0 {
		return nil
	}
	settings := streams[0]
	stream.Present = true
	ssrc, _ := settings.uint(1)
	stream.SSRC = uint32(ssrc)
	if streamField != 5 {
		return nil
	}
	width, _ := settings.uint(4)
	height, _ := settings.uint(5)
	tiles, _ := settings.uint(6)
	pixelFormats, _ := settings.uint(8)
	stream.Width = int(min(width, 1<<16))
	stream.Height = int(min(height, 1<<16))
	stream.TilesPerFrame = int(max(min(tiles, 16), 1))
	stream.PixelFormats = PixelFormats(pixelFormats)
	bandwidthBit := uint64(remoteDesktopBandwidthBit)
	if stream.Mode == negotiatorModeNearbyScreenSharing {
		bandwidthBit = nearbyBandwidthBit
	}
	for _, tier := range blob.messages(9) {
		kind, _ := tier.uint(1)
		bitrate, _ := tier.uint(2)
		bufferCap, _ := tier.uint(3)
		if kind == 0 && bufferCap&bandwidthBit != 0 {
			stream.MaximumBitrate = int64(min(bitrate, 1<<40))
		}
	}
	return nil
}

func answerPixelFormat(offered PixelFormats, preferred PixelFormats) PixelFormats {
	if preferred == 0 {
		preferred = defaultServerPixelFormat
	}
	if offered == 0 || offered&preferred != 0 {
		return preferred
	}
	format := common.Find([]PixelFormats{PixelFormat420FullRange, PixelFormat420VideoRange}, func(format PixelFormats) bool {
		return offered&format != 0
	})
	if format == 0 {
		return preferred
	}
	return format
}
