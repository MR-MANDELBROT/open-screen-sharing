//go:build darwin && !ios

package main

import (
	"context"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/nekohasekai/open-screen-sharing/internal/coredevice"
	"github.com/nekohasekai/open-screen-sharing/internal/coredevice/lockdown"
	"github.com/nekohasekai/open-screen-sharing/internal/deviceserver"
	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
)

const usage = `usage: shareserve (-password PASSWORD | -no-authentication) [-udid UDID] [flags]

Serves the device's screen on -listen until interrupted. Viewers on this Mac
need -allow-loopback-target, since a viewer on the same address cannot be
reached otherwise.

flags:
`

func main() {
	udid := flag.String("udid", "", "the device's UDID (default: the first device usbmuxd lists, USB before network)")
	listen := flag.String("listen", "127.0.0.1:5910", "RFB TCP address")
	password := flag.String("password", os.Getenv("SHARESERVE_PASSWORD"), "password viewers sign in with (or SHARESERVE_PASSWORD)")
	username := flag.String("username", "", "the only user name viewers may sign in with (default: any)")
	noAuthentication := flag.Bool("no-authentication", false, "let viewers in without signing in, by asking to share the screen")
	name := flag.String("name", "shareserve", "computer name viewers show")
	audio := flag.Bool("audio", true, "send the device's sound")
	clipboard := flag.Bool("clipboard", false, "share the device's clipboard with viewers that ask")
	input := flag.Bool("input", true, "pass the viewers' pointer, keys, scroll and gestures to the device")
	diskImage := flag.String("ddi", "", "directory with the Developer Disk Image's files, to mount it when it is not")
	download := flag.Bool("download", false, "download the Developer Disk Image into -ddi when it has none")
	statsInterval := flag.Duration("stats", 5*time.Second, "interval of media statistics, 0 for none")
	verbose := flag.Bool("v", false, "debug logging")

	serverFlags := flag.String("flags", "", "ServerInit flags word (default rfb.DefaultServerFlags)")
	commands := flag.String("commands", "", "comma-separated client message types offered in ServerInit")
	commandBitmap := flag.String("command-bitmap", "", "32 hex digits sent as the command bitmap without checks")
	displayFlags := flag.String("display-flags", "", "the display's layout flags (default rfb.DisplayFlagMain)")
	rekeyLate := flag.Bool("rekey-after-encryption-start", false, "send the record keys after the viewer's second SetEncryption")
	model := flag.String("model", "", "model sent in encoding 1110 instead of the device's, such as Mac14,3")
	videoPort := flag.Uint("video-port", 0, "video UDP port (default: the listen port + 1)")
	audioPort := flag.Uint("audio-port", 0, "audio UDP port (default: the listen port + 2)")
	disableAudioLeg := flag.Bool("disable-audio-leg", false, "leave the audio port out of the ports message")
	keepProfile := flag.Bool("keep-extension-profile", false, "forward the device's RTP header extension profile unchanged")
	stripTrailer := flag.Bool("strip-slice-trailer", false, "remove the 14 bytes the device appends to each slice")
	allowLoopback := flag.Bool("allow-loopback-target", false, "send media to a loopback viewer before it reports, for viewers on this Mac")
	flag.Usage = func() {
		fmt.Fprint(os.Stderr, usage)
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() > 0 {
		flag.Usage()
		os.Exit(2)
	}
	if *password == "" && !*noAuthentication {
		fatal(errors.New("-password, SHARESERVE_PASSWORD or -no-authentication is required"))
	}
	if *udid == "" {
		found, err := firstDevice()
		if err != nil {
			fatal(fmt.Errorf("-udid not given: %w", err))
		}
		*udid = found
	}

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	_, portText, err := net.SplitHostPort(*listen)
	if err != nil {
		fatal(fmt.Errorf("-listen: %w", err))
	}
	listenPort, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		fatal(fmt.Errorf("-listen: %w", err))
	}
	if *videoPort == 0 {
		*videoPort = uint(listenPort) + 1
	}
	if *audioPort == 0 {
		*audioPort = uint(listenPort) + 2
	}
	if *videoPort > 65535 || *audioPort > 65535 {
		fatal(errors.New("media ports must be below 65536"))
	}
	rfbOptions := rfb.ServerOptions{
		Name:                      *name,
		Password:                  *password,
		Username:                  *username,
		NoAuthentication:          *noAuthentication,
		RekeyAfterEncryptionStart: *rekeyLate,
		DeviceModel:               *model,
	}
	rfbOptions.Media.VideoPort = uint16(*videoPort)
	rfbOptions.Media.AudioPort = uint16(*audioPort)
	rfbOptions.Media.DisableAudioLeg = *disableAudioLeg
	rfbOptions.Media.KeepExtensionProfile = *keepProfile
	rfbOptions.Media.StripSliceTrailer = *stripTrailer
	rfbOptions.Media.AllowLoopbackTarget = *allowLoopback
	if rfbOptions.Flags, err = parseWord(*serverFlags); err != nil {
		fatal(fmt.Errorf("-flags: %w", err))
	}
	if rfbOptions.DisplayFlags, err = parseWord(*displayFlags); err != nil {
		fatal(fmt.Errorf("-display-flags: %w", err))
	}
	if rfbOptions.Commands, err = parseCommands(*commands); err != nil {
		fatal(fmt.Errorf("-commands: %w", err))
	}
	if *commandBitmap != "" {
		bitmap, decodeErr := hex.DecodeString(*commandBitmap)
		if decodeErr != nil || len(bitmap) != 16 {
			fatal(errors.New("-command-bitmap needs 32 hex digits"))
		}
		rfbOptions.CommandBitmap = (*[16]byte)(bitmap)
	}

	observer := &logObserver{logger: logger, failed: make(chan error, 1)}
	deviceLogger := logger.With("component", "device")
	server, err := deviceserver.New(deviceserver.Options{
		Connect: func(ctx context.Context) (*coredevice.Device, error) {
			return coredevice.ConnectUSBMux(ctx, *udid, deviceLogger)
		},
		Logger:             logger,
		Observer:           observer,
		Listen:             *listen,
		RFB:                rfbOptions,
		Audio:              *audio,
		SharedClipboard:    *clipboard,
		DisableInput:       !*input,
		DiskImageDirectory: *diskImage,
		DownloadDiskImage:  *download,
	})
	if err != nil {
		fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err = server.Start()
	if err != nil {
		fatal(err)
	}
	if *statsInterval > 0 {
		go logStats(ctx, logger, server, *statsInterval)
	}
	var failure error
	select {
	case <-ctx.Done():
	case failure = <-observer.failed:
	}
	stop()
	logger.Info("stopping")
	server.Close()
	logger.Info("stopped")
	if failure != nil {
		fatal(failure)
	}
}

func firstDevice() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	devices, err := lockdown.ListDevices(ctx)
	if err != nil {
		return "", err
	}
	found := ""
	for _, device := range devices {
		if device.UDID == "" {
			continue
		}
		if device.ConnectionType == "USB" {
			return device.UDID, nil
		}
		if found == "" {
			found = device.UDID
		}
	}
	if found == "" {
		return "", errors.New("usbmuxd lists no device")
	}
	return found, nil
}

func parseWord(text string) (uint32, error) {
	if text == "" {
		return 0, nil
	}
	value, err := strconv.ParseUint(text, 0, 32)
	return uint32(value), err
}

func parseCommands(text string) ([]uint8, error) {
	if text == "" {
		return nil, nil
	}
	var commands []uint8
	for _, field := range strings.Split(text, ",") {
		value, err := strconv.ParseUint(strings.TrimSpace(field), 0, 8)
		if err != nil {
			return nil, err
		}
		commands = append(commands, uint8(value))
	}
	return commands, nil
}

func logStats(ctx context.Context, logger *slog.Logger, server *deviceserver.Server, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		for _, session := range server.Sessions() {
			stats, ok := session.MediaStats()
			if !ok {
				continue
			}
			logger.Info("media", "viewer", session.ID(), "running", stats.Running,
				"offers", stats.Offers, "answers", stats.Answers, "errors", stats.Errors,
				"video_packets", stats.VideoPackets, "video_bytes", stats.VideoBytes,
				"audio_packets", stats.AudioPackets, "held", stats.HeldPackets,
				"reports", stats.ClientReports, "auth_failures", stats.AuthFailures,
				"send_errors", stats.SendErrors, "keyframe_requests", stats.KeyframeRequests,
				"video_target", stats.VideoTarget.String(), "audio_target", stats.AudioTarget.String())
		}
	}
}

type logObserver struct {
	logger *slog.Logger
	failed chan error
}

func (o *logObserver) StateChanged(state deviceserver.State, err error) {
	if state == deviceserver.StateFailed {
		select {
		case o.failed <- err:
		default:
		}
	}
}

func (o *logObserver) DeviceConnected(info coredevice.DeviceInfo) {
	property := func(key string) any { return info.Properties[key] }
	o.logger.Info("device", "class", info.DeviceClass,
		"description", property("ProductTypeDescForUserVisibility"),
		"model_description", property("HWModelDescriptionForUserVisibility"),
		"version", property("HumanReadableProductVersionString"))
}

func (o *logObserver) DiskImageProgress(received int64) {
	o.logger.Info("disk image download", "received", received)
}

func (o *logObserver) ViewerConnected(*rfb.ServerSession) {}

func (o *logObserver) ViewerDisconnected(*rfb.ServerSession, error) {}

func (o *logObserver) StreamingChanged(bool) {}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "shareserve:", err)
	os.Exit(1)
}
