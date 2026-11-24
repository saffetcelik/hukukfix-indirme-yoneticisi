//go:build e2e

// Test-only hook for tools/e2e: runs JavaScript in the window and returns its result over a localhost port.
// It is compiled only with -tags e2e; release builds (build.ps1) do not contain it. WebView2's remote debugging
// port cannot be used instead, because the WebView2 loader Wails uses clears WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"hukukfixdm/internal/update"
)

var (
	e2eSeq     atomic.Int64
	e2eMu      sync.Mutex
	e2eWaiting = map[string]chan string{}
)

func init() {
	// The test build runs only under tools/e2e. Started any other way (for instance by a real browser through a
	// host the test registered for a moment) it would work on the user's real data folder, so it refuses.
	if os.Getenv("HFDM_E2E_PORT") == "" && installSteps {
		os.Exit(3)
	}
	e2eHook = startE2EHook
	instanceID += "-e2e"
	// A test build never talks to GitHub and never touches the real desktop or Start menu: the updater uses the
	// test server and throwaway key tools/e2e passes in, shortcuts go to a folder of the test (or nowhere).
	update.LatestURL = os.Getenv("HFDM_UPDATE_URL")
	update.PublicKey = os.Getenv("HFDM_UPDATE_PUBKEY")
	update.AllowHTTP = true
	if ms, err := strconv.Atoi(os.Getenv("HFDM_UPDATE_FIRST")); err == nil {
		firstCheckDelay = time.Duration(ms) * time.Millisecond
	}
	if ms, err := strconv.Atoi(os.Getenv("HFDM_UPDATE_RETRY")); err == nil {
		retryDelay = time.Duration(ms) * time.Millisecond
	}
	dir := os.Getenv("HFDM_SHORTCUT_DIR")
	shortcutDirs = func() (string, string) {
		if dir == "" {
			return "", ""
		}
		return filepath.Join(dir, "Masaustu"), filepath.Join(dir, "Programlar")
	}
}

func startE2EHook(a *App) {
	port := os.Getenv("HFDM_E2E_PORT")
	if port == "" {
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/js", func(w http.ResponseWriter, r *http.Request) {
		expr, _ := io.ReadAll(r.Body)
		id := strconv.FormatInt(e2eSeq.Add(1), 10)
		ch := make(chan string, 1)
		e2eMu.Lock()
		e2eWaiting[id] = ch
		e2eMu.Unlock()
		js := fmt.Sprintf(`(async () => { let v, e; try { v = await (async () => (%s))(); } catch (x) { e = String((x && x.stack) || x); }
			window.go.main.App.E2EResult(%q, JSON.stringify({ v: v === undefined ? null : v, e: e || null })); })()`, expr, id)
		wait := 20 * time.Second
		if ms, err := strconv.Atoi(r.URL.Query().Get("t")); err == nil && ms > 0 && ms < 20000 {
			wait = time.Duration(ms) * time.Millisecond
		}
		runtime.WindowExecJS(a.ctx, js)
		select {
		case res := <-ch:
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, res)
		case <-time.After(wait):
			http.Error(w, "timeout", http.StatusGatewayTimeout)
		}
		e2eMu.Lock()
		delete(e2eWaiting, id)
		e2eMu.Unlock()
	})
	go func() { _ = http.ListenAndServe("127.0.0.1:"+port, mux) }()
}

// E2EResult receives the value of a script started by the e2e hook.
func (a *App) E2EResult(id, result string) {
	e2eMu.Lock()
	ch := e2eWaiting[id]
	e2eMu.Unlock()
	if ch != nil && json.Valid([]byte(result)) {
		ch <- result
	}
}
