//go:build darwin && !ios

package coredevice

import (
	"context"
	"log/slog"
	"os"

	"github.com/google/uuid"
	"github.com/nekohasekai/open-screen-sharing/internal/coredevice/lockdown"
)

func ConnectUSBMux(ctx context.Context, udid string, logger *slog.Logger) (*Device, error) {
	// pymobiledevice3 uses its stable host ID for RSD on a tunnel of its own
	// (remote/remotexpc.py, default_handshake_uuid).
	hostName, _ := os.Hostname()
	identity := uuid.NewSHA1(uuid.NameSpaceOID, []byte("open-screen-sharing:usbmux:"+hostName))
	return retry(ctx, logger, func() (*Device, error) {
		record, dial, err := lockdown.OpenUSBMux(ctx, udid)
		if err != nil {
			return nil, err
		}
		return connectLockdown(ctx, record, dial, identity, logger)
	})
}
