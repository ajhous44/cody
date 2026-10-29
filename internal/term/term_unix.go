//go:build darwin || linux

package term

import "golang.org/x/sys/unix"

func IsTerminal(fd uintptr) bool {
	_, err := unix.IoctlGetTermios(int(fd), ioctlGetTermios)
	return err == nil
}

// MakeRaw turns off line buffering, echo, and signal keys (Ctrl+C arrives as
// a byte), but keeps output processing so "\n" still starts a new line.
func MakeRaw(fd uintptr) (restore func() error, err error) {
	old, err := unix.IoctlGetTermios(int(fd), ioctlGetTermios)
	if err != nil {
		return nil, err
	}
	raw := *old
	raw.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	raw.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	raw.Cflag &^= unix.CSIZE | unix.PARENB
	raw.Cflag |= unix.CS8
	raw.Cc[unix.VMIN] = 1
	raw.Cc[unix.VTIME] = 0
	if err := unix.IoctlSetTermios(int(fd), ioctlSetTermios, &raw); err != nil {
		return nil, err
	}
	return func() error { return unix.IoctlSetTermios(int(fd), ioctlSetTermios, old) }, nil
}

// Width is the terminal's column count, or 0 when unknown.
func Width(fd uintptr) int {
	ws, err := unix.IoctlGetWinsize(int(fd), unix.TIOCGWINSZ)
	if err != nil {
		return 0
	}
	return int(ws.Col)
}

// EnableVT is needed only on Windows; Unix terminals understand ANSI already.
func EnableVT(fd uintptr) {}
