// DDI build 27A9266e ships dtremotedisplayd, dtuhidd and dtpasteboardd in
// usr/libexec in both image variants. Its PersonalizedDMG identities name
// ApBoardID and ApChipID; its Cryptex identity has neither. The three daemon
// binaries are identical between these two images (usr/libexec in
// Personalized/Image.dmg and Cryptex/Image.dmg, DDI 27A9266e;
// BuildManifest.plist, DDI 27A9266e).
package imagemounter

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nekohasekai/open-screen-sharing/internal/coredevice"
	E "github.com/sagernet/sing/common/exceptions"
)

const (
	// pymobiledevice3 uses this RSD shim for mobile_image_mounter
	// (pymobiledevice3 9c33842, pymobiledevice3/services/mobile_image_mounter.py:54).
	mounterService = "com.apple.mobile.mobile_image_mounter.shim.remote"
	// pymobiledevice3 uses this RemoteXPC service for cryptexd
	// (pymobiledevice3 9c33842, pymobiledevice3/services/cryptexd.py:384).
	cryptexService = "com.apple.security.cryptexd.remote"

	// pymobiledevice3 identifies the DDI cryptex as com.apple.MobileAsset.DDI
	// (pymobiledevice3 9c33842, pymobiledevice3/services/cryptexd.py:113).
	ddiCryptexIdentifier = "com.apple.MobileAsset.DDI"

	// pymobiledevice3 uses ImageType Personalized and queries the nonce and
	// stored ticket with PersonalizedImageType DeveloperDiskImage
	// (pymobiledevice3 9c33842,
	// pymobiledevice3/services/mobile_image_mounter.py:374,401-403,432).
	personalizedImageType = "Personalized"
	personalizedDDIType   = "DeveloperDiskImage"

	uploadTimeout = 5 * time.Minute
)

var (
	ErrAlreadyMounted   = E.New("imagemounter: a Developer Disk Image is already mounted")
	ErrDeveloperModeOff = E.New("imagemounter: Developer Mode is off")
)

type Client struct {
	device *coredevice.Device
	logger *slog.Logger
}

func New(device *coredevice.Device, logger *slog.Logger) *Client {
	return &Client{device: device, logger: logger}
}

type Status struct {
	DeveloperMode bool
	Personalized  bool
	Cryptex       bool
	CryptexError  error
}

func (s Status) Mounted() bool {
	return s.Personalized || s.Cryptex
}

func (c *Client) Status(ctx context.Context) (Status, error) {
	var status Status
	err := c.withMounter(ctx, func(conn *mounterConn) error {
		var err error
		status.DeveloperMode, err = conn.developerModeStatus()
		if err != nil {
			return err
		}
		status.Personalized, err = conn.imageMounted(personalizedImageType)
		return err
	})
	if err != nil {
		return Status{}, err
	}
	if c.device.HasService(cryptexService) {
		status.Cryptex, err = c.ddiCryptexInstalled(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return Status{}, ctx.Err()
			}
			c.logger.Warn("list installed cryptexes", "error", err)
			status.CryptexError = err
		}
	}
	return status, nil
}

func (c *Client) checkCanMount(ctx context.Context) error {
	status, err := c.Status(ctx)
	if err != nil {
		return err
	}
	if status.Mounted() {
		return ErrAlreadyMounted
	}
	if !status.DeveloperMode {
		return ErrDeveloperModeOff
	}
	return nil
}

func (c *Client) ecid(ctx context.Context) (uint64, error) {
	if ecid := c.device.Info().ECID; ecid != 0 {
		return ecid, nil
	}
	if !c.device.HasService(cryptexService) {
		return 0, E.New("imagemounter: the device's ECID is unknown")
	}
	chip, err := c.invokeCryptexd(ctx, "read-personalization-id", map[string]any{}, nil)
	if err != nil {
		return 0, E.Cause(err, "imagemounter: read the ECID")
	}
	ecid, ok := numberValue(chip["img4_chip_ecid"])
	if !ok {
		return 0, E.New("imagemounter: cryptexd's chip instance has no ECID")
	}
	return ecid, nil
}

type DeviceError struct {
	Command string
	Message string
	Detail  string
}

func (e *DeviceError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("imagemounter: %s: %s: %s", e.Command, e.Message, e.Detail)
	}
	return fmt.Sprintf("imagemounter: %s: %s", e.Command, e.Message)
}

func (e *DeviceError) Unwrap() error {
	switch {
	case strings.Contains(e.Detail, "Developer mode is not enabled"):
		return ErrDeveloperModeOff
	case strings.Contains(e.Detail, "is already mounted"):
		return ErrAlreadyMounted
	}
	return nil
}

func numberValue(value any) (uint64, bool) {
	switch value := value.(type) {
	case uint64:
		return value, true
	case int64:
		return uint64(value), value >= 0
	case string:
		return parseNumber(value)
	}
	return 0, false
}

func boolValue(value any) (bool, bool) {
	switch value := value.(type) {
	case bool:
		return value, true
	case uint64:
		return value != 0, true
	case int64:
		return value != 0, true
	}
	return false, false
}
