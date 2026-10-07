// The wire format follows go-ios (ios/xpc, MIT, Copyright (c) 2019
// danielpaulus), idevice (src/xpc, MIT, Jackson Coxson) and pymobiledevice3
// (remote/xpc_message.py).
package xpc

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"maps"
	"math"
	"reflect"
	"slices"
	"time"

	"github.com/google/uuid"
)

const (
	wrapperMagic  = 0x29b00b92
	bodyMagic     = 0x42133742
	bodyVersion   = 5
	wrapperLength = 24
)

const (
	FlagAlwaysSet           = 0x00000001
	FlagData                = 0x00000100
	FlagWantingReply        = 0x00010000
	FlagFileTransferRequest = 0x00100000
	FlagInitHandshake       = 0x00400000
)

const (
	typeNull         = 0x00001000
	typeBool         = 0x00002000
	typeInt64        = 0x00003000
	typeUint64       = 0x00004000
	typeDouble       = 0x00005000
	typeDate         = 0x00007000
	typeData         = 0x00008000
	typeString       = 0x00009000
	typeUUID         = 0x0000a000
	typeArray        = 0x0000e000
	typeDictionary   = 0x0000f000
	typeFileTransfer = 0x0001a000
)

type Message struct {
	Flags uint32
	ID    uint64
	Body  map[string]any
	size  int
}

type FileTransfer struct {
	ID   uint64
	Size uint64
}

func Marshal(message Message) ([]byte, error) {
	out := binary.LittleEndian.AppendUint32(nil, wrapperMagic)
	out = binary.LittleEndian.AppendUint32(out, message.Flags)
	out = binary.LittleEndian.AppendUint64(out, 0)
	out = binary.LittleEndian.AppendUint64(out, message.ID)
	if message.Body == nil {
		return out, nil
	}
	out = binary.LittleEndian.AppendUint32(out, bodyMagic)
	out = binary.LittleEndian.AppendUint32(out, bodyVersion)
	out, err := appendObject(out, message.Body)
	if err != nil {
		return nil, err
	}
	binary.LittleEndian.PutUint64(out[8:], uint64(len(out)-wrapperLength))
	return out, nil
}

func appendObject(out []byte, value any) ([]byte, error) {
	switch value := value.(type) {
	case nil:
		return binary.LittleEndian.AppendUint32(out, typeNull), nil
	case bool:
		out = binary.LittleEndian.AppendUint32(out, typeBool)
		if value {
			return append(out, 1, 0, 0, 0), nil
		}
		return append(out, 0, 0, 0, 0), nil
	case int:
		return appendInt64(out, int64(value)), nil
	case int8:
		return appendInt64(out, int64(value)), nil
	case int16:
		return appendInt64(out, int64(value)), nil
	case int32:
		return appendInt64(out, int64(value)), nil
	case int64:
		return appendInt64(out, value), nil
	case uint:
		return appendUint64(out, uint64(value)), nil
	case uint8:
		return appendUint64(out, uint64(value)), nil
	case uint16:
		return appendUint64(out, uint64(value)), nil
	case uint32:
		return appendUint64(out, uint64(value)), nil
	case uint64:
		return appendUint64(out, value), nil
	case float32:
		return appendDouble(out, float64(value)), nil
	case float64:
		return appendDouble(out, value), nil
	case time.Time:
		out = binary.LittleEndian.AppendUint32(out, typeDate)
		return binary.LittleEndian.AppendUint64(out, uint64(value.UnixNano())), nil
	case []byte:
		out = binary.LittleEndian.AppendUint32(out, typeData)
		out = binary.LittleEndian.AppendUint32(out, uint32(len(value)))
		return pad(append(out, value...), len(value)), nil
	case string:
		out = binary.LittleEndian.AppendUint32(out, typeString)
		out = binary.LittleEndian.AppendUint32(out, uint32(len(value)+1))
		return pad(append(append(out, value...), 0), len(value)+1), nil
	case uuid.UUID:
		out = binary.LittleEndian.AppendUint32(out, typeUUID)
		return append(out, value[:]...), nil
	case FileTransfer:
		out = binary.LittleEndian.AppendUint32(out, typeFileTransfer)
		out = binary.LittleEndian.AppendUint64(out, value.ID)
		return appendObject(out, map[string]any{"s": value.Size})
	case map[string]any:
		out, start := beginContainer(out, typeDictionary, len(value))
		for _, key := range slices.Sorted(maps.Keys(value)) {
			out = pad(append(append(out, key...), 0), len(key)+1)
			var err error
			out, err = appendObject(out, value[key])
			if err != nil {
				return nil, fmt.Errorf("%s: %w", key, err)
			}
		}
		return endContainer(out, start), nil
	case []any:
		out, start := beginContainer(out, typeArray, len(value))
		for i, element := range value {
			var err error
			out, err = appendObject(out, element)
			if err != nil {
				return nil, fmt.Errorf("[%d]: %w", i, err)
			}
		}
		return endContainer(out, start), nil
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Slice, reflect.Array:
		elements := make([]any, reflected.Len())
		for i := range elements {
			elements[i] = reflected.Index(i).Interface()
		}
		return appendObject(out, elements)
	case reflect.Map:
		if reflected.Type().Key().Kind() == reflect.String {
			entries := make(map[string]any, reflected.Len())
			iterator := reflected.MapRange()
			for iterator.Next() {
				entries[iterator.Key().String()] = iterator.Value().Interface()
			}
			return appendObject(out, entries)
		}
	}
	return nil, fmt.Errorf("xpc: cannot encode %T", value)
}

func appendInt64(out []byte, value int64) []byte {
	out = binary.LittleEndian.AppendUint32(out, typeInt64)
	return binary.LittleEndian.AppendUint64(out, uint64(value))
}

func appendUint64(out []byte, value uint64) []byte {
	out = binary.LittleEndian.AppendUint32(out, typeUint64)
	return binary.LittleEndian.AppendUint64(out, value)
}

func appendDouble(out []byte, value float64) []byte {
	out = binary.LittleEndian.AppendUint32(out, typeDouble)
	return binary.LittleEndian.AppendUint64(out, math.Float64bits(value))
}

// idevice and pymobiledevice3 include the count word in array/container
// lengths; go-ios 273d3e06 omits it from its encoded array length.
func beginContainer(out []byte, kind uint32, count int) ([]byte, int) {
	out = binary.LittleEndian.AppendUint32(out, kind)
	out = binary.LittleEndian.AppendUint32(out, 0)
	start := len(out)
	return binary.LittleEndian.AppendUint32(out, uint32(count)), start
}

func endContainer(out []byte, start int) []byte {
	binary.LittleEndian.PutUint32(out[start-4:], uint32(len(out)-start))
	return out
}

func pad(out []byte, length int) []byte {
	for length%4 != 0 {
		out = append(out, 0)
		length++
	}
	return out
}

var errShort = errors.New("xpc: message is truncated")

func Unmarshal(data []byte) (Message, int, error) {
	if len(data) < wrapperLength {
		return Message{}, 0, errShort
	}
	if magic := binary.LittleEndian.Uint32(data); magic != wrapperMagic {
		return Message{}, 0, fmt.Errorf("xpc: bad message magic 0x%08x", magic)
	}
	message := Message{
		Flags: binary.LittleEndian.Uint32(data[4:]),
		ID:    binary.LittleEndian.Uint64(data[16:]),
	}
	bodyLength := binary.LittleEndian.Uint64(data[8:])
	if bodyLength > math.MaxInt32 {
		return Message{}, 0, fmt.Errorf("xpc: body length %d is too large", bodyLength)
	}
	total := wrapperLength + int(bodyLength)
	if len(data) < total {
		return Message{}, 0, errShort
	}
	if bodyLength == 0 {
		return message, total, nil
	}
	body := data[wrapperLength:total]
	if len(body) < 8 {
		return Message{}, 0, fmt.Errorf("xpc: body length %d is too small", len(body))
	}
	if magic := binary.LittleEndian.Uint32(body); magic != bodyMagic {
		return Message{}, 0, fmt.Errorf("xpc: bad body magic 0x%08x", magic)
	}
	if version := binary.LittleEndian.Uint32(body[4:]); version != bodyVersion {
		return Message{}, 0, fmt.Errorf("xpc: unsupported body version %d", version)
	}
	decoder := &decoder{data: body[8:]}
	value, err := decoder.object(0)
	if err != nil {
		return Message{}, 0, err
	}
	dictionary, isDictionary := value.(map[string]any)
	if !isDictionary {
		return Message{}, 0, fmt.Errorf("xpc: message body is %T, not a dictionary", value)
	}
	message.Body = dictionary
	return message, total, nil
}

type decoder struct {
	data []byte
}

const maxDepth = 64

func (d *decoder) take(n int) ([]byte, error) {
	if n < 0 || n > len(d.data) {
		return nil, fmt.Errorf("xpc: object needs %d bytes, %d left", n, len(d.data))
	}
	value := d.data[:n]
	d.data = d.data[n:]
	return value, nil
}

func (d *decoder) uint32() (uint32, error) {
	value, err := d.take(4)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(value), nil
}

func (d *decoder) uint64() (uint64, error) {
	value, err := d.take(8)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(value), nil
}

func (d *decoder) padded(length int) ([]byte, error) {
	value, err := d.take(length)
	if err != nil {
		return nil, err
	}
	if extra := (4 - length%4) % 4; extra > 0 {
		_, err = d.take(min(extra, len(d.data)))
	}
	return value, err
}

func (d *decoder) object(depth int) (any, error) {
	if depth > maxDepth {
		return nil, errors.New("xpc: objects nested too deeply")
	}
	kind, err := d.uint32()
	if err != nil {
		return nil, err
	}
	switch kind {
	case typeNull:
		return nil, nil
	case typeBool:
		value, err := d.take(4)
		if err != nil {
			return nil, err
		}
		return value[0] != 0, nil
	case typeInt64:
		value, err := d.uint64()
		return int64(value), err
	case typeUint64:
		return d.uint64()
	case typeDouble:
		value, err := d.uint64()
		return math.Float64frombits(value), err
	case typeDate:
		value, err := d.uint64()
		return time.Unix(0, int64(value)), err
	case typeData:
		length, err := d.uint32()
		if err != nil {
			return nil, err
		}
		value, err := d.padded(int(length))
		if err != nil {
			return nil, err
		}
		return append([]byte(nil), value...), nil
	case typeString:
		length, err := d.uint32()
		if err != nil {
			return nil, err
		}
		value, err := d.padded(int(length))
		if err != nil {
			return nil, err
		}
		return string(bytes.TrimRight(value, "\x00")), nil
	case typeUUID:
		value, err := d.take(16)
		if err != nil {
			return nil, err
		}
		return uuid.UUID(value), nil
	case typeArray, typeDictionary:
		length, err := d.uint32()
		if err != nil {
			return nil, err
		}
		content, err := d.take(int(length))
		if err != nil {
			return nil, err
		}
		inner := &decoder{data: content}
		count, err := inner.uint32()
		if err != nil {
			return nil, err
		}
		if int(count) > len(inner.data)/4 {
			return nil, fmt.Errorf("xpc: %d entries do not fit in %d bytes", count, len(inner.data))
		}
		if kind == typeArray {
			values := make([]any, count)
			for i := range values {
				values[i], err = inner.object(depth + 1)
				if err != nil {
					return nil, fmt.Errorf("[%d]: %w", i, err)
				}
			}
			return values, nil
		}
		values := make(map[string]any, count)
		for range count {
			key, err := inner.key()
			if err != nil {
				return nil, err
			}
			values[key], err = inner.object(depth + 1)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", key, err)
			}
		}
		return values, nil
	case typeFileTransfer:
		id, err := d.uint64()
		if err != nil {
			return nil, err
		}
		value, err := d.object(depth + 1)
		if err != nil {
			return nil, err
		}
		dictionary, _ := value.(map[string]any)
		size, isSize := dictionary["s"].(uint64)
		if !isSize {
			return nil, fmt.Errorf("xpc: file transfer without size: %v", value)
		}
		return FileTransfer{ID: id, Size: size}, nil
	}
	return nil, fmt.Errorf("xpc: unknown object type 0x%08x", kind)
}

func (d *decoder) key() (string, error) {
	end := bytes.IndexByte(d.data, 0)
	if end < 0 {
		return "", errors.New("xpc: dictionary key is not terminated")
	}
	key := string(d.data[:end])
	_, err := d.padded(end + 1)
	return key, err
}
