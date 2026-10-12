package display

import "encoding/binary"

const rtcpPayloadSpecificFeedback = 206

func isRTCP(packet []byte) bool {
	return len(packet) >= 2 && packet[1] >= 192 && packet[1] <= 223
}

// A probe sent PLI+FIR without a leading RR at 4.000583709 s; VPS/SPS/PPS
// and an IDR began arriving at 4.0128 s (observed on iPad17,1, build
// 24B5089g).
func keyFrameRequest(senderSSRC, mediaSSRC uint32, sequence uint8) []byte {
	packet := []byte{0x81, rtcpPayloadSpecificFeedback, 0, 2}
	packet = binary.BigEndian.AppendUint32(packet, senderSSRC)
	packet = binary.BigEndian.AppendUint32(packet, mediaSSRC)
	packet = append(packet, 0x84, rtcpPayloadSpecificFeedback, 0, 4)
	packet = binary.BigEndian.AppendUint32(packet, senderSSRC)
	packet = binary.BigEndian.AppendUint32(packet, 0)
	packet = binary.BigEndian.AppendUint32(packet, mediaSSRC)
	return append(packet, sequence, 0, 0, 0)
}
