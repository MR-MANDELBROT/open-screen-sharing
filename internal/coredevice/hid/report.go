// The keyboard report and descriptor follow idevice
// idevice/src/services/core_device/hid.rs (https://github.com/jkcoxson/idevice,
// MIT License, Copyright 2026 Jackson Coxson).

package hid

import (
	"encoding/binary"
	"time"
)

const (
	keyboardReportID        = 0x01
	keyboardBitmapSize      = 29
	keyboardReportSize      = 39
	keyboardTimestampOffset = 31
)

// The descriptor and 39-byte report match idevice: report ID, 29 usage
// bytes, one constant byte, six timestamp bytes and two zero bytes (idevice
// d32c818, idevice/src/services/core_device/hid.rs:152-159, 344-354).
var keyboardReportDescriptor = []byte{
	0x85, 0x01, 0x05, 0x07, 0x19, 0x01, 0x29, 0xe7, 0x96, 0xe8, 0x00, 0x75, 0x01, 0x15, 0x01, 0x26,
	0xe7, 0x00, 0x81, 0x02, 0xa1, 0x02, 0x06, 0x1a, 0xff, 0x0a, 0xf1, 0xe0, 0x19, 0x00, 0x29, 0x00,
	0x75, 0x08, 0x95, 0x01, 0x81, 0x01, 0xc0, 0x06, 0x00, 0xff, 0x0a, 0x02, 0x01, 0x15, 0x00, 0x26,
	0xff, 0x00, 0x75, 0x08, 0x95, 0x08, 0x81, 0x02,
}

func keyboardReport(pressed *[keyboardBitmapSize]byte, timestamp uint64) []byte {
	report := make([]byte, keyboardReportSize)
	report[0] = keyboardReportID
	copy(report[1:], pressed[:])
	putTimestamp(report[keyboardTimestampOffset:], timestamp)
	return report
}

func putTimestamp(dst []byte, timestamp uint64) {
	var buffer [8]byte
	binary.LittleEndian.PutUint64(buffer[:], timestamp)
	copy(dst[:6], buffer[:6])
}

var epoch = time.Now()

func now() uint64 {
	return uint64(time.Since(epoch))
}
