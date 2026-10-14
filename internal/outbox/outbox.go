package outbox

import (
	"net"
	"sync"
	"sync/atomic"
	"time"
)

const (
	writeStallLimit = 30 * time.Second
	writeChunk      = 64 << 10
)

type Outbox struct {
	access        sync.Mutex
	messages      [][]byte
	waiters       []chan error
	pendingMotion int
	wake          chan struct{}
	closed        bool
	err           error
}

func New() *Outbox {
	return &Outbox{pendingMotion: -1, wake: make(chan struct{}, 1)}
}

func (o *Outbox) PushWait(message []byte) error {
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
	o.signal()
	return <-written
}

func (o *Outbox) Push(message []byte, motion bool) error {
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
	o.signal()
	return nil
}

func (o *Outbox) Close(err error) {
	o.access.Lock()
	if !o.closed {
		o.closed = true
		o.err = err
	}
	o.access.Unlock()
	o.signal()
}

func (o *Outbox) signal() {
	select {
	case o.wake <- struct{}{}:
	default:
	}
}

func (o *Outbox) take() ([][]byte, []chan error, bool) {
	o.access.Lock()
	defer o.access.Unlock()
	messages, waiters := o.messages, o.waiters
	o.messages, o.waiters = nil, nil
	o.pendingMotion = -1
	return messages, waiters, o.closed
}

type Writer struct {
	conn    net.Conn
	encode  func(wire []byte, data []byte) []byte
	data    []byte
	wire    []byte
	written atomic.Uint64

	deadlineAccess sync.Mutex
	finishing      bool
}

func NewWriter(conn net.Conn) *Writer {
	return &Writer{conn: conn}
}

func (w *Writer) SetEncoder(encode func(wire []byte, data []byte) []byte) {
	w.encode = encode
}

func (w *Writer) Written() uint64 {
	return w.written.Load()
}

func (w *Writer) FinishWithin(limit time.Duration) {
	w.deadlineAccess.Lock()
	w.finishing = true
	w.conn.SetWriteDeadline(time.Now().Add(limit))
	w.deadlineAccess.Unlock()
}

func (w *Writer) send(data []byte) error {
	for len(data) > 0 {
		w.deadlineAccess.Lock()
		if !w.finishing {
			w.conn.SetWriteDeadline(time.Now().Add(writeStallLimit))
		}
		w.deadlineAccess.Unlock()
		piece := data[:min(len(data), writeChunk)]
		written, err := w.conn.Write(piece)
		w.written.Add(uint64(written))
		data = data[written:]
		if err != nil {
			return err
		}
	}
	return nil
}

func (w *Writer) Write(messages [][]byte) error {
	w.data = w.data[:0]
	for _, message := range messages {
		w.data = append(w.data, message...)
	}
	if w.encode == nil {
		return w.send(w.data)
	}
	w.wire = w.encode(w.wire[:0], w.data)
	return w.send(w.wire)
}

func (w *Writer) Run(box *Outbox) error {
	for range box.wake {
		messages, waiters, closed := box.take()
		var err error
		if len(messages) > 0 {
			err = w.Write(messages)
		}
		for _, waiter := range waiters {
			waiter <- err
		}
		if err != nil {
			box.Close(err)
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
