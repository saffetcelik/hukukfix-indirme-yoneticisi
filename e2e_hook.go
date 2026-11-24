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
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

var (
	e2eSeq     atomic.Int64
	e2eMu      sync.Mutex
	e2eWaiting = map[string]chan string{}
)

func init() { e2eHook = startE2EHook }

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
		runtime.WindowExecJS(a.ctx, js)
		select {
		case res := <-ch:
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, res)
		case <-time.After(20 * time.Second):
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
