package tool

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Let Git interpret nested ignore rules, negations, and the user's global excludes.
// Outside Git (or without Git), retain the existing directory exclusions.
func walkSearch(ctx context.Context, root, base string, visit func(string) error) error {
	probe, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(probe, "git", "-C", root, "ls-files", "--cached", "--others", "--exclude-standard", "--deduplicate", "-z", "--", base)
	pipe, err := cmd.StdoutPipe()
	if err == nil {
		err = cmd.Start()
	}
	if err == nil {
		const maxPaths = 32 << 20
		data, readErr := io.ReadAll(io.LimitReader(pipe, maxPaths+1))
		if len(data) > maxPaths || readErr != nil {
			cancel()
		}
		waitErr := cmd.Wait()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if readErr != nil {
			return readErr
		}
		if len(data) > maxPaths {
			return fmt.Errorf("too many files; narrow the search path")
		}
		if probe.Err() != nil {
			return probe.Err()
		}
		if waitErr == nil {
			for _, rel := range strings.Split(string(data), "\x00") {
				if err := ctx.Err(); err != nil {
					return err
				}
				if rel == "" {
					continue
				}
				skip := false
				parts := strings.Split(rel, "/")
				for _, part := range parts[:len(parts)-1] {
					if skipSearchDir(part) {
						skip = true
						break
					}
				}
				if skip {
					continue
				}
				if err := visit(rel); err != nil {
					if err == fs.SkipAll {
						return nil
					}
					return err
				}
			}
			return nil
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer dir.Close()
	return fs.WalkDir(dir.FS(), filepath.ToSlash(base), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			if p != base && skipSearchDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		return visit(p)
	})
}
