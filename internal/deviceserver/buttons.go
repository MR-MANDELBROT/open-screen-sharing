package deviceserver

import (
	"context"
	"sync"
	"time"

	"github.com/nekohasekai/open-screen-sharing/internal/coredevice"
	"github.com/nekohasekai/open-screen-sharing/internal/coredevice/hid"
	E "github.com/sagernet/sing/common/exceptions"
)

// dtuhidd drops the events it has not dispatched yet once their connection
// ends, however it ends (observed on iPhone18,2, iOS 27.2 24B5110a).
const buttonLinger = time.Second

type buttonPresser struct {
	access     sync.Mutex
	device     *coredevice.Device
	client     *hid.Client
	generation uint64
	lastPress  time.Time
}

func (s *Server) AdjustBrightness(steps int) error {
	button := hid.ButtonBrightnessUp
	if steps < 0 {
		button, steps = hid.ButtonBrightnessDown, -steps
	}
	return s.pressButton(button, steps)
}

func (s *Server) LockScreen() error {
	button := hid.ButtonLock
	button.Hold = 0
	return s.pressButton(button, 1)
}

// Home wakes an already-locked display without toggling an ongoing video wake
// back off, as a second power-button event would.
func (s *Server) WakeScreen() error {
	return s.pressButton(hid.ButtonHome, 1)
}

func (s *Server) pressButton(button hid.Button, count int) error {
	a := s.currentDevice()
	if a == nil {
		return E.New("the device is not connected")
	}
	ctx, cancel := context.WithTimeout(a.ctx, inputTimeout)
	defer cancel()
	b := &s.buttons
	b.access.Lock()
	defer b.access.Unlock()
	if b.client != nil && b.device != a.device {
		b.closeLocked()
	}
	if b.client == nil {
		b.client = hid.New(a.device, s.logger.With("component", "buttons"))
		b.device = a.device
	}
	var err error
	for range count {
		if err = b.client.PressButton(ctx, button); err != nil {
			break
		}
	}
	b.lastPress = time.Now()
	b.generation++
	generation := b.generation
	time.AfterFunc(buttonLinger, func() {
		b.access.Lock()
		defer b.access.Unlock()
		if b.client != nil && b.generation == generation {
			b.closeLocked()
		}
	})
	return err
}

func (b *buttonPresser) close() {
	b.access.Lock()
	defer b.access.Unlock()
	if b.client == nil {
		return
	}
	time.Sleep(time.Until(b.lastPress.Add(buttonLinger)))
	b.closeLocked()
}

func (b *buttonPresser) closeLocked() {
	b.client.Close()
	b.client, b.device = nil, nil
	b.generation++
}
