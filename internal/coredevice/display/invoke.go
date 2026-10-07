package display

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/nekohasekai/open-screen-sharing/internal/coredevice/xpc"
)

var ErrNoReply = errors.New("the display service did not reply; dtremotedisplayd may be wedged, rebooting the device clears it")

const defaultInvokeTimeout = 15 * time.Second

type Error struct {
	Feature     string
	Code        int64
	Domain      string
	Description string
}

const (
	ErrorCodeUnsupported = 9021
	ErrorCodeMediaInUse  = 9022
)

func (e *Error) Error() string {
	description := e.Description
	if description == "" {
		description = "failed"
	}
	if e.Domain != "" {
		return fmt.Sprintf("%s: %s (%s %d)", e.Feature, description, e.Domain, e.Code)
	}
	return fmt.Sprintf("%s: %s (code %d)", e.Feature, description, e.Code)
}

func IsMediaInUse(err error) bool {
	var coreDeviceError *Error
	return errors.As(err, &coreDeviceError) && coreDeviceError.Code == ErrorCodeMediaInUse
}

func IsUnsupported(err error) bool {
	var coreDeviceError *Error
	return errors.As(err, &coreDeviceError) && coreDeviceError.Code == ErrorCodeUnsupported
}

// pymobiledevice3 1146717 uses CoreDevice 629.3 and DDI protocol 2
// (remote/core_device/core_device_service.py:19, 78-79).
var coreDeviceVersion = map[string]any{
	"components":              []any{uint64(629), uint64(3)},
	"originalComponentsCount": int64(2),
	"stringValue":             "629.3",
}

func coreDeviceRequest(deviceIdentifier, feature, action string, input map[string]any) map[string]any {
	request := map[string]any{
		"CoreDevice.CoreDeviceDDIProtocolVersion": int64(2),
		"CoreDevice.action":                       map[string]any{},
		"CoreDevice.coreDeviceVersion":            coreDeviceVersion,
		"CoreDevice.deviceIdentifier":             deviceIdentifier,
		"CoreDevice.featureIdentifier":            feature,
		"CoreDevice.input":                        input,
		"CoreDevice.invocationIdentifier":         uuid.NewString(),
	}
	if action != "" {
		request["CoreDevice.actionIdentifier"] = action
	}
	return request
}

func request(ctx context.Context, conn *xpc.Conn, message map[string]any) (map[string]any, error) {
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultInvokeTimeout)
		defer cancel()
	}
	reply, err := conn.Request(ctx, message)
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return nil, ErrNoReply
	}
	if err != nil {
		return nil, err
	}
	return reply, nil
}

func invoke(ctx context.Context, conn *xpc.Conn, deviceIdentifier, feature, action string, input map[string]any) (map[string]any, error) {
	reply, err := request(ctx, conn, coreDeviceRequest(deviceIdentifier, feature, action, input))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", feature, err)
	}
	if output, isDictionary := reply["CoreDevice.output"].(map[string]any); isDictionary {
		return output, nil
	}
	return nil, parseCoreDeviceError(feature, reply)
}

func parseCoreDeviceError(feature string, reply map[string]any) error {
	errorValue := dictionary(reply["CoreDevice.error"])
	if errorValue == nil {
		return fmt.Errorf("%s: unexpected reply: %v", feature, reply)
	}
	result := &Error{
		Feature:     feature,
		Domain:      stringValue(errorValue["domain"]),
		Description: stringValue(dictionary(errorValue["userInfo"])["NSLocalizedDescription"]),
	}
	switch code := errorValue["code"].(type) {
	case int64:
		result.Code = code
	case uint64:
		result.Code = int64(code)
	}
	return result
}
