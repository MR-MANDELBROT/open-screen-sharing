package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb"
	E "github.com/sagernet/sing/common/exceptions"
)

const usage = `usage: sharectl [flags] [action ...]

Actions run in order once the session is up:
  wait:DURATION            keep receiving, e.g. wait:2s
  png:FILE                 save the framebuffer as PNG
  cursor:FILE              save the last cursor image as PNG
  move:X,Y[,BUTTONS]       move the pointer (framebuffer pixels) with BUTTONS held
  click:X,Y[,BUTTONS]      press and release buttons (1 left, 2 middle, 4 right)
  scroll:X,Y,DY[,DX]       scroll by points, positive DY up
  key:KEYSYM               press and release a keysym (hex or decimal)
  keydown:KEYSYM / keyup:KEYSYM
  keycode:CODE[,down|up|tap[,TYPE[,KEYSYM]]]  press a Mac key code (kVK_*) on keyboard type
                           TYPE (40 ANSI, 41 ISO, 42 JIS), KEYSYM for the Mac's password fields
  type:TEXT                type text
  mode:observe|control|exclusive
  quality:adaptive|full|high|medium|low
  display:ID|all           select a display
  scale:FACTOR             server-side scaling, (0, 1]
  virtual:WxH[@DENSITY]    virtual display in points at density 1 or 2
  hdr:on|off               HDR reference mode of the virtual display
  keysource:ID             raw SetKeyboardInputSource: the Mac re-selects ID before every
                           key until keysource: with no ID
  keysource-once:ID        switch the Mac's keyboard input source once
  paste-text:TEXT          type text through the Mac's pasteboard and Command-V
  clipboard-set:TEXT       put text on the remote pasteboard
  clipboard-get            fetch the remote pasteboard
  shared-clipboard:on|off  follow the Mac's pasteboard or stop
  refresh                  request the whole framebuffer
  exec:COMMAND             run a shell command; the script stops if it fails
  raw:HEX                  send one raw client message
  upload:LOCAL,REMOTE-FOLDER / download:REMOTE,LOCAL-FOLDER  copy a file or folder
  hp:DURATION[,FILE[,FLAG...]]  receive High Performance video (HEVC, Annex-B to FILE);
                           flags: audio, 420, h264, bands, and the profile:
                           nearby, remotedesktop or coredevice
  hp-start[:FILE[,FLAG...]] / hp-stop    the same, around other actions
  clipboard-set-flavors:TYPE=FILE,...  put one item with these flavors on the pasteboard
  clipboard-save / clipboard-restore   fetch the pasteboard and put it back later
  stats                    print receive counters
  dblclick:X,Y / drag:X1,Y1,X2,Y2   double-click, or drag with the left button
  drop-start:PATH,...      start dragging local files onto the Mac; move with button 1 held
  drop-cancel / drop-finish:TIMEOUT  cancel it, or wait for the folder it landed on and send the files
  drag-request / drag-cancel         ask for the Mac's own drag, or cancel it
  drag-get:LOCAL-FOLDER    copy the files of the Mac's own drag
  pinch:X,Y,STEPS,AMOUNT   magnify gesture: STEPS events of AMOUNT each (0.1 zooms in)
  rotate:X,Y,STEPS,DEGREES rotate gesture, counterclockwise positive
  smart-magnify:X,Y / lookup:X,Y      two-finger double tap / look up
  swipe:X,Y,back|forward|up|down      page swipe
  flick:X,Y,DY,VELOCITY[,DX,VX]       trackpad scroll with phases, then momentum
  system:mission-control|app-windows|show-desktop|launchpad   toggle it on the Mac
  assist:X,Y[,POINTER[,pressed]] / assist-off   show the assist cursor to the Mac's user
  a11y:SETTING,on|off      Mac display setting for the session: contrast, motion, color,
                           transparency, borders (or the feature number)
`

func main() {
	address := flag.String("address", "127.0.0.1:5900", "server host:port")
	username := flag.String("user", "", "account name")
	password := flag.String("password", os.Getenv("SHARECTL_PASSWORD"), "password (or SHARECTL_PASSWORD)")
	security := flag.String("security", "", "comma-separated security types in order of preference (numbers or names)")
	dialect := flag.String("dialect", "auto", "auto, apple or standard")
	rfbVersion := flag.String("rfb-version", "", "highest RFB version for a standard server: 3.3, 3.7 or 3.8")
	quality := flag.String("quality", "full", "adaptive, full, high, medium or low")
	mode := flag.String("mode", "observe", "observe, control or exclusive")
	clipboard := flag.Bool("clipboard", false, "share the clipboard")
	automatic := flag.Bool("automatic", true, "use Apple's pushed updates when offered")
	interval := flag.Duration("interval", 0, "minimum interval of pushed updates")
	display := flag.Uint("display", 0, "display id to share, 0 for all")
	encodings := flag.String("encodings", "", "comma-separated picture encodings replacing the quality's")
	dump := flag.String("dump", "", "directory to write raw rectangles into")
	dumpEncodings := flag.String("dump-encodings", "", "comma-separated encodings to dump (default all)")
	session := flag.String("session", "cancel", "answer to session selection: console, own or cancel")
	name := flag.String("name", "sharectl", "name shown to the Mac's user for permission requests")
	allowUnverified := flag.Bool("allow-unverified", false, "allow account logins that send the password to an unverified server (types 30, and 33 without 36)")
	probe := flag.Bool("probe", false, "print the offered security types and exit without logging in")
	verbose := flag.Bool("v", false, "debug logging")
	flag.Usage = func() {
		fmt.Fprint(os.Stderr, usage, "\nflags:\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	options := rfb.Options{
		Address:          *address,
		Username:         *username,
		Password:         *password,
		ClientName:       *name,
		Clipboard:        *clipboard,
		AutomaticUpdates: *automatic,
		UpdateInterval:   *interval,
		DisplayID:        uint32(*display),
		Logger:           logger,

		AllowUnverifiedLogin: *allowUnverified,
	}
	err := parseOptions(&options, *security, *dialect, *quality, *mode)
	if err != nil {
		fatal(err)
	}
	sessionChoice, err := parseSession(*session)
	if err != nil {
		fatal(err)
	}
	if *rfbVersion != "" {
		_, err = fmt.Sscanf(*rfbVersion, "%d.%d", &options.MaximumVersion.Major, &options.MaximumVersion.Minor)
		if err != nil {
			fatal(err)
		}
	}
	for _, name := range strings.Split(*encodings, ",") {
		if name == "" {
			continue
		}
		encoding, parseErr := rfb.ParseEncoding(strings.TrimSpace(name))
		if parseErr != nil {
			fatal(parseErr)
		}
		options.Encodings = append(options.Encodings, encoding)
	}
	handler := newHandler(logger, sessionChoice)
	if *dump != "" {
		options.RectangleObserver, err = newDumper(*dump, *dumpEncodings)
		if err != nil {
			fatal(err)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	started := time.Now()
	if *probe {
		probed, probeErr := rfb.Probe(ctx, options)
		if probeErr != nil {
			fatal(probeErr)
		}
		logger.Info("probed", "elapsed", time.Since(started).Round(time.Millisecond),
			"version", probed.Version.String(), "dialect", probed.Dialect.String(), "offered", fmt.Sprint(probed.SecurityTypes))
		return
	}
	client, err := rfb.Dial(ctx, options, handler)
	if err != nil {
		fatal(err)
	}
	handler.client = client
	info := client.Info()
	logger.Info("connected", "elapsed", time.Since(started).Round(time.Millisecond), "name", info.Name,
		"version", info.Version.String(), "dialect", info.Dialect.String(), "security", info.SecurityType.String(),
		"offered", fmt.Sprint(info.SecurityTypes), "size", fmt.Sprint(info.Width, "x", info.Height),
		"flags", fmt.Sprintf("%#x", info.Flags), "commands", fmt.Sprintf("%x", info.Commands))
	done := make(chan struct{})
	var runErr error
	go func() {
		runErr = client.Run()
		close(done)
	}()
	scriptErr := runScript(ctx, client, handler, flag.Args(), done, &runErr)
	client.Close()
	<-done
	if scriptErr != nil && !errors.Is(scriptErr, errSessionEnded) {
		fatal(scriptErr)
	}
	if runErr != nil && !errors.Is(runErr, net.ErrClosed) {
		fatal(runErr)
	}
}

func parseOptions(options *rfb.Options, security, dialect, quality, mode string) error {
	if security != "" {
		for _, value := range strings.Split(security, ",") {
			securityType, err := rfb.ParseSecurityType(strings.TrimSpace(value))
			if err != nil {
				return err
			}
			options.SecurityTypes = append(options.SecurityTypes, securityType)
		}
	}
	switch dialect {
	case "auto":
		options.Dialect = rfb.DialectAutomatic
	case "apple":
		options.Dialect = rfb.DialectApple
	case "standard":
		options.Dialect = rfb.DialectStandard
	default:
		return E.New("unknown dialect ", strconv.Quote(dialect))
	}
	var err error
	options.Quality, err = rfb.ParseQuality(quality)
	if err != nil {
		return err
	}
	options.Mode, err = parseMode(mode)
	return err
}

func parseSession(value string) (rfb.SessionChoice, error) {
	switch value {
	case "console":
		return rfb.SessionShareConsole, nil
	case "own":
		return rfb.SessionOwn, nil
	case "cancel":
		return rfb.SessionCancel, nil
	default:
		return 0, E.New("unknown session answer ", strconv.Quote(value))
	}
}

func parseMode(value string) (rfb.Mode, error) {
	for mode := rfb.ModeObserve; mode <= rfb.ModeExclusiveControl; mode++ {
		if mode.String() == value {
			return mode, nil
		}
	}
	return 0, E.New("unknown mode ", strconv.Quote(value))
}

func parseNumbers[T any](value string, count int, parse func(string) (T, error)) ([]T, error) {
	parts := strings.Split(value, ",")
	if len(parts) < count {
		return nil, E.New(strconv.Quote(value), " needs ", count, " numbers")
	}
	numbers := make([]T, len(parts))
	for i, part := range parts {
		number, err := parse(strings.TrimSpace(part))
		if err != nil {
			return nil, err
		}
		numbers[i] = number
	}
	return numbers, nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "sharectl:", err)
	os.Exit(1)
}
