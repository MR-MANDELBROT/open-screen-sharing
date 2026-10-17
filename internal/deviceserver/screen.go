package deviceserver

import (
	"github.com/nekohasekai/open-screen-sharing/internal/coredevice/display"
	"github.com/nekohasekai/open-screen-sharing/internal/coredevice/hid"
	"github.com/sagernet/sing/common"
)

type displayGeometry struct {
	Width, Height             int
	NativeWidth, NativeHeight int
	Orientation               string
}

func geometryOf(info display.DisplayInfo) displayGeometry {
	return displayGeometry{
		Width:        info.Width,
		Height:       info.Height,
		NativeWidth:  info.NativeWidth,
		NativeHeight: info.NativeHeight,
		Orientation:  info.CurrentOrientation,
	}
}

type screenGeometry struct {
	Width, Height  int
	Scale, Density float64
	Turns          int
}

func (g screenGeometry) points(distance float64) float64 {
	return distance * g.Scale
}

func (g screenGeometry) viewerPoints(distance float64) float64 {
	return distance * g.Density
}

func primaryDisplay(displays []display.DisplayInfo) (display.DisplayInfo, bool) {
	info := common.Find(displays, func(info display.DisplayInfo) bool {
		return info.Primary && info.Width > 0 && info.Height > 0
	})
	if info.Width == 0 {
		info = common.Find(displays, func(info display.DisplayInfo) bool {
			return info.ID == display.DefaultDisplayID && info.Width > 0 && info.Height > 0
		})
	}
	return info, info.Width > 0
}

func orientationTurns(orientation string) int {
	switch orientation {
	case "rot90":
		return 1
	case "rot180":
		return 2
	case "rot270":
		return 3
	}
	return 0
}

func touchTurns(geometry displayGeometry, width, height int) int {
	turns := orientationTurns(geometry.Orientation)
	if geometry.NativeWidth > 0 && geometry.NativeHeight > 0 && width > 0 && height > 0 &&
		geometry.NativeWidth != geometry.NativeHeight && width != height {
		sideways := (width > height) != (geometry.NativeWidth > geometry.NativeHeight)
		switch {
		case sideways && turns%2 == 0:
			turns = 1
		case !sideways && turns%2 == 1:
			turns = 0
		}
	}
	if geometry.NativeWidth > geometry.NativeHeight {
		turns += 3
	}
	return turns & 3
}

func touchPoint(geometry screenGeometry, at point) hid.Touch {
	u := normalize(at.X, geometry.Width)
	v := normalize(at.Y, geometry.Height)
	for range geometry.Turns & 3 {
		u, v = v, 1-u
	}
	return hid.Touch{X: u, Y: v}
}

func touchEdge(geometry screenGeometry, at point) hid.Edge {
	region := geometry.points(edgeRegion)
	distances := [4]float64{at.Y, float64(geometry.Width-1) - at.X, float64(geometry.Height-1) - at.Y, at.X}
	nearest := -1
	for side, distance := range distances {
		if distance <= region && (nearest < 0 || distance < distances[nearest]) {
			nearest = side
		}
	}
	if nearest < 0 {
		return hid.EdgeNone
	}
	edges := [4]hid.Edge{hid.EdgeTop, hid.EdgeRight, hid.EdgeBottom, hid.EdgeLeft}
	return edges[(nearest-geometry.Turns)&3]
}

func normalize(position float64, size int) float64 {
	return min(max(position/float64(size-1), 0), 1)
}
