package dtx

import (
	"encoding/binary"
	"io"
	"math"

	E "github.com/sagernet/sing/common/exceptions"
)

// Fragment and payload headers, message types and the expects-reply flag
// follow pymobiledevice3 1146717, pymobiledevice3/dtx/structs.py:16-97. A
// message longer than one fragment body starts with a header-only fragment
// that carries the message size (pymobiledevice3/dtx/fragmenter.py:198-262).
const (
	fragmentMagic      = 0x1f3d5b79
	fragmentHeaderSize = 32
	payloadHeaderSize  = 16
	maxMessageSize     = 16 << 20
	maxFragmentSize    = 128 << 10
	maxFragmentCount   = 1024
	messageOK          = 0
	messageDispatch    = 2
	messageObject      = 3
	messageError       = 4
)

type message struct {
	identifier   uint32
	conversation uint32
	channel      int32
	flags        uint32
	body         []byte
}

type messageKey struct {
	identifier   uint32
	conversation uint32
	channel      int32
}

type assembly struct {
	message  message
	parts    [][]byte
	total    int
	received int
	size     int
}

type messageReader struct {
	reader    io.Reader
	fragments map[messageKey]*assembly
	buffered  int
}

func (r *messageReader) read() (message, error) {
	for {
		var header [fragmentHeaderSize]byte
		if _, err := io.ReadFull(r.reader, header[:]); err != nil {
			return message{}, err
		}
		le := binary.LittleEndian
		if le.Uint32(header[:4]) != fragmentMagic {
			return message{}, E.New("invalid fragment magic")
		}
		headerSize := le.Uint32(header[4:8])
		index := le.Uint16(header[8:10])
		count := le.Uint16(header[10:12])
		size := le.Uint32(header[12:16])
		m := message{
			identifier:   le.Uint32(header[16:20]),
			conversation: le.Uint32(header[20:24]),
			channel:      int32(le.Uint32(header[24:28])),
			flags:        le.Uint32(header[28:32]),
		}
		if headerSize < fragmentHeaderSize || headerSize > 4096 || count == 0 || count > maxFragmentCount || index >= count || size == 0 || size > maxMessageSize || m.flags&^uint32(1) != 0 {
			return message{}, E.New("invalid fragment header")
		}
		if m.flags&1 != 0 && (m.identifier == 0 || m.conversation == math.MaxUint32) {
			return message{}, E.New("invalid request identifier or conversation")
		}
		if _, err := io.CopyN(io.Discard, r.reader, int64(headerSize-fragmentHeaderSize)); err != nil {
			return message{}, err
		}
		key := messageKey{m.identifier, m.conversation, m.channel}
		if count > 1 && index == 0 {
			if size < payloadHeaderSize || r.fragments[key] != nil || len(r.fragments) >= maxQueuedMessages || r.buffered+int(size) > maxBufferedBytes {
				return message{}, E.New("invalid or excessive fragment assembly")
			}
			r.fragments[key] = &assembly{message: m, parts: make([][]byte, count-1), total: int(size)}
			r.buffered += int(size)
			continue
		}
		if count == 1 {
			if size < payloadHeaderSize || r.fragments[key] != nil {
				return message{}, E.New("invalid single-fragment message")
			}
			m.body = make([]byte, size)
			_, err := io.ReadFull(r.reader, m.body)
			return m, err
		}
		a := r.fragments[key]
		if a == nil || int(count) != len(a.parts)+1 || a.message.flags != m.flags || a.parts[index-1] != nil || a.size+int(size) > a.total {
			return message{}, E.New("invalid message continuation")
		}
		part := make([]byte, size)
		if _, err := io.ReadFull(r.reader, part); err != nil {
			return message{}, err
		}
		a.parts[index-1] = part
		a.size += int(size)
		a.received++
		if a.received != len(a.parts) {
			continue
		}
		if a.size != a.total {
			return message{}, E.New("fragment assembly size mismatch")
		}
		m = a.message
		m.body = make([]byte, 0, a.total)
		for _, part := range a.parts {
			m.body = append(m.body, part...)
		}
		delete(r.fragments, key)
		r.buffered -= a.total
		return m, nil
	}
}

func writeMessage(writer io.Writer, m message) error {
	var header [fragmentHeaderSize]byte
	le := binary.LittleEndian
	le.PutUint32(header[:4], fragmentMagic)
	le.PutUint32(header[4:8], fragmentHeaderSize)
	le.PutUint32(header[16:20], m.identifier)
	le.PutUint32(header[20:24], m.conversation)
	le.PutUint32(header[24:28], uint32(m.channel))
	le.PutUint32(header[28:32], m.flags)
	count := 1
	if len(m.body) > maxFragmentSize {
		count = 1 + (len(m.body)+maxFragmentSize-1)/maxFragmentSize
	}
	le.PutUint16(header[10:12], uint16(count))
	le.PutUint32(header[12:16], uint32(len(m.body)))
	if _, err := writer.Write(header[:]); err != nil {
		return err
	}
	if count == 1 {
		_, err := writer.Write(m.body)
		return err
	}
	for index, offset := 1, 0; offset < len(m.body); index++ {
		size := min(maxFragmentSize, len(m.body)-offset)
		le.PutUint16(header[8:10], uint16(index))
		le.PutUint32(header[12:16], uint32(size))
		if _, err := writer.Write(header[:]); err != nil {
			return err
		}
		if _, err := writer.Write(m.body[offset : offset+size]); err != nil {
			return err
		}
		offset += size
	}
	return nil
}

func dispatchMessage(selector string, args []any) (*message, error) {
	payload, err := MarshalArchive(selector)
	if err != nil {
		return nil, err
	}
	aux, err := encodeArguments(args)
	if err != nil {
		return nil, err
	}
	if payloadHeaderSize+len(aux)+len(payload) > maxMessageSize {
		return nil, E.New("dtx: outgoing message too large")
	}
	body := make([]byte, payloadHeaderSize, payloadHeaderSize+len(aux)+len(payload))
	body[0] = messageDispatch
	binary.LittleEndian.PutUint32(body[4:8], uint32(len(aux)))
	binary.LittleEndian.PutUint32(body[8:12], uint32(len(aux)+len(payload)))
	body = append(body, aux...)
	body = append(body, payload...)
	return &message{body: body}, nil
}

func decodeMessage(body []byte) (byte, any, []any, int, error) {
	if len(body) < payloadHeaderSize {
		return 0, nil, nil, 0, E.New("short message payload header")
	}
	kind := body[0]
	auxSize := binary.LittleEndian.Uint32(body[4:8])
	totalSize := binary.LittleEndian.Uint32(body[8:12])
	if kind > 5 || body[1] != 0 || body[2] != 0 || body[3] != 0 || totalSize != uint32(len(body)-payloadHeaderSize) || auxSize > totalSize {
		return 0, nil, nil, 0, E.New("invalid or unsupported message payload header")
	}
	budget := &archiveBudget{}
	if err := budget.add(1, 128); err != nil {
		return 0, nil, nil, 0, err
	}
	args, err := decodeArguments(body[payloadHeaderSize:payloadHeaderSize+int(auxSize)], budget)
	if err != nil {
		return 0, nil, nil, 0, err
	}
	var payload any
	if data := body[payloadHeaderSize+int(auxSize):]; len(data) != 0 {
		payload, err = unarchive(data, budget)
		if err != nil {
			return 0, nil, nil, 0, E.Cause(err, "decode message payload")
		}
	}
	return kind, payload, args, budget.cost, nil
}

// Arguments travel as one primitive dictionary of null-keyed values, with an
// empty buffer for nil (pymobiledevice3 1146717,
// pymobiledevice3/dtx/message_aux.py:23-87,
// pymobiledevice3/dtx/primitives.py:95-283).
func encodeArguments(args []any) ([]byte, error) {
	if len(args) == 0 {
		return nil, nil
	}
	data := make([]byte, 16)
	binary.LittleEndian.PutUint32(data[:4], 0x1f0)
	for _, arg := range args {
		var encoded []byte
		var err error
		if arg != nil {
			encoded, err = MarshalArchive(arg)
			if err != nil {
				return nil, err
			}
		}
		data = binary.LittleEndian.AppendUint32(data, 10)
		data = binary.LittleEndian.AppendUint32(data, 2)
		data = binary.LittleEndian.AppendUint32(data, uint32(len(encoded)))
		data = append(data, encoded...)
	}
	binary.LittleEndian.PutUint64(data[8:16], uint64(len(data)-16))
	return data, nil
}

func decodeArguments(data []byte, budget *archiveBudget) ([]any, error) {
	if len(data) == 0 {
		return nil, nil
	}
	le := binary.LittleEndian
	if len(data) < 16 || le.Uint32(data[:4])&0xff != 0xf0 || le.Uint64(data[8:16]) != uint64(len(data)-16) {
		return nil, E.New("invalid auxiliary dictionary")
	}
	data = data[16:]
	var args []any
	for len(data) != 0 {
		if len(data) < 8 || le.Uint32(data[:4])&0xff != 10 || len(args) >= 4096 {
			return nil, E.New("invalid auxiliary argument")
		}
		if err := budget.add(1, 32); err != nil {
			return nil, err
		}
		kind := le.Uint32(data[4:8]) & 0xff
		data = data[8:]
		var value any
		switch kind {
		case 10:
		case 1, 2:
			if len(data) < 4 || le.Uint32(data[:4]) > uint32(len(data)-4) {
				return nil, E.New("invalid auxiliary data length")
			}
			size := int(le.Uint32(data[:4]))
			encoded := data[4 : 4+size]
			data = data[4+size:]
			if kind == 1 {
				if err := budget.add(0, len(encoded)); err != nil {
					return nil, err
				}
				value = string(encoded)
			} else if len(encoded) != 0 {
				var err error
				value, err = unarchive(encoded, budget)
				if err != nil {
					return nil, E.Cause(err, "decode auxiliary argument")
				}
			}
		case 3:
			if len(data) < 4 {
				return nil, E.New("short auxiliary integer")
			}
			value = int64(int32(le.Uint32(data[:4])))
			data = data[4:]
		case 6, 9:
			if len(data) < 8 {
				return nil, E.New("short auxiliary number")
			}
			if kind == 6 {
				value = int64(le.Uint64(data[:8]))
			} else {
				value = math.Float64frombits(le.Uint64(data[:8]))
			}
			data = data[8:]
		default:
			return nil, E.New("unsupported auxiliary type ", kind)
		}
		args = append(args, value)
	}
	return args, nil
}
