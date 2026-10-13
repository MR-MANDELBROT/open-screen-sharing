//go:build !darwin

package vpnpeer

import "net/netip"

func scan() ([]tunnel, []netip.Addr) {
	return nil, nil
}

func onLocalNetwork(address netip.Addr) bool {
	return false
}
