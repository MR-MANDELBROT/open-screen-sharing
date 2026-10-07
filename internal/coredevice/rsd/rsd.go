package rsd

import (
	"context"
	"fmt"
	"strconv"

	"github.com/google/uuid"
	"github.com/nekohasekai/open-screen-sharing/internal/coredevice/xpc"
)

// pymobiledevice3 and go-ios use 58783 as the default RSD port.
const Port = 58783

// idevice sends these MessagingProtocolVersion and RemoteXPCVersionFlags
// values in its RSD handshake.
const (
	messagingProtocolVersion = 7
	remoteXPCVersionFlags    = 0x0100000000000006
)

type Peer struct {
	MessagingProtocolVersion uint64
	Properties               map[string]any
	Services                 map[string]uint16
}

// pymobiledevice3 reuses a hostname-derived UUID across RSD handshakes
// (default_handshake_uuid in remote/remotexpc.py).
func Handshake(ctx context.Context, conn *xpc.Conn, identity uuid.UUID) (*Peer, error) {
	err := conn.Send(map[string]any{
		"MessageType":              "Handshake",
		"MessagingProtocolVersion": uint64(messagingProtocolVersion),
		"UUID":                     identity,
		"Properties": map[string]any{
			"RemoteXPCVersionFlags":      uint64(remoteXPCVersionFlags),
			"SensitivePropertiesVisible": true,
		},
		"Services": map[string]any{},
	}, false)
	if err != nil {
		return nil, fmt.Errorf("RSD handshake: %w", err)
	}
	for {
		message, err := conn.ReceiveMessage(ctx)
		if err != nil {
			return nil, fmt.Errorf("RSD handshake: %w", err)
		}
		if message.Body["MessageType"] != "Handshake" {
			continue
		}
		return parsePeer(message.Body)
	}
}

func parsePeer(body map[string]any) (*Peer, error) {
	peer := &Peer{Services: map[string]uint16{}}
	peer.MessagingProtocolVersion, _ = unsigned(body["MessagingProtocolVersion"])
	peer.Properties, _ = body["Properties"].(map[string]any)
	services, isDictionary := body["Services"].(map[string]any)
	if !isDictionary {
		return nil, fmt.Errorf("RSD handshake: no services in %v", body)
	}
	for name, value := range services {
		entry, _ := value.(map[string]any)
		port, err := parsePort(entry["Port"])
		if err != nil {
			continue
		}
		peer.Services[name] = port
	}
	return peer, nil
}

func parsePort(value any) (uint16, error) {
	switch value := value.(type) {
	case string:
		port, err := strconv.ParseUint(value, 10, 16)
		if err != nil {
			return 0, fmt.Errorf("bad port %q", value)
		}
		return uint16(port), nil
	case int64, uint64:
		port, _ := unsigned(value)
		if port == 0 || port > 65535 {
			return 0, fmt.Errorf("bad port %d", port)
		}
		return uint16(port), nil
	}
	return 0, fmt.Errorf("bad port %v", value)
}

func unsigned(value any) (uint64, bool) {
	switch value := value.(type) {
	case uint64:
		return value, true
	case int64:
		if value >= 0 {
			return uint64(value), true
		}
	}
	return 0, false
}
