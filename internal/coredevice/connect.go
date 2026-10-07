package coredevice

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/nekohasekai/open-screen-sharing/internal/coredevice/lockdown"
	"github.com/nekohasekai/open-screen-sharing/internal/coredevice/pairing"
	"github.com/nekohasekai/open-screen-sharing/internal/coredevice/tlspsk"
	"github.com/nekohasekai/open-screen-sharing/internal/coredevice/tunnel"
)

type RemotePairingOptions struct {
	Address netip.AddrPort
	Record  *pairing.Record
	Logger  *slog.Logger
}

var ErrLoopbackAddress = errors.New("remote pairing through a loopback address is refused: " +
	"it leaves the device's remotepairingd unable to set up tunnels until Developer Mode is turned off and on again; " +
	"use the VPN's loopback address (such as 10.7.0.1) instead")

const (
	connectAttempts = 3
	retryDelay      = 500 * time.Millisecond
)

func ConnectRemotePairing(ctx context.Context, options RemotePairingOptions) (*Device, error) {
	if address := options.Address.Addr().Unmap(); address.IsLoopback() || address.IsUnspecified() {
		return nil, ErrLoopbackAddress
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	identity := handshakeIdentity(options.Record.Identifier)
	return retry(ctx, logger, func() (*Device, error) {
		return connectRemotePairing(ctx, options.Address, options.Record, identity, logger)
	})
}

func connectRemotePairing(ctx context.Context, address netip.AddrPort, record *pairing.Record, identity uuid.UUID, logger *slog.Logger) (*Device, error) {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", address.String())
	if err != nil {
		return nil, fmt.Errorf("connect to remote pairing: %w", err)
	}
	session, err := pairing.Verify(ctx, conn, record, logger)
	if err != nil {
		return nil, err
	}
	port, err := session.CreateListener(ctx)
	if err != nil {
		session.Close()
		return nil, err
	}
	logger.Debug("remote pairing listener", slog.Int("port", int(port)))
	tunnelConn, err := dialer.DialContext(ctx, "tcp", netip.AddrPortFrom(address.Addr(), port).String())
	if err != nil {
		session.Close()
		return nil, fmt.Errorf("connect to tunnel port: %w", err)
	}
	secureConn, err := tlspsk.Client(ctx, tunnelConn, session.Key())
	if err != nil {
		session.Close()
		return nil, fmt.Errorf("tunnel TLS: %w", err)
	}
	tun, err := tunnel.Open(ctx, secureConn, logger)
	if err != nil {
		session.Close()
		return nil, err
	}
	return newDevice(ctx, tun, session, identity, logger)
}

type LockdownOptions struct {
	Address netip.Addr
	Record  *lockdown.PairRecord
	Logger  *slog.Logger
}

func ConnectLockdown(ctx context.Context, options LockdownOptions) (*Device, error) {
	logger := options.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	dial := func(ctx context.Context, port uint16) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "tcp", netip.AddrPortFrom(options.Address, port).String())
	}
	identity := handshakeIdentity(options.Record.HostID)
	return retry(ctx, logger, func() (*Device, error) {
		return connectLockdown(ctx, options.Record, dial, identity, logger)
	})
}

func connectLockdown(ctx context.Context, record *lockdown.PairRecord, dial lockdown.Dialer, identity uuid.UUID, logger *slog.Logger) (*Device, error) {
	conn, err := lockdown.DialService(ctx, record, dial, lockdown.CoreDeviceProxy, logger)
	if err != nil {
		return nil, err
	}
	tun, err := tunnel.Open(ctx, conn, logger)
	if err != nil {
		return nil, err
	}
	return newDevice(ctx, tun, nil, identity, logger)
}

// pymobiledevice3 sends a stable host UUID in RSD handshakes
// (remote/remotexpc.py, default_handshake_uuid).
func handshakeIdentity(name string) uuid.UUID {
	identity, err := uuid.Parse(name)
	if err != nil {
		identity = uuid.NewSHA1(uuid.NameSpaceOID, []byte("open-screen-sharing:"+name))
	}
	return identity
}

func retry(ctx context.Context, logger *slog.Logger, connect func() (*Device, error)) (*Device, error) {
	for attempt := 1; ; attempt++ {
		device, err := connect()
		if err == nil || attempt == connectAttempts || !isTransient(err) || ctx.Err() != nil {
			return device, err
		}
		logger.Debug("connect failed, retrying", slog.Int("attempt", attempt), slog.Any("error", err))
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(retryDelay * time.Duration(attempt)):
		}
	}
}

func isTransient(err error) bool {
	var rejected *pairing.PairingRejectedError
	var alert *tlspsk.AlertError
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded),
		errors.Is(err, pairing.ErrPairVerifyFailed), errors.As(err, &rejected), errors.As(err, &alert):
		return false
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF),
		errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.ECONNREFUSED),
		errors.Is(err, syscall.EPIPE), errors.Is(err, syscall.ECONNABORTED),
		errors.Is(err, syscall.ETIMEDOUT), errors.Is(err, net.ErrClosed):
		return true
	}
	var netError net.Error
	return errors.As(err, &netError) && netError.Timeout()
}
