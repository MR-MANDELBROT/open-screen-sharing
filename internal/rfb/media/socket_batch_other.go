//go:build !darwin

package media

import "net"

func newBatchReader(conn *net.UDPConn, size int) batchReader {
	return nil
}
