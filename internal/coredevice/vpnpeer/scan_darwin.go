package vpnpeer

import (
	"net"
	"net/netip"
	"strings"
	"syscall"

	"github.com/sagernet/sing/common"
	"golang.org/x/net/route"
)

func scan() ([]tunnel, []netip.Addr) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, nil
	}
	var local []netip.Addr
	var tunnels []tunnel
	indexes := map[int]int{}
	pointToPoint := map[int]bool{}
	for _, iface := range interfaces {
		addresses, err := iface.Addrs()
		if err != nil {
			continue
		}
		var hasIPv4, hasIPv6 bool
		for _, address := range addresses {
			network, isNetwork := address.(*net.IPNet)
			if !isNetwork {
				continue
			}
			ip, valid := netip.AddrFromSlice(network.IP)
			if !valid {
				continue
			}
			ip = ip.Unmap()
			local = append(local, ip)
			if ip.Is4() {
				hasIPv4 = true
			} else {
				hasIPv6 = true
			}
		}
		if iface.Flags&net.FlagUp == 0 || !strings.HasPrefix(iface.Name, "utun") || !hasIPv4 || hasIPv6 {
			continue
		}
		indexes[iface.Index] = len(tunnels)
		pointToPoint[iface.Index] = iface.Flags&net.FlagPointToPoint != 0
		tunnels = append(tunnels, tunnel{name: iface.Name})
	}
	if len(tunnels) == 0 {
		return nil, local
	}
	for _, message := range readRIB(route.RIBTypeRoute) {
		routeMessage, isRoute := message.(*route.RouteMessage)
		if !isRoute {
			continue
		}
		index, found := indexes[routeMessage.Index]
		if !found {
			continue
		}
		if address, ok := ipv4(routeMessage.Addrs, syscall.RTAX_GATEWAY); ok {
			tunnels[index].gateways = append(tunnels[index].gateways, address)
		}
		if address, ok := ipv4(routeMessage.Addrs, syscall.RTAX_DST); ok {
			tunnels[index].destinations = append(tunnels[index].destinations, address)
		}
	}
	for _, message := range readRIB(route.RIBTypeInterface) {
		addressMessage, isAddress := message.(*route.InterfaceAddrMessage)
		if !isAddress || !pointToPoint[addressMessage.Index] {
			continue
		}
		index, found := indexes[addressMessage.Index]
		if !found {
			continue
		}
		if address, ok := ipv4(addressMessage.Addrs, syscall.RTAX_BRD); ok {
			tunnels[index].peers = append(tunnels[index].peers, address)
		}
	}
	common.SortBy(tunnels, func(it tunnel) string { return it.name })
	return tunnels, local
}

func onLocalNetwork(address netip.Addr) bool {
	interfaces, err := net.Interfaces()
	if err != nil {
		return false
	}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagBroadcast == 0 {
			continue
		}
		addresses, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, ifaceAddress := range addresses {
			network, isNetwork := ifaceAddress.(*net.IPNet)
			if !isNetwork {
				continue
			}
			ip, valid := netip.AddrFromSlice(network.IP)
			if valid && ip.Unmap() == address {
				return true
			}
		}
	}
	return false
}

func readRIB(kind route.RIBType) []route.Message {
	rib, err := route.FetchRIB(syscall.AF_UNSPEC, kind, 0)
	if err != nil {
		return nil
	}
	messages, err := route.ParseRIB(kind, rib)
	if err != nil {
		return nil
	}
	return messages
}

func ipv4(addresses []route.Addr, slot int) (netip.Addr, bool) {
	if slot >= len(addresses) {
		return netip.Addr{}, false
	}
	address, isIPv4 := addresses[slot].(*route.Inet4Addr)
	if !isIPv4 {
		return netip.Addr{}, false
	}
	return netip.AddrFrom4(address.IP), true
}
