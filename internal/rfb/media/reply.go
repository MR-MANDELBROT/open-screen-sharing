package media

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"io"
	"reflect"

	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
)

const (
	payloadTypeHEVC    = 100
	payloadTypeAudio   = 101
	payloadTypeH264    = 123
	payloadTypeH264Alt = 126
	payloadTypeRED     = 20
	maxNegotiatorBlob  = 1 << 20
)

type Leg struct {
	Port    uint16
	Enabled bool
	HDR     bool
}

type Ports struct {
	Audio  Leg
	Video  Leg
	Video2 Leg
}

type Answer struct {
	Audio       StreamAnswer
	Video       StreamAnswer
	Version     int
	FrameRate60 bool
}

type StreamAnswer struct {
	SSRC              uint32
	PayloadType       int
	Codec             Codec
	Width             int
	Height            int
	Present           bool
	AccessNetworkType int

	TilesPerFrame int
	AllowRTCPFB   bool
	LTRPEnabled   bool
	FECEnabled    bool
	RTXEnabled    bool
	FeatureString string
	PixelFormats  PixelFormats
	HDRModes      uint32
	HDRMode       int
	PixelFormat   PixelFormats

	UseSBR bool
	RED    bool
}

type ServerError struct {
	Type    uint32
	SubCode uint32
}

func (e *ServerError) Error() string {
	return fmt.Sprint("media: server refused the media stream (type ", e.Type, ", sub-code ", e.SubCode, ")")
}

func ParsePorts(payload []byte) (*Ports, error) {
	reply, err := parseReply(payload)
	if err != nil {
		return nil, err
	}
	ports, isPorts := reply.(*Ports)
	if !isPorts {
		return nil, E.New("media: expected a ports message, got ", reflect.TypeOf(reply))
	}
	return ports, nil
}

func parseReply(payload []byte) (any, error) {
	if len(payload) < 2 {
		return nil, E.New("media: truncated media stream reply: ", len(payload), " of 2 length bytes")
	}
	size := int(binary.BigEndian.Uint16(payload))
	if size < 8 {
		return nil, E.New("media: media stream reply declares ", size, " bytes, less than its 8-byte header")
	}
	if len(payload) < 2+size {
		return nil, E.New("media: truncated media stream reply: ", len(payload)-2, " of ", size, " bytes")
	}
	body := payload[2 : 2+size]
	kind := binary.BigEndian.Uint16(body)
	switch kind {
	case 1:
		if len(body) < 26 {
			return nil, E.New("media: truncated ports reply: ", len(body), " of 26 bytes")
		}
		leg := func(offset int) Leg {
			flags := binary.BigEndian.Uint32(body[offset+2:])
			return Leg{
				Port:    binary.BigEndian.Uint16(body[offset:]),
				Enabled: flags&1 != 0,
				HDR:     flags&2 != 0,
			}
		}
		return &Ports{Audio: leg(8), Video: leg(14), Video2: leg(20)}, nil
	case 2:
		if len(body) < 18 {
			return nil, E.New("media: truncated answer reply: ", len(body), " of 18 header bytes")
		}
		audioLength := int(binary.BigEndian.Uint16(body[8:]))
		videoLength := int(binary.BigEndian.Uint16(body[10:]))
		video2Length := int(binary.BigEndian.Uint16(body[12:]))
		if len(body) < 18+audioLength+videoLength+video2Length {
			return nil, E.New("media: truncated answer reply: ", len(body), " of ", 18+audioLength+videoLength+video2Length, " bytes")
		}
		answer := &Answer{Version: int(binary.BigEndian.Uint16(body[2:]))}
		answer.FrameRate60 = answer.Version <= 1 || binary.BigEndian.Uint32(body[4:])&1 != 0
		audioBlob := body[18 : 18+audioLength]
		videoBlob := body[18+audioLength : 18+audioLength+videoLength]
		var err error
		answer.Audio, err = parseStreamAnswer(audioBlob, 3)
		if err != nil {
			return nil, E.Cause(err, "parse audio answer")
		}
		answer.Video, err = parseStreamAnswer(videoBlob, 5)
		if err != nil {
			return nil, E.Cause(err, "parse video answer")
		}
		return answer, nil
	case 3:
		if len(body) < 16 {
			return nil, E.New("media: truncated error reply: ", len(body), " of 16 bytes")
		}
		return &ServerError{
			Type:    binary.BigEndian.Uint32(body[8:]),
			SubCode: binary.BigEndian.Uint32(body[12:]),
		}, nil
	default:
		return nil, E.New("media: unknown media stream reply type ", kind)
	}
}

// AVConference createAnswer puts the compressed response blob under
// avcMediaStreamNegotiatorMediaBlob in a binary plist when the offer carries
// avcMediaStreamOptionRemoteEndpointInfo, and returns the bare compressed blob
// otherwise (AVConference 2235.63.5.2, macOS 27.0.1 26A434, UUID
// 93D8C8C0-8B9C-3A5E-A47D-01B5FAF73E1B,
// -[AVCMediaStreamNegotiator processOfferWithError:errorReason:] 0x1c12dfa14..0x1c12dfa20,
// -[AVCMediaStreamNegotiator createAnswer] 0x1c12e0294..0x1c12e03a0,
// -[VCMediaNegotiator newResponseBlob] 0x1c122d9d0; AVConference 2260.14.1,
// macOS 27.2 26B5101f, UUID EDFB99EF-2F5E-35DD-8E63-089CBD102EFE,
// -[AVCMediaStreamNegotiator processOfferWithError:errorReason:] 0x1c0953804..0x1c0953810,
// -[AVCMediaStreamNegotiator createAnswer] 0x1c0954084..0x1c0954190,
// -[VCMediaNegotiator newResponseBlob] 0x1c08a0708).
func parseStreamAnswer(blob []byte, streamField uint64) (StreamAnswer, error) {
	var answer StreamAnswer
	if len(blob) == 0 {
		return answer, nil
	}
	compressed := blob
	value, err := decodeBinaryPlist(blob)
	if err == nil {
		dictionary, isDictionary := value.(map[string]any)
		if !isDictionary {
			return answer, E.New("media: property list is ", reflect.TypeOf(value), ", not a dictionary")
		}
		var isData bool
		compressed, isData = dictionary["avcMediaStreamNegotiatorMediaBlob"].([]byte)
		if !isData {
			return answer, E.New("media: no avcMediaStreamNegotiatorMediaBlob")
		}
	}
	message, err := parseNegotiatorBlob(compressed)
	if err != nil {
		return answer, err
	}
	accessNetworkType, _ := message.uint(18)
	answer.AccessNetworkType = int(accessNetworkType)
	streams := message.messages(streamField)
	if len(streams) == 0 {
		return answer, nil
	}
	stream := streams[0]
	answer.Present = true
	ssrc, _ := stream.uint(1)
	answer.SSRC = uint32(ssrc)
	switch streamField {
	case 3:
		readAudioAnswer(&answer, stream)
	case 5:
		readScreenAnswer(&answer, stream)
	}
	return answer, nil
}

func parseNegotiatorBlob(compressed []byte) (protoMessage, error) {
	reader, err := zlib.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, E.Cause(err, "media: decompress negotiator blob")
	}
	inflated, err := io.ReadAll(io.LimitReader(reader, maxNegotiatorBlob))
	if err != nil {
		return nil, E.Cause(err, "media: decompress negotiator blob")
	}
	return parseProto(inflated)
}

func readScreenAnswer(answer *StreamAnswer, stream protoMessage) {
	var selected protoMessage
	found := false
	for _, payload := range stream.messages(3) {
		payloadType, hasType := payload.uint(1)
		if !hasType {
			continue
		}
		if !found || payloadType == payloadTypeHEVC {
			selected, found = payload, true
			answer.PayloadType = int(payloadType)
		}
		if payloadType == payloadTypeHEVC {
			break
		}
	}
	if found {
		answer.Codec = codecForPayloadType(answer.PayloadType)
		features, _ := selected.bytes(3)
		answer.FeatureString = string(features)
	}
	width, _ := stream.uint(4)
	height, _ := stream.uint(5)
	answer.Width = int(width)
	answer.Height = int(height)
	tiles, _ := stream.uint(6)
	answer.TilesPerFrame = int(tiles)
	flag := func(number uint64) bool {
		value, _ := stream.uint(number)
		return value != 0
	}
	answer.AllowRTCPFB = flag(2)
	answer.LTRPEnabled = flag(7)
	answer.FECEnabled = flag(10)
	answer.RTXEnabled = flag(11)
	pixelFormats, _ := stream.uint(8)
	hdrModes, _ := stream.uint(9)
	answer.PixelFormats = PixelFormats(pixelFormats)
	answer.HDRModes = uint32(hdrModes)
	if answer.HDRModes&(1<<hdrModeReference) != 0 {
		answer.HDRMode = hdrModeReference
	}
	answer.PixelFormat = pickPixelFormat(answer.PixelFormats, answer.HDRMode)
}

// AVConference pickBestPixelFormatFromSet:forPayload: prefers 444f/420f for
// mode-7 SDR and xf44/444f/420f for HDR mode 3, with 420f fallback
// (AVConference 2235.63.5.2, macOS 27.0.1 26A434, UUID
// 93D8C8C0-8B9C-3A5E-A47D-01B5FAF73E1B,
// -[AVCMediaStreamNegotiator pickBestPixelFormatFromSet:forPayload:] 0x1c12e2324..0x1c12e2494,
// -[AVCMediaStreamNegotiator pickBestPixelFormatFromSet:preferenceList:count:acceptDefault:] 0x1c12e2544..0x1c12e256c,
// lists 0x1e577943c and 0x1e5779444; AVConference 2260.14.1, macOS 27.2
// 26B5101f, UUID EDFB99EF-2F5E-35DD-8E63-089CBD102EFE,
// -[AVCMediaStreamNegotiator pickBestPixelFormatFromSet:forPayload:] 0x1c0956114..0x1c0956284,
// -[AVCMediaStreamNegotiator pickBestPixelFormatFromSet:preferenceList:count:acceptDefault:] 0x1c0956334..0x1c095635c,
// lists 0x1ec938d74 and 0x1ec938d7c).
func pickPixelFormat(formats PixelFormats, hdrMode int) PixelFormats {
	preference := []PixelFormats{PixelFormat444FullRange, PixelFormat420FullRange}
	if hdrMode == hdrModeReference {
		preference = []PixelFormats{PixelFormat444TenBitFullRange, PixelFormat444FullRange, PixelFormat420FullRange}
	}
	format := common.Find(preference, func(format PixelFormats) bool {
		return formats&format != 0
	})
	if format == 0 {
		return PixelFormat420FullRange
	}
	return format
}

// AVConference payloadFromNegotiationPayload: maps these audio bits; bit 13
// returns its unknown-payload value 128 (AVConference 2235.63.5.2, macOS 27.0.1
// 26A434, UUID 93D8C8C0-8B9C-3A5E-A47D-01B5FAF73E1B,
// +[VCMediaNegotiationBlobAudioSettings(AudioRules) payloadFromNegotiationPayload:] 0x1c131483c..0x1c13149d0;
// AVConference 2260.14.1, macOS 27.2 26B5101f, UUID
// EDFB99EF-2F5E-35DD-8E63-089CBD102EFE,
// +[VCMediaNegotiationBlobAudioSettings(AudioRules) payloadFromNegotiationPayload:] 0x1c09850b4..0x1c0985248).
var audioPayloadTypes = [...]int{9, 106, 105, 104, 122, 121, 119, 97, 98, 13, payloadTypeRED, 108, payloadTypeAudio, -1, 113}

func readAudioAnswer(answer *StreamAnswer, stream protoMessage) {
	payloads, _ := stream.uint(4)
	secondary, _ := stream.uint(5)
	for bit, payloadType := range audioPayloadTypes {
		if payloadType < 0 || payloads&(1<<bit) == 0 {
			continue
		}
		if payloadType == payloadTypeRED {
			answer.RED = true
		}
		if secondary&(1<<bit) == 0 && answer.PayloadType == 0 {
			answer.PayloadType = payloadType
		}
	}
	sbr, _ := stream.uint(6)
	answer.UseSBR = sbr != 0
}

func codecForPayloadType(payloadType int) Codec {
	switch payloadType {
	case payloadTypeHEVC:
		return CodecHEVC
	case payloadTypeH264, payloadTypeH264Alt:
		return CodecH264
	default:
		return 0
	}
}
