// The handshake and GCM record layer follow go-ios
// (https://github.com/danielpaulus/go-ios, ios/tunnel/tlspsk/tlspsk.go),
// MIT License, Copyright (c) 2019 danielpaulus.
package tlspsk

import (
	"bufio"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"io"
	"net"
	"slices"
	"time"
)

const (
	TLS_PSK_WITH_AES_128_CBC_SHA    uint16 = 0x008C
	TLS_PSK_WITH_AES_256_CBC_SHA    uint16 = 0x008D
	TLS_PSK_WITH_AES_128_GCM_SHA256 uint16 = 0x00A8
	TLS_PSK_WITH_AES_256_GCM_SHA384 uint16 = 0x00A9
	TLS_PSK_WITH_AES_128_CBC_SHA256 uint16 = 0x00AE
	TLS_PSK_WITH_AES_256_CBC_SHA384 uint16 = 0x00AF
)

type cipherSuite struct {
	id      uint16
	keyLen  int
	prfHash func() hash.Hash
	macHash func() hash.Hash
	macLen  int
}

// go-ios offers AES-256-GCM-SHA384; idevice offers AES-256-CBC-SHA384
// and AES-128-CBC-SHA (their tunnel/tlspsk and remote_pairing/tls_psk).
var cipherSuites = []cipherSuite{
	{id: TLS_PSK_WITH_AES_256_GCM_SHA384, keyLen: 32, prfHash: sha512.New384},
	{id: TLS_PSK_WITH_AES_128_GCM_SHA256, keyLen: 16, prfHash: sha256.New},
	{id: TLS_PSK_WITH_AES_256_CBC_SHA384, keyLen: 32, prfHash: sha512.New384, macHash: sha512.New384, macLen: 48},
	{id: TLS_PSK_WITH_AES_128_CBC_SHA256, keyLen: 16, prfHash: sha256.New, macHash: sha256.New, macLen: 32},
	{id: TLS_PSK_WITH_AES_256_CBC_SHA, keyLen: 32, prfHash: sha256.New, macHash: sha1.New, macLen: 20},
	{id: TLS_PSK_WITH_AES_128_CBC_SHA, keyLen: 16, prfHash: sha256.New, macHash: sha1.New, macLen: 20},
}

func lookupCipherSuite(id uint16) *cipherSuite {
	index := slices.IndexFunc(cipherSuites, func(suite cipherSuite) bool {
		return suite.id == id
	})
	if index < 0 {
		return nil
	}
	return &cipherSuites[index]
}

const (
	recordChangeCipherSpec byte = 20
	recordAlert            byte = 21
	recordHandshake        byte = 22
	recordApplicationData  byte = 23

	handshakeClientHello       byte = 1
	handshakeServerHello       byte = 2
	handshakeCertificate       byte = 11
	handshakeServerKeyExchange byte = 12
	handshakeServerHelloDone   byte = 14
	handshakeClientKeyExchange byte = 16
	handshakeFinished          byte = 20

	versionTLS12 uint16 = 0x0303

	recordHeaderLen     = 5
	maxPlaintext        = 16384
	maxCiphertext       = maxPlaintext + 2048
	gcmExplicitNonceLen = 8
	gcmFixedIVLen       = 4
	gcmTagLen           = 16
	cbcBlockLen         = aes.BlockSize
	masterSecretLen     = 48
	verifyDataLen       = 12

	maxHandshakeMessage = maxPlaintext
	maxServerFlight     = 8
	recordOverhead      = recordHeaderLen + cbcBlockLen + 48 + cbcBlockLen
	maxRetainedWrite    = 512 << 10
	readerSize          = 64 << 10
)

type AlertError struct {
	Level       byte
	Description byte
}

func (e *AlertError) Error() string {
	return fmt.Sprintf("tls alert %d (level %d)", e.Description, e.Level)
}

type Conn struct {
	conn  net.Conn
	suite *cipherSuite

	reader      *bufio.Reader
	readState   recordState
	readBuffer  []byte
	pending     []byte
	readErr     error
	handshake   bytes.Buffer
	transcript  bytes.Buffer
	writeState  recordState
	writeBuffer []byte
	writeErr    error
}

type recordState struct {
	enabled  bool
	sequence uint64
	aead     cipher.AEAD
	fixedIV  [gcmFixedIVLen]byte
	block    cipher.Block
	mac      hash.Hash
}

func Client(ctx context.Context, conn net.Conn, key []byte) (*Conn, error) {
	c := &Conn{
		conn:       conn,
		reader:     bufio.NewReaderSize(conn, readerSize),
		readBuffer: make([]byte, maxCiphertext),
	}
	stop := context.AfterFunc(ctx, func() {
		conn.SetDeadline(time.Unix(1, 0))
	})
	err := c.runHandshake(key)
	if !stop() || ctx.Err() != nil {
		if err == nil {
			err = context.Cause(ctx)
		} else {
			err = fmt.Errorf("%w (%w)", err, context.Cause(ctx))
		}
	}
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("tlspsk: %w", err)
	}
	conn.SetDeadline(time.Time{})
	c.handshake = bytes.Buffer{}
	c.transcript = bytes.Buffer{}
	return c, nil
}

func (c *Conn) runHandshake(key []byte) error {
	var clientRandom [32]byte
	rand.Read(clientRandom[:])
	hello := make([]byte, 0, 64)
	hello = binary.BigEndian.AppendUint16(hello, versionTLS12)
	hello = append(hello, clientRandom[:]...)
	hello = append(hello, 0)
	hello = binary.BigEndian.AppendUint16(hello, uint16(2*len(cipherSuites)))
	for _, suite := range cipherSuites {
		hello = binary.BigEndian.AppendUint16(hello, suite.id)
	}
	hello = append(hello, 1, 0)
	err := c.writeHandshake(handshakeClientHello, hello)
	if err != nil {
		return fmt.Errorf("write ClientHello: %w", err)
	}

	var serverRandom []byte
serverFlight:
	for count := 0; ; count++ {
		if count == maxServerFlight {
			return errors.New("too many handshake messages from the server")
		}
		messageType, body, err := c.readHandshake()
		if err != nil {
			return fmt.Errorf("read server hello: %w", err)
		}
		switch messageType {
		case handshakeServerHello:
			if serverRandom != nil {
				return errors.New("second ServerHello")
			}
			serverRandom, err = c.parseServerHello(body)
			if err != nil {
				return err
			}
		case handshakeServerKeyExchange:
		case handshakeCertificate:
			return errors.New("server sent a certificate, which plain PSK suites don't use")
		case handshakeServerHelloDone:
			break serverFlight
		default:
			return fmt.Errorf("unexpected handshake message %d", messageType)
		}
	}
	if serverRandom == nil {
		return errors.New("missing ServerHello")
	}

	// go-ios and idevice send an empty PSK identity.
	err = c.writeHandshake(handshakeClientKeyExchange, []byte{0, 0})
	if err != nil {
		return fmt.Errorf("write ClientKeyExchange: %w", err)
	}

	keyLen := len(key)
	preMaster := make([]byte, 0, 4+2*keyLen)
	preMaster = binary.BigEndian.AppendUint16(preMaster, uint16(keyLen))
	preMaster = append(preMaster, make([]byte, keyLen)...)
	preMaster = binary.BigEndian.AppendUint16(preMaster, uint16(keyLen))
	preMaster = append(preMaster, key...)
	master := prf(c.suite.prfHash, preMaster, "master secret", slices.Concat(clientRandom[:], serverRandom), masterSecretLen)
	err = c.setupKeys(master, clientRandom[:], serverRandom)
	if err != nil {
		return err
	}

	err = c.writeRecord(recordChangeCipherSpec, []byte{1})
	if err != nil {
		return fmt.Errorf("write ChangeCipherSpec: %w", err)
	}
	c.writeState.enabled = true
	err = c.writeHandshake(handshakeFinished, c.verifyData(master, "client finished"))
	if err != nil {
		return fmt.Errorf("write Finished: %w", err)
	}

	contentType, _, err := c.readRecord()
	if err != nil {
		return fmt.Errorf("read ChangeCipherSpec: %w", err)
	}
	if contentType != recordChangeCipherSpec {
		return fmt.Errorf("expected ChangeCipherSpec, got record type %d", contentType)
	}
	c.readState.enabled = true
	expected := c.verifyData(master, "server finished")
	messageType, body, err := c.readHandshake()
	if err != nil {
		return fmt.Errorf("read Finished: %w", err)
	}
	if messageType != handshakeFinished {
		return fmt.Errorf("expected Finished, got handshake message %d", messageType)
	}
	if !hmac.Equal(body, expected) {
		return errors.New("server Finished doesn't match, the key is probably wrong")
	}
	return nil
}

func (c *Conn) parseServerHello(body []byte) ([]byte, error) {
	if len(body) < 35 {
		return nil, errors.New("short ServerHello")
	}
	version := binary.BigEndian.Uint16(body)
	if version != versionTLS12 {
		return nil, fmt.Errorf("server chose version 0x%04x instead of TLS 1.2", version)
	}
	serverRandom := bytes.Clone(body[2:34])
	offset := 35 + int(body[34])
	if len(body) < offset+3 {
		return nil, errors.New("truncated ServerHello")
	}
	id := binary.BigEndian.Uint16(body[offset:])
	c.suite = lookupCipherSuite(id)
	if c.suite == nil {
		return nil, fmt.Errorf("server chose cipher suite 0x%04x, which wasn't offered", id)
	}
	if body[offset+2] != 0 {
		return nil, fmt.Errorf("server chose compression method %d", body[offset+2])
	}
	return serverRandom, nil
}

func (c *Conn) setupKeys(master []byte, clientRandom []byte, serverRandom []byte) error {
	suite := c.suite
	var length int
	if suite.macHash == nil {
		length = 2*suite.keyLen + 2*gcmFixedIVLen
	} else {
		length = 2*suite.macLen + 2*suite.keyLen
	}
	keyBlock := prf(suite.prfHash, master, "key expansion", slices.Concat(serverRandom, clientRandom), length)
	if suite.macHash == nil {
		clientKey := keyBlock[:suite.keyLen]
		serverKey := keyBlock[suite.keyLen : 2*suite.keyLen]
		ivs := keyBlock[2*suite.keyLen:]
		clientAEAD, err := newGCM(clientKey)
		if err != nil {
			return err
		}
		serverAEAD, err := newGCM(serverKey)
		if err != nil {
			return err
		}
		c.writeState.aead = clientAEAD
		copy(c.writeState.fixedIV[:], ivs[:gcmFixedIVLen])
		c.readState.aead = serverAEAD
		copy(c.readState.fixedIV[:], ivs[gcmFixedIVLen:])
		return nil
	}
	clientMAC := keyBlock[:suite.macLen]
	serverMAC := keyBlock[suite.macLen : 2*suite.macLen]
	keys := keyBlock[2*suite.macLen:]
	clientBlock, err := aes.NewCipher(keys[:suite.keyLen])
	if err != nil {
		return err
	}
	serverBlock, err := aes.NewCipher(keys[suite.keyLen:])
	if err != nil {
		return err
	}
	c.writeState.block = clientBlock
	c.writeState.mac = hmac.New(suite.macHash, clientMAC)
	c.readState.block = serverBlock
	c.readState.mac = hmac.New(suite.macHash, serverMAC)
	return nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func (c *Conn) verifyData(master []byte, label string) []byte {
	transcriptHash := c.suite.prfHash()
	transcriptHash.Write(c.transcript.Bytes())
	return prf(c.suite.prfHash, master, label, transcriptHash.Sum(nil), verifyDataLen)
}

func (c *Conn) writeHandshake(messageType byte, body []byte) error {
	message := make([]byte, 4, 4+len(body))
	message[0] = messageType
	message[1], message[2], message[3] = byte(len(body)>>16), byte(len(body)>>8), byte(len(body))
	message = append(message, body...)
	c.transcript.Write(message)
	return c.writeRecord(recordHandshake, message)
}

func (c *Conn) writeRecord(contentType byte, payload []byte) error {
	c.writeBuffer = c.appendRecord(c.writeBuffer[:0], contentType, payload)
	_, err := c.conn.Write(c.writeBuffer)
	return err
}

func (c *Conn) readHandshake() (byte, []byte, error) {
	for {
		data := c.handshake.Bytes()
		if len(data) >= 4 {
			length := int(data[1])<<16 | int(data[2])<<8 | int(data[3])
			if length > maxHandshakeMessage {
				return 0, nil, fmt.Errorf("handshake message of %d bytes is too large", length)
			}
			if len(data) >= 4+length {
				message := bytes.Clone(data[:4+length])
				c.handshake.Next(4 + length)
				c.transcript.Write(message)
				return message[0], message[4:], nil
			}
		}
		contentType, payload, err := c.readRecord()
		if err != nil {
			return 0, nil, err
		}
		if contentType != recordHandshake {
			return 0, nil, fmt.Errorf("expected handshake record, got type %d", contentType)
		}
		c.handshake.Write(payload)
	}
}

func (c *Conn) appendRecord(buffer []byte, contentType byte, payload []byte) []byte {
	state := &c.writeState
	start := len(buffer)
	buffer = append(buffer, contentType, byte(versionTLS12>>8), byte(versionTLS12&0xff), 0, 0)
	switch {
	case !state.enabled:
		buffer = append(buffer, payload...)
	case state.aead != nil:
		var nonce [gcmFixedIVLen + gcmExplicitNonceLen]byte
		copy(nonce[:], state.fixedIV[:])
		binary.BigEndian.PutUint64(nonce[gcmFixedIVLen:], state.sequence)
		buffer = append(buffer, nonce[gcmFixedIVLen:]...)
		additional := additionalData(state.sequence, contentType, len(payload))
		buffer = state.aead.Seal(buffer, nonce[:], payload, additional[:])
		state.sequence++
	default:
		macLen := c.suite.macLen
		header := additionalData(state.sequence, contentType, len(payload))
		state.mac.Reset()
		state.mac.Write(header[:])
		state.mac.Write(payload)
		paddingLen := cbcBlockLen - (len(payload)+macLen+1)%cbcBlockLen
		if paddingLen == cbcBlockLen {
			paddingLen = 0
		}
		ivStart := len(buffer)
		buffer = append(buffer, make([]byte, cbcBlockLen)...)
		rand.Read(buffer[ivStart:])
		bodyStart := len(buffer)
		buffer = append(buffer, payload...)
		buffer = state.mac.Sum(buffer)
		for range paddingLen + 1 {
			buffer = append(buffer, byte(paddingLen))
		}
		body := buffer[bodyStart:]
		cipher.NewCBCEncrypter(state.block, buffer[ivStart:bodyStart]).CryptBlocks(body, body)
		state.sequence++
	}
	binary.BigEndian.PutUint16(buffer[start+3:], uint16(len(buffer)-start-recordHeaderLen))
	return buffer
}

func additionalData(sequence uint64, contentType byte, length int) [13]byte {
	var data [13]byte
	binary.BigEndian.PutUint64(data[:], sequence)
	data[8] = contentType
	binary.BigEndian.PutUint16(data[9:], versionTLS12)
	binary.BigEndian.PutUint16(data[11:], uint16(length))
	return data
}

func (c *Conn) readRecord() (byte, []byte, error) {
	return c.readRecordInto(c.readBuffer)
}

func (c *Conn) readRecordInto(dst []byte) (byte, []byte, error) {
	header, err := c.reader.Peek(recordHeaderLen)
	if err != nil {
		if err == io.EOF && len(header) > 0 {
			err = io.ErrUnexpectedEOF
		}
		return 0, nil, err
	}
	contentType := header[0]
	length := int(binary.BigEndian.Uint16(header[3:]))
	if length > maxCiphertext {
		return 0, nil, fmt.Errorf("record of %d bytes is too large", length)
	}
	record, err := c.reader.Peek(recordHeaderLen + length)
	if err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return 0, nil, err
	}
	payload, err := c.openRecord(dst, contentType, record[recordHeaderLen:])
	c.reader.Discard(len(record))
	if err != nil {
		return 0, nil, err
	}
	if contentType == recordAlert {
		if len(payload) < 2 {
			return 0, nil, errors.New("malformed alert")
		}
		if payload[1] == 0 {
			return 0, nil, io.EOF
		}
		return 0, nil, &AlertError{Level: payload[0], Description: payload[1]}
	}
	return contentType, payload, nil
}

func (c *Conn) openRecord(dst []byte, contentType byte, fragment []byte) ([]byte, error) {
	state := &c.readState
	if !state.enabled {
		return append(dst[:0], fragment...), nil
	}
	if state.aead != nil {
		if len(fragment) < gcmExplicitNonceLen+gcmTagLen {
			return nil, errors.New("short encrypted record")
		}
		var nonce [gcmFixedIVLen + gcmExplicitNonceLen]byte
		copy(nonce[:], state.fixedIV[:])
		copy(nonce[gcmFixedIVLen:], fragment[:gcmExplicitNonceLen])
		ciphertext := fragment[gcmExplicitNonceLen:]
		additional := additionalData(state.sequence, contentType, len(ciphertext)-gcmTagLen)
		payload, err := state.aead.Open(dst[:0], nonce[:], ciphertext, additional[:])
		if err != nil {
			return nil, errors.New("record authentication failed")
		}
		state.sequence++
		return payload, nil
	}
	macLen := c.suite.macLen
	if len(fragment) < cbcBlockLen+cbcBlockLen || len(fragment)%cbcBlockLen != 0 {
		return nil, errors.New("malformed encrypted record")
	}
	body := dst[:len(fragment)-cbcBlockLen]
	cipher.NewCBCDecrypter(state.block, fragment[:cbcBlockLen]).CryptBlocks(body, fragment[cbcBlockLen:])
	paddingLen := int(body[len(body)-1])
	if paddingLen+1+macLen > len(body) {
		return nil, errors.New("record authentication failed")
	}
	for _, value := range body[len(body)-1-paddingLen:] {
		if int(value) != paddingLen {
			return nil, errors.New("record authentication failed")
		}
	}
	payloadLen := len(body) - 1 - paddingLen - macLen
	payload, received := body[:payloadLen], body[payloadLen:payloadLen+macLen]
	header := additionalData(state.sequence, contentType, payloadLen)
	state.mac.Reset()
	state.mac.Write(header[:])
	state.mac.Write(payload)
	if subtle.ConstantTimeCompare(state.mac.Sum(nil), received) != 1 {
		return nil, errors.New("record authentication failed")
	}
	state.sequence++
	return payload, nil
}

func (c *Conn) Read(p []byte) (int, error) {
	for len(c.pending) == 0 {
		if c.readErr != nil {
			return 0, c.readErr
		}
		direct := len(p) >= maxCiphertext
		dst := c.readBuffer
		if direct {
			dst = p
		}
		contentType, payload, err := c.readRecordInto(dst)
		if err != nil {
			c.readErr = err
			return 0, err
		}
		switch contentType {
		case recordApplicationData:
			if direct {
				if len(payload) == 0 {
					continue
				}
				return len(payload), nil
			}
			c.pending = payload
		case recordHandshake:
		default:
			c.readErr = fmt.Errorf("tlspsk: unexpected record type %d", contentType)
			return 0, c.readErr
		}
	}
	n := copy(p, c.pending)
	c.pending = c.pending[n:]
	return n, nil
}

func (c *Conn) Write(p []byte) (int, error) {
	if c.writeErr != nil {
		return 0, c.writeErr
	}
	if len(p) == 0 {
		return 0, nil
	}
	records := (len(p) + maxPlaintext - 1) / maxPlaintext
	buffer := slices.Grow(c.writeBuffer[:0], len(p)+records*recordOverhead)
	for offset := 0; offset < len(p); offset += maxPlaintext {
		buffer = c.appendRecord(buffer, recordApplicationData, p[offset:min(offset+maxPlaintext, len(p))])
	}
	_, err := c.conn.Write(buffer)
	if cap(buffer) <= maxRetainedWrite {
		c.writeBuffer = buffer
	} else {
		c.writeBuffer = nil
	}
	if err != nil {
		c.writeErr = err
		return 0, err
	}
	return len(p), nil
}

func (c *Conn) Close() error {
	return c.conn.Close()
}

func (c *Conn) LocalAddr() net.Addr {
	return c.conn.LocalAddr()
}

func (c *Conn) RemoteAddr() net.Addr {
	return c.conn.RemoteAddr()
}

func (c *Conn) SetDeadline(t time.Time) error {
	return c.conn.SetDeadline(t)
}

func (c *Conn) SetReadDeadline(t time.Time) error {
	return c.conn.SetReadDeadline(t)
}

func (c *Conn) SetWriteDeadline(t time.Time) error {
	return c.conn.SetWriteDeadline(t)
}

func prf(newHash func() hash.Hash, secret []byte, label string, seed []byte, length int) []byte {
	labelSeed := slices.Concat([]byte(label), seed)
	result := make([]byte, 0, length)
	mac := hmac.New(newHash, secret)
	mac.Write(labelSeed)
	a := mac.Sum(nil)
	for len(result) < length {
		mac.Reset()
		mac.Write(a)
		mac.Write(labelSeed)
		result = mac.Sum(result)
		mac.Reset()
		mac.Write(a)
		a = mac.Sum(a[:0])
	}
	return result[:length]
}
