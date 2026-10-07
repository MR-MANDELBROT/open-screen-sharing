package rfb

import (
	"net"
	"time"

	"golang.org/x/sys/unix"
)

func tcpConnectionInfo(conn *net.TCPConn) (ConnectionInfo, bool) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return ConnectionInfo{}, false
	}
	var info *unix.TCPConnectionInfo
	controlErr := raw.Control(func(fd uintptr) {
		info, err = unix.GetsockoptTCPConnectionInfo(int(fd), unix.IPPROTO_TCP, unix.TCP_CONNECTION_INFO)
	})
	if controlErr != nil || err != nil {
		return ConnectionInfo{}, false
	}
	return ConnectionInfo{RoundTrip: time.Duration(info.Srtt) * time.Millisecond, ReceivedBytes: info.Rxbytes}, true
}
