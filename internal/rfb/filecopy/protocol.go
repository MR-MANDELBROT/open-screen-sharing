package filecopy

import (
	"encoding/binary"
	"io/fs"
	"strconv"
	"time"

	E "github.com/sagernet/sing/common/exceptions"
)

const MessageType = 0x22

// ScreenSharing checks the server's size field against 8..0x400000 (ScreenSharing 776.27,
// macOS 27.0.1 26A434, UUID EC0B2C20-F816-3FCE-822D-F9403C09EB50,
// _HandleServerMessage 0x1cc06202c..0x1cc06204c).
const maximumServerMessageSize = 4 << 20

const maximumClientMessageSize = 1 << 20

const (
	headerSize      = 14
	protocolVersion = 1
	chunkSize       = 64 << 10
)

const (
	commandStartSend      = 1
	commandStartReceive   = 2
	commandPause          = 3
	commandContinue       = 4
	commandStop           = 5
	commandItemInfo       = 100
	commandNewItem        = 101
	commandData           = 102
	commandCompressedData = 103
	commandEnd            = 104
	commandReceiverDone   = 200
)

const (
	itemTypeFile   = 1
	itemTypeFolder = 2
	itemTypeLink   = 3
)

const (
	itemInfoBodySize = 52
	newItemBodySize  = 104
)

const startSendImmediately = 1

const itemInfoSingleFile = 1

const nodeFlagDirectory = 0x0010

func appendHeader(message []byte, command uint16, session uint32, bodySize int) []byte {
	message = append(message, MessageType, 0)
	message = binary.BigEndian.AppendUint32(message, uint32(headerSize-6+bodySize))
	message = binary.BigEndian.AppendUint16(message, protocolVersion)
	message = binary.BigEndian.AppendUint16(message, command)
	return binary.BigEndian.AppendUint32(message, session)
}

func sessionStateMessage(command uint16, session uint32) []byte {
	return appendHeader(make([]byte, 0, headerSize), command, session, 0)
}

func startMessage(command uint16, session uint32, flags uint32, path string) []byte {
	bodySize := 10 + len(path) + 1
	message := appendHeader(make([]byte, 0, headerSize+bodySize), command, session, bodySize)
	message = binary.BigEndian.AppendUint32(message, flags)
	message = binary.BigEndian.AppendUint32(message, 0)
	message = binary.BigEndian.AppendUint16(message, uint16(len(path)))
	message = append(message, path...)
	return append(message, 0)
}

type totals struct {
	singleFile bool
	logical    uint64
	physical   uint64
	files      uint64
	folders    uint64
}

func itemInfoMessage(session uint32, info totals) []byte {
	message := appendHeader(make([]byte, 0, headerSize+itemInfoBodySize), commandItemInfo, session, itemInfoBodySize)
	var flags uint32
	if info.singleFile {
		flags = itemInfoSingleFile
	}
	message = binary.BigEndian.AppendUint32(message, flags)
	message = binary.BigEndian.AppendUint32(message, 0)
	message = binary.BigEndian.AppendUint64(message, info.logical)
	message = binary.BigEndian.AppendUint64(message, info.physical)
	message = binary.BigEndian.AppendUint64(message, info.files)
	message = binary.BigEndian.AppendUint64(message, 0)
	message = binary.BigEndian.AppendUint64(message, info.folders)
	return binary.BigEndian.AppendUint32(message, 0)
}

func parseItemInfo(body []byte) (totals, error) {
	if len(body) < itemInfoBodySize {
		return totals{}, E.New("filecopy: item info of ", len(body), " bytes is shorter than ", itemInfoBodySize)
	}
	return totals{
		singleFile: binary.BigEndian.Uint32(body)&itemInfoSingleFile != 0,
		logical:    binary.BigEndian.Uint64(body[8:]),
		physical:   binary.BigEndian.Uint64(body[16:]),
		files:      binary.BigEndian.Uint64(body[24:]),
		folders:    binary.BigEndian.Uint64(body[40:]),
	}, nil
}

type wireItem struct {
	itemType           uint8
	userAccess         uint8
	finderInfo         [16]byte
	extendedFinderInfo [16]byte
	resourceForkSize   uint64
	dataSize           uint64
	created            time.Time
	modified           time.Time
	attributesModified time.Time
	accessed           time.Time
	backedUp           time.Time
	nodeFlags          uint16
	level              uint16
	mode               uint16
	textEncoding       uint32
	name               string
	linkTarget         string
}

func newItemMessage(session uint32, item *wireItem) ([]byte, error) {
	var linkLength uint16
	if item.itemType == itemTypeLink {
		if item.linkTarget == "" {
			return nil, E.New("filecopy: symbolic link ", strconv.Quote(item.name), " has no target")
		}
		// SSFileCopySenderFW stores the full target size in the data-size field and the size
		// minus one in the link-length field (SSFileCopySenderFW 776.27, macOS 27.0.1 26A434,
		// UUID F446D013-CEF6-3119-ABFE-75392B969C92, sub_1000032bc 0x100003448..0x100003450,
		// 0x100003854..0x100003868).
		linkLength = uint16(len(item.linkTarget) - 1)
	}
	bodySize := newItemBodySize + len(item.name) + 1 + len(item.linkTarget)
	if headerSize-6+bodySize > maximumClientMessageSize {
		return nil, E.New("filecopy: item record for ", strconv.Quote(item.name), " is ", headerSize-6+bodySize, " bytes, over the ", maximumClientMessageSize, "-byte limit")
	}
	message := appendHeader(make([]byte, 0, headerSize+bodySize), commandNewItem, session, bodySize)
	message = append(message, item.itemType, item.userAccess)
	message = append(message, item.finderInfo[:]...)
	message = append(message, item.extendedFinderInfo[:]...)
	message = binary.BigEndian.AppendUint64(message, item.resourceForkSize)
	message = binary.BigEndian.AppendUint64(message, item.dataSize)
	for _, date := range []time.Time{item.created, item.modified, item.attributesModified, item.accessed, item.backedUp} {
		message = appendDate(message, date)
	}
	message = binary.BigEndian.AppendUint16(message, item.nodeFlags)
	message = binary.BigEndian.AppendUint16(message, item.level)
	message = binary.BigEndian.AppendUint16(message, item.mode)
	message = binary.BigEndian.AppendUint32(message, item.textEncoding)
	message = binary.BigEndian.AppendUint16(message, uint16(len(item.name)))
	message = binary.BigEndian.AppendUint16(message, linkLength)
	message = append(message, item.name...)
	message = append(message, 0)
	return append(message, item.linkTarget...), nil
}

func parseNewItem(body []byte) (*wireItem, error) {
	if len(body) < newItemBodySize {
		return nil, E.New("filecopy: item record of ", len(body), " bytes is shorter than ", newItemBodySize)
	}
	item := &wireItem{
		itemType:         body[0],
		userAccess:       body[1],
		resourceForkSize: binary.BigEndian.Uint64(body[34:]),
		dataSize:         binary.BigEndian.Uint64(body[42:]),
		nodeFlags:        binary.BigEndian.Uint16(body[90:]),
		level:            binary.BigEndian.Uint16(body[92:]),
		mode:             binary.BigEndian.Uint16(body[94:]),
		textEncoding:     binary.BigEndian.Uint32(body[96:]),
	}
	copy(item.finderInfo[:], body[2:18])
	copy(item.extendedFinderInfo[:], body[18:34])
	dates := []*time.Time{&item.created, &item.modified, &item.attributesModified, &item.accessed, &item.backedUp}
	for i, date := range dates {
		*date = parseDate(body[50+i*8:])
	}
	nameLength := int(binary.BigEndian.Uint16(body[100:]))
	rest := body[newItemBodySize:]
	if nameLength == 0 || nameLength >= len(rest) || rest[nameLength] != 0 {
		return nil, E.New("filecopy: invalid item name of ", nameLength, " bytes in ", len(rest), " remaining bytes")
	}
	item.name = string(rest[:nameLength])
	rest = rest[nameLength+1:]
	if item.itemType == itemTypeLink {
		if item.dataSize == 0 || item.dataSize > uint64(len(rest)) {
			return nil, E.New("filecopy: link ", strconv.Quote(item.name), " has an invalid target of ", item.dataSize, " bytes in ", len(rest), " remaining bytes")
		}
		item.linkTarget = string(rest[:item.dataSize])
		rest = rest[item.dataSize:]
	}
	if len(rest) > 0 {
		err := validateExtendedAttributes(rest)
		if err != nil {
			return nil, err
		}
	}
	return item, nil
}

func dataMessage(session uint32, data []byte) []byte {
	bodySize := 4 + len(data)
	message := appendHeader(make([]byte, 0, headerSize+bodySize), commandData, session, bodySize)
	message = binary.BigEndian.AppendUint32(message, uint32(len(data)))
	return append(message, data...)
}

func parseData(body []byte) ([]byte, error) {
	if len(body) < 4 {
		return nil, E.New("filecopy: data record of ", len(body), " bytes is shorter than 4")
	}
	length := binary.BigEndian.Uint32(body)
	if uint64(length) > uint64(len(body)-4) {
		return nil, E.New("filecopy: data length ", length, " exceeds the ", len(body)-4, " bytes in its record")
	}
	return body[4 : 4+length], nil
}

const compressionZlib = 1

type compressedData struct {
	uncompressedSize uint32
	data             []byte
}

func parseCompressedData(body []byte) (compressedData, error) {
	if len(body) < 10 {
		return compressedData{}, E.New("filecopy: compressed data record of ", len(body), " bytes is shorter than 10")
	}
	compression := binary.BigEndian.Uint16(body)
	if compression != compressionZlib {
		return compressedData{}, E.New("filecopy: unknown compression ", compression)
	}
	length := binary.BigEndian.Uint32(body[6:])
	if uint64(length) > uint64(len(body)-10) {
		return compressedData{}, E.New("filecopy: compressed data length ", length, " exceeds the ", len(body)-10, " bytes in its record")
	}
	return compressedData{uncompressedSize: binary.BigEndian.Uint32(body[2:]), data: body[10 : 10+length]}, nil
}

// SSFileCopySenderFW swaps the message size but stores the command-104 status without
// swapping, in little-endian order (SSFileCopySenderFW 776.27, macOS 27.0.1 26A434,
// UUID F446D013-CEF6-3119-ABFE-75392B969C92, sub_100001140 0x100001788..0x100001794,
// 0x10000197c..0x100001988).
func endMessage(session uint32, status uint32) []byte {
	message := appendHeader(make([]byte, 0, headerSize+4), commandEnd, session, 4)
	return binary.LittleEndian.AppendUint32(message, status)
}

func parseEnd(body []byte) int32 {
	if len(body) < 4 {
		return 0
	}
	return int32(binary.LittleEndian.Uint32(body))
}

func parseReceiverDone(body []byte) (int16, string, error) {
	if len(body) < 4 {
		return 0, "", E.New("filecopy: receiver result of ", len(body), " bytes is shorter than 4")
	}
	status := int16(binary.BigEndian.Uint16(body))
	nameLength := int(binary.BigEndian.Uint16(body[2:]))
	if nameLength > len(body)-4 {
		return 0, "", E.New("filecopy: receiver result name of ", nameLength, " bytes exceeds the ", len(body)-4, " bytes in its record")
	}
	return status, string(body[4 : 4+nameLength]), nil
}

var carbonEpoch = time.Date(1904, time.January, 1, 0, 0, 0, 0, time.UTC)

const carbonEpochUnix = -2082844800

func appendDate(message []byte, date time.Time) []byte {
	if date.IsZero() || date.Before(carbonEpoch) {
		return append(message, 0, 0, 0, 0, 0, 0, 0, 0)
	}
	seconds := uint64(date.Unix() - carbonEpochUnix)
	fraction := uint16(uint64(date.Nanosecond()) << 16 / uint64(time.Second))
	message = binary.BigEndian.AppendUint16(message, uint16(seconds>>32))
	message = binary.BigEndian.AppendUint32(message, uint32(seconds))
	return binary.BigEndian.AppendUint16(message, fraction)
}

func parseDate(data []byte) time.Time {
	seconds := uint64(binary.BigEndian.Uint16(data))<<32 | uint64(binary.BigEndian.Uint32(data[2:]))
	fraction := binary.BigEndian.Uint16(data[6:])
	if seconds == 0 && fraction == 0 {
		return time.Time{}
	}
	nanoseconds := int64(fraction) * int64(time.Second) >> 16
	return time.Unix(int64(seconds)+carbonEpochUnix, nanoseconds)
}

var attributesMagic = [4]byte{'e', 'x', 't', '1'}

func validateExtendedAttributes(block []byte) error {
	if len(block) < 18 || [4]byte(block[:4]) != attributesMagic {
		return nil
	}
	blockLength := int(binary.BigEndian.Uint16(block[8:]))
	if blockLength < 18 || blockLength > len(block) {
		return E.New("filecopy: extended attribute block length ", blockLength, " is not between 18 and ", len(block))
	}
	table := block[10:blockLength]
	count := int(binary.BigEndian.Uint32(table[4:]))
	if count > (len(table)-8)/8 {
		return E.New("filecopy: ", count, " extended attributes do not fit in a table of ", len(table), " bytes")
	}
	data := table[8+8*count:]
	for i := range count {
		nameLength := int(binary.BigEndian.Uint32(table[8+8*i:]))
		valueLength := int(binary.BigEndian.Uint32(table[12+8*i:]))
		if nameLength < 1 || valueLength < 0 || nameLength > len(data) || valueLength > len(data)-nameLength {
			return E.New("filecopy: extended attribute ", i, " with a ", nameLength, "-byte name and a ", valueLength, "-byte value does not fit in ", len(data), " bytes")
		}
		data = data[nameLength+valueLength:]
	}
	return nil
}

func fileModeFromPOSIX(mode uint16) fs.FileMode {
	result := fs.FileMode(mode) & fs.ModePerm
	if mode&0o4000 != 0 {
		result |= fs.ModeSetuid
	}
	if mode&0o2000 != 0 {
		result |= fs.ModeSetgid
	}
	if mode&0o1000 != 0 {
		result |= fs.ModeSticky
	}
	return result
}

func posixMode(mode fs.FileMode, itemType uint8) uint16 {
	result := uint16(mode & fs.ModePerm)
	if mode&fs.ModeSticky != 0 {
		result |= 0o1000
	}
	switch itemType {
	case itemTypeFolder:
		result |= 0o040000
	case itemTypeLink:
		result |= 0o120000
	default:
		result |= 0o100000
	}
	return result
}
