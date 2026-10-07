package display

import "context"

// pymobiledevice3 1146717 sends getdisplayinfo to deviceinfo with no
// action identifier (remote/core_device/device_info.py:12-27).
const (
	deviceInfoServiceName = "com.apple.coredevice.deviceinfo"
	featureDisplayInfo    = "com.apple.coredevice.feature.getdisplayinfo"
)

type DisplayInfo struct {
	ID            int
	Primary       bool
	Width, Height int
	// devicectl 651.13.2 reported nativeSize=[2420,1668] and
	// nativeOrientation=rot270 for iPad17,1.
	NativeWidth, NativeHeight int
	CurrentOrientation        string
	PointScale                int
}

func (c *Client) Displays(ctx context.Context) ([]DisplayInfo, error) {
	conn, err := c.device.DialXPC(ctx, deviceInfoServiceName)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	output, err := invoke(ctx, conn, c.deviceIdentifier, featureDisplayInfo, "", map[string]any{})
	if err != nil {
		return nil, err
	}
	var displays []DisplayInfo
	for _, value := range array(output["displays"]) {
		display := dictionary(value)
		info := DisplayInfo{
			ID:                 intField(display, "displayId"),
			CurrentOrientation: stringValue(display["currentOrientation"]),
			PointScale:         intField(display, "pointScale"),
		}
		info.Primary, _ = boolValue(display["primary"])
		info.Width, info.Height = sizeValue(dictionary(display["currentMode"])["size"])
		info.NativeWidth, info.NativeHeight = sizeValue(display["nativeSize"])
		displays = append(displays, info)
	}
	return displays, nil
}

func sizeValue(value any) (int, int) {
	size := array(value)
	if len(size) != 2 {
		return 0, 0
	}
	width, _ := floatValue(size[0])
	height, _ := floatValue(size[1])
	return int(width), int(height)
}
