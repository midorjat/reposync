// Package desktop holds the OS-specific bits of the tray app: notifications,
// starting at login, and opening files.
package desktop

import (
	"log"
	"os"
	"path/filepath"
)

// Notifier shows desktop notifications; failures are logged, never fatal.
type Notifier struct {
	Log *log.Logger
}

func (n Notifier) Notify(title, body string) {
	if err := notify(title, body); err != nil && n.Log != nil {
		n.Log.Printf("notification failed (%v): %s: %s", err, title, body)
	}
}

// TrayExe is the tray app's path, next to the running executable.
func TrayExe() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	name := "reposync-tray"
	if filepath.Ext(self) == ".exe" {
		name += ".exe"
	}
	return filepath.Join(filepath.Dir(self), name), nil
}

// LogPath is where the tray app writes its log.
func LogPath() (string, error) {
	dir, err := os.UserCacheDir() // %LOCALAPPDATA% on Windows
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "reposync", "agent.log"), nil
}
