package tool

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"unsafe"

	"golang.org/x/sys/unix"
)

func sandboxCommand(ctx context.Context, sb Sandbox, root, command string) (*exec.Cmd, error) {
	if !sb.on() {
		return shellCommand(ctx, command), nil
	}
	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("shell sandbox unavailable: %w", err)
	}
	paths := sb.writable(root)
	args := append([]string{SandboxHelperArg, sb.Mode, strconv.Itoa(len(paths))}, paths...)
	return exec.CommandContext(ctx, self, append(args, command)...), nil
}

// RunSandboxHelper handles `cody __sandbox MODE N PATH... COMMAND`. On success
// it does not return: the process becomes bash. Any failure exits 126 with a
// message, so a command never runs without the sandbox it asked for.
func RunSandboxHelper(args []string) int {
	fail := func(err error) int {
		fmt.Fprintln(os.Stderr, "cody sandbox:", err)
		return 126
	}
	if len(args) < 3 {
		return fail(errors.New("usage: __sandbox MODE N PATH... COMMAND"))
	}
	n, err := strconv.Atoi(args[1])
	if err != nil || n < 0 || len(args) != n+3 {
		return fail(errors.New("malformed arguments"))
	}
	mode, paths, command := args[0], args[2:2+n], args[2+n]
	bash, err := exec.LookPath("bash")
	if err != nil {
		return fail(err)
	}
	// Landlock and no_new_privs apply to the calling thread, which must also
	// be the one that execs.
	runtime.LockOSThread()
	if err := landlock(mode, paths); err != nil {
		return fail(err)
	}
	return fail(unix.Exec(bash, []string{"bash", "-c", command}, os.Environ()))
}

const (
	llFileWrite = unix.LANDLOCK_ACCESS_FS_WRITE_FILE
	llDirWrite  = unix.LANDLOCK_ACCESS_FS_REMOVE_DIR | unix.LANDLOCK_ACCESS_FS_REMOVE_FILE |
		unix.LANDLOCK_ACCESS_FS_MAKE_CHAR | unix.LANDLOCK_ACCESS_FS_MAKE_DIR | unix.LANDLOCK_ACCESS_FS_MAKE_REG |
		unix.LANDLOCK_ACCESS_FS_MAKE_SOCK | unix.LANDLOCK_ACCESS_FS_MAKE_FIFO | unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK |
		unix.LANDLOCK_ACCESS_FS_MAKE_SYM
)

// landlock restricts writes to paths (plus /dev/null) and, in strict mode,
// all TCP. Reading and executing stay unrestricted.
func landlock(mode string, paths []string) error {
	abi, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	if errno != 0 {
		return fmt.Errorf("Landlock unavailable on this kernel (%v); use the container profile", errno)
	}
	fileAccess := uint64(llFileWrite)
	dirAccess := uint64(llFileWrite | llDirWrite)
	if abi >= 2 {
		dirAccess |= unix.LANDLOCK_ACCESS_FS_REFER
	}
	if abi >= 3 {
		fileAccess |= unix.LANDLOCK_ACCESS_FS_TRUNCATE
		dirAccess |= unix.LANDLOCK_ACCESS_FS_TRUNCATE
	}
	attr := unix.LandlockRulesetAttr{Handled_access_fs: dirAccess}
	if mode == "strict" {
		if abi < 4 {
			return fmt.Errorf("strict mode needs Landlock network rules (Linux 6.7+); this kernel has ABI %d", abi)
		}
		attr.Handled_access_net = unix.LANDLOCK_ACCESS_NET_BIND_TCP | unix.LANDLOCK_ACCESS_NET_CONNECT_TCP
	}
	fd, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0)
	if errno != 0 {
		return fmt.Errorf("create Landlock ruleset: %v", errno)
	}
	defer unix.Close(int(fd))
	for _, p := range append(paths, "/dev/null") {
		pfd, err := unix.Open(p, unix.O_PATH|unix.O_CLOEXEC, 0)
		if err != nil {
			continue // a missing optional folder (no cache dir yet) is not an error
		}
		var st unix.Stat_t
		access := dirAccess
		if unix.Fstat(pfd, &st) == nil && st.Mode&unix.S_IFMT != unix.S_IFDIR {
			access = fileAccess
		}
		rule := unix.LandlockPathBeneathAttr{Allowed_access: access, Parent_fd: int32(pfd)}
		_, _, errno := unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, fd, unix.LANDLOCK_RULE_PATH_BENEATH,
			uintptr(unsafe.Pointer(&rule)), 0, 0, 0)
		unix.Close(pfd)
		if errno != 0 {
			return fmt.Errorf("add Landlock rule for %s: %v", p, errno)
		}
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("set no_new_privs: %w", err)
	}
	if _, _, errno := unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, fd, 0, 0); errno != 0 {
		return fmt.Errorf("apply Landlock: %v", errno)
	}
	return nil
}
