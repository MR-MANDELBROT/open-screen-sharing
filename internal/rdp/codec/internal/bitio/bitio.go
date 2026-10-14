package bitio

import "encoding/binary"

func Refill(src []byte, pos int, acc uint64, have uint) (uint64, uint, int) {
	if pos+8 <= len(src) {
		return acc | binary.BigEndian.Uint64(src[pos:])>>have, have | 56, pos + int(63-have)>>3
	}
	for have <= 56 {
		if pos < len(src) {
			acc |= uint64(src[pos]) << (56 - have)
			pos++
		}
		have += 8
	}
	return acc, have, pos
}

func CopyMatch(buf []byte, dst, src, n int) {
	if src >= dst || dst-src >= n {
		copy(buf[dst:dst+n], buf[src:src+n])
		return
	}
	for copied := 0; copied < n; {
		copied += copy(buf[dst+copied:dst+n], buf[src:dst+copied])
	}
}
