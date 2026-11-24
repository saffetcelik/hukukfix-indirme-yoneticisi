package engine

import (
	"mime"
	"net/url"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"
)

var reservedNames = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true, "COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true, "LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// sanitizeFileName makes name safe to create on NTFS. It never returns a path:
// separators are replaced, so a hostile server cannot escape the download folder.
func sanitizeFileName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r < 0x20 || r == 0x7f:
			// drop control characters
		case strings.ContainsRune(`<>:"/\|?*`, r):
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	s := strings.TrimRight(strings.TrimSpace(b.String()), ". ")
	s = strings.TrimLeft(s, " ")
	if s == "" {
		return ""
	}
	ext := path.Ext(s)
	stem := strings.TrimSuffix(s, ext)
	if reservedNames[strings.ToUpper(stem)] {
		stem = "_" + stem
	}
	if len(ext) > 20 {
		ext = ""
		stem = s
	}
	// Keep the whole name comfortably below the 255 UTF-16 unit limit.
	for utf8.RuneCountInString(stem)+utf8.RuneCountInString(ext) > 180 {
		_, size := utf8.DecodeLastRuneInString(stem)
		stem = stem[:len(stem)-size]
	}
	return strings.TrimRight(stem, ". ") + ext
}

// nameFromURL derives a file name from the last path element of a URL.
func nameFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	p := strings.TrimRight(u.Path, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		p = p[i+1:]
	}
	if d, err := url.PathUnescape(p); err == nil {
		p = d
	}
	return sanitizeFileName(toValidUTF8(p))
}

var dispositionFallback = regexp.MustCompile(`(?i)filename\s*=\s*("([^"]*)"|[^;]+)`)

// nameFromDisposition extracts the file name from a Content-Disposition header
// (RFC 6266, including the RFC 5987 filename* form).
func nameFromDisposition(h string) string {
	if h == "" {
		return ""
	}
	name := ""
	if _, params, err := mime.ParseMediaType(h); err == nil {
		name = params["filename"]
	}
	if name == "" {
		if m := dispositionFallback.FindStringSubmatch(h); m != nil {
			name = m[2]
			if name == "" {
				name = strings.TrimSpace(m[1])
			}
		}
	}
	if name == "" {
		return ""
	}
	// Servers commonly percent-encode the plain filename= form.
	if strings.Contains(name, "%") {
		if d, err := url.PathUnescape(name); err == nil && utf8.ValidString(d) {
			name = d
		}
	}
	name = toValidUTF8(name)
	// Never trust directory components sent by a server.
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	return sanitizeFileName(name)
}

// toValidUTF8 reinterprets invalid UTF-8 as Latin-1, the usual culprit.
func toValidUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	r := make([]rune, 0, len(s))
	for i := 0; i < len(s); i++ {
		r = append(r, rune(s[i]))
	}
	return string(r)
}

var mimeExt = map[string]string{
	"application/pdf":                         "pdf",
	"application/zip":                         "zip",
	"application/x-zip-compressed":            "zip",
	"application/x-rar-compressed":            "rar",
	"application/vnd.rar":                     "rar",
	"application/x-7z-compressed":             "7z",
	"application/gzip":                        "gz",
	"application/x-gzip":                      "gz",
	"application/x-tar":                       "tar",
	"application/x-msdownload":                "exe",
	"application/x-msi":                       "msi",
	"application/vnd.android.package-archive": "apk",
	"application/x-iso9660-image":             "iso",
	"application/json":                        "json",
	"application/epub+zip":                    "epub",
	"application/msword":                      "doc",
	"application/vnd.ms-excel":                "xls",
	"application/vnd.ms-powerpoint":           "ppt",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document":   "docx",
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         "xlsx",
	"application/vnd.openxmlformats-officedocument.presentationml.presentation": "pptx",
	"video/mp4":        "mp4",
	"video/webm":       "webm",
	"video/x-matroska": "mkv",
	"video/quicktime":  "mov",
	"video/x-msvideo":  "avi",
	"video/x-flv":      "flv",
	"video/mp2t":       "ts",
	"audio/mpeg":       "mp3",
	"audio/mp4":        "m4a",
	"audio/aac":        "aac",
	"audio/flac":       "flac",
	"audio/ogg":        "ogg",
	"audio/wav":        "wav",
	"audio/x-wav":      "wav",
	"audio/webm":       "weba",
	"image/jpeg":       "jpg",
	"image/png":        "png",
	"image/gif":        "gif",
	"image/webp":       "webp",
	"image/svg+xml":    "svg",
	"text/plain":       "txt",
	"text/html":        "html",
	"text/csv":         "csv",
}

func baseMime(ct string) string {
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	return strings.ToLower(strings.TrimSpace(ct))
}

func extOf(name string) string {
	return strings.ToLower(strings.TrimPrefix(path.Ext(name), "."))
}

// chooseFileName picks the final name. A name supplied by the browser wins
// (the browser has already applied Content-Disposition), then the server's
// header, then the URL.
func chooseFileName(hint, disposition, finalURL, origURL, contentType string) string {
	name := sanitizeFileName(hint)
	if name == "" {
		name = nameFromDisposition(disposition)
	}
	if name == "" {
		name = nameFromURL(finalURL)
	}
	if name == "" {
		name = nameFromURL(origURL)
	}
	if name == "" {
		name = "download"
	}
	if extOf(name) == "" {
		if ext, ok := mimeExt[baseMime(contentType)]; ok {
			name += "." + ext
		}
	}
	return name
}

var categories = map[string][]string{
	"video":    {"mp4", "mkv", "webm", "avi", "mov", "wmv", "flv", "m4v", "ts", "mpg", "mpeg", "3gp"},
	"audio":    {"mp3", "m4a", "aac", "flac", "wav", "ogg", "opus", "wma", "weba"},
	"image":    {"jpg", "jpeg", "png", "gif", "webp", "bmp", "svg", "tif", "tiff", "heic", "ico", "psd"},
	"document": {"pdf", "doc", "docx", "xls", "xlsx", "ppt", "pptx", "txt", "rtf", "odt", "ods", "epub", "csv", "md", "json"},
	"archive":  {"zip", "rar", "7z", "tar", "gz", "bz2", "xz", "tgz", "zst", "iso", "img", "cab"},
	"program":  {"exe", "msi", "msix", "appx", "apk", "dmg", "pkg", "deb", "rpm", "jar", "bat", "bin"},
}

var categoryByExt = func() map[string]string {
	m := map[string]string{}
	for c, exts := range categories {
		for _, e := range exts {
			m[e] = c
		}
	}
	return m
}()

// CategoryOf classifies a file name: video, audio, image, document, archive, program or other.
func CategoryOf(name string) string {
	if c, ok := categoryByExt[extOf(name)]; ok {
		return c
	}
	return "other"
}

var categoryFolder = map[string]string{
	"video": "Video", "audio": "Music", "image": "Images", "document": "Documents",
	"archive": "Archives", "program": "Programs",
}

// Captures reports whether a browser download with this name, URL and size
// (0 or less when unknown) is one the app should take over under settings s.
func Captures(s Settings, name, rawURL string, size int64) bool {
	if !s.Capture {
		return false
	}
	ext := extOf(name)
	if ext == "" {
		if u, err := url.Parse(rawURL); err == nil {
			ext = extOf(u.Path)
		}
	}
	if ext == "" {
		return false
	}
	found := false
	for _, e := range s.CaptureExts {
		if e == ext {
			found = true
			break
		}
	}
	if !found {
		return false
	}
	return !(s.CaptureMinKB > 0 && size > 0 && size < int64(s.CaptureMinKB)*1024)
}
