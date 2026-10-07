//go:build !darwin

package rfb

import "net"

func tcpConnectionInfo(*net.TCPConn) (ConnectionInfo, bool) {
	return ConnectionInfo{}, false
}
