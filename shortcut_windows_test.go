package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestWriteShortcut writes a real .lnk through the Windows shell and reads its target back with WScript.Shell.
func TestWriteShortcut(t *testing.T) {
	dir := t.TempDir()
	target, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	lnk := filepath.Join(dir, "HukukFix İndirme Yöneticisi.lnk")
	if err := writeShortcut(lnk, target); err != nil {
		t.Fatalf("writeShortcut: %v", err)
	}
	if st, err := os.Stat(lnk); err != nil || st.Size() == 0 {
		t.Fatalf("no shortcut file: %v", err)
	}
	cmd := exec.Command("powershell", "-NoProfile", "-Command", "(New-Object -ComObject WScript.Shell).CreateShortcut($env:HFDM_LNK).TargetPath")
	cmd.Env = append(os.Environ(), "HFDM_LNK="+lnk)
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("WScript.Shell not available to check the target: %v", err)
	}
	if got := strings.TrimSpace(string(out)); !strings.EqualFold(got, target) {
		t.Fatalf("shortcut points to %q, want %q", got, target)
	}
}
