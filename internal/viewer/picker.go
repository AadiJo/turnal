package viewer

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// pickerTimeout bounds how long the viewer waits on a native dialog. A user who
// walks away must not pin an HTTP handler open forever.
const pickerTimeout = 5 * time.Minute

// ErrPickerUnavailable reports that no native folder dialog could be run, so the
// caller should fall back to typing a path.
type ErrPickerUnavailable struct{ Reason string }

func (err ErrPickerUnavailable) Error() string {
	return "no folder picker is available: " + err.Reason
}

// ErrPickerCancelled reports that the user dismissed the dialog. This is an
// ordinary outcome, not a failure.
type ErrPickerCancelled struct{}

func (ErrPickerCancelled) Error() string { return "folder selection was cancelled" }

// pickDirectory opens the platform's folder chooser and returns the selected
// path. The viewer is loopback-only and launched from a terminal the user
// controls, so showing them a native dialog is the honest way to ask for a
// directory: a browser file input reports only a name, never a usable path.
func pickDirectory(ctx context.Context, start string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, pickerTimeout)
	defer cancel()

	if insideWSL() {
		return pickWithWindowsExplorer(ctx, start)
	}
	switch runtime.GOOS {
	case "windows":
		return pickWithPowerShell(ctx, "powershell.exe", start)
	case "darwin":
		return pickWithAppleScript(ctx, start)
	default:
		return pickWithLinuxDialog(ctx, start)
	}
}

// insideWSL reports whether this Linux process is running under WSL, where the
// useful file dialog is the Windows one reached through interop.
func insideWSL() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	if strings.TrimSpace(os.Getenv("WSL_DISTRO_NAME")) != "" {
		return true
	}
	data, err := os.ReadFile("/proc/version")
	if err != nil {
		return false
	}
	lower := strings.ToLower(string(data))
	return strings.Contains(lower, "microsoft") || strings.Contains(lower, "wsl")
}

// pickWithWindowsExplorer shows the Windows folder browser from WSL and
// translates the chosen Windows path back to a Linux one with wslpath.
func pickWithWindowsExplorer(ctx context.Context, start string) (string, error) {
	powershell, err := findWindowsPowerShell()
	if err != nil {
		return "", err
	}
	selected, err := pickWithPowerShell(ctx, powershell, toWindowsPath(start))
	if err != nil {
		return "", err
	}
	// The dialog returns a Windows path; the store lives on the Linux side.
	converted, convertErr := runPicker(ctx, "wslpath", "-u", selected)
	if convertErr != nil {
		return "", fmt.Errorf("translate selected Windows path %q: %w", selected, convertErr)
	}
	return strings.TrimSpace(converted), nil
}

// findWindowsPowerShell locates the Windows shell used to raise the dialog.
// Interop can be enabled while the Windows directories are absent from PATH, so
// the mounted install is checked before giving up.
func findWindowsPowerShell() (string, error) {
	if path, err := exec.LookPath("powershell.exe"); err == nil {
		return path, nil
	}
	for _, candidate := range []string{
		"/mnt/c/Windows/System32/WindowsPowerShell/v1.0/powershell.exe",
		"/mnt/c/Program Files/PowerShell/7/pwsh.exe",
	} {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", ErrPickerUnavailable{Reason: "powershell.exe is not reachable through WSL interop"}
}

// toWindowsPath converts a Linux path to its Windows form so the dialog opens in
// the right place. A failure only costs the starting directory.
func toWindowsPath(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	converted, err := runPicker(context.Background(), "wslpath", "-w", path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(converted)
}

// pickWithPowerShell drives the Windows Shell folder browser. Explorer's own
// dialog is used rather than a custom window so the result is a real path the
// user recognizes.
func pickWithPowerShell(ctx context.Context, powershell, start string) (string, error) {
	script := `
$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Windows.Forms | Out-Null
$dialog = New-Object System.Windows.Forms.FolderBrowserDialog
$dialog.Description = 'Choose a project directory for Turnal to record'
$dialog.ShowNewFolderButton = $false
if ($env:TURNAL_PICK_START) { $dialog.SelectedPath = $env:TURNAL_PICK_START }
if ($dialog.ShowDialog() -eq [System.Windows.Forms.DialogResult]::OK) {
  Write-Output $dialog.SelectedPath
}
`
	command := exec.CommandContext(ctx, powershell, "-NoLogo", "-NoProfile", "-NonInteractive",
		"-STA", "-ExecutionPolicy", "Bypass", "-Command", script)
	command.Env = append(os.Environ(), "TURNAL_PICK_START="+start)
	output, err := command.Output()
	if err != nil {
		if ctx.Err() != nil {
			return "", ErrPickerCancelled{}
		}
		return "", ErrPickerUnavailable{Reason: "the Windows folder dialog could not be shown"}
	}
	selected := strings.TrimSpace(lastLine(string(output)))
	if selected == "" {
		return "", ErrPickerCancelled{}
	}
	return selected, nil
}

func pickWithAppleScript(ctx context.Context, start string) (string, error) {
	script := `choose folder with prompt "Choose a project directory for Turnal to record"`
	if strings.TrimSpace(start) != "" {
		script += fmt.Sprintf(` default location POSIX file %q`, start)
	}
	output, err := runPicker(ctx, "osascript", "-e", "POSIX path of ("+script+")")
	if err != nil {
		// osascript exits non-zero when the user presses Cancel.
		return "", ErrPickerCancelled{}
	}
	selected := strings.TrimSpace(lastLine(output))
	if selected == "" {
		return "", ErrPickerCancelled{}
	}
	// choose folder returns a trailing separator.
	return strings.TrimSuffix(selected, string(os.PathSeparator)), nil
}

// pickWithLinuxDialog tries the desktop portal helpers in turn. A headless Linux
// box has none of them, which is reported as unavailable so the UI can fall back
// to a typed path.
func pickWithLinuxDialog(ctx context.Context, start string) (string, error) {
	if start == "" {
		start, _ = os.UserHomeDir()
	}
	candidates := [][]string{
		{"zenity", "--file-selection", "--directory", "--title=Choose a project directory for Turnal to record", "--filename=" + ensureTrailingSeparator(start)},
		{"kdialog", "--getexistingdirectory", start},
		{"qarma", "--file-selection", "--directory", "--filename=" + ensureTrailingSeparator(start)},
	}
	var missing []string
	for _, candidate := range candidates {
		if _, err := exec.LookPath(candidate[0]); err != nil {
			missing = append(missing, candidate[0])
			continue
		}
		output, err := runPicker(ctx, candidate[0], candidate[1:]...)
		if err != nil {
			// These helpers exit non-zero on Cancel.
			return "", ErrPickerCancelled{}
		}
		selected := strings.TrimSpace(lastLine(output))
		if selected == "" {
			return "", ErrPickerCancelled{}
		}
		return selected, nil
	}
	return "", ErrPickerUnavailable{
		Reason: "install one of " + strings.Join(missing, ", ") + ", or type the path instead",
	}
}

func runPicker(ctx context.Context, name string, args ...string) (string, error) {
	output, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return "", err
	}
	return string(output), nil
}

// lastLine returns the final non-empty line, since some shells emit banners
// before the value we asked for.
func lastLine(value string) string {
	lines := strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if trimmed := strings.TrimSpace(lines[i]); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func ensureTrailingSeparator(path string) string {
	if path == "" {
		return path
	}
	if strings.HasSuffix(path, string(os.PathSeparator)) {
		return path
	}
	return path + string(os.PathSeparator)
}

// defaultPickerStart is where the dialog opens: the directory the viewer was
// launched from, falling back to the user's home.
func defaultPickerStart() string {
	if cwd, err := os.Getwd(); err == nil {
		return filepath.Clean(cwd)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}
