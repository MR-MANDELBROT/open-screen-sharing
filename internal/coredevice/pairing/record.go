package pairing

import (
	"bytes"
	"crypto/ed25519"

	E "github.com/sagernet/sing/common/exceptions"
	"howett.net/plist"
)

type Record struct {
	Identifier string
	PublicKey  ed25519.PublicKey
	PrivateKey ed25519.PrivateKey
	AltIRK     []byte
	HostIRK    []byte

	DeviceIdentifier string
	DevicePublicKey  ed25519.PublicKey
	DeviceName       string
	DeviceModel      string
	DeviceUDID       string
}

// idevice RpPairingFile uses these first four plist keys and stores a
// 32-byte Ed25519 seed in private_key (idevice 3854a5d,
// idevice/src/remote_pairing/rp_pairing_file.rs:69-121).
type recordFile struct {
	PublicKey        []byte `plist:"public_key"`
	PrivateKey       []byte `plist:"private_key"`
	Identifier       string `plist:"identifier"`
	AltIRK           []byte `plist:"alt_irk,omitempty"`
	HostIRK          []byte `plist:"host_irk,omitempty"`
	DeviceIdentifier string `plist:"device_identifier,omitempty"`
	DevicePublicKey  []byte `plist:"device_public_key,omitempty"`
	DeviceName       string `plist:"device_name,omitempty"`
	DeviceModel      string `plist:"device_model,omitempty"`
	DeviceUDID       string `plist:"device_udid,omitempty"`
}

// go-ios selfIdentity.plist uses identifier, irk, privateKey and publicKey;
// privateKey is the Ed25519 seed (go-ios ca2c56d,
// ios/tunnel/pairrecord.go:16-28, 51).
type selfIdentityFile struct {
	Identifier string `plist:"identifier"`
	IRK        []byte `plist:"irk"`
	PrivateKey []byte `plist:"privateKey"`
	PublicKey  []byte `plist:"publicKey"`
}

func ParseRecord(data []byte) (*Record, error) {
	var keys map[string]any
	_, err := plist.Unmarshal(data, &keys)
	if err != nil {
		return nil, E.Cause(err, "parse pair record")
	}
	switch {
	case keys["private_key"] != nil:
		var file recordFile
		_, err = plist.Unmarshal(data, &file)
		if err != nil {
			return nil, E.Cause(err, "parse pair record")
		}
		record := &Record{
			Identifier:       file.Identifier,
			AltIRK:           file.AltIRK,
			HostIRK:          file.HostIRK,
			DeviceIdentifier: file.DeviceIdentifier,
			DeviceName:       file.DeviceName,
			DeviceModel:      file.DeviceModel,
			DeviceUDID:       file.DeviceUDID,
		}
		if len(file.DevicePublicKey) == ed25519.PublicKeySize {
			record.DevicePublicKey = file.DevicePublicKey
		}
		return record, record.setKeys(file.PrivateKey, file.PublicKey)
	case keys["privateKey"] != nil:
		var file selfIdentityFile
		_, err = plist.Unmarshal(data, &file)
		if err != nil {
			return nil, E.Cause(err, "parse pair record")
		}
		record := &Record{Identifier: file.Identifier, HostIRK: file.IRK}
		return record, record.setKeys(file.PrivateKey, file.PublicKey)
	default:
		return nil, E.New("pairing: not a pairing record")
	}
}

func (r *Record) setKeys(seed []byte, publicKey []byte) error {
	if r.Identifier == "" {
		return E.New("pairing: pair record: missing identifier")
	}
	if len(seed) != ed25519.SeedSize {
		return E.New("pairing: pair record: private key is ", len(seed), " bytes, expected ", ed25519.SeedSize)
	}
	r.PrivateKey = ed25519.NewKeyFromSeed(seed)
	r.PublicKey = r.PrivateKey.Public().(ed25519.PublicKey)
	if len(publicKey) > 0 && !bytes.Equal(publicKey, r.PublicKey) {
		return E.New("pairing: pair record: public key does not match private key")
	}
	return nil
}

func (r *Record) Marshal() ([]byte, error) {
	return plist.MarshalIndent(recordFile{
		PublicKey:        r.PrivateKey.Public().(ed25519.PublicKey),
		PrivateKey:       r.PrivateKey.Seed(),
		Identifier:       r.Identifier,
		AltIRK:           r.AltIRK,
		HostIRK:          r.HostIRK,
		DeviceIdentifier: r.DeviceIdentifier,
		DevicePublicKey:  r.DevicePublicKey,
		DeviceName:       r.DeviceName,
		DeviceModel:      r.DeviceModel,
		DeviceUDID:       r.DeviceUDID,
	}, plist.XMLFormat, "\t")
}
