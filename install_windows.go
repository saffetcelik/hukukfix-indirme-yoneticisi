package main

// Installing without an installer. The release is a single HukukFixDM.exe. Started from wherever it was
// downloaded, it copies itself to %LOCALAPPDATA%\HukukFixDM, puts shortcuts on the desktop and in the Start
// menu and starts that copy. Started from there, it keeps the browser bridge (HukukFixDM-host.exe, embedded in
// this exe) next to itself and an entry in Windows' "Installed apps" list, whose Uninstall button runs
// "HukukFixDM.exe --uninstall". Everything is per user: no administrator rights, no UAC prompt.

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"hukukfixdm/internal/shared"
	"hukukfixdm/internal/update"
)

// build.ps1 copies the freshly built host into hostbin/ before building the app. A checkout that has not been
// built yet holds only hostbin/README.md; such a development build falls back to a host lying next to the exe.
//
//go:embed hostbin
var hostFS embed.FS

func installDir() string   { return shared.DataDir() }
func installedExe() string { return filepath.Join(installDir(), shared.AppExe) }

func samePath(a, b string) bool { return strings.EqualFold(filepath.Clean(a), filepath.Clean(b)) }

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// isInside reports whether p is dir itself or somewhere below it.
func isInside(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// install.json records which version the installed copy is, so an older download never replaces a newer
// (auto-updated) installation, and so the new version can say "updated" once.
func installInfoFile() string { return filepath.Join(installDir(), "install.json") }

func readInstalledVersion() string {
	var info struct {
		Version string `json:"version"`
	}
	if b, err := os.ReadFile(installInfoFile()); err == nil {
		_ = json.Unmarshal(b, &info)
	}
	return info.Version
}

func writeInstalledVersion(v string) error {
	b, _ := json.Marshal(map[string]string{"version": v})
	return os.WriteFile(installInfoFile(), b, 0o644)
}

// selfInstall runs first. Outside the install folder it installs this copy (unless the installed one is the
// same or newer), refreshes the shortcuts and starts the installed copy. It returns true when this process
// has handed over and must exit.
func selfInstall(args []string) bool {
	self, target := exePath(), installedExe()
	if samePath(self, target) {
		return false
	}
	if err := os.MkdirAll(installDir(), 0o755); err != nil {
		return false
	}
	c := update.Compare(readInstalledVersion(), shared.Version)
	if !fileExists(target) || c < 0 || (c == 0 && !sameContent(self, target)) {
		stopRunningInstance() // it may be the installed copy, which is about to be replaced
		data, err := os.ReadFile(self)
		if err != nil {
			return false
		}
		tmp := target + ".new"
		if err := os.WriteFile(tmp, data, 0o755); err != nil {
			return false
		}
		if err := replaceFile(tmp, target); err != nil {
			_ = os.Remove(tmp)
			return false // run from here instead
		}
	}
	createShortcuts(target)
	return startDetached(target, args) == nil
}

// prepareInstall is the per-start upkeep of the installed copy.
func prepareInstall(dir string) (updatedFrom string) {
	_ = os.MkdirAll(dir, 0o755)
	cleanupOld(dir)
	extractHost(dir)
	if samePath(exePath(), installedExe()) {
		prev := readInstalledVersion()
		if prev != shared.Version {
			_ = writeInstalledVersion(shared.Version)
		}
		if prev != "" && update.Compare(prev, shared.Version) < 0 {
			updatedFrom = prev
		}
		registerUninstall(exePath())
	}
	return updatedFrom
}

func sameContent(a, b string) bool {
	sa, err1 := os.Stat(a)
	sb, err2 := os.Stat(b)
	if err1 != nil || err2 != nil || sa.Size() != sb.Size() {
		return false
	}
	da, err1 := os.ReadFile(a)
	db, err2 := os.ReadFile(b)
	return err1 == nil && err2 == nil && bytes.Equal(da, db)
}

// replaceFile moves src over dst. A running program cannot be overwritten on Windows, but it can be renamed,
// so dst is moved aside first; the leftover *.old file is removed now or, if still in use, on a later start.
func replaceFile(src, dst string) error {
	if !fileExists(dst) {
		return os.Rename(src, dst)
	}
	old := fmt.Sprintf("%s.%d.old", dst, time.Now().UnixNano())
	if err := os.Rename(dst, old); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err != nil {
		_ = os.Rename(old, dst)
		return err
	}
	_ = os.Remove(old)
	return nil
}

func cleanupOld(dir string) {
	old, _ := filepath.Glob(filepath.Join(dir, "*.old"))
	for _, p := range old {
		_ = os.Remove(p)
	}
}

// extractHost keeps HukukFixDM-host.exe (the browser bridge) next to the installed program. It is rewritten
// only when this build carries a different one.
func extractHost(dir string) bool {
	target := filepath.Join(dir, shared.HostExe)
	data, err := hostFS.ReadFile("hostbin/" + shared.HostExe)
	if err != nil {
		side := filepath.Join(filepath.Dir(exePath()), shared.HostExe)
		if samePath(side, target) {
			return fileExists(target)
		}
		if data, err = os.ReadFile(side); err != nil {
			return fileExists(target)
		}
	}
	if old, err := os.ReadFile(target); err == nil && bytes.Equal(old, data) {
		return true
	}
	tmp := target + ".new"
	if err := os.WriteFile(tmp, data, 0o755); err != nil {
		return fileExists(target)
	}
	if err := replaceFile(tmp, target); err != nil { // a browser may be running the old host right now
		_ = os.Remove(tmp)
		return fileExists(target)
	}
	return true
}

func startDetached(exe string, args []string) error {
	cmd := exec.Command(exe, args...)
	cmd.Dir = filepath.Dir(exe)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func waitForExit(pid int, d time.Duration) {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return
	}
	defer windows.CloseHandle(h)
	_, _ = windows.WaitForSingleObject(h, uint32(d.Milliseconds()))
}

// runningInstance opens the HukukFix process named in bridge.json if it is still running. The image name is
// checked because process IDs are reused.
func runningInstance() (windows.Handle, shared.BridgeInfo, bool) {
	b, err := shared.ReadBridgeInfo()
	if err != nil || b.PID <= 0 || b.PID == os.Getpid() {
		return 0, b, false
	}
	h, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(b.PID))
	if err != nil {
		return 0, b, false
	}
	var code uint32
	buf := make([]uint16, 1024)
	n := uint32(len(buf))
	if windows.GetExitCodeProcess(h, &code) != nil || code != 259 /* STILL_ACTIVE */ ||
		windows.QueryFullProcessImageName(h, 0, &buf[0], &n) != nil {
		windows.CloseHandle(h)
		return 0, b, false
	}
	name := strings.ToLower(filepath.Base(windows.UTF16ToString(buf[:n])))
	if !strings.HasPrefix(name, "hukukfixdm") || name == strings.ToLower(shared.HostExe) {
		windows.CloseHandle(h)
		return 0, b, false
	}
	return h, b, true
}

func otherInstanceRunning() bool {
	h, _, ok := runningInstance()
	if ok {
		windows.CloseHandle(h)
	}
	return ok
}

// stopRunningInstance asks a running HukukFix to quit (it saves its list and closes cleanly) and waits for it.
// Versions before 1.2 do not understand the request; they are stopped, and their download list, which is
// saved continuously, survives that.
func stopRunningInstance() {
	h, b, ok := runningInstance()
	if !ok {
		return
	}
	defer windows.CloseHandle(h)
	req, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/v1/app/quit", b.Port), nil)
	req.Header.Set("X-HFDM-Token", b.Token)
	req.Header.Set("X-HFDM-Internal", "quit")
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}}
	if res, err := client.Do(req); err == nil {
		res.Body.Close()
	}
	if ev, _ := windows.WaitForSingleObject(h, 10000); ev == windows.WAIT_OBJECT_0 {
		return
	}
	_ = windows.TerminateProcess(h, 1)
	_, _ = windows.WaitForSingleObject(h, 5000)
}

// ---- "Installed apps" entry and uninstalling ----

const uninstallKey = `Software\Microsoft\Windows\CurrentVersion\Uninstall\` + shared.AppID

func registerUninstall(exe string) {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, uninstallKey, registry.SET_VALUE)
	if err != nil {
		return
	}
	defer k.Close()
	q := `"` + exe + `"`
	_ = k.SetStringValue("DisplayName", displayName())
	_ = k.SetStringValue("DisplayVersion", shared.Version)
	_ = k.SetStringValue("Publisher", "HukukFix")
	_ = k.SetStringValue("DisplayIcon", exe)
	_ = k.SetStringValue("InstallLocation", filepath.Dir(exe))
	_ = k.SetStringValue("UninstallString", q+" --uninstall")
	_ = k.SetStringValue("QuietUninstallString", q+" --uninstall --yes")
	_ = k.SetStringValue("URLInfoAbout", "https://github.com/"+update.Repo)
	_ = k.SetDWordValue("NoModify", 1)
	_ = k.SetDWordValue("NoRepair", 1)
	if st, err := os.Stat(exe); err == nil {
		_ = k.SetDWordValue("EstimatedSize", uint32(st.Size()/1024))
	}
}

var uninstallText = map[string][2]string{
	"tr": {
		"HukukFix İndirme Yöneticisi kaldırılsın mı?\n\nProgram, ayarları ve indirme listesi silinir. İndirdiğiniz dosyalar yerinde kalır.",
		"HukukFix kaldırıldı.\n\nTarayıcı uzantısını tarayıcının Uzantılar sayfasından kaldırabilirsiniz.",
	},
	"en": {
		"Remove HukukFix Download Manager?\n\nThe program, its settings and the download list are deleted. The files you downloaded stay where they are.",
		"HukukFix has been removed.\n\nYou can remove the browser extension on your browser's Extensions page.",
	},
}

// uninstall removes everything HukukFix put on this computer except the downloaded files.
func uninstall(quiet bool) {
	text := uninstallText[systemLang()]
	if !quiet && !askBox(text[0]) {
		return
	}
	stopRunningInstance()
	removeShortcuts()
	_ = setAutostart(false)
	for _, base := range nativeHostKeys {
		_ = registry.DeleteKey(registry.CURRENT_USER, base+shared.NativeHostName)
	}
	_ = registry.DeleteKey(registry.CURRENT_USER, uninstallKey)

	dir, self := installDir(), exePath()
	keep := downloadDir(dir)
	for attempt := 0; attempt < 20; attempt++ { // WebView2 helpers may hold their profile for a moment
		if removeContents(dir, self, keep) == 0 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if isInside(self, dir) {
		// A running program cannot delete itself: cmd.exe removes it (and the then empty folder) right after.
		cmd := exec.Command("cmd.exe")
		cmd.SysProcAttr = &syscall.SysProcAttr{
			HideWindow:    true,
			CreationFlags: windows.CREATE_NO_WINDOW,
			CmdLine:       fmt.Sprintf(`cmd.exe /d /c ping -n 3 127.0.0.1 >nul & del /f /q "%s" & rmdir "%s"`, self, dir),
		}
		if cmd.Start() == nil {
			_ = cmd.Process.Release()
		}
	}
	if !quiet {
		infoBox(text[1])
	}
}

// downloadDir returns the configured download folder when it lies inside the install folder, so uninstalling
// never deletes downloaded files.
func downloadDir(installFolder string) string {
	var s struct {
		DownloadDir string `json:"downloadDir"`
	}
	if b, err := os.ReadFile(filepath.Join(installFolder, "settings.json")); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	if s.DownloadDir != "" && isInside(s.DownloadDir, installFolder) {
		return s.DownloadDir
	}
	return ""
}

// removeContents deletes the install folder's contents except the running exe and the download folder, and
// returns how many entries could not be removed yet.
func removeContents(dir, self, keep string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	left := 0
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if samePath(p, self) || (keep != "" && isInside(keep, p)) {
			continue
		}
		if os.RemoveAll(p) != nil {
			left++
		}
	}
	return left
}

func askBox(msg string) bool {
	t, _ := windows.UTF16PtrFromString(displayName())
	m, _ := windows.UTF16PtrFromString(msg)
	r, _ := windows.MessageBox(0, m, t, windows.MB_YESNO|windows.MB_ICONQUESTION|windows.MB_DEFBUTTON2)
	return r == 6 // IDYES
}

func infoBox(msg string) {
	t, _ := windows.UTF16PtrFromString(displayName())
	m, _ := windows.UTF16PtrFromString(msg)
	_, _ = windows.MessageBox(0, m, t, windows.MB_OK|windows.MB_ICONINFORMATION)
}
