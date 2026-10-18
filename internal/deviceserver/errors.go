package deviceserver

import (
	"context"
	"errors"
	"net"
	"syscall"

	"github.com/nekohasekai/open-screen-sharing/internal/coredevice"
	"github.com/nekohasekai/open-screen-sharing/internal/coredevice/display"
	"github.com/nekohasekai/open-screen-sharing/internal/coredevice/imagemounter"
	"github.com/nekohasekai/open-screen-sharing/internal/coredevice/pairing"
	E "github.com/sagernet/sing/common/exceptions"
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

func newError(problem Problem, message ...any) *Error {
	return &Error{Problem: problem, Err: E.New(message...)}
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
	case display.IsMediaInUse(err):
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
	if E.IsMulti(err,
		syscall.ECONNREFUSED, syscall.ETIMEDOUT, syscall.EHOSTUNREACH,
		syscall.ENETUNREACH, syscall.ENETDOWN, syscall.EHOSTDOWN, syscall.EADDRNOTAVAIL,
	) {
		return true
	}
	operationError, ok := errors.AsType[*net.OpError](err)
	return ok && operationError.Op == "dial"
}
