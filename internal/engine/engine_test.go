package engine

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeServer struct {
	data      []byte
	ranges    bool
	delay     time.Duration // per 64 KiB, to make transfers observable
	maxConns  int32         // refuse (503) beyond this many parallel requests; 0 = unlimited
	failEvery int32         // cut every n-th ranged response short; 0 = never
	etag      atomic.Value

	cur, peak, reqs int32
	mu              sync.Mutex
}

func (s *fakeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	n := atomic.AddInt32(&s.cur, 1)
	defer atomic.AddInt32(&s.cur, -1)
	for {
		p := atomic.LoadInt32(&s.peak)
		if n <= p || atomic.CompareAndSwapInt32(&s.peak, p, n) {
			break
		}
	}
	if s.maxConns > 0 && n > s.maxConns {
		http.Error(w, "busy", http.StatusServiceUnavailable)
		return
	}
	req := atomic.AddInt32(&s.reqs, 1)
	etag, _ := s.etag.Load().(string)
	if etag != "" {
		w.Header().Set("ETag", etag)
	}
	w.Header().Set("Content-Disposition", `attachment; filename="payload.bin"`)
	start, end := int64(0), int64(len(s.data))-1
	rng := r.Header.Get("Range")
	if s.ranges && rng != "" {
		if ir := r.Header.Get("If-Range"); ir != "" && ir != etag {
			rng = ""
		}
	}
	partial := false
	if s.ranges && strings.HasPrefix(rng, "bytes=") {
		parts := strings.SplitN(strings.TrimPrefix(rng, "bytes="), "-", 2)
		start, _ = strconv.ParseInt(parts[0], 10, 64)
		if parts[1] != "" {
			end, _ = strconv.ParseInt(parts[1], 10, 64)
		}
		if start > end || end >= int64(len(s.data)) {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		partial = true
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(s.data)))
	}
	w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
	if partial {
		w.WriteHeader(http.StatusPartialContent)
	}
	body := s.data[start : end+1]
	cut := s.failEvery > 0 && partial && len(body) > 1 && req%s.failEvery == 0
	if cut {
		body = body[:len(body)/2]
	}
	for len(body) > 0 {
		c := min(len(body), 64<<10)
		if _, err := w.Write(body[:c]); err != nil {
			return
		}
		body = body[c:]
		if s.delay > 0 {
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
			time.Sleep(s.delay)
		}
	}
	if cut {
		panic(http.ErrAbortHandler) // drop the connection mid-body
	}
}

func payload(n int) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(int64(n))).Read(b)
	return b
}

func newTestEngine(t *testing.T) (*Engine, string) {
	t.Helper()
	dir := t.TempDir()
	e, err := New(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	s := e.Settings()
	s.DownloadDir = filepath.Join(dir, "dl")
	e.SetSettings(s)
	e.Start()
	t.Cleanup(e.Close)
	return e, s.DownloadDir
}

func waitState(t *testing.T, e *Engine, id string, want State, d time.Duration) Snapshot {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		s, ok := e.Get(id)
		if ok && s.State == want {
			return s
		}
		if ok && s.State == StateFailed && want != StateFailed {
			t.Fatalf("download failed: %s %s", s.ErrorCode, s.ErrorDetail)
		}
		time.Sleep(20 * time.Millisecond)
	}
	s, _ := e.Get(id)
	t.Fatalf("timeout waiting for %s, state=%s received=%d/%d", want, s.State, s.Received, s.Total)
	return s
}

func checkFile(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("content mismatch: got %d bytes sha %x, want %d bytes sha %x", len(got), sha256.Sum256(got), len(want), sha256.Sum256(want))
	}
}

func TestSegmented(t *testing.T) {
	fs := &fakeServer{data: payload(37<<20 + 12345), ranges: true}
	fs.etag.Store(`"v1"`)
	srv := httptest.NewServer(fs)
	defer srv.Close()
	e, dl := newTestEngine(t)

	snap, err := e.Add(AddRequest{URL: srv.URL + "/x"})
	if err != nil {
		t.Fatal(err)
	}
	done := waitState(t, e, snap.ID, StateCompleted, 30*time.Second)
	if done.FileName != "payload.bin" {
		t.Errorf("name = %q", done.FileName)
	}
	checkFile(t, filepath.Join(dl, "payload.bin"), fs.data)
	if p := atomic.LoadInt32(&fs.peak); p < 2 {
		t.Errorf("expected parallel connections, peak=%d", p)
	}
	if exists(filepath.Join(dl, "payload.bin.hfdm")) {
		t.Error("part file left behind")
	}
	if b, err := os.ReadFile(filepath.Join(dl, "payload.bin") + ":Zone.Identifier"); err != nil || !strings.Contains(string(b), "ZoneId=3") {
		t.Errorf("mark of the web missing: %v", err)
	}

	// The same name again must not overwrite.
	snap2, _ := e.Add(AddRequest{URL: srv.URL + "/y"})
	d2 := waitState(t, e, snap2.ID, StateCompleted, 30*time.Second)
	if d2.FileName != "payload (1).bin" {
		t.Errorf("second name = %q", d2.FileName)
	}
}

func TestPauseResumeAndRestart(t *testing.T) {
	fs := &fakeServer{data: payload(24 << 20), ranges: true, delay: 4 * time.Millisecond}
	fs.etag.Store(`"v1"`)
	srv := httptest.NewServer(fs)
	defer srv.Close()

	dir := t.TempDir()
	e, _ := New(filepath.Join(dir, "data"))
	s := e.Settings()
	s.DownloadDir = filepath.Join(dir, "dl")
	e.SetSettings(s)
	e.Start()

	snap, _ := e.Add(AddRequest{URL: srv.URL + "/f"})
	for i := 0; i < 500; i++ {
		if g, _ := e.Get(snap.ID); g.Received > 4<<20 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.Pause(snap.ID)
	time.Sleep(300 * time.Millisecond)
	p, _ := e.Get(snap.ID)
	if p.State != StatePaused || p.Received == 0 || p.Received >= p.Total {
		t.Fatalf("after pause: state=%s received=%d", p.State, p.Received)
	}
	// Simulate an app restart: close, reopen, the paused download must keep its progress.
	e.Close()
	e2, err := New(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	e2.Start()
	defer e2.Close()
	p2, ok := e2.Get(snap.ID)
	if !ok || p2.State != StatePaused || p2.Received != p.Received {
		t.Fatalf("after reload: ok=%v state=%s received=%d want %d", ok, p2.State, p2.Received, p.Received)
	}
	before := atomic.LoadInt32(&fs.reqs)
	e2.Resume(snap.ID)
	waitState(t, e2, snap.ID, StateCompleted, 60*time.Second)
	checkFile(t, filepath.Join(s.DownloadDir, "payload.bin"), fs.data)
	if atomic.LoadInt32(&fs.reqs) == before {
		t.Error("resume made no requests")
	}
}

func TestChangedFileRestarts(t *testing.T) {
	fs := &fakeServer{data: payload(16 << 20), ranges: true, delay: 4 * time.Millisecond}
	fs.etag.Store(`"v1"`)
	srv := httptest.NewServer(fs)
	defer srv.Close()
	e, dl := newTestEngine(t)

	snap, _ := e.Add(AddRequest{URL: srv.URL + "/f"})
	for i := 0; i < 500; i++ {
		if g, _ := e.Get(snap.ID); g.Received > 2<<20 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.Pause(snap.ID)
	time.Sleep(300 * time.Millisecond)
	// The remote file changes while paused: same size, different content.
	fs.data = payload(16<<20 + 1)[:16<<20]
	fs.etag.Store(`"v2"`)
	fs.delay = 0
	e.Resume(snap.ID)
	waitState(t, e, snap.ID, StateCompleted, 60*time.Second)
	checkFile(t, filepath.Join(dl, "payload.bin"), fs.data)
}

func TestNoRangeServer(t *testing.T) {
	fs := &fakeServer{data: payload(5<<20 + 7), ranges: false}
	srv := httptest.NewServer(fs)
	defer srv.Close()
	e, dl := newTestEngine(t)
	snap, _ := e.Add(AddRequest{URL: srv.URL + "/f", FileName: "custom name.dat"})
	d := waitState(t, e, snap.ID, StateCompleted, 30*time.Second)
	if d.Resumable {
		t.Error("should not be resumable")
	}
	checkFile(t, filepath.Join(dl, "custom name.dat"), fs.data)
	if r := atomic.LoadInt32(&fs.reqs); r != 1 {
		t.Errorf("expected the probe response to be reused, got %d requests", r)
	}
}

func TestConnectionCapAndFlakyServer(t *testing.T) {
	fs := &fakeServer{data: payload(20 << 20), ranges: true, maxConns: 3, failEvery: 4, delay: time.Millisecond}
	fs.etag.Store(`"v1"`)
	srv := httptest.NewServer(fs)
	defer srv.Close()
	e, dl := newTestEngine(t)
	snap, _ := e.Add(AddRequest{URL: srv.URL + "/f"})
	waitState(t, e, snap.ID, StateCompleted, 90*time.Second)
	checkFile(t, filepath.Join(dl, "payload.bin"), fs.data)
}

func TestHTTPErrorAndHTMLGuard(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/missing.zip", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	mux.HandleFunc("/login.zip", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, "<html>please log in</html>")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	e, dl := newTestEngine(t)

	a, _ := e.Add(AddRequest{URL: srv.URL + "/missing.zip"})
	f := waitState(t, e, a.ID, StateFailed, 20*time.Second)
	if f.ErrorCode != "http" || f.ErrorDetail != "404" {
		t.Errorf("error = %s %s", f.ErrorCode, f.ErrorDetail)
	}
	b, _ := e.Add(AddRequest{URL: srv.URL + "/login.zip"})
	g := waitState(t, e, b.ID, StateFailed, 20*time.Second)
	if g.ErrorCode != "html_page" {
		t.Errorf("error = %s", g.ErrorCode)
	}
	if entries, _ := os.ReadDir(dl); len(entries) != 0 {
		t.Errorf("failed downloads left %d files behind", len(entries))
	}
	if _, err := e.Add(AddRequest{URL: "ftp://example.com/x"}); err == nil {
		t.Error("ftp url accepted")
	}
}

func TestSpeedLimit(t *testing.T) {
	fs := &fakeServer{data: payload(3 << 20), ranges: true}
	srv := httptest.NewServer(fs)
	defer srv.Close()
	e, dl := newTestEngine(t)
	s := e.Settings()
	s.SpeedLimitKB = 1024 // 1 MiB/s -> about 3 s
	e.SetSettings(s)
	start := time.Now()
	snap, _ := e.Add(AddRequest{URL: srv.URL + "/f"})
	waitState(t, e, snap.ID, StateCompleted, 30*time.Second)
	if el := time.Since(start); el < 2*time.Second || el > 6*time.Second {
		t.Errorf("3 MiB at 1 MiB/s took %v", el)
	}
	checkFile(t, filepath.Join(dl, "payload.bin"), fs.data)
}

func TestFileNames(t *testing.T) {
	cases := []struct{ in, want string }{
		{`attachment; filename="report.pdf"`, "report.pdf"},
		{`attachment; filename*=UTF-8''%C3%B6zet%20rapor.pdf`, "özet rapor.pdf"},
		{`attachment; filename="..\..\evil.exe"`, "evil.exe"},
		{`attachment; filename=plain.zip`, "plain.zip"},
		{`attachment; filename="a%20b.zip"`, "a b.zip"},
	}
	for _, c := range cases {
		if got := nameFromDisposition(c.in); got != c.want {
			t.Errorf("nameFromDisposition(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := sanitizeFileName(`CON.txt`); got != "_CON.txt" {
		t.Errorf("reserved: %q", got)
	}
	if got := sanitizeFileName(`a<b>:c?.txt. `); got != "a_b__c_.txt" {
		t.Errorf("sanitize: %q", got)
	}
	if got := nameFromURL("https://x.com/dir/My%20File.tar.gz?token=1"); got != "My File.tar.gz" {
		t.Errorf("url name: %q", got)
	}
	if got := chooseFileName("", "", "https://x.com/get", "", "application/pdf"); got != "get.pdf" {
		t.Errorf("mime ext: %q", got)
	}
}

func TestCaptures(t *testing.T) {
	s := defaultSettings()
	s.CaptureMinKB = 100
	cases := []struct {
		name, url string
		size      int64
		want      bool
	}{
		{"setup.exe", "https://x.com/get", 5 << 20, true},
		{"", "https://x.com/files/movie.MP4?sig=1", 0, true},
		{"page.html", "https://x.com/page.html", 5 << 20, false},
		{"small.zip", "https://x.com/small.zip", 50 << 10, false},
		{"noext", "https://x.com/download", 5 << 20, false},
	}
	for _, c := range cases {
		if got := Captures(s, c.name, c.url, c.size); got != c.want {
			t.Errorf("Captures(%q, %q, %d) = %v, want %v", c.name, c.url, c.size, got, c.want)
		}
	}
	s.Capture = false
	if Captures(s, "setup.exe", "", 0) {
		t.Error("capture disabled but still capturing")
	}
}
