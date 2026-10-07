package deviceserver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"syscall"

	"github.com/nekohasekai/open-screen-sharing/internal/coredevice"
	"github.com/nekohasekai/open-screen-sharing/internal/coredevice/display"
	"github.com/nekohasekai/open-screen-sharing/internal/coredevice/imagemounter"
	"github.com/nekohasekai/open-screen-sharing/internal/coredevice/pairing"
)

type Problem int

const (
	ProblemNone Problem = iota
	ProblemNoTunnel
	ProblemPairingRejected
	ProblemDeveloperModeOff
	ProblemNoDiskImage
	ProblemDiskImage
	ProblemListen
	ProblemUnsupported
	ProblemScreenInUse
	ProblemLoopbackAddress Problem = 10
	ProblemOther           Problem = 100
)

func (p Problem) String() string {
	switch p {
	case ProblemNone:
		return "none"
	case ProblemNoTunnel:
		return "no tunnel"
	case ProblemPairingRejected:
		return "pairing rejected"
	case ProblemDeveloperModeOff:
		return "developer mode off"
	case ProblemNoDiskImage:
		return "no disk image"
	case ProblemDiskImage:
		return "disk image"
	case ProblemListen:
		return "listen"
	case ProblemUnsupported:
		return "unsupported"
	case ProblemScreenInUse:
		return "screen in use"
	case ProblemLoopbackAddress:
		return "loopback address"
	}
	return "other"
}

type Error struct {
	Problem Problem
	Err     error
}

func (e *Error) Error() string {
	return e.Err.Error()
}

func (e *Error) Unwrap() error {
	return e.Err
}

func newError(problem Problem, format string, args ...any) *Error {
	return &Error{Problem: problem, Err: fmt.Errorf(format, args...)}
}

func ProblemOf(err error) Problem {
	if err == nil {
		return ProblemNone
	}
	if problemError, ok := errors.AsType[*Error](err); ok {
		return problemError.Problem
	}
	_, rejected := errors.AsType[*pairing.PairingRejectedError](err)
	switch {
	case errors.Is(err, coredevice.ErrLoopbackAddress):
		return ProblemLoopbackAddress
	case errors.Is(err, pairing.ErrPairVerifyFailed), rejected:
		return ProblemPairingRejected
	case errors.Is(err, imagemounter.ErrDeveloperModeOff):
		return ProblemDeveloperModeOff
	case errors.Is(err, display.ErrNoMedia), display.IsMediaInUse(err):
		return ProblemScreenInUse
	case display.IsUnsupported(err):
		return ProblemUnsupported
	case unreachable(err):
		return ProblemNoTunnel
	}
	return ProblemOther
}

func unreachable(err error) bool {
	if errors.Is(err, context.Canceled) {
		return false
	}
	for _, errno := range []syscall.Errno{
		syscall.ECONNREFUSED, syscall.ETIMEDOUT, syscall.EHOSTUNREACH,
		syscall.ENETUNREACH, syscall.ENETDOWN, syscall.EHOSTDOWN, syscall.EADDRNOTAVAIL,
	} {
		if errors.Is(err, errno) {
			return true
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	operationError, ok := errors.AsType[*net.OpError](err)
	return ok && operationError.Op == "dial"
}

func permanent(err error) bool {
	switch ProblemOf(err) {
	case ProblemPairingRejected, ProblemUnsupported, ProblemLoopbackAddress:
		return true
	}
	return false
}
