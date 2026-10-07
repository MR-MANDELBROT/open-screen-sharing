package rfb

import (
	"bufio"
	"encoding/binary"
	"io"
	"net"
	"sync"
	"time"
)

type stream struct {
	conn    net.Conn
	raw     *bufio.Reader
	source  io.Reader
	records *recordReader

	capturing bool
	captured  []byte
	scratch   [8]byte
}

func newStream(conn net.Conn) *stream {
	raw := bufio.NewReaderSize(conn, 256*1024)
	return &stream{conn: conn, raw: raw, source: raw}
}

func (s *stream) startRecords(keys recordKeys) error {
	records := &recordReader{source: s.raw}
	err := records.decoder.rekey(keys)
	if err != nil {
		return err
	}
	s.records = records
	s.source = records
	return nil
}

func (s *stream) Read(data []byte) (int, error) {
	err := s.readFull(data)
	if err != nil {
		return 0, err
	}
	return len(data), nil
}

func (s *stream) readFull(data []byte) error {
	_, err := io.ReadFull(s.source, data)
	if err != nil {
		return err
	}
	if s.capturing {
		s.captured = append(s.captured, data...)
	}
	return nil
}

func (s *stream) bytes(length int) ([]byte, error) {
	data := make([]byte, length)
	err := s.readFull(data)
	return data, err
}

func (s *stream) u8() (uint8, error) {
	err := s.readFull(s.scratch[:1])
	return s.scratch[0], err
}

func (s *stream) u16() (uint16, error) {
	err := s.readFull(s.scratch[:2])
	return binary.BigEndian.Uint16(s.scratch[:2]), err
}

func (s *stream) u32() (uint32, error) {
	err := s.readFull(s.scratch[:4])
	return binary.BigEndian.Uint32(s.scratch[:4]), err
}

func (s *stream) discard(length int) error {
	var buffer [4096]byte
	for length > 0 {
		chunk := min(length, len(buffer))
		err := s.readFull(buffer[:chunk])
		if err != nil {
			return err
		}
		length -= chunk
	}
	return nil
}

func (s *stream) startCapture() {
	s.capturing = true
	s.captured = s.captured[:0]
}

func (s *stream) stopCapture() []byte {
	s.capturing = false
	return s.captured
}

type outbox struct {
	access        sync.Mutex
	messages      [][]byte
	waiters       []chan error
	pendingMotion int
	wake          chan struct{}
	closed        bool
	err           error
}

func newOutbox() *outbox {
	return &outbox{pendingMotion: -1, wake: make(chan struct{}, 1)}
}

func (o *outbox) pushWait(message []byte) error {
	written := make(chan error, 1)
	o.access.Lock()
	if o.closed {
		o.access.Unlock()
		return net.ErrClosed
	}
	o.messages = append(o.messages, message)
	o.waiters = append(o.waiters, written)
	o.pendingMotion = -1
	o.access.Unlock()
	select {
	case o.wake <- struct{}{}:
	default:
	}
	return <-written
}

func (o *outbox) push(message []byte, motion bool) error {
	o.access.Lock()
	if o.closed {
		err := o.err
		o.access.Unlock()
		if err == nil {
			err = net.ErrClosed
		}
		return err
	}
	if motion && o.pendingMotion >= 0 {
		o.messages[o.pendingMotion] = message
	} else {
		o.messages = append(o.messages, message)
		if motion {
			o.pendingMotion = len(o.messages) - 1
		} else {
			o.pendingMotion = -1
		}
	}
	o.access.Unlock()
	select {
	case o.wake <- struct{}{}:
	default:
	}
	return nil
}

func (o *outbox) take() ([][]byte, []chan error, bool) {
	o.access.Lock()
	defer o.access.Unlock()
	messages, waiters := o.messages, o.waiters
	o.messages, o.waiters = nil, nil
	o.pendingMotion = -1
	return messages, waiters, o.closed
}

func (o *outbox) close(err error) {
	o.access.Lock()
	if !o.closed {
		o.closed = true
		o.err = err
	}
	o.access.Unlock()
	select {
	case o.wake <- struct{}{}:
	default:
	}
}

const (
	writeStallLimit = 30 * time.Second
	writeChunk      = 64 << 10
)

type writer struct {
	conn    net.Conn
	encoder *recordEncoder
	buffer  []byte
	wire    []byte

	deadlineAccess sync.Mutex
	finishing      bool
}

func (w *writer) finishWithin(limit time.Duration) {
	w.deadlineAccess.Lock()
	w.finishing = true
	w.conn.SetWriteDeadline(time.Now().Add(limit))
	w.deadlineAccess.Unlock()
}

func (w *writer) send(data []byte) error {
	for len(data) > 0 {
		w.deadlineAccess.Lock()
		if !w.finishing {
			w.conn.SetWriteDeadline(time.Now().Add(writeStallLimit))
		}
		w.deadlineAccess.Unlock()
		piece := data[:min(len(data), writeChunk)]
		written, err := w.conn.Write(piece)
		data = data[written:]
		if err != nil {
			return err
		}
	}
	return nil
}

func (w *writer) write(messages [][]byte) error {
	if w.encoder == nil {
		w.buffer = w.buffer[:0]
		for _, message := range messages {
			w.buffer = append(w.buffer, message...)
		}
		return w.send(w.buffer)
	}
	w.wire = w.wire[:0]
	w.buffer = w.buffer[:0]
	for _, message := range messages {
		for len(message) > 0 {
			room := recordMaximumBody - len(w.buffer)
			if room == 0 {
				w.wire = w.encoder.seal(w.wire, w.buffer)
				w.buffer = w.buffer[:0]
				room = recordMaximumBody
			}
			chunk := min(room, len(message))
			w.buffer = append(w.buffer, message[:chunk]...)
			message = message[chunk:]
		}
	}
	if len(w.buffer) > 0 {
		w.wire = w.encoder.seal(w.wire, w.buffer)
	}
	return w.send(w.wire)
}

func (w *writer) run(box *outbox) error {
	for range box.wake {
		messages, waiters, closed := box.take()
		var err error
		if len(messages) > 0 {
			err = w.write(messages)
		}
		for _, waiter := range waiters {
			waiter <- err
		}
		if err != nil {
			box.close(err)
			_, waiters, _ = box.take()
			for _, waiter := range waiters {
				waiter <- err
			}
			return err
		}
		if closed {
			return nil
		}
	}
	return nil
}
