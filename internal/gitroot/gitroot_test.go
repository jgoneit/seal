package gitroot

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestFindReturnsResolvedTopLevelFromNestedDirectory(t *testing.T) {
	root := t.TempDir()
	if output, err := exec.Command("git", "-C", root, "init", "--quiet").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Find(nested)
	if err != nil || got != want {
		t.Fatalf("Find() = %q, %v; want %q", got, err, want)
	}
}

func TestFindClassifiesNonRepository(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(directory))
	if _, err := Find(directory); !errors.Is(err, ErrNotRepository) {
		t.Fatalf("Find() error = %v, want ErrNotRepository", err)
	}
}

func TestFindClassifiesMissingGit(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := Find(t.TempDir()); !errors.Is(err, ErrGitUnavailable) {
		t.Fatalf("Find() error = %v, want ErrGitUnavailable", err)
	}
}
