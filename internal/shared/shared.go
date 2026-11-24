// Package shared holds what the app and its native messaging host agree on.
package shared

import (
	"encoding/json"
	"os"
	"path/filepath"
)

const (
	// AppID names things on disk and in the registry (ASCII on purpose).
	AppID = "HukukFixDM"
	// AppName is what people see; AppNameEN is shown on non-Turkish systems.
	AppName   = "HukukFix İndirme Yöneticisi"
	AppNameEN = "HukukFix Download Manager"

	// NativeHostName is the name browsers use to find the native messaging host.
	NativeHostName = "com.hukukfix.downloadmanager"

	// ExtensionID is fixed by the public key in extension/manifest.json.
	ExtensionID = "kphedpgokeneghjfepkodjncihcjhjlh"

	AppExe  = "HukukFixDM.exe"
	HostExe = "HukukFixDM-host.exe"
)

// Version is a variable only so the test build can pose as a newer release
// (-ldflags "-X hukukfixdm/internal/shared.Version=9.9.9"). Keep wails.json productVersion in step.
var Version = "1.2.1"

// DataDir is where settings, the download list and the bridge file live.
func DataDir() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		base, _ = os.UserConfigDir()
	}
	return filepath.Join(base, AppID)
}

// BridgeInfo tells the browser extension how to reach the running app.
type BridgeInfo struct {
	Port    int    `json:"port"`
	Token   string `json:"token"`
	PID     int    `json:"pid"`
	Version string `json:"version"`
}

func bridgeFile() string { return filepath.Join(DataDir(), "bridge.json") }

func ReadBridgeInfo() (BridgeInfo, error) {
	var b BridgeInfo
	data, err := os.ReadFile(bridgeFile())
	if err != nil {
		return b, err
	}
	err = json.Unmarshal(data, &b)
	return b, err
}

func WriteBridgeInfo(b BridgeInfo) error {
	data, err := json.Marshal(b)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(DataDir(), 0o755); err != nil {
		return err
	}
	tmp := bridgeFile() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, bridgeFile())
}
