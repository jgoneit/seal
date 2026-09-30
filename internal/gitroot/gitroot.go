// Package gitroot resolves the enclosing Git worktree the way the frozen
// Reference's task and verify commands do: by asking Git with
// `git rev-parse --show-toplevel` in the caller's environment.
package gitroot

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

var (
	// ErrGitUnavailable reports that the git executable could not be started.
	ErrGitUnavailable = errors.New("git executable is unavailable")
	// ErrNotRepository reports that cwd is not inside a Git worktree.
	ErrNotRepository = errors.New("not inside a Git worktree")
)

// Find returns the resolved top-level directory of the worktree enclosing cwd.
// An empty cwd means the process working directory.
func Find(cwd string) (string, error) {
	if cwd == "" {
		cwd = "."
	}

	command := exec.Command("git", "-C", cwd, "rev-parse", "--show-toplevel")
	stdout, err := command.Output()
	if err != nil {
		var executableError *exec.Error
		if errors.As(err, &executableError) {
			return "", fmt.Errorf("%w: %w", ErrGitUnavailable, err)
		}
		return "", fmt.Errorf("%w: %w", ErrNotRepository, err)
	}

	root := strings.TrimSpace(string(stdout))
	if root == "" {
		return "", ErrNotRepository
	}

	resolved, err := filepath.EvalSymlinks(root)
	if err == nil {
		return resolved, nil
	}
	absolute, absoluteError := filepath.Abs(root)
	if absoluteError == nil {
		return filepath.Clean(absolute), nil
	}
	return filepath.Clean(root), nil
}
