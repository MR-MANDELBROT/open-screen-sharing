package media

import "time"

const (
	// AVConference on macOS 27.0.1 (26A434) and 27.2 (26B5101f): VCPayloadUtils
	// returns 48000 Hz and 480 samples for PT 101; RemoteDesktopSystemAudio has 2 channels.
	audioSampleRate      = 48000
	audioChannels        = 2
	audioFramesPerPacket = 480
	audioFormatAACELD    = 0x61616365
)

var audioSpecificConfig = []byte{0xf8, 0xe6, 0x50, 0x00}

func audioMagicCookie(specificConfig []byte) []byte {
	decoderSpecific := append([]byte{0x05, byte(len(specificConfig))}, specificConfig...)
	decoderConfig := []byte{
		0x04, byte(13 + len(decoderSpecific)),
		0x40,
		0x15,
		0x00, 0x18, 0x00,
		0, 0, 0, 0,
		0, 0, 0, 0,
	}
	decoderConfig = append(decoderConfig, decoderSpecific...)
	cookie := []byte{0x03, byte(3 + len(decoderConfig)), 0x00, 0x01, 0x00}
	return append(cookie, decoderConfig...)
}

func audioFormat() AudioFormat {
	return AudioFormat{
		FormatID:            audioFormatAACELD,
		SampleRate:          audioSampleRate,
		Channels:            audioChannels,
		FramesPerPacket:     audioFramesPerPacket,
		AudioSpecificConfig: append([]byte(nil), audioSpecificConfig...),
		MagicCookie:         audioMagicCookie(audioSpecificConfig),
	}
}

type audioReceiver struct {
	sink         Sink
	announced    bool
	ssrc         uint32
	haveSequence bool
	next         uint16
	held         map[uint16]heldAudio
	heldSince    time.Time
	timestamps   timestampExtender
	packets      uint64
	lost         uint64
	late         uint64
}

type heldAudio struct {
	packet  rtpPacket
	arrival time.Time
}

const (
	audioReorderWait = 20 * time.Millisecond
	maxHeldAudio     = 32
)

func (r *audioReceiver) push(packet rtpPacket, arrival time.Time) {
	if packet.payloadType != payloadTypeAudio || len(packet.payload) == 0 {
		return
	}
	if !r.announced {
		r.announced = true
		r.sink.AudioFormat(audioFormat())
	}
	if r.haveSequence && packet.ssrc != r.ssrc {
		r.skip(arrival, true)
		r.haveSequence = false
		r.timestamps = timestampExtender{}
	}
	if !r.haveSequence {
		r.haveSequence = true
		r.ssrc = packet.ssrc
		r.next = packet.sequence
		if r.held == nil {
			r.held = make(map[uint16]heldAudio)
		}
	}
	switch {
	case packet.sequence == r.next:
		r.deliver(packet, append([]byte(nil), packet.payload...), 0, arrival)
		r.drain(arrival)
	case sequenceNewer(packet.sequence, r.next):
		if _, held := r.held[packet.sequence]; held {
			return
		}
		if len(r.held) == 0 {
			r.heldSince = arrival
		}
		packet.payload = append([]byte(nil), packet.payload...)
		r.held[packet.sequence] = heldAudio{packet: packet, arrival: arrival}
		if len(r.held) > maxHeldAudio {
			r.skip(arrival, false)
		}
	default:
		r.late++
	}
}

func (r *audioReceiver) tick(now time.Time) {
	if len(r.held) > 0 && now.Sub(r.heldSince) >= audioReorderWait {
		r.skip(now, false)
	}
}

func (r *audioReceiver) skip(now time.Time, all bool) {
	for len(r.held) > 0 {
		var lowest uint16
		first := true
		for sequence := range r.held {
			if first || sequenceNewer(lowest, sequence) {
				lowest = sequence
				first = false
			}
		}
		held := r.held[lowest]
		delete(r.held, lowest)
		r.deliver(held.packet, held.packet.payload, int(lowest-r.next), held.arrival)
		r.drain(now)
		if !all {
			return
		}
	}
}

func (r *audioReceiver) drain(now time.Time) {
	for len(r.held) > 0 {
		held, found := r.held[r.next]
		if !found {
			r.heldSince = now
			return
		}
		delete(r.held, r.next)
		r.deliver(held.packet, held.packet.payload, 0, held.arrival)
	}
}

func (r *audioReceiver) deliver(packet rtpPacket, data []byte, lost int, arrival time.Time) {
	r.next = packet.sequence + 1
	r.packets++
	r.lost += uint64(lost)
	r.sink.AudioPacket(AudioPacket{
		Data:       data,
		Timestamp:  r.timestamps.extend(packet.timestamp),
		Lost:       lost,
		ReceivedAt: arrival,
	})
}
