package term

import "golang.org/x/sys/windows"

func IsTerminal(fd uintptr) bool {
	var mode uint32
	return windows.GetConsoleMode(windows.Handle(fd), &mode) == nil
}

// MakeRaw delivers keys one at a time as VT sequences, without echo or
// Ctrl+C processing.
func MakeRaw(fd uintptr) (restore func() error, err error) {
	h := windows.Handle(fd)
	var old uint32
	if err := windows.GetConsoleMode(h, &old); err != nil {
		return nil, err
	}
	raw := old&^(windows.ENABLE_ECHO_INPUT|windows.ENABLE_PROCESSED_INPUT|windows.ENABLE_LINE_INPUT) | windows.ENABLE_VIRTUAL_TERMINAL_INPUT
	if err := windows.SetConsoleMode(h, raw); err != nil {
		return nil, err
	}
	return func() error { return windows.SetConsoleMode(h, old) }, nil
}

func Width(fd uintptr) int {
	var info windows.ConsoleScreenBufferInfo
	if err := windows.GetConsoleScreenBufferInfo(windows.Handle(fd), &info); err != nil {
		return 0
	}
	return int(info.Window.Right - info.Window.Left + 1)
}

// EnableVT turns on ANSI escape handling for a console output handle.
func EnableVT(fd uintptr) {
	h := windows.Handle(fd)
	var mode uint32
	if windows.GetConsoleMode(h, &mode) == nil {
		_ = windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING)
	}
}
