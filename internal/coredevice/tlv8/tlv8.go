package tlv8

import (
	"errors"
	"fmt"
)

const (
	TypeIdentifier    byte = 0x01
	TypeSalt          byte = 0x02
	TypePublicKey     byte = 0x03
	TypeProof         byte = 0x04
	TypeEncryptedData byte = 0x05
	TypeState         byte = 0x06
	TypeError         byte = 0x07
	TypeSignature     byte = 0x0A
	TypeInfo          byte = 0x11
)

const (
	ErrorUnknown        byte = 0x01
	ErrorAuthentication byte = 0x02
	ErrorBackoff        byte = 0x03
	ErrorUnknownPeer    byte = 0x04
	ErrorMaxPeers       byte = 0x05
	ErrorMaxTries       byte = 0x06
)

type Item struct {
	Type  byte
	Value []byte
}

func Encode(items ...Item) []byte {
	var size int
	for _, item := range items {
		size += 2 + len(item.Value) + 2*(len(item.Value)/255)
	}
	data := make([]byte, 0, size)
	for _, item := range items {
		data = appendItem(data, item.Type, item.Value)
	}
	return data
}

func appendItem(data []byte, itemType byte, value []byte) []byte {
	if len(value) == 0 {
		return append(data, itemType, 0)
	}
	for len(value) > 0 {
		length := min(len(value), 255)
		data = append(data, itemType, byte(length))
		data = append(data, value[:length]...)
		value = value[length:]
	}
	return data
}

type Values map[byte][]byte

func Decode(data []byte) (Values, error) {
	values := make(Values)
	for len(data) > 0 {
		if len(data) < 2 {
			return nil, errors.New("truncated item header")
		}
		itemType, length := data[0], int(data[1])
		data = data[2:]
		if len(data) < length {
			return nil, fmt.Errorf("item 0x%02x: length %d exceeds %d remaining bytes", itemType, length, len(data))
		}
		value := data[:length]
		data = data[length:]
		values[itemType] = append(values[itemType], value...)
	}
	return values, nil
}

func (v Values) State() byte {
	value := v[TypeState]
	if len(value) == 0 {
		return 0
	}
	return value[0]
}

func (v Values) Error() (byte, bool) {
	value, loaded := v[TypeError]
	if !loaded {
		return 0, false
	}
	if len(value) == 0 {
		return ErrorUnknown, true
	}
	return value[0], true
}

func ErrorString(code byte) string {
	switch code {
	case ErrorUnknown:
		return "unknown error"
	case ErrorAuthentication:
		return "authentication failed"
	case ErrorBackoff:
		return "too many attempts, try again later"
	case ErrorUnknownPeer:
		return "unknown peer"
	case ErrorMaxPeers:
		return "too many paired peers"
	case ErrorMaxTries:
		return "too many failed attempts"
	default:
		return fmt.Sprint("error ", code)
	}
}
