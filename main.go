// HukukFix — a fast, minimal download manager for Windows.
package main

import (
	"embed"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"

	"hukukfixdm/internal/engine"
	"hukukfixdm/internal/shared"
)

//go:embed all:frontend/dist
var assets embed.FS

// instanceID makes a second start hand its arguments to the running copy. The test build uses its own, so
// tests can run while the real HukukFix is open.
var instanceID = "hukukfix-download-manager-7c1f3a52"

func main() {
	background, uninstalling, quiet, after := false, false, false, 0
	var urls, passOn []string // passOn: what a hand-over to another copy keeps
	for _, arg := range os.Args[1:] {
		switch {
		case arg == "--background":
			background = true
			passOn = append(passOn, arg)
		case arg == "--uninstall": // from Windows' "Installed apps" list
			uninstalling = true
		case arg == "--yes":
			quiet = true
		case strings.HasPrefix(arg, "--after="): // started by an update: let the old version exit first
			after, _ = strconv.Atoi(strings.TrimPrefix(arg, "--after="))
		case isHTTPURL(arg):
			urls = append(urls, arg)
			passOn = append(passOn, arg)
		}
	}
	dataDir := shared.DataDir()
	updatedFrom := ""
	if installSteps { // never in the binary "wails build" runs to generate bindings (see buildmode.go)
		if uninstalling {
			uninstall(quiet)
			return
		}
		if after > 0 {
			waitForExit(after, 20*time.Second)
		}
		if selfInstall(passOn) {
			return // the installed copy took over
		}
		if applyStagedUpdate(dataDir, passOn) {
			return // the new version took over
		}
		updatedFrom = prepareInstall(dataDir)
	}

	eng, err := engine.New(dataDir)
	if err != nil {
		fatalBox("HukukFix could not open its data folder:\n" + err.Error())
		os.Exit(1)
	}
	app := newApp(eng, dataDir, background, urls)
	app.updatedFrom = updatedFrom

	err = wails.Run(&options.App{
		Title:             displayName() + " " + shared.Version, // like "Internet Download Manager 6.42"
		Width:             860,
		Height:            560,
		MinWidth:          620,
		MinHeight:         420,
		StartHidden:       background,
		HideWindowOnClose: true, // closing the window keeps HukukFix in the tray
		BackgroundColour:  windowBackground(eng.Settings().Theme),
		AssetServer:       &assetserver.Options{Assets: assets},
		OnStartup:         app.startup,
		OnShutdown:        app.shutdown,
		Bind:              []interface{}{app},
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId: instanceID,
			OnSecondInstanceLaunch: func(d options.SecondInstanceData) {
				app.secondInstance(d.Args)
			},
		},
		Windows: &windows.Options{
			Theme:                windows.SystemDefault,
			WebviewUserDataPath:  filepath.Join(dataDir, "webview"),
			IsZoomControlEnabled: false,
			DisablePinchZoom:     true,
			CustomTheme: &windows.ThemeSettings{
				DarkModeTitleBar:           windows.RGB(21, 22, 25),
				DarkModeTitleBarInactive:   windows.RGB(21, 22, 25),
				DarkModeTitleText:          windows.RGB(236, 237, 240),
				DarkModeTitleTextInactive:  windows.RGB(140, 144, 152),
				DarkModeBorder:             windows.RGB(42, 44, 50),
				DarkModeBorderInactive:     windows.RGB(42, 44, 50),
				LightModeTitleBar:          windows.RGB(250, 250, 251),
				LightModeTitleBarInactive:  windows.RGB(250, 250, 251),
				LightModeTitleText:         windows.RGB(24, 25, 28),
				LightModeTitleTextInactive: windows.RGB(130, 134, 142),
				LightModeBorder:            windows.RGB(226, 228, 233),
				LightModeBorderInactive:    windows.RGB(226, 228, 233),
			},
		},
	})
	if err != nil {
		fatalBox(err.Error())
		os.Exit(1)
	}
}
