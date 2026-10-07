// The lockdownd session follows go-ios (ios/lockdown.go,
// ios/startsession.go; MIT, Copyright (c) 2019 danielpaulus).

package lockdown

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"

	"github.com/nekohasekai/open-screen-sharing/internal/coredevice/rsd"
	"howett.net/plist"
)

const Port = 62078

// go-ios opens CoreDeviceProxy through lockdownd, then exchanges CDTunnel
// parameters over that connection.
const CoreDeviceProxy = "com.apple.internal.devicecompute.CoreDeviceProxy"

const progName = "open-screen-sharing"

var ErrNotPairRecord = errors.New("not a lockdown pair record")

type PairRecord struct {
	HostID      string
	SystemBUID  string
	UDID        string
	Certificate tls.Certificate
}

// jitterbugpair writes the usbmuxd pair record with the device's UDID added
// (JitterbugPair/main.c).
type pairRecordFile struct {
	HostID          string `plist:"HostID"`
	SystemBUID      string `plist:"SystemBUID"`
	UDID            string `plist:"UDID"`
	HostCertificate []byte `plist:"HostCertificate"`
	HostPrivateKey  []byte `plist:"HostPrivateKey"`
}

func ParsePairRecord(data []byte) (*PairRecord, error) {
	var file pairRecordFile
	_, err := plist.Unmarshal(data, &file)
	if err != nil || file.HostID == "" || len(file.HostCertificate) == 0 {
		return nil, ErrNotPairRecord
	}
	certificate, err := tls.X509KeyPair(file.HostCertificate, file.HostPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("pair record certificate: %w", err)
	}
	return &PairRecord{
		HostID:      file.HostID,
		SystemBUID:  file.SystemBUID,
		UDID:        file.UDID,
		Certificate: certificate,
	}, nil
}

type Dialer func(ctx context.Context, port uint16) (net.Conn, error)

func DialService(ctx context.Context, record *PairRecord, dial Dialer, service string, logger *slog.Logger) (net.Conn, error) {
	tlsConfig := &tls.Config{
		Certificates:       []tls.Certificate{record.Certificate},
		InsecureSkipVerify: true,
	}
	conn, err := dial(ctx, Port)
	if err != nil {
		return nil, fmt.Errorf("connect to lockdown: %w", err)
	}
	defer conn.Close()
	lockdown := rsd.NewPlistConn(conn)
	reply, err := lockdown.Request(ctx, map[string]any{
		"Label":           progName,
		"ProtocolVersion": "2",
		"Request":         "StartSession",
		"HostID":          record.HostID,
		"SystemBUID":      record.SystemBUID,
	})
	if err != nil {
		return nil, fmt.Errorf("lockdown StartSession: %w", err)
	}
	if message, failed := reply["Error"]; failed {
		return nil, fmt.Errorf("lockdown StartSession: %v", message)
	}
	if reply["EnableSessionSSL"] == true {
		tlsConn := tls.Client(conn, tlsConfig)
		err = tlsConn.HandshakeContext(ctx)
		if err != nil {
			return nil, fmt.Errorf("lockdown TLS: %w", err)
		}
		lockdown = rsd.NewPlistConn(tlsConn)
	}
	reply, err = lockdown.Request(ctx, map[string]any{
		"Label":           progName,
		"ProtocolVersion": "2",
		"Request":         "StartService",
		"Service":         service,
	})
	if err != nil {
		return nil, fmt.Errorf("lockdown StartService %s: %w", service, err)
	}
	if message, failed := reply["Error"]; failed {
		return nil, fmt.Errorf("lockdown StartService %s: %v", service, message)
	}
	port, _ := reply["Port"].(uint64)
	if port == 0 || port > 65535 {
		return nil, fmt.Errorf("lockdown StartService %s: no port in %v", service, reply)
	}
	logger.Debug("started lockdown service", slog.String("service", service), slog.Uint64("port", port))
	serviceConn, err := dial(ctx, uint16(port))
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", service, err)
	}
	if reply["EnableServiceSSL"] == true {
		tlsConn := tls.Client(serviceConn, tlsConfig)
		err = tlsConn.HandshakeContext(ctx)
		if err != nil {
			serviceConn.Close()
			return nil, fmt.Errorf("%s TLS: %w", service, err)
		}
		return tlsConn, nil
	}
	return serviceConn, nil
}
