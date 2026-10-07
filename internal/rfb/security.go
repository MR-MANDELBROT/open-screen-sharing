package rfb

import (
	"crypto/des"
	"errors"
	"fmt"
	"math/bits"
	"slices"
	"strconv"
	"strings"
)

type SecurityType uint8

const (
	SecurityNone        SecurityType = 1
	SecurityVNCPassword SecurityType = 2
	SecurityAppleDH     SecurityType = 30
	SecurityAskObserve  SecurityType = 31
	SecurityAskControl  SecurityType = 32
	SecurityAppleRSA    SecurityType = 33
	SecurityApplePSK    SecurityType = 34
	SecurityKerberos    SecurityType = 35
	SecurityAppleSRP    SecurityType = 36
)

var securityNames = map[SecurityType]string{
	SecurityNone:        "none",
	SecurityVNCPassword: "vnc-password",
	SecurityAppleDH:     "apple-dh",
	SecurityAskObserve:  "ask-observe",
	SecurityAskControl:  "ask-control",
	SecurityAppleRSA:    "apple-rsa-srp",
	SecurityApplePSK:    "apple-psk",
	SecurityKerberos:    "kerberos",
	SecurityAppleSRP:    "apple-srp",
}

func (t SecurityType) String() string {
	name, found := securityNames[t]
	if found {
		return name + "(" + strconv.Itoa(int(t)) + ")"
	}
	return "security(" + strconv.Itoa(int(t)) + ")"
}

func ParseSecurityType(value string) (SecurityType, error) {
	number, err := strconv.Atoi(value)
	if err == nil && number > 0 && number < 256 {
		return SecurityType(number), nil
	}
	for securityType, name := range securityNames {
		if strings.EqualFold(name, value) {
			return securityType, nil
		}
	}
	return 0, fmt.Errorf("unknown security type %q", value)
}

var (
	SecurityAccount        = []SecurityType{SecurityAppleRSA, SecurityAppleSRP, SecurityAppleDH}
	SecurityAskPermission  = []SecurityType{SecurityAskControl, SecurityAskObserve}
	SecurityObserveRequest = []SecurityType{SecurityAskObserve, SecurityAskControl}
)

type authenticator func(h *handshake) ([]byte, error)

var authenticators = map[SecurityType]authenticator{
	SecurityNone:        authenticateNone,
	SecurityVNCPassword: authenticateVNCPassword,
	SecurityAppleDH:     authenticateAppleDH,
	SecurityAskObserve:  authenticateAppleDH,
	SecurityAskControl:  authenticateAppleDH,
	SecurityAppleRSA:    authenticateAppleRSA,
	SecurityAppleSRP:    authenticateAppleSRP,
}

var ErrUnsupportedServer = errors.New("unsupported server")

var ErrUnverifiedServer = errors.New("the server offers only account logins that reveal the password")

func chooseSecurityType(offered []SecurityType, preferred []SecurityType, allowUnverified bool) (SecurityType, error) {
	var unverified bool
	for _, candidate := range preferred {
		_, implemented := authenticators[candidate]
		if !implemented || !slices.Contains(offered, candidate) {
			continue
		}
		if !allowUnverified && revealsPassword(candidate, offered) {
			unverified = true
			continue
		}
		return candidate, nil
	}
	if unverified {
		return 0, fmt.Errorf("%w: it offers security types %v", ErrUnverifiedServer, offered)
	}
	return 0, fmt.Errorf("%w: it offers security types %v, none of %v", ErrUnsupportedServer, offered, preferred)
}

func revealsPassword(securityType SecurityType, offered []SecurityType) bool {
	switch securityType {
	case SecurityAppleDH:
		return true
	case SecurityAppleRSA:
		return !slices.Contains(offered, SecurityAppleSRP)
	default:
		return false
	}
}

func authenticateNone(*handshake) ([]byte, error) {
	return nil, nil
}

func authenticateVNCPassword(h *handshake) ([]byte, error) {
	challenge, err := h.stream.bytes(16)
	if err != nil {
		return nil, err
	}
	var key [8]byte
	copy(key[:], h.options.Password)
	for i := range key {
		key[i] = bits.Reverse8(key[i])
	}
	block, err := des.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	response := make([]byte, 16)
	block.Encrypt(response[:8], challenge[:8])
	block.Encrypt(response[8:], challenge[8:])
	_, err = h.conn.Write(response)
	return nil, err
}

var errLongCredentials = errors.New("user name and password must be shorter than 64 bytes")

func credentialBlock(username, password string) ([]byte, error) {
	// ScreenSharing's type-33 credential block randomizes 128 bytes, then copies
	// the username and password into separate 64-byte slots before encryption.
	// Verified in macOS 27.0.1 (26A434), ScreenSharing UUID
	// EC0B2C20-F816-3FCE-822D-F9403C09EB50, _RFBAuthenticateCore,
	// 0x1cc13faf4-0x1cc13fb44.
	if len(username) > 63 || len(password) > 63 {
		return nil, errLongCredentials
	}
	if strings.IndexByte(username, 0) >= 0 || strings.IndexByte(password, 0) >= 0 {
		return nil, errors.New("user name or password contains NUL")
	}
	block := randomBytes(128)
	copy(block, username)
	block[len(username)] = 0
	copy(block[64:], password)
	block[64+len(password)] = 0
	return block, nil
}
