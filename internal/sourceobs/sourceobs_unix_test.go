//go:build !windows

package sourceobs

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestObserveRejectsInvalidUTF8Filename(t *testing.T) {
	repository, baseline := basicFixture(t)
	name := string([]byte{'b', 'a', 'd', '-', 0xff})
	if err := os.WriteFile(filepath.Join(repository.root, name), []byte("bad\n"), 0o644); err != nil {
		t.Skipf("filesystem does not support an invalid UTF-8 filename: %v", err)
	}
	assertErrorKind(t, repository.observe(baseline), RepositoryState, "UTF-8")
}

func TestObserveRejectsNonIgnoredFIFO(t *testing.T) {
	repository, baseline := basicFixture(t)
	if err := syscall.Mkfifo(filepath.Join(repository.root, "source.pipe"), 0o600); err != nil {
		t.Skipf("FIFO is unavailable: %v", err)
	}
	assertErrorKind(t, repository.observe(baseline), RepositoryState, "FIFO")
}
