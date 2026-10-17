package rfb

import (
	"net"
	"time"
)

type ConnectionInfo struct {
	RoundTrip     time.Duration
	ReceivedBytes uint64
}

func (c *Client) ConnectionInfo() (ConnectionInfo, bool) {
	return tcpConnectionInfo(c.conn.(*net.TCPConn))
}
