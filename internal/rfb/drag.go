package rfb

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/url"
	"strings"
)

const (
	TypeFileURL   = "public.file-url"
	TypeDragImage = "com.apple.remotedesktop.dragimage-png"

	// ScreenSharing 776.27 (macOS 27.0.1, arm64e UUID EC0B2C20-F816-3FCE-822D-F9403C09EB50)
	// refuses to send a drag above 7 MiB compressed in _RFBPostDropEventCore at 0x1cc142f6c,
	// and closes the connection on a server drag whose sizes pass 0x63fffff at
	// 0x1cc061be0..0x1cc061bf4.
	maximumDragSent     = 7 << 20
	maximumDragReceived = 0x63fffff
	maximumDropPath     = 1023
)

var errDragUnsupported = errors.New("the Mac does not take dragged items")

type RemoteDragItem struct {
	Path   string
	Folder bool
}

type RemoteDrag struct {
	Items     []RemoteDragItem
	Image     []byte
	Clipboard *Clipboard
}

// NewFileClipboard describes files for SendDrag. SSDragHelper (arm64e UUID
// E020362A-B6FF-3C2D-AE9F-8A83196BD555) turns only flavors tagged with OSType 'furl' into
// file promises, in ScrapDataFilter at 0x1000063d4; it never reads the files, so the paths
// only name them. A path ending in a slash names a folder.
func NewFileClipboard(paths []string) *Clipboard {
	clipboard := &Clipboard{}
	for _, path := range paths {
		location := (&url.URL{Scheme: "file", Path: path}).String()
		clipboard.Items = append(clipboard.Items, ClipboardItem{Flavors: []ClipboardFlavor{{
			Type: TypeFileURL,
			Tags: []ClipboardTag{{Key: "com.apple.ostype", Value: "furl"}},
			Data: []byte(location),
		}}})
	}
	return clipboard
}

func (c *Client) checkDrag(messageType uint8) error {
	if c.info.Dialect != DialectApple || !c.info.Accepts(messageType) {
		return errDragUnsupported
	}
	if c.Mode() == ModeObserve {
		return errObserving
	}
	return nil
}

// The Mac's drag follows pointer events with the left button held and drops where the
// button is released.
func (c *Client) SendDrag(clipboard *Clipboard) (uint32, error) {
	err := c.checkDrag(0x20)
	if err != nil {
		return 0, err
	}
	archive := buildPasteboardArchive(clipboard)
	compressed, err := compressPasteboard(archive)
	if err != nil {
		return 0, err
	}
	if len(compressed) > maximumDragSent {
		return 0, fmt.Errorf("drag of %d bytes is larger than the Mac takes", len(compressed))
	}
	id := newDragID()
	return id, c.send(append(dragMessage(id, uint32(len(archive)), uint32(len(compressed))), compressed...))
}

func (c *Client) CancelDrag(id uint32) error {
	err := c.checkDrag(0x20)
	if err != nil {
		return err
	}
	return c.send(dragMessage(id, 0, 0))
}

// ScreensharingAgent's CancelDrag ends a drag of the Mac's own whatever the ID, by
// activating Finder and posting Escape (0x10001b3bc, 0x100022890).
func (c *Client) CancelRemoteDrag() error {
	return c.CancelDrag(newDragID())
}

// A Mac with no drag in progress doesn't answer.
func (c *Client) RequestDrag() error {
	err := c.checkDrag(0x0e)
	if err != nil {
		return err
	}
	message := []byte{0x0e, 0, 0, 0}
	return c.send(binary.BigEndian.AppendUint32(message, newDragID()))
}

func newDragID() uint32 {
	return rand.Uint32N(0x7fffffff) + 1
}

func dragMessage(id, size, compressedSize uint32) []byte {
	message := []byte{0x20, 0, 0, 0}
	message = binary.BigEndian.AppendUint32(message, id)
	message = binary.BigEndian.AppendUint32(message, size)
	return binary.BigEndian.AppendUint32(message, compressedSize)
}

// ScreensharingAgent (arm64e UUID DA1CD9BA-97A1-3758-B425-00FA34B428A3) writes the drag as
// 20 00 00 00 | A | uncompressed size | compressed size, in ScrapConnectionProcessPasteboardTransmission
// at 0x10000819c..0x1000081d8, with A always 1, and no data once the drag ends on the Mac;
// ScreenSharing ignores A = 0 in _HandleServerMessage at 0x1cc061b28.
func (c *Client) receiveDrag() error {
	header, err := c.stream.bytes(15)
	if err != nil {
		return err
	}
	id := binary.BigEndian.Uint32(header[3:])
	size := binary.BigEndian.Uint32(header[7:])
	compressedSize := binary.BigEndian.Uint32(header[11:])
	if size > maximumDragReceived || compressedSize > maximumDragReceived {
		return fmt.Errorf("drag of %d bytes, %d compressed", size, compressedSize)
	}
	compressed, err := c.stream.bytes(int(compressedSize))
	if err != nil {
		return err
	}
	if id == 0 {
		return nil
	}
	if compressedSize == 0 {
		c.handler.RemoteDragChanged(nil)
		return nil
	}
	archive, err := inflatePasteboard(compressed, size)
	if err != nil {
		return fmt.Errorf("drag: %w", err)
	}
	clipboard, err := parsePasteboardArchive(archive)
	if err != nil {
		c.logger.Warn("unreadable drag", "error", err)
		return nil
	}
	drag := &RemoteDrag{Clipboard: clipboard}
	for _, item := range clipboard.Items {
		for _, flavor := range item.Flavors {
			switch flavor.Type {
			case TypeDragImage:
				drag.Image = flavor.Data
			case TypeFileURL:
				location, parseErr := url.Parse(string(flavor.Data))
				if parseErr != nil || location.Scheme != "file" || location.Path == "" {
					continue
				}
				path := location.Path
				folder := len(path) > 1 && strings.HasSuffix(path, "/")
				if folder {
					path = path[:len(path)-1]
				}
				drag.Items = append(drag.Items, RemoteDragItem{Path: path, Folder: folder})
			}
		}
	}
	c.handler.RemoteDragChanged(drag)
	return nil
}

// ScreensharingAgent writes the folder a drag from SendDrag landed on as
// 1e 00 00 00 | A | path length | path, without a terminator, in ScrapConnectionProcessFileTransfer
// at 0x100007ec0..0x100007eec, A being the ID SendDrag sent; ScreenSharing's viewer treats a path
// longer than 1023 bytes as an error.
func (c *Client) receiveDropDestination() error {
	header, err := c.stream.bytes(11)
	if err != nil {
		return err
	}
	length := binary.BigEndian.Uint32(header[7:])
	if length > maximumDropPath {
		return fmt.Errorf("drop path of %d bytes", length)
	}
	path, err := c.stream.bytes(int(length))
	if err != nil {
		return err
	}
	c.handler.DropDestinationReceived(binary.BigEndian.Uint32(header[3:]), string(path))
	return nil
}
