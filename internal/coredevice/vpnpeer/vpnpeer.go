package vpnpeer

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"time"
)

// LocalDevVPN defaults its peer address to 10.7.0.1/32; StosVPN defaults
// tunnelFakeIp to 10.7.0.1 (Constants.swift; PacketTunnelProvider.swift).
var fixedAddress = netip.AddrFrom4([4]byte{10, 7, 0, 1})

type tunnel struct {
	name         string
	gateways     []netip.Addr
	destinations []netip.Addr
	peers        []netip.Addr
}

// SideStore minimuxer selects IPv4-only utun interfaces by name, then
// tries route gateways, route destinations and point-to-point peers
// (Sources/Services/DeviceConnectionManager.swift).
func Candidates() []netip.Addr {
	tunnels, local := scan()
	return candidates(tunnels, local)
}

func candidates(tunnels []tunnel, local []netip.Addr) []netip.Addr {
	var result []netip.Addr
	add := func(address netip.Addr) {
		if address.IsUnspecified() || address.IsLoopback() || address.IsMulticast() ||
			address == netip.AddrFrom4([4]byte{255, 255, 255, 255}) ||
			slices.Contains(local, address) || slices.Contains(result, address) {
			return
		}
		result = append(result, address)
	}
	for _, tunnel := range tunnels {
		for _, list := range [][]netip.Addr{tunnel.gateways, tunnel.destinations, tunnel.peers} {
			for _, address := range list {
				add(address)
			}
		}
	}
	add(fixedAddress)
	return result
}

func Find(ctx context.Context, candidates []netip.Addr, port uint16, timeout time.Duration) (netip.Addr, error) {
	if len(candidates) == 0 {
		return netip.Addr{}, errors.New("no VPN address to try")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	type result struct {
		index int
		err   error
	}
	results := make(chan result, len(candidates))
	for index, address := range candidates {
		go func() {
			var dialer net.Dialer
			conn, err := dialer.DialContext(ctx, "tcp", netip.AddrPortFrom(address, port).String())
			if err == nil {
				_ = conn.Close()
			}
			results <- result{index: index, err: err}
		}()
	}
	errs := make([]error, len(candidates))
	for range candidates {
		answer := <-results
		if answer.err == nil {
			return candidates[answer.index], nil
		}
		errs[answer.index] = answer.err
	}
	return netip.Addr{}, fmt.Errorf("none of the VPN addresses %v answers on port %d: %w", candidates, port, errs[0])
}
