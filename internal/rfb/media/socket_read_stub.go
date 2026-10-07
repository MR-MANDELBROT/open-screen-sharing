//go:build darwin && !badlinkname

package media

import "net"

func newRawRead(conn *net.UDPConn) func(f func(fd uintptr) bool) error {
	return nil
}
