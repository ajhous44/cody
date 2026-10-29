package tool

import (
	"os"
	"testing"
)

// Sandboxed bash on Linux re-executes the running binary as the sandbox
// helper; under go test that binary is this test program.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == SandboxHelperArg {
		os.Exit(RunSandboxHelper(os.Args[2:]))
	}
	os.Exit(m.Run())
}
