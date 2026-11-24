package engine

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func encryptAES128(plain, key, iv []byte) []byte {
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	data := append(append([]byte(nil), plain...), bytes.Repeat([]byte{byte(pad)}, pad)...)
	block, _ := aes.NewCipher(key)
	out := make([]byte, len(data))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, data)
	return out
}

// hlsSite serves whatever is put into files, counting requests per path.
type hlsSite struct {
	mu    sync.Mutex
	files map[string][]byte
	types map[string]string
	hits  map[string]int
	delay time.Duration
	srv   *httptest.Server
}

func newHLSSite(t *testing.T) *hlsSite {
	s := &hlsSite{files: map[string][]byte{}, types: map[string]string{}, hits: map[string]int{}}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		data, ok := s.files[r.URL.Path]
		s.hits[r.URL.Path]++
		delay := s.delay
		ct := s.types[r.URL.Path]
		s.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		if delay > 0 && !strings.HasSuffix(r.URL.Path, ".m3u8") {
			time.Sleep(delay)
		}
		if ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data)) // honours Range
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *hlsSite) put(path string, data []byte) {
	s.mu.Lock()
	s.files[path] = data
	s.mu.Unlock()
}

func (s *hlsSite) hit(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits[path]
}

func TestHLSEncryptedTSAndQuality(t *testing.T) {
	site := newHLSSite(t)
	key := []byte("0123456789abcdef")
	iv := bytes.Repeat([]byte{7}, 16)
	var hi, lo []byte
	hiList := "#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXT-X-MEDIA-SEQUENCE:5\n#EXT-X-KEY:METHOD=AES-128,URI=\"key.bin\"\n"
	loList := "#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXT-X-KEY:METHOD=AES-128,URI=\"../key.bin\",IV=0x" + strings.Repeat("07", 16) + "\n"
	for i := 0; i < 14; i++ {
		seg := payload(90_000 + i*1000)
		hi = append(hi, seg...)
		seqIV := make([]byte, 16) // default IV = media sequence number
		binary.BigEndian.PutUint64(seqIV[8:], uint64(5+i))
		site.put(fmt.Sprintf("/v/hi/s%d.ts", i), encryptAES128(seg, key, seqIV))
		hiList += fmt.Sprintf("#EXTINF:4.0,\ns%d.ts\n", i)

		small := payload(20_000 + i)
		lo = append(lo, small...)
		site.put(fmt.Sprintf("/v/lo/s%d.ts", i), encryptAES128(small, key, iv))
		loList += fmt.Sprintf("#EXTINF:4.0,\ns%d.ts\n", i)
	}
	site.put("/v/hi/key.bin", key)
	site.put("/v/key.bin", key)
	site.put("/v/hi/index.m3u8", []byte(hiList+"#EXT-X-ENDLIST\n"))
	site.put("/v/lo/index.m3u8", []byte(loList+"#EXT-X-ENDLIST\n"))
	site.put("/v/master.m3u8", []byte("#EXTM3U\n"+
		"#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=640x360,CODECS=\"avc1.4d401e,mp4a.40.2\"\nlo/index.m3u8\n"+
		"#EXT-X-STREAM-INF:BANDWIDTH=2800000,RESOLUTION=1280x720,CODECS=\"avc1.4d401f,mp4a.40.2\"\nhi/index.m3u8\n"))

	e, dl := newTestEngine(t)
	best, _ := e.Add(AddRequest{URL: site.srv.URL + "/v/master.m3u8", FileName: "Ders 1: Giriş.mp4"})
	done := waitState(t, e, best.ID, StateCompleted, 30*time.Second)
	if done.FileName != "Ders 1_ Giriş.ts" || done.Kind != "hls" {
		t.Errorf("name=%q kind=%q", done.FileName, done.Kind)
	}
	checkFile(t, filepath.Join(dl, done.FileName), hi)
	if done.Total != int64(len(hi)) || done.Received != done.Total {
		t.Errorf("size %d/%d, want %d", done.Received, done.Total, len(hi))
	}
	if entries, _ := os.ReadDir(dl); len(entries) != 1 {
		t.Errorf("parts not cleaned up: %d entries", len(entries))
	}

	// A wanted height picks the matching stream; the playlist URL alone is enough to know it is HLS.
	small, _ := e.Add(AddRequest{URL: site.srv.URL + "/v/master.m3u8?x=1", Quality: 480})
	d2 := waitState(t, e, small.ID, StateCompleted, 30*time.Second)
	checkFile(t, filepath.Join(dl, d2.FileName), lo)
}

func TestHLSByteRangesAndContentTypeDetection(t *testing.T) {
	site := newHLSSite(t)
	whole := payload(600_000)
	site.put("/media/all.ts", whole)
	list := "#EXTM3U\n#EXT-X-VERSION:4\n"
	for off := 0; off < len(whole); off += 100_000 {
		if off == 0 {
			list += "#EXTINF:2,\n#EXT-X-BYTERANGE:100000@0\nall.ts\n"
		} else {
			list += "#EXTINF:2,\n#EXT-X-BYTERANGE:100000\nall.ts\n" // continues after the previous range
		}
	}
	site.put("/media/play", []byte(list+"#EXT-X-ENDLIST\n")) // no .m3u8 in the URL
	site.types["/media/play"] = "application/vnd.apple.mpegurl"

	e, dl := newTestEngine(t)
	snap, _ := e.Add(AddRequest{URL: site.srv.URL + "/media/play"})
	done := waitState(t, e, snap.ID, StateCompleted, 30*time.Second)
	if done.Kind != "hls" || !strings.HasSuffix(done.FileName, ".ts") {
		t.Errorf("kind=%q name=%q", done.Kind, done.FileName)
	}
	checkFile(t, filepath.Join(dl, done.FileName), whole)
}

func TestHLSRejectsLiveAndDRM(t *testing.T) {
	site := newHLSSite(t)
	site.put("/live.m3u8", []byte("#EXTM3U\n#EXTINF:4,\na.ts\n#EXTINF:4,\nb.ts\n"))
	site.put("/drm.m3u8", []byte("#EXTM3U\n#EXT-X-KEY:METHOD=SAMPLE-AES,URI=\"skd://x\"\n#EXTINF:4,\na.ts\n#EXT-X-ENDLIST\n"))
	site.put("/split.m3u8", []byte("#EXTM3U\n#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"a\",DEFAULT=YES,URI=\"aud.m3u8\"\n"+
		"#EXT-X-STREAM-INF:BANDWIDTH=1,RESOLUTION=640x360,AUDIO=\"a\"\nvid.m3u8\n"))
	site.put("/vid.m3u8", []byte("#EXTM3U\n#EXTINF:4,\nv.ts\n#EXT-X-ENDLIST\n"))
	site.put("/aud.m3u8", []byte("#EXTM3U\n#EXTINF:4,\na.ts\n#EXT-X-ENDLIST\n"))
	e, dl := newTestEngine(t)
	for path, want := range map[string]string{"/live.m3u8": "live", "/drm.m3u8": "drm", "/split.m3u8": "hls_audio", "/gone.m3u8": "http"} {
		s, _ := e.Add(AddRequest{URL: site.srv.URL + path})
		if f := waitState(t, e, s.ID, StateFailed, 20*time.Second); f.ErrorCode != want {
			t.Errorf("%s: error %q, want %q", path, f.ErrorCode, want)
		}
	}
	if entries, _ := os.ReadDir(dl); len(entries) != 0 {
		t.Errorf("rejected streams left %d files behind", len(entries))
	}
}

func TestHLSPauseKeepsFinishedSegments(t *testing.T) {
	site := newHLSSite(t)
	site.delay = 25 * time.Millisecond
	var want []byte
	list := "#EXTM3U\n"
	for i := 0; i < 60; i++ {
		seg := payload(30_000 + i)
		want = append(want, seg...)
		site.put(fmt.Sprintf("/p/%d.ts", i), seg)
		list += fmt.Sprintf("#EXTINF:2,\n%d.ts\n", i)
	}
	site.put("/p/index.m3u8", []byte(list+"#EXT-X-ENDLIST\n"))

	e, dl := newTestEngine(t)
	s := e.Settings()
	s.MaxConnections = 4
	e.SetSettings(s)
	snap, _ := e.Add(AddRequest{URL: site.srv.URL + "/p/index.m3u8", FileName: "clip"})
	for i := 0; i < 400; i++ {
		if g, _ := e.Get(snap.ID); g.Received > int64(len(want))/3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.Pause(snap.ID)
	time.Sleep(300 * time.Millisecond)
	p, _ := e.Get(snap.ID)
	if p.State != StatePaused {
		t.Fatalf("state %s", p.State)
	}
	first := site.hit("/p/0.ts")
	e.Resume(snap.ID)
	done := waitState(t, e, snap.ID, StateCompleted, 60*time.Second)
	checkFile(t, filepath.Join(dl, done.FileName), want)
	if site.hit("/p/0.ts") != first {
		t.Error("a finished segment was downloaded again after resume")
	}
	var fetched int32
	for i := 0; i < 60; i++ {
		atomic.AddInt32(&fetched, int32(site.hit(fmt.Sprintf("/p/%d.ts", i))))
	}
	if fetched > 60+8 { // at most the segments in flight at the pause are repeated
		t.Errorf("%d segment requests for 60 segments", fetched)
	}
}

// ---- fragmented MP4 with separate audio ----

func be32(v uint32) []byte { b := make([]byte, 4); binary.BigEndian.PutUint32(b, v); return b }

func fakeInit(trackID uint32, handler string) []byte {
	mvhd := mp4Wrap("mvhd", make([]byte, 12), be32(1000), make([]byte, 80), be32(trackID+1)) // v0, timescale 1000
	tkhd := mp4Wrap("tkhd", make([]byte, 12), be32(trackID), make([]byte, 68))
	trak := mp4Wrap("trak", tkhd, mp4Wrap("mdia", mp4Wrap("hdlr", make([]byte, 8), []byte(handler))))
	trex := mp4Wrap("trex", make([]byte, 4), be32(trackID), make([]byte, 16))
	return append(mp4Wrap("ftyp", []byte("iso5"), make([]byte, 4)), mp4Wrap("moov", mvhd, trak, mp4Wrap("mvex", trex))...)
}

func fakeFragment(trackID uint32, body string) []byte {
	tfhd := mp4Wrap("tfhd", []byte{0, 2, 0, 0}, be32(trackID)) // default-base-is-moof
	moof := mp4Wrap("moof", mp4Wrap("mfhd", make([]byte, 4), be32(99)), mp4Wrap("traf", tfhd))
	return append(append(mp4Wrap("styp", []byte("msdh")), moof...), mp4Wrap("mdat", []byte(body))...)
}

func TestHLSFragmentedMP4WithSeparateAudio(t *testing.T) {
	site := newHLSSite(t)
	site.put("/f/v/init.mp4", fakeInit(1, "vide"))
	site.put("/f/a/init.mp4", fakeInit(1, "soun")) // same track ID as the video, as in real streams
	vList := "#EXTM3U\n#EXT-X-MAP:URI=\"init.mp4\"\n"
	aList := "#EXTM3U\n#EXT-X-MAP:URI=\"init.mp4\"\n"
	for i := 0; i < 4; i++ {
		site.put(fmt.Sprintf("/f/v/%d.m4s", i), fakeFragment(1, fmt.Sprintf("V%d", i)))
		site.put(fmt.Sprintf("/f/a/%d.m4s", i), fakeFragment(1, fmt.Sprintf("A%d", i)))
		vList += fmt.Sprintf("#EXTINF:4,\n%d.m4s\n", i)
		aList += fmt.Sprintf("#EXTINF:4,\n%d.m4s\n", i)
	}
	site.put("/f/v/index.m3u8", []byte(vList+"#EXT-X-ENDLIST\n"))
	site.put("/f/a/index.m3u8", []byte(aList+"#EXT-X-ENDLIST\n"))
	site.put("/f/master.m3u8", []byte("#EXTM3U\n"+
		"#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"aud\",NAME=\"English\",DEFAULT=YES,URI=\"a/index.m3u8\"\n"+
		"#EXT-X-STREAM-INF:BANDWIDTH=2000000,RESOLUTION=1280x720,CODECS=\"avc1.64001f,mp4a.40.2\",AUDIO=\"aud\"\nv/index.m3u8\n"))

	e, dl := newTestEngine(t)
	snap, _ := e.Add(AddRequest{URL: site.srv.URL + "/f/master.m3u8", FileName: "film"})
	done := waitState(t, e, snap.ID, StateCompleted, 30*time.Second)
	if done.FileName != "film.mp4" {
		t.Errorf("name %q", done.FileName)
	}
	out, err := os.ReadFile(filepath.Join(dl, "film.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	top := mp4Boxes(out)
	var order []string
	var moov []byte
	var lastSeq uint32
	for _, b := range top {
		switch b.typ {
		case "moov":
			moov = b.payload(out)
		case "moof":
			p := b.payload(out)
			mfhd, _ := mp4Find(p, "mfhd")
			seq := binary.BigEndian.Uint32(mfhd[4:])
			if seq != lastSeq+1 {
				t.Errorf("fragment sequence %d after %d", seq, lastSeq)
			}
			lastSeq = seq
			traf, _ := mp4Find(p, "traf")
			tfhd, _ := mp4Find(traf, "tfhd")
			order = append(order, fmt.Sprint("t", binary.BigEndian.Uint32(tfhd[4:])))
		case "mdat":
			order = append(order, string(b.payload(out)))
		case "styp":
			t.Error("styp box copied into the output")
		}
	}
	if got, want := strings.Join(order, " "), "t1 V0 t2 A0 t1 V1 t2 A1 t1 V2 t2 A2 t1 V3 t2 A3"; got != want {
		t.Errorf("fragments:\n got %s\nwant %s", got, want)
	}
	var ids []uint32
	var handlers []string
	for _, b := range mp4Boxes(moov) {
		switch b.typ {
		case "trak":
			tkhd, _ := mp4Find(b.payload(moov), "tkhd")
			ids = append(ids, binary.BigEndian.Uint32(tkhd[12:]))
			mdia, _ := mp4Find(b.payload(moov), "mdia")
			hdlr, _ := mp4Find(mdia, "hdlr")
			handlers = append(handlers, string(hdlr[8:]))
		case "mvex":
			p := b.payload(moov)
			mehd, ok := mp4Find(p, "mehd")
			if !ok || binary.BigEndian.Uint64(mehd[4:]) != 16000 { // 4 x 4 s at timescale 1000
				t.Errorf("mehd missing or wrong: %v", mehd)
			}
			for _, c := range mp4Boxes(p) {
				if c.typ == "trex" {
					ids = append(ids, 100+binary.BigEndian.Uint32(c.payload(p)[4:]))
				}
			}
		}
	}
	if fmt.Sprint(ids) != "[1 2 101 102]" || fmt.Sprint(handlers) != "[vide soun]" {
		t.Errorf("tracks: ids=%v handlers=%v", ids, handlers)
	}
}

func TestPickVariant(t *testing.T) {
	vs := []hlsVariant{
		{URL: "360", Height: 360, Bandwidth: 800, Codecs: "avc1,mp4a"},
		{URL: "720", Height: 720, Bandwidth: 2800, Codecs: "avc1,mp4a"},
		{URL: "1080-hevc", Height: 1080, Bandwidth: 4000, Codecs: "hvc1,mp4a"},
		{URL: "1080", Height: 1080, Bandwidth: 5000, Codecs: "avc1,mp4a"},
		{URL: "1080-ac3", Height: 1080, Bandwidth: 5200, Codecs: "avc1,ac-3"},
	}
	for q, want := range map[int]string{0: "1080", 1080: "1080", 900: "720", 720: "720", 200: "360"} {
		if got := pickVariant(vs, q).URL; got != want {
			t.Errorf("quality %d -> %s, want %s", q, got, want)
		}
	}
	base, _ := url.Parse("https://cdn.example.com/a/b/master.m3u8?t=1")
	p, err := parseHLS(base, "#EXTM3U\r\n#EXT-X-STREAM-INF:BANDWIDTH=1,RESOLUTION=1920x1080,CODECS=\"avc1.640028,mp4a.40.2\"\r\n../c/v.m3u8?x=1,2\r\n")
	if err != nil || len(p.Variants) != 1 || p.Variants[0].URL != "https://cdn.example.com/a/c/v.m3u8?x=1,2" || p.Variants[0].Height != 1080 {
		t.Errorf("parse: %+v %v", p, err)
	}
	if _, err := parseHLS(base, "<html>"); err == nil {
		t.Error("html accepted as playlist")
	}
}
