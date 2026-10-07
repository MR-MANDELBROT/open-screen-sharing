package media

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"unicode/utf16"
)

var errPlist = errors.New("media: malformed binary property list")

const (
	plistVisitLimit = 1024
	plistTextLimit  = 1 << 20
)

type plistEntry struct {
	key   string
	value any
}

func encodeBinaryPlist(entries []plistEntry) []byte {
	objectCount := 1 + 2*len(entries)
	out := []byte("bplist00")
	offsets := make([]int, 0, objectCount)
	writeMarker := func(kind byte, count int) {
		if count < 15 {
			out = append(out, kind|byte(count))
			return
		}
		out = append(out, kind|0x0f)
		switch {
		case count <= math.MaxUint8:
			out = append(out, 0x10, byte(count))
		case count <= math.MaxUint16:
			out = append(out, 0x11)
			out = binary.BigEndian.AppendUint16(out, uint16(count))
		default:
			out = append(out, 0x12)
			out = binary.BigEndian.AppendUint32(out, uint32(count))
		}
	}
	offsets = append(offsets, len(out))
	writeMarker(0xd0, len(entries))
	for i := range entries {
		out = append(out, byte(1+i))
	}
	for i := range entries {
		out = append(out, byte(1+len(entries)+i))
	}
	for _, entry := range entries {
		offsets = append(offsets, len(out))
		writeMarker(0x50, len(entry.key))
		out = append(out, entry.key...)
	}
	for _, entry := range entries {
		offsets = append(offsets, len(out))
		switch value := entry.value.(type) {
		case []byte:
			writeMarker(0x40, len(value))
			out = append(out, value...)
		case string:
			writeMarker(0x50, len(value))
			out = append(out, value...)
		case int64:
			switch {
			case value >= 0 && value <= math.MaxUint8:
				out = append(out, 0x10, byte(value))
			case value >= 0 && value <= math.MaxUint16:
				out = append(out, 0x11)
				out = binary.BigEndian.AppendUint16(out, uint16(value))
			case value >= 0 && value <= math.MaxUint32:
				out = append(out, 0x12)
				out = binary.BigEndian.AppendUint32(out, uint32(value))
			default:
				out = append(out, 0x13)
				out = binary.BigEndian.AppendUint64(out, uint64(value))
			}
		}
	}
	tableOffset := len(out)
	offsetSize := 1
	if tableOffset > math.MaxUint8 {
		offsetSize = 2
	}
	for _, offset := range offsets {
		if offsetSize == 1 {
			out = append(out, byte(offset))
		} else {
			out = binary.BigEndian.AppendUint16(out, uint16(offset))
		}
	}
	out = append(out, 0, 0, 0, 0, 0, 0, byte(offsetSize), 1)
	out = binary.BigEndian.AppendUint64(out, uint64(objectCount))
	out = binary.BigEndian.AppendUint64(out, 0)
	out = binary.BigEndian.AppendUint64(out, uint64(tableOffset))
	return out
}

type plistDecoder struct {
	data       []byte
	offsets    []int
	objectSize int
	depth      int
	visits     int
	text       int
}

func decodeBinaryPlist(data []byte) (any, error) {
	if len(data) < 8+32 || !bytes.HasPrefix(data, []byte("bplist00")) {
		return nil, errPlist
	}
	trailer := data[len(data)-32:]
	offsetSize := int(trailer[6])
	objectSize := int(trailer[7])
	objectCount := binary.BigEndian.Uint64(trailer[8:16])
	topObject := binary.BigEndian.Uint64(trailer[16:24])
	tableOffset := binary.BigEndian.Uint64(trailer[24:32])
	if offsetSize < 1 || offsetSize > 8 || objectSize < 1 || objectSize > 8 ||
		objectCount == 0 || objectCount > 1<<16 || topObject >= objectCount ||
		tableOffset+objectCount*uint64(offsetSize) > uint64(len(data)-32) {
		return nil, errPlist
	}
	decoder := &plistDecoder{data: data[:len(data)-32], objectSize: objectSize}
	decoder.offsets = make([]int, objectCount)
	for i := range decoder.offsets {
		start := int(tableOffset) + i*offsetSize
		offset := readSizedUint(data[start : start+offsetSize])
		if offset >= tableOffset {
			return nil, errPlist
		}
		decoder.offsets[i] = int(offset)
	}
	return decoder.object(int(topObject))
}

func readSizedUint(data []byte) uint64 {
	var value uint64
	for _, b := range data {
		value = value<<8 | uint64(b)
	}
	return value
}

func (d *plistDecoder) object(index int) (any, error) {
	d.visits++
	if index < 0 || index >= len(d.offsets) || d.depth > 16 || d.visits > plistVisitLimit {
		return nil, errPlist
	}
	d.depth++
	defer func() { d.depth-- }()
	position := d.offsets[index]
	marker := d.data[position]
	kind := marker >> 4
	info := int(marker & 0x0f)
	position++
	switch kind {
	case 0x0:
		switch marker {
		case 0x08:
			return false, nil
		case 0x09:
			return true, nil
		default:
			return nil, nil
		}
	case 0x1:
		size := 1 << info
		if size > 16 || position+size > len(d.data) {
			return nil, errPlist
		}
		if size == 16 {
			position += 8
			size = 8
		}
		return int64(readSizedUint(d.data[position : position+size])), nil
	case 0x2, 0x3:
		size := 8
		if kind == 0x2 {
			size = 1 << info
		}
		if position+size > len(d.data) {
			return nil, errPlist
		}
		switch size {
		case 4:
			return float64(math.Float32frombits(binary.BigEndian.Uint32(d.data[position:]))), nil
		case 8:
			return math.Float64frombits(binary.BigEndian.Uint64(d.data[position:])), nil
		}
		return nil, errPlist
	case 0x4, 0x5, 0x6:
		count, start, err := d.count(info, position)
		if err != nil {
			return nil, err
		}
		unitSize := 1
		if kind == 0x6 {
			unitSize = 2
		}
		if start+unitSize*count > len(d.data) {
			return nil, errPlist
		}
		content := d.data[start : start+unitSize*count]
		if kind == 0x4 {
			return content, nil
		}
		d.text += len(content)
		if d.text > plistTextLimit {
			return nil, errPlist
		}
		if kind == 0x5 {
			return string(content), nil
		}
		units := make([]uint16, count)
		for i := range units {
			units[i] = binary.BigEndian.Uint16(content[2*i:])
		}
		return string(utf16.Decode(units)), nil
	case 0xa:
		count, start, err := d.count(info, position)
		if err != nil {
			return nil, err
		}
		references, err := d.references(start, count)
		if err != nil {
			return nil, err
		}
		array := make([]any, 0, count)
		for _, reference := range references {
			var value any
			value, err = d.object(reference)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		return array, nil
	case 0xd:
		count, start, err := d.count(info, position)
		if err != nil {
			return nil, err
		}
		references, err := d.references(start, 2*count)
		if err != nil {
			return nil, err
		}
		dictionary := make(map[string]any, count)
		for i := range count {
			var key, value any
			key, err = d.object(references[i])
			if err != nil {
				return nil, err
			}
			keyString, isString := key.(string)
			if !isString {
				return nil, errPlist
			}
			value, err = d.object(references[count+i])
			if err != nil {
				return nil, err
			}
			dictionary[keyString] = value
		}
		return dictionary, nil
	}
	return nil, errPlist
}

func (d *plistDecoder) count(info int, position int) (int, int, error) {
	if info != 0x0f {
		return info, position, nil
	}
	if position >= len(d.data) || d.data[position]>>4 != 0x1 {
		return 0, 0, errPlist
	}
	size := 1 << (d.data[position] & 0x0f)
	position++
	if position+size > len(d.data) || size > 8 {
		return 0, 0, errPlist
	}
	count := readSizedUint(d.data[position : position+size])
	if count > uint64(len(d.data)) {
		return 0, 0, errPlist
	}
	return int(count), position + size, nil
}

func (d *plistDecoder) references(position int, count int) ([]int, error) {
	if position+count*d.objectSize > len(d.data) {
		return nil, errPlist
	}
	references := make([]int, count)
	for i := range references {
		start := position + i*d.objectSize
		references[i] = int(readSizedUint(d.data[start : start+d.objectSize]))
	}
	return references, nil
}
