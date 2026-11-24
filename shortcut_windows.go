package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"hukukfixdm/internal/shared"
)

// shortcutDirs returns the desktop and the Start menu's Programs folder. The test build replaces it so that
// tests never touch the real desktop.
var shortcutDirs = func() (desktop, programs string) {
	desktop, _ = windows.KnownFolderPath(windows.FOLDERID_Desktop, 0)
	programs, _ = windows.KnownFolderPath(windows.FOLDERID_Programs, 0)
	return desktop, programs
}

// Both names, because the shortcut is named in the system's language.
var shortcutNames = []string{shared.AppName + ".lnk", shared.AppNameEN + ".lnk"}

// createShortcuts puts "HukukFix İndirme Yöneticisi" on the desktop and in the Start menu.
func createShortcuts(target string) {
	desktop, programs := shortcutDirs()
	name := displayName() + ".lnk"
	for _, dir := range []string{desktop, programs} {
		if dir == "" {
			continue
		}
		for _, n := range shortcutNames {
			if n != name {
				_ = os.Remove(filepath.Join(dir, n))
			}
		}
		_ = writeShortcut(filepath.Join(dir, name), target)
	}
}

func removeShortcuts() {
	desktop, programs := shortcutDirs()
	for _, dir := range []string{desktop, programs} {
		if dir == "" {
			continue
		}
		for _, n := range shortcutNames {
			_ = os.Remove(filepath.Join(dir, n))
		}
	}
}

var (
	procCoCreateInstance = windows.NewLazySystemDLL("ole32.dll").NewProc("CoCreateInstance")
	clsidShellLink       = windows.GUID{Data1: 0x00021401, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidShellLinkW        = windows.GUID{Data1: 0x000214F9, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidPersistFile       = windows.GUID{Data1: 0x0000010B, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
)

// Method numbers in the IShellLinkW and IPersistFile vtables.
const (
	comQueryInterface   = 0
	comRelease          = 2
	linkSetDescription  = 7
	linkSetWorkingDir   = 9
	linkSetIconLocation = 17
	linkSetPath         = 20
	persistFileSave     = 6
	clsctxInprocServer  = 1
	sFalse              = syscall.Errno(1) // CoInitializeEx: already initialised on this thread
)

func comCall(obj unsafe.Pointer, method int, args ...uintptr) uintptr {
	vtbl := *(**[32]uintptr)(obj)
	r, _, _ := syscall.SyscallN(vtbl[method], append([]uintptr{uintptr(obj)}, args...)...)
	return r
}

// writeShortcut creates a .lnk file through the Windows shell (IShellLink), on a thread of its own so the COM
// apartment never meets the one the window uses.
func writeShortcut(lnk, target string) error {
	done := make(chan error, 1)
	go func() {
		goruntime.LockOSThread()
		defer goruntime.UnlockOSThread()
		if err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED); err != nil && !errors.Is(err, sFalse) {
			done <- err
			return
		}
		defer windows.CoUninitialize()

		var link unsafe.Pointer
		hr, _, _ := procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidShellLink)), 0, clsctxInprocServer,
			uintptr(unsafe.Pointer(&iidShellLinkW)), uintptr(unsafe.Pointer(&link)))
		if hr != 0 || link == nil {
			done <- fmt.Errorf("CoCreateInstance(ShellLink): 0x%x", hr)
			return
		}
		defer comCall(link, comRelease)

		pTarget, _ := windows.UTF16PtrFromString(target)
		pDir, _ := windows.UTF16PtrFromString(filepath.Dir(target))
		pDesc, _ := windows.UTF16PtrFromString(displayName())
		pLnk, _ := windows.UTF16PtrFromString(lnk)
		comCall(link, linkSetPath, uintptr(unsafe.Pointer(pTarget)))
		comCall(link, linkSetWorkingDir, uintptr(unsafe.Pointer(pDir)))
		comCall(link, linkSetDescription, uintptr(unsafe.Pointer(pDesc)))
		comCall(link, linkSetIconLocation, uintptr(unsafe.Pointer(pTarget)), 0)

		var file unsafe.Pointer
		if hr := comCall(link, comQueryInterface, uintptr(unsafe.Pointer(&iidPersistFile)), uintptr(unsafe.Pointer(&file))); hr != 0 || file == nil {
			done <- fmt.Errorf("QueryInterface(IPersistFile): 0x%x", hr)
			return
		}
		defer comCall(file, comRelease)
		if hr := comCall(file, persistFileSave, uintptr(unsafe.Pointer(pLnk)), 1); hr != 0 {
			done <- fmt.Errorf("IPersistFile.Save: 0x%x", hr)
			return
		}
		goruntime.KeepAlive(pTarget)
		goruntime.KeepAlive(pDir)
		goruntime.KeepAlive(pDesc)
		goruntime.KeepAlive(pLnk)
		done <- nil
	}()
	return <-done
}
