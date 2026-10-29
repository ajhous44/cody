//go:build !darwin && !linux && !windows

package term

import "errors"

func IsTerminal(fd uintptr) bool { return false }
func MakeRaw(fd uintptr) (func() error, error) {
	return nil, errors.New("raw terminal mode is not supported on this OS")
}
func Width(fd uintptr) int { return 0 }
func EnableVT(fd uintptr)  {}
