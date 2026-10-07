package libsharing

import (
	"fmt"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
)

const (
	GesturePhaseBegan     int32 = 1
	GesturePhaseChanged   int32 = 2
	GesturePhaseEnded     int32 = 4
	GesturePhaseCancelled int32 = 8

	GesturePhaseMayBegin int32 = 0x80
)

const (
	SwipeBack    int32 = 0
	SwipeForward int32 = 1
	SwipeUp      int32 = 2
	SwipeDown    int32 = 3
)

const (
	SystemCommandMissionControl int32 = 1
	SystemCommandAppWindows     int32 = 2
	SystemCommandShowDesktop    int32 = 3
	SystemCommandLaunchpad      int32 = 4
)

const (
	AssistPointerHighlight int32 = 1
	AssistPointerArrow     int32 = 2
)

var systemCommands = map[int32]rfb.SystemCommand{
	SystemCommandMissionControl: rfb.SystemCommandMissionControl,
	SystemCommandAppWindows:     rfb.SystemCommandAppWindows,
	SystemCommandShowDesktop:    rfb.SystemCommandShowDesktop,
	SystemCommandLaunchpad:      rfb.SystemCommandLaunchpad,
}

func gesturePhase(phase int32) (rfb.GesturePhase, error) {
	switch phase {
	case GesturePhaseBegan, GesturePhaseChanged, GesturePhaseEnded, GesturePhaseCancelled, GesturePhaseMayBegin:
		return rfb.GesturePhase(phase), nil
	default:
		return 0, fmt.Errorf("gesture phase %d", phase)
	}
}

func (s *Session) SendMagnify(x int32, y int32, magnification float64, phase int32) error {
	client, err := s.connected()
	if err != nil {
		return err
	}
	gesture, err := gesturePhase(phase)
	if err != nil {
		return err
	}
	return client.SendMagnify(int(x), int(y), magnification, gesture)
}

func (s *Session) SendRotate(x int32, y int32, degrees float64, phase int32) error {
	client, err := s.connected()
	if err != nil {
		return err
	}
	gesture, err := gesturePhase(phase)
	if err != nil {
		return err
	}
	return client.SendRotate(int(x), int(y), degrees, gesture)
}

func (s *Session) SendSmartMagnify(x int32, y int32) error {
	client, err := s.connected()
	if err != nil {
		return err
	}
	return client.SendSmartMagnify(int(x), int(y))
}

func (s *Session) SendLookup(x int32, y int32) error {
	client, err := s.connected()
	if err != nil {
		return err
	}
	return client.SendLookup(int(x), int(y))
}

func (s *Session) SendSwipe(x int32, y int32, direction int32) error {
	client, err := s.connected()
	if err != nil {
		return err
	}
	if direction < SwipeBack || direction > SwipeDown {
		return fmt.Errorf("swipe direction %d", direction)
	}
	return client.SendSwipe(int(x), int(y), rfb.SwipeDirection(direction))
}

func (s *Session) SendScrollPhase(x int32, y int32, deltaX float64, deltaY float64, phase int32) error {
	client, err := s.connected()
	if err != nil {
		return err
	}
	gesture, err := gesturePhase(phase)
	if err != nil {
		return err
	}
	return client.SendScrollPhase(int(x), int(y), deltaX, deltaY, gesture)
}

func (s *Session) StartScrollMomentum(x int32, y int32, velocityX float64, velocityY float64) error {
	client, err := s.connected()
	if err != nil {
		return err
	}
	return client.StartScrollMomentum(int(x), int(y), velocityX, velocityY)
}

func (s *Session) StopScrollMomentum() {
	client, err := s.connected()
	if err == nil {
		client.StopScrollMomentum()
	}
}

func (s *Session) SendSystemCommand(command int32) error {
	client, err := s.connected()
	if err != nil {
		return err
	}
	systemCommand, found := systemCommands[command]
	if !found {
		return fmt.Errorf("system command %d", command)
	}
	return client.SendSystemCommand(systemCommand)
}

func (s *Session) SetAssistCursor(x int32, y int32, pointer int32, pressed bool) error {
	client, err := s.connected()
	if err != nil {
		return err
	}
	return client.SetAssistCursor(int(x), int(y), uint32(pointer), pressed)
}

func (s *Session) HideAssistCursor() error {
	client, err := s.connected()
	if err != nil {
		return err
	}
	return client.HideAssistCursor()
}

func (i *ServerInfo) setSystemCommands(commands []rfb.SystemCommand) {
	for _, command := range commands {
		switch command {
		case rfb.SystemCommandMissionControl:
			i.SupportsMissionControl = true
		case rfb.SystemCommandAppWindows:
			i.SupportsAppWindows = true
		case rfb.SystemCommandShowDesktop:
			i.SupportsShowDesktop = true
		case rfb.SystemCommandLaunchpad:
			i.SupportsLaunchpad = true
		}
	}
}

func (e *sessionEvents) SystemCommandsChanged(commands []rfb.SystemCommand) {
	client, err := e.session.connected()
	if err != nil {
		return
	}
	info := newServerInfo(client.Info())
	info.setSystemCommands(commands)
	e.session.handler.OnServerInfo(info)
}
