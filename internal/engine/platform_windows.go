package engine

import (
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

func defaultDownloadDir() string {
	if p, err := windows.KnownFolderPath(windows.FOLDERID_Downloads, 0); err == nil && p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Downloads")
}

const fsctlSetSparse = 0x000900C4

// markSparse lets segments be written at arbitrary offsets without NTFS
// zero-filling everything before them first.
func markSparse(f *os.File) {
	var n uint32
	_ = windows.DeviceIoControl(windows.Handle(f.Fd()), fsctlSetSparse, nil, 0, nil, 0, &n, nil)
}

// freeSpace reports the bytes available to the user in dir, or -1 if unknown.
func freeSpace(dir string) int64 {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return -1
	}
	var free uint64
	if err := windows.GetDiskFreeSpaceEx(p, &free, nil, nil); err != nil {
		return -1
	}
	return int64(free)
}

func isDiskFull(err error) bool {
	return errors.Is(err, windows.ERROR_DISK_FULL) || errors.Is(err, windows.ERROR_HANDLE_DISK_FULL)
}

// markOfTheWeb tags a finished file as downloaded from the internet, exactly
// as browsers do, so SmartScreen and Office Protected View keep working.
func markOfTheWeb(path, hostURL, referer string) {
	var b strings.Builder
	b.WriteString("[ZoneTransfer]\r\nZoneId=3\r\n")
	if referer != "" {
		b.WriteString("ReferrerUrl=" + stripLine(referer) + "\r\n")
	}
	if hostURL != "" {
		b.WriteString("HostUrl=" + stripLine(hostURL) + "\r\n")
	}
	_ = os.WriteFile(path+":Zone.Identifier", []byte(b.String()), 0o644)
}

func stripLine(s string) string {
	return strings.NewReplacer("\r", "", "\n", "").Replace(s)
}

// ---- system proxy (Internet Options) ----

type proxyConfig struct {
	enabled bool
	http    string
	https   string
	bypass  []string
}

var (
	proxyMu     sync.Mutex
	proxyCache  proxyConfig
	proxyLoaded time.Time
)

func loadProxy() proxyConfig {
	proxyMu.Lock()
	defer proxyMu.Unlock()
	if time.Since(proxyLoaded) < 30*time.Second {
		return proxyCache
	}
	proxyLoaded = time.Now()
	proxyCache = proxyConfig{}
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.QUERY_VALUE)
	if err != nil {
		return proxyCache
	}
	defer k.Close()
	if v, _, err := k.GetIntegerValue("ProxyEnable"); err != nil || v == 0 {
		return proxyCache
	}
	server, _, _ := k.GetStringValue("ProxyServer")
	if server == "" {
		return proxyCache
	}
	c := proxyConfig{enabled: true}
	if strings.Contains(server, "=") {
		for _, part := range strings.Split(server, ";") {
			kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
			if len(kv) != 2 {
				continue
			}
			switch strings.ToLower(kv[0]) {
			case "http":
				c.http = kv[1]
			case "https":
				c.https = kv[1]
			}
		}
	} else {
		c.http, c.https = server, server
	}
	if override, _, err := k.GetStringValue("ProxyOverride"); err == nil {
		for _, b := range strings.Split(override, ";") {
			if b = strings.ToLower(strings.TrimSpace(b)); b != "" {
				c.bypass = append(c.bypass, b)
			}
		}
	}
	proxyCache = c
	return c
}

// SystemProxy is the proxy choice downloads use, for other HTTP clients of the app (the updater).
func SystemProxy(req *http.Request) (*url.URL, error) { return systemProxy(req) }

// systemProxy honours HTTP(S)_PROXY first and falls back to the Windows
// per-user proxy configuration.
func systemProxy(req *http.Request) (*url.URL, error) {
	if u, err := http.ProxyFromEnvironment(req); u != nil || err != nil {
		return u, err
	}
	c := loadProxy()
	if !c.enabled {
		return nil, nil
	}
	host := strings.ToLower(req.URL.Hostname())
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return nil, nil
	}
	for _, b := range c.bypass {
		if b == "<local>" {
			if !strings.Contains(host, ".") {
				return nil, nil
			}
			continue
		}
		if ok, _ := filepath.Match(b, host); ok {
			return nil, nil
		}
	}
	target := c.http
	if req.URL.Scheme == "https" {
		target = c.https
	}
	if target == "" {
		return nil, nil
	}
	if !strings.Contains(target, "://") {
		target = "http://" + target
	}
	return url.Parse(target)
}
