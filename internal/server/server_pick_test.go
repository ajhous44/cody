package server

import (
	"context"
	"errors"
	"os/exec"
	"testing"
)

func TestPickerFailureAllowsManualFallback(t *testing.T) {
	for _, tc := range []struct {
		name, platform string
		err            error
		want           error
	}{
		{"user cancelled", "darwin", &exec.ExitError{Stderr: []byte("execution error: User canceled. (-128)")}, errPickerCancelled},
		{"automation denied", "darwin", &exec.ExitError{Stderr: []byte("not authorized to send Apple events. (-1743)")}, errPickerUnavailable},
		{"helper missing", "darwin", exec.ErrNotFound, errPickerUnavailable},
		{"no display", "linux", &exec.ExitError{Stderr: []byte("cannot open display")}, errPickerUnavailable},
		{"powershell failed", "windows", &exec.ExitError{Stderr: []byte("unable to load dialog")}, errPickerUnavailable},
		{"timed out", "darwin", context.DeadlineExceeded, errPickerUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := pickerError(tc.platform, tc.err); !errors.Is(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
