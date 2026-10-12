package deviceserver

import "encoding/binary"

// In modes 5, 7 and 9, the device's streams put VPS/SPS/PPS before each IDR (observed on iPad17,1, iPadOS 27.2 24B5099f).

const (
	hevcNALSPS         = 33
	hevcNALAggregation = 48
)

const largestPicture = 16384

func hevcNALType(nal []byte) int {
	return int(nal[0]>>1) & 0x3f
}

func rtpPayload(packet []byte) []byte {
	if len(packet) < 12 || packet[0]>>6 != 2 {
		return nil
	}
	offset := 12 + 4*int(packet[0]&0x0f)
	if packet[0]&0x10 != 0 {
		if len(packet) < offset+4 {
			return nil
		}
		offset += 4 + 4*int(binary.BigEndian.Uint16(packet[offset+2:]))
	}
	end := len(packet)
	if packet[0]&0x20 != 0 {
		end -= int(packet[end-1])
	}
	if offset >= end {
		return nil
	}
	return packet[offset:end]
}

func hevcSPS(payload []byte) []byte {
	if len(payload) < 3 {
		return nil
	}
	switch hevcNALType(payload) {
	case hevcNALSPS:
		return payload
	case hevcNALAggregation:
		body := payload[2:]
		for len(body) >= 2 {
			size := int(binary.BigEndian.Uint16(body))
			if size < 3 || len(body) < 2+size {
				return nil
			}
			nal := body[2 : 2+size]
			if hevcNALType(nal) == hevcNALSPS {
				return nal
			}
			body = body[2+size:]
		}
	}
	return nil
}
