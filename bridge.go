package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"hukukfixdm/internal/engine"
	"hukukfixdm/internal/shared"
)

// The bridge is a loopback-only HTTP API for the browser extension.
//
// Every request must carry the access token, which the extension can only
// obtain through the native messaging host (and browsers only let our
// extension ID talk to that host). Ordinary web pages can therefore neither
// read nor guess it, and the Host check defeats DNS rebinding.

const preferredPort = 47821

type bridge struct {
	app   *App
	token string
	port  int
	srv   *http.Server
}

func loadToken(dataDir string) string {
	p := filepath.Join(dataDir, "bridge.key")
	if b, err := os.ReadFile(p); err == nil && len(strings.TrimSpace(string(b))) == 64 {
		return strings.TrimSpace(string(b))
	}
	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	tok := hex.EncodeToString(raw)
	_ = os.WriteFile(p, []byte(tok), 0o600)
	return tok
}

func startBridge(app *App, dataDir string) (*bridge, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", preferredPort))
	if err != nil {
		if ln, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
			return nil, err
		}
	}
	b := &bridge{app: app, token: loadToken(dataDir), port: ln.Addr().(*net.TCPAddr).Port}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/status", b.guard(b.status))
	mux.HandleFunc("/v1/download", b.guard(b.download))
	mux.HandleFunc("/v1/downloads", b.guard(b.downloads))
	mux.HandleFunc("/v1/action", b.guard(b.action))
	mux.HandleFunc("/v1/app/quit", b.guard(b.quit))
	b.srv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
	go func() { _ = b.srv.Serve(ln) }()
	err = shared.WriteBridgeInfo(shared.BridgeInfo{Port: b.port, Token: b.token, PID: os.Getpid(), Version: shared.Version})
	return b, err
}

func (b *bridge) close() {
	if b != nil && b.srv != nil {
		_ = b.srv.Close()
	}
}

func extensionOrigin(o string) bool {
	return strings.HasPrefix(o, "chrome-extension://") || strings.HasPrefix(o, "moz-extension://")
}

func (b *bridge) guard(h func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if host != "127.0.0.1:"+strconv.Itoa(b.port) && host != "localhost:"+strconv.Itoa(b.port) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		origin := r.Header.Get("Origin")
		if origin != "" && !extensionOrigin(origin) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		}
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-HFDM-Token")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-HFDM-Token")), []byte(b.token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		w.Header().Set("Cache-Control", "no-store")
		h(w, r)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

// quit is used by a newer HukukFixDM.exe that is installing itself over this one. The native messaging host
// never sends the X-HFDM-Internal header and web pages cannot know the token, so neither the extension nor a
// page can close the app this way.
func (b *bridge) quit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.Header.Get("Origin") != "" || r.Header.Get("X-HFDM-Internal") != "quit" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
	go func() {
		time.Sleep(150 * time.Millisecond)
		b.app.Quit()
	}()
}

func (b *bridge) status(w http.ResponseWriter, r *http.Request) {
	s := b.app.eng.Settings()
	active, speed := b.app.eng.Stats()
	b.app.extensionSeen()
	out := map[string]any{
		"ok": true, "app": shared.AppID, "version": shared.Version,
		"active": active, "speed": speed,
		"capture": s.Capture, "captureExts": s.CaptureExts, "captureMinKB": s.CaptureMinKB,
	}
	if r.URL.Query().Get("items") == "1" { // the popup wants everything in one round trip
		out["items"] = b.slimList()
	}
	writeJSON(w, out)
}

func (b *bridge) download(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		engine.AddRequest
		Auto bool  `json:"auto"` // taken over from the browser, not asked for explicitly
		Size int64 `json:"size"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	b.app.extensionSeen()
	// The app has the final word on what it takes over: the extension only
	// works from a copy of these rules, which may be out of date.
	if s := b.app.eng.Settings(); req.Auto && !engine.Captures(s, req.FileName, req.URL, req.Size) {
		writeJSON(w, map[string]any{
			"ok": false, "declined": true,
			"capture": s.Capture, "captureExts": s.CaptureExts, "captureMinKB": s.CaptureMinKB,
		})
		return
	}
	req.Dir = "" // the browser never chooses where files go
	req.Source = "browser"
	snap, err := b.app.eng.Add(req.AddRequest)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	b.app.captured(snap)
	writeJSON(w, map[string]any{"ok": true, "id": snap.ID, "fileName": snap.FileName})
}

type slimDownload struct {
	ID       string       `json:"id"`
	FileName string       `json:"fileName"`
	State    engine.State `json:"state"`
	Total    int64        `json:"total"`
	Received int64        `json:"received"`
	Speed    float64      `json:"speed"`
	ETA      int64        `json:"eta"`
	Error    string       `json:"error,omitempty"`
}

func (b *bridge) slimList() []slimDownload {
	list := b.app.eng.List()
	if len(list) > 30 {
		list = list[:30]
	}
	out := make([]slimDownload, 0, len(list))
	for _, s := range list {
		out = append(out, slimDownload{s.ID, s.FileName, s.State, s.Total, s.Received, s.Speed, s.ETA, s.ErrorCode})
	}
	return out
}

func (b *bridge) downloads(w http.ResponseWriter, r *http.Request) {
	active, speed := b.app.eng.Stats()
	writeJSON(w, map[string]any{"ok": true, "items": b.slimList(), "active": active, "speed": speed})
}

func (b *bridge) action(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID     string `json:"id"`
		Action string `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var err error
	switch req.Action {
	case "pause":
		b.app.eng.Pause(req.ID)
	case "resume":
		b.app.eng.Resume(req.ID)
	case "remove":
		b.app.eng.Remove(req.ID, false)
	case "open":
		err = b.app.OpenFile(req.ID)
	case "folder":
		err = b.app.ShowInFolder(req.ID)
	case "show":
		b.app.showWindow()
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}
