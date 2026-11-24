package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type item struct {
	mu  sync.Mutex
	rec Record

	running bool
	intent  string // "", "pause" or "remove": why the transfer is being cancelled
	cancel  context.CancelFunc
	file    *os.File
	conns   int
	noRange bool // the server proved unreliable with Range requests

	speed     float64
	lastBytes int64
	lastTick  time.Time
}

// Engine owns every download and schedules them.
type Engine struct {
	mu       sync.Mutex
	items    map[string]*item
	order    []string // newest first
	settings Settings
	closed   bool

	dataDir string
	client  *http.Client
	lim     *limiter

	saveMu sync.Mutex
	dirty  bool
	urgent bool // a structural change happened: persist on the next tick
	stop   chan struct{}
	wg     sync.WaitGroup // transfers
	bg     sync.WaitGroup // ticker

	// OnChange fires (possibly very often) whenever any snapshot changed.
	OnChange func()
	// OnFinished fires once when a download completes or fails.
	OnFinished func(Snapshot)
}

// New loads settings and the download list from dataDir.
func New(dataDir string) (*Engine, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	e := &Engine{
		items:    map[string]*item{},
		settings: defaultSettings(),
		dataDir:  dataDir,
		lim:      &limiter{},
		stop:     make(chan struct{}),
	}
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true) // one TCP connection per segment is the whole point
	e.client = &http.Client{Transport: &http.Transport{
		Proxy:                 systemProxy,
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		MaxIdleConns:          128,
		MaxIdleConnsPerHost:   32,
		IdleConnTimeout:       60 * time.Second,
		DisableCompression:    true,
		Protocols:             protocols,
		ReadBufferSize:        64 << 10,
	}}

	if b, err := os.ReadFile(filepath.Join(dataDir, "settings.json")); err == nil {
		_ = json.Unmarshal(b, &e.settings)
	}
	e.settings.normalize()
	e.lim.setRate(e.settings.SpeedLimitKB * 1024)

	var recs []Record
	if b, err := os.ReadFile(filepath.Join(dataDir, "downloads.json")); err == nil {
		if err := json.Unmarshal(b, &recs); err != nil {
			// Keep the unreadable file for inspection instead of silently overwriting it.
			_ = os.Rename(filepath.Join(dataDir, "downloads.json"), filepath.Join(dataDir, "downloads.corrupt.json"))
			recs = nil
		}
	}
	for i := range recs {
		r := recs[i]
		if r.ID == "" || e.items[r.ID] != nil {
			continue
		}
		// Transfers that were running when the app exited continue automatically.
		if r.State == StateConnecting || r.State == StateDownloading {
			r.State = StateQueued
		}
		e.items[r.ID] = &item{rec: r}
		e.order = append(e.order, r.ID)
	}
	return e, nil
}

// Start begins scheduling. Call once after the callbacks are set.
func (e *Engine) Start() {
	e.bg.Add(1)
	go e.tick()
	e.schedule()
}

// Close stops every transfer, keeping its progress, and saves the state.
func (e *Engine) Close() {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return
	}
	e.closed = true
	for _, it := range e.items {
		it.mu.Lock()
		if it.cancel != nil {
			it.cancel()
		}
		it.mu.Unlock()
	}
	e.mu.Unlock()
	close(e.stop)
	e.wg.Wait()
	e.bg.Wait()
	e.save()
}

func (e *Engine) changed() {
	e.saveMu.Lock()
	e.dirty = true
	e.saveMu.Unlock()
	if f := e.OnChange; f != nil {
		f()
	}
}

// committed is changed() for events that must survive a crash right away:
// a download was added, removed, renamed, stopped or finished.
func (e *Engine) committed() {
	e.saveMu.Lock()
	e.urgent = true
	e.saveMu.Unlock()
	e.changed()
}

// ---- settings ----

func (e *Engine) Settings() Settings {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := e.settings
	s.CaptureExts = append([]string(nil), s.CaptureExts...)
	return s
}

func (e *Engine) SetSettings(s Settings) Settings {
	s.normalize()
	exts := make([]string, 0, len(s.CaptureExts))
	seen := map[string]bool{}
	for _, x := range s.CaptureExts {
		x = strings.ToLower(strings.Trim(strings.TrimSpace(x), "."))
		if x != "" && !seen[x] && len(x) <= 12 {
			seen[x] = true
			exts = append(exts, x)
		}
	}
	s.CaptureExts = exts
	e.mu.Lock()
	e.settings = s
	e.mu.Unlock()
	e.lim.setRate(s.SpeedLimitKB * 1024)
	e.saveSettings()
	e.schedule()
	e.changed()
	return s
}

func (e *Engine) saveSettings() {
	s := e.Settings()
	if b, err := json.MarshalIndent(s, "", "  "); err == nil {
		_ = writeAtomic(filepath.Join(e.dataDir, "settings.json"), b)
	}
}

// ---- public operations ----

// Add queues a new download. If the same URL is already in progress the
// existing download is returned (and resumed when it was stopped).
func (e *Engine) Add(req AddRequest) (Snapshot, error) {
	raw := strings.TrimSpace(req.URL)
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return Snapshot{}, &codeError{code: "bad_url", detail: raw}
	}
	raw = u.String()

	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return Snapshot{}, errors.New("engine closed")
	}
	for _, id := range e.order {
		it := e.items[id]
		it.mu.Lock()
		same := it.rec.URL == raw && it.rec.State != StateCompleted
		state := it.rec.State
		it.mu.Unlock()
		if same {
			e.mu.Unlock()
			if state == StatePaused || state == StateFailed {
				e.Resume(id)
			}
			return e.snapshotOf(it), nil
		}
	}

	hint := req.FileName
	if i := strings.LastIndexAny(hint, `/\`); i >= 0 {
		hint = hint[i+1:]
	}
	hint = sanitizeFileName(hint)
	display := hint
	if display == "" {
		display = nameFromURL(raw)
	}
	if display == "" {
		display = u.Hostname()
	}
	id := newID()
	it := &item{rec: Record{
		ID:        id,
		URL:       raw,
		FileName:  display,
		Dir:       strings.TrimSpace(req.Dir),
		Total:     -1,
		State:     StateQueued,
		Referer:   req.Referer,
		Cookie:    req.Cookie,
		UserAgent: req.UserAgent,
		NameHint:  hint, // a caller-supplied name outranks the server's
		Kind:      kindOf(req.Kind, raw),
		Quality:   max(req.Quality, 0),
		MimeHint:  baseMime(req.Mime),
		Source:    req.Source,
		CreatedAt: nowMs(),
	}}
	e.items[id] = it
	e.order = append([]string{id}, e.order...)
	e.mu.Unlock()

	e.schedule()
	e.committed()
	return e.snapshotOf(it), nil
}

// Pause stops a download but keeps what was received.
func (e *Engine) Pause(id string) {
	it := e.get(id)
	if it == nil {
		return
	}
	it.mu.Lock()
	switch it.rec.State {
	case StateQueued, StateConnecting, StateDownloading:
		it.rec.State = StatePaused
		if it.running && it.cancel != nil {
			it.intent = "pause"
			it.cancel()
		}
	}
	it.mu.Unlock()
	e.committed()
	e.schedule()
}

// Resume continues a paused download or retries a failed one.
func (e *Engine) Resume(id string) {
	it := e.get(id)
	if it == nil {
		return
	}
	it.mu.Lock()
	if it.rec.State == StatePaused || it.rec.State == StateFailed {
		it.rec.State = StateQueued
		it.rec.ErrorCode, it.rec.ErrorDetail = "", ""
	}
	it.mu.Unlock()
	e.committed()
	e.schedule()
}

// Remove forgets a download. Unfinished data is always deleted; the finished
// file is deleted only when deleteFile is set.
func (e *Engine) Remove(id string, deleteFile bool) {
	e.mu.Lock()
	it := e.items[id]
	if it == nil {
		e.mu.Unlock()
		return
	}
	delete(e.items, id)
	for i, x := range e.order {
		if x == id {
			e.order = append(e.order[:i], e.order[i+1:]...)
			break
		}
	}
	e.mu.Unlock()

	it.mu.Lock()
	running := it.running
	if running {
		it.intent = "remove"
		if it.cancel != nil {
			it.cancel()
		}
	}
	rec := it.rec
	it.mu.Unlock()

	if !running {
		if rec.State == StateCompleted {
			if deleteFile {
				_ = os.Remove(filepath.Join(rec.Dir, rec.FileName))
			}
		} else if rec.NameFixed {
			removeParts(&rec)
		}
	}
	e.committed()
	e.schedule()
}

func (e *Engine) PauseAll() {
	for _, id := range e.ids() {
		e.Pause(id)
	}
}

func (e *Engine) ResumeAll() {
	for _, id := range e.ids() {
		if it := e.get(id); it != nil {
			it.mu.Lock()
			paused := it.rec.State == StatePaused
			it.mu.Unlock()
			if paused {
				e.Resume(id)
			}
		}
	}
}

// ClearFinished removes completed downloads from the list (files stay).
func (e *Engine) ClearFinished() {
	for _, id := range e.ids() {
		if it := e.get(id); it != nil {
			it.mu.Lock()
			done := it.rec.State == StateCompleted
			it.mu.Unlock()
			if done {
				e.Remove(id, false)
			}
		}
	}
}

// List returns every download, newest first.
func (e *Engine) List() []Snapshot {
	e.mu.Lock()
	its := make([]*item, 0, len(e.order))
	for _, id := range e.order {
		its = append(its, e.items[id])
	}
	e.mu.Unlock()
	out := make([]Snapshot, 0, len(its))
	for _, it := range its {
		out = append(out, e.snapshotOf(it))
	}
	return out
}

// Get returns one download.
func (e *Engine) Get(id string) (Snapshot, bool) {
	it := e.get(id)
	if it == nil {
		return Snapshot{}, false
	}
	return e.snapshotOf(it), true
}

// Stats reports the number of running transfers and their combined speed.
func (e *Engine) Stats() (active int, speed float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, it := range e.items {
		it.mu.Lock()
		if it.running && it.intent == "" {
			active++
			speed += it.speed
		}
		it.mu.Unlock()
	}
	return
}

func (e *Engine) get(id string) *item {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.items[id]
}

func (e *Engine) ids() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.order...)
}

func (e *Engine) snapshotOf(it *item) Snapshot {
	e.mu.Lock()
	baseDir := e.settings.DownloadDir
	e.mu.Unlock()

	it.mu.Lock()
	defer it.mu.Unlock()
	r := &it.rec
	dir := r.Dir
	if dir == "" {
		dir = baseDir
	}
	s := Snapshot{
		ID: r.ID, URL: r.URL, FileName: r.FileName, Dir: dir,
		Path:     filepath.Join(dir, r.FileName),
		Category: CategoryOf(r.FileName),
		Kind:     r.Kind,
		Total:    r.Total, Received: r.Received, State: r.State,
		ErrorCode: r.ErrorCode, ErrorDetail: r.ErrorDetail,
		Conns: it.conns, Resumable: r.Resumable, Source: r.Source,
		CreatedAt: r.CreatedAt, FinishedAt: r.FinishedAt, ActiveMs: r.ActiveMs,
		ETA: -1,
	}
	if u, err := url.Parse(r.URL); err == nil {
		s.Host = u.Hostname()
	}
	if it.running && it.intent == "" {
		s.Speed = it.speed
		if it.speed > 1 && r.Total > 0 {
			s.ETA = int64(float64(r.Total-r.Received) / it.speed)
		}
	}
	if r.State != StateCompleted && len(r.Segments) > 0 {
		s.Segments = make([][3]int64, len(r.Segments))
		for i, sg := range r.Segments {
			s.Segments[i] = [3]int64{sg.Start, sg.End, sg.Pos}
		}
	}
	return s
}

// ---- scheduling ----

func (e *Engine) schedule() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return
	}
	active := 0
	for _, it := range e.items {
		it.mu.Lock()
		if it.running && it.intent == "" {
			active++
		}
		it.mu.Unlock()
	}
	// Oldest queued download first.
	for i := len(e.order) - 1; i >= 0 && active < e.settings.MaxActive; i-- {
		it := e.items[e.order[i]]
		it.mu.Lock()
		if it.rec.State == StateQueued && !it.running {
			ctx, cancel := context.WithCancel(context.Background())
			it.running = true
			it.intent = ""
			it.cancel = cancel
			it.rec.State = StateConnecting
			it.rec.ErrorCode, it.rec.ErrorDetail = "", ""
			it.lastBytes = it.rec.Received
			it.lastTick = time.Now()
			it.speed = 0
			active++
			e.wg.Add(1)
			go e.run(ctx, it, e.settings.MaxConnections)
		}
		it.mu.Unlock()
	}
}

func (e *Engine) run(ctx context.Context, it *item, maxConns int) {
	defer e.wg.Done()
	err := e.download(ctx, it, maxConns)

	it.mu.Lock()
	cancel := it.cancel
	intent := it.intent
	it.running = false
	it.cancel = nil
	it.intent = ""
	it.conns = 0
	it.speed = 0
	finished := false
	switch {
	case intent == "remove" || intent == "pause":
		// The state was already set by the caller.
	case err != nil && ctx.Err() != nil:
		// The engine is closing: leave the state so the transfer resumes next launch.
	case err == nil:
		it.rec.State = StateCompleted
		it.rec.FinishedAt = nowMs()
		it.rec.Segments = nil
		it.rec.Cookie = "" // credentials are not kept longer than needed
		finished = true
	default:
		it.rec.State = StateFailed
		it.rec.ErrorCode, it.rec.ErrorDetail = classify(err)
		finished = true
	}
	rec := it.rec
	it.mu.Unlock()
	if cancel != nil {
		cancel()
	}

	if intent == "remove" && rec.NameFixed {
		removeParts(&rec)
	}
	e.committed()
	if finished {
		if f := e.OnFinished; f != nil {
			f(e.snapshotOf(it))
		}
	}
	e.schedule()
}

// tick measures speed twice a second and persists progress periodically.
func (e *Engine) tick() {
	defer e.bg.Done()
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	n := 0
	for {
		select {
		case <-e.stop:
			return
		case <-t.C:
		}
		n++
		e.mu.Lock()
		its := make([]*item, 0, len(e.items))
		for _, it := range e.items {
			its = append(its, it)
		}
		e.mu.Unlock()

		any := false
		now := time.Now()
		for _, it := range its {
			it.mu.Lock()
			if it.running {
				any = true
				dt := now.Sub(it.lastTick).Seconds()
				if dt > 0.05 {
					inst := float64(it.rec.Received-it.lastBytes) / dt
					if inst < 0 {
						inst = 0
					}
					if it.speed == 0 {
						it.speed = inst
					} else {
						it.speed = it.speed*0.7 + inst*0.3
					}
					if it.rec.State == StateDownloading {
						it.rec.ActiveMs += int64(dt * 1000)
					}
					it.lastBytes = it.rec.Received
					it.lastTick = now
				}
			}
			it.mu.Unlock()
		}
		if any {
			e.changed()
		}
		e.saveMu.Lock()
		dirty, urgent := e.dirty, e.urgent
		e.saveMu.Unlock()
		// Progress is checkpointed every 2 s, so a crash loses at most that much.
		if dirty && (urgent || n%4 == 0 || !any) {
			e.save()
		}
	}
}

// save writes the download list. Segment positions are captured before the
// part files are flushed, so the saved positions never run ahead of the disk.
func (e *Engine) save() {
	e.saveMu.Lock()
	defer e.saveMu.Unlock()
	e.dirty, e.urgent = false, false

	e.mu.Lock()
	its := make([]*item, 0, len(e.order))
	for _, id := range e.order {
		its = append(its, e.items[id])
	}
	e.mu.Unlock()

	recs := make([]Record, 0, len(its))
	var files []*os.File
	for _, it := range its {
		it.mu.Lock()
		compact(&it.rec)
		r := it.rec
		r.Segments = make([]*Segment, len(it.rec.Segments))
		for i, s := range it.rec.Segments {
			c := *s
			r.Segments[i] = &c
		}
		if it.file != nil {
			files = append(files, it.file)
		}
		it.mu.Unlock()
		recs = append(recs, r)
	}
	for _, f := range files {
		_ = f.Sync()
	}
	if b, err := json.Marshal(recs); err == nil {
		_ = writeAtomic(filepath.Join(e.dataDir, "downloads.json"), b)
	}
}

func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func partPath(r *Record) string {
	return filepath.Join(r.Dir, r.FileName) + ".hfdm"
}

// ---- errors ----

type codeError struct {
	code   string
	detail string
}

func (e *codeError) Error() string { return e.code + ": " + e.detail }

type httpError struct{ status int }

func (e *httpError) Error() string { return fmt.Sprintf("HTTP %d", e.status) }

func (e *httpError) throttle() bool {
	switch e.status {
	case 408, 425, 429, 500, 502, 503, 504, 509:
		return true
	}
	return false
}

// restartError means the data on disk can no longer be trusted (the remote
// file changed, or the server stopped honouring ranges): start over.
type restartError struct{ noRange bool }

func (e *restartError) Error() string { return "remote file changed" }

func classify(err error) (code, detail string) {
	var ce *codeError
	var he *httpError
	var re *restartError
	switch {
	case errors.As(err, &ce):
		return ce.code, ce.detail
	case errors.As(err, &he):
		return "http", fmt.Sprint(he.status)
	case errors.As(err, &re):
		return "changed", ""
	case isDiskFull(err):
		return "disk_full", ""
	}
	var pe *os.PathError
	if errors.As(err, &pe) {
		return "disk", pe.Error()
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return "dns", dns.Name
	}
	msg := err.Error()
	if len(msg) > 300 {
		msg = msg[:300]
	}
	return "network", msg
}
