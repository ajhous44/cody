package bench

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Task struct {
	Name        string
	Description string
	Prompt      string
	Setup       func(dir string) error
	Verify      func(dir, answer string) (bool, string)
	Timeout     time.Duration
}

func Builtin() []Task {
	return []Task{
		readTask(),
		writeTask(),
		editTask(),
		shellTask(),
		fixTestTask(),
	}
}

func readTask() Task {
	return Task{
		Name:        "read",
		Description: "read a file and answer a question about it",
		Prompt:      "Read NOTES.md and reply with the build codeword it contains. Reply with the codeword only, nothing else.",
		Setup: func(dir string) error {
			return os.WriteFile(filepath.Join(dir, "NOTES.md"), []byte(
				"# Notes\n\nThe build codeword is ORANGE-FALCON-7.\nDo not share it outside the team.\n"), 0o644)
		},
		Verify: func(dir, answer string) (bool, string) {
			if strings.Contains(strings.ToUpper(answer), "ORANGE-FALCON-7") {
				return true, "codeword found"
			}
			return false, "codeword missing from reply"
		},
	}
}

func writeTask() Task {
	return Task{
		Name:        "write",
		Description: "create a new file with requested content",
		Prompt:      "Create hello.go in the workspace root with a package-level function Greet() string that returns exactly \"hello from cody\". Do not create any other files.",
		Verify: func(dir, answer string) (bool, string) {
			b, err := os.ReadFile(filepath.Join(dir, "hello.go"))
			if err != nil {
				return false, "hello.go not created"
			}
			s := string(b)
			if strings.Contains(s, "func Greet()") && strings.Contains(s, "hello from cody") {
				return true, "file matches"
			}
			return false, "file content does not match"
		},
	}
}

func editTask() Task {
	return Task{
		Name:        "edit",
		Description: "change one value in an existing file",
		Prompt:      "In app.go, change MaxRetries from 3 to 7. Make only that change and keep the file valid Go.",
		Setup: func(dir string) error {
			return os.WriteFile(filepath.Join(dir, "app.go"), []byte(`package main

import "fmt"

const MaxRetries = 3

func main() {
	fmt.Println("retries:", MaxRetries)
}
`), 0o644)
		},
		Verify: func(dir, answer string) (bool, string) {
			b, err := os.ReadFile(filepath.Join(dir, "app.go"))
			if err != nil {
				return false, "app.go missing"
			}
			s := string(b)
			if strings.Contains(s, "MaxRetries = 7") && !strings.Contains(s, "MaxRetries = 3") {
				return true, "value updated"
			}
			return false, "value not updated"
		},
	}
}

func shellTask() Task {
	return Task{
		Name:        "shell",
		Description: "use bash to produce a file",
		Prompt:      "Use the bash tool to create a file named out.txt in the workspace root containing exactly the text cody-bench followed by a newline. Then confirm it exists.",
		Verify: func(dir, answer string) (bool, string) {
			b, err := os.ReadFile(filepath.Join(dir, "out.txt"))
			if err != nil {
				return false, "out.txt missing"
			}
			if strings.TrimSpace(string(b)) == "cody-bench" {
				return true, "out.txt correct"
			}
			return false, "out.txt has unexpected content"
		},
	}
}

func fixTestTask() Task {
	return Task{
		Name:        "fix-test",
		Description: "find and fix a bug so the test suite passes",
		Prompt:      "The tests in this directory fail. Run them, find the bug, fix it with a minimal edit, and run the tests again to confirm they pass. Report the failing test and the fix.",
		Timeout:     6 * time.Minute,
		Setup: func(dir string) error {
			files := map[string]string{
				"go.mod": "module fixture\n\ngo 1.24\n",
				"mathx/mathx.go": `package mathx

func Add(a, b int) int {
	return a - b
}
`,
				"mathx/mathx_test.go": `package mathx

import "testing"

func TestAdd(t *testing.T) {
	if got := Add(2, 3); got != 5 {
		t.Fatalf("Add(2, 3) = %d, want 5", got)
	}
}
`,
			}
			for name, content := range files {
				path := filepath.Join(dir, filepath.FromSlash(name))
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					return err
				}
				if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
					return err
				}
			}
			return nil
		},
		Verify: func(dir, answer string) (bool, string) {
			return runGoTests(dir)
		},
	}
}
