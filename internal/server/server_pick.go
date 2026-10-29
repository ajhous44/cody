package server

import (
	"context"
	"errors"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

var (
	errPickerUnavailable = errors.New("no native folder picker is available on this system; enter a path instead")
	errPickerCancelled   = errors.New("cancelled")
)

// pickFolder opens the operating system's own folder chooser. It runs on the
// machine hosting the server, which for local use is the same machine as the
// browser. Every platform is best-effort: when no dialog helper exists the
// caller falls back to manual path entry.
func pickFolder(ctx context.Context) (string, error) {
	switch runtime.GOOS {
	case "darwin":
		out, err := exec.CommandContext(ctx, "osascript", "-e",
			`POSIX path of (choose folder with prompt "Choose a project folder")`).Output()
		if err != nil {
			return "", pickerError(runtime.GOOS, err)
		}
		return cleanPickedPath(string(out), true), nil

	case "linux":
		if _, err := exec.LookPath("zenity"); err == nil {
			out, err := exec.CommandContext(ctx, "zenity",
				"--file-selection", "--directory", "--title=Choose a project folder").Output()
			if err != nil {
				return "", pickerError(runtime.GOOS, err)
			}
			if p := cleanPickedPath(string(out), false); p != "" {
				return p, nil
			}
			return "", errPickerCancelled
		}
		if _, err := exec.LookPath("kdialog"); err == nil {
			out, err := exec.CommandContext(ctx, "kdialog",
				"--getexistingdirectory", ".", "Choose a project folder").Output()
			if err != nil {
				return "", pickerError(runtime.GOOS, err)
			}
			if p := cleanPickedPath(string(out), false); p != "" {
				return p, nil
			}
			return "", errPickerCancelled
		}
		return "", errPickerUnavailable

	case "windows":
		script := `Add-Type -AssemblyName System.Windows.Forms; ` +
			`$d = New-Object System.Windows.Forms.FolderBrowserDialog; ` +
			`$d.Description = 'Choose a project folder'; ` +
			`if ($d.ShowDialog() -eq [System.Windows.Forms.DialogResult]::OK) { [Console]::Out.Write($d.SelectedPath) }`
		out, err := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", script).Output()
		if err != nil {
			return "", pickerError(runtime.GOOS, err)
		}
		if p := cleanPickedPath(string(out), false); p != "" {
			return p, nil
		}
		return "", errPickerCancelled
	}
	return "", errPickerUnavailable
}

// Only an actual user cancellation should suppress the manual fallback.
func pickerError(platform string, err error) error {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		stderr := strings.TrimSpace(string(exit.Stderr))
		if platform == "darwin" && strings.Contains(stderr, "(-128)") {
			return errPickerCancelled
		}
		if platform == "linux" && exit.ExitCode() == 1 && stderr == "" {
			return errPickerCancelled
		}
	}
	return errPickerUnavailable
}

func cleanPickedPath(out string, stripTrailingSlash bool) string {
	p := strings.TrimSpace(out)
	if p == "" {
		return ""
	}
	if stripTrailingSlash {
		for len(p) > 1 && strings.HasSuffix(p, "/") {
			p = strings.TrimSuffix(p, "/")
		}
	}
	return p
}

// handlePickFolder is GET/POST so both a link and fetch() can drive it.
func (s *Server) handlePickFolder(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	path, err := pickFolder(ctx)
	switch {
	case errors.Is(err, errPickerCancelled):
		s.writeJSON(w, map[string]any{"cancelled": true})
	case errors.Is(err, errPickerUnavailable):
		http.Error(w, err.Error(), http.StatusNotImplemented)
	case err != nil:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	default:
		s.writeJSON(w, map[string]any{"path": path})
	}
}
