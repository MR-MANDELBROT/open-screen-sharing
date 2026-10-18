package dtx

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"sync"

	E "github.com/sagernet/sing/common/exceptions"
)

const (
	maxQueuedMessages = 128
	maxBufferedBytes  = 32 << 20
)

type Event struct {
	Selector  string
	Arguments []any
	Payload   any
}

type Handler func(event Event) (reply any, handled bool)

type queuedEvent struct {
	event Event
	size  int
}

type result struct {
	value any
	err   error
}

type writeRequest struct {
	ctx     context.Context
	message message
	done    chan error
}

type Conn struct {
	ctx         context.Context
	conn        net.Conn
	done        chan struct{}
	writes      chan writeRequest
	events      chan queuedEvent
	access      sync.Mutex
	err         error
	nextID      uint32
	pending     map[uint32]chan result
	queuedBytes int
	eventBytes  int
	handler     Handler
	stopContext func() bool
}

func NewConn(ctx context.Context, conn net.Conn, handler Handler) (*Conn, error) {
	c := &Conn{
		ctx:     ctx,
		conn:    conn,
		done:    make(chan struct{}),
		writes:  make(chan writeRequest, maxQueuedMessages),
		events:  make(chan queuedEvent, maxQueuedMessages),
		nextID:  1,
		pending: make(map[uint32]chan result),
		handler: handler,
	}
	c.access.Lock()
	c.stopContext = context.AfterFunc(ctx, func() { c.fail(ctx.Err()) })
	c.access.Unlock()
	if err := ctx.Err(); err != nil {
		c.fail(err)
		return nil, err
	}
	go c.readLoop()
	go c.writeLoop()
	return c, nil
}

func (c *Conn) Invoke(ctx context.Context, selector string, args ...any) (value any, err error) {
	defer func() {
		if contextErr := ctx.Err(); contextErr != nil {
			c.fail(contextErr)
			value, err = nil, contextErr
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m, err := dispatchMessage(selector, args)
	if err != nil {
		return nil, err
	}
	m.flags = 1
	reply := make(chan result, 1)
	written, err := c.enqueue(ctx, m, reply)
	if err != nil {
		return nil, err
	}
	defer func() {
		c.access.Lock()
		delete(c.pending, m.identifier)
		c.access.Unlock()
	}()
	for {
		select {
		case response := <-reply:
			return response.value, response.err
		case err := <-written:
			if err != nil {
				return nil, err
			}
			written = nil
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-c.done:
			select {
			case response := <-reply:
				return response.value, response.err
			default:
			}
			return nil, c.failure()
		}
	}
}

func (c *Conn) Notify(ctx context.Context, selector string, args ...any) (err error) {
	defer func() {
		if contextErr := ctx.Err(); contextErr != nil {
			c.fail(contextErr)
			err = contextErr
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	m, err := dispatchMessage(selector, args)
	if err != nil {
		return err
	}
	written, err := c.enqueue(ctx, m, nil)
	if err != nil {
		return err
	}
	select {
	case err := <-written:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		select {
		case err := <-written:
			return err
		default:
		}
		return c.failure()
	}
}

func (c *Conn) Receive(ctx context.Context) (Event, error) {
	select {
	case item := <-c.events:
		c.access.Lock()
		c.eventBytes -= item.size
		c.access.Unlock()
		return item.event, nil
	case <-ctx.Done():
		return Event{}, ctx.Err()
	case <-c.done:
		return Event{}, c.failure()
	}
}

func (c *Conn) Close() error {
	c.fail(net.ErrClosed)
	return nil
}

func (c *Conn) Wait() error {
	<-c.done
	return c.failure()
}

func (c *Conn) fail(err error) {
	c.access.Lock()
	if c.err != nil {
		c.access.Unlock()
		return
	}
	c.err = err
	stopContext := c.stopContext
	c.stopContext = nil
	close(c.done)
	c.access.Unlock()
	if stopContext != nil {
		stopContext()
	}
	c.conn.Close()
}

func (c *Conn) failure() error {
	c.access.Lock()
	defer c.access.Unlock()
	return c.err
}

func (c *Conn) enqueue(ctx context.Context, m *message, reply chan result) (<-chan error, error) {
	c.access.Lock()
	defer c.access.Unlock()
	if c.err != nil {
		return nil, c.err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if (reply != nil && len(c.pending) >= maxQueuedMessages) || c.queuedBytes+len(m.body) > maxBufferedBytes {
		return nil, E.New("dtx: outgoing queue limit exceeded")
	}
	if m.identifier == 0 {
		if c.nextID == 0 {
			return nil, E.New("dtx: message identifiers exhausted")
		}
		m.identifier = c.nextID
		c.nextID++
	}
	done := make(chan error, 1)
	if reply != nil {
		c.pending[m.identifier] = reply
	}
	select {
	case c.writes <- writeRequest{ctx, *m, done}:
		c.queuedBytes += len(m.body)
		return done, nil
	default:
		if reply != nil {
			delete(c.pending, m.identifier)
		}
		return nil, E.New("dtx: outgoing queue limit exceeded")
	}
}

func (c *Conn) writeLoop() {
	for {
		select {
		case <-c.done:
			return
		case request := <-c.writes:
			err := request.ctx.Err()
			if err == nil {
				err = c.failure()
			}
			if err == nil {
				err = writeMessage(c.conn, request.message)
			}
			if contextErr := request.ctx.Err(); contextErr != nil {
				err = contextErr
			}
			c.access.Lock()
			c.queuedBytes -= len(request.message.body)
			c.access.Unlock()
			if err != nil {
				c.fail(E.Cause(err, "dtx: write"))
				err = c.failure()
			}
			request.done <- err
			if err != nil {
				return
			}
		}
	}
}

func (c *Conn) readLoop() {
	r := messageReader{reader: c.conn, fragments: make(map[messageKey]*assembly)}
	for {
		m, err := r.read()
		if err == nil {
			err = c.deliver(m)
		}
		if err != nil {
			c.fail(E.Cause(err, "dtx: read"))
			return
		}
	}
}

func (c *Conn) deliver(m message) error {
	if m.channel != 0 {
		return E.New("unsupported channel ", m.channel)
	}
	kind, payload, args, cost, err := decodeMessage(m.body)
	if err != nil {
		return err
	}
	if m.conversation != 0 {
		if err := c.reply(m, nil); err != nil {
			return err
		}
		if kind != messageOK && kind != messageObject && kind != messageError {
			return E.New("unexpected reply type ", kind)
		}
		response := result{value: payload}
		if kind == messageError {
			response.err = E.New("dtx: remote error: ", fmt.Sprint(payload))
		}
		c.access.Lock()
		if pending := c.pending[m.identifier]; pending != nil {
			delete(c.pending, m.identifier)
			pending <- response
		}
		c.access.Unlock()
		return nil
	}
	selector, _ := payload.(string)
	event := queuedEvent{Event{Selector: selector, Arguments: args, Payload: payload}, len(m.body) + cost}
	var response any
	var handled bool
	if c.handler != nil {
		response, handled = c.handler(event.event)
	}
	if err := c.reply(m, response); err != nil {
		return err
	}
	if handled {
		return nil
	}
	c.access.Lock()
	defer c.access.Unlock()
	if c.eventBytes+event.size > maxBufferedBytes {
		return E.New("event queue limit exceeded")
	}
	select {
	case c.events <- event:
		c.eventBytes += event.size
		return nil
	default:
		return E.New("event queue limit exceeded")
	}
}

// A reply keeps the request identifier and increments its conversation
// index; it is OK without a payload or OBJECT with one (pymobiledevice3
// 1146717, pymobiledevice3/dtx/channel.py:225-232,
// pymobiledevice3/dtx/_sender.py:157-185).
func (c *Conn) reply(request message, value any) error {
	if request.flags&1 == 0 {
		return nil
	}
	body := make([]byte, payloadHeaderSize)
	if value != nil {
		payload, err := MarshalArchive(value)
		if err != nil {
			return err
		}
		if payloadHeaderSize+len(payload) > maxMessageSize {
			return E.New("dtx: reply too large")
		}
		body[0] = messageObject
		binary.LittleEndian.PutUint32(body[8:12], uint32(len(payload)))
		body = append(body, payload...)
	}
	_, err := c.enqueue(c.ctx, &message{identifier: request.identifier, conversation: request.conversation + 1, body: body}, nil)
	return err
}
