package media

import (
	"context"
	"net"
	"net/netip"
	"syscall"
)

// ScreensharingAgent writes the same UDP port into both endpoint sockaddrs
// at 0x100008f50–0x100008f58: macOS 27.0.1 (26A434), arm64e UUID
// DA1CD9BA-97A1-3758-B425-00FA34B428A3; macOS 27.2 (26B5101f), arm64e UUID
// D8791A89-6EDB-3A0A-A720-EC3F5FB1AD4F.
func dialLeg(local netip.Addr, remote netip.AddrPort, receiveBuffer int) (*net.UDPConn, error) {
	dialer := net.Dialer{
		LocalAddr: net.UDPAddrFromAddrPort(netip.AddrPortFrom(local, remote.Port())),
		Control: func(network string, address string, raw syscall.RawConn) error {
			var optionErr error
			err := raw.Control(func(fd uintptr) {
				optionErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
				if optionErr == nil {
					optionErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEPORT, 1)
				}
			})
			if err != nil {
				return err
			}
			return optionErr
		},
	}
	conn, err := dialer.DialContext(context.Background(), "udp", remote.String())
	if err != nil {
		return nil, err
	}
	udpConn := conn.(*net.UDPConn)
	_ = udpConn.SetReadBuffer(receiveBuffer)
	return udpConn, nil
}
