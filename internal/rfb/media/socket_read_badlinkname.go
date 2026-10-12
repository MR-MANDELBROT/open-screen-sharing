//go:build darwin && badlinkname

package media

import (
	"net"
	"reflect"
	"runtime"
	"unsafe"
)

// net.rawConn.Read wraps internal/poll.FD.RawRead errors in OpError
// (Go 1.26.8, src/net/rawconn.go:44-48).
func newRawRead(conn *net.UDPConn) func(f func(fd uintptr) bool) error {
	fd := reflect.ValueOf(conn).Elem().FieldByName("fd")
	if !fd.IsValid() || fd.Kind() != reflect.Pointer || fd.IsNil() {
		return nil
	}
	pfd := fd.Elem().FieldByName("pfd")
	if !pfd.IsValid() || pfd.Type().PkgPath() != "internal/poll" || pfd.Type().Name() != "FD" {
		return nil
	}
	pointer := unsafe.Pointer(pfd.UnsafeAddr())
	return func(f func(fd uintptr) bool) error {
		err := pollFDRawRead(pointer, f)
		runtime.KeepAlive(conn)
		return err
	}
}

//go:linkname pollFDRawRead internal/poll.(*FD).RawRead
func pollFDRawRead(fd unsafe.Pointer, f func(fd uintptr) bool) error
