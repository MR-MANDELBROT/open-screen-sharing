package media

import (
	"net"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// XNU’s private msghdr_x appends size_t msg_datalen after the msghdr fields
// (xnu ac9718f, bsd/sys/socket_private.h:405-414, bsd/sys/socket.h:603-611).
type msghdrX struct {
	msghdr  unix.Msghdr
	datalen uint64
}

type msgXReader struct {
	rawRead  func(f func(fd uintptr) bool) error
	headers  []msghdrX
	iovecs   []unix.Iovec
	buffers  [][]byte
	count    int
	errno    syscall.Errno
	readFunc func(fd uintptr) bool
}

func newBatchReader(conn *net.UDPConn, size int) batchReader {
	rawRead := newRawRead(conn)
	if rawRead == nil {
		raw, err := conn.SyscallConn()
		if err != nil {
			return nil
		}
		rawRead = raw.Read
	}
	r := &msgXReader{
		rawRead: rawRead,
		headers: make([]msghdrX, size),
		iovecs:  make([]unix.Iovec, size),
	}
	r.readFunc = r.readFD
	return r
}

func (r *msgXReader) read(buffers [][]byte, datagrams [][]byte) ([][]byte, error) {
	r.buffers = buffers[:min(len(buffers), len(r.headers))]
	r.count, r.errno = 0, 0
	err := r.rawRead(r.readFunc)
	if err != nil {
		return datagrams, err
	}
	if r.errno != 0 {
		return datagrams, os.NewSyscallError("recvmsg_x", r.errno)
	}
	for i := range r.count {
		datagrams = append(datagrams, r.buffers[i][:r.headers[i].datalen])
	}
	return datagrams, nil
}

func (r *msgXReader) readFD(fd uintptr) bool {
	for i, buffer := range r.buffers {
		r.iovecs[i] = unix.Iovec{Base: &buffer[0]}
		r.iovecs[i].SetLen(len(buffer))
		r.headers[i] = msghdrX{}
		r.headers[i].msghdr.Iov = &r.iovecs[i]
		r.headers[i].msghdr.SetIovlen(1)
	}
	for {
		//nolint:staticcheck
		n, _, errno := unix.Syscall6(unix.SYS_RECVMSG_X, fd, uintptr(unsafe.Pointer(&r.headers[0])), uintptr(len(r.buffers)), 0, 0, 0)
		switch errno {
		case syscall.EINTR:
			continue
		case syscall.EAGAIN:
			return false
		}
		r.count, r.errno = int(n), errno
		return true
	}
}
