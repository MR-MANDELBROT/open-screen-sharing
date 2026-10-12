package media

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"reflect"
	"unsafe"
)

// For AES blocks, cipher.NewCTR returns an aesCtrWrapper holding *aes.CTR.
// The internal CTR stores ivlo, ivhi and offset as uint64; newCTR copies the
// AES Block and resets offset (Go 1.26.8, src/crypto/cipher/ctr.go:41-44,
// src/crypto/cipher/ctr.go:66-69, src/crypto/internal/fips140/aes/ctr.go:15-19,
// src/crypto/internal/fips140/aes/ctr.go:28-39).
type keyStream struct {
	stream cipher.Stream
	low    *uint64
	high   *uint64
	offset *uint64
}

func newKeyStream(block cipher.Block) *keyStream {
	var iv [aes.BlockSize]byte
	stream := cipher.NewCTR(block, iv[:])
	wrapper := reflect.ValueOf(stream)
	if wrapper.Kind() != reflect.Struct || wrapper.NumField() != 1 || wrapper.Field(0).Kind() != reflect.Pointer {
		return nil
	}
	ctr := wrapper.Field(0).Elem()
	if ctr.Kind() != reflect.Struct {
		return nil
	}
	s := &keyStream{
		stream: stream,
		low:    counterField(ctr, "ivlo"),
		high:   counterField(ctr, "ivhi"),
		offset: counterField(ctr, "offset"),
	}
	if s.low == nil || s.high == nil || s.offset == nil || !s.matches(block) {
		return nil
	}
	return s
}

func counterField(ctr reflect.Value, name string) *uint64 {
	field := ctr.FieldByName(name)
	if !field.IsValid() || field.Kind() != reflect.Uint64 {
		return nil
	}
	return (*uint64)(unsafe.Pointer(field.UnsafeAddr()))
}

func (s *keyStream) xorKeyStream(iv *[aes.BlockSize]byte, data []byte) {
	*s.high = binary.BigEndian.Uint64(iv[:8])
	*s.low = binary.BigEndian.Uint64(iv[8:])
	*s.offset = 0
	s.stream.XORKeyStream(data, data)
}

func (s *keyStream) matches(block cipher.Block) bool {
	ivs := [][aes.BlockSize]byte{
		{15: 1},
		{0: 0x5a, 7: 0x01, 8: 0xff, 9: 0xff, 10: 0xff, 11: 0xff, 12: 0xff, 13: 0xff, 14: 0xff, 15: 0xf0},
		{0: 0xff, 1: 0xff, 2: 0xff, 3: 0xff, 4: 0xff, 5: 0xff, 6: 0xff, 7: 0xff, 8: 0xff, 9: 0xff, 10: 0xff, 11: 0xff, 12: 0xff, 13: 0xff, 14: 0xff, 15: 0xfe},
	}
	want := make([]byte, 1300)
	got := make([]byte, len(want))
	for _, iv := range ivs {
		for _, length := range []int{1, 100, len(want)} {
			clear(want[:length])
			cipher.NewCTR(block, iv[:]).XORKeyStream(want[:length], want[:length])
			clear(got[:length])
			s.xorKeyStream(&iv, got[:length])
			if !bytes.Equal(got[:length], want[:length]) {
				return false
			}
		}
	}
	return true
}
