package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sagernet/sing/common"
)

const (
	project     = "OpenScreenSharing.xcodeproj"
	scheme      = "OpenScreenSharing"
	derivedData = "build/DerivedData-screenshots-"
	outputPath  = "screenshots"
	resultsPath = "build/screenshots"
	testsToRun  = "OpenScreenSharingUITests/ScreenshotTests"
	macPage     = "01-Connections"
)

type size struct {
	width  int
	height int
}

type preference struct {
	domain string
	key    string
	kind   string
	value  string
}

type device struct {
	name        string
	platform    string
	deviceType  string
	sizes       []size
	preferences []preference
}

var devices = []device{
	{
		name:       "iphone",
		platform:   "iOS Simulator",
		deviceType: "com.apple.CoreSimulator.SimDeviceType.iPhone-17-Pro-Max",
		sizes:      []size{{1320, 2868}, {1290, 2796}, {1260, 2736}},
	},
	{
		name:       "ipad",
		platform:   "iOS Simulator",
		deviceType: "com.apple.CoreSimulator.SimDeviceType.iPad-Pro-13-inch-M5-12GB",
		sizes:      []size{{2064, 2752}, {2048, 2732}},
		// Full Screen Apps in Settings › Multitasking & Gestures; windowed apps get a resize grabber and the app name in the status bar.
		preferences: []preference{
			{"com.apple.springboard", "SBMedusaMultitaskingEnabled", "-bool", "false"},
			{"com.apple.springboard", "SBChamoisWindowingEnabled", "-bool", "false"},
		},
	},
	{
		name:     "mac",
		platform: "macOS",
		sizes:    []size{{1280, 800}, {1440, 900}, {2560, 1600}, {2880, 1800}},
	},
}

type variant struct {
	name     string
	devices  []string
	settings []string
}

var variants = []variant{
	{
		name:    "app-store",
		devices: []string{"iphone", "ipad", "mac"},
		settings: []string{
			"SWIFT_ACTIVE_COMPILATION_CONDITIONS=DEMO",
			"EXCLUDED_SOURCE_FILE_NAMES=$(SRCROOT)/OpenScreenSharing/Server/*",
		},
	},
	{
		name:    "sideload",
		devices: []string{"iphone", "ipad"},
		settings: []string{
			"SWIFT_ACTIVE_COMPILATION_CONDITIONS=SIDELOAD DEMO",
			"INFOPLIST_PREPROCESSOR_DEFINITIONS=SIDELOAD=1",
			"EXCLUDED_SOURCE_FILE_NAMES=",
		},
	},
}

var simulatorPreferences = []preference{
	{"-g", "AppleLanguages", "-array", "en-US"},
	{"-g", "AppleLocale", "-string", "en_US"},
}

var regions = map[string]string{
	"en":      "US",
	"zh-Hans": "CN",
}

var screenshotName = regexp.MustCompile(`^([A-Za-z-]+)_(\d\d-[A-Za-z]+)_`)

func main() {
	languages := flag.String("languages", "en", "the languages to take the screenshots in")
	deviceNames := flag.String("devices", "iphone,ipad,mac", "the devices to take the screenshots on")
	variantNames := flag.String("variants", "app-store,sideload", "the builds to take the screenshots of")
	appearance := flag.String("appearance", "light", "light or dark")
	skipBuild := flag.Bool("skip-build", false, "use the apps and tests as last built")
	flag.Parse()

	var chosenDevices []device
	for _, name := range strings.Split(*deviceNames, ",") {
		index := slices.IndexFunc(devices, func(device device) bool { return device.name == name })
		if index < 0 {
			log.Fatal("unknown device: ", name)
		}
		chosenDevices = append(chosenDevices, devices[index])
	}
	var chosenVariants []variant
	for _, name := range strings.Split(*variantNames, ",") {
		index := slices.IndexFunc(variants, func(variant variant) bool { return variant.name == name })
		if index < 0 {
			log.Fatal("unknown variant: ", name)
		}
		chosenVariants = append(chosenVariants, variants[index])
	}
	chosenLanguages := strings.Split(*languages, ",")
	for _, language := range chosenLanguages {
		if regions[language] == "" {
			log.Fatal("unknown language: ", language)
		}
	}
	if !*skipBuild {
		for _, variant := range chosenVariants {
			var platforms []string
			for _, device := range chosenDevices {
				if slices.Contains(variant.devices, device.name) && !slices.Contains(platforms, device.platform) {
					platforms = append(platforms, device.platform)
				}
			}
			for _, platform := range platforms {
				action := "build-for-testing"
				if platform == "macOS" {
					action = "build"
				}
				run(exec.Command("xcodebuild", append([]string{
					action, "-quiet",
					"-project", project,
					"-scheme", scheme,
					"-configuration", "Release",
					"-destination", "generic/platform=" + platform,
					"-derivedDataPath", derivedData + variant.name,
					"ARCHS=$(NATIVE_ARCH_ACTUAL)",
					"ENABLE_CODE_COVERAGE=NO",
				}, variant.settings...)...))
			}
		}
	}
	runtime := latestRuntime()
	log.Print("on ", runtime)

	for _, device := range chosenDevices {
		deviceVariants := common.Filter(chosenVariants, func(variant variant) bool { return slices.Contains(variant.devices, device.name) })
		if len(deviceVariants) == 0 {
			continue
		}
		if device.platform == "macOS" {
			for _, variant := range deviceVariants {
				takeMacScreenshots(device, variant, chosenLanguages, *appearance)
			}
			continue
		}
		simulator := startSimulator(device, runtime, *appearance)
		for _, variant := range deviceVariants {
			takeScreenshots(device, simulator, variant, chosenLanguages)
		}
		run(exec.Command("xcrun", "simctl", "shutdown", simulator))
	}
}

func latestTestRun(variant variant) string {
	paths, err := filepath.Glob(filepath.Join(derivedData+variant.name, "Build/Products/*.xctestrun"))
	if err != nil {
		log.Fatal(err)
	}
	if len(paths) == 0 {
		log.Fatal("no .xctestrun in ", derivedData+variant.name, "; build for testing first")
	}
	var latest string
	var latestTime int64
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			log.Fatal(err)
		}
		if info.ModTime().UnixNano() > latestTime {
			latest = path
			latestTime = info.ModTime().UnixNano()
		}
	}
	return latest
}

func latestRuntime() string {
	var list struct {
		Runtimes []struct {
			Identifier  string `json:"identifier"`
			Platform    string `json:"platform"`
			Version     string `json:"version"`
			IsAvailable bool   `json:"isAvailable"`
		} `json:"runtimes"`
	}
	decode(output(exec.Command("xcrun", "simctl", "list", "runtimes", "--json")), &list)
	var latest string
	var latestVersion []int
	for _, runtime := range list.Runtimes {
		if runtime.Platform != "iOS" || !runtime.IsAvailable {
			continue
		}
		version := parseVersion(runtime.Version)
		if slices.Compare(version, latestVersion) > 0 {
			latest = runtime.Identifier
			latestVersion = version
		}
	}
	if latest == "" {
		log.Fatal("no iOS simulator runtime")
	}
	return latest
}

func parseVersion(version string) []int {
	var parts []int
	for _, part := range strings.Split(version, ".") {
		number, err := strconv.Atoi(part)
		if err != nil {
			log.Fatal("bad version: ", version)
		}
		parts = append(parts, number)
	}
	return parts
}

type simulator struct {
	UDID                 string `json:"udid"`
	Name                 string `json:"name"`
	State                string `json:"state"`
	DeviceTypeIdentifier string `json:"deviceTypeIdentifier"`
	DataPath             string `json:"dataPath"`
}

func findSimulator(device device, runtime string) (simulator, bool) {
	var list struct {
		Devices map[string][]simulator `json:"devices"`
	}
	decode(output(exec.Command("xcrun", "simctl", "list", "devices", "--json")), &list)
	for _, existing := range list.Devices[runtime] {
		if existing.Name == "Screenshots "+device.name && existing.DeviceTypeIdentifier == device.deviceType {
			return existing, true
		}
	}
	return simulator{}, false
}

func startSimulator(device device, runtime string, appearance string) string {
	existing, found := findSimulator(device, runtime)
	if !found {
		output(exec.Command("xcrun", "simctl", "create", "Screenshots "+device.name, device.deviceType, runtime))
		existing, _ = findSimulator(device, runtime)
	}
	simulator := existing.UDID
	if existing.State == "Shutdown" {
		run(exec.Command("xcrun", "simctl", "boot", simulator))
	}
	output(exec.Command("xcrun", "simctl", "bootstatus", simulator, "-b"))
	var changed bool
	for _, preference := range append(slices.Clone(simulatorPreferences), device.preferences...) {
		file := preference.domain
		if file == "-g" {
			file = ".GlobalPreferences"
		}
		keyPath := preference.key
		if preference.kind == "-array" {
			keyPath += ".0"
		}
		current, _ := exec.Command("plutil", "-extract", keyPath, "raw", "-o", "-", filepath.Join(existing.DataPath, "Library/Preferences", file+".plist")).Output()
		if strings.TrimSpace(string(current)) != preference.value {
			run(exec.Command("xcrun", "simctl", "spawn", simulator, "defaults", "write", preference.domain, preference.key, preference.kind, preference.value))
			changed = true
		}
	}
	if changed {
		run(exec.Command("xcrun", "simctl", "shutdown", simulator))
		run(exec.Command("xcrun", "simctl", "boot", simulator))
		output(exec.Command("xcrun", "simctl", "bootstatus", simulator, "-b"))
	}
	run(exec.Command("xcrun", "simctl", "ui", simulator, "appearance", appearance))
	run(exec.Command("xcrun", "simctl", "status_bar", simulator, "override",
		"--dataNetwork", "wifi",
		"--wifiMode", "active",
		"--wifiBars", "3",
		"--cellularMode", "active",
		"--cellularBars", "4",
		"--batteryState", "charged",
		"--batteryLevel", "100",
	))
	run(exec.Command("xcrun", "simctl", "status_bar", simulator, "override",
		"--time", time.Date(2007, time.January, 9, 9, 41, 0, 0, time.Local).Format("2006-01-02T15:04:05.000Z07:00"),
	))
	return simulator
}

func takeScreenshots(device device, simulator string, variant variant, languages []string) {
	log.Print("taking ", variant.name, " ", device.name, " screenshots")
	resultBundle := filepath.Join(resultsPath, variant.name, device.name+".xcresult")
	attachments := filepath.Join(resultsPath, variant.name, device.name)
	paths := []string{resultBundle, attachments}
	var locales []string
	for _, language := range languages {
		paths = append(paths, filepath.Join(outputPath, variant.name, language, device.name))
		locales = append(locales, language+"_"+regions[language])
	}
	for _, path := range paths {
		err := os.RemoveAll(path)
		if err != nil {
			log.Fatal(err)
		}
	}
	test := exec.Command("xcodebuild", "test-without-building", "-quiet",
		"-xctestrun", latestTestRun(variant),
		"-destination", "id="+simulator,
		"-only-testing:"+testsToRun,
		"-parallel-testing-enabled", "NO",
		"-collect-test-diagnostics", "never",
		"-resultBundlePath", resultBundle,
	)
	test.Env = append(os.Environ(), "TEST_RUNNER_SCREENSHOT_LOCALES="+strings.Join(locales, ","))
	run(test)

	run(exec.Command("xcrun", "xcresulttool", "export", "attachments", "--path", resultBundle, "--output-path", attachments))
	var manifest []struct {
		Attachments []struct {
			ExportedFileName           string `json:"exportedFileName"`
			SuggestedHumanReadableName string `json:"suggestedHumanReadableName"`
		} `json:"attachments"`
	}
	manifestData, err := os.ReadFile(filepath.Join(attachments, "manifest.json"))
	if err != nil {
		log.Fatal(err)
	}
	decode(manifestData, &manifest)
	for _, test := range manifest {
		for _, attachment := range test.Attachments {
			match := screenshotName.FindStringSubmatch(attachment.SuggestedHumanReadableName)
			if match == nil {
				continue
			}
			data, err := os.ReadFile(filepath.Join(attachments, attachment.ExportedFileName))
			if err != nil {
				log.Fatal(err)
			}
			save(data, device, variant, match[1], match[2])
		}
	}
}

func takeMacScreenshots(device device, variant variant, languages []string, appearance string) {
	log.Print("taking ", variant.name, " ", device.name, " screenshots")
	executables, err := filepath.Glob(filepath.Join(derivedData+variant.name, "Build/Products/Release/*.app/Contents/MacOS/*"))
	if err != nil {
		log.Fatal(err)
	}
	if len(executables) != 1 {
		log.Fatal("no single macOS app in ", derivedData+variant.name, "; build it first")
	}
	style := "Light"
	if appearance == "dark" {
		style = "Dark"
	}
	for _, language := range languages {
		err = os.RemoveAll(filepath.Join(outputPath, variant.name, language, device.name))
		if err != nil {
			log.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		app := exec.CommandContext(ctx, executables[0],
			"-AppleLanguages", "("+language+")",
			"-AppleLocale", language+"_"+regions[language],
			"-AppleInterfaceStyle", style,
			// Blue; otherwise the sidebar takes this Mac's accent color.
			"-AppleAccentColor", "4",
			"-DemoScreenshot", macPage,
			// AppKit reads the arguments as -key value pairs and opens any left over as documents, and then no window opens.
			"-Demo", "YES",
		)
		var stdout, stderr bytes.Buffer
		app.Stdout = &stdout
		app.Stderr = &stderr
		start := time.Now()
		err = app.Run()
		cancel()
		if err != nil {
			log.Fatal(app.Args, ": ", err, "\n", stdout.String(), stderr.String())
		}
		logTime(app, start)
		lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
		captured := lines[len(lines)-1]
		data, err := os.ReadFile(captured)
		if err != nil {
			log.Fatal(err)
		}
		save(data, device, variant, language, macPage)
		err = os.Remove(captured)
		if err != nil {
			log.Fatal(err)
		}
	}
}

func save(data []byte, device device, variant variant, language string, name string) {
	err := checkScreenshot(data, device.sizes)
	if err != nil {
		log.Fatal(name, ": ", err)
	}
	destination := filepath.Join(outputPath, variant.name, language, device.name)
	err = os.MkdirAll(destination, 0o755)
	if err != nil {
		log.Fatal(err)
	}
	path := filepath.Join(destination, name+".png")
	err = os.WriteFile(path, data, 0o644)
	if err != nil {
		log.Fatal(err)
	}
	log.Print(path)
}

func checkScreenshot(data []byte, sizes []size) error {
	signature := []byte("\x89PNG\r\n\x1a\n")
	if !bytes.HasPrefix(data, signature) {
		return errors.New("not a PNG")
	}
	var width, height int
	var colorType byte
	for rest := data[len(signature):]; len(rest) >= 12; {
		length := int(binary.BigEndian.Uint32(rest))
		if len(rest) < 12+length {
			return errors.New("truncated PNG")
		}
		chunk, content := string(rest[4:8]), rest[8:8+length]
		switch chunk {
		case "IHDR":
			width = int(binary.BigEndian.Uint32(content))
			height = int(binary.BigEndian.Uint32(content[4:]))
			colorType = content[9]
		case "tRNS":
			return errors.New("transparent")
		}
		rest = rest[12+length:]
	}
	if colorType == 4 || colorType == 6 {
		return errors.New("has an alpha channel")
	}
	for _, accepted := range sizes {
		if width == accepted.width && height == accepted.height || width == accepted.height && height == accepted.width {
			return nil
		}
	}
	return fmt.Errorf("%d×%d isn't a size the App Store takes for the device", width, height)
}

func decode(data []byte, value any) {
	err := json.Unmarshal(data, value)
	if err != nil {
		log.Fatal(err)
	}
}

func output(command *exec.Cmd) []byte {
	command.Stderr = os.Stderr
	start := time.Now()
	data, err := command.Output()
	if err != nil {
		log.Fatal(command.Args, ": ", err)
	}
	logTime(command, start)
	return data
}

func run(command *exec.Cmd) {
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	start := time.Now()
	err := command.Run()
	if err != nil {
		log.Fatal(command.Args, ": ", err)
	}
	logTime(command, start)
}

func logTime(command *exec.Cmd, start time.Time) {
	log.Printf("%5.1fs %s", time.Since(start).Seconds(), strings.Join(command.Args, " "))
}
