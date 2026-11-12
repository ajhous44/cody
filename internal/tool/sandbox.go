package tool

import (
	"fmt"
	"os"
	"path/filepath"
)

// Sandbox limits what shell commands can change. The zero value is off.
//   - workspace: commands may write only inside the project, temp folders, the
//     user cache folder, and Writable; reading stays unrestricted.
//   - strict: workspace, plus no network.
//
// It is enforced by the OS (sandbox-exec on macOS, Landlock on Linux). Where
// that is unavailable a sandboxed command fails instead of running unconfined.
type Sandbox struct {
	Mode     string
	Writable []string
}

// SandboxHelperArg makes cody act as the Linux sandbox helper: it applies
// Landlock to itself and then becomes bash. Go cannot run code between fork
// and exec, so the bash tool re-executes cody instead.
const SandboxHelperArg = "__sandbox"

func (s Sandbox) on() bool { return s.Mode != "" && s.Mode != "off" }

func (s Sandbox) validate() error {
	switch s.Mode {
	case "", "off", "workspace", "strict":
		return nil
	}
	return fmt.Errorf("sandbox must be off, workspace, or strict, not %q", s.Mode)
}

// writable resolves the folders a sandboxed command may write. Symlinks are
// resolved because both enforcers match real paths (macOS /tmp is /private/tmp).
func (s Sandbox) writable(root string) []string {
	candidates := append([]string{root, os.TempDir(), "/tmp"}, s.Writable...)
	if cache, err := os.UserCacheDir(); err == nil {
		candidates = append(candidates, cache)
	}
	seen := map[string]bool{}
	var out []string
	for _, p := range candidates {
		real, err := filepath.EvalSymlinks(p)
		if err != nil || seen[real] {
			continue
		}
		seen[real] = true
		out = append(out, real)
	}
	return out
}
