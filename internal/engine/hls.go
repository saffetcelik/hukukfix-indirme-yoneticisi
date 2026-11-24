package engine

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// HLS (HTTP Live Streaming) downloads: a playlist lists hundreds of short
// media segments. They are fetched in parallel into a parts folder — so a
// paused or interrupted download continues where it stopped — and then joined
// into one file: a .ts for MPEG-TS streams, an .mp4 for fragmented-MP4 ones
// (merging a separately delivered audio track when there is one).

var errIsHLS = errors.New("hls playlist")

func looksLikeHLS(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && strings.HasSuffix(strings.ToLower(u.Path), ".m3u8")
}

func isHLSType(ct string) bool {
	switch baseMime(ct) {
	case "application/vnd.apple.mpegurl", "application/x-mpegurl", "audio/mpegurl", "audio/x-mpegurl":
		return true
	}
	return false
}

type hlsVariant struct {
	URL       string
	Bandwidth int
	Height    int
	Codecs    string
	Audio     string // audio group, when the sound comes as a separate rendition
}

type hlsRendition struct {
	Group, URI string
	Default    bool
}

type hlsKey struct {
	Method, URI string
	IV          []byte
}

type hlsMap struct {
	URL      string
	Off, Len int64 // Len < 0: the whole resource
}

type hlsSegment struct {
	URL      string
	Duration float64
	Seq      int64
	Off, Len int64 // Len < 0: the whole resource
	Key      *hlsKey
	Map      *hlsMap
}

type hlsPlaylist struct {
	Variants []hlsVariant
	Audio    []hlsRendition
	Segments []hlsSegment
	Ended    bool
}

func hlsAttrs(s string) map[string]string {
	m := map[string]string{}
	for len(s) > 0 {
		eq := strings.IndexByte(s, '=')
		if eq < 0 {
			break
		}
		key := strings.ToUpper(strings.TrimSpace(s[:eq]))
		s = s[eq+1:]
		var val string
		if strings.HasPrefix(s, `"`) {
			if end := strings.IndexByte(s[1:], '"'); end < 0 {
				val, s = s[1:], ""
			} else {
				val, s = s[1:1+end], strings.TrimPrefix(s[2+end:], ",")
			}
		} else if end := strings.IndexByte(s, ','); end < 0 {
			val, s = s, ""
		} else {
			val, s = s[:end], s[end+1:]
		}
		m[key] = val
	}
	return m
}

func atoi64(s string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n
}

func parseHLS(base *url.URL, text string) (*hlsPlaylist, error) {
	text = strings.TrimPrefix(text, string([]byte{0xEF, 0xBB, 0xBF})) // UTF-8 byte order mark
	lines := strings.Split(strings.ReplaceAll(text, "\r", ""), "\n")
	if !strings.HasPrefix(strings.TrimSpace(lines[0]), "#EXTM3U") {
		return nil, &codeError{code: "hls", detail: "not a playlist"}
	}
	resolve := func(ref string) string {
		u, err := base.Parse(strings.TrimSpace(ref))
		if err != nil {
			return ""
		}
		return u.String()
	}
	p := &hlsPlaylist{}
	var (
		variant  *hlsVariant
		key      *hlsKey
		mp       *hlsMap
		dur      = -1.0
		seq      int64
		rangeLen = int64(-1)
		rangeOff int64
		lastEnd  int64
	)
	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "#") {
			switch {
			case variant != nil:
				if variant.URL = resolve(line); variant.URL != "" {
					p.Variants = append(p.Variants, *variant)
				}
				variant = nil
			case dur >= 0:
				s := hlsSegment{URL: resolve(line), Duration: dur, Seq: seq, Len: -1, Key: key, Map: mp}
				if rangeLen >= 0 {
					s.Off, s.Len = rangeOff, rangeLen
					lastEnd = rangeOff + rangeLen
				} else {
					lastEnd = 0
				}
				p.Segments = append(p.Segments, s)
				seq++
				dur, rangeLen = -1, -1
			}
			continue
		}
		tag, val, _ := strings.Cut(line, ":")
		switch tag {
		case "#EXT-X-STREAM-INF":
			a := hlsAttrs(val)
			v := &hlsVariant{Codecs: strings.ToLower(a["CODECS"]), Audio: a["AUDIO"]}
			v.Bandwidth, _ = strconv.Atoi(a["BANDWIDTH"])
			if _, h, ok := strings.Cut(strings.ToLower(a["RESOLUTION"]), "x"); ok {
				v.Height, _ = strconv.Atoi(h)
			}
			variant = v
		case "#EXT-X-MEDIA":
			if a := hlsAttrs(val); a["TYPE"] == "AUDIO" {
				r := hlsRendition{Group: a["GROUP-ID"], Default: a["DEFAULT"] == "YES"}
				if a["URI"] != "" {
					r.URI = resolve(a["URI"])
				}
				p.Audio = append(p.Audio, r)
			}
		case "#EXTINF":
			d, _, _ := strings.Cut(val, ",")
			if dur, _ = strconv.ParseFloat(strings.TrimSpace(d), 64); dur < 0 {
				dur = 0
			}
		case "#EXT-X-BYTERANGE":
			n, o, hasOff := strings.Cut(val, "@")
			rangeLen, rangeOff = atoi64(n), lastEnd
			if hasOff {
				rangeOff = atoi64(o)
			}
		case "#EXT-X-KEY":
			a := hlsAttrs(val)
			if m := strings.ToUpper(a["METHOD"]); m == "" || m == "NONE" {
				key = nil
			} else {
				k := &hlsKey{Method: m}
				if a["URI"] != "" {
					k.URI = resolve(a["URI"])
				}
				if iv := strings.TrimPrefix(strings.ToLower(a["IV"]), "0x"); iv != "" {
					k.IV, _ = hex.DecodeString(iv)
				}
				key = k
			}
		case "#EXT-X-MAP":
			a := hlsAttrs(val)
			m := &hlsMap{URL: resolve(a["URI"]), Len: -1}
			if br := a["BYTERANGE"]; br != "" {
				n, o, _ := strings.Cut(br, "@")
				m.Len, m.Off = atoi64(n), atoi64(o)
			}
			mp = m
		case "#EXT-X-MEDIA-SEQUENCE":
			seq = atoi64(val)
		case "#EXT-X-ENDLIST":
			p.Ended = true
		case "#EXT-X-PLAYLIST-TYPE":
			if strings.TrimSpace(val) == "VOD" {
				p.Ended = true
			}
		}
	}
	return p, nil
}

// hlsPlayable ranks codecs by how widely they play: H.264 with AAC first.
func hlsPlayable(v hlsVariant) int {
	score := 0
	c := v.Codecs
	if c == "" || strings.Contains(c, "avc1") {
		score += 2
	}
	if !strings.Contains(c, "ac-3") && !strings.Contains(c, "ec-3") {
		score++
	}
	return score
}

// pickVariant chooses the stream for the wanted height (0 = the best), never
// going above it when a lower one exists.
func pickVariant(vs []hlsVariant, quality int) hlsVariant {
	better := func(a, b hlsVariant) bool {
		if pa, pb := hlsPlayable(a), hlsPlayable(b); pa != pb {
			return pa > pb
		}
		if a.Height != b.Height {
			return a.Height > b.Height
		}
		return a.Bandwidth > b.Bandwidth
	}
	var pool []hlsVariant
	if quality > 0 {
		for _, v := range vs {
			if v.Height > 0 && v.Height <= quality {
				pool = append(pool, v)
			}
		}
		if pool == nil { // everything is larger: take the smallest
			small := vs[0]
			for _, v := range vs {
				if v.Height < small.Height || (v.Height == small.Height && better(v, small)) {
					small = v
				}
			}
			return small
		}
	} else {
		pool = vs
	}
	best := pool[0]
	for _, v := range pool[1:] {
		if better(v, best) {
			best = v
		}
	}
	return best
}

func (e *Engine) loadPlaylist(ctx context.Context, it *item, raw string) (*hlsPlaylist, error) {
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			sleep(ctx, backoff(attempt))
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		s, err := e.open(ctx, it, raw, "", "")
		if err != nil {
			lastErr = err
			continue
		}
		if st := s.resp.StatusCode; st < 200 || st > 299 {
			s.Close()
			lastErr = &httpError{st}
			if !(&httpError{st}).throttle() {
				return nil, lastErr
			}
			continue
		}
		body, err := io.ReadAll(io.LimitReader(s, 32<<20))
		base := s.resp.Request.URL
		s.Close()
		if err != nil {
			lastErr = err
			continue
		}
		return parseHLS(base, string(body))
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return nil, lastErr
}

type hlsJob struct {
	url      string
	off, ln  int64
	key      *hlsKey
	seq      int64
	file     string
	duration float64
}

type hlsTrack struct {
	segs  []hlsSegment
	files []string          // part file of every segment
	inits map[string]string // map key -> part file
}

func mapKey(m *hlsMap) string { return fmt.Sprintf("%s|%d|%d", m.URL, m.Off, m.Len) }

func (e *Engine) runHLS(ctx context.Context, it *item, maxConns int) error {
	it.mu.Lock()
	raw, quality, hint, nameFixed := it.rec.URL, it.rec.Quality, it.rec.NameHint, it.rec.NameFixed
	it.mu.Unlock()

	pl, err := e.loadPlaylist(ctx, it, raw)
	if err != nil {
		return err
	}
	var video, audio *hlsPlaylist
	if len(pl.Variants) > 0 {
		v := pickVariant(pl.Variants, quality)
		if video, err = e.loadPlaylist(ctx, it, v.URL); err != nil {
			return err
		}
		if v.Audio != "" {
			var pick *hlsRendition
			for i := range pl.Audio {
				if r := &pl.Audio[i]; r.Group == v.Audio && (pick == nil || (r.Default && !pick.Default)) {
					pick = r
				}
			}
			// A rendition without its own playlist is already part of the video stream.
			if pick != nil && pick.URI != "" && pick.URI != v.URL {
				if audio, err = e.loadPlaylist(ctx, it, pick.URI); err != nil {
					return err
				}
			}
		}
	} else {
		video = pl
	}

	tracks := []*hlsTrack{{segs: video.Segments}}
	if audio != nil {
		tracks = append(tracks, &hlsTrack{segs: audio.Segments})
	}
	fmp4 := false
	for _, t := range tracks {
		if len(t.segs) == 0 {
			return &codeError{code: "hls", detail: "empty playlist"}
		}
		for _, s := range t.segs {
			if s.Key != nil && s.Key.Method != "AES-128" {
				return &codeError{code: "drm", detail: s.Key.Method}
			}
			if s.Map != nil {
				fmp4 = true
			}
		}
	}
	if !video.Ended || (audio != nil && !audio.Ended) {
		return &codeError{code: "live"}
	}
	if audio != nil && !fmp4 {
		// Joining two separate MPEG-TS streams needs a real remuxer.
		return &codeError{code: "hls_audio"}
	}

	// Settle the output name and create the (still empty) target file.
	ext := ".ts"
	if fmp4 {
		ext = ".mp4"
	} else if u, err := url.Parse(video.Segments[0].URL); err == nil {
		switch x := strings.ToLower(path.Ext(u.Path)); x {
		case ".aac", ".mp3", ".ac3":
			ext = x
		}
	}
	var out *os.File
	if nameFixed {
		it.mu.Lock()
		p := partPath(&it.rec)
		it.mu.Unlock()
		out, err = os.OpenFile(p, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	} else {
		stem := strings.TrimSuffix(hint, filepath.Ext(hint))
		if stem == "" {
			if u, err := url.Parse(raw); err == nil {
				stem = sanitizeFileName(path.Base(path.Dir(u.Path)))
				if stem == "" || stem == "." || stem == "/" {
					stem = sanitizeFileName(u.Hostname())
				}
			}
		}
		if stem == "" {
			stem = "video"
		}
		out, err = e.fixName(it, stem+ext)
	}
	if err != nil {
		return err
	}
	defer func() {
		if out != nil {
			out.Close()
		}
	}()
	it.mu.Lock()
	partsDir := partPath(&it.rec) + ".parts"
	it.mu.Unlock()
	if err := os.MkdirAll(partsDir, 0o755); err != nil {
		return err
	}

	// Plan every piece: init segments first, then media segments in order.
	var jobs []hlsJob
	total := 0.0
	for ti, t := range tracks {
		tag := string(rune('v' + ti*('a'-'v'))) // "v" for video, "a" for audio
		t.inits = map[string]string{}
		for i, s := range t.segs {
			if s.Map != nil {
				if _, ok := t.inits[mapKey(s.Map)]; !ok {
					f := filepath.Join(partsDir, fmt.Sprintf("%s-init%03d", tag, len(t.inits)))
					t.inits[mapKey(s.Map)] = f
					jobs = append(jobs, hlsJob{url: s.Map.URL, off: s.Map.Off, ln: s.Map.Len, file: f})
				}
			}
			f := filepath.Join(partsDir, fmt.Sprintf("%s%06d", tag, i))
			t.files = append(t.files, f)
			jobs = append(jobs, hlsJob{url: s.URL, off: s.Off, ln: s.Len, key: s.Key, seq: s.Seq, file: f, duration: s.Duration})
			total += s.Duration
		}
	}

	// Pieces already on disk (an earlier, interrupted run) are not fetched again.
	var haveBytes int64
	var haveDur float64
	pending := jobs[:0:0]
	for _, j := range jobs {
		if st, err := os.Stat(j.file); err == nil {
			haveBytes += st.Size()
			haveDur += j.duration
		} else {
			pending = append(pending, j)
		}
	}
	estimate := func(bytes int64, dur float64) int64 {
		if dur <= 0 || total <= 0 {
			return -1
		}
		return max(bytes, int64(float64(bytes)/dur*total))
	}
	it.mu.Lock()
	it.rec.Segments = nil
	it.rec.Resumable = true
	it.rec.State = StateDownloading
	it.rec.Received = haveBytes
	it.rec.Total = estimate(haveBytes, haveDur)
	it.lastBytes = haveBytes
	it.mu.Unlock()
	e.committed()

	if err := e.fetchHLS(ctx, it, pending, min(max(maxConns, 1), 16), haveBytes, haveDur, estimate); err != nil {
		return err
	}

	// Join the pieces.
	if fmp4 {
		err = joinFMP4(out, tracks, total)
	} else {
		for _, f := range tracks[0].files {
			if err = appendFile(out, f); err != nil {
				break
			}
		}
	}
	if err == nil {
		err = out.Sync()
	}
	if err != nil {
		return &localError{err}
	}
	size, _ := out.Seek(0, io.SeekEnd)
	cerr := out.Close()
	out = nil
	if cerr != nil {
		return &localError{cerr}
	}
	_ = os.RemoveAll(partsDir)
	it.mu.Lock()
	it.rec.Total, it.rec.Received = size, size
	it.mu.Unlock()
	return e.finalize(it)
}

// fetchHLS downloads the pending pieces with a pool of connections.
func (e *Engine) fetchHLS(parent context.Context, it *item, jobs []hlsJob, workers int, doneBytes int64, doneDur float64,
	estimate func(int64, float64) int64) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	keys := &hlsKeys{e: e, it: it, cache: map[string][]byte{}}
	var (
		mu    sync.Mutex
		next  int
		fatal error
		wg    sync.WaitGroup
	)
	worker := func() {
		defer wg.Done()
		it.mu.Lock()
		it.conns++
		it.mu.Unlock()
		defer func() {
			it.mu.Lock()
			it.conns--
			it.mu.Unlock()
		}()
		for {
			mu.Lock()
			if fatal != nil || next >= len(jobs) || ctx.Err() != nil {
				mu.Unlock()
				return
			}
			j := jobs[next]
			next++
			mu.Unlock()

			n, err := e.fetchPiece(ctx, it, j, keys)
			if err != nil {
				mu.Lock()
				if fatal == nil && ctx.Err() == nil {
					fatal = err
				}
				mu.Unlock()
				cancel()
				return
			}
			mu.Lock()
			doneBytes += n
			doneDur += j.duration
			est := estimate(doneBytes, doneDur)
			mu.Unlock()
			it.mu.Lock()
			if est > 0 {
				it.rec.Total = max(est, it.rec.Received)
			}
			it.mu.Unlock()
		}
	}
	for i := 0; i < min(workers, len(jobs)); i++ {
		wg.Add(1)
		go worker()
	}
	wg.Wait()
	if fatal != nil {
		return fatal
	}
	return parent.Err()
}

type hlsKeys struct {
	e     *Engine
	it    *item
	mu    sync.Mutex
	cache map[string][]byte
}

func (k *hlsKeys) get(ctx context.Context, uri string) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if b, ok := k.cache[uri]; ok {
		return b, nil
	}
	s, err := k.e.open(ctx, k.it, uri, "", "")
	if err != nil {
		return nil, err
	}
	defer s.Close()
	if st := s.resp.StatusCode; st < 200 || st > 299 {
		return nil, &httpError{st}
	}
	b, err := io.ReadAll(io.LimitReader(s, 1024))
	if err != nil {
		return nil, err
	}
	if len(b) != 16 {
		return nil, &codeError{code: "drm", detail: "unusable key"}
	}
	k.cache[uri] = b
	return b, nil
}

// fetchPiece downloads one segment (decrypting it when needed) into its part
// file and returns its size. Live progress is added to the download as bytes
// arrive and taken back if the attempt fails.
func (e *Engine) fetchPiece(ctx context.Context, it *item, j hlsJob, keys *hlsKeys) (int64, error) {
	var lastErr error
	for attempt := 0; attempt < 6; attempt++ {
		if attempt > 0 {
			sleep(ctx, backoff(attempt))
		}
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		data, counted, err := e.readPiece(ctx, it, j)
		if err == nil && j.key != nil {
			var key []byte
			if key, err = keys.get(ctx, j.key.URI); err == nil {
				data, err = decryptAES128(data, key, j.key.IV, j.seq)
			}
		}
		if err == nil {
			tmp := j.file + ".tmp"
			if err = os.WriteFile(tmp, data, 0o644); err == nil {
				err = os.Rename(tmp, j.file)
			}
			if err != nil {
				return 0, &localError{err}
			}
			return int64(len(data)), nil
		}
		it.mu.Lock()
		it.rec.Received -= counted
		it.mu.Unlock()
		var ce *codeError
		var he *httpError
		if errors.As(err, &ce) || (errors.As(err, &he) && !he.throttle() && attempt >= 2) {
			return 0, err
		}
		lastErr = err
	}
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	return 0, lastErr
}

func (e *Engine) readPiece(ctx context.Context, it *item, j hlsJob) (data []byte, counted int64, err error) {
	rng := ""
	if j.ln >= 0 {
		rng = fmt.Sprintf("bytes=%d-%d", j.off, j.off+j.ln-1)
	}
	s, err := e.open(ctx, it, j.url, rng, "")
	if err != nil {
		return nil, 0, err
	}
	defer s.Close()
	if st := s.resp.StatusCode; st != http.StatusOK && st != http.StatusPartialContent {
		return nil, 0, &httpError{st}
	}
	if s.resp.ContentLength > 0 {
		data = make([]byte, 0, s.resp.ContentLength)
	}
	buf := make([]byte, 64<<10)
	for {
		n, rerr := s.Read(buf[:min(len(buf), e.lim.chunk())])
		if n > 0 {
			data = append(data, buf[:n]...)
			counted += int64(n)
			it.mu.Lock()
			it.rec.Received += int64(n)
			it.mu.Unlock()
			e.lim.wait(ctx, n)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return nil, counted, rerr
		}
	}
	if want := s.resp.ContentLength; want > 0 && int64(len(data)) != want {
		return nil, counted, io.ErrUnexpectedEOF
	}
	// A server that ignored the Range header sent the whole resource: cut our part out.
	if j.ln >= 0 && s.resp.StatusCode == http.StatusOK && int64(len(data)) >= j.off+j.ln {
		data = data[j.off : j.off+j.ln]
	}
	return data, counted, nil
}

func decryptAES128(data, key, iv []byte, seq int64) ([]byte, error) {
	if len(data) == 0 || len(data)%aes.BlockSize != 0 {
		return nil, &codeError{code: "hls", detail: "bad encrypted segment"}
	}
	if len(iv) != aes.BlockSize { // the default IV is the segment's sequence number
		iv = make([]byte, aes.BlockSize)
		binary.BigEndian.PutUint64(iv[8:], uint64(seq))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(data))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, data)
	pad := int(out[len(out)-1])
	if pad < 1 || pad > aes.BlockSize || pad > len(out) {
		return nil, &codeError{code: "hls", detail: "bad padding"}
	}
	return out[:len(out)-pad], nil
}

func appendFile(w io.Writer, name string) error {
	f, err := os.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}

// joinFMP4 writes the final .mp4: one header, then the fragments of every
// track interleaved by time so players can read sound and picture together.
func joinFMP4(w io.Writer, tracks []*hlsTrack, total float64) error {
	initOf := func(t *hlsTrack) ([]byte, error) {
		for _, s := range t.segs {
			if s.Map != nil {
				return os.ReadFile(t.inits[mapKey(s.Map)])
			}
		}
		return nil, errors.New("stream has no init segment")
	}
	vInit, err := initOf(tracks[0])
	if err != nil {
		return err
	}
	separate := len(tracks) > 1
	var aInit []byte
	if separate {
		if aInit, err = initOf(tracks[1]); err != nil {
			return err
		}
	}
	dur := total
	if separate { // total counts both tracks; they run in parallel
		dur = 0
		for _, s := range tracks[0].segs {
			dur += s.Duration
		}
	}
	head, err := mp4Init(vInit, aInit, dur)
	if err != nil {
		if separate {
			return err
		}
		head = vInit // unusual header: keep the original as it is
	}
	if _, err := w.Write(head); err != nil {
		return err
	}
	pos := int64(len(head))
	var seq uint32
	idx := make([]int, len(tracks))
	at := make([]float64, len(tracks))
	for {
		pick := -1
		for ti, t := range tracks {
			if idx[ti] < len(t.segs) && (pick < 0 || at[ti] < at[pick]) {
				pick = ti
			}
		}
		if pick < 0 {
			return nil
		}
		t := tracks[pick]
		data, err := os.ReadFile(t.files[idx[pick]])
		if err != nil {
			return err
		}
		track := uint32(0)
		if separate {
			track = uint32(pick + 1)
		}
		if err := mp4Fragments(w, data, track, &seq, &pos); err != nil {
			return err
		}
		at[pick] += t.segs[idx[pick]].Duration
		idx[pick]++
	}
}

// removeParts deletes what an unfinished download left on disk.
func removeParts(r *Record) {
	if !r.NameFixed {
		return
	}
	p := partPath(r)
	_ = os.Remove(p)
	_ = os.RemoveAll(p + ".parts")
}

func kindOf(kind, raw string) string {
	if kind == "hls" || looksLikeHLS(raw) {
		return "hls"
	}
	return ""
}
