package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	minSplit     = 1 << 20   // a connection is only split off for at least this much work
	bufSize      = 256 << 10 // must stay below minSplit, see pickSegment
	stallTimeout = 40 * time.Second
	maxFailures  = 12
	defaultUA    = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
)

// ---- rate limiter ----

// limiter is a token bucket shared by every connection of every download.
type limiter struct {
	mu     sync.Mutex
	rate   float64 // bytes per second, 0 = unlimited
	tokens float64
	last   time.Time
}

func (l *limiter) setRate(bytesPerSec int) {
	l.mu.Lock()
	l.rate = float64(bytesPerSec)
	l.tokens = 0
	l.last = time.Now()
	l.mu.Unlock()
}

// chunk is the read size to use: small reads keep a low limit smooth.
func (l *limiter) chunk() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.rate <= 0 {
		return bufSize
	}
	c := int(l.rate / 8)
	if c < 2048 {
		c = 2048
	}
	if c > bufSize {
		c = bufSize
	}
	return c
}

func (l *limiter) wait(ctx context.Context, n int) {
	l.mu.Lock()
	if l.rate <= 0 {
		l.mu.Unlock()
		return
	}
	now := time.Now()
	l.tokens += now.Sub(l.last).Seconds() * l.rate
	if burst := l.rate / 4; l.tokens > burst {
		l.tokens = burst
	}
	l.last = now
	l.tokens -= float64(n)
	var d time.Duration
	if l.tokens < 0 {
		d = time.Duration(-l.tokens / l.rate * float64(time.Second))
	}
	l.mu.Unlock()
	if d > 0 {
		sleep(ctx, d)
	}
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

func backoff(failures int) time.Duration {
	d := time.Duration(1<<uint(min(failures, 5))) * 500 * time.Millisecond
	if d > 20*time.Second {
		d = 20 * time.Second
	}
	return d
}

// ---- HTTP plumbing ----

// stream is a response body guarded by a stall watchdog: if no byte arrives
// for stallTimeout the request is cancelled and the read fails.
type stream struct {
	resp   *http.Response
	cancel context.CancelFunc
	timer  *time.Timer
}

func (s *stream) Read(p []byte) (int, error) {
	s.timer.Reset(stallTimeout)
	return s.resp.Body.Read(p)
}

func (s *stream) Close() {
	s.timer.Stop()
	s.cancel()
	_ = s.resp.Body.Close()
}

func sameHost(a, b string) bool {
	ua, err1 := url.Parse(a)
	ub, err2 := url.Parse(b)
	return err1 == nil && err2 == nil && strings.EqualFold(ua.Host, ub.Host) && ua.Scheme == ub.Scheme
}

func (e *Engine) open(ctx context.Context, it *item, rawURL, rng, ifRange string) (*stream, error) {
	cctx, cancel := context.WithCancel(ctx)
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, rawURL, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	it.mu.Lock()
	ua, referer, cookie, orig, kind := it.rec.UserAgent, it.rec.Referer, it.rec.Cookie, it.rec.URL, it.rec.Kind
	it.mu.Unlock()
	if ua == "" {
		ua = defaultUA
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Encoding", "identity")
	if referer != "" {
		req.Header.Set("Referer", referer)
		// Stream servers often check where the player lives, like for any page request.
		if u, err := url.Parse(referer); err == nil && kind == "hls" && u.Host != "" && !strings.EqualFold(u.Host, req.URL.Host) {
			req.Header.Set("Origin", u.Scheme+"://"+u.Host)
		}
	}
	// Cookies only ever go to the host the download was requested from.
	if cookie != "" && sameHost(rawURL, orig) {
		req.Header.Set("Cookie", cookie)
	}
	if rng != "" {
		req.Header.Set("Range", rng)
		if ifRange != "" {
			req.Header.Set("If-Range", ifRange)
		}
	}
	timer := time.AfterFunc(stallTimeout, cancel)
	resp, err := e.client.Do(req)
	if err != nil {
		timer.Stop()
		cancel()
		return nil, err
	}
	return &stream{resp: resp, cancel: cancel, timer: timer}, nil
}

// parseContentRange reads "bytes a-b/total"; total is -1 when unknown.
func parseContentRange(h string) (start, end, total int64, ok bool) {
	h = strings.TrimSpace(h)
	if !strings.HasPrefix(strings.ToLower(h), "bytes") {
		return 0, 0, -1, false
	}
	h = strings.TrimSpace(h[5:])
	h = strings.TrimPrefix(h, "=")
	slash := strings.IndexByte(h, '/')
	if slash < 0 {
		return 0, 0, -1, false
	}
	total = -1
	if t, err := strconv.ParseInt(strings.TrimSpace(h[slash+1:]), 10, 64); err == nil {
		total = t
	}
	dash := strings.IndexByte(h[:slash], '-')
	if dash < 0 {
		return 0, 0, total, false
	}
	s, err1 := strconv.ParseInt(strings.TrimSpace(h[:dash]), 10, 64)
	en, err2 := strconv.ParseInt(strings.TrimSpace(h[dash+1:slash]), 10, 64)
	if err1 != nil || err2 != nil {
		return 0, 0, total, false
	}
	return s, en, total, true
}

// ---- probing ----

type probeResult struct {
	total       int64
	ranges      bool
	disposition string
	ctype       string
	finalURL    string
	etag        string
	lastMod     string
	body        *stream // set when the server already started sending the whole file
}

func (p *probeResult) close() {
	if p.body != nil {
		p.body.Close()
		p.body = nil
	}
}

// probe asks for the first byte to learn the size, the name and whether the
// server supports ranges. A server that ignores the Range header answers with
// the complete file, which is then consumed directly instead of re-requested.
func (e *Engine) probe(ctx context.Context, it *item) (*probeResult, error) {
	it.mu.Lock()
	raw := it.rec.URL
	noRange := it.noRange
	it.mu.Unlock()

	triedPlain := noRange
	var lastErr error
	failures := 0
	for iter := 0; failures < 6 && iter < 12; iter++ {
		if failures > 0 {
			sleep(ctx, backoff(failures))
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		rng := "bytes=0-0"
		if noRange {
			rng = ""
		}
		s, err := e.open(ctx, it, raw, rng, "")
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = err
			failures++
			continue
		}
		resp := s.resp
		pr := &probeResult{
			total:       -1,
			disposition: resp.Header.Get("Content-Disposition"),
			ctype:       resp.Header.Get("Content-Type"),
			finalURL:    resp.Request.URL.String(),
			etag:        resp.Header.Get("ETag"),
			lastMod:     resp.Header.Get("Last-Modified"),
		}
		status := resp.StatusCode
		switch {
		case status == http.StatusPartialContent:
			_, _, total, _ := parseContentRange(resp.Header.Get("Content-Range"))
			s.Close()
			if total <= 0 {
				// Ranges without a known size are useless: take the plain stream.
				noRange, triedPlain = true, true
				continue
			}
			pr.total, pr.ranges = total, true
			return pr, nil
		case status >= 200 && status < 300:
			pr.total = resp.ContentLength
			pr.body = s
			return pr, nil
		case status == http.StatusRequestedRangeNotSatisfiable && !noRange:
			s.Close()
			noRange, triedPlain = true, true
		case (&httpError{status}).throttle():
			s.Close()
			lastErr = &httpError{status}
			failures++
		default:
			s.Close()
			// Some servers reject Range outright but serve a normal request.
			if !triedPlain {
				noRange, triedPlain = true, true
				continue
			}
			return nil, &httpError{status}
		}
	}
	if lastErr == nil {
		lastErr = errors.New("probe failed")
	}
	return nil, lastErr
}

var pageExts = map[string]bool{"": true, "html": true, "htm": true, "php": true, "asp": true, "aspx": true, "jsp": true, "xhtml": true}

// ---- one download ----

func (e *Engine) download(ctx context.Context, it *item, maxConns int) error {
	it.mu.Lock()
	kind := it.rec.Kind
	it.mu.Unlock()
	if kind == "hls" {
		return e.runHLS(ctx, it, maxConns)
	}
	for restart := 0; ; restart++ {
		err := e.attempt(ctx, it, maxConns)
		if errors.Is(err, errIsHLS) { // the link turned out to be a stream playlist
			it.mu.Lock()
			it.rec.Kind = "hls"
			it.mu.Unlock()
			return e.runHLS(ctx, it, maxConns)
		}
		var rs *restartError
		if errors.As(err, &rs) && restart < 2 && ctx.Err() == nil {
			it.mu.Lock()
			it.rec.Segments = nil
			it.rec.Received = 0
			if rs.noRange {
				it.noRange = true
			}
			it.mu.Unlock()
			continue
		}
		return err
	}
}

func validatorsMatch(r *Record, pr *probeResult) bool {
	if r.ETag != "" && pr.etag != "" {
		return r.ETag == pr.etag
	}
	if r.LastModified != "" && pr.lastMod != "" {
		return r.LastModified == pr.lastMod
	}
	return true
}

func (e *Engine) attempt(ctx context.Context, it *item, maxConns int) error {
	pr, err := e.probe(ctx, it)
	if err != nil {
		return err
	}
	defer pr.close()

	it.mu.Lock()
	r := &it.rec
	resume := r.NameFixed && len(r.Segments) > 0 && r.Total > 0 && pr.ranges && pr.total == r.Total && validatorsMatch(r, pr)
	expect := r.NameHint
	if expect == "" {
		expect = nameFromURL(r.URL)
	}
	nameFixed := r.NameFixed
	it.mu.Unlock()

	if isHLSType(pr.ctype) && !nameFixed {
		return errIsHLS
	}
	// A login or error page served in place of the file must not be saved as the file.
	if baseMime(pr.ctype) == "text/html" && !pageExts[extOf(expect)] {
		return &codeError{code: "html_page"}
	}

	var f *os.File
	if resume {
		if f, err = os.OpenFile(partPath(r), os.O_RDWR, 0); err != nil {
			resume = false
		}
	}
	if !resume {
		if nameFixed {
			f, err = os.OpenFile(partPath(r), os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
		} else {
			name := chooseFileName(r.NameHint, pr.disposition, pr.finalURL, r.URL, pr.ctype)
			f, err = e.fixName(it, name)
		}
		if err != nil {
			return err
		}
		it.mu.Lock()
		r.Segments = nil
		r.Received = 0
		r.Total = pr.total
		r.ETag, r.LastModified = pr.etag, pr.lastMod
		r.Resumable = pr.ranges && pr.total > 0
		dir := r.Dir
		it.mu.Unlock()
		if free := freeSpace(dir); pr.total > 0 && free >= 0 && pr.total > free {
			f.Close()
			return &codeError{code: "disk_full"}
		}
	}

	it.mu.Lock()
	r.FinalURL = pr.finalURL
	r.State = StateDownloading
	resumable := r.Resumable
	if resumable && !resume {
		n := int(min(int64(maxConns), max(1, r.Total/minSplit)))
		size := r.Total / int64(n)
		for i := 0; i < n; i++ {
			start := int64(i) * size
			end := start + size - 1
			if i == n-1 {
				end = r.Total - 1
			}
			r.Segments = append(r.Segments, &Segment{Start: start, End: end, Pos: start})
		}
	}
	for _, s := range r.Segments {
		s.active = false
	}
	it.file = f
	it.lastBytes = r.Received
	total := r.Total
	it.mu.Unlock()
	e.committed()

	if resumable {
		pr.close()
		if !resume {
			markSparse(f)
			if err = f.Truncate(total); err == nil {
				err = e.runMulti(ctx, it, f, maxConns)
			}
		} else {
			err = e.runMulti(ctx, it, f, maxConns)
		}
	} else {
		body := pr.body
		pr.body = nil
		err = e.runSingle(ctx, it, f, body)
	}

	it.mu.Lock()
	it.file = nil
	it.mu.Unlock()
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return e.finalize(it)
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// fixName settles the destination folder and a name nobody else is using, and
// creates the part file under it.
func (e *Engine) fixName(it *item, name string) (*os.File, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	it.mu.Lock()
	dir := it.rec.Dir
	it.mu.Unlock()
	if dir == "" {
		dir = e.settings.DownloadDir
		if e.settings.CategoryFolders {
			if sub, ok := categoryFolder[CategoryOf(name)]; ok {
				dir = filepath.Join(dir, sub)
			}
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	taken := map[string]bool{}
	for _, other := range e.items {
		if other == it {
			continue
		}
		other.mu.Lock()
		if other.rec.NameFixed && other.rec.State != StateCompleted {
			taken[strings.ToLower(filepath.Join(other.rec.Dir, other.rec.FileName))] = true
		}
		other.mu.Unlock()
	}

	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 0; i < 10000; i++ {
		cand := name
		if i > 0 {
			cand = fmt.Sprintf("%s (%d)%s", stem, i, ext)
		}
		full := filepath.Join(dir, cand)
		if taken[strings.ToLower(full)] || exists(full) || exists(full+".hfdm") {
			continue
		}
		f, err := os.OpenFile(full+".hfdm", os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			if os.IsExist(err) {
				continue
			}
			return nil, err
		}
		it.mu.Lock()
		it.rec.Dir, it.rec.FileName, it.rec.NameFixed = dir, cand, true
		it.mu.Unlock()
		return f, nil
	}
	return nil, &codeError{code: "disk", detail: "no free file name"}
}

func (e *Engine) finalize(it *item) error {
	it.mu.Lock()
	r := it.rec
	it.mu.Unlock()
	part := partPath(&r)
	final := filepath.Join(r.Dir, r.FileName)
	if exists(final) { // something appeared under our name meanwhile
		ext := filepath.Ext(r.FileName)
		stem := strings.TrimSuffix(r.FileName, ext)
		for i := 1; i < 10000; i++ {
			cand := fmt.Sprintf("%s (%d)%s", stem, i, ext)
			if !exists(filepath.Join(r.Dir, cand)) {
				final = filepath.Join(r.Dir, cand)
				it.mu.Lock()
				it.rec.FileName = cand
				it.mu.Unlock()
				break
			}
		}
	}
	if err := os.Rename(part, final); err != nil {
		return err
	}
	markOfTheWeb(final, r.URL, r.Referer)
	return nil
}

// ---- segmented transfer ----

// pickSegment returns work for a free connection: an unowned unfinished
// segment if there is one, otherwise the second half of the busiest segment.
//
// Splitting is safe without stopping the owner: the split point is at least
// minSplit bytes ahead of its position, and the owner writes at most
// bufSize (< minSplit) bytes between two looks at its segment's End.
func pickSegment(r *Record) *Segment {
	var victim *Segment
	vi := -1
	for i, s := range r.Segments {
		if s.done() {
			continue
		}
		if !s.active {
			return s
		}
		if victim == nil || s.remaining() > victim.remaining() {
			victim, vi = s, i
		}
	}
	if victim == nil || victim.remaining() < 2*minSplit {
		return nil
	}
	mid := victim.Pos + victim.remaining()/2
	ns := &Segment{Start: mid, End: victim.End, Pos: mid}
	victim.End = mid - 1
	r.Segments = append(r.Segments, nil)
	copy(r.Segments[vi+2:], r.Segments[vi+1:])
	r.Segments[vi+1] = ns
	return ns
}

// compact folds finished segments into their right-hand neighbour so the list
// stays short no matter how often segments were split.
func compact(r *Record) {
	segs := r.Segments
	out := segs[:0]
	for i, s := range segs {
		if s.done() && !s.active && i+1 < len(segs) && segs[i+1].Start == s.End+1 {
			segs[i+1].Start = s.Start
			continue
		}
		out = append(out, s)
	}
	for i := len(out); i < len(segs); i++ {
		segs[i] = nil
	}
	r.Segments = out
}

func (e *Engine) runMulti(parent context.Context, it *item, f *os.File, maxConns int) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	// All of these are guarded by it.mu.
	limit := maxConns
	active := 0
	failures := 0
	var fatal error
	var notBefore time.Time

	wake := make(chan struct{}, 1)
	signal := func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	}

	worker := func(seg *Segment) {
		progressed, err := e.fetchSegment(ctx, it, f, seg)

		it.mu.Lock()
		seg.active = false
		active--
		it.conns = active
		now := time.Now()
		var he *httpError
		var rs *restartError
		switch {
		case err == nil:
			failures = 0
		case ctx.Err() != nil:
			// stopping; the error is just the cancellation
		case errors.As(err, &rs) || isLocal(err):
			if fatal == nil {
				fatal = err
			}
			cancel()
		default:
			if progressed {
				failures = 0
			} else if errors.As(err, &he) && active > 0 {
				// Refused while other connections work: the server caps
				// parallel connections. Settle for what it allows.
				if limit > active {
					limit = active
				}
				if now.After(notBefore) {
					notBefore = now.Add(time.Second)
				}
			} else if now.After(notBefore) {
				// Count one failure per round, not one per connection.
				failures++
				if he != nil && !he.throttle() {
					failures += 3 // 403/404/410 rarely heal
				}
				if he != nil && limit > 1 {
					limit = max(1, limit/2)
				}
				notBefore = now.Add(backoff(failures))
			}
			if failures > maxFailures && fatal == nil {
				fatal = err
				cancel()
			}
		}
		it.mu.Unlock()
		signal()
	}

	for {
		it.mu.Lock()
		if fatal == nil && ctx.Err() == nil && !time.Now().Before(notBefore) {
			for active < limit {
				seg := pickSegment(&it.rec)
				if seg == nil {
					break
				}
				seg.active = true
				active++
				it.conns = active
				go worker(seg)
			}
		}
		allDone := true
		for _, s := range it.rec.Segments {
			if !s.done() {
				allDone = false
				break
			}
		}
		a, lim, fe, nb := active, limit, fatal, notBefore
		it.mu.Unlock()

		if a == 0 {
			switch {
			case fe != nil:
				return fe
			case parent.Err() != nil:
				return parent.Err()
			case allDone:
				return nil
			}
		}

		var timer <-chan time.Time
		if d := time.Until(nb); d > 0 && a < lim {
			timer = time.After(d + 5*time.Millisecond)
		}
		done := ctx.Done()
		if ctx.Err() != nil {
			done = nil // already stopping: just wait for the workers
		}
		select {
		case <-wake:
		case <-timer:
		case <-done:
		}
	}
}

// isLocal reports errors that retrying cannot fix (disk problems).
func isLocal(err error) bool {
	var le *localError
	return errors.As(err, &le)
}

type localError struct{ err error }

func (e *localError) Error() string { return e.err.Error() }
func (e *localError) Unwrap() error { return e.err }

// fetchSegment downloads seg from its current position until it is finished.
// progressed reports whether any byte was written.
func (e *Engine) fetchSegment(ctx context.Context, it *item, f *os.File, seg *Segment) (progressed bool, err error) {
	it.mu.Lock()
	start, end := seg.Pos, seg.End
	target := it.rec.FinalURL
	if target == "" {
		target = it.rec.URL
	}
	total := it.rec.Total
	ifRange := ""
	switch {
	case it.rec.ETag != "" && !strings.HasPrefix(it.rec.ETag, "W/"):
		ifRange = it.rec.ETag
	case it.rec.LastModified != "":
		ifRange = it.rec.LastModified
	}
	it.mu.Unlock()
	if start > end {
		return false, nil
	}

	s, err := e.open(ctx, it, target, fmt.Sprintf("bytes=%d-%d", start, end), ifRange)
	if err != nil {
		return false, err
	}
	defer s.Close()

	switch status := s.resp.StatusCode; {
	case status == http.StatusPartialContent:
		cs, _, ct, ok := parseContentRange(s.resp.Header.Get("Content-Range"))
		if ok && (cs != start || (ct > 0 && ct != total)) {
			return false, &restartError{}
		}
	case status == http.StatusOK || status == http.StatusRequestedRangeNotSatisfiable:
		// The validator no longer matches, or ranges stopped working.
		return false, &restartError{noRange: status == http.StatusOK}
	default:
		return false, &httpError{status}
	}

	buf := make([]byte, bufSize)
	for {
		n, rerr := s.Read(buf[:e.lim.chunk()])
		if n > 0 {
			it.mu.Lock()
			off := seg.Pos
			allowed := int64(n)
			if room := seg.End + 1 - seg.Pos; allowed > room {
				allowed = room
			}
			it.mu.Unlock()
			if allowed > 0 {
				if _, werr := f.WriteAt(buf[:allowed], off); werr != nil {
					return progressed, &localError{werr}
				}
				it.mu.Lock()
				seg.Pos += allowed
				it.rec.Received += allowed
				finished := seg.done()
				it.mu.Unlock()
				progressed = true
				if finished {
					return true, nil
				}
			} else {
				return progressed, nil
			}
			e.lim.wait(ctx, int(allowed))
		}
		if rerr != nil {
			it.mu.Lock()
			finished := seg.done()
			it.mu.Unlock()
			if finished {
				return progressed, nil
			}
			if rerr == io.EOF {
				rerr = io.ErrUnexpectedEOF
			}
			return progressed, rerr
		}
	}
}

// ---- single-stream transfer (server without range support) ----

func (e *Engine) runSingle(ctx context.Context, it *item, f *os.File, body *stream) error {
	defer func() {
		if body != nil {
			body.Close()
		}
	}()
	it.mu.Lock()
	it.conns = 1
	target := it.rec.URL
	it.mu.Unlock()

	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			sleep(ctx, backoff(attempt))
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err := f.Truncate(0); err != nil {
				return &localError{err}
			}
			it.mu.Lock()
			it.rec.Received = 0
			it.mu.Unlock()
		}
		if body == nil {
			s, err := e.open(ctx, it, target, "", "")
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				lastErr = err
				continue
			}
			if st := s.resp.StatusCode; st < 200 || st > 299 {
				s.Close()
				lastErr = &httpError{st}
				if !(&httpError{st}).throttle() {
					return lastErr
				}
				continue
			}
			it.mu.Lock()
			it.rec.Total = s.resp.ContentLength
			it.mu.Unlock()
			body = s
		}

		err := e.copyStream(ctx, it, f, body)
		body.Close()
		body = nil
		if err == nil {
			it.mu.Lock()
			short := it.rec.Total > 0 && it.rec.Received < it.rec.Total
			if !short {
				it.rec.Total = it.rec.Received
			}
			it.mu.Unlock()
			if !short {
				return nil
			}
			err = io.ErrUnexpectedEOF
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if isLocal(err) {
			return err
		}
		lastErr = err
	}
	return lastErr
}

func (e *Engine) copyStream(ctx context.Context, it *item, f *os.File, body *stream) error {
	buf := make([]byte, bufSize)
	var off int64
	for {
		n, rerr := body.Read(buf[:e.lim.chunk()])
		if n > 0 {
			if _, werr := f.WriteAt(buf[:n], off); werr != nil {
				return &localError{werr}
			}
			off += int64(n)
			it.mu.Lock()
			it.rec.Received = off
			it.mu.Unlock()
			e.lim.wait(ctx, n)
		}
		if rerr == io.EOF {
			return nil
		}
		if rerr != nil {
			return rerr
		}
	}
}
