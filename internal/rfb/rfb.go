package rfb

import (
	"fmt"
	"log/slog"
	"strconv"
	"time"

	E "github.com/sagernet/sing/common/exceptions"
)

type Dialect int

const (
	DialectAutomatic Dialect = iota
	DialectApple
	DialectStandard
)

func (d Dialect) String() string {
	switch d {
	case DialectAutomatic:
		return "automatic"
	case DialectApple:
		return "apple"
	case DialectStandard:
		return "standard"
	default:
		return fmt.Sprint("dialect(", int(d), ")")
	}
}

type Quality int

const (
	QualityAdaptive Quality = iota
	QualityFull
	QualityHigh
	QualityMedium
	QualityLow
)

func (q Quality) String() string {
	switch q {
	case QualityAdaptive:
		return "adaptive"
	case QualityFull:
		return "full"
	case QualityHigh:
		return "high"
	case QualityMedium:
		return "medium"
	case QualityLow:
		return "low"
	default:
		return fmt.Sprint("quality(", int(q), ")")
	}
}

func ParseQuality(name string) (Quality, error) {
	for quality := QualityAdaptive; quality <= QualityLow; quality++ {
		if quality.String() == name {
			return quality, nil
		}
	}
	return 0, E.New("rfb: unknown quality ", strconv.Quote(name))
}

type Mode int

const (
	ModeObserve Mode = iota
	ModeControl
	ModeExclusiveControl
)

func (m Mode) String() string {
	switch m {
	case ModeObserve:
		return "observe"
	case ModeControl:
		return "control"
	case ModeExclusiveControl:
		return "exclusive"
	default:
		return fmt.Sprint("mode(", int(m), ")")
	}
}

type Options struct {
	Address  string
	Username string
	Password string

	Dialect              Dialect
	MaximumVersion       ProtocolVersion
	SecurityTypes        []SecurityType
	ClientName           string
	AllowUnverifiedLogin bool

	Quality             Quality
	Mode                Mode
	Clipboard           bool
	DisplayID           uint32
	DecoderMaxPixelRate int64
	MaximumFrameRate    int

	AutomaticUpdates bool
	UpdateInterval   time.Duration
	Encodings        []Encoding

	DialTimeout time.Duration
	Logger      *slog.Logger

	RectangleObserver func(RawRectangle)

	Prepare func(*Client) error
}

type RawRectangle struct {
	Sequence uint64
	Rectangle
	Payload []byte
}

type Rectangle struct {
	X        int
	Y        int
	Width    int
	Height   int
	Encoding Encoding
}

func (r Rectangle) Empty() bool {
	return r.Width <= 0 || r.Height <= 0
}

func (r Rectangle) Union(other Rectangle) Rectangle {
	if r.Empty() {
		return other
	}
	if other.Empty() {
		return r
	}
	left := min(r.X, other.X)
	top := min(r.Y, other.Y)
	right := max(r.X+r.Width, other.X+other.Width)
	bottom := max(r.Y+r.Height, other.Y+other.Height)
	return Rectangle{X: left, Y: top, Width: right - left, Height: bottom - top}
}

func (r Rectangle) String() string {
	return fmt.Sprint(r.Width, "x", r.Height, "+", r.X, "+", r.Y)
}
