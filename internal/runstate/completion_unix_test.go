//go:build darwin || linux

package runstate

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestCompleteRejectsFIFOCompletionDestination(t *testing.T) {
	repository, taskID, run := passingCompletionRun(t)
	path := filepath.Join(run.EvidencePath, "completion.json")
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Complete(repository, taskID, run.RunID)
	assertCompletionExit(t, err, 8)
	info, statErr := os.Lstat(path)
	if statErr != nil || info.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("FIFO destination changed: %v, %v", info, statErr)
	}
	assertNoCompletionTemps(t, run.EvidencePath)
}
