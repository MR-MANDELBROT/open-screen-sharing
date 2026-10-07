package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
	"github.com/nekohasekai/open-screen-sharing/internal/rfb/filecopy"
	"github.com/nekohasekai/open-screen-sharing/internal/rfb/media"
)

var errSessionEnded = errors.New("session ended")

func runScript(ctx context.Context, client *rfb.Client, h *handler, actions []string, done <-chan struct{}, runErr *error) error {
	wait := func(duration time.Duration) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
			if *runErr != nil {
				return *runErr
			}
			return errSessionEnded
		case <-time.After(duration):
			return nil
		}
	}
	var saved *rfb.Clipboard
	var recorder *mediaRecorder
	for _, action := range actions {
		verb, argument, _ := strings.Cut(action, ":")
		h.logger.Info("action", "action", action)
		var err error
		switch verb {
		case "wait":
			var duration time.Duration
			duration, err = time.ParseDuration(argument)
			if err == nil {
				err = wait(duration)
			}
		case "png":
			err = savePNG(client, argument)
		case "cursor":
			err = saveCursor(h, argument)
		case "move":
			var numbers []int
			numbers, err = parseNumbers(argument, 2, strconv.Atoi)
			if err == nil {
				err = client.SendPointer(numbers[0], numbers[1], 0)
			}
		case "click":
			var numbers []int
			numbers, err = parseNumbers(argument, 2, strconv.Atoi)
			if err == nil {
				buttons := uint8(rfb.ButtonLeft)
				if len(numbers) > 2 {
					buttons = uint8(numbers[2])
				}
				err = client.SendPointer(numbers[0], numbers[1], buttons)
				if err == nil {
					err = client.SendPointer(numbers[0], numbers[1], 0)
				}
			}
		case "scroll":
			var numbers []int
			numbers, err = parseNumbers(argument, 3, strconv.Atoi)
			if err == nil {
				deltaX := 0
				if len(numbers) > 3 {
					deltaX = numbers[3]
				}
				err = client.SendScroll(numbers[0], numbers[1], float64(deltaX), float64(numbers[2]))
			}
		case "key":
			var keysym uint64
			keysym, err = strconv.ParseUint(argument, 0, 32)
			if err == nil {
				err = client.SendKey(uint32(keysym), true)
				if err == nil {
					err = client.SendKey(uint32(keysym), false)
				}
			}
		case "keydown", "keyup":
			var keysym uint64
			keysym, err = strconv.ParseUint(argument, 0, 32)
			if err == nil {
				err = client.SendKey(uint32(keysym), verb == "keydown")
			}
		case "keycode":
			err = sendKeyCode(client, argument)
		case "type":
			err = client.SendText(argument)
		case "mode":
			var mode rfb.Mode
			mode, err = parseMode(argument)
			if err == nil {
				err = client.SetMode(mode)
			}
		case "quality":
			var quality rfb.Quality
			quality, err = rfb.ParseQuality(argument)
			if err == nil {
				err = client.SetQuality(quality)
			}
		case "display":
			var id uint64
			if argument != "all" {
				id, err = strconv.ParseUint(argument, 0, 32)
			}
			if err == nil {
				err = client.SelectDisplay(uint32(id))
			}
		case "scale":
			var factor float64
			factor, err = strconv.ParseFloat(argument, 64)
			if err == nil {
				err = client.SetScale(factor)
			}
		case "virtual":
			err = setVirtualDisplay(client, argument)
		case "hdr":
			err = client.SetHDR(argument == "on")
		case "keysource":
			err = client.SetKeyboardSource(argument)
		case "keysource-once":
			err = client.SelectKeyboardSource(argument)
		case "paste-text":
			err = client.PasteText(argument)
		case "clipboard-set":
			err = client.SetClipboard(rfb.NewTextClipboard(argument))
		case "clipboard-get":
			_, err = fetchClipboard(client, h)
		case "shared-clipboard":
			err = client.SetSharedClipboard(argument == "on")
		case "clipboard-set-flavors":
			err = setClipboardFlavors(client, argument)
		case "clipboard-save":
			saved, err = fetchClipboard(client, h)
		case "clipboard-restore":
			if saved == nil {
				err = errors.New("nothing saved")
			} else {
				err = client.SetClipboard(saved)
			}
		case "hp":
			err = runHighPerformance(client, h, argument, wait)
		case "hp-start":
			parts := strings.Split(argument, ",")
			recorder, err = newMediaRecorder(h.logger, parts[0])
			if err == nil {
				err = client.StartMediaStream(parseMediaOptions(parts[1:]), recorder)
			}
		case "hp-stop":
			logStreamStatistics(h, client)
			err = client.StopMediaStream()
			if recorder != nil {
				recorder.close()
				recorder = nil
			}
		case "upload", "download":
			err = copyFile(ctx, client, h, verb, argument)
		case "raw":
			var message []byte
			message, err = hex.DecodeString(strings.ReplaceAll(argument, " ", ""))
			if err == nil {
				err = client.SendMessage(message)
			}
		case "exec":
			command := exec.CommandContext(ctx, "/bin/sh", "-c", argument)
			command.Stdout, command.Stderr = os.Stderr, os.Stderr
			err = command.Run()
		case "a11y":
			err = setAccessibility(client, argument)
		case "refresh":
			err = client.RequestRefresh()
		case "stats":
			statistics := client.Statistics()
			h.access.Lock()
			updates, pixels := h.updates, h.pixels
			h.access.Unlock()
			h.logger.Info("statistics", "updates", statistics.Updates, "rectangles", statistics.Rectangles,
				"records", statistics.Records, "wire-bytes", statistics.WireBytes, "dirty-updates", updates, "pixels", pixels)
		default:
			var handled bool
			handled, err = runGestureAction(client, verb, argument, wait)
			if !handled {
				err = fmt.Errorf("unknown action %q", action)
			}
		}
		if err != nil {
			return fmt.Errorf("%s: %w", action, err)
		}
	}
	return nil
}

func sendKeyCode(client *rfb.Client, argument string) error {
	parts := strings.Split(argument, ",")
	keyCode, err := strconv.ParseUint(parts[0], 0, 16)
	if err != nil {
		return err
	}
	action := "tap"
	if len(parts) > 1 {
		action = parts[1]
	}
	keyboardType := rfb.KeyboardANSI
	if len(parts) > 2 {
		var value uint64
		value, err = strconv.ParseUint(parts[2], 0, 16)
		if err != nil {
			return err
		}
		keyboardType = rfb.KeyboardType(value)
	}
	var keysym uint64
	if len(parts) > 3 {
		keysym, err = strconv.ParseUint(parts[3], 0, 32)
		if err != nil {
			return err
		}
	}
	switch action {
	case "down", "up":
		return client.SendKeyCode(uint16(keyCode), keyboardType, uint32(keysym), action == "down")
	case "tap":
		err = client.SendKeyCode(uint16(keyCode), keyboardType, uint32(keysym), true)
		if err != nil {
			return err
		}
		return client.SendKeyCode(uint16(keyCode), keyboardType, uint32(keysym), false)
	default:
		return fmt.Errorf("keycode action %q is not down, up or tap", action)
	}
}

var accessibilitySettings = map[string]rfb.AccessibilityFeature{
	"contrast":     rfb.AccessibilityIncreaseContrast,
	"motion":       rfb.AccessibilityReduceMotion,
	"color":        rfb.AccessibilityDifferentiateWithoutColor,
	"transparency": rfb.AccessibilityReduceTransparency,
	"borders":      rfb.AccessibilityShowBorders,
}

func setAccessibility(client *rfb.Client, argument string) error {
	name, state, found := strings.Cut(argument, ",")
	if !found || (state != "on" && state != "off") {
		return fmt.Errorf("a11y needs SETTING,on|off, got %q", argument)
	}
	feature, known := accessibilitySettings[name]
	if !known {
		number, err := strconv.ParseUint(name, 10, 32)
		if err != nil {
			return fmt.Errorf("unknown accessibility setting %q", name)
		}
		feature = rfb.AccessibilityFeature(number)
	}
	return client.SetAccessibilitySetting(feature, state == "on")
}

func setClipboardFlavors(client *rfb.Client, argument string) error {
	var item rfb.ClipboardItem
	for _, pair := range strings.Split(argument, ",") {
		flavorType, path, found := strings.Cut(pair, "=")
		if !found {
			return fmt.Errorf("flavor %q is not TYPE=FILE", pair)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		item.Flavors = append(item.Flavors, rfb.ClipboardFlavor{Type: flavorType, Data: data})
	}
	return client.SetClipboard(&rfb.Clipboard{Items: []rfb.ClipboardItem{item}})
}

func runHighPerformance(client *rfb.Client, h *handler, argument string, wait func(time.Duration) error) error {
	parts := strings.Split(argument, ",")
	duration, err := time.ParseDuration(parts[0])
	if err != nil {
		return err
	}
	path := ""
	if len(parts) > 1 {
		path = parts[1]
	}
	options := media.OfferOptions{Codecs: []media.Codec{media.CodecHEVC}, FrameRate60: true, Audio: media.AudioOff}
	if len(parts) > 2 {
		options = parseMediaOptions(parts[2:])
	}
	recorder, err := newMediaRecorder(h.logger, path)
	if err != nil {
		return err
	}
	defer recorder.close()
	err = client.StartMediaStream(options, recorder)
	if err != nil {
		return err
	}
	err = wait(duration)
	logStreamStatistics(h, client)
	stopErr := client.StopMediaStream()
	return errors.Join(err, stopErr)
}

func logStreamStatistics(h *handler, client *rfb.Client) {
	stream := client.MediaStream()
	if stream == nil {
		return
	}
	statistics := stream.Stats()
	h.logger.Info("stream statistics", "video-packets", statistics.VideoPackets,
		"access-units-received", statistics.VideoAccessUnitsReceived, "access-units", statistics.VideoAccessUnits,
		"losses", statistics.VideoLosses, "auth-failures", statistics.AuthFailures, "keyframe-requests", statistics.KeyframeRequests,
		"sender-reports", statistics.SenderReports, "video-hdr", statistics.Ports.Video.HDR,
		"answer-ssrc", fmt.Sprintf("%#x", statistics.Answer.Video.SSRC), "canvas", fmt.Sprint(statistics.Answer.Video.Width, "x", statistics.Answer.Video.Height))
}

func parseMediaOptions(flags []string) media.OfferOptions {
	options := media.OfferOptions{Codecs: []media.Codec{media.CodecHEVC}, FrameRate60: true, Audio: media.AudioOff}
	for _, flag := range flags {
		switch flag {
		case "audio":
			options.Audio = media.AudioFromServer
		case "420":
			options.PixelFormats = media.PixelFormatsChroma420
		case "h264":
			options.Codecs = []media.Codec{media.CodecH264}
		case "bands":
			options.TilesPerFrame = 4
		case "nearby":
			options.VideoMode = media.VideoModeNearby
		case "remotedesktop":
			options.VideoMode = media.VideoModeRemoteDesktop
		case "coredevice":
			options.VideoMode = media.VideoModeCoreDevice
		}
	}
	return options
}

func copyFile(ctx context.Context, client *rfb.Client, h *handler, verb, argument string) error {
	source, destination, found := strings.Cut(argument, ",")
	if !found {
		return fmt.Errorf("%s needs SOURCE,DESTINATION", verb)
	}
	progress := func(progress filecopy.Progress) {
		h.logger.Debug("file copy", "item", progress.Item, "transferred", progress.Transferred, "total", progress.Total)
	}
	var transfer *filecopy.Transfer
	var err error
	if verb == "upload" {
		transfer, err = client.FileCopy().Upload(ctx, filecopy.PathSource(source), destination, progress)
	} else {
		transfer, err = client.FileCopy().Download(ctx, source, filecopy.DirectoryReceiver(destination), progress)
	}
	if err != nil {
		return err
	}
	result, err := transfer.Wait(ctx)
	if err != nil {
		return err
	}
	final := transfer.Progress()
	h.logger.Info("file copy done", "name", result.Name, "bytes", final.Transferred, "total", final.Total)
	return nil
}

func fetchClipboard(client *rfb.Client, h *handler) (*rfb.Clipboard, error) {
	for len(h.clipboards) > 0 {
		<-h.clipboards
	}
	err := client.RequestClipboard()
	if err != nil {
		return nil, err
	}
	select {
	case clipboard := <-h.clipboards:
		return clipboard, nil
	case <-time.After(10 * time.Second):
		return nil, errors.New("no pasteboard within 10s")
	}
}

func setVirtualDisplay(client *rfb.Client, argument string) error {
	size, densityText, _ := strings.Cut(argument, "@")
	widthText, heightText, found := strings.Cut(size, "x")
	if !found {
		return fmt.Errorf("virtual display %q is not WxH", argument)
	}
	width, err := strconv.Atoi(widthText)
	if err != nil {
		return err
	}
	height, err := strconv.Atoi(heightText)
	if err != nil {
		return err
	}
	density := 1.0
	if densityText != "" {
		density, err = strconv.ParseFloat(densityText, 64)
		if err != nil {
			return err
		}
	}
	return client.SetVirtualDisplay(width, height, density, 0, 0)
}

func savePNG(client *rfb.Client, path string) error {
	var picture *image.RGBA
	client.Framebuffer(func(framebuffer *rfb.Framebuffer) {
		picture = image.NewRGBA(image.Rect(0, 0, framebuffer.Width, framebuffer.Height))
		for y := 0; y < framebuffer.Height; y++ {
			source := framebuffer.Pix[y*framebuffer.Stride : y*framebuffer.Stride+framebuffer.Width*4]
			target := picture.Pix[y*picture.Stride : y*picture.Stride+framebuffer.Width*4]
			for x := 0; x < len(source); x += 4 {
				target[x], target[x+1], target[x+2], target[x+3] = source[x+2], source[x+1], source[x], source[x+3]
			}
		}
	})
	return writePNG(path, picture)
}

func saveCursor(h *handler, path string) error {
	h.access.Lock()
	cursor := h.cursor
	h.access.Unlock()
	if cursor == nil {
		return errors.New("no cursor received")
	}
	picture := image.NewNRGBA(image.Rect(0, 0, cursor.Width, cursor.Height))
	for i := 0; i < cursor.Width*cursor.Height; i++ {
		blue, green, red, alpha := cursor.Pixels[i*4], cursor.Pixels[i*4+1], cursor.Pixels[i*4+2], cursor.Pixels[i*4+3]
		if alpha > 0 {
			red = byte(int(red) * 255 / int(alpha))
			green = byte(int(green) * 255 / int(alpha))
			blue = byte(int(blue) * 255 / int(alpha))
		}
		picture.Pix[i*4], picture.Pix[i*4+1], picture.Pix[i*4+2], picture.Pix[i*4+3] = red, green, blue, alpha
	}
	return writePNG(path, picture)
}

func writePNG(path string, picture image.Image) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	err = png.Encode(file, picture)
	closeErr := file.Close()
	return errors.Join(err, closeErr)
}
