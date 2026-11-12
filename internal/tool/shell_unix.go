//go:build !windows

package tool

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

const shellName = "Bash"

func shellCommand(ctx context.Context, command string) *exec.Cmd {
	return exec.CommandContext(ctx, "bash", "-c", command)
}

func runShell(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = time.Second
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	defer cmd.Cancel() // Reap background commands even when the shell exits normally.
	return cmd.Wait()
}
