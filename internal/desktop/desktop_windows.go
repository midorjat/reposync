package desktop

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows/registry"
)

const createNoWindow = 0x08000000

func hidden(cmd *exec.Cmd) *exec.Cmd {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	return cmd
}

// Toasts are shown through PowerShell's registered app id, which needs no
// installer. Title and body travel in environment variables so they can't
// break out of the script.
const toastScript = `
[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] > $null
$t = [Windows.UI.Notifications.ToastNotificationManager]::GetTemplateContent([Windows.UI.Notifications.ToastTemplateType]::ToastText02)
$x = $t.GetElementsByTagName('text')
$x.Item(0).AppendChild($t.CreateTextNode($env:REPOSYNC_TITLE)) > $null
$x.Item(1).AppendChild($t.CreateTextNode($env:REPOSYNC_BODY)) > $null
$n = [Windows.UI.Notifications.ToastNotification]::new($t)
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\WindowsPowerShell\v1.0\powershell.exe').Show($n)
`

func notify(title, body string) error {
	cmd := hidden(exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", toastScript))
	cmd.Env = append(os.Environ(), "REPOSYNC_TITLE="+title, "REPOSYNC_BODY="+body)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, out)
	}
	return nil
}

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

// SetAutostart starts the tray app at login (HKCU Run key, no admin needed).
func SetAutostart(on bool) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !on {
		if err := k.DeleteValue("reposync"); err != nil && err != registry.ErrNotExist {
			return err
		}
		return nil
	}
	exe, err := TrayExe()
	if err != nil {
		return err
	}
	if _, err := os.Stat(exe); err != nil {
		return fmt.Errorf("%s not found; keep reposync-tray.exe next to reposync.exe", exe)
	}
	return k.SetStringValue("reposync", `"`+exe+`"`)
}

func Autostart() (string, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return "", nil
	}
	defer k.Close()
	v, _, err := k.GetStringValue("reposync")
	if err == registry.ErrNotExist {
		return "", nil
	}
	return v, err
}

// OpenFile opens a text file in Notepad.
func OpenFile(path string) error {
	return exec.Command("notepad.exe", path).Start()
}

// OpenStatus opens a console window showing `reposync status`.
func OpenStatus() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	cli := filepath.Join(filepath.Dir(self), "reposync.exe")
	cmd := exec.Command("cmd.exe")
	// cmd's quoting rules don't match Go's escaping, so build the line ourselves.
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow: true,
		CmdLine:    fmt.Sprintf(`cmd.exe /c start "reposync" cmd.exe /k ""%s" status"`, cli),
	}
	return cmd.Start()
}
