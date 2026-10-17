// The encoder structure follows go-ios ios/display/offer.go
// (https://github.com/danielpaulus/go-ios, MIT License, Copyright (c) 2019
// danielpaulus).

package display

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"howett.net/plist"
)

// The captured video and audio media blobs kept by pymobiledevice3 contain
// these tier tables, codec-bank layout, decoder name and five-byte SSRC
// varints (pymobiledevice3 1146717,
// pymobiledevice3/remote/core_device/media_stream_offer.py:484-505).

type Mode int

// The mode-5 capture is 2432x1680; modes 7 and 9 display 2420x1668 from a
// 2432x1680 coded frame (observed on iPad17,1, iPadOS 27.2 24B5099f).
const (
	// Mode 5 returned TXMaxBitrate=6000000 and 420f (observed on iPad17,1,
	// iPadOS 27.2 24B5099f).
	ModeCoreDevice Mode = 5
	// Mode 7 returned TXMaxBitrate=60000000 and 444f; its capture is HEVC
	// range extensions, 4:4:4 (observed on iPad17,1, iPadOS 27.2 24B5099f).
	ModeRemoteDesktop Mode = 7
	// Mode 9 returned TXMaxBitrate=40000000 and 420v (observed on iPad17,1,
	// iPadOS 27.2 24B5099f).
	ModeNearby Mode = 9
)

const (
	negotiatorModeAudio = 6

	decoderName = "Viceroy 1.7.0"

	resEntryCodecCapID = 50115

	bankPayloadTypeA = 123
	bankPayloadTypeB = 100

	bankFeatures = "FLS;SW:1;"
)

// An offer with these endpoint values succeeded (observed on iPad17,1,
// build 24B5089g).
const (
	hostModel            = "Mac17,7"
	hostFrameworkVersion = "2205.3.1"
	hostBuild            = "25F71"
)

type bitrateTier struct {
	kind      uint64
	bps       uint64
	bufferCap uint64
}

// Mode 7 with the 262144 tier set to 10 Mbit/s and mode 9 with the 12288
// tier set to 10 Mbit/s each returned TXMaxBitrate=10000000 while
// TXMinBitrate stayed 20000000 (observed on iPad17,1, iPadOS 27.2 24B5099f).
const (
	screenTierBufferCap = 262144
	screenTierLimit     = 60_000_000
	nearbyTierBufferCap = 12288
	nearbyTierLimit     = 40_000_000
)

const videoPixelFormats = 63

var (
	videoBitrateTiers = []bitrateTier{
		{kind: 4074, bps: 0, bufferCap: 16384},
		{kind: 0, bps: 75_000_000, bufferCap: 524288},
		{kind: 0, bps: 40_000_000, bufferCap: 12288},
		{kind: 16, bps: 4100},
		{kind: 0, bps: 20_000_000, bufferCap: 98304},
		{kind: 4, bps: 6500},
		{kind: 0, bps: 6_000_000, bufferCap: 131072},
		{kind: 0, bps: 100_000_000, bufferCap: 1048576},
		{kind: 0, bps: 60_000_000, bufferCap: 262144},
		{kind: 1, bps: 299},
	}
	audioBitrateTiers = []bitrateTier{
		{kind: 4074, bps: 0, bufferCap: 16384},
		{kind: 1, bps: 299},
		{kind: 0, bps: 60_000_000, bufferCap: 262144},
		{kind: 4, bps: 6500},
		{kind: 0, bps: 20_000_000, bufferCap: 98304},
		{kind: 0, bps: 100_000_000, bufferCap: 1048576},
		{kind: 0, bps: 40_000_000, bufferCap: 12288},
		{kind: 0, bps: 6_000_000, bufferCap: 131072},
		{kind: 16, bps: 4100},
		{kind: 0, bps: 75_000_000, bufferCap: 524288},
	}
)

func appendVarint(out []byte, field, value uint64) []byte {
	out = binary.AppendUvarint(out, field<<3)
	return binary.AppendUvarint(out, value)
}

func appendBytes(out []byte, field uint64, value []byte) []byte {
	out = binary.AppendUvarint(out, field<<3|2)
	out = binary.AppendUvarint(out, uint64(len(value)))
	return append(out, value...)
}

func appendPaddedVarint(out []byte, field, value uint64) []byte {
	out = binary.AppendUvarint(out, field<<3)
	encoded := binary.AppendUvarint(nil, value)
	for len(encoded) < 5 {
		encoded[len(encoded)-1] |= 0x80
		encoded = append(encoded, 0x00)
	}
	return append(out, encoded...)
}

func resEntry(pairIndex uint64) []byte {
	entry := appendVarint(nil, 1, 1)
	entry = appendVarint(entry, 2, pairIndex)
	entry = appendVarint(entry, 3, resEntryCodecCapID)
	return appendVarint(entry, 4, 0)
}

func codecBank(payloadType uint64, features string, trailer uint64, resPairCount int) []byte {
	bank := appendVarint(nil, 1, payloadType)
	for i := range resPairCount {
		bank = appendBytes(bank, 2, resEntry(uint64(1+i%2)))
	}
	bank = appendBytes(bank, 3, []byte(features))
	return appendVarint(bank, 4, trailer)
}

// AVConference writes these video-settings fields: rtpSSRC=1,
// allowRTCPFB=2, videoPayloadCollections=3, tilesPerFrame=6, ltrpEnabled=7,
// pixelFormats=8 and blackFrameOnClearScreenEnabled=12 (AVConference
// 2235.63.5.2, macOS 27.0.1 26A434, UUID 93D8C8C0-8B9C-3A5E-A47D-01B5FAF73E1B,
// -[VCMediaNegotiationBlobVideoSettings writeTo:] 0x1c1014608..0x1c1014908).
// pymobiledevice3 uses the same field numbers and always sends 63 in field 8
// and 1 in field 12 (pymobiledevice3 1146717,
// pymobiledevice3/remote/core_device/media_stream_offer.py:292-302).
func videoBlob(options StreamOptions, timestamp uint64) []byte {
	pixelFormats := uint64(videoPixelFormats)
	if narrowed := uint64(options.PixelFormats) & videoPixelFormats; narrowed != 0 {
		pixelFormats = narrowed
	}
	tiers := videoBitrateTiers
	bufferCap, limit := uint64(screenTierBufferCap), uint64(screenTierLimit)
	switch options.Mode {
	case ModeNearby:
		bufferCap, limit = nearbyTierBufferCap, nearbyTierLimit
	case ModeCoreDevice:
		bufferCap = 0
	}
	if options.MaximumBitrate > 0 && bufferCap != 0 {
		tiers = slices.Clone(tiers)
		for i := range tiers {
			if tiers[i].kind == 0 && tiers[i].bufferCap == bufferCap {
				tiers[i].bps = min(options.MaximumBitrate, limit)
			}
		}
	}
	settings := appendPaddedVarint(nil, 1, uint64(options.SSRC))
	settings = appendVarint(settings, 2, 1)
	settings = appendBytes(settings, 3, codecBank(bankPayloadTypeA, bankFeatures, 1, 4))
	settings = appendBytes(settings, 3, codecBank(bankPayloadTypeB, bankFeatures, 14, 2))
	if options.TilesPerFrame > 1 {
		settings = appendVarint(settings, 6, uint64(options.TilesPerFrame))
	}
	settings = appendVarint(settings, 7, 1)
	settings = appendVarint(settings, 8, pixelFormats)
	settings = appendVarint(settings, 12, 1)
	return encodeMediaBlob(5, settings, tiers, timestamp)
}

func audioBlob(ssrc uint32, timestamp uint64) []byte {
	settings := appendPaddedVarint(nil, 1, uint64(ssrc))
	settings = appendVarint(settings, 2, 0)
	settings = appendVarint(settings, 3, 0)
	settings = appendVarint(settings, 4, 24191)
	settings = appendVarint(settings, 5, 0)
	settings = appendVarint(settings, 6, 0)
	return encodeMediaBlob(3, settings, audioBitrateTiers, timestamp)
}

func encodeMediaBlob(settingsField uint64, settings []byte, tiers []bitrateTier, timestamp uint64) []byte {
	blob := appendVarint(nil, 1, 1)
	blob = appendVarint(blob, 2, 1)
	blob = appendBytes(blob, settingsField, settings)
	blob = appendBytes(blob, 6, []byte(decoderName))
	blob = appendVarint(blob, 8, 0)
	for _, tier := range tiers {
		entry := appendVarint(nil, 1, tier.kind)
		entry = appendVarint(entry, 2, tier.bps)
		if tier.bufferCap != 0 {
			entry = appendVarint(entry, 3, tier.bufferCap)
		}
		blob = appendBytes(blob, 9, entry)
	}
	blob = appendVarint(blob, 13, timestamp)
	blob = appendVarint(blob, 14, 2)
	blob = appendVarint(blob, 16, 0)
	return appendVarint(blob, 18, 1)
}

func remoteEndpointInfo() []byte {
	// idevice encodes call_id and client_version in fields 1 and 2 (idevice
	// d32c818,
	// idevice/src/services/core_device/display_stream/negotiation.rs:128-142).
	info := appendVarint(nil, 1, 0)
	info = appendVarint(info, 2, 1)
	info = appendBytes(info, 3, []byte(hostModel))
	info = appendBytes(info, 4, []byte(hostFrameworkVersion))
	return appendBytes(info, 5, []byte(hostBuild))
}

type negotiatorOffer struct {
	RemoteEndpointInfo []byte `plist:"avcMediaStreamOptionRemoteEndpointInfo"`
	NegotiatorMode     int    `plist:"avcMediaStreamNegotiatorMode"`
	MediaBlob          []byte `plist:"avcMediaStreamNegotiatorMediaBlob"`
	CallID             string `plist:"avcMediaStreamOptionCallID"`
}

func encodeOffer(streamType StreamType, options StreamOptions, now time.Time) []byte {
	var mode int
	var blob []byte
	switch streamType {
	case Video:
		mode, blob = int(options.Mode), videoBlob(options, ntpTimestamp(now))
	case Audio:
		mode, blob = negotiatorModeAudio, audioBlob(options.SSRC, ntpTimestamp(now))
	}
	data, err := plist.Marshal(negotiatorOffer{
		RemoteEndpointInfo: remoteEndpointInfo(),
		NegotiatorMode:     mode,
		MediaBlob:          compressMediaBlob(blob),
		CallID:             strings.ToUpper(uuid.NewString()),
	}, plist.BinaryFormat)
	if err != nil {
		panic(err)
	}
	return data
}

func compressMediaBlob(blob []byte) []byte {
	var compressed bytes.Buffer
	writer, _ := zlib.NewWriterLevel(&compressed, zlib.BestCompression)
	_, _ = writer.Write(blob)
	_ = writer.Close()
	return compressed.Bytes()
}

func ntpTimestamp(now time.Time) uint64 {
	const ntpEpochOffset = 2208988800
	seconds := uint64(now.Unix() + ntpEpochOffset)
	fraction := uint64(now.Nanosecond()) << 32 / uint64(time.Second)
	return seconds<<32 | fraction
}
