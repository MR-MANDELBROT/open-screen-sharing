package media_test

import (
	"encoding/binary"
	"testing"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb/media"
)

func TestDecoderCapacityFollowsEncodedDimensions(t *testing.T) {
	const pixelRate = 1920 * 1080 * 3 * 60
	offer, err := media.NewOffer(media.OfferOptions{DecoderMaxPixelRate: pixelRate, FrameRate60: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		width, height int
		want60        bool
	}{
		{0, 0, false},
		{1920, 1080, true},
		{1920, 1081, false},
		{3840, 2160, false},
		{1280, 720, true},
		{0, 1080, false},
		{1920, 1080, true},
	} {
		offer.UpdateVideoDimensions(tc.width, tc.height)
		got := binary.BigEndian.Uint32(offer.Message()[6:])&1 != 0
		if got != tc.want60 {
			t.Errorf("%dx%d: 60 FPS = %v, want %v", tc.width, tc.height, got, tc.want60)
		}
	}
}

func TestInvalidOfferOptions(t *testing.T) {
	for _, options := range []media.OfferOptions{
		{DecoderMaxPixelRate: -1},
		{MaximumBitrate: -1},
		{MaximumBitrate: 4_999_999},
		{MaximumBitrate: 60_000_001},
		{TilesPerFrame: 2},
		{TilesPerFrame: 4, VideoMode: media.VideoModeNearby},
		{VideoMode: media.VideoMode(8)},
		{Codecs: []media.Codec{media.Codec(9)}},
	} {
		if _, err := media.NewOffer(options); err == nil {
			t.Errorf("accepted invalid options: %+v", options)
		}
	}
}
