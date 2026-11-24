package main

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"hukukfixdm/internal/engine"
	"hukukfixdm/internal/shared"
)

// App is the object the window's JavaScript talks to (window.go.main.App).
type App struct {
	ctx     context.Context
	eng     *engine.Engine
	dataDir string
	br      *bridge

	background  bool
	initialURLs []string

	extDir    string
	hostFound bool
	extSeen   atomic.Int64 // unix seconds the extension last called

	emit    chan struct{}
	mu      sync.Mutex
	listSig string
	notify  bool
}

func newApp(eng *engine.Engine, dataDir string, background bool, urls []string) *App {
	return &App{eng: eng, dataDir: dataDir, background: background, initialURLs: urls, emit: make(chan struct{}, 1)}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.eng.OnChange = func() {
		select {
		case a.emit <- struct{}{}:
		default:
		}
	}
	a.eng.OnFinished = a.finished
	a.eng.Start()

	if br, err := startBridge(a, a.dataDir); err == nil {
		a.br = br
	}
	a.hostFound, _ = registerNativeHost(a.dataDir)
	a.extDir, _ = extractExtension(a.dataDir)
	s := a.eng.Settings()
	_ = setAutostart(s.Autostart)
	a.applyTheme(s.Theme)
	if err := runtime.InitializeNotifications(ctx); err == nil {
		a.notify = true
	}

	go a.emitLoop()
	go a.runTray()
	for _, u := range a.initialURLs {
		_, _ = a.eng.Add(engine.AddRequest{URL: u, Source: "manual"})
	}
}

func (a *App) shutdown(ctx context.Context) {
	a.br.close()
	a.eng.Close()
	stopTray()
}

func (a *App) secondInstance(args []string) {
	added := false
	background := false
	for _, arg := range args {
		if arg == "--background" {
			background = true
		} else if isHTTPURL(arg) {
			if _, err := a.eng.Add(engine.AddRequest{URL: arg, Source: "manual"}); err == nil {
				added = true
			}
		}
	}
	if added || !background {
		a.showWindow()
	}
}

func isHTTPURL(s string) bool {
	u, err := url.Parse(strings.TrimSpace(s))
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func (a *App) showWindow() {
	if a.ctx == nil {
		return
	}
	runtime.WindowShow(a.ctx)
	runtime.WindowUnminimise(a.ctx)
}

func (a *App) applyTheme(theme string) {
	switch theme {
	case "dark":
		runtime.WindowSetDarkTheme(a.ctx)
	case "light":
		runtime.WindowSetLightTheme(a.ctx)
	default:
		runtime.WindowSetSystemDefaultTheme(a.ctx)
	}
}

func (a *App) lang() string {
	if l := a.eng.Settings().Language; l == "tr" || l == "en" {
		return l
	}
	return systemLang()
}

// ---- pushing state to the window ----

// StatePayload is what the "state" event carries. Live holds every unfinished
// download; All is only present when the list itself changed (something was
// added, removed or finished), so a long history costs nothing per tick.
type StatePayload struct {
	All    []engine.Snapshot `json:"all,omitempty"`
	Live   []engine.Snapshot `json:"live"`
	Active int               `json:"active"`
	Speed  float64           `json:"speed"`
}

func (a *App) emitLoop() {
	for range a.emit {
		runtime.EventsEmit(a.ctx, "state", a.payload(false))
		time.Sleep(200 * time.Millisecond)
	}
}

func (a *App) payload(full bool) StatePayload {
	list := a.eng.List()
	var sig strings.Builder
	live := make([]engine.Snapshot, 0, 8)
	for _, s := range list {
		sig.WriteString(s.ID)
		sig.WriteByte(':')
		sig.WriteString(string(s.State))
		sig.WriteString(s.FileName)
		sig.WriteByte('|')
		if s.State != engine.StateCompleted {
			live = append(live, s)
		}
	}
	p := StatePayload{Live: live}
	p.Active, p.Speed = a.eng.Stats()
	a.mu.Lock()
	if full || sig.String() != a.listSig {
		a.listSig = sig.String()
		p.All = list
		if p.All == nil {
			p.All = []engine.Snapshot{}
		}
	}
	a.mu.Unlock()
	return p
}

func (a *App) captured(s engine.Snapshot) {
	if a.eng.Settings().ShowOnCapture {
		a.showWindow()
	}
	runtime.EventsEmit(a.ctx, "captured", s.ID)
}

func (a *App) extensionSeen() { a.extSeen.Store(time.Now().Unix()) }

func (a *App) finished(s engine.Snapshot) {
	if !a.notify || !a.eng.Settings().Notify {
		return
	}
	title := map[string][2]string{
		"tr": {"İndirme tamamlandı", "İndirme başarısız"},
		"en": {"Download complete", "Download failed"},
	}[a.lang()]
	t := title[0]
	if s.State == engine.StateFailed {
		t = title[1]
	}
	_ = runtime.SendNotification(a.ctx, runtime.NotificationOptions{ID: s.ID, Title: t, Body: s.FileName})
}

// ---- methods called from JavaScript ----

// InitData is everything the window needs to draw its first frame.
type InitData struct {
	Version   string          `json:"version"`
	Lang      string          `json:"lang"`
	Settings  engine.Settings `json:"settings"`
	State     StatePayload    `json:"state"`
	Extension ExtensionInfo   `json:"extension"`
	Defaults  []string        `json:"defaultCaptureExts"`
}

type ExtensionInfo struct {
	Dir       string   `json:"dir"`
	ID        string   `json:"id"`
	HostFound bool     `json:"hostFound"`
	Connected bool     `json:"connected"`
	Browsers  []string `json:"browsers"`
}

func (a *App) Init() InitData {
	return InitData{
		Version:   shared.Version,
		Lang:      systemLang(),
		Settings:  a.eng.Settings(),
		State:     a.payload(true),
		Extension: a.Extension(),
		Defaults:  engine.DefaultCaptureExts,
	}
}

func (a *App) Extension() ExtensionInfo {
	seen := a.extSeen.Load()
	browsers := installedBrowsers()
	if browsers == nil {
		browsers = []string{}
	}
	return ExtensionInfo{
		Dir:       a.extDir,
		ID:        shared.ExtensionID,
		HostFound: a.hostFound,
		Connected: seen > 0 && time.Now().Unix()-seen < 120,
		Browsers:  browsers,
	}
}

// Add queues one download per non-empty line of urls and reports how many
// lines were not valid http(s) links.
func (a *App) Add(urls string, dir string) (int, error) {
	bad, ok := 0, 0
	for _, line := range strings.Split(urls, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if _, err := a.eng.Add(engine.AddRequest{URL: line, Dir: dir, Source: "manual"}); err != nil {
			bad++
		} else {
			ok++
		}
	}
	if ok == 0 && bad > 0 {
		return bad, errors.New("bad_url")
	}
	return bad, nil
}

func (a *App) Pause(id string)  { a.eng.Pause(id) }
func (a *App) Resume(id string) { a.eng.Resume(id) }
func (a *App) PauseAll()        { a.eng.PauseAll() }
func (a *App) ResumeAll()       { a.eng.ResumeAll() }
func (a *App) ClearFinished()   { a.eng.ClearFinished() }

func (a *App) Remove(id string, deleteFile bool) { a.eng.Remove(id, deleteFile) }

func (a *App) OpenFile(id string) error {
	s, ok := a.eng.Get(id)
	if !ok || s.State != engine.StateCompleted {
		return errors.New("not_ready")
	}
	if _, err := os.Stat(s.Path); err != nil {
		return errors.New("file_missing")
	}
	return shellOpen(s.Path)
}

func (a *App) ShowInFolder(id string) error {
	s, ok := a.eng.Get(id)
	if !ok {
		return errors.New("not_found")
	}
	p := s.Path
	if s.State != engine.StateCompleted {
		if _, err := os.Stat(p + ".hfdm"); err == nil {
			p += ".hfdm"
		}
	}
	return revealInFolder(p)
}

func (a *App) ChooseFolder(current string) string {
	dir, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{DefaultDirectory: current})
	if err != nil {
		return ""
	}
	return dir
}

func (a *App) SaveSettings(s engine.Settings) engine.Settings {
	s = a.eng.SetSettings(s)
	_ = setAutostart(s.Autostart)
	a.applyTheme(s.Theme)
	refreshTray(a.lang())
	return s
}

// ClipboardURL returns the clipboard text when it is a single http(s) link.
func (a *App) ClipboardURL() string {
	text, err := runtime.ClipboardGetText(a.ctx)
	if err != nil {
		return ""
	}
	text = strings.TrimSpace(text)
	if len(text) > 4096 || strings.ContainsAny(text, " \r\n\t") || !isHTTPURL(text) {
		return ""
	}
	return text
}

func (a *App) CopyText(text string) { _ = runtime.ClipboardSetText(a.ctx, text) }

func (a *App) OpenExtensionFolder() error {
	if a.extDir == "" {
		return errors.New("not_found")
	}
	return shellOpen(a.extDir)
}

func (a *App) OpenExtensionsPage(browser string) error { return openExtensionsPage(browser) }

func (a *App) OpenDownloadFolder() error {
	dir := a.eng.Settings().DownloadDir
	_ = os.MkdirAll(dir, 0o755)
	return shellOpen(filepath.Clean(dir))
}

func (a *App) Quit() { runtime.Quit(a.ctx) }
