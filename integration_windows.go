package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/wailsapp/wails/v2/pkg/options"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"hukukfixdm/internal/shared"
)

//go:embed all:extension
var extensionFS embed.FS

func exePath() string {
	p, err := os.Executable()
	if err != nil {
		return os.Args[0]
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	return p
}

// Chromium-family browsers each look under their own key; all of them are
// per-user, so no administrator rights are ever needed.
var nativeHostKeys = []string{
	`Software\Google\Chrome\NativeMessagingHosts\`,
	`Software\Microsoft\Edge\NativeMessagingHosts\`,
	`Software\BraveSoftware\Brave-Browser\NativeMessagingHosts\`,
	`Software\Chromium\NativeMessagingHosts\`,
	`Software\Vivaldi\NativeMessagingHosts\`,
}

// registerNativeHost points the browsers at HukukFixDM-host.exe. It runs on every
// start so moving the program folder never breaks the integration.
func registerNativeHost(dataDir string) (bool, error) {
	host := filepath.Join(filepath.Dir(exePath()), shared.HostExe)
	if _, err := os.Stat(host); err != nil {
		return false, nil
	}
	manifest := map[string]any{
		"name":            shared.NativeHostName,
		"description":     "HukukFix Download Manager",
		"path":            host,
		"type":            "stdio",
		"allowed_origins": []string{"chrome-extension://" + shared.ExtensionID + "/"},
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return true, err
	}
	file := filepath.Join(dataDir, "native-host.json")
	if old, err := os.ReadFile(file); err != nil || !bytes.Equal(old, data) {
		if err := os.WriteFile(file, data, 0o644); err != nil {
			return true, err
		}
	}
	var firstErr error
	for _, base := range nativeHostKeys {
		k, _, err := registry.CreateKey(registry.CURRENT_USER, base+shared.NativeHostName, registry.SET_VALUE)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if err := k.SetStringValue("", file); err != nil && firstErr == nil {
			firstErr = err
		}
		k.Close()
	}
	return true, firstErr
}

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

func setAutostart(on bool) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !on {
		if err := k.DeleteValue(shared.AppID); err != nil && !errors.Is(err, registry.ErrNotExist) {
			return err
		}
		return nil
	}
	return k.SetStringValue(shared.AppID, `"`+exePath()+`" --background`)
}

// extractExtension keeps an up-to-date copy of the browser extension in the
// data folder, where "Load unpacked" can pick it up.
func extractExtension(dataDir string) (string, error) {
	root := filepath.Join(dataDir, "extension")
	err := fs.WalkDir(extensionFS, "extension", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel("extension", filepath.FromSlash(p))
		target := filepath.Join(root, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := extensionFS.ReadFile(p)
		if err != nil {
			return err
		}
		if old, err := os.ReadFile(target); err == nil && bytes.Equal(old, data) {
			return nil
		}
		return os.WriteFile(target, data, 0o644)
	})
	return root, err
}

func shellOpen(target string) error {
	verb, _ := windows.UTF16PtrFromString("open")
	file, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL)
}

func revealInFolder(path string) error {
	if _, err := os.Stat(path); err != nil {
		// The file is gone (or not there yet): open the folder instead.
		return shellOpen(filepath.Dir(path))
	}
	cmd := exec.Command("explorer.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `explorer.exe /select,"` + path + `"`}
	return cmd.Start()
}

var browserExes = map[string][2]string{
	"chrome": {"chrome.exe", "chrome://extensions/"},
	"edge":   {"msedge.exe", "edge://extensions/"},
	"brave":  {"brave.exe", "brave://extensions/"},
}

func browserPath(exe string) string {
	for _, root := range []registry.Key{registry.CURRENT_USER, registry.LOCAL_MACHINE} {
		k, err := registry.OpenKey(root, `SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\`+exe, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		p, _, err := k.GetStringValue("")
		k.Close()
		if err == nil && p != "" {
			return strings.Trim(p, `"`)
		}
	}
	return ""
}

func installedBrowsers() []string {
	var out []string
	for _, name := range []string{"chrome", "edge", "brave"} {
		if browserPath(browserExes[name][0]) != "" {
			out = append(out, name)
		}
	}
	return out
}

// openExtensionsPage opens the browser's extension manager; internal pages
// like chrome://extensions can only be reached by launching the browser itself.
func openExtensionsPage(browser string) error {
	b, ok := browserExes[browser]
	if !ok {
		return errors.New("unknown browser")
	}
	p := browserPath(b[0])
	if p == "" {
		return errors.New("browser not installed")
	}
	return exec.Command(p, b[1]).Start()
}

var procGetUserDefaultUILanguage = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetUserDefaultUILanguage")

// systemLang returns "tr" on a Turkish Windows and "en" otherwise.
func systemLang() string {
	id, _, _ := procGetUserDefaultUILanguage.Call()
	if id&0x3ff == 0x1f {
		return "tr"
	}
	return "en"
}

func fatalBox(msg string) {
	t, _ := windows.UTF16PtrFromString(displayName())
	m, _ := windows.UTF16PtrFromString(msg)
	_, _ = windows.MessageBox(0, m, t, windows.MB_OK|windows.MB_ICONERROR)
}

// displayName is the product name in the system's language.
func displayName() string {
	if systemLang() == "tr" {
		return shared.AppName
	}
	return shared.AppNameEN
}

// windowBackground is the colour the window shows before the page has drawn: the theme's own background, so a light
// theme does not flash dark at start-up. "system" follows Windows' app mode (AppsUseLightTheme).
func windowBackground(theme string) *options.RGBA {
	dark := &options.RGBA{R: 21, G: 22, B: 25, A: 255}
	light := &options.RGBA{R: 250, G: 250, B: 251, A: 255}
	switch theme {
	case "dark":
		return dark
	case "light":
		return light
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return light
	}
	defer k.Close()
	if v, _, err := k.GetIntegerValue("AppsUseLightTheme"); err == nil && v == 0 {
		return dark
	}
	return light
}
