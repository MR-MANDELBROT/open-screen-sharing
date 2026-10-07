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
	conn, isTCP := c.conn.(*net.TCPConn)
	if !isTCP {
		return ConnectionInfo{}, false
	}
	return tcpConnectionInfo(conn)
}
