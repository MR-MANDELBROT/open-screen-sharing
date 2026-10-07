package tlspsk

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"testing"
)

func connPair(t *testing.T, suite *cipherSuite) (*Conn, *Conn) {
	t.Helper()
	writerSide, readerSide := net.Pipe()
	t.Cleanup(func() {
		writerSide.Close()
		readerSide.Close()
	})
	master := bytes.Repeat([]byte{1}, masterSecretLen)
	random := bytes.Repeat([]byte{2}, 32)
	writer := &Conn{conn: writerSide, suite: suite}
	reader := &Conn{
		conn:       readerSide,
		suite:      suite,
		reader:     bufio.NewReaderSize(readerSide, readerSize),
		readBuffer: make([]byte, maxCiphertext),
	}
	for _, c := range []*Conn{writer, reader} {
		err := c.setupKeys(master, random, random)
		if err != nil {
			t.Fatal(err)
		}
	}
	reader.readState = reader.writeState
	writer.writeState.enabled = true
	reader.readState.enabled = true
	return writer, reader
}

func TestReadRecords(t *testing.T) {
	sizes := []int{1, 100, maxPlaintext, maxPlaintext + 1, 100000}
	var sent []byte
	for i, size := range sizes {
		sent = append(sent, bytes.Repeat([]byte{byte(i + 1)}, size)...)
	}
	for _, suite := range cipherSuites {
		for _, readSize := range []int{100, maxCiphertext, 128 << 10} {
			t.Run(fmt.Sprintf("%04x/%d", suite.id, readSize), func(t *testing.T) {
				writer, reader := connPair(t, &suite)
				go func() {
					for i, size := range sizes {
						offset := 0
						for _, previous := range sizes[:i] {
							offset += previous
						}
						_, err := writer.Write(sent[offset : offset+size])
						if err != nil {
							return
						}
					}
				}()
				received := make([]byte, 0, len(sent))
				buffer := make([]byte, readSize)
				for len(received) < len(sent) {
					n, err := reader.Read(buffer)
					if err != nil {
						t.Fatalf("after %d bytes: %v", len(received), err)
					}
					if readSize >= maxCiphertext && len(reader.pending) != 0 {
						t.Fatal("a large read left part of a record pending")
					}
					received = append(received, buffer[:n]...)
				}
				if !bytes.Equal(received, sent) {
					t.Fatal("received data differs from what was sent")
				}
			})
		}
	}
}

func TestReadRecordAuthentication(t *testing.T) {
	for _, suite := range cipherSuites {
		t.Run(fmt.Sprintf("%04x", suite.id), func(t *testing.T) {
			writer, reader := connPair(t, &suite)
			record := writer.appendRecord(nil, recordApplicationData, []byte("hello"))
			record[len(record)-1] ^= 1
			go writer.conn.Write(record)
			_, err := reader.Read(make([]byte, maxCiphertext))
			if err == nil || err == io.EOF {
				t.Fatalf("read a changed record: %v", err)
			}
		})
	}
}
