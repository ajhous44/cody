package tool

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// sandboxCommand wraps bash in sandbox-exec. The profile allows everything
// except writes outside the writable folders (and, in strict mode, network).
// Paths travel as -D parameters, so no path needs quoting in the profile.
func sandboxCommand(ctx context.Context, sb Sandbox, root, command string) (*exec.Cmd, error) {
	if !sb.on() {
		return shellCommand(ctx, command), nil
	}
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		return nil, fmt.Errorf("shell sandbox unavailable: sandbox-exec not found")
	}
	var args, allow []string
	for i, p := range sb.writable(root) {
		args = append(args, "-D", fmt.Sprintf("W%d=%s", i, p))
		allow = append(allow, fmt.Sprintf(`(subpath (param "W%d"))`, i))
	}
	profile := `(version 1)
(allow default)
(deny file-write*)
(allow file-write* ` + strings.Join(allow, " ") + `
  (literal "/dev/null") (literal "/dev/zero") (literal "/dev/tty") (literal "/dev/dtracehelper")
  (regex #"^/dev/fd/") (regex #"^/dev/ttys"))
`
	if sb.Mode == "strict" {
		profile += "(deny network*)\n"
	}
	args = append(args, "-p", profile, "bash", "-c", command)
	return exec.CommandContext(ctx, "sandbox-exec", args...), nil
}

// RunSandboxHelper exists for Linux, where Landlock is applied by re-executing
// cody; macOS uses sandbox-exec instead.
func RunSandboxHelper(args []string) int { return 1 }
