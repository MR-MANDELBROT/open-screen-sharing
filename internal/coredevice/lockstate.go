package coredevice

import (
	"context"
	"fmt"

	"github.com/nekohasekai/open-screen-sharing/internal/coredevice/rsd"
	E "github.com/sagernet/sing/common/exceptions"
)

const remoteLockdownService = "com.apple.mobile.lockdown.remote.trusted"

type LockState struct {
	PasscodeRequired bool
}

type LockStateConn struct {
	conn *rsd.PlistConn
}

func (d *Device) DialLockState(ctx context.Context) (*LockStateConn, error) {
	conn, err := d.DialShim(ctx, remoteLockdownService)
	if err != nil {
		return nil, err
	}
	return &LockStateConn{conn: conn}, nil
}

// CoreDeviceService implements its lockstate action by reading the lockdown
// PasswordProtected value without starting a session, rather than invoking
// deviceinfo's getlockstate feature (CoreDeviceService 651.19, macOS 27.0.1
// 26A434, UUID 1AF54B66-86E0-36F3-B054-FE83F8016E22, 0x10010677c..0x100106968;
// CoreDevice 651.19, UUID 090CB4EF-90CA-32E9-846D-9B5B45C52F7B, imports no
// AMDeviceStartSession).
func (c *LockStateConn) LockState(ctx context.Context) (LockState, error) {
	reply, err := c.conn.Request(ctx, map[string]any{
		"Label":           checkinLabel,
		"ProtocolVersion": "2",
		"Request":         "GetValue",
		"Key":             "PasswordProtected",
	})
	if err != nil {
		return LockState{}, err
	}
	if message, failed := reply["Error"]; failed {
		return LockState{}, E.New("read the device's lock state: ", fmt.Sprint(message))
	}
	required, ok := reply["Value"].(bool)
	if !ok || reply["Request"] != "GetValue" || reply["Key"] != "PasswordProtected" {
		return LockState{}, E.New("the device did not report whether a passcode is required")
	}
	return LockState{PasscodeRequired: required}, nil
}

func (c *LockStateConn) Close() error {
	return c.conn.Close()
}
