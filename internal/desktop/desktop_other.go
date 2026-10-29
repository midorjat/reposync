//go:build !windows

package desktop

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func notify(title, body string) error {
	if _, err := exec.LookPath("notify-send"); err != nil {
		return errors.New("notify-send not installed")
	}
	return exec.Command("notify-send", "--app-name=reposync", title, body).Run()
}

func autostartFile() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "autostart", "reposync.desktop"), nil
}

// SetAutostart uses an XDG autostart entry (Linux desktops).
func SetAutostart(on bool) error {
	p, err := autostartFile()
	if err != nil {
		return err
	}
	if !on {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	exe, err := TrayExe()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	entry := fmt.Sprintf("[Desktop Entry]\nType=Application\nName=reposync\nExec=%q\nX-GNOME-Autostart-enabled=true\n", exe)
	return os.WriteFile(p, []byte(entry), 0o644)
}

func Autostart() (string, error) {
	p, err := autostartFile()
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(p); err != nil {
		return "", nil
	}
	return p, nil
}

func OpenFile(path string) error { return exec.Command("xdg-open", path).Start() }

func OpenStatus() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	cli := filepath.Join(filepath.Dir(self), "reposync")
	return exec.Command("x-terminal-emulator", "-e", "sh", "-c", fmt.Sprintf("%q status; read _", cli)).Start()
}
