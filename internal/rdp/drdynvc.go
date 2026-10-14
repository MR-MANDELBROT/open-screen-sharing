package rdp

import (
	"bytes"
	"encoding/binary"
	"errors"
	"log/slog"
	"sync"

	"github.com/nekohasekai/open-screen-sharing/internal/rdp/codec/zgfx"

	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
)

const (
	dynvcCreate              = 0x01
	dynvcDataFirst           = 0x02
	dynvcData                = 0x03
	dynvcClose               = 0x04
	dynvcCapabilities        = 0x05
	dynvcDataFirstCompressed = 0x06
	dynvcDataCompressed      = 0x07

	dynvcVersion          = 3
	dynvcChunkLength      = 1590
	dynvcMaximumName      = 255
	dynvcCreateOK         = 0
	dynvcCreateNoListener = 0xc0000225
)

type dynamicListener struct {
	name     string
	handler  channelHandler
	required bool
}

type dynamicChannel struct {
	id       uint32
	listener *dynamicListener
	fragment *channelFragment
	zgfx     *zgfx.Decompressor
}

type drdynvc struct {
	logger    *slog.Logger
	listeners []*dynamicListener

	access   sync.Mutex
	send     func([]byte) error
	version  uint16
	channels map[uint32]*dynamicChannel
}

func readDynamicUint(r *reader, size byte) uint32 {
	switch size {
	case 0:
		return uint32(r.u8())
	case 1:
		return uint32(r.u16())
	default:
		return r.u32()
	}
}

func dynamicUintSize(value uint32) byte {
	switch {
	case value <= 0xff:
		return 0
	case value <= 0xffff:
		return 1
	default:
		return 2
	}
}

func appendDynamicUint(dst []byte, value uint32, size byte) []byte {
	switch size {
	case 0:
		return append(dst, byte(value))
	case 1:
		return binary.LittleEndian.AppendUint16(dst, uint16(value))
	default:
		return binary.LittleEndian.AppendUint32(dst, value)
	}
}

func dynamicHeader(command byte, idSize byte, lengthSize byte) byte {
	return command<<4 | lengthSize<<2 | idSize
}

func (d *drdynvc) OnOpen(send func([]byte) error) error {
	d.access.Lock()
	defer d.access.Unlock()
	d.send = send
	d.version = 0
	d.channels = make(map[uint32]*dynamicChannel)
	return nil
}

func (d *drdynvc) OnClose() {
	d.access.Lock()
	channels := d.channels
	d.send, d.channels = nil, nil
	d.access.Unlock()
	for _, channel := range channels {
		channel.listener.handler.OnClose()
	}
}

func (d *drdynvc) OnData(data []byte) error {
	err := d.receive(data)
	if err == nil || errors.Is(err, errRequiredChannel) {
		return err
	}
	d.access.Lock()
	defer d.access.Unlock()
	for _, channel := range d.channels {
		if channel.listener.required {
			return E.Cause1(errRequiredChannel, err)
		}
	}
	return err
}

func (d *drdynvc) receive(data []byte) error {
	r := &reader{data: data}
	header := r.u8()
	command, idSize, lengthSize := header>>4, header&3, header>>2&3
	if r.err != nil {
		return r.err
	}
	if command == dynvcCapabilities {
		r.take(1)
		version := r.u16()
		if r.err != nil {
			return r.err
		}
		if version == 0 {
			return E.New("rdp: dynamic channel capabilities request version 0")
		}
		d.access.Lock()
		d.version = min(version, dynvcVersion)
		response := binary.LittleEndian.AppendUint16([]byte{dynamicHeader(dynvcCapabilities, 0, 0), 0}, d.version)
		d.access.Unlock()
		return d.sendStatic(response)
	}
	id := readDynamicUint(r, idSize)
	if r.err != nil {
		return r.err
	}
	if command == dynvcCreate {
		return d.create(id, idSize, r.rest())
	}
	d.access.Lock()
	channel := d.channels[id]
	d.access.Unlock()
	if channel == nil {
		return nil
	}
	switch command {
	case dynvcDataFirst, dynvcData, dynvcDataFirstCompressed, dynvcDataCompressed:
		err := channel.receive(command, lengthSize, r)
		if err != nil {
			return d.fail(channel, err)
		}
		return nil
	case dynvcClose:
		d.access.Lock()
		delete(d.channels, id)
		d.access.Unlock()
		channel.listener.handler.OnClose()
		return d.sendStatic(appendDynamicUint([]byte{dynamicHeader(dynvcClose, idSize, 0)}, id, idSize))
	default:
		return nil
	}
}

func (d *drdynvc) sendStatic(data []byte) error {
	d.access.Lock()
	send := d.send
	d.access.Unlock()
	if send == nil {
		return E.New("rdp: virtual channel ", staticChannelDynamic, " is closed")
	}
	return send(data)
}

func (c *dynamicChannel) receive(command byte, lengthSize byte, r *reader) error {
	first := command == dynvcDataFirst || command == dynvcDataFirstCompressed
	var total uint32
	if first {
		total = readDynamicUint(r, lengthSize)
		if r.err != nil {
			return r.err
		}
		if total > maxChannelMessage {
			return E.New("rdp: dynamic channel ", c.listener.name, " message of ", total, " bytes exceeds ", maxChannelMessage, " bytes")
		}
	}
	payload := r.rest()
	if command == dynvcDataFirstCompressed || command == dynvcDataCompressed {
		if c.zgfx == nil {
			c.zgfx = zgfx.NewDecompressor()
		}
		var err error
		payload, err = c.zgfx.Decompress(payload)
		if err != nil {
			return E.Cause(err, "rdp: decompress ", c.listener.name, " data")
		}
	}
	if len(payload) > maxChannelMessage {
		return E.New("rdp: dynamic channel ", c.listener.name, " chunk of ", len(payload), " bytes exceeds ", maxChannelMessage, " bytes")
	}
	if first {
		if f := c.fragment; f != nil {
			return E.New("rdp: dynamic channel ", c.listener.name, " message interrupted at ", len(f.data), " of ", f.total, " bytes")
		}
		c.fragment = &channelFragment{total: total}
	}
	if f := c.fragment; f != nil {
		if len(payload) > int(f.total)-len(f.data) {
			return E.New("rdp: dynamic channel ", c.listener.name, " chunk of ", len(payload), " bytes overruns its ", f.total, "-byte message at ", len(f.data))
		}
		f.data = append(f.data, payload...)
		if len(f.data) != int(f.total) {
			return nil
		}
		payload = f.data
		c.fragment = nil
	}
	return c.listener.handler.OnData(payload)
}

func (d *drdynvc) create(id uint32, idSize byte, nameData []byte) error {
	end := bytes.IndexByte(nameData, 0)
	if end < 0 {
		return E.New("rdp: dynamic channel name of ", len(nameData), " bytes has no terminator")
	}
	if end > dynvcMaximumName {
		return E.New("rdp: dynamic channel name of ", end, " bytes exceeds ", dynvcMaximumName, " bytes")
	}
	name := string(nameData[:end])
	listener := common.Find(d.listeners, func(listener *dynamicListener) bool { return listener.name == name })
	d.access.Lock()
	needsCapabilities := d.version == 0
	if needsCapabilities {
		d.version = dynvcVersion
	}
	_, duplicate := d.channels[id]
	for _, active := range d.channels {
		duplicate = duplicate || active.listener == listener
	}
	d.access.Unlock()
	if needsCapabilities {
		err := d.sendStatic(binary.LittleEndian.AppendUint16([]byte{dynamicHeader(dynvcCapabilities, 0, 0), 0}, dynvcVersion))
		if err != nil {
			return err
		}
	}
	status := uint32(dynvcCreateOK)
	if listener == nil || duplicate {
		status = dynvcCreateNoListener
	}
	err := d.sendStatic(binary.LittleEndian.AppendUint32(appendDynamicUint([]byte{dynamicHeader(dynvcCreate, idSize, 0)}, id, idSize), status))
	if err != nil || status != dynvcCreateOK {
		return err
	}
	channel := &dynamicChannel{id: id, listener: listener}
	d.access.Lock()
	if d.channels == nil {
		d.access.Unlock()
		return E.New("rdp: virtual channel ", staticChannelDynamic, " closed while opening ", name)
	}
	d.channels[id] = channel
	d.access.Unlock()
	err = listener.handler.OnOpen(func(payload []byte) error { return d.sendData(channel, payload) })
	if err != nil {
		return d.fail(channel, err)
	}
	return nil
}

func (d *drdynvc) fail(channel *dynamicChannel, err error) error {
	if channel.listener.required {
		return E.Cause1(errRequiredChannel, E.Cause(err, "process ", channel.listener.name))
	}
	d.logger.Warn("close dynamic virtual channel", "channel", channel.listener.name, "error", err)
	d.access.Lock()
	open := d.channels[channel.id] == channel
	if open {
		delete(d.channels, channel.id)
	}
	d.access.Unlock()
	if !open {
		return nil
	}
	channel.listener.handler.OnClose()
	idSize := dynamicUintSize(channel.id)
	return d.sendStatic(appendDynamicUint([]byte{dynamicHeader(dynvcClose, idSize, 0)}, channel.id, idSize))
}

func (d *drdynvc) sendData(channel *dynamicChannel, data []byte) error {
	if len(data) > maxChannelMessage {
		return E.New("rdp: dynamic channel ", channel.listener.name, " message of ", len(data), " bytes exceeds ", maxChannelMessage, " bytes")
	}
	d.access.Lock()
	send := d.send
	open := d.channels[channel.id] == channel
	d.access.Unlock()
	if !open || send == nil {
		return E.New("rdp: dynamic channel ", channel.listener.name, " is closed")
	}
	idSize := dynamicUintSize(channel.id)
	for first := true; first || len(data) > 0; first = false {
		var packet []byte
		if first && len(data) > dynvcChunkLength {
			lengthSize := dynamicUintSize(uint32(len(data)))
			packet = appendDynamicUint([]byte{dynamicHeader(dynvcDataFirst, idSize, lengthSize)}, channel.id, idSize)
			packet = appendDynamicUint(packet, uint32(len(data)), lengthSize)
		} else {
			packet = appendDynamicUint([]byte{dynamicHeader(dynvcData, idSize, 0)}, channel.id, idSize)
		}
		count := min(len(data), dynvcChunkLength)
		err := send(append(packet, data[:count]...))
		if err != nil {
			return err
		}
		data = data[count:]
	}
	return nil
}
