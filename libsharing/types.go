package libsharing

import (
	"bytes"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
	"github.com/sagernet/sing/common"
)

const (
	LoginAccount int32 = 0

	LoginRequestControl int32 = 1

	LoginRequestObserve int32 = 2

	LoginVNCPassword int32 = 3

	LoginNone int32 = 4
)

const (
	QualityAdaptive int32 = 0
	QualityFull     int32 = 1
	QualityHigh     int32 = 2
	QualityMedium   int32 = 3
	QualityLow      int32 = 4
)

const (
	ModeObserve          int32 = 0
	ModeControl          int32 = 1
	ModeExclusiveControl int32 = 2
)

const (
	DialectAutomatic int32 = 0
	DialectApple     int32 = 1
	DialectStandard  int32 = 2
)

const (
	KeyboardANSI = int32(rfb.KeyboardANSI)
	KeyboardISO  = int32(rfb.KeyboardISO)
	KeyboardJIS  = int32(rfb.KeyboardJIS)
)

const (
	StateIdle               int32 = 0
	StateConnecting         int32 = 1
	StateAuthenticating     int32 = 2
	StateWaitingForApproval int32 = 3
	StateChoosingSession    int32 = 4
	StateConnected          int32 = 5
	StateDisconnected       int32 = 6
)

const (
	ErrorNone              int32 = 0
	ErrorAuthentication    int32 = 1
	ErrorPermissionDenied  int32 = 2
	ErrorNetwork           int32 = 3
	ErrorProtocol          int32 = 4
	ErrorCancelled         int32 = 5
	ErrorUnsupportedServer int32 = 6

	ErrorUnverifiedServer int32 = 7

	ErrorSessionEnded int32 = 8

	ErrorHostNotFound int32 = 9

	ErrorOwnScreen int32 = 10
)

const (
	ButtonLeft   int32 = 1
	ButtonMiddle int32 = 2
	ButtonRight  int32 = 4
)

const (
	PromptChooseSession int32 = 1
)

const (
	ClipboardProblemUnreadable  = int32(rfb.ClipboardUnreadable)
	ClipboardProblemNotRestored = int32(rfb.ClipboardNotRestored)
)

const (
	SessionChoiceCancel       int32 = -1
	SessionChoiceShareConsole int32 = 0
	SessionChoiceOwnSession   int32 = 2
)

const (
	StatusSessionEnding      int32 = 1
	StatusDisplaysSleep      int32 = 5
	StatusDisplaysWake       int32 = 6
	StatusConnectionPaused   int32 = 7
	StatusConnectionResumed  int32 = 8
	StatusControlAllowed     int32 = 9
	StatusObserveOnly        int32 = 10
	StatusPointerHidden      int32 = 11
	StatusPointerShown       int32 = 12
	StatusBusyCursorStart    int32 = 13
	StatusBusyCursorEnd      int32 = 14
	StatusUserSessionChanged int32 = 17
)

const (
	AccessibilityIncreaseContrast          int32 = 3
	AccessibilityReduceMotion              int32 = 4
	AccessibilityDifferentiateWithoutColor int32 = 5
	AccessibilityReduceTransparency        int32 = 6
	AccessibilityShowBorders               int32 = 8
)

const AllDisplays int64 = 0

type SessionOptions struct {
	Host     string
	Port     int32
	Username string
	Password string

	LoginType int32

	Quality int32

	Mode int32

	SharedClipboard bool

	DisplayID int64

	ClientName string

	Dialect int32

	AllowUnverifiedLogin bool

	DecoderMaxPixelRate int64

	MaximumFrameRate int32

	UpdateRate int32
}

func NewSessionOptions() *SessionOptions {
	return &SessionOptions{
		Port:            5900,
		LoginType:       LoginAccount,
		Quality:         QualityAdaptive,
		Mode:            ModeControl,
		SharedClipboard: true,
		DisplayID:       AllDisplays,
		Dialect:         DialectAutomatic,
	}
}

type ServerInfo struct {
	Width  int32
	Height int32

	IsMac bool

	ObserveOnly bool

	CaptureNotPermitted      bool
	SupportsDisplaySelection bool
	SupportsScaling          bool
	SupportsVirtualDisplay   bool
	SupportsHighPerformance  bool
	SupportsClipboard        bool
	SupportsKeyboardSource   bool

	SupportsKeyCodes    bool
	SupportsScrollWheel bool

	SupportsFileTransfer bool

	SupportsCurtain        bool
	MaximumVirtualDisplays int32

	SupportsGestures bool

	SupportsAssistCursor bool

	SupportsMissionControl bool
	SupportsAppWindows     bool
	SupportsShowDesktop    bool
	SupportsLaunchpad      bool

	SupportsAccessibilitySettings bool
	info                          rfb.ServerInfo
}

func newServerInfo(info rfb.ServerInfo) *ServerInfo {
	apple := info.Dialect == rfb.DialectApple
	return &ServerInfo{
		Width:                         int32(info.Width),
		Height:                        int32(info.Height),
		IsMac:                         apple,
		ObserveOnly:                   info.Flags&rfb.ServerFlagObserveOnly != 0,
		CaptureNotPermitted:           info.Flags&rfb.ServerFlagCaptureNotPermitted != 0,
		SupportsDisplaySelection:      info.Accepts(0x0d),
		SupportsScaling:               info.Accepts(0x08),
		SupportsVirtualDisplay:        info.Accepts(0x1d),
		SupportsHighPerformance:       info.Accepts(0x1c) && info.Accepts(0x1d),
		SupportsClipboard:             !apple || info.Accepts(0x15),
		SupportsKeyboardSource:        info.Accepts(0x1a),
		SupportsKeyCodes:              info.Accepts(0x10),
		SupportsScrollWheel:           info.Accepts(0x17),
		SupportsFileTransfer:          info.Accepts(0x22) && info.Flags&rfb.ServerFlagObserveOnly == 0,
		SupportsCurtain:               info.Accepts(0x0c),
		MaximumVirtualDisplays:        int32(info.MaximumVirtualDisplays()),
		SupportsGestures:              info.Accepts(0x17),
		SupportsAssistCursor:          info.Accepts(0x16),
		SupportsAccessibilitySettings: info.Accepts(36),
		info:                          info,
	}
}

type Display struct {
	DisplayID int64

	Density float64

	Scale float64

	LogicalX      int32
	LogicalY      int32
	LogicalWidth  int32
	LogicalHeight int32

	BackingX      int32
	BackingY      int32
	BackingWidth  int32
	BackingHeight int32
	IsMain        bool
	IsMirrored    bool
	IsVirtual     bool
}

type DisplayList struct {
	Current           int64
	FramebufferWidth  int32
	FramebufferHeight int32
	DesktopWidth      int32
	DesktopHeight     int32

	SessionState int64
	displays     []*Display
}

func (d *DisplayList) Len() int32 {
	return int32(len(d.displays))
}

func (d *DisplayList) Get(index int32) *Display {
	if index < 0 || int(index) >= len(d.displays) {
		return nil
	}
	return d.displays[index]
}

func newDisplayList(layout *rfb.DisplayLayout) *DisplayList {
	list := &DisplayList{
		Current:           int64(layout.Current),
		FramebufferWidth:  int32(layout.FramebufferWidth),
		FramebufferHeight: int32(layout.FramebufferHeight),
		DesktopWidth:      int32(layout.DesktopWidth),
		DesktopHeight:     int32(layout.DesktopHeight),
		SessionState:      int64(layout.SessionState),
	}
	if layout.Current == rfb.AllDisplays {
		list.Current = AllDisplays
	}
	for _, display := range layout.Displays {
		list.displays = append(list.displays, &Display{
			DisplayID:     int64(display.ID),
			Density:       display.Density,
			Scale:         display.Scale,
			LogicalX:      int32(display.Logical.X),
			LogicalY:      int32(display.Logical.Y),
			LogicalWidth:  int32(display.Logical.Width),
			LogicalHeight: int32(display.Logical.Height),
			BackingX:      int32(display.Backing.X),
			BackingY:      int32(display.Backing.Y),
			BackingWidth:  int32(display.Backing.Width),
			BackingHeight: int32(display.Backing.Height),
			IsMain:        display.Flags&rfb.DisplayFlagMain != 0,
			IsMirrored:    display.Flags&rfb.DisplayFlagMirrored != 0,
			IsVirtual:     display.Flags&rfb.DisplayFlagVirtual != 0,
		})
	}
	return list
}

type CursorImage struct {
	Width    int32
	Height   int32
	HotspotX int32
	HotspotY int32
	Pixels   []byte
}

type Clipboard struct {
	items []*ClipboardItem
}

func NewClipboard() *Clipboard {
	return &Clipboard{}
}

func (c *Clipboard) Len() int32 {
	return int32(len(c.items))
}

func (c *Clipboard) Get(index int32) *ClipboardItem {
	if index < 0 || int(index) >= len(c.items) {
		return nil
	}
	return c.items[index]
}

func (c *Clipboard) AddItem(item *ClipboardItem) {
	c.items = append(c.items, item)
}

func (c *Clipboard) Text() string {
	text, _ := c.toRFB().Text()
	return text
}

func (c *Clipboard) HasText() bool {
	_, found := c.toRFB().Text()
	return found
}

type ClipboardItem struct {
	flavors []*ClipboardFlavor
}

func NewClipboardItem() *ClipboardItem {
	return &ClipboardItem{}
}

func (i *ClipboardItem) Len() int32 {
	return int32(len(i.flavors))
}

func (i *ClipboardItem) Get(index int32) *ClipboardFlavor {
	if index < 0 || int(index) >= len(i.flavors) {
		return nil
	}
	return i.flavors[index]
}

// gomobile v0.1.13 borrows NSMutableData bytes for a method argument;
// for immutable NSData it allocates a copy and frees it after the call
// (bind/genobjc.go; bind/objc/seq_darwin.m.support).
func (i *ClipboardItem) AddFlavor(typeIdentifier string, data []byte) {
	i.flavors = append(i.flavors, &ClipboardFlavor{Type: typeIdentifier, Data: bytes.Clone(data)})
}

type ClipboardFlavor struct {
	Type string
	Data []byte

	Promise bool
}

func newClipboard(clipboard *rfb.Clipboard) *Clipboard {
	return &Clipboard{items: common.Map(clipboard.Items, func(item rfb.ClipboardItem) *ClipboardItem {
		return &ClipboardItem{flavors: common.Map(item.Flavors, func(flavor rfb.ClipboardFlavor) *ClipboardFlavor {
			return &ClipboardFlavor{Type: flavor.Type, Data: flavor.Data, Promise: flavor.Promise}
		})}
	})}
}

func (c *Clipboard) toRFB() *rfb.Clipboard {
	return &rfb.Clipboard{Items: common.Map(c.items, func(item *ClipboardItem) rfb.ClipboardItem {
		return rfb.ClipboardItem{Flavors: common.Map(item.flavors, func(flavor *ClipboardFlavor) rfb.ClipboardFlavor {
			return rfb.ClipboardFlavor{Type: flavor.Type, Data: flavor.Data, Promise: flavor.Promise}
		})}
	})}
}

type Prompt struct {
	PromptID int64

	Kind int32

	ConsoleUser        string
	AllowsShareConsole bool
	AllowsOwnSession   bool
}
