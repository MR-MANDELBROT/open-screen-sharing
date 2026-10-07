package display

import (
	"math"
	"net/netip"
)

func dictionary(value any) map[string]any {
	result, _ := value.(map[string]any)
	return result
}

func array(value any) []any {
	result, _ := value.([]any)
	return result
}

func stringValue(value any) string {
	result, _ := value.(string)
	return result
}

func boolValue(value any) (bool, bool) {
	switch v := value.(type) {
	case bool:
		return v, true
	case int64:
		return v != 0, true
	case uint64:
		return v != 0, true
	}
	return false, false
}

func uintValue(value any) (uint64, bool) {
	switch v := value.(type) {
	case uint64:
		return v, true
	case int64:
		if v < 0 {
			return 0, false
		}
		return uint64(v), true
	case float64:
		if v < 0 || v > math.MaxUint64 || v != math.Trunc(v) {
			return 0, false
		}
		return uint64(v), true
	}
	return 0, false
}

func floatValue(value any) (float64, bool) {
	if v, ok := value.(float64); ok {
		return v, true
	}
	number, ok := uintValue(value)
	return float64(number), ok
}

func intField(dictionary map[string]any, key string) int {
	number, ok := uintValue(dictionary[key])
	if !ok || number > math.MaxInt32 {
		return 0
	}
	return int(number)
}

func uint32Field(dictionary map[string]any, key string) (uint32, bool) {
	number, ok := uintValue(dictionary[key])
	if !ok || number > math.MaxUint32 {
		return 0, false
	}
	return uint32(number), true
}

func addrPort(ip string, port int) netip.AddrPort {
	address, err := netip.ParseAddr(ip)
	if err != nil || port <= 0 || port > math.MaxUint16 {
		return netip.AddrPort{}
	}
	return netip.AddrPortFrom(address, uint16(port))
}
