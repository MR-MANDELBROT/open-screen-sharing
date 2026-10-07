package pairing

import (
	"context"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"slices"

	"github.com/nekohasekai/open-screen-sharing/internal/coredevice/tlv8"

	"golang.org/x/crypto/chacha20poly1305"
)

var ErrPairVerifyFailed = errors.New("pair-verify failed: the device does not know this pair record")

// idevice initiates pair-verify with wire protocol version 19
// (idevice/src/remote_pairing/mod.rs).
const clientWireProtocolVersion = 19

// pymobiledevice3 keeps its remote-pairing control connection open around
// the tunnel context (remote/tunnel_service.py, start_tunnel_over_remotepairing).
type Session struct {
	conn         *conn
	logger       *slog.Logger
	key          []byte
	clientCipher cipher.AEAD
	serverCipher cipher.AEAD
	counter      uint64
}

func Verify(ctx context.Context, netConn net.Conn, record *Record, logger *slog.Logger) (*Session, error) {
	session := &Session{conn: newConn(netConn, originHost), logger: logger}
	stop := withContext(ctx, netConn)
	err := session.verify(record)
	if !stop() && err == nil {
		err = ctx.Err()
	}
	if err != nil {
		netConn.Close()
		return nil, contextError(ctx, err)
	}
	return session, nil
}

func (s *Session) verify(record *Record) error {
	err := s.conn.writePlain(object{"request": object{"_0": object{"handshake": object{"_0": object{
		"hostOptions":         object{"attemptPairVerify": true},
		"wireProtocolVersion": clientWireProtocolVersion,
	}}}}})
	if err != nil {
		return fmt.Errorf("send handshake: %w", err)
	}
	reply, err := s.conn.readPlain()
	if err != nil {
		return fmt.Errorf("read handshake: %w", err)
	}
	handshake := lookupObject(reply, "response", "_1", "handshake", "_0")
	if handshake == nil {
		return fmt.Errorf("unexpected handshake reply: %v", reply)
	}
	s.logger.Debug("remote pairing handshake", slog.Any("reply", handshake))

	ephemeral, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	err = s.conn.writePlain(pairingDataEvent("verifyManualPairing", tlv8.Encode(
		tlv8.Item{Type: tlv8.TypeState, Value: []byte{1}},
		tlv8.Item{Type: tlv8.TypePublicKey, Value: ephemeral.PublicKey().Bytes()},
	), true))
	if err != nil {
		return fmt.Errorf("send pair-verify M1: %w", err)
	}
	data, err := s.conn.readPairingData()
	if err != nil {
		return fmt.Errorf("read pair-verify M2: %w", err)
	}
	m2, err := tlv8.Decode(data)
	if err != nil {
		return fmt.Errorf("pair-verify M2: %w", err)
	}
	if code, failed := m2.Error(); failed {
		s.conn.writePlain(object{"event": object{"_0": object{"pairVerifyFailed": object{}}}})
		return fmt.Errorf("%w (%s)", ErrPairVerifyFailed, tlv8.ErrorString(code))
	}
	devicePublic, err := ecdh.X25519().NewPublicKey(m2[tlv8.TypePublicKey])
	if err != nil {
		return fmt.Errorf("pair-verify M2: device key: %w", err)
	}
	shared, err := ephemeral.ECDH(devicePublic)
	if err != nil {
		return err
	}
	verifyKey, err := hkdf.Key(sha512.New, shared, []byte("Pair-Verify-Encrypt-Salt"), "Pair-Verify-Encrypt-Info", chacha20poly1305.KeySize)
	if err != nil {
		return err
	}
	verifyCipher, err := chacha20poly1305.New(verifyKey)
	if err != nil {
		return err
	}
	// idevice proceeds from the device's X25519 public key to M3 without
	// checking the encrypted M2 identity
	// (idevice/src/remote_pairing/mod.rs, attempt_pair_verify).
	signed := slices.Concat(ephemeral.PublicKey().Bytes(), []byte(record.Identifier), devicePublic.Bytes())
	plaintext := tlv8.Encode(
		tlv8.Item{Type: tlv8.TypeIdentifier, Value: []byte(record.Identifier)},
		tlv8.Item{Type: tlv8.TypeSignature, Value: ed25519.Sign(record.PrivateKey, signed)},
	)
	encrypted := verifyCipher.Seal(nil, pairingNonce("PV-Msg03"), plaintext, nil)
	err = s.conn.writePlain(pairingDataEvent("verifyManualPairing", tlv8.Encode(
		tlv8.Item{Type: tlv8.TypeState, Value: []byte{3}},
		tlv8.Item{Type: tlv8.TypeEncryptedData, Value: encrypted},
	), false))
	if err != nil {
		return fmt.Errorf("send pair-verify M3: %w", err)
	}
	data, err = s.conn.readPairingData()
	if err != nil {
		return fmt.Errorf("read pair-verify M4: %w", err)
	}
	m4, err := tlv8.Decode(data)
	if err != nil {
		return fmt.Errorf("pair-verify M4: %w", err)
	}
	if code, failed := m4.Error(); failed {
		s.conn.writePlain(object{"event": object{"_0": object{"pairVerifyFailed": object{}}}})
		return fmt.Errorf("%w (%s)", ErrPairVerifyFailed, tlv8.ErrorString(code))
	}
	s.key = shared
	s.clientCipher, s.serverCipher, err = mainCiphers(shared)
	return err
}

// HKDF labels match idevice derive_main_ciphers
// (idevice/src/remote_pairing/mod.rs).
func mainCiphers(shared []byte) (client cipher.AEAD, server cipher.AEAD, err error) {
	clientKey, err := hkdf.Key(sha512.New, shared, nil, "ClientEncrypt-main", chacha20poly1305.KeySize)
	if err != nil {
		return nil, nil, err
	}
	serverKey, err := hkdf.Key(sha512.New, shared, nil, "ServerEncrypt-main", chacha20poly1305.KeySize)
	if err != nil {
		return nil, nil, err
	}
	client, err = chacha20poly1305.New(clientKey)
	if err != nil {
		return nil, nil, err
	}
	server, err = chacha20poly1305.New(serverKey)
	return client, server, err
}

// idevice uses four zero bytes before the eight-byte PV/PS step label
// (idevice/src/remote_pairing/mod.rs and responder.rs).
func pairingNonce(label string) []byte {
	return append(make([]byte, 4), label...)
}

// pymobiledevice3 uses the pair-verify X25519 secret as the TCP tunnel
// TLS PSK (remote/tunnel_service.py).
func (s *Session) Key() []byte {
	return s.key
}

func (s *Session) request(ctx context.Context, request map[string]any) (map[string]any, error) {
	plaintext, err := json.Marshal(object{"request": object{"_0": request}})
	if err != nil {
		return nil, err
	}
	stop := withContext(ctx, s.conn.conn)
	result, err := s.exchange(plaintext)
	if !stop() && err == nil {
		err = ctx.Err()
	}
	if err != nil {
		s.conn.conn.Close()
		return nil, contextError(ctx, err)
	}
	return result, nil
}

func (s *Session) exchange(plaintext []byte) (map[string]any, error) {
	// idevice uses the same nonce for request and reply: a little-endian
	// 64-bit counter followed by four zero bytes
	// (idevice/src/remote_pairing/mod.rs, send_receive_encrypted_request).
	nonce := make([]byte, chacha20poly1305.NonceSize)
	binary.LittleEndian.PutUint64(nonce, s.counter)
	err := s.conn.writeEncrypted(s.clientCipher.Seal(nil, nonce, plaintext, nil))
	if err != nil {
		return nil, err
	}
	for {
		message, err := s.conn.read()
		if err != nil {
			return nil, err
		}
		if message.Plain != nil {
			s.logger.Debug("remote pairing event while waiting for a reply", slog.Any("message", message.Plain))
			continue
		}
		decrypted, err := s.serverCipher.Open(nil, nonce, message.Encrypted, nil)
		if err != nil {
			return nil, fmt.Errorf("decrypt reply: %w", err)
		}
		s.counter++
		var reply object
		err = json.Unmarshal(decrypted, &reply)
		if err != nil {
			return nil, fmt.Errorf("parse reply: %w", err)
		}
		result := lookupObject(reply, "response", "_1")
		if result == nil {
			return nil, fmt.Errorf("unexpected reply: %s", decrypted)
		}
		return result, nil
	}
}

func (s *Session) CreateListener(ctx context.Context) (uint16, error) {
	reply, err := s.request(ctx, object{"createListener": object{
		"key":                   base64.StdEncoding.EncodeToString(s.key),
		"transportProtocolType": "tcp",
	}})
	if err != nil {
		return 0, fmt.Errorf("create listener: %w", err)
	}
	port, isNumber := lookup(reply, "createListener", "port").(float64)
	if !isNumber || port <= 0 || port > 65535 {
		return 0, fmt.Errorf("create listener: unexpected reply %v", reply)
	}
	return uint16(port), nil
}

func (s *Session) Wait() error {
	for {
		message, err := s.conn.read()
		if err != nil {
			return err
		}
		if message.Plain != nil {
			s.logger.Debug("remote pairing event", slog.Any("message", message.Plain))
		}
	}
}

func (s *Session) Close() error {
	return s.conn.conn.Close()
}
