package libsharing

import (
	"context"
	"errors"
	"net"
	"strconv"
	"time"

	"github.com/nekohasekai/open-screen-sharing/internal/rdp"
	E "github.com/sagernet/sing/common/exceptions"
)

const (
	defaultRFBPort = 5900
	defaultRDPPort = 3389

	rfbGreetingWait   = time.Second
	rfbPreferenceWait = time.Second
)

func ProbeProtocol(host string, port int32, timeoutSeconds float64) *ServerProbe {
	timeout := time.Duration(timeoutSeconds * float64(time.Second))
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if port != 0 {
		return probeProtocol(ctx, host, port, func() {})
	}
	rfbResults, rdpResults := make(chan *ServerProbe, 1), make(chan *ServerProbe, 1)
	rfbGreeted := make(chan struct{})
	go func() { rfbResults <- probeProtocol(ctx, host, defaultRFBPort, func() { close(rfbGreeted) }) }()
	go func() { rdpResults <- probeRDP(ctx, host, defaultRDPPort) }()
	var rfbResult, rdpResult *ServerProbe
	select {
	case rfbResult = <-rfbResults:
		if rfbResult.ErrorCode == ErrorNone {
			return rfbResult
		}
		rdpResult = <-rdpResults
	case rdpResult = <-rdpResults:
		if rdpResult.ErrorCode != ErrorNone {
			rfbResult = <-rfbResults
			break
		}
		select {
		case rfbResult = <-rfbResults:
		case <-rfbGreeted:
			rfbResult = <-rfbResults
		case <-time.After(rfbPreferenceWait):
			return rdpResult
		}
		if rfbResult.ErrorCode == ErrorNone {
			return rfbResult
		}
		return rdpResult
	}
	if rdpResult.ErrorCode == ErrorNone {
		return rdpResult
	}
	for _, code := range []int32{ErrorOwnScreen, ErrorHostNotFound} {
		if rfbResult.ErrorCode == code {
			return rfbResult
		}
		if rdpResult.ErrorCode == code {
			return rdpResult
		}
	}
	if rfbResult.ErrorCode == ErrorNetwork && rdpResult.ErrorCode != ErrorNetwork {
		return rdpResult
	}
	return rfbResult
}

func probeProtocol(ctx context.Context, host string, port int32, greeted func()) *ServerProbe {
	err := checkOwnScreen(ctx, host, port)
	if err != nil {
		return failedProbe(port, err)
	}
	conn, err := new(net.Dialer).DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(int(port))))
	if err != nil {
		return failedProbe(port, err)
	}
	defer conn.Close()
	err = conn.SetReadDeadline(time.Now().Add(rfbGreetingWait))
	if err != nil {
		return failedProbe(port, err)
	}
	_, err = conn.Read(make([]byte, 1))
	if err == nil {
		greeted()
		_ = conn.Close()
		return probeServer(ctx, host, port)
	}
	if !E.IsTimeout(err) {
		return failedProbe(port, err)
	}
	err = conn.SetReadDeadline(time.Time{})
	if err != nil {
		return failedProbe(port, err)
	}
	fingerprint, err := rdp.Probe(ctx, conn)
	if errors.Is(err, rdp.ErrNotRDP) {
		_ = conn.Close()
		return probeServer(ctx, host, port)
	}
	if err != nil {
		return failedProbe(port, err)
	}
	return &ServerProbe{Protocol: ProtocolRDP, Port: port, Fingerprint: fingerprint, SupportsAccount: true}
}

func ProbeRDPServer(host string, port int32, timeoutSeconds float64) *ServerProbe {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSeconds*float64(time.Second)))
	defer cancel()
	return probeRDP(ctx, host, port)
}

func probeRDP(ctx context.Context, host string, port int32) *ServerProbe {
	conn, err := new(net.Dialer).DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(int(port))))
	if err != nil {
		return failedProbe(port, err)
	}
	defer conn.Close()
	fingerprint, err := rdp.Probe(ctx, conn)
	if err != nil {
		return failedProbe(port, err)
	}
	return &ServerProbe{Protocol: ProtocolRDP, Port: port, Fingerprint: fingerprint, SupportsAccount: true}
}

func failedProbe(port int32, err error) *ServerProbe {
	code, message := classifyError(err)
	return &ServerProbe{Protocol: -1, Port: port, ErrorCode: code, ErrorMessage: message}
}
