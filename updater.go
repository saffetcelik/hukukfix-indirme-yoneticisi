package main

// Automatic updates. The app asks GitHub for the latest release 10 seconds after starting and every six hours,
// 10 minutes later when a check could not reach GitHub (when "Update automatically" is on, or when asked from Settings), downloads HukukFixDM.exe and its signed
// manifest into <data>\update, and installs it only if the signature, version and SHA-256 all check out (see
// internal/update). A verified update is installed when the user clicks "Restart" or on the next start.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"hukukfixdm/internal/engine"
	"hukukfixdm/internal/shared"
	"hukukfixdm/internal/update"
)

const (
	checkInterval = 6 * time.Hour
	maxUpdateSize = 200 << 20
)

// Variables only so the test build can shorten them.
var (
	// firstCheckDelay leaves the start itself alone and gives a network that comes up with Windows a moment.
	firstCheckDelay = 10 * time.Second
	// retryDelay follows a check that could not finish (no network yet, download cut off), so a start without
	// internet does not postpone the update by six hours.
	retryDelay = 10 * time.Minute
)

// UpdateInfo is what the window shows. State: idle, checking, downloading, latest, ready or error.
type UpdateInfo struct {
	State   string `json:"state"`
	Version string `json:"version,omitempty"`
	Notes   bool   `json:"notes,omitempty"` // a release page can be opened
	Error   string `json:"error,omitempty"` // network, signature, mismatch, no_asset, disk
}

type updater struct {
	app  *App
	dir  string
	busy atomic.Bool
	kick chan struct{}

	mu       sync.Mutex
	info     UpdateInfo
	notesURL string
	notified string
}

func updateDir(dataDir string) string { return filepath.Join(dataDir, "update") }

func newUpdater(app *App, dataDir string) *updater {
	return &updater{app: app, dir: updateDir(dataDir), kick: make(chan struct{}, 1), info: UpdateInfo{State: "idle"}}
}

func (u *updater) start() {
	if update.LatestURL == "" || !installSteps {
		return
	}
	// Downloaded in an earlier session but not installed yet (another copy was running at the last start).
	if m, notes, err := readStaged(u.dir); err == nil {
		u.mu.Lock()
		u.info, u.notesURL = UpdateInfo{State: "ready", Version: m.Version, Notes: notes != ""}, notes
		u.mu.Unlock()
	}
	go u.loop()
}

func (u *updater) loop() {
	wait := firstCheckDelay
	for {
		select {
		case <-time.After(wait):
		case <-u.kick:
		}
		wait = checkInterval
		if u.app.eng.Settings().AutoUpdate {
			u.check()
			if st := u.current(); st.State == "error" && retryable(st.Error) {
				wait = retryDelay
			}
		}
	}
}

// retryable failures are about this computer's connection or disk; a release that fails its signature check is
// not retried before the next regular check.
func retryable(code string) bool { return code == "network" || code == "mismatch" || code == "disk" }

// checkSoon runs a check now (used when automatic updates are switched on).
func (u *updater) checkSoon() {
	select {
	case u.kick <- struct{}{}:
	default:
	}
}

func (u *updater) current() UpdateInfo {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.info
}

func (u *updater) set(info UpdateInfo) {
	u.mu.Lock()
	u.info = info
	u.mu.Unlock()
	if u.app.ctx != nil {
		runtime.EventsEmit(u.app.ctx, "update", info)
	}
}

// fail reports a failed check, unless an update downloaded earlier is still waiting to be installed.
func (u *updater) fail(code string) {
	if m, notes, err := readStaged(u.dir); err == nil {
		u.ready(m.Version, notes)
		return
	}
	u.set(UpdateInfo{State: "error", Error: code})
}

// check asks GitHub for the latest release and stages it when it is newer and properly signed.
func (u *updater) check() {
	if update.LatestURL == "" || !installSteps || !u.busy.CompareAndSwap(false, true) {
		return
	}
	defer u.busy.Store(false)
	if u.current().State != "ready" { // keep the "restart" banner up while checking again
		u.set(UpdateInfo{State: "checking"})
	}

	var rel update.Release
	body, err := u.fetch(update.LatestURL, 1<<20, 30*time.Second)
	if err != nil || json.Unmarshal(body, &rel) != nil {
		u.fail("network")
		return
	}
	tag := update.Clean(rel.TagName)
	if rel.Draft || rel.Prerelease || update.Compare(tag, shared.Version) <= 0 {
		os.RemoveAll(u.dir) // a staged update older than what runs now is useless
		u.set(UpdateInfo{State: "latest"})
		return
	}
	notes := ""
	if strings.HasPrefix(rel.HTMLURL, update.ReleasesPage) {
		notes = rel.HTMLURL
	}
	if m, _, err := readStaged(u.dir); err == nil && update.Compare(m.Version, tag) >= 0 {
		u.ready(m.Version, notes)
		return
	}
	exeAsset, ok1 := rel.Find(update.AssetExe)
	sigAsset, ok2 := rel.Find(update.AssetSig)
	if !ok1 || !ok2 {
		u.fail("no_asset")
		return
	}
	u.set(UpdateInfo{State: "downloading", Version: tag})

	raw, err := u.fetch(sigAsset.URL, 64<<10, 60*time.Second)
	if err != nil {
		u.fail("network")
		return
	}
	var m update.Manifest
	if json.Unmarshal(raw, &m) != nil || m.Accept(update.PublicKey, tag, shared.Version) != nil {
		u.fail("signature")
		return
	}
	if m.Size > maxUpdateSize || (exeAsset.Size > 0 && exeAsset.Size != m.Size) {
		u.fail("mismatch")
		return
	}
	if err := u.download(exeAsset.URL, m, notes); err != nil {
		if errors.Is(err, update.ErrMismatch) {
			u.fail("mismatch")
		} else if errors.Is(err, errDisk) {
			u.fail("disk")
		} else {
			u.fail("network")
		}
		return
	}
	u.ready(m.Version, notes)
}

func (u *updater) ready(version, notes string) {
	u.mu.Lock()
	u.notesURL = notes
	first := u.notified != version
	u.notified = version
	u.mu.Unlock()
	u.set(UpdateInfo{State: "ready", Version: version, Notes: notes != ""})
	if first && u.app.notify && u.app.eng.Settings().Notify {
		t := map[string][2]string{
			"tr": {"Güncelleme hazır", "HukukFix %s indirildi. Yeniden başlatınca kurulur."},
			"en": {"Update ready", "HukukFix %s has been downloaded. It is installed when you restart."},
		}[u.app.lang()]
		_ = runtime.SendNotification(u.app.ctx, runtime.NotificationOptions{ID: "update-" + version, Title: t[0], Body: fmt.Sprintf(t[1], version)})
	}
}

var errDisk = errors.New("disk")

func (u *updater) client(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: &http.Transport{Proxy: engine.SystemProxy, TLSHandshakeTimeout: 15 * time.Second, ResponseHeaderTimeout: 30 * time.Second},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 || !allowedURL(req.URL.String()) {
				return errors.New("redirect refused")
			}
			return nil
		},
	}
}

func allowedURL(u string) bool {
	return strings.HasPrefix(u, "https://") || (update.AllowHTTP && strings.HasPrefix(u, "http://127.0.0.1:"))
}

func (u *updater) request(ctx context.Context, url string) (*http.Request, error) {
	if !allowedURL(url) {
		return nil, errors.New("insecure url")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "HukukFixDM/"+shared.Version)
	req.Header.Set("Accept", "application/vnd.github+json, application/octet-stream;q=0.9, */*;q=0.8")
	return req, nil
}

func (u *updater) fetch(url string, limit int64, timeout time.Duration) ([]byte, error) {
	req, err := u.request(context.Background(), url)
	if err != nil {
		return nil, err
	}
	res, err := u.client(timeout).Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http %d", res.StatusCode)
	}
	return io.ReadAll(io.LimitReader(res.Body, limit))
}

// download saves the new exe next to its manifest. The file is hashed while it arrives and kept only if it is
// exactly the signed one.
func (u *updater) download(url string, m update.Manifest, notes string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	req, err := u.request(ctx, url)
	if err != nil {
		return err
	}
	res, err := u.client(0).Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("http %d", res.StatusCode)
	}
	_ = os.RemoveAll(u.dir)
	if err := os.MkdirAll(u.dir, 0o755); err != nil {
		return errDisk
	}
	part := filepath.Join(u.dir, shared.AppExe+".part")
	f, err := os.Create(part)
	if err != nil {
		return errDisk
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(res.Body, m.Size+1))
	if cerr := f.Close(); err == nil && cerr != nil {
		err = errDisk
	}
	if err == nil {
		err = m.Matches(n, hex.EncodeToString(h.Sum(nil)))
	}
	if err == nil {
		err = os.Rename(part, filepath.Join(u.dir, shared.AppExe))
	}
	if err == nil {
		data, _ := json.MarshalIndent(stagedFile{Manifest: m, Notes: notes}, "", "  ")
		if werr := os.WriteFile(filepath.Join(u.dir, "update.json"), data, 0o644); werr != nil {
			err = errDisk
		}
	}
	if err != nil {
		_ = os.RemoveAll(u.dir)
	}
	return err
}

type stagedFile struct {
	Manifest update.Manifest `json:"manifest"`
	Notes    string          `json:"notes"`
}

// readStaged returns a downloaded update only if it is still a valid, newer, intact release; anything else
// in the folder is thrown away.
func readStaged(dir string) (update.Manifest, string, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "update.json"))
	if err != nil {
		if fileExists(dir) {
			_ = os.RemoveAll(dir)
		}
		return update.Manifest{}, "", err
	}
	var s stagedFile
	err = json.Unmarshal(raw, &s)
	if err == nil {
		err = s.Manifest.Accept(update.PublicKey, s.Manifest.Version, shared.Version)
	}
	if err == nil {
		var data []byte
		if data, err = os.ReadFile(filepath.Join(dir, shared.AppExe)); err == nil {
			sum := sha256.Sum256(data)
			err = s.Manifest.Matches(int64(len(data)), hex.EncodeToString(sum[:]))
		}
	}
	if err != nil {
		_ = os.RemoveAll(dir)
		return update.Manifest{}, "", err
	}
	return s.Manifest, s.Notes, nil
}

// installStaged puts the verified new exe in place of the running one.
func installStaged(dir string) error {
	if _, _, err := readStaged(dir); err != nil {
		return err
	}
	if err := replaceFile(filepath.Join(dir, shared.AppExe), exePath()); err != nil {
		return err
	}
	_ = os.RemoveAll(dir)
	return nil
}

// applyStagedUpdate installs a downloaded update before the window opens and starts the new version in this
// process's place. It does nothing while another copy is running: that one offers the update itself.
func applyStagedUpdate(dataDir string, args []string) bool {
	dir := updateDir(dataDir)
	if !fileExists(dir) || otherInstanceRunning() {
		return false
	}
	if err := installStaged(dir); err != nil {
		return false
	}
	return startDetached(exePath(), args) == nil
}

// ---- methods called from JavaScript ----

// CheckForUpdates starts a check; the result arrives as an "update" event.
func (a *App) CheckForUpdates() {
	if a.upd != nil {
		go a.upd.check()
	}
}

// ApplyUpdate installs the downloaded update and restarts into it.
func (a *App) ApplyUpdate() error {
	if a.upd == nil {
		return errors.New("no_update")
	}
	if err := installStaged(a.upd.dir); err != nil {
		a.upd.fail("mismatch")
		return err
	}
	if err := startDetached(exePath(), []string{"--after=" + strconv.Itoa(os.Getpid())}); err != nil {
		return err // the new version is in place and starts next time
	}
	a.Quit()
	return nil
}

// OpenReleaseNotes opens the GitHub page of the downloaded release.
func (a *App) OpenReleaseNotes() error {
	if a.upd == nil {
		return errors.New("no_update")
	}
	a.upd.mu.Lock()
	notes := a.upd.notesURL
	a.upd.mu.Unlock()
	if !strings.HasPrefix(notes, update.ReleasesPage) {
		return errors.New("no_notes")
	}
	return shellOpen(notes)
}
