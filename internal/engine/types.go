// Package engine is HukukFix's download core: segmented HTTP transfers with
// dynamic re-splitting, crash-safe resume, queueing and rate limiting.
package engine

import "time"

// State is the lifecycle state of a download.
type State string

const (
	StateQueued      State = "queued"
	StateConnecting  State = "connecting"
	StateDownloading State = "downloading"
	StatePaused      State = "paused"
	StateCompleted   State = "completed"
	StateFailed      State = "failed"
)

// Segment is a contiguous byte range of the file. Bytes [Start, Pos) are on
// disk; the segment is finished once Pos > End.
type Segment struct {
	Start int64 `json:"s"`
	End   int64 `json:"e"`
	Pos   int64 `json:"p"`

	active bool // a connection currently owns this segment
}

func (s *Segment) done() bool       { return s.Pos > s.End }
func (s *Segment) remaining() int64 { return s.End - s.Pos + 1 }

// Record is everything about a download that survives a restart.
type Record struct {
	ID           string     `json:"id"`
	URL          string     `json:"url"`
	FinalURL     string     `json:"finalUrl,omitempty"`
	FileName     string     `json:"fileName"`
	Dir          string     `json:"dir"`
	NameFixed    bool       `json:"nameFixed,omitempty"` // the part file exists under this name
	Total        int64      `json:"total"`               // -1 when the server does not say
	Received     int64      `json:"received"`
	State        State      `json:"state"`
	ErrorCode    string     `json:"errorCode,omitempty"`
	ErrorDetail  string     `json:"errorDetail,omitempty"`
	Resumable    bool       `json:"resumable"`
	Segments     []*Segment `json:"segments,omitempty"`
	ETag         string     `json:"etag,omitempty"`
	LastModified string     `json:"lastModified,omitempty"`
	Referer      string     `json:"referer,omitempty"`
	Cookie       string     `json:"cookie,omitempty"`
	UserAgent    string     `json:"userAgent,omitempty"`
	NameHint     string     `json:"nameHint,omitempty"`
	Kind         string     `json:"kind,omitempty"`    // "" = plain file, "hls" = segmented stream
	Quality      int        `json:"quality,omitempty"` // wanted video height for streams, 0 = best
	MimeHint     string     `json:"mimeHint,omitempty"`
	Source       string     `json:"source,omitempty"`
	CreatedAt    int64      `json:"createdAt"`
	FinishedAt   int64      `json:"finishedAt,omitempty"`
	ActiveMs     int64      `json:"activeMs,omitempty"` // time spent transferring
}

// AddRequest describes a new download.
type AddRequest struct {
	URL       string `json:"url"`
	FileName  string `json:"filename"`
	Dir       string `json:"dir"`
	Referer   string `json:"referer"`
	Cookie    string `json:"cookie"`
	UserAgent string `json:"userAgent"`
	Mime      string `json:"mime"`
	Kind      string `json:"kind"`
	Quality   int    `json:"quality"`
	Source    string `json:"source"`
}

// Snapshot is the read-only view of a download handed to the UI and the bridge.
type Snapshot struct {
	ID          string     `json:"id"`
	URL         string     `json:"url"`
	Host        string     `json:"host"`
	FileName    string     `json:"fileName"`
	Dir         string     `json:"dir"`
	Path        string     `json:"path"`
	Category    string     `json:"category"`
	Kind        string     `json:"kind,omitempty"`
	Total       int64      `json:"total"`
	Received    int64      `json:"received"`
	State       State      `json:"state"`
	ErrorCode   string     `json:"errorCode,omitempty"`
	ErrorDetail string     `json:"errorDetail,omitempty"`
	Speed       float64    `json:"speed"`
	ETA         int64      `json:"eta"` // seconds, -1 when unknown
	Conns       int        `json:"conns"`
	Resumable   bool       `json:"resumable"`
	Segments    [][3]int64 `json:"segments,omitempty"`
	Source      string     `json:"source,omitempty"`
	CreatedAt   int64      `json:"createdAt"`
	FinishedAt  int64      `json:"finishedAt,omitempty"`
	ActiveMs    int64      `json:"activeMs,omitempty"`
}

// Settings are the user-tunable engine options.
type Settings struct {
	DownloadDir     string   `json:"downloadDir"`
	CategoryFolders bool     `json:"categoryFolders"`
	MaxConnections  int      `json:"maxConnections"` // per download, 1..32
	MaxActive       int      `json:"maxActive"`      // simultaneous downloads, 1..10
	SpeedLimitKB    int      `json:"speedLimitKB"`   // 0 = unlimited
	Capture         bool     `json:"capture"`        // take over browser downloads
	CaptureExts     []string `json:"captureExts"`
	CaptureMinKB    int      `json:"captureMinKB"`
	ShowOnCapture   bool     `json:"showOnCapture"`
	Notify          bool     `json:"notify"`
	Autostart       bool     `json:"autostart"`
	Language        string   `json:"language"` // "auto", "tr", "en"
	Theme           string   `json:"theme"`    // "system", "light", "dark"
}

// DefaultCaptureExts are the file types taken over from the browser by default.
var DefaultCaptureExts = []string{
	"zip", "rar", "7z", "tar", "gz", "bz2", "xz", "tgz", "zst", "iso", "img", "cab",
	"exe", "msi", "msix", "appx", "apk", "dmg", "pkg", "deb", "rpm", "jar", "bin",
	"mp4", "mkv", "webm", "avi", "mov", "wmv", "flv", "m4v", "mpg", "mpeg", "3gp",
	"mp3", "m4a", "aac", "flac", "wav", "ogg", "opus", "wma",
	"pdf", "doc", "docx", "xls", "xlsx", "ppt", "pptx", "epub",
	"torrent", "psd", "ttf", "otf",
}

func defaultSettings() Settings {
	return Settings{
		DownloadDir:    defaultDownloadDir(),
		MaxConnections: 8,
		MaxActive:      3,
		Capture:        true,
		CaptureExts:    append([]string(nil), DefaultCaptureExts...),
		ShowOnCapture:  true,
		Notify:         true,
		Language:       "auto",
		Theme:          "system",
	}
}

func (s *Settings) normalize() {
	d := defaultSettings()
	if s.DownloadDir == "" {
		s.DownloadDir = d.DownloadDir
	}
	s.MaxConnections = clamp(s.MaxConnections, 1, 32)
	s.MaxActive = clamp(s.MaxActive, 1, 10)
	if s.SpeedLimitKB < 0 {
		s.SpeedLimitKB = 0
	}
	if s.CaptureMinKB < 0 {
		s.CaptureMinKB = 0
	}
	if s.CaptureExts == nil {
		s.CaptureExts = d.CaptureExts
	}
	if s.Language != "tr" && s.Language != "en" {
		s.Language = "auto"
	}
	if s.Theme != "light" && s.Theme != "dark" {
		s.Theme = "system"
	}
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func nowMs() int64 { return time.Now().UnixMilli() }
