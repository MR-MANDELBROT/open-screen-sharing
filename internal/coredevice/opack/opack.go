// The decoder, including back references, is ported from idevice
// (https://github.com/jkcoxson/idevice, idevice/src/remote_pairing/opack.rs),
// MIT License, Copyright (c) Jackson Coxson.
package opack

import (
	"bytes"
	"encoding/binary"
	"maps"
	"math"
	"reflect"
	"slices"
	"strconv"
	"time"

	"github.com/google/uuid"
	E "github.com/sagernet/sing/common/exceptions"
)

var appleEpoch = time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)

func Marshal(v any) ([]byte, error) {
	var buffer bytes.Buffer
	err := encode(&buffer, v)
	if err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func encode(buffer *bytes.Buffer, v any) error {
	switch value := v.(type) {
	case string:
		encodeSized(buffer, 0x40, 0x61, len(value))
		buffer.WriteString(value)
	case []byte:
		encodeSized(buffer, 0x70, 0x91, len(value))
		buffer.Write(value)
	case map[string]any:
		if len(value) < 15 {
			buffer.WriteByte(0xE0 + byte(len(value)))
		} else {
			buffer.WriteByte(0xEF)
		}
		for _, key := range slices.Sorted(maps.Keys(value)) {
			encodeSized(buffer, 0x40, 0x61, len(key))
			buffer.WriteString(key)
			err := encode(buffer, value[key])
			if err != nil {
				return E.Cause(err, "encode ", key)
			}
		}
		if len(value) >= 15 {
			buffer.WriteByte(0x03)
		}
	default:
		return E.New("opack: unsupported type ", reflect.TypeOf(v))
	}
	return nil
}

func encodeSized(buffer *bytes.Buffer, inlineBase byte, sizedBase byte, length int) {
	switch {
	case length <= 0x20:
		buffer.WriteByte(inlineBase + byte(length))
	case length <= math.MaxUint8:
		buffer.WriteByte(sizedBase)
		buffer.WriteByte(byte(length))
	case length <= math.MaxUint16:
		buffer.WriteByte(sizedBase + 1)
		buffer.Write(binary.LittleEndian.AppendUint16(nil, uint16(length)))
	case length <= 0xFFFFFF:
		buffer.WriteByte(sizedBase + 2)
		buffer.Write([]byte{byte(length), byte(length >> 8), byte(length >> 16)})
	default:
		buffer.WriteByte(sizedBase + 3)
		buffer.Write(binary.LittleEndian.AppendUint32(nil, uint32(length)))
	}
}

func Unmarshal(data []byte) (any, error) {
	decoder := &decoder{data: data}
	value, err := decoder.decode()
	if err != nil {
		return nil, err
	}
	if decoder.offset != len(data) {
		return nil, E.New("opack: ", len(data)-decoder.offset, " unexpected trailing bytes")
	}
	return value, nil
}

type decoder struct {
	data    []byte
	offset  int
	objects []any
}

func (d *decoder) read(n int) ([]byte, error) {
	if n < 0 || len(d.data)-d.offset < n {
		return nil, E.New("opack: unexpected end of data at offset ", d.offset)
	}
	value := d.data[d.offset : d.offset+n]
	d.offset += n
	return value, nil
}

func (d *decoder) readUint(n int) (uint64, error) {
	data, err := d.read(n)
	if err != nil {
		return 0, err
	}
	var value uint64
	for i := n - 1; i >= 0; i-- {
		value = value<<8 | uint64(data[i])
	}
	return value, nil
}

func (d *decoder) remember(value any) any {
	if !slices.ContainsFunc(d.objects, func(object any) bool {
		return equalScalar(object, value)
	}) {
		d.objects = append(d.objects, value)
	}
	return value
}

func equalScalar(a, b any) bool {
	switch av := a.(type) {
	case []byte:
		bv, isBytes := b.([]byte)
		return isBytes && bytes.Equal(av, bv)
	case time.Time:
		bv, isTime := b.(time.Time)
		return isTime && av.Equal(bv)
	default:
		if _, isBytes := b.([]byte); isBytes {
			return false
		}
		return a == b
	}
}

func (d *decoder) decode() (any, error) {
	tagData, err := d.read(1)
	if err != nil {
		return nil, err
	}
	tag := tagData[0]
	switch {
	case tag == 0x01:
		return true, nil
	case tag == 0x02:
		return false, nil
	case tag == 0x03:
		return nil, E.New("opack: unexpected terminator at offset ", d.offset-1)
	case tag == 0x04:
		return nil, nil
	case tag == 0x05:
		data, err := d.read(16)
		if err != nil {
			return nil, err
		}
		return d.remember(uuid.UUID(data)), nil
	case tag == 0x06:
		bits, err := d.readUint(8)
		if err != nil {
			return nil, err
		}
		seconds := math.Float64frombits(bits)
		return d.remember(appleEpoch.Add(time.Duration(seconds * float64(time.Second)))), nil
	case tag >= 0x08 && tag <= 0x2F:
		return int64(tag) - 8, nil
	case tag >= 0x30 && tag <= 0x33:
		value, err := d.readUint(1 << (tag - 0x30))
		if err != nil {
			return nil, err
		}
		return d.remember(int64(value)), nil
	case tag == 0x35:
		bits, err := d.readUint(4)
		if err != nil {
			return nil, err
		}
		return d.remember(float64(math.Float32frombits(uint32(bits)))), nil
	case tag == 0x36:
		bits, err := d.readUint(8)
		if err != nil {
			return nil, err
		}
		return d.remember(math.Float64frombits(bits)), nil
	case tag >= 0x40 && tag <= 0x64:
		length, err := d.sizedLength(tag, 0x40, 0x61)
		if err != nil {
			return nil, err
		}
		data, err := d.read(length)
		if err != nil {
			return nil, err
		}
		return d.remember(string(data)), nil
	case tag >= 0x70 && tag <= 0x94:
		length, err := d.sizedLength(tag, 0x70, 0x91)
		if err != nil {
			return nil, err
		}
		data, err := d.read(length)
		if err != nil {
			return nil, err
		}
		return d.remember(bytes.Clone(data)), nil
	case tag >= 0xA0 && tag <= 0xC0:
		return d.lookup(uint64(tag - 0xA0))
	case tag >= 0xC1 && tag <= 0xC4:
		index, err := d.readUint(int(tag - 0xC0))
		if err != nil {
			return nil, err
		}
		return d.lookup(index)
	case tag >= 0xD0 && tag <= 0xDF:
		return d.decodeArray(int(tag-0xD0), tag == 0xDF)
	case tag >= 0xE0 && tag <= 0xEF:
		return d.decodeDictionary(int(tag-0xE0), tag == 0xEF)
	default:
		return nil, E.New("opack: unsupported tag 0x", strconv.FormatUint(uint64(tag), 16), " at offset ", d.offset-1)
	}
}

func (d *decoder) sizedLength(tag byte, inlineBase byte, sizedBase byte) (int, error) {
	if tag < sizedBase {
		return int(tag - inlineBase), nil
	}
	length, err := d.readUint(int(tag-sizedBase) + 1)
	if err != nil {
		return 0, err
	}
	if length > uint64(len(d.data)) {
		return 0, E.New("opack: length ", length, " exceeds data size ", len(d.data))
	}
	return int(length), nil
}

func (d *decoder) lookup(index uint64) (any, error) {
	if index >= uint64(len(d.objects)) {
		return nil, E.New("opack: back reference ", index, " out of range, ", len(d.objects), " objects seen")
	}
	return d.objects[index], nil
}

func (d *decoder) atTerminator() bool {
	return d.offset < len(d.data) && d.data[d.offset] == 0x03
}

func (d *decoder) decodeArray(count int, terminated bool) ([]any, error) {
	array := make([]any, 0, min(count, 15))
	for i := 0; terminated || i < count; i++ {
		if terminated && d.atTerminator() {
			d.offset++
			break
		}
		value, err := d.decode()
		if err != nil {
			return nil, err
		}
		array = append(array, value)
	}
	return array, nil
}

func (d *decoder) decodeDictionary(count int, terminated bool) (map[string]any, error) {
	dictionary := make(map[string]any, min(count, 15))
	for i := 0; terminated || i < count; i++ {
		if terminated && d.atTerminator() {
			d.offset++
			break
		}
		keyValue, err := d.decode()
		if err != nil {
			return nil, err
		}
		key, isString := keyValue.(string)
		if !isString {
			return nil, E.New("opack: dictionary key is ", reflect.TypeOf(keyValue), ", not a string")
		}
		value, err := d.decode()
		if err != nil {
			return nil, E.Cause(err, "decode ", key)
		}
		dictionary[key] = value
	}
	return dictionary, nil
}
