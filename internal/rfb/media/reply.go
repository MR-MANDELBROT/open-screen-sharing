package media

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	payloadTypeHEVC    = 100
	payloadTypeAudio   = 101
	payloadTypeH264    = 123
	payloadTypeH264Alt = 126
	payloadTypeRED     = 20
	maxNegotiatorBlob  = 1 << 20
)

var errShortReply = errors.New("media: truncated media stream reply")

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
		return nil, fmt.Errorf("media: not a ports message")
	}
	return ports, nil
}

func parseReply(payload []byte) (any, error) {
	if len(payload) < 2 {
		return nil, errShortReply
	}
	size := int(binary.BigEndian.Uint16(payload))
	if len(payload) < 2+size || size < 8 {
		return nil, errShortReply
	}
	body := payload[2 : 2+size]
	kind := binary.BigEndian.Uint16(body)
	switch kind {
	case 1:
		if len(body) < 26 {
			return nil, errShortReply
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
			return nil, errShortReply
		}
		audioLength := int(binary.BigEndian.Uint16(body[8:]))
		videoLength := int(binary.BigEndian.Uint16(body[10:]))
		video2Length := int(binary.BigEndian.Uint16(body[12:]))
		if len(body) < 18+audioLength+videoLength+video2Length {
			return nil, errShortReply
		}
		answer := &Answer{Version: int(binary.BigEndian.Uint16(body[2:]))}
		answer.FrameRate60 = answer.Version <= 1 || binary.BigEndian.Uint32(body[4:])&1 != 0
		audioBlob := body[18 : 18+audioLength]
		videoBlob := body[18+audioLength : 18+audioLength+videoLength]
		var err error
		answer.Audio, err = parseStreamAnswer(audioBlob, 3)
		if err != nil {
			return nil, fmt.Errorf("%w in the audio answer", err)
		}
		answer.Video, err = parseStreamAnswer(videoBlob, 5)
		if err != nil {
			return nil, fmt.Errorf("%w in the video answer", err)
		}
		return answer, nil
	case 3:
		if len(body) < 16 {
			return nil, errShortReply
		}
		return &ServerError{
			Type:    binary.BigEndian.Uint32(body[8:]),
			SubCode: binary.BigEndian.Uint32(body[12:]),
		}, nil
	default:
		return nil, fmt.Errorf("media: unknown media stream reply type %d", kind)
	}
}

// AVConference createAnswer returns a plist containing compressed
// avcMediaStreamNegotiatorMediaBlob data in native mode-7 negotiation
// (macOS 27.0.1 26A434 and 27.2 26B5101f).
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
			return answer, errors.New("media: a property list that is not a dictionary")
		}
		var isData bool
		compressed, isData = dictionary["avcMediaStreamNegotiatorMediaBlob"].([]byte)
		if !isData {
			return answer, errors.New("media: no avcMediaStreamNegotiatorMediaBlob")
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
		return nil, fmt.Errorf("media: negotiator blob: %w", err)
	}
	inflated, err := io.ReadAll(io.LimitReader(reader, maxNegotiatorBlob))
	if err != nil {
		return nil, fmt.Errorf("media: negotiator blob: %w", err)
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
// (macOS 27.0.1 26A434 and 27.2 26B5101f).
func pickPixelFormat(formats PixelFormats, hdrMode int) PixelFormats {
	preference := []PixelFormats{PixelFormat444FullRange, PixelFormat420FullRange}
	if hdrMode == hdrModeReference {
		preference = []PixelFormats{PixelFormat444TenBitFullRange, PixelFormat444FullRange, PixelFormat420FullRange}
	}
	for _, format := range preference {
		if formats&format != 0 {
			return format
		}
	}
	return PixelFormat420FullRange
}

// AVConference payloadFromNegotiationPayload: maps these audio bits; bit 13
// returns its unknown-payload value 128 (macOS 27.0.1 26A434 and 27.2 26B5101f).
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
