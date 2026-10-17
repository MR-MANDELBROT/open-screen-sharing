package hid

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/nekohasekai/open-screen-sharing/internal/coredevice/xpc"
	E "github.com/sagernet/sing/common/exceptions"
)

// The createService XPC types match idevice: PrimaryUsage, PrimaryUsagePage
// and _ServiceID are uint64 at the top level, while the Codable storage
// repeats properties in typed dictionaries (idevice d32c818,
// idevice/src/services/core_device/hid.rs:142-146, 276-321).
const (
	keyboardUsagePage = 0x01
	keyboardUsage     = 0x06
	keyboardVendorID  = 0x05ac
	keyboardProductID = 0x0250
	// pymobiledevice3 uses 0x100002001 as its default virtual keyboard
	// service ID (pymobiledevice3 1146717,
	// pymobiledevice3/remote/core_device/hid_service.py:123, 445).
	keyboardServiceID = 0x1_0000_2001

	keyboardProduct      = "Screen Sharing Keyboard"
	keyboardManufacturer = "Open Screen Sharing"
)

func createKeyboardPayload() map[string]any {
	storage := map[string]any{
		"Manufacturer":     map[string]any{"string": keyboardManufacturer},
		"Product":          map[string]any{"string": keyboardProduct},
		"ProductID":        map[string]any{"int": int64(keyboardProductID)},
		"VendorID":         map[string]any{"int": int64(keyboardVendorID)},
		"PrimaryUsage":     map[string]any{"int": int64(keyboardUsage)},
		"PrimaryUsagePage": map[string]any{"int": int64(keyboardUsagePage)},
		"DeviceUsagePairs": map[string]any{"array": []any{
			map[string]any{"dictionary": map[string]any{
				"DeviceUsage":     map[string]any{"int": int64(keyboardUsage)},
				"DeviceUsagePage": map[string]any{"int": int64(keyboardUsagePage)},
			}},
		}},
		"Transport":                      map[string]any{"string": "USB"},
		"ReportDescriptor":               map[string]any{"data": keyboardReportDescriptor},
		"UniversalControlVirtualService": map[string]any{"bool": true},
		"_ServiceID":                     map[string]any{"uint": uint64(keyboardServiceID)},
	}
	return map[string]any{"createService": map[string]any{"_0": map[string]any{
		"DeviceUsagePairs": []any{map[string]any{
			"DeviceUsage":     int64(keyboardUsage),
			"DeviceUsagePage": int64(keyboardUsagePage),
		}},
		"PrimaryUsage":                       uint64(keyboardUsage),
		"PrimaryUsagePage":                   uint64(keyboardUsagePage),
		"Product":                            keyboardProduct,
		"ProductID":                          int64(keyboardProductID),
		"VendorID":                           int64(keyboardVendorID),
		"_CoreDevice_codablePropertyStorage": storage,
		"_ServiceID":                         uint64(keyboardServiceID),
	}}}
}

func (c *Client) createKeyboard(ctx context.Context) error {
	if c.keyboard != 0 {
		return nil
	}
	conn, err := c.universalConn(ctx)
	if err != nil {
		return err
	}
	reply, err := conn.Request(ctx, universalRequest(createKeyboardPayload()))
	if err == nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		c.closeUniversal()
		return E.Cause(err, "hid: create keyboard")
	}
	serviceID, ok := uintValue(reply["serviceID"])
	if !ok || serviceID == 0 {
		return E.New("hid: create keyboard: reply without a service ID: ", strings.Join(slices.Sorted(maps.Keys(reply)), ", "))
	}
	c.keyboard = serviceID
	c.logger.Debug("created keyboard", "service", serviceID)
	return nil
}

func (c *Client) removeKeyboard() error {
	if c.keyboard == 0 {
		return nil
	}
	if err := removeService(c.universal, c.keyboard); err != nil {
		c.closeUniversal()
		return E.Cause(err, "hid: remove keyboard")
	}
	c.keyboard = 0
	c.pressed = [keyboardBitmapSize]byte{}
	return nil
}

// idevice sends resetGestureState before removeService, without reply
// flags (idevice d32c818, idevice/src/services/core_device/hid.rs:886-891, 987-997).
func removeService(conn *xpc.Conn, serviceID uint64) error {
	for _, call := range []string{"resetGestureState", "removeService"} {
		err := conn.Send(universalRequest(map[string]any{
			call: map[string]any{"_0": serviceID},
		}), false)
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) KeyDown(ctx context.Context, usage uint8) error {
	return c.changeKey(ctx, usage, true)
}

func (c *Client) KeyUp(ctx context.Context, usage uint8) error {
	return c.changeKey(ctx, usage, false)
}

func (c *Client) changeKey(ctx context.Context, usage uint8, down bool) error {
	pressed := c.pressed
	if down {
		pressed[usage/8] |= 1 << (usage % 8)
	} else {
		pressed[usage/8] &^= 1 << (usage % 8)
	}
	return c.sendKeys(ctx, pressed)
}

func (c *Client) releaseKeys(ctx context.Context) error {
	if c.keyboard == 0 || c.pressed == [keyboardBitmapSize]byte{} {
		return nil
	}
	return c.sendKeys(ctx, [keyboardBitmapSize]byte{})
}

func (c *Client) sendKeys(ctx context.Context, pressed [keyboardBitmapSize]byte) error {
	if err := c.createKeyboard(ctx); err != nil {
		return err
	}
	if err := c.sendReport(ctx, c.keyboard, keyboardReport(&pressed, now())); err != nil {
		return err
	}
	c.pressed = pressed
	return nil
}
