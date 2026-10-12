package vpnpeer

import (
	"context"
	"net"
	"net/netip"
	"slices"
	"strings"
	"time"

	E "github.com/sagernet/sing/common/exceptions"
	F "github.com/sagernet/sing/common/format"
)

// LocalDevVPN defaults its peer address to 10.7.0.1/32; StosVPN defaults
// tunnelFakeIp to 10.7.0.1 (LocalDevVPN af3fd69,
// LocalDevVPN/Constants.swift:16; StosVPN 010bc14,
// TunnelProv/PacketTunnelProvider.swift:12).
var fixedAddress = netip.AddrFrom4([4]byte{10, 7, 0, 1})

type tunnel struct {
	name         string
	gateways     []netip.Addr
	destinations []netip.Addr
	peers        []netip.Addr
}

// SideStore minimuxer takes utun interfaces that have IPv4 but no IPv6
// addresses, sorted by name, then tries route gateways, route destinations
// and point-to-point peers, in that order (minimuxer 12be70d,
// Sources/Services/DeviceConnectionManager.swift:206-214, 260-283).
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
		return netip.Addr{}, E.New("vpnpeer: no VPN address to try")
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
	return netip.Addr{}, E.Cause(E.Errors(errs...), "none of the VPN addresses ", strings.Join(F.MapToString(candidates), ", "), " answers on port ", port)
}
