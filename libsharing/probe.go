package libsharing

import (
	"context"
	"net"
	"slices"
	"strconv"
	"time"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
)

type ServerProbe struct {
	Protocol int32
	Port     int32

	Fingerprint string

	SupportsAccount bool

	SupportsRequestControl bool
	SupportsRequestObserve bool

	SupportsVNCPassword bool

	SupportsNoPassword bool

	GrantsRequests bool

	ErrorCode    int32
	ErrorMessage string
}

func ProbeServer(host string, port int32, timeoutSeconds float64) *ServerProbe {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSeconds*float64(time.Second)))
	defer cancel()
	return probeServer(ctx, host, port)
}

func probeServer(ctx context.Context, host string, port int32) *ServerProbe {
	err := checkOwnScreen(ctx, host, port)
	if err != nil {
		code, message := classifyError(err)
		return &ServerProbe{ErrorCode: code, ErrorMessage: message}
	}
	deadline, _ := ctx.Deadline()
	info, err := rfb.Probe(ctx, rfb.Options{
		Address:     net.JoinHostPort(host, strconv.Itoa(int(port))),
		DialTimeout: time.Until(deadline),
		Logger:      newLogger(),
	})
	if err != nil {
		code, message := classifyError(err)
		return &ServerProbe{ErrorCode: code, ErrorMessage: message}
	}
	offered := info.SecurityTypes
	accountTypes := slices.ContainsFunc(rfb.SecurityAccount, func(securityType rfb.SecurityType) bool {
		return slices.Contains(offered, securityType)
	})
	return &ServerProbe{
		Protocol:               ProtocolRFB,
		Port:                   port,
		SupportsAccount:        accountTypes,
		SupportsRequestControl: slices.Contains(offered, rfb.SecurityAskControl),
		SupportsRequestObserve: slices.Contains(offered, rfb.SecurityAskObserve),
		SupportsVNCPassword:    slices.Contains(offered, rfb.SecurityVNCPassword),
		SupportsNoPassword:     slices.Contains(offered, rfb.SecurityNone),
		GrantsRequests:         rfb.GrantsAsking(offered),
	}
}

type LoginCheck struct {
	SupportsHighPerformance bool

	Fingerprint string

	ErrorCode    int32
	ErrorMessage string
}

func CheckLogin(options *SessionOptions, timeoutSeconds float64) *LoginCheck {
	if options.Protocol == ProtocolRDP {
		fingerprint, err := checkRDPLogin(options, timeoutSeconds)
		code, message := classifyError(err)
		return &LoginCheck{Fingerprint: fingerprint, ErrorCode: code, ErrorMessage: message}
	}
	rfbOptions, err := rfbOptions(options)
	if err != nil {
		code, message := classifyError(err)
		return &LoginCheck{ErrorCode: code, ErrorMessage: message}
	}
	rfbOptions.DialTimeout = time.Duration(timeoutSeconds * float64(time.Second))
	ctx, cancel := context.WithTimeout(context.Background(), rfbOptions.DialTimeout)
	defer cancel()
	err = checkOwnScreen(ctx, options.Host, options.Port)
	if err != nil {
		code, message := classifyError(err)
		return &LoginCheck{ErrorCode: code, ErrorMessage: message}
	}
	info, err := rfb.CheckLogin(context.Background(), rfbOptions)
	if err != nil {
		code, message := classifyError(err)
		return &LoginCheck{ErrorCode: code, ErrorMessage: message}
	}
	return &LoginCheck{SupportsHighPerformance: newServerInfo(info, options.Platform).SupportsHighPerformance}
}
