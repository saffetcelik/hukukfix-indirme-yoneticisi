// HukukFix — a fast, minimal download manager for Windows.
package main

import (
	"embed"
	"os"
	"path/filepath"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"

	"hukukfixdm/internal/engine"
	"hukukfixdm/internal/shared"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	background := false
	var urls []string
	for _, arg := range os.Args[1:] {
		if arg == "--background" {
			background = true
		} else if isHTTPURL(arg) {
			urls = append(urls, arg)
		}
	}

	dataDir := shared.DataDir()
	eng, err := engine.New(dataDir)
	if err != nil {
		fatalBox("HukukFix could not open its data folder:\n" + err.Error())
		os.Exit(1)
	}
	app := newApp(eng, dataDir, background, urls)

	err = wails.Run(&options.App{
		Title:             displayName(),
		Width:             980,
		Height:            660,
		MinWidth:          620,
		MinHeight:         420,
		StartHidden:       background,
		HideWindowOnClose: true, // closing the window keeps HukukFix in the tray
		BackgroundColour:  &options.RGBA{R: 21, G: 22, B: 25, A: 255},
		AssetServer:       &assetserver.Options{Assets: assets},
		OnStartup:         app.startup,
		OnShutdown:        app.shutdown,
		Bind:              []interface{}{app},
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId: "hukukfix-download-manager-7c1f3a52",
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
