//go:build !windows

package runstate

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestCompletePostCreateTempFailuresLeaveNoResidue(t *testing.T) {
	tests := []struct {
		name  string
		hooks completionTempHooks
	}{
		{
			name: "chmod",
			hooks: completionTempHooks{
				chmod: func(*os.File, fs.FileMode) error { return errors.New("chmod fault") },
			},
		},
		{
			name: "stat",
			hooks: completionTempHooks{
				stat: func(*os.File) (fs.FileInfo, error) { return nil, errors.New("stat fault") },
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository, taskID, run := passingCompletionRun(t)
			_, err := completeWithHooks(repository, taskID, run.RunID, completeHooks{
				tempNameGenerator: func() (string, error) {
					return ".completion-0123456789abcdef0123456789abcdef.tmp", nil
				},
				tempHooks: test.hooks,
			})
			assertCompletionExit(t, err, 8)
			if _, statErr := os.Lstat(filepath.Join(run.EvidencePath, "completion.json")); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("completion destination exists after %s failure: %v", test.name, statErr)
			}
			assertNoCompletionTemps(t, run.EvidencePath)
		})
	}
}

func TestPrivateCompletionPostCreateCleanupPreservesReplacement(t *testing.T) {
	tests := []struct {
		name  string
		hooks func(func() error) completionTempHooks
	}{
		{
			name: "chmod",
			hooks: func(replace func() error) completionTempHooks {
				return completionTempHooks{
					chmod: func(*os.File, fs.FileMode) error { return replace() },
				}
			},
		},
		{
			name: "stat",
			hooks: func(replace func() error) completionTempHooks {
				return completionTempHooks{
					stat: func(*os.File) (fs.FileInfo, error) { return nil, replace() },
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			root, err := os.OpenRoot(directory)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()

			const name = ".completion-0123456789abcdef0123456789abcdef.tmp"
			moved := filepath.Join(directory, "created-file-moved")
			replacement := []byte("unrelated replacement\n")
			replace := func() error {
				if err := os.Rename(filepath.Join(directory, name), moved); err != nil {
					return err
				}
				if err := os.WriteFile(filepath.Join(directory, name), replacement, 0o600); err != nil {
					return err
				}
				return errors.New(test.name + " fault")
			}

			file, info, err := createPrivateCompletionTempWithHooks(root, name, test.hooks(replace))
			if err == nil || file != nil || info != nil {
				t.Fatalf("createPrivateCompletionTempWithHooks() = %v, %v, %v; want nil, nil, error", file, info, err)
			}
			got, readErr := os.ReadFile(filepath.Join(directory, name))
			if readErr != nil {
				t.Fatalf("replacement was removed: %v", readErr)
			}
			if string(got) != string(replacement) {
				t.Fatalf("replacement bytes = %q, want %q", got, replacement)
			}
			if _, statErr := os.Stat(moved); statErr != nil {
				t.Fatalf("created file object was not preserved under its replacement name: %v", statErr)
			}
		})
	}
}

func TestPrivateCompletionCleanupDoesNotRemoveNameWithoutDescriptorIdentity(t *testing.T) {
	directory := t.TempDir()
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	const name = ".completion-0123456789abcdef0123456789abcdef.tmp"
	moved := filepath.Join(directory, "created-file-moved")
	replacement := []byte("unrelated replacement\n")
	hooks := completionTempHooks{
		stat: func(file *os.File) (fs.FileInfo, error) {
			if err := os.Rename(filepath.Join(directory, name), moved); err != nil {
				return nil, err
			}
			if err := os.WriteFile(filepath.Join(directory, name), replacement, 0o600); err != nil {
				return nil, err
			}
			if err := file.Close(); err != nil {
				return nil, err
			}
			return nil, errors.New("binding stat fault")
		},
	}

	file, info, err := createPrivateCompletionTempWithHooks(root, name, hooks)
	if err == nil || file != nil || info != nil {
		t.Fatalf("createPrivateCompletionTempWithHooks() = %v, %v, %v; want nil, nil, error", file, info, err)
	}
	got, readErr := os.ReadFile(filepath.Join(directory, name))
	if readErr != nil {
		t.Fatalf("unbound replacement was removed: %v", readErr)
	}
	if string(got) != string(replacement) {
		t.Fatalf("replacement bytes = %q, want %q", got, replacement)
	}
	if _, statErr := os.Stat(moved); statErr != nil {
		t.Fatalf("created file object was not preserved after identity became unavailable: %v", statErr)
	}
}
