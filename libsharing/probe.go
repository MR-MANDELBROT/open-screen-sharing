package libsharing

import (
	"context"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
	"github.com/sagernet/sing/common"
)

type ServerProbe struct {
	IsMac bool

	SupportsAccount bool

	AccountIsUnverified bool

	SupportsRequestControl bool
	SupportsRequestObserve bool

	SupportsVNCPassword bool

	SupportsNoPassword bool

	GrantsRequests bool

	SecurityTypes string

	ErrorCode    int32
	ErrorMessage string
}

func ProbeServer(host string, port int32, timeoutSeconds float64) *ServerProbe {
	timeout := time.Duration(timeoutSeconds * float64(time.Second))
	info, err := rfb.Probe(context.Background(), rfb.Options{
		Address:     net.JoinHostPort(host, strconv.Itoa(int(port))),
		DialTimeout: timeout,
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
	numbers := common.Map(offered, func(securityType rfb.SecurityType) string {
		return strconv.Itoa(int(securityType))
	})
	return &ServerProbe{
		IsMac:                  info.Dialect == rfb.DialectApple,
		SupportsAccount:        accountTypes,
		AccountIsUnverified:    accountTypes && !slices.Contains(offered, rfb.SecurityAppleSRP),
		SupportsRequestControl: slices.Contains(offered, rfb.SecurityAskControl),
		SupportsRequestObserve: slices.Contains(offered, rfb.SecurityAskObserve),
		SupportsVNCPassword:    slices.Contains(offered, rfb.SecurityVNCPassword),
		SupportsNoPassword:     slices.Contains(offered, rfb.SecurityNone),
		GrantsRequests:         rfb.GrantsAsking(offered),
		SecurityTypes:          strings.Join(numbers, ","),
	}
}

type LoginCheck struct {
	SupportsHighPerformance bool

	ErrorCode    int32
	ErrorMessage string
}

func CheckLogin(options *SessionOptions, timeoutSeconds float64) *LoginCheck {
	if options == nil {
		return &LoginCheck{ErrorCode: ErrorProtocol, ErrorMessage: "options are required"}
	}
	rfbOptions, err := rfbOptions(options)
	if err != nil {
		code, message := classifyError(err)
		return &LoginCheck{ErrorCode: code, ErrorMessage: message}
	}
	rfbOptions.DialTimeout = time.Duration(timeoutSeconds * float64(time.Second))
	info, err := rfb.CheckLogin(context.Background(), rfbOptions)
	if err != nil {
		code, message := classifyError(err)
		return &LoginCheck{ErrorCode: code, ErrorMessage: message}
	}
	return &LoginCheck{SupportsHighPerformance: newServerInfo(info).SupportsHighPerformance}
}
