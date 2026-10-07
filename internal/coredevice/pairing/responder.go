package pairing

// The responder is a port of idevice's PairableHost
// (https://github.com/jkcoxson/idevice, idevice/src/remote_pairing/responder.rs),
// MIT License, Copyright (c) Jackson Coxson.

import (
	"context"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha512"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nekohasekai/open-screen-sharing/internal/coredevice/opack"
	"github.com/nekohasekai/open-screen-sharing/internal/coredevice/tlv8"

	"github.com/google/uuid"
	"golang.org/x/crypto/chacha20poly1305"
)

// idevice PairableHost advertises _remotepairing-pairable-host._tcp
// (idevice/src/remote_pairing/responder.rs).
const ServiceType = "_remotepairing-pairable-host._tcp"

const (
	// idevice PairableHostInfo defaults to Mac17,7.
	responderModel = "Mac17,7"

	// idevice PairableHostInfo defaults to wire version 26, answers with
	// minimum version 8, and advertises minVer=17
	// (idevice/src/remote_pairing/responder.rs).
	responderWireProtocolVersion = 26
	responderMinimumWireProtocol = 8
	responderMinimumWireless     = 17
)

type ResponderOptions struct {
	HostName string
	OnPIN    func(pin string)
	Identity *Record
	Logger   *slog.Logger
}

type Responder struct {
	options  ResponderOptions
	logger   *slog.Logger
	listener *net.TCPListener
	identity *Record

	drainAccess sync.Mutex
	draining    map[net.Conn]struct{}
	closed      bool
}

func NewResponder(options ResponderOptions) (*Responder, error) {
	logger := options.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	identity, err := responderIdentity(options.Identity)
	if err != nil {
		return nil, err
	}
	listener, err := net.ListenTCP("tcp", &net.TCPAddr{})
	if err != nil {
		return nil, fmt.Errorf("responder: listen: %w", err)
	}
	return &Responder{options: options, logger: logger, listener: listener, identity: identity}, nil
}

func responderIdentity(previous *Record) (*Record, error) {
	var identity Record
	if previous == nil {
		publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		identity = Record{
			Identifier: strings.ToUpper(uuid.NewString()),
			PublicKey:  publicKey,
			PrivateKey: privateKey,
		}
	} else {
		identity = Record{
			Identifier: previous.Identifier,
			PublicKey:  previous.PublicKey,
			PrivateKey: previous.PrivateKey,
			HostIRK:    previous.HostIRK,
		}
	}
	if len(identity.HostIRK) != 16 {
		identity.HostIRK = make([]byte, 16)
		rand.Read(identity.HostIRK)
	}
	return &identity, nil
}

func (r *Responder) Port() uint16 {
	return uint16(r.listener.Addr().(*net.TCPAddr).Port)
}

// idevice requires the Bonjour instance name to be the identifier that its
// TXT record carries and its authTag is derived from
// (idevice/src/remote_pairing/responder.rs, mdns_txt_records).
func (r *Responder) ServiceName() string {
	return r.identity.Identifier
}

// idevice advertises these TXT keys with flags=1
// (idevice/src/remote_pairing/responder.rs, mdns_txt_records).
func (r *Responder) TXTRecord() map[string]string {
	return map[string]string{
		"name":       r.options.HostName,
		"identifier": r.identity.Identifier,
		"authTag":    base64.StdEncoding.EncodeToString(AuthTag(r.identity.HostIRK, r.identity.Identifier)),
		"model":      responderModel,
		"flags":      "1",
		"ver":        strconv.Itoa(responderWireProtocolVersion),
		"minVer":     strconv.Itoa(responderMinimumWireless),
	}
}

const (
	responderConnections       = 8
	responderHandshakeTimeout  = 10 * time.Second
	responderSetupStartTimeout = 2 * time.Minute
	responderCodeTimeout       = 5 * time.Minute
	responderStepTimeout       = 30 * time.Second
	responderDrainTimeout      = 5 * time.Second
)

type acceptState struct {
	ctx     context.Context
	results chan acceptResult
	access  sync.Mutex
	attempt uint64
}

type acceptResult struct {
	record *Record
	err    error
}

func (r *Responder) Accept(ctx context.Context) (*Record, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	state := &acceptState{ctx: ctx, results: make(chan acceptResult, 1)}
	listenerStopped := make(chan struct{})
	stopListener := context.AfterFunc(ctx, func() {
		r.listener.SetDeadline(time.Unix(1, 0))
		close(listenerStopped)
	})
	var group sync.WaitGroup
	acceptErr := make(chan error, 1)
	group.Go(func() {
		slots := make(chan struct{}, responderConnections)
		for {
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				return
			}
			netConn, err := r.listener.Accept()
			if err != nil {
				acceptErr <- err
				return
			}
			group.Go(func() {
				r.serve(state, netConn)
				<-slots
			})
		}
	})
	var result acceptResult
	select {
	case result = <-state.results:
	case err := <-acceptErr:
		result.err = contextError(ctx, err)
	case <-ctx.Done():
		select {
		case result = <-state.results:
		default:
			result.err = ctx.Err()
		}
	}
	cancel()
	group.Wait()
	if !stopListener() {
		<-listenerStopped
	}
	r.listener.SetDeadline(time.Time{})
	return result.record, result.err
}

func (r *Responder) serve(state *acceptState, netConn net.Conn) {
	logger := r.logger.With(slog.String("from", netConn.RemoteAddr().String()))
	logger.Info("pairing connection")
	stopClose := context.AfterFunc(state.ctx, func() { netConn.Close() })
	c := newConn(netConn, originDevice)
	record, attempt, err := r.pair(state, c)
	if err == nil && !stopClose() {
		err = state.ctx.Err()
	}
	if err != nil {
		stopClose()
		netConn.Close()
		if attempt == 0 || state.ctx.Err() != nil || !state.isLast(attempt) {
			logger.Info("pairing connection closed", slog.Any("error", err))
			return
		}
		state.report(acceptResult{err: err})
		return
	}
	state.report(acceptResult{record: record})
	r.drain(c, logger)
}

func (r *Responder) pair(state *acceptState, c *conn) (record *Record, attempt uint64, err error) {
	c.conn.SetDeadline(time.Now().Add(responderHandshakeTimeout))
	err = r.handshake(c)
	if err != nil {
		return nil, 0, err
	}
	c.conn.SetDeadline(time.Now().Add(responderSetupStartTimeout))
	data, err := c.readPairingData()
	if err != nil {
		return nil, 0, fmt.Errorf("read pair-setup M1: %w", err)
	}
	m1, err := tlv8.Decode(data)
	if err != nil || m1.State() != 1 {
		return nil, 0, fmt.Errorf("unexpected pair-setup M1 %x", data)
	}
	return r.pairSetup(state, c)
}

func (s *acceptState) isLast(attempt uint64) bool {
	s.access.Lock()
	defer s.access.Unlock()
	return attempt == s.attempt
}

func (s *acceptState) report(result acceptResult) {
	select {
	case s.results <- result:
	default:
	}
}

func (r *Responder) drain(c *conn, logger *slog.Logger) {
	r.drainAccess.Lock()
	if r.closed {
		r.drainAccess.Unlock()
		c.conn.Close()
		return
	}
	if r.draining == nil {
		r.draining = make(map[net.Conn]struct{})
	}
	r.draining[c.conn] = struct{}{}
	r.drainAccess.Unlock()
	go func() {
		defer func() {
			r.drainAccess.Lock()
			delete(r.draining, c.conn)
			r.drainAccess.Unlock()
			c.conn.Close()
		}()
		if tcpConn, isTCP := c.conn.(*net.TCPConn); isTCP {
			tcpConn.CloseWrite()
		}
		c.conn.SetDeadline(time.Now().Add(responderDrainTimeout))
		for {
			message, err := c.read()
			if err != nil {
				return
			}
			logger.Debug("message after pair-setup", slog.Any("message", message.Plain))
		}
	}()
}

// Handshake keys and option values match idevice PairableHost defaults
// (idevice/src/remote_pairing/responder.rs, handshake).
func (r *Responder) handshake(c *conn) error {
	request, err := c.readPlain()
	if err != nil {
		return fmt.Errorf("read handshake: %w", err)
	}
	handshake := lookupObject(request, "request", "_0", "handshake", "_0")
	if handshake == nil {
		return fmt.Errorf("unexpected handshake request %v", request)
	}
	r.logger.Debug("pairing handshake", slog.Any("request", handshake))
	if attempt, _ := lookup(handshake, "hostOptions", "attemptPairVerify").(bool); attempt {
		return errors.New("the device asked for pair-verify, which this responder does not support; only new pairings are accepted")
	}
	return c.writePlain(object{"response": object{
		"forRequestIdentifier": 0,
		"_1": object{"handshake": object{"_0": object{
			"wireProtocolVersion":                 responderWireProtocolVersion,
			"minimumSupportedWireProtocolVersion": responderMinimumWireProtocol,
			"deviceOptions": object{
				"allowsPairSetup":                 true,
				"allowsPinlessPairing":            false,
				"allowsIncomingTunnelConnections": false,
				"allowsUpgradeOfLockdownPairings": false,
				"allowsSharingSensitiveInfo":      false,
			},
			"peerDeviceInfo": object{
				"udid":                           "",
				"deviceKVSIncludesSensitiveInfo": false,
				"identifier":                     "",
				"name":                           r.options.HostName,
				"model":                          responderModel,
			},
		}}},
	}})
}

func (r *Responder) writeSetup(c *conn, items ...tlv8.Item) error {
	return c.writePlain(pairingDataEvent("setupManualPairing", tlv8.Encode(items...), false))
}

func (r *Responder) readSetup(c *conn, state byte) (tlv8.Values, error) {
	data, err := c.readPairingData()
	if err != nil {
		return nil, err
	}
	values, err := tlv8.Decode(data)
	if err != nil {
		return nil, err
	}
	if code, failed := values.Error(); failed {
		return nil, fmt.Errorf("the device reported %s", tlv8.ErrorString(code))
	}
	if values.State() != state {
		return nil, fmt.Errorf("expected state %d, got %d", state, values.State())
	}
	return values, nil
}

func (r *Responder) pairSetup(state *acceptState, c *conn) (*Record, uint64, error) {
	number, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return nil, 0, err
	}
	pin := fmt.Sprintf("%06d", number.Int64())
	server := newSRPServer(pin)
	state.access.Lock()
	if state.ctx.Err() != nil {
		state.access.Unlock()
		return nil, 0, state.ctx.Err()
	}
	state.attempt++
	attempt := state.attempt
	r.options.OnPIN(pin)
	state.access.Unlock()
	record, err := r.pairSetupSteps(c, server)
	if err != nil {
		return nil, attempt, fmt.Errorf("pair-setup: %w", err)
	}
	return record, attempt, nil
}

func (r *Responder) pairSetupSteps(c *conn, server *srpServer) (*Record, error) {
	c.conn.SetDeadline(time.Now().Add(responderCodeTimeout))
	err := r.writeSetup(c,
		tlv8.Item{Type: tlv8.TypeState, Value: []byte{2}},
		tlv8.Item{Type: tlv8.TypeSalt, Value: server.salt},
		tlv8.Item{Type: tlv8.TypePublicKey, Value: server.PublicKey()},
	)
	if err != nil {
		return nil, fmt.Errorf("send M2: %w", err)
	}
	m3, err := r.readSetup(c, 3)
	if err != nil {
		return nil, fmt.Errorf("read M3: %w", err)
	}
	c.conn.SetDeadline(time.Now().Add(responderStepTimeout))
	key, serverProof, err := server.Verify(m3[tlv8.TypePublicKey], m3[tlv8.TypeProof])
	if err != nil {
		r.writeSetup(c,
			tlv8.Item{Type: tlv8.TypeState, Value: []byte{4}},
			tlv8.Item{Type: tlv8.TypeError, Value: []byte{tlv8.ErrorAuthentication}},
		)
		return nil, err
	}
	err = r.writeSetup(c,
		tlv8.Item{Type: tlv8.TypeState, Value: []byte{4}},
		tlv8.Item{Type: tlv8.TypeProof, Value: serverProof},
	)
	if err != nil {
		return nil, fmt.Errorf("send M4: %w", err)
	}

	// idevice uses these Pair-Setup HKDF labels for M5/M6
	// (idevice/src/remote_pairing/responder.rs).
	setupKey, err := hkdf.Key(sha512.New, key, []byte("Pair-Setup-Encrypt-Salt"), "Pair-Setup-Encrypt-Info", chacha20poly1305.KeySize)
	if err != nil {
		return nil, err
	}
	setupCipher, err := chacha20poly1305.New(setupKey)
	if err != nil {
		return nil, err
	}
	m5, err := r.readSetup(c, 5)
	if err != nil {
		return nil, fmt.Errorf("read M5: %w", err)
	}
	plaintext, err := setupCipher.Open(nil, pairingNonce("PS-Msg05"), m5[tlv8.TypeEncryptedData], nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt M5: %w", err)
	}
	// idevice parses the decrypted M5 identity without verifying its signature
	// (idevice/src/remote_pairing/responder.rs, pair_setup).
	deviceValues, err := tlv8.Decode(plaintext)
	if err != nil {
		return nil, fmt.Errorf("M5: %w", err)
	}
	record := *r.identity
	record.DeviceIdentifier = string(deviceValues[tlv8.TypeIdentifier])
	if devicePublicKey := deviceValues[tlv8.TypePublicKey]; len(devicePublicKey) == ed25519.PublicKeySize {
		record.DevicePublicKey = devicePublicKey
	}
	if infoData := deviceValues[tlv8.TypeInfo]; len(infoData) > 0 {
		info, err := opack.Unmarshal(infoData)
		if err != nil {
			return nil, fmt.Errorf("M5 info: %w", err)
		}
		r.logger.Debug("paired device info", slog.Any("info", info))
		infoMap, _ := info.(map[string]any)
		record.AltIRK, _ = infoMap["altIRK"].([]byte)
		record.DeviceName, _ = infoMap["name"].(string)
		record.DeviceModel, _ = infoMap["model"].(string)
		record.DeviceUDID, _ = infoMap["remotepairing_udid"].(string)
	}
	// idevice rejects altIRK values whose length is not 16 bytes
	// (idevice/src/remote_pairing/peer_device.rs).
	if len(record.AltIRK) != 16 {
		return nil, fmt.Errorf("M5: the device sent an altIRK of %d bytes, expected 16", len(record.AltIRK))
	}

	m6, err := r.accessoryIdentity(&record, key)
	if err != nil {
		return nil, err
	}
	err = r.writeSetup(c,
		tlv8.Item{Type: tlv8.TypeEncryptedData, Value: setupCipher.Seal(nil, pairingNonce("PS-Msg06"), m6, nil)},
		tlv8.Item{Type: tlv8.TypeState, Value: []byte{6}},
	)
	if err != nil {
		return nil, fmt.Errorf("send M6: %w", err)
	}
	r.logger.Info("paired", slog.String("device", record.DeviceName), slog.String("udid", record.DeviceUDID))
	return &record, nil
}

func (r *Responder) accessoryIdentity(record *Record, key []byte) ([]byte, error) {
	accessoryX, err := hkdf.Key(sha512.New, key, []byte("Pair-Setup-Accessory-Sign-Salt"), "Pair-Setup-Accessory-Sign-Info", 32)
	if err != nil {
		return nil, err
	}
	signed := slices.Concat(accessoryX, []byte(record.Identifier), record.PublicKey)
	// Info keys and placeholder addresses match idevice PairableHost
	// (idevice/src/remote_pairing/responder.rs, build_accessory_identity_tlv).
	info, err := opack.Marshal(map[string]any{
		"altIRK":                      record.HostIRK,
		"btAddr":                      "11:22:33:44:55:66",
		"mac":                         []byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66},
		"remotepairing_serial_number": "AAAAAAAAAAAA",
		"accountID":                   record.Identifier,
		"remotepairing_udid":          "",
		"model":                       responderModel,
		"name":                        r.options.HostName,
	})
	if err != nil {
		return nil, err
	}
	return tlv8.Encode(
		tlv8.Item{Type: tlv8.TypeIdentifier, Value: []byte(record.Identifier)},
		tlv8.Item{Type: tlv8.TypePublicKey, Value: record.PublicKey},
		tlv8.Item{Type: tlv8.TypeSignature, Value: ed25519.Sign(record.PrivateKey, signed)},
		tlv8.Item{Type: tlv8.TypeInfo, Value: info},
	), nil
}

func (r *Responder) Close() error {
	r.drainAccess.Lock()
	r.closed = true
	for netConn := range r.draining {
		netConn.Close()
	}
	r.drainAccess.Unlock()
	return r.listener.Close()
}
