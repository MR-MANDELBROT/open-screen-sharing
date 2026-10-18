package pairing

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"

	E "github.com/sagernet/sing/common/exceptions"
)

// idevice frames RPPairing JSON with this magic and a big-endian uint16
// length; its envelope uses message, plain/streamEncrypted, originatedBy
// and sequenceNumber (idevice 3854a5d, idevice/src/remote_pairing/mod.rs:44;
// idevice/src/remote_pairing/socket.rs:69-110).
const rpPairingMagic = "RPPairing"

const (
	originHost   = "host"
	originDevice = "device"
)

type conn struct {
	conn     net.Conn
	reader   *bufio.Reader
	origin   string
	sequence uint64
}

func newConn(netConn net.Conn, origin string) *conn {
	return &conn{conn: netConn, reader: bufio.NewReader(netConn), origin: origin}
}

type object = map[string]any

type envelope struct {
	Plain     object
	Encrypted []byte
}

func (c *conn) writeEnvelope(message object) error {
	data, err := json.Marshal(object{
		"message":        message,
		"originatedBy":   c.origin,
		"sequenceNumber": c.sequence,
	})
	if err != nil {
		return err
	}
	frame := make([]byte, 0, len(rpPairingMagic)+2+len(data))
	frame = append(frame, rpPairingMagic...)
	frame = binary.BigEndian.AppendUint16(frame, uint16(len(data)))
	frame = append(frame, data...)
	_, err = c.conn.Write(frame)
	if err != nil {
		return err
	}
	c.sequence++
	return nil
}

func (c *conn) writePlain(value object) error {
	return c.writeEnvelope(object{"plain": object{"_0": value}})
}

func (c *conn) writeEncrypted(ciphertext []byte) error {
	return c.writeEnvelope(object{"streamEncrypted": object{"_0": base64.StdEncoding.EncodeToString(ciphertext)}})
}

func (c *conn) read() (*envelope, error) {
	header := make([]byte, len(rpPairingMagic)+2)
	_, err := io.ReadFull(c.reader, header)
	if err != nil {
		return nil, err
	}
	if string(header[:len(rpPairingMagic)]) != rpPairingMagic {
		return nil, E.New("pairing: bad RPPairing magic ", strconv.Quote(string(header[:len(rpPairingMagic)])))
	}
	data := make([]byte, binary.BigEndian.Uint16(header[len(rpPairingMagic):]))
	_, err = io.ReadFull(c.reader, data)
	if err != nil {
		return nil, err
	}
	var message struct {
		Message struct {
			Plain *struct {
				Value object `json:"_0"`
			} `json:"plain"`
			StreamEncrypted *struct {
				Value []byte `json:"_0"`
			} `json:"streamEncrypted"`
		} `json:"message"`
	}
	err = json.Unmarshal(data, &message)
	if err != nil {
		return nil, E.Cause(err, "parse RPPairing message")
	}
	switch {
	case message.Message.Plain != nil:
		return &envelope{Plain: message.Message.Plain.Value}, nil
	case message.Message.StreamEncrypted != nil:
		return &envelope{Encrypted: message.Message.StreamEncrypted.Value}, nil
	default:
		return nil, E.New("pairing: unknown RPPairing message: ", string(data))
	}
}

func (c *conn) readPlain() (object, error) {
	message, err := c.read()
	if err != nil {
		return nil, err
	}
	if message.Plain == nil {
		return nil, E.New("pairing: unexpected encrypted RPPairing message")
	}
	return message.Plain, nil
}

func contextError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func lookup(value any, path ...string) any {
	for _, key := range path {
		dictionary, isObject := value.(object)
		if !isObject {
			return nil
		}
		value = dictionary[key]
	}
	return value
}

func lookupObject(value any, path ...string) object {
	result, _ := lookup(value, path...).(object)
	return result
}

func lookupString(value any, path ...string) string {
	result, _ := lookup(value, path...).(string)
	return result
}

func lookupBytes(value any, path ...string) ([]byte, error) {
	text, isString := lookup(value, path...).(string)
	if !isString {
		return nil, E.New("pairing: missing ", strings.Join(path, "."))
	}
	return base64.StdEncoding.DecodeString(text)
}

func pairingDataEvent(kind string, data []byte, startNewSession bool) object {
	return object{"event": object{"_0": object{"pairingData": object{"_0": object{
		"data":            base64.StdEncoding.EncodeToString(data),
		"kind":            kind,
		"startNewSession": startNewSession,
	}}}}}
}

type PairingRejectedError struct {
	Reason string
}

func (e *PairingRejectedError) Error() string {
	if e.Reason == "" {
		return "the device rejected pairing"
	}
	return "the device rejected pairing: " + e.Reason
}

func (c *conn) readPairingData() ([]byte, error) {
	message, err := c.readPlain()
	if err != nil {
		return nil, err
	}
	event := lookupObject(message, "event", "_0")
	if event == nil {
		return nil, E.New("pairing: expected a pairing event, got ", fmt.Sprint(message))
	}
	if rejected := lookupObject(event, "pairingRejectedWithError"); rejected != nil {
		return nil, &PairingRejectedError{Reason: lookupString(rejected, "wrappedError", "userInfo", "NSLocalizedDescription")}
	}
	if lookup(event, "pairVerifyFailed") != nil {
		return nil, ErrPairVerifyFailed
	}
	data, err := lookupBytes(event, "pairingData", "_0", "data")
	if err != nil {
		return nil, E.Cause(err, "unexpected pairing event ", fmt.Sprint(event))
	}
	return data, nil
}
