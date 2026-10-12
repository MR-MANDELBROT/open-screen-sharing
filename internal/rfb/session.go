package rfb

import (
	"encoding/binary"
	"fmt"
	"strings"

	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
)

type SessionRequest struct {
	ConsoleUser string
	Choices     []SessionChoice
}

type SessionChoice int

const (
	SessionShareConsole SessionChoice = 0
	SessionOwn          SessionChoice = 2
	SessionCancel       SessionChoice = -1
)

func (s SessionChoice) String() string {
	switch s {
	case SessionShareConsole:
		return "share-console"
	case SessionOwn:
		return "own-session"
	case SessionCancel:
		return "cancel"
	default:
		return fmt.Sprint("session-choice(", int(s), ")")
	}
}

var ErrSessionCancelled = E.New("rfb: session selection cancelled")

func (c *Client) selectSession() error {
	// ScreenSharing writes the session choice at byte 6 of the response body;
	// its credential-bearing response has a 200-byte body with two 64-byte
	// credential slots starting at body offset 0x48 (ScreenSharing 776.27,
	// macOS 27.0.1 26A434, UUID EC0B2C20-F816-3FCE-822D-F9403C09EB50,
	// _RFBSelectSession 0x1cc05ead0, 0x1cc05eb04..0x1cc05eb50, 0x1cc05f494).
	prompt, err := c.stream.bytes(4)
	if err != nil {
		return err
	}
	length := int(binary.BigEndian.Uint16(prompt))
	rest, err := c.stream.bytes(length - 2)
	if err != nil {
		return err
	}
	prompt = append(prompt, rest...)
	if len(prompt) < 13 {
		return E.New("rfb: session selection of ", len(prompt), " bytes, want at least 13")
	}
	flags := binary.BigEndian.Uint32(prompt[4:])
	consoleUser, _, _ := strings.Cut(string(prompt[12:]), "\x00")
	request := SessionRequest{
		ConsoleUser: consoleUser,
		Choices: common.Filter([]SessionChoice{SessionShareConsole, SessionOwn}, func(choice SessionChoice) bool {
			return flags&(1<<choice) != 0
		}),
	}
	c.logger.Debug("session selection", "console", consoleUser, "flags", fmt.Sprintf("%#x", flags))
	c.handler.StateChanged(StateChoosingSession)
	choice, err := c.handler.ChooseSession(request)
	if err != nil {
		return err
	}
	var body []byte
	switch choice {
	case SessionShareConsole:
		body = make([]byte, 72)
		binary.BigEndian.PutUint16(body, 2)
		copy(body[8:71], consoleUser)
		c.handler.StateChanged(StateWaitingForApproval)
	case SessionOwn:
		if len(c.options.Username) > 63 {
			return E.New("rfb: user name of ", len(c.options.Username), " bytes is over the 63-byte limit")
		}
		if len(c.options.Password) > 63 {
			return E.New("rfb: password is over the 63-byte limit")
		}
		body = make([]byte, 200)
		binary.BigEndian.PutUint16(body, 2)
		body[6] = 2
		var credentials [128]byte
		copy(credentials[:63], c.options.Username)
		copy(credentials[64:127], c.options.Password)
		err = encryptECB(c.wrapKey[:], credentials[:])
		if err != nil {
			return err
		}
		copy(body[0x48:], credentials[:])
	default:
		return ErrSessionCancelled
	}
	message := binary.BigEndian.AppendUint16(nil, uint16(len(body)))
	_, err = c.conn.Write(append(message, body...))
	return err
}
