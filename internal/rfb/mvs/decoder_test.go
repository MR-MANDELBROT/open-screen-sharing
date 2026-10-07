package mvs_test

import (
	"encoding/binary"
	"hash/crc32"
	"os"
	"testing"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb/mvs"
)

type sessionRecord struct {
	kind    byte
	x       int
	y       int
	width   int
	height  int
	payload []byte
}

func loadSession(t testing.TB) ([]sessionRecord, []uint32) {
	sequence, err := os.ReadFile("testdata/session.bin")
	if err != nil {
		t.Fatal(err)
	}
	checksumData, err := os.ReadFile("testdata/session.crc32")
	if err != nil {
		t.Fatal(err)
	}
	if string(sequence[:8]) != "MVSSEQ1\n" {
		t.Fatal("unknown session format")
	}
	var records []sessionRecord
	position := 8
	for position < len(sequence) {
		record := sessionRecord{kind: sequence[position]}
		position++
		switch record.kind {
		case 'R':
			record.width = int(binary.BigEndian.Uint16(sequence[position:]))
			record.height = int(binary.BigEndian.Uint16(sequence[position+2:]))
			position += 4
		case 'P', 'M':
			record.x = int(binary.BigEndian.Uint16(sequence[position:]))
			record.y = int(binary.BigEndian.Uint16(sequence[position+2:]))
			record.width = int(binary.BigEndian.Uint16(sequence[position+4:]))
			record.height = int(binary.BigEndian.Uint16(sequence[position+6:]))
			position += 8
			length := record.width * record.height * 4
			if record.kind == 'M' {
				length = int(binary.BigEndian.Uint32(sequence[position:]))
				position += 4
			}
			record.payload = sequence[position : position+length]
			position += length
		default:
			t.Fatalf("unknown record %q", record.kind)
		}
		records = append(records, record)
	}
	checksums := make([]uint32, len(checksumData)/4)
	for i := range checksums {
		checksums[i] = binary.BigEndian.Uint32(checksumData[i*4:])
	}
	return records, checksums
}

func newTarget(width, height int) *mvs.Target {
	target := &mvs.Target{Pix: make([]byte, width*height*4), Stride: width * 4, Width: width, Height: height}
	for i := 3; i < len(target.Pix); i += 4 {
		target.Pix[i] = 0xff
	}
	return target
}

func applyPixels(target *mvs.Target, record *sessionRecord) {
	for row := range record.height {
		offset := (record.y+row)*target.Stride + record.x*4
		copy(target.Pix[offset:offset+record.width*4], record.payload[row*record.width*4:])
	}
}

func TestDecodeRecordedSession(t *testing.T) {
	records, checksums := loadSession(t)
	decoder := mvs.NewDecoder()
	var target *mvs.Target
	update := 0
	mismatches := 0
	for i := range records {
		record := &records[i]
		switch record.kind {
		case 'R':
			target = newTarget(record.width, record.height)
			decoder.Reset(record.width, record.height)
		case 'P':
			applyPixels(target, record)
		case 'M':
			err := decoder.Decode(record.payload, record.x, record.y, record.width, record.height, target)
			if err != nil {
				t.Fatalf("update %d (type %d, %dx%d at %d,%d): %v", update, record.payload[0], record.width, record.height, record.x, record.y, err)
			}
			if update >= len(checksums) {
				t.Fatalf("more updates than reference checksums")
			}
			if crc32.ChecksumIEEE(target.Pix) != checksums[update] {
				mismatches++
				if mismatches <= 10 {
					t.Errorf("update %d (type %d, %dx%d at %d,%d): framebuffer differs from the reference", update, record.payload[0], record.width, record.height, record.x, record.y)
				}
			}
			update++
		}
	}
	if update != len(checksums) {
		t.Fatalf("decoded %d updates, reference has %d", update, len(checksums))
	}
	if mismatches > 0 {
		t.Fatalf("%d of %d updates differ from the reference", mismatches, update)
	}
}
