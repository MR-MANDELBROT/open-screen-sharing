package filecopy

import (
	"encoding/binary"
	"io"
	"maps"
	"net"
	"slices"
	"sync"
	"time"

	E "github.com/sagernet/sing/common/exceptions"
)

var (
	ErrUnsupported = E.New("filecopy: the Mac does not accept file copy")
	ErrObserveOnly = E.New("filecopy: the Mac only allows observing")
	ErrCanceled    = E.New("filecopy: transfer canceled")
)

type Conn interface {
	SendMessage(message []byte) error
}

type DownloadPacer interface {
	SetDownloading(active bool) error
}

type Capabilities struct {
	FileCopy    bool
	ObserveOnly bool
}

type Manager struct {
	conn Conn

	access       sync.Mutex
	capabilities Capabilities
	nextSession  uint32
	transfers    map[uint32]*Transfer
	downloads    int
	closeError   error

	pacingAccess sync.Mutex
	pacing       bool
}

func NewManager(conn Conn) *Manager {
	return &Manager{conn: conn, transfers: make(map[uint32]*Transfer)}
}

func (m *Manager) SetCapabilities(capabilities Capabilities) {
	m.access.Lock()
	m.capabilities = capabilities
	m.access.Unlock()
}

func (m *Manager) ReadMessage(r io.Reader) error {
	message := make([]byte, 6, 64)
	message[0] = MessageType
	_, err := io.ReadFull(r, message[1:6])
	if err != nil {
		return err
	}
	size := binary.BigEndian.Uint32(message[2:])
	if size < headerSize-6 || size > maximumServerMessageSize {
		return E.New("filecopy: server message size ", size, " is not between ", headerSize-6, " and ", maximumServerMessageSize)
	}
	message = slices.Grow(message, int(size))[:6+int(size)]
	_, err = io.ReadFull(r, message[6:])
	if err != nil {
		return err
	}
	parsed, body, err := parseMessage(message)
	if err != nil {
		return err
	}
	m.access.Lock()
	transfer := m.transfers[parsed.session]
	m.access.Unlock()
	if transfer != nil {
		transfer.handler.handle(parsed.command, body)
	}
	return nil
}

func (m *Manager) Close(err error) {
	if err == nil {
		err = net.ErrClosed
	}
	m.access.Lock()
	if m.closeError == nil {
		m.closeError = err
	}
	transfers := slices.Collect(maps.Values(m.transfers))
	m.access.Unlock()
	var stopped []uint32
	for _, transfer := range transfers {
		if transfer.halt(err) {
			stopped = append(stopped, transfer.session)
		}
	}
	if len(stopped) == 0 {
		return
	}
	sent := make(chan struct{})
	go func() {
		for _, session := range stopped {
			m.conn.SendMessage(sessionStateMessage(commandStop, session))
		}
		close(sent)
	}()
	select {
	case <-sent:
	case <-time.After(time.Second):
	}
}

func (m *Manager) register(transfer *Transfer) error {
	m.access.Lock()
	defer m.access.Unlock()
	if m.closeError != nil {
		return m.closeError
	}
	if !m.capabilities.FileCopy {
		return ErrUnsupported
	}
	if m.capabilities.ObserveOnly {
		return ErrObserveOnly
	}
	m.nextSession++
	for m.nextSession == 0 || m.transfers[m.nextSession] != nil {
		m.nextSession++
	}
	transfer.session = m.nextSession
	m.transfers[transfer.session] = transfer
	if transfer.direction == FromMac {
		m.downloads++
	}
	return nil
}

func (m *Manager) unregister(transfer *Transfer) {
	m.access.Lock()
	if m.transfers[transfer.session] == transfer {
		delete(m.transfers, transfer.session)
		if transfer.direction == FromMac {
			m.downloads--
		}
	}
	m.access.Unlock()
	if transfer.direction == FromMac {
		m.updatePacing()
	}
}

func (m *Manager) updatePacing() {
	pacer, found := m.conn.(DownloadPacer)
	if !found {
		return
	}
	m.pacingAccess.Lock()
	defer m.pacingAccess.Unlock()
	m.access.Lock()
	active := m.downloads > 0
	m.access.Unlock()
	if active == m.pacing {
		return
	}
	m.pacing = active
	pacer.SetDownloading(active)
}
