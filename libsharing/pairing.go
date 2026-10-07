package libsharing

import (
	"context"
	"encoding/base64"
	"errors"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/nekohasekai/open-screen-sharing/internal/coredevice/lockdown"
	"github.com/nekohasekai/open-screen-sharing/internal/coredevice/pairing"
)

type PairingHandler interface {
	OnPIN(pin string)
	OnPaired(record []byte)
	OnPairingFailed(errorCode int32, message string)
}

type PairingResponder struct {
	handler   PairingHandler
	responder *pairing.Responder

	access sync.Mutex
	cancel context.CancelFunc
	closed bool
}

func NewPairingResponderWithRecord(hostName string, record []byte, handler PairingHandler) (*PairingResponder, error) {
	if handler == nil {
		return nil, errors.New("no pairing handler")
	}
	var identity *pairing.Record
	if len(record) > 0 {
		file, err := parsePairingFile(record)
		if err != nil {
			return nil, err
		}
		identity = file.remote
	}
	r := &PairingResponder{handler: handler}
	responder, err := pairing.NewResponder(pairing.ResponderOptions{
		HostName: hostName,
		Identity: identity,
		OnPIN: func(pin string) {
			if r.open() {
				handler.OnPIN(pin)
			}
		},
		Logger: newLogger(),
	})
	if err != nil {
		return nil, err
	}
	r.responder = responder
	return r, nil
}

func (r *PairingResponder) Port() int32 { return int32(r.responder.Port()) }

func (r *PairingResponder) ServiceType() string { return pairing.ServiceType }

func (r *PairingResponder) ServiceName() string { return r.responder.ServiceName() }

func (r *PairingResponder) TXTRecord() *TXTRecord {
	entries := r.responder.TXTRecord()
	record := &TXTRecord{keys: slices.Sorted(maps.Keys(entries))}
	for _, key := range record.keys {
		record.values = append(record.values, entries[key])
	}
	return record
}

func (r *PairingResponder) Start() {
	r.access.Lock()
	defer r.access.Unlock()
	if r.closed || r.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	go r.accept(ctx, cancel)
}

func (r *PairingResponder) accept(ctx context.Context, cancel context.CancelFunc) {
	record, err := r.responder.Accept(ctx)
	var data []byte
	if err == nil {
		data, err = record.Marshal()
	}
	r.access.Lock()
	closed := r.closed
	r.cancel = nil
	r.access.Unlock()
	cancel()
	if closed {
		return
	}
	if err != nil {
		r.handler.OnPairingFailed(DeviceErrorPairing, err.Error())
		return
	}
	r.handler.OnPaired(data)
}

func (r *PairingResponder) open() bool {
	r.access.Lock()
	defer r.access.Unlock()
	return !r.closed
}

func (r *PairingResponder) Close() {
	r.access.Lock()
	if r.closed {
		r.access.Unlock()
		return
	}
	r.closed = true
	cancel := r.cancel
	r.access.Unlock()
	if cancel != nil {
		cancel()
	}
	_ = r.responder.Close()
}

type pairingFile struct {
	remote   *pairing.Record
	lockdown *lockdown.PairRecord
}

func parsePairingFile(data []byte) (pairingFile, error) {
	record, err := lockdown.ParsePairRecord(data)
	if !errors.Is(err, lockdown.ErrNotPairRecord) {
		return pairingFile{lockdown: record}, err
	}
	remote, err := pairing.ParseRecord(data)
	return pairingFile{remote: remote}, err
}

type TXTRecord struct {
	keys   []string
	values []string
}

func (t *TXTRecord) Len() int32 { return int32(len(t.keys)) }

func (t *TXTRecord) Key(index int32) string { return t.keys[index] }

func (t *TXTRecord) Value(index int32) string { return t.values[index] }

type PairingRecordInfo struct {
	Identifier       string
	DeviceIdentifier string
	DeviceName       string
	DeviceModel      string
	DeviceUDID       string
	CanMatchService  bool
}

func ParsePairingRecord(record []byte) (*PairingRecordInfo, error) {
	file, err := parsePairingFile(record)
	if err != nil {
		return nil, err
	}
	if file.lockdown != nil {
		return &PairingRecordInfo{Identifier: file.lockdown.HostID, DeviceUDID: file.lockdown.UDID}, nil
	}
	parsed := file.remote
	return &PairingRecordInfo{
		Identifier:       parsed.Identifier,
		DeviceIdentifier: parsed.DeviceIdentifier,
		DeviceName:       parsed.DeviceName,
		DeviceModel:      parsed.DeviceModel,
		DeviceUDID:       parsed.DeviceUDID,
		CanMatchService:  len(parsed.AltIRK) == 16,
	}, nil
}

// idevice validates Bonjour authTag as SipHash of identifier with altIRK,
// taking reversed bytes 0 through 5 of the little-endian result
// (idevice/src/remote_pairing/peer_device.rs, validate_auth_tag).
func PairingRecordMatchesService(record []byte, identifier string, authTag string) bool {
	parsed, err := pairing.ParseRecord(record)
	if err != nil || len(parsed.AltIRK) != 16 || identifier == "" {
		return false
	}
	tag, err := base64.StdEncoding.DecodeString(strings.TrimSpace(authTag))
	if err != nil || len(tag) != 6 {
		return false
	}
	return string(pairing.AuthTag(parsed.AltIRK, identifier)) == string(tag)
}
