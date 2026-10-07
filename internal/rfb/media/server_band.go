package media

import "encoding/binary"

// VideoProcessing reads the feature trailer from the last VCL NAL and skips
// its emulation-prevention bytes (macOS 26A434 and 26B5101f).
const (
	controlByteChain = 0x80
	// VideoProcessing's POS:5 field has three bytes; the first two encode
	// x/16 and y/16 (macOS 26A434 and 26B5101f).
	controlBytePosition = 1 << 5
	controlByteKnown    = 0x03
	positionUnit        = 16

	bandPositionGrowth = 6
	maxServerBands     = 16
)

type bandPositioner struct {
	base   uint32
	bands  int
	height int
	zeros  [maxServerBands]uint8
}

func newBandPositioner(info ServerVideoInfo) *bandPositioner {
	if info.TilesPerFrame <= 1 || info.TilesPerFrame > maxServerBands {
		return nil
	}
	bands := info.TilesPerFrame
	return &bandPositioner{
		base:   info.SSRC,
		bands:  bands,
		height: (info.Height + bands*positionUnit - 1) / (bands * positionUnit) * positionUnit,
	}
}

func (p *bandPositioner) rewrite(packet []byte, header rtpPacket) []byte {
	band := header.ssrc - p.base
	if band >= uint32(p.bands) || packet[0]&0x20 != 0 {
		return packet
	}

	offset := header.payloadOffset
	payload := packet[offset:]
	if len(payload) < 5 {
		return packet
	}
	switch nalType := hevcNALType(payload); {
	case nalType < hevcNALAggregation:
		p.zeros[band] = 0
		if nalType == hevcNALSPS {
			p.readHeight(payload[4:])
		}
		if header.marker && nalType < 32 {
			return p.place(packet, offset+4, band, 0, -1)
		}
	case nalType == hevcNALAggregation:
		p.zeros[band] = 0
		for at := 4; at+2 <= len(payload); {
			size := int(binary.BigEndian.Uint16(payload[at:]))
			if size < 3 || at+2+size > len(payload) {
				return packet
			}
			unit := payload[at+2 : at+2+size]
			if hevcNALType(unit) == hevcNALSPS {
				p.readHeight(unit[2:])
			}
			if at+2+size == len(payload) && header.marker && hevcNALType(unit) < 32 {
				return p.place(packet, offset+at+4, band, 0, offset+at)
			}
			at += 2 + size
		}
	case nalType == hevcNALFragment:
		fragment := payload[2]
		if len(payload) < 6 {
			return packet
		}
		var carried uint8
		if fragment&0x80 == 0 {
			carried = p.zeros[band]
		}
		if fragment&0x40 == 0 {
			p.zeros[band] = endingZeros(payload[5:], carried)
			return packet
		}
		p.zeros[band] = 0
		if header.marker && int(fragment&0x3f) < 32 {
			return p.place(packet, offset+5, band, carried, -1)
		}
	}
	return packet
}

func (p *bandPositioner) place(packet []byte, data int, band uint32, carried uint8, sizeField int) []byte {
	if data >= len(packet) {
		return packet
	}
	control := packet[len(packet)-1]
	bits := control &^ controlByteChain
	if bits&^controlByteKnown != 0 || (control&controlByteChain != 0 && bits != 0) {
		return packet
	}
	y := int(band) * p.height
	if y%positionUnit != 0 || y/positionUnit > 0xff {
		return packet
	}

	end := len(packet) - 1
	zeros := endingZeros(packet[data:end], carried)
	var written [1 + bandPositionGrowth]byte
	n := 0
	for _, value := range [...]byte{0, byte(y / positionUnit), 0, bits | controlBytePosition} {
		if zeros >= 2 && value <= 3 {
			written[n] = 3
			n++
			zeros = 0
		}
		written[n] = value
		n++
		if value == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	packet = append(packet[:end], written[:n]...)
	if sizeField >= 0 {
		size := binary.BigEndian.Uint16(packet[sizeField:])
		binary.BigEndian.PutUint16(packet[sizeField:], size+uint16(n-1))
	}
	return packet
}

func endingZeros(data []byte, carried uint8) uint8 {
	zeros := uint8(0)
	for i := len(data) - 1; i >= 0 && zeros < 2; i-- {
		if data[i] != 0 {
			return zeros
		}
		zeros++
	}
	return min(zeros+carried, 2)
}

func (p *bandPositioner) readHeight(sps []byte) {
	r := newBitReader(unescapeRBSP(sps))
	r.bits(4)
	maxSubLayersMinus1 := int(r.bits(3))
	r.bits(1)
	skipHEVCProfileTierLevel(r, maxSubLayersMinus1)
	r.unsignedGolomb()
	if r.unsignedGolomb() == 3 {
		r.bits(1)
	}
	r.unsignedGolomb()
	height := int(r.unsignedGolomb())
	if !r.overrun && height > 0 {
		p.height = height
	}
}
