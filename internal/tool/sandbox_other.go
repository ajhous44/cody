//go:build !darwin && !linux

package tool

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
)

func sandboxCommand(ctx context.Context, sb Sandbox, root, command string) (*exec.Cmd, error) {
	if !sb.on() {
		return shellCommand(ctx, command), nil
	}
	return nil, fmt.Errorf("the shell sandbox is not available on %s; run cody in the container profile instead (see README)", runtime.GOOS)
}

func RunSandboxHelper(args []string) int { return 1 }
