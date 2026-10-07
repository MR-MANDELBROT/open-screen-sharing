package media

import (
	"encoding/binary"
	"errors"
)

var errProtobuf = errors.New("media: malformed protobuf")

const (
	protoVarint  = 0
	protoFixed64 = 1
	protoBytes   = 2
	protoFixed32 = 5
)

type protoField struct {
	number   uint64
	wireType uint8
	value    uint64
	bytes    []byte
}

type protoMessage []protoField

func (m *protoMessage) addUint(number uint64, value uint64) {
	*m = append(*m, protoField{number: number, wireType: protoVarint, value: value})
}

func (m *protoMessage) addBytes(number uint64, value []byte) {
	*m = append(*m, protoField{number: number, wireType: protoBytes, bytes: value})
}

func (m *protoMessage) addMessage(number uint64, value protoMessage) {
	m.addBytes(number, value.encode())
}

func (m protoMessage) encode() []byte {
	var out []byte
	for _, field := range m {
		out = binary.AppendUvarint(out, field.number<<3|uint64(field.wireType))
		switch field.wireType {
		case protoVarint:
			out = binary.AppendUvarint(out, field.value)
		case protoFixed64:
			out = binary.LittleEndian.AppendUint64(out, field.value)
		case protoFixed32:
			out = binary.LittleEndian.AppendUint32(out, uint32(field.value))
		case protoBytes:
			out = binary.AppendUvarint(out, uint64(len(field.bytes)))
			out = append(out, field.bytes...)
		}
	}
	return out
}

func parseProto(data []byte) (protoMessage, error) {
	var message protoMessage
	for len(data) > 0 {
		tag, n := binary.Uvarint(data)
		if n <= 0 {
			return nil, errProtobuf
		}
		data = data[n:]
		field := protoField{number: tag >> 3, wireType: uint8(tag & 7)}
		switch field.wireType {
		case protoVarint:
			field.value, n = binary.Uvarint(data)
			if n <= 0 {
				return nil, errProtobuf
			}
			data = data[n:]
		case protoFixed64:
			if len(data) < 8 {
				return nil, errProtobuf
			}
			field.value = binary.LittleEndian.Uint64(data)
			data = data[8:]
		case protoFixed32:
			if len(data) < 4 {
				return nil, errProtobuf
			}
			field.value = uint64(binary.LittleEndian.Uint32(data))
			data = data[4:]
		case protoBytes:
			length, lengthSize := binary.Uvarint(data)
			if lengthSize <= 0 || length > uint64(len(data)-lengthSize) {
				return nil, errProtobuf
			}
			field.bytes = data[lengthSize : lengthSize+int(length)]
			data = data[lengthSize+int(length):]
		default:
			return nil, errProtobuf
		}
		message = append(message, field)
	}
	return message, nil
}

func (m protoMessage) uint(number uint64) (uint64, bool) {
	for _, field := range m {
		if field.number == number && field.wireType == protoVarint {
			return field.value, true
		}
	}
	return 0, false
}

func (m protoMessage) bytes(number uint64) ([]byte, bool) {
	for _, field := range m {
		if field.number == number && field.wireType == protoBytes {
			return field.bytes, true
		}
	}
	return nil, false
}

func (m protoMessage) messages(number uint64) []protoMessage {
	var result []protoMessage
	for _, field := range m {
		if field.number != number || field.wireType != protoBytes {
			continue
		}
		nested, err := parseProto(field.bytes)
		if err == nil {
			result = append(result, nested)
		}
	}
	return result
}
