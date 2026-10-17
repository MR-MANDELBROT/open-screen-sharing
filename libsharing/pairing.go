package libsharing

import (
	"context"
	"encoding/base64"
	"errors"
	"maps"
	"slices"
	"strings"

	"github.com/nekohasekai/open-screen-sharing/internal/coredevice/lockdown"
	"github.com/nekohasekai/open-screen-sharing/internal/coredevice/pairing"
	"github.com/sagernet/sing/common"
)

type PairingHandler interface {
	OnPIN(pin string)
	OnPaired(record []byte)
	OnPairingFailed(errorCode int32, message string)
}

type PairingResponder struct {
	handler   PairingHandler
	responder *pairing.Responder
	ctx       context.Context
	cancel    context.CancelFunc
}

func NewPairingResponderWithRecord(hostName string, record []byte, handler PairingHandler) (*PairingResponder, error) {
	var identity *pairing.Record
	if len(record) > 0 {
		file, err := parsePairingFile(record)
		if err != nil {
			return nil, err
		}
		identity = file.remote
	}
	ctx, cancel := context.WithCancel(context.Background())
	responder, err := pairing.NewResponder(pairing.ResponderOptions{
		HostName: hostName,
		Identity: identity,
		OnPIN: func(pin string) {
			if ctx.Err() == nil {
				handler.OnPIN(pin)
			}
		},
		Logger: newLogger(),
	})
	if err != nil {
		cancel()
		return nil, err
	}
	return &PairingResponder{handler: handler, responder: responder, ctx: ctx, cancel: cancel}, nil
}

func (r *PairingResponder) Port() int32 { return int32(r.responder.Port()) }

func (r *PairingResponder) ServiceType() string { return pairing.ServiceType }

func (r *PairingResponder) ServiceName() string { return r.responder.ServiceName() }

func (r *PairingResponder) TXTRecord() *TXTRecord {
	entries := r.responder.TXTRecord()
	keys := slices.Sorted(maps.Keys(entries))
	return &TXTRecord{keys: keys, values: common.Map(keys, func(key string) string {
		return entries[key]
	})}
}

func (r *PairingResponder) Start() {
	go r.accept()
}

func (r *PairingResponder) accept() {
	record, err := r.responder.Accept(r.ctx)
	var data []byte
	if err == nil {
		data, err = record.Marshal()
	}
	if r.ctx.Err() != nil {
		return
	}
	if err != nil {
		r.handler.OnPairingFailed(DeviceErrorPairing, err.Error())
		return
	}
	r.handler.OnPaired(data)
}

func (r *PairingResponder) Close() {
	r.cancel()
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
	DeviceName      string
	CanMatchService bool
}

func ParsePairingRecord(record []byte) (*PairingRecordInfo, error) {
	file, err := parsePairingFile(record)
	if err != nil {
		return nil, err
	}
	if file.lockdown != nil {
		return &PairingRecordInfo{}, nil
	}
	return &PairingRecordInfo{
		DeviceName:      file.remote.DeviceName,
		CanMatchService: len(file.remote.AltIRK) == 16,
	}, nil
}

// idevice validates Bonjour authTag as SipHash of identifier with altIRK,
// taking reversed bytes 0 through 5 of the little-endian result
// (idevice 3854a5d, idevice/src/remote_pairing/peer_device.rs:33-45,
// idevice/src/remote_pairing/peer_device.rs:129-140).
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
