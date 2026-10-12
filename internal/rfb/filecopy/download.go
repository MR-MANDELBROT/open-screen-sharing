package filecopy

import (
	"compress/zlib"
	"context"
	"fmt"
	"io"
	"io/fs"
	"strconv"
	"strings"
	"time"

	E "github.com/sagernet/sing/common/exceptions"
)

type Kind int

const (
	File Kind = iota
	Folder
	Link
)

func (k Kind) String() string {
	switch k {
	case File:
		return "file"
	case Folder:
		return "folder"
	case Link:
		return "link"
	default:
		return fmt.Sprint("kind(", int(k), ")")
	}
}

type Item struct {
	Path               string
	Kind               Kind
	Size               int64
	ResourceForkSize   int64
	Mode               fs.FileMode
	Created            time.Time
	Modified           time.Time
	Accessed           time.Time
	Locked             bool
	LinkTarget         string
	ExtendedAttributes map[string][]byte
}

type Receiver interface {
	Item(item Item) (io.WriteCloser, error)
	Finish(err error) error
}

const maximumUncompressedSize = 16 << 20

type received struct {
	command uint16
	body    []byte
}

type download struct {
	transfer *Transfer
	source   string
	receiver Receiver
	messages chan received

	folders           []string
	rootName          string
	itemPath          string
	writer            io.WriteCloser
	dataRemaining     uint64
	resourceRemaining uint64
	inflater          *inflater
	output            []byte
}

func (m *Manager) Download(ctx context.Context, source string, receiver Receiver, onProgress func(Progress)) (*Transfer, error) {
	if !strings.HasPrefix(source, "/") {
		return nil, E.New("filecopy: source ", strconv.Quote(source), " is not an absolute path")
	}
	if receiver == nil {
		return nil, E.New("filecopy: no receiver")
	}
	job := &download{
		source:   source,
		receiver: receiver,
		messages: make(chan received, 32),
		folders:  []string{""},
	}
	transfer := newTransfer(m, FromMac, onProgress)
	transfer.handler = job
	job.transfer = transfer
	err := m.register(transfer)
	if err != nil {
		return nil, err
	}
	m.updatePacing()
	go transfer.notifyProgress()
	go job.run(ctx)
	return transfer, nil
}

func (d *download) handle(command uint16, body []byte) {
	select {
	case d.messages <- received{command: command, body: body}:
	case <-d.transfer.stop:
	}
}

func (d *download) run(ctx context.Context) {
	stop := context.AfterFunc(ctx, func() {
		d.transfer.Cancel()
	})
	err := d.receive()
	stop()
	if d.writer != nil {
		d.writer.Close()
		d.writer = nil
	}
	err = E.Errors(err, d.receiver.Finish(err))
	d.transfer.finish(Result{Name: d.rootName}, err)
}

func (d *download) receive() error {
	conn := d.transfer.manager.conn
	err := conn.SendMessage(startMessage(commandStartSend, d.transfer.session, startSendImmediately, d.source))
	if err != nil {
		return err
	}
	for {
		select {
		case message := <-d.messages:
			var finished bool
			finished, err = d.process(message)
			if finished {
				return err
			}
			if err != nil {
				if d.transfer.halt(err) {
					conn.SendMessage(sessionStateMessage(commandStop, d.transfer.session))
				}
				return err
			}
		case <-d.transfer.stop:
			return d.transfer.stopReason()
		}
	}
}

func (d *download) process(message received) (bool, error) {
	switch message.command {
	case commandItemInfo:
		info, err := parseItemInfo(message.body)
		if err != nil {
			return false, err
		}
		d.transfer.update(func(progress *Progress) {
			progress.Total = int64(info.logical)
		})
	case commandNewItem:
		return false, d.newItem(message.body)
	case commandData:
		data, err := parseData(message.body)
		if err != nil {
			return false, err
		}
		return false, d.write(data)
	case commandCompressedData:
		compressed, err := parseCompressedData(message.body)
		if err != nil {
			return false, err
		}
		limit := min(d.dataRemaining+d.resourceRemaining, maximumUncompressedSize)
		if uint64(compressed.uncompressedSize) > limit {
			return false, E.New("filecopy: compressed data expands to ", compressed.uncompressedSize, " bytes, more than the ", limit, " bytes allowed")
		}
		if d.inflater == nil {
			d.inflater = &inflater{}
		}
		d.output, err = d.inflater.inflate(compressed.data, int(compressed.uncompressedSize), d.output)
		if err != nil {
			return false, err
		}
		return false, d.write(d.output)
	case commandEnd:
		status := parseEnd(message.body)
		if status != 0 {
			return true, &SourceError{Status: status}
		}
		if d.dataRemaining+d.resourceRemaining > 0 {
			return true, E.New("filecopy: ", d.itemPath, " ended ", d.dataRemaining+d.resourceRemaining, " bytes short")
		}
		if d.rootName == "" {
			return true, E.New("filecopy: the Mac sent no item")
		}
		return true, nil
	}
	return false, nil
}

func (d *download) newItem(body []byte) error {
	if d.dataRemaining+d.resourceRemaining > 0 {
		return E.New("filecopy: ", d.itemPath, " is missing ", d.dataRemaining+d.resourceRemaining, " bytes")
	}
	wire, err := parseNewItem(body)
	if err != nil {
		return err
	}
	item := Item{
		Mode:     fileModeFromPOSIX(wire.mode),
		Created:  wire.created,
		Modified: wire.modified,
		Accessed: wire.accessed,
		Locked:   wire.nodeFlags&nodeFlagLocked != 0,
	}
	name := wire.name
	switch wire.itemType {
	case itemTypeFile:
		item.Kind = File
		item.Size = int64(wire.dataSize)
		item.ResourceForkSize = int64(wire.resourceForkSize)
		name = carbonName(name)
	case itemTypeFolder:
		item.Kind = Folder
		name = carbonName(name)
	case itemTypeLink:
		item.Kind = Link
		item.LinkTarget = wire.linkTarget
	default:
		return E.New("filecopy: unknown item type ", wire.itemType)
	}
	if name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
		return E.New("filecopy: invalid item name ", strconv.Quote(name))
	}
	level := int(wire.level)
	if level >= len(d.folders) || (level == 0 && d.rootName != "") {
		return E.New("filecopy: item ", strconv.Quote(name), " at level ", level, " outside its folder")
	}
	item.Path = name
	if level > 0 {
		item.Path = d.folders[level] + "/" + name
	}
	d.folders = d.folders[:level+1]
	if item.Kind == Folder {
		d.folders = append(d.folders, item.Path)
	}
	if level == 0 {
		d.rootName = name
	}
	if len(wire.attributes) > 0 {
		item.ExtendedAttributes = make(map[string][]byte, len(wire.attributes))
		for _, attribute := range wire.attributes {
			item.ExtendedAttributes[attribute.name] = attribute.value
		}
	}
	d.itemPath = item.Path
	var linkSize int64
	if item.Kind == Link {
		linkSize = int64(wire.dataSize)
	}
	d.transfer.update(func(progress *Progress) {
		progress.Item = item.Path
		progress.Transferred += linkSize
		progress.Stored = progress.Transferred
	})
	writer, err := d.receiver.Item(item)
	if err != nil {
		return err
	}
	if item.Kind != File {
		if writer != nil {
			return writer.Close()
		}
		return nil
	}
	d.writer = writer
	d.dataRemaining = wire.dataSize
	d.resourceRemaining = wire.resourceForkSize
	if d.dataRemaining == 0 {
		return d.closeWriter()
	}
	return nil
}

func (d *download) write(data []byte) error {
	length := len(data)
	for len(data) > 0 {
		switch {
		case d.dataRemaining > 0:
			n := min(uint64(len(data)), d.dataRemaining)
			if d.writer != nil {
				_, err := d.writer.Write(data[:n])
				if err != nil {
					return err
				}
			}
			d.dataRemaining -= n
			data = data[n:]
			if d.dataRemaining == 0 {
				err := d.closeWriter()
				if err != nil {
					return err
				}
			}
		case d.resourceRemaining > 0:
			n := min(uint64(len(data)), d.resourceRemaining)
			d.resourceRemaining -= n
			data = data[n:]
		default:
			return E.New("filecopy: ", len(data), " bytes more than ", d.itemPath, " holds")
		}
	}
	d.transfer.update(func(progress *Progress) {
		progress.Transferred += int64(length)
		progress.Stored = progress.Transferred
	})
	return nil
}

func (d *download) closeWriter() error {
	if d.writer == nil {
		return nil
	}
	err := d.writer.Close()
	d.writer = nil
	return err
}

// SSFileCopySender reuses one zlib stream for every command 103: it initializes the stream
// once at level 9 and calls deflate with Z_SYNC_FLUSH (2) (SSFileCopySender 756.36.5.2,
// macOS 27.0.1 26A434, UUID ED0F67AF-49DE-30F6-8611-CFB982CE83CD,
// sub_100003d40 0x100004180..0x1000041c0, 0x100004278..0x10000429c, 0x10000445c..0x100004464,
// 0x1000044bc..0x1000044c0).
type inflater struct {
	input  []byte
	offset int
	reader io.Reader
}

func (f *inflater) Read(p []byte) (int, error) {
	if f.offset == len(f.input) {
		return 0, io.ErrUnexpectedEOF
	}
	n := copy(p, f.input[f.offset:])
	f.offset += n
	return n, nil
}

func (f *inflater) ReadByte() (byte, error) {
	if f.offset == len(f.input) {
		return 0, io.ErrUnexpectedEOF
	}
	f.offset++
	return f.input[f.offset-1], nil
}

func (f *inflater) inflate(compressed []byte, size int, output []byte) ([]byte, error) {
	f.input = append(f.input[:copy(f.input, f.input[f.offset:])], compressed...)
	f.offset = 0
	if f.reader == nil {
		reader, err := zlib.NewReader(f)
		if err != nil {
			return nil, err
		}
		f.reader = reader
	}
	if cap(output) < size {
		output = make([]byte, size)
	}
	output = output[:size]
	_, err := io.ReadFull(f.reader, output)
	if err != nil {
		return nil, E.Cause(err, "filecopy: inflate")
	}
	return output, nil
}
