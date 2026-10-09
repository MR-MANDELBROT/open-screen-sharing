package libsharing

import (
	"context"
	"net"
	"net/netip"
	"os"
	"runtime"
	"slices"
	"sync"

	"howett.net/plist"
)

const macScreenSharingPort = 5900

type ownScreenError struct {
	address netip.Addr
}

func (e *ownScreenError) Error() string {
	return e.address.String() + " is an address of this device"
}

var servingDevices = struct {
	access sync.Mutex
	ports  map[*DeviceServer]int32
}{ports: make(map[*DeviceServer]int32)}

func servesOwnScreen(port int32) bool {
	if runtime.GOOS == "darwin" && port == macScreenSharingPort {
		return macScreenSharingEnabled()
	}
	servingDevices.access.Lock()
	defer servingDevices.access.Unlock()
	for _, served := range servingDevices.ports {
		if served == port {
			return true
		}
	}
	return false
}

// launchd keeps the enabled state of system daemons in this world-readable override file;
// com.apple.screensharing defaults to disabled and is listed as false once Screen Sharing
// is turned on.
func macScreenSharingEnabled() bool {
	content, err := os.ReadFile("/private/var/db/com.apple.xpc.launchd/disabled.plist")
	if err != nil {
		return true
	}
	var disabled map[string]bool
	_, err = plist.Unmarshal(content, &disabled)
	if err != nil {
		return true
	}
	value, overridden := disabled["com.apple.screensharing"]
	return overridden && !value
}

func checkOwnScreen(ctx context.Context, host string, port int32) error {
	if !servesOwnScreen(port) {
		return nil
	}
	targets, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return err
	}
	locals, _ := net.InterfaceAddrs()
	for _, target := range targets {
		target = target.WithZone("").Unmap()
		if target.IsLoopback() || target.IsUnspecified() || slices.ContainsFunc(locals, func(local net.Addr) bool {
			prefix, isPrefix := local.(*net.IPNet)
			return isPrefix && prefix.IP.Equal(target.AsSlice())
		}) {
			return &ownScreenError{address: target}
		}
	}
	return nil
}
