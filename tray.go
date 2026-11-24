package main

import (
	_ "embed"
	goruntime "runtime"
	"sync"

	"fyne.io/systray"
)

//go:embed build/tray.ico
var trayIcon []byte

var trayText = map[string][4]string{
	"tr": {"HukukFix'i aç", "Tümünü duraklat", "Tümünü sürdür", "Çıkış"},
	"en": {"Open HukukFix", "Pause all", "Resume all", "Quit"},
}

var tray struct {
	mu    sync.Mutex
	ready bool
	items [4]*systray.MenuItem
}

// runTray owns the notification-area icon. HukukFix keeps running there after
// its window is closed so the browser can always hand downloads over.
func (a *App) runTray() {
	goruntime.LockOSThread()
	systray.Run(func() {
		systray.SetIcon(trayIcon)
		systray.SetTooltip(displayName())
		systray.SetOnTapped(a.showWindow)
		t := trayText[a.lang()]
		tray.mu.Lock()
		tray.items[0] = systray.AddMenuItem(t[0], "")
		systray.AddSeparator()
		tray.items[1] = systray.AddMenuItem(t[1], "")
		tray.items[2] = systray.AddMenuItem(t[2], "")
		systray.AddSeparator()
		tray.items[3] = systray.AddMenuItem(t[3], "")
		tray.ready = true
		items := tray.items
		tray.mu.Unlock()
		go func() {
			for {
				select {
				case <-items[0].ClickedCh:
					a.showWindow()
				case <-items[1].ClickedCh:
					a.eng.PauseAll()
				case <-items[2].ClickedCh:
					a.eng.ResumeAll()
				case <-items[3].ClickedCh:
					a.Quit()
					return
				}
			}
		}()
	}, nil)
}

func refreshTray(lang string) {
	tray.mu.Lock()
	defer tray.mu.Unlock()
	if !tray.ready {
		return
	}
	for i, it := range tray.items {
		it.SetTitle(trayText[lang][i])
	}
}

func stopTray() {
	tray.mu.Lock()
	ready := tray.ready
	tray.mu.Unlock()
	if ready {
		systray.Quit()
	}
}
