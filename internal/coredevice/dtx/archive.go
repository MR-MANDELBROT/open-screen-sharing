package dtx

import (
	"encoding/binary"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"unicode/utf16"

	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
	"howett.net/plist"
)

const (
	maxArchiveSize    = maxMessageSize
	maxArchiveObjects = 1 << 16
	maxArchiveDepth   = 64
)

type Object struct {
	ClassName       string
	Fields          map[string]any
	PrimitiveFields []string
}

func MarshalArchive(value any) ([]byte, error) {
	e := archiveEncoder{objects: []any{"$null"}, classes: make(map[string]plist.UID)}
	root, err := e.encode(reflect.ValueOf(value), 0)
	if err != nil {
		return nil, err
	}
	data, err := plist.Marshal(map[string]any{
		"$archiver": "NSKeyedArchiver", "$version": uint64(100000),
		"$objects": e.objects, "$top": map[string]any{"root": root},
	}, plist.BinaryFormat)
	if err != nil {
		return nil, E.Cause(err, "dtx: encode archive")
	}
	return data, nil
}

type archiveEncoder struct {
	objects []any
	classes map[string]plist.UID
}

func (e *archiveEncoder) append(value any) (plist.UID, error) {
	if len(e.objects) >= maxArchiveObjects {
		return 0, E.New("dtx: too many archive objects")
	}
	uid := plist.UID(len(e.objects))
	e.objects = append(e.objects, value)
	return uid, nil
}

func (e *archiveEncoder) class(name string) (plist.UID, error) {
	if uid, ok := e.classes[name]; ok {
		return uid, nil
	}
	uid, err := e.append(map[string]any{"$classname": name, "$classes": []string{name, "NSObject"}})
	if err == nil {
		e.classes[name] = uid
	}
	return uid, err
}

func (e *archiveEncoder) encode(value reflect.Value, depth int) (plist.UID, error) {
	if depth > maxArchiveDepth {
		return 0, E.New("dtx: archive nesting limit exceeded")
	}
	if !value.IsValid() {
		return 0, nil
	}
	if value.Kind() == reflect.Interface {
		if value.IsNil() {
			return 0, nil
		}
		return e.encode(value.Elem(), depth)
	}
	if (value.Kind() == reflect.Map || value.Kind() == reflect.Slice || value.Kind() == reflect.Pointer) && value.IsNil() {
		return 0, nil
	}
	if value.CanInterface() {
		switch object := value.Interface().(type) {
		case Object:
			return e.object(object, depth)
		case *Object:
			return e.object(*object, depth)
		}
	}
	if primitive, ok := archivePrimitive(value); ok {
		return e.append(primitive)
	}
	return e.collection(value, depth)
}

func archivePrimitive(value reflect.Value) (any, bool) {
	switch value.Kind() {
	case reflect.String:
		return value.String(), true
	case reflect.Bool:
		return value.Bool(), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return value.Int(), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return value.Uint(), true
	case reflect.Float32, reflect.Float64:
		return value.Float(), true
	case reflect.Slice:
		if value.Type().Elem().Kind() == reflect.Uint8 {
			return value.Bytes(), true
		}
	}
	return nil, false
}

func (e *archiveEncoder) object(value Object, depth int) (plist.UID, error) {
	for _, name := range value.PrimitiveFields {
		if _, exists := value.Fields[name]; !exists {
			return 0, E.New("dtx: missing primitive archive field ", strconv.Quote(name))
		}
	}
	object := make(map[string]any, len(value.Fields)+1)
	uid, err := e.append(object)
	if err != nil {
		return 0, err
	}
	object["$class"], err = e.class(value.ClassName)
	if err != nil {
		return 0, err
	}
	for _, name := range slices.Sorted(maps.Keys(value.Fields)) {
		field := reflect.ValueOf(value.Fields[name])
		if slices.Contains(value.PrimitiveFields, name) {
			var ok bool
			object[name], ok = archivePrimitive(field)
			if !ok {
				return 0, E.New("dtx: unsupported primitive archive field ", strconv.Quote(name))
			}
		} else {
			object[name], err = e.encode(field, depth+1)
			if err != nil {
				return 0, err
			}
		}
	}
	return uid, nil
}

func (e *archiveEncoder) collection(value reflect.Value, depth int) (plist.UID, error) {
	if value.Kind() != reflect.Map && value.Kind() != reflect.Slice && value.Kind() != reflect.Array {
		return 0, E.New("dtx: unsupported archive value ", value.Type())
	}
	if value.Kind() == reflect.Map && value.Type().Key().Kind() != reflect.String {
		return 0, E.New("dtx: archive dictionary keys must be strings")
	}
	object := make(map[string]any)
	uid, err := e.append(object)
	if err != nil {
		return 0, err
	}
	className := "NSArray"
	if value.Kind() == reflect.Map {
		className = "NSDictionary"
	}
	object["$class"], err = e.class(className)
	if err != nil {
		return 0, err
	}
	values := make([]plist.UID, value.Len())
	if value.Kind() == reflect.Map {
		keys := value.MapKeys()
		common.SortBy(keys, reflect.Value.String)
		keyUIDs := make([]plist.UID, len(keys))
		for i, key := range keys {
			keyUIDs[i], err = e.encode(key, depth+1)
			if err != nil {
				return 0, err
			}
			values[i], err = e.encode(value.MapIndex(key), depth+1)
			if err != nil {
				return 0, err
			}
		}
		object["NS.keys"] = keyUIDs
	} else {
		for i := range values {
			values[i], err = e.encode(value.Index(i), depth+1)
			if err != nil {
				return 0, err
			}
		}
	}
	object["NS.objects"] = values
	return uid, nil
}

func UnmarshalArchive(data []byte) (any, error) {
	return unarchive(data, new(archiveBudget))
}

type archiveBudget struct {
	cost  int
	nodes int
}

func (b *archiveBudget) add(nodes, cost int) error {
	if nodes < 0 || cost < 0 || nodes > maxArchiveObjects-b.nodes || cost > maxArchiveSize-b.cost {
		return E.New("dtx: archive resource limit exceeded")
	}
	b.nodes += nodes
	b.cost += cost
	return nil
}

func unarchive(data []byte, budget *archiveBudget) (result any, err error) {
	// howett.net/plist v1.0.1 returns its own parse failures as errors but
	// rethrows runtime panics, such as out-of-range reads of malformed binary
	// data (decode.go:27-34, bplist_parser.go:66-74).
	defer func() {
		if failure := recover(); failure != nil {
			result, err = nil, E.New("dtx: invalid archive: ", fmt.Sprint(failure))
		}
	}()
	if err := validateArchivePlist(data, budget); err != nil {
		return nil, err
	}
	var envelope map[string]any
	if _, err := plist.Unmarshal(data, &envelope); err != nil {
		return nil, E.Cause(err, "dtx: decode archive")
	}
	if envelope["$archiver"] != "NSKeyedArchiver" || envelope["$version"] != uint64(100000) {
		return nil, E.New("dtx: invalid keyed archive envelope")
	}
	objects, ok := envelope["$objects"].([]any)
	if !ok || len(objects) == 0 || len(objects) > maxArchiveObjects {
		return nil, E.New("dtx: invalid keyed archive object table")
	}
	if null, ok := objects[0].(string); !ok || null != "$null" {
		return nil, E.New("dtx: invalid keyed archive null object")
	}
	top, ok := envelope["$top"].(map[string]any)
	if !ok {
		return nil, E.New("dtx: invalid keyed archive root")
	}
	root, ok := top["root"].(plist.UID)
	if !ok {
		return nil, E.New("dtx: missing keyed archive root reference")
	}
	d := archiveDecoder{objects: objects, active: make(map[plist.UID]bool), cache: make(map[plist.UID]any)}
	return d.resolve(root, 0)
}

type archiveDecoder struct {
	objects []any
	active  map[plist.UID]bool
	cache   map[plist.UID]any
	visited int
}

func (d *archiveDecoder) resolve(value any, depth int) (any, error) {
	d.visited++
	if depth > maxArchiveDepth || d.visited > maxArchiveObjects {
		return nil, E.New("dtx: archive nesting or object limit exceeded")
	}
	switch value := value.(type) {
	case plist.UID:
		if uint64(value) >= uint64(len(d.objects)) {
			return nil, E.New("dtx: archive reference ", uint64(value), " is out of bounds")
		}
		if value == 0 {
			return nil, nil
		}
		if d.active[value] {
			return nil, E.New("dtx: cyclic archive reference ", uint64(value))
		}
		if result, ok := d.cache[value]; ok {
			return result, nil
		}
		d.active[value] = true
		result, err := d.resolve(d.objects[value], depth+1)
		delete(d.active, value)
		if err == nil {
			d.cache[value] = result
		}
		return result, err
	case []any:
		result := make([]any, len(value))
		for i, item := range value {
			var err error
			result[i], err = d.resolve(item, depth+1)
			if err != nil {
				return nil, err
			}
		}
		return result, nil
	case map[string]any:
		return d.object(value, depth)
	default:
		return value, nil
	}
}

func (d *archiveDecoder) object(object map[string]any, depth int) (any, error) {
	var name string
	if class, exists := object["$class"]; exists {
		uid, ok := class.(plist.UID)
		if !ok || uid == 0 || uint64(uid) >= uint64(len(d.objects)) {
			return nil, E.New("dtx: invalid archive class reference")
		}
		metadata, ok := d.objects[uid].(map[string]any)
		if !ok {
			return nil, E.New("dtx: invalid archive class metadata")
		}
		name, ok = metadata["$classname"].(string)
		if !ok || name == "" {
			return nil, E.New("dtx: missing archive class name")
		}
	}
	switch name {
	case "NSNull":
		return nil, nil
	case "NSArray", "NSMutableArray", "NSSet", "NSMutableSet", "NSOrderedSet", "NSMutableOrderedSet":
		values, ok := object["NS.objects"].([]any)
		if !ok {
			return nil, E.New("dtx: invalid ", name, " elements")
		}
		return d.resolve(values, depth+1)
	case "NSDictionary", "NSMutableDictionary":
		keys, keysOK := object["NS.keys"].([]any)
		values, valuesOK := object["NS.objects"].([]any)
		if !keysOK || !valuesOK || len(keys) != len(values) {
			return nil, E.New("dtx: invalid ", name, " entries")
		}
		result := make(map[any]any, len(keys))
		allStrings := true
		for i, key := range keys {
			decoded, err := d.resolve(key, depth+1)
			if err != nil {
				return nil, err
			}
			switch decoded.(type) {
			case string:
			case int64, uint64:
				allStrings = false
			default:
				return nil, E.New("dtx: archive dictionary key must be a string or integer, got ", reflect.TypeOf(decoded))
			}
			if _, duplicate := result[decoded]; duplicate {
				return nil, E.New("dtx: duplicate archive dictionary key ", decoded)
			}
			result[decoded], err = d.resolve(values[i], depth+1)
			if err != nil {
				return nil, err
			}
		}
		if allStrings {
			strings := make(map[string]any, len(result))
			for key, value := range result {
				strings[key.(string)] = value
			}
			return strings, nil
		}
		return result, nil
	case "NSString", "NSMutableString", "NSData", "NSMutableData", "NSNumber":
		fields := []string{"NS.string"}
		if name == "NSData" || name == "NSMutableData" {
			fields = []string{"NS.data"}
		} else if name == "NSNumber" {
			fields = []string{"NS.boolval", "NS.intval", "NS.floatval", "NS.doubleval"}
		}
		for _, field := range fields {
			if value, ok := object[field]; ok {
				return d.resolve(value, depth+1)
			}
		}
		return nil, E.New("dtx: missing ", name, " value")
	default:
		result := make(map[string]any, len(object))
		for key, value := range object {
			if key == "$class" {
				result[key] = name
				continue
			}
			var err error
			result[key], err = d.resolve(value, depth+1)
			if err != nil {
				return nil, err
			}
		}
		return result, nil
	}
}

// howett.net/plist v1.0.1 builds a new Go value for every reference to a
// container (unmarshal.go:316-331).
func validateArchivePlist(data []byte, budget *archiveBudget) error {
	if len(data) < 40 || len(data) > maxArchiveSize || string(data[:8]) != "bplist00" {
		return E.New("dtx: invalid binary archive size or header")
	}
	trailer := data[len(data)-32:]
	offsetSize, refSize := int(trailer[6]), int(trailer[7])
	count := binary.BigEndian.Uint64(trailer[8:])
	root := binary.BigEndian.Uint64(trailer[16:])
	table := binary.BigEndian.Uint64(trailer[24:])
	if offsetSize < 1 || offsetSize > 8 || refSize < 1 || refSize > 8 || count == 0 || count > maxArchiveObjects || root >= count || table < 8 || table > uint64(len(data)-32) || count*uint64(offsetSize) != uint64(len(data)-32)-table {
		return E.New("dtx: invalid binary archive trailer")
	}
	if err := budget.add(0, int(count)*32); err != nil {
		return err
	}
	read := func(data []byte) uint64 {
		var value uint64
		for _, b := range data {
			value = value<<8 | uint64(b)
		}
		return value
	}
	offsets := make([]uint64, count)
	for i := range offsets {
		start := table + uint64(i*offsetSize)
		offsets[i] = read(data[start : start+uint64(offsetSize)])
		if offsets[i] < 8 || offsets[i] >= table {
			return E.New("dtx: invalid binary archive object offset")
		}
	}
	active := make([]bool, count)
	visited, expandedBytes := 0, uint64(0)
	var visit func(uint64, int) error
	visit = func(index uint64, depth int) error {
		visited++
		if index >= count || depth > maxArchiveDepth || visited > maxArchiveObjects {
			return E.New("dtx: binary archive reference or nesting limit exceeded")
		}
		if active[index] {
			return E.New("dtx: cyclic binary archive")
		}
		if err := budget.add(1, 128); err != nil {
			return err
		}
		active[index] = true
		defer func() { active[index] = false }()
		offset := offsets[index]
		tag := data[offset]
		start, length := offset+1, uint64(tag&15)
		kind := tag >> 4
		if (kind >= 4 && kind <= 6 || kind == 10 || kind == 13) && length == 15 {
			if start >= table || data[start]>>4 != 1 || data[start]&15 > 3 {
				return E.New("dtx: invalid binary archive object length")
			}
			width := uint64(1) << (data[start] & 15)
			start++
			if width > table-start {
				return E.New("dtx: truncated binary archive length")
			}
			length = read(data[start : start+width])
			start += width
		}
		var bytes uint64
		switch kind {
		case 0:
			if tag != 8 && tag != 9 {
				return E.New("dtx: unsupported binary archive atom")
			}
		case 1:
			if length > 4 {
				return E.New("dtx: invalid binary archive integer")
			}
			bytes = 1 << length
		case 2:
			if length != 2 && length != 3 {
				return E.New("dtx: invalid binary archive real")
			}
			bytes = 1 << length
		case 3:
			if length != 3 {
				return E.New("dtx: invalid binary archive date")
			}
			bytes = 8
		case 4, 5, 6:
			bytes = length
			if kind == 6 {
				if length > maxArchiveSize/2 {
					return E.New("dtx: binary archive string too large")
				}
				bytes *= 2
			}
		case 8:
			if length > 7 {
				return E.New("dtx: invalid binary archive UID")
			}
			bytes = length + 1
		case 10, 13:
			if length > maxArchiveObjects {
				return E.New("dtx: binary archive container too large")
			}
			if kind == 13 {
				length *= 2
			}
			bytes = length * uint64(refSize)
		default:
			return E.New("dtx: unsupported binary archive object")
		}
		if bytes > table-start {
			return E.New("dtx: truncated binary archive object")
		}
		if kind == 1 && bytes == 16 {
			high := binary.BigEndian.Uint64(data[start:])
			low := binary.BigEndian.Uint64(data[start+8:])
			if high != 0 && (high != ^uint64(0) || low>>63 == 0) {
				return E.New("dtx: binary archive integer exceeds 64 bits")
			}
		}
		expandedBytes += bytes
		if expandedBytes > maxArchiveSize {
			return E.New("dtx: expanded binary archive exceeds size limit")
		}
		allocationBytes := bytes * 2
		if kind == 6 {
			allocationBytes = bytes * 12
		} else if kind == 5 {
			allocationBytes = bytes * 4
		}
		if err := budget.add(0, int(allocationBytes)); err != nil {
			return err
		}
		if kind == 10 || kind == 13 {
			var keys map[string]struct{}
			if kind == 13 {
				keys = make(map[string]struct{}, length/2)
			}
			for i := uint64(0); i < length; i++ {
				position := start + i*uint64(refSize)
				reference := read(data[position : position+uint64(refSize)])
				if err := visit(reference, depth+1); err != nil {
					return err
				}
				if kind == 13 && i < length/2 {
					// howett.net/plist v1.0.1 keeps the last duplicate key
					// (unmarshal.go:264-277, 324-331).
					keyOffset := offsets[reference]
					keyTag := data[keyOffset]
					if keyTag>>4 != 5 && keyTag>>4 != 6 {
						return E.New("dtx: binary archive dictionary key is not a string")
					}
					keyStart, keyLength := keyOffset+1, uint64(keyTag&15)
					if keyLength == 15 {
						width := uint64(1) << (data[keyStart] & 15)
						keyStart++
						keyLength = read(data[keyStart : keyStart+width])
						keyStart += width
					}
					var key string
					if keyTag>>4 == 5 {
						key = string(data[keyStart : keyStart+keyLength])
					} else {
						units := make([]uint16, keyLength)
						for j := range units {
							units[j] = binary.BigEndian.Uint16(data[keyStart+uint64(j)*2:])
						}
						key = string(utf16.Decode(units))
					}
					if _, duplicate := keys[key]; duplicate {
						return E.New("dtx: duplicate binary archive dictionary key")
					}
					keys[key] = struct{}{}
				}
			}
		}
		return nil
	}
	return visit(root, 0)
}
