package sourceobs

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
)

type fixtureRepository struct {
	t    *testing.T
	root string
}

func TestObserveCleanCanonicalDocumentsAndDetachedResults(t *testing.T) {
	repository := newFixtureRepository(t, "sha1")
	repository.write("src/base.txt", []byte("baseline\n"), 0o644)
	baseline := repository.commit("baseline")

	result, err := Observe(Request{CWD: filepath.Join(repository.root, "src"), Baseline: baseline, Scope: []string{"src"}})
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	if entries := result.Snapshot().Entries; len(entries) != 0 {
		t.Fatalf("Snapshot entries = %#v, want clean", entries)
	}
	payload := fmt.Sprintf(`{"baseline":"%s","entries":[],"schema_version":1}`, baseline)
	digest := sha256.Sum256([]byte(payload))
	wantDigest := hex.EncodeToString(digest[:])
	if got := result.SnapshotSHA256(); got != wantDigest {
		t.Fatalf("SnapshotSHA256() = %q, want %q", got, wantDigest)
	}
	wantSnapshotJSON := fmt.Sprintf("{\n  \"baseline\": \"%s\",\n  \"entries\": [],\n  \"schema_version\": 1,\n  \"snapshot_sha256\": \"%s\"\n}\n", baseline, wantDigest)
	if got := string(result.SnapshotJSON()); got != wantSnapshotJSON {
		t.Fatalf("SnapshotJSON() =\n%s\nwant:\n%s", got, wantSnapshotJSON)
	}
	wantChangedJSON := fmt.Sprintf("{\n  \"baseline\": \"%s\",\n  \"changes\": [],\n  \"schema_version\": 1,\n  \"scope\": [\n    \"src\"\n  ]\n}\n", baseline)
	if got := string(result.ChangedFilesJSON()); got != wantChangedJSON {
		t.Fatalf("ChangedFilesJSON() =\n%s\nwant:\n%s", got, wantChangedJSON)
	}
	if got := result.DiffPatch(); len(got) != 0 {
		t.Fatalf("DiffPatch() = %q, want empty", got)
	}

	snapshotBytes := result.SnapshotJSON()
	snapshotBytes[0] = 'x'
	changedBytes := result.ChangedFilesJSON()
	changedBytes[0] = 'x'
	diffBytes := result.DiffPatch()
	diffBytes = append(diffBytes, 'x')
	snapshot := result.Snapshot()
	snapshot.Entries = append(snapshot.Entries, Entry{Path: "mutated"})
	changes := result.Changes()
	changes.Scope[0] = "mutated"
	changes.Changes = append(changes.Changes, Change{Path: "mutated"})
	if got := string(result.SnapshotJSON()); got != wantSnapshotJSON {
		t.Fatal("SnapshotJSON exposed mutable storage")
	}
	if got := string(result.ChangedFilesJSON()); got != wantChangedJSON {
		t.Fatal("ChangedFilesJSON exposed mutable storage")
	}
	if len(result.Snapshot().Entries) != 0 || result.Changes().Scope[0] != "src" || len(result.Changes().Changes) != 0 || len(result.DiffPatch()) != 0 {
		t.Fatal("Result accessors exposed mutable model storage")
	}
}

func TestSnapshotDigestUsesReferenceASCIIJSON(t *testing.T) {
	baseline := strings.Repeat("a", 40)
	mode, size, contentDigest := "100644", int64(7), strings.Repeat("b", 64)
	path := "src/é😀\x7f.txt"
	snapshot, artifact, err := buildSnapshot(baseline, []Entry{{
		Path: path, State: "present", Mode: &mode, SizeBytes: &size, SHA256: &contentDigest,
	}})
	if err != nil {
		t.Fatalf("buildSnapshot() error = %v", err)
	}
	payload := `{"baseline":"` + baseline + `","entries":[{"mode":"100644","path":"src/\u00e9\ud83d\ude00\u007f.txt","sha256":"` + contentDigest + `","size_bytes":7,"state":"present"}],"schema_version":1}`
	want := sha256.Sum256([]byte(payload))
	if snapshot.SHA256 != hex.EncodeToString(want[:]) {
		t.Fatalf("digest = %q, want digest of %q", snapshot.SHA256, payload)
	}
	if !bytes.Contains(artifact, []byte(path)) || bytes.Contains(artifact, []byte(`\u00e9`)) {
		t.Fatalf("pretty artifact does not preserve UTF-8 path: %q", artifact)
	}
}

func TestSnapshotIdentityIsIndependentOfGitLayer(t *testing.T) {
	repository, baseline := basicFixture(t)
	repository.write("src/base.txt", []byte("same final bytes\n"), 0o644)
	unstaged, err := Observe(Request{CWD: repository.root, Baseline: baseline, Scope: []string{"src"}})
	if err != nil {
		t.Fatalf("Observe(unstaged) error = %v", err)
	}
	repository.git("add", "src/base.txt")
	staged, err := Observe(Request{CWD: repository.root, Baseline: baseline, Scope: []string{"src"}})
	if err != nil {
		t.Fatalf("Observe(staged) error = %v", err)
	}
	repository.git("commit", "-q", "-m", "same final bytes")
	committed, err := Observe(Request{CWD: repository.root, Baseline: baseline, Scope: []string{"src"}})
	if err != nil {
		t.Fatalf("Observe(committed) error = %v", err)
	}
	if !bytes.Equal(unstaged.SnapshotJSON(), staged.SnapshotJSON()) || !bytes.Equal(staged.SnapshotJSON(), committed.SnapshotJSON()) {
		t.Fatalf("snapshot identity changed across layers:\nunstaged=%s\nstaged=%s\ncommitted=%s", unstaged.SnapshotJSON(), staged.SnapshotJSON(), committed.SnapshotJSON())
	}
}

func TestSnapshotObservationIgnoresConcurrentSealMetadata(t *testing.T) {
	repository, baseline := basicFixture(t)
	context, err := resolveContext(repository.root, baseline)
	if err != nil {
		t.Fatal(err)
	}
	baselineBlobs := make(map[string]blobIdentity)
	before, err := collectSnapshotObservation(context, baselineBlobs)
	if err != nil {
		t.Fatal(err)
	}
	repository.write(
		".seal/evidence/TASK/.tmp-run/checks/000-check.stdout",
		[]byte("concurrent Evidence\n"),
		0o600,
	)
	after, err := collectSnapshotObservation(context, baselineBlobs)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("Seal metadata changed snapshot stability:\nbefore=%#v\nafter=%#v", before, after)
	}

	repository.write("product.txt", []byte("product change\n"), 0o644)
	changed, err := collectSnapshotObservation(context, baselineBlobs)
	if err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(after, changed) {
		t.Fatal("product source mutation did not change snapshot observation")
	}
}

func TestPhaseAPIsKeepSnapshotsBeforeLayeredChanges(t *testing.T) {
	repository, baseline := basicFixture(t)
	repository.write("src/base.txt", []byte("before checks\n"), 0o644)

	s0, err := ObserveSnapshot(SnapshotRequest{CWD: repository.root, Baseline: baseline})
	if err != nil {
		t.Fatalf("ObserveSnapshot(S0) error = %v", err)
	}
	if _, exposed := reflect.TypeOf(s0).MethodByName("Changes"); exposed {
		t.Fatal("SnapshotResult exposes layered changes")
	}
	if _, exposed := reflect.TypeOf(s0).MethodByName("DiffPatch"); exposed {
		t.Fatal("SnapshotResult exposes a raw diff")
	}
	s0Bytes := s0.SnapshotJSON()
	s0Bytes[0] = 'x'
	if s0.SnapshotJSON()[0] == 'x' {
		t.Fatal("SnapshotResult exposes artifact byte storage")
	}

	// Simulate source mutation by checks between the required S0 and S1 calls.
	repository.write("src/base.txt", []byte("after checks\n"), 0o644)
	s1, err := ObserveSnapshot(SnapshotRequest{CWD: repository.root, Baseline: baseline})
	if err != nil {
		t.Fatalf("ObserveSnapshot(S1) error = %v", err)
	}
	if bytes.Equal(s0.SnapshotJSON(), s1.SnapshotJSON()) {
		t.Fatal("S0 and S1 snapshots did not detect changed final source")
	}

	changes, err := ObserveChanges(Request{CWD: repository.root, Baseline: baseline, Scope: []string{"src"}})
	if err != nil {
		t.Fatalf("ObserveChanges(post-S1) error = %v", err)
	}
	if _, exposed := reflect.TypeOf(changes).MethodByName("Snapshot"); exposed {
		t.Fatal("ChangeResult exposes a Source Snapshot")
	}
	changeModel := changes.Changes()
	changeModel.Scope[0] = "mutated"
	if changes.Changes().Scope[0] != "src" {
		t.Fatal("ChangeResult exposes model storage")
	}
	findChange(t, changes.Changes().Changes, "unstaged", "modified", "src/base.txt")
	if !bytes.Contains(changes.DiffPatch(), []byte("after checks")) {
		t.Fatalf("post-S1 diff does not contain final bytes:\n%s", changes.DiffPatch())
	}
	if _, err := ObserveChanges(Request{CWD: repository.root, Baseline: baseline}); err == nil {
		t.Fatal("ObserveChanges without Scope succeeded")
	} else {
		assertErrorKind(t, err, InvalidRequest, "scope")
	}
}

func TestObservePreservesLayersAndProductBoundary(t *testing.T) {
	repository := newFixtureRepository(t, "sha1")
	repository.write(".gitignore", []byte("ignored.txt\n"), 0o644)
	repository.write("src/committed.txt", []byte("base committed\n"), 0o644)
	repository.write("src/staged.txt", []byte("base staged\n"), 0o644)
	repository.write("src/delete.txt", []byte("delete me\n"), 0o644)
	repository.write("src/old.txt", []byte("rename me\n"), 0o644)
	if runtime.GOOS != "windows" {
		repository.write("src/mode.sh", []byte("#!/bin/sh\n"), 0o644)
	}
	baseline := repository.commit("baseline")

	repository.rename("src/old.txt", "outside/new.txt")
	repository.write("src/committed.txt", []byte("committed change\n"), 0o644)
	repository.git("add", "-A")
	repository.git("commit", "-q", "-m", "committed layer")
	repository.write("src/staged.txt", []byte("staged change\n"), 0o644)
	repository.write("src/added.txt", []byte("staged add\n"), 0o644)
	repository.git("add", "src/staged.txt", "src/added.txt")
	repository.write("src/staged.txt", []byte("working change\n"), 0o644)
	repository.remove("src/delete.txt")
	if runtime.GOOS != "windows" {
		if err := os.Chmod(filepath.Join(repository.root, "src", "mode.sh"), 0o755); err != nil {
			t.Fatalf("chmod tracked executable: %v", err)
		}
	}
	repository.write("src/tool.sh", []byte("#!/bin/sh\nexit 0\n"), 0o755)
	repository.write("outside/blob.bin", []byte{0, 1, 2, 0xff}, 0o644)
	repository.write("ignored.txt", []byte("ignored\n"), 0o644)
	repository.write(".seal/tasks/task.json", []byte("task metadata\n"), 0o644)
	repository.write(".seal/evidence/run/artifact", []byte("evidence metadata\n"), 0o644)
	repository.write(".seal/config.json", []byte("config metadata\n"), 0o644)
	repository.write(".seal/lessons.md", []byte("lessons metadata\n"), 0o644)
	repository.write(".seal/runs.jsonl", []byte("runs metadata\n"), 0o644)
	repository.write(".seal/checks.json", []byte("checks product\n"), 0o644)
	if runtime.GOOS != "windows" {
		repository.symlink("../missing-target", "src/link")
	}

	result, err := Observe(Request{CWD: repository.root, Baseline: baseline, Scope: []string{"src"}})
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	changes := result.Changes()
	if changes.ScopePassed() {
		t.Fatal("ScopePassed() = true, want out-of-scope product changes")
	}
	assertLayerOrder(t, changes.Changes)
	rename := findChange(t, changes.Changes, "committed", "renamed", "outside/new.txt")
	if rename.PreviousPath == nil || *rename.PreviousPath != "src/old.txt" || rename.InScope {
		t.Fatalf("rename = %#v, want both-path Scope violation", rename)
	}
	binary := findChange(t, changes.Changes, "untracked", "untracked", "outside/blob.bin")
	if !binary.Binary {
		t.Fatalf("binary change = %#v, want is_binary", binary)
	}
	findChange(t, changes.Changes, "staged", "added", "src/added.txt")
	findChange(t, changes.Changes, "unstaged", "deleted", "src/delete.txt")
	if runtime.GOOS != "windows" {
		modeChange := findChange(t, changes.Changes, "unstaged", "modified", "src/mode.sh")
		if !modeChange.ModeChanged || modeChange.OldMode == nil || *modeChange.OldMode != "100644" || modeChange.NewMode == nil || *modeChange.NewMode != "100755" {
			t.Fatalf("mode change = %#v, want 100644 -> 100755", modeChange)
		}
	}
	findChange(t, changes.ProductChanges, "untracked", "untracked", ".seal/checks.json")
	for _, path := range []string{".seal/tasks/task.json", ".seal/evidence/run/artifact", ".seal/config.json", ".seal/lessons.md", ".seal/runs.jsonl"} {
		findChange(t, changes.MetadataChanges, "untracked", "untracked", path)
		if snapshotEntry(result.Snapshot().Entries, path) != nil {
			t.Fatalf("metadata path %q leaked into Source Snapshot", path)
		}
	}
	if snapshotEntry(result.Snapshot().Entries, "ignored.txt") != nil {
		t.Fatal("Git-ignored untracked file leaked into Source Snapshot")
	}
	checksEntry := snapshotEntry(result.Snapshot().Entries, ".seal/checks.json")
	if checksEntry == nil || checksEntry.State != "present" {
		t.Fatalf(".seal/checks.json entry = %#v, want included product source", checksEntry)
	}
	*checksEntry.Mode = "mutated"
	if fresh := snapshotEntry(result.Snapshot().Entries, ".seal/checks.json"); fresh == nil || fresh.Mode == nil || *fresh.Mode == "mutated" {
		t.Fatal("Snapshot() exposed entry pointer storage")
	}
	*rename.PreviousPath = "mutated"
	if fresh := findChange(t, result.Changes().Changes, "committed", "renamed", "outside/new.txt"); fresh.PreviousPath == nil || *fresh.PreviousPath != "src/old.txt" {
		t.Fatal("Changes() exposed previous_path pointer storage")
	}
	toolEntry := snapshotEntry(result.Snapshot().Entries, "src/tool.sh")
	if toolEntry == nil || toolEntry.Mode == nil || *toolEntry.Mode != "100755" {
		t.Fatalf("tool entry = %#v, want executable mode", toolEntry)
	}
	if runtime.GOOS != "windows" {
		linkEntry := snapshotEntry(result.Snapshot().Entries, "src/link")
		if linkEntry == nil || linkEntry.Mode == nil || *linkEntry.Mode != "120000" {
			t.Fatalf("link entry = %#v, want Git symlink", linkEntry)
		}
		want := sha256.Sum256([]byte("../missing-target"))
		if linkEntry.SHA256 == nil || *linkEntry.SHA256 != hex.EncodeToString(want[:]) {
			t.Fatalf("link digest = %#v, want target-text digest", linkEntry.SHA256)
		}
	}
	if !json.Valid(result.SnapshotJSON()) || !json.Valid(result.ChangedFilesJSON()) {
		t.Fatal("rendered Source Observation documents are not valid JSON")
	}
	patch := result.DiffPatch()
	if !bytes.Contains(patch, []byte("GIT binary patch")) || !bytes.Contains(patch, []byte(".seal/checks.json")) {
		t.Fatalf("DiffPatch() lacks binary/product content:\n%s", patch)
	}
	for _, forbidden := range [][]byte{[]byte("task metadata"), []byte("evidence metadata"), []byte("config metadata"), []byte("lessons metadata"), []byte("runs metadata")} {
		if bytes.Contains(patch, forbidden) {
			t.Fatalf("DiffPatch() contains Seal metadata %q", forbidden)
		}
	}
}

func TestObserveSupportsDetachedLinkedWorktree(t *testing.T) {
	repository := newFixtureRepository(t, "sha1")
	repository.write("src/base.txt", []byte("base\n"), 0o644)
	baseline := repository.commit("baseline")
	linked := filepath.Join(t.TempDir(), "linked")
	repository.git("worktree", "add", "-q", "--detach", linked, baseline)
	t.Cleanup(func() { repository.git("worktree", "remove", "--force", linked) })

	result, err := Observe(Request{CWD: linked, Baseline: baseline, Scope: []string{"src"}})
	if err != nil {
		t.Fatalf("Observe(linked detached worktree) error = %v", err)
	}
	if len(result.Snapshot().Entries) != 0 || len(result.Changes().Changes) != 0 {
		t.Fatalf("linked clean result = %#v / %#v", result.Snapshot(), result.Changes())
	}
}

func TestObserveSupportsSHA256Repository(t *testing.T) {
	repository, ok := tryNewFixtureRepository(t, "sha256")
	if !ok {
		t.Skip("Git does not support SHA-256 repositories")
	}
	repository.write("src/base.txt", []byte("base\n"), 0o644)
	baseline := repository.commit("baseline")
	if len(baseline) != 64 {
		t.Fatalf("SHA-256 baseline length = %d, want 64", len(baseline))
	}
	if _, err := Observe(Request{CWD: repository.root, Baseline: baseline, Scope: []string{"."}}); err != nil {
		t.Fatalf("Observe(SHA-256) error = %v", err)
	}
}

func TestObserveRejectsHiddenAndConflictedRepositoryState(t *testing.T) {
	t.Run("replace ref", func(t *testing.T) {
		repository, baseline := basicFixture(t)
		repository.write("src/base.txt", []byte("replacement\n"), 0o644)
		replacement := repository.commit("replacement")
		repository.git("replace", baseline, replacement)
		assertErrorKind(t, repository.observe(baseline), RepositoryState, "replacement")
	})
	t.Run("assume unchanged", func(t *testing.T) {
		repository, baseline := basicFixture(t)
		repository.git("update-index", "--assume-unchanged", "src/base.txt")
		assertErrorKind(t, repository.observe(baseline), RepositoryState, "assume-unchanged")
	})
	t.Run("skip worktree", func(t *testing.T) {
		repository, baseline := basicFixture(t)
		repository.git("update-index", "--skip-worktree", "src/base.txt")
		assertErrorKind(t, repository.observe(baseline), RepositoryState, "skip-worktree")
	})
	t.Run("sparse checkout", func(t *testing.T) {
		repository, baseline := basicFixture(t)
		repository.git("config", "core.sparseCheckout", "true")
		assertErrorKind(t, repository.observe(baseline), RepositoryState, "Sparse")
	})
	t.Run("unmerged", func(t *testing.T) {
		repository, baseline := basicFixture(t)
		repository.git("checkout", "-q", "-b", "other")
		repository.write("src/base.txt", []byte("other\n"), 0o644)
		repository.commit("other")
		repository.git("checkout", "-q", "main")
		repository.write("src/base.txt", []byte("main\n"), 0o644)
		repository.commit("main")
		repository.gitAllowed([]int{1}, "merge", "--no-edit", "other")
		assertErrorKind(t, repository.observe(baseline), RepositoryState, "Unmerged")
	})
	t.Run("custom replace base", func(t *testing.T) {
		repository, baseline := basicFixture(t)
		t.Setenv("GIT_REPLACE_REF_BASE", "refs/custom-replace")
		assertErrorKind(t, repository.observe(baseline), RepositoryState, "replacement")
	})
}

func TestObserveAllowsUnchangedGitlinkAndRejectsMutation(t *testing.T) {
	repository := newFixtureRepository(t, "sha1")
	repository.write("seed.txt", []byte("one\n"), 0o644)
	first := repository.commit("first")
	repository.write("seed.txt", []byte("two\n"), 0o644)
	second := repository.commit("second")
	repository.git("update-index", "--add", "--cacheinfo", "160000,"+first+",vendor/sub")
	repository.git("commit", "-q", "-m", "gitlink baseline")
	baseline := strings.TrimSpace(repository.git("rev-parse", "HEAD"))

	result, err := Observe(Request{CWD: repository.root, Baseline: baseline, Scope: []string{"."}})
	if err != nil {
		t.Fatalf("Observe(unchanged gitlink) error = %v", err)
	}
	if snapshotEntry(result.Snapshot().Entries, "vendor/sub") != nil {
		t.Fatal("unchanged gitlink leaked into product snapshot")
	}
	if err := os.MkdirAll(filepath.Join(repository.root, "vendor", "sub"), 0o755); err != nil {
		t.Fatalf("create unchanged gitlink directory: %v", err)
	}
	if _, err := Observe(Request{CWD: repository.root, Baseline: baseline, Scope: []string{"."}}); err != nil {
		t.Fatalf("Observe(unchanged gitlink directory) error = %v", err)
	}
	if err := os.RemoveAll(filepath.Join(repository.root, "vendor", "sub")); err != nil {
		t.Fatalf("remove unchanged gitlink directory: %v", err)
	}
	if err := os.Remove(filepath.Join(repository.root, "vendor")); err != nil {
		t.Fatalf("remove gitlink parent directory: %v", err)
	}
	repository.write("vendor", []byte("not a directory\n"), 0o644)
	assertErrorKind(t, repository.observe(baseline), RepositoryState, "Gitlink")
	repository.remove("vendor")
	repository.write("vendor/sub", []byte("not a submodule directory\n"), 0o644)
	assertErrorKind(t, repository.observe(baseline), RepositoryState, "Gitlink")
	repository.remove("vendor/sub")
	repository.git("update-index", "--cacheinfo", "160000,"+second+",vendor/sub")
	assertErrorKind(t, repository.observe(baseline), RepositoryState, "Gitlink")
}

func TestObserveRejectsInvalidRequest(t *testing.T) {
	repository, baseline := basicFixture(t)
	for _, request := range []Request{
		{CWD: repository.root, Baseline: baseline[:12], Scope: []string{"src"}},
		{CWD: repository.root, Baseline: baseline, Scope: nil},
		{CWD: repository.root, Baseline: baseline, Scope: []string{"../src"}},
	} {
		_, err := Observe(request)
		assertErrorKind(t, err, InvalidRequest, "")
	}
}

func TestDecodeGitPathRejectsInvalidUTF8(t *testing.T) {
	_, err := decodeGitPath([]byte{'b', 'a', 'd', 0xff})
	assertErrorKind(t, err, RepositoryState, "UTF-8")
}

func basicFixture(t *testing.T) (*fixtureRepository, string) {
	t.Helper()
	repository := newFixtureRepository(t, "sha1")
	repository.write("src/base.txt", []byte("base\n"), 0o644)
	return repository, repository.commit("baseline")
}

func newFixtureRepository(t *testing.T, objectFormat string) *fixtureRepository {
	t.Helper()
	repository, ok := tryNewFixtureRepository(t, objectFormat)
	if !ok {
		t.Fatalf("could not initialize %s Git repository", objectFormat)
	}
	return repository
}

func tryNewFixtureRepository(t *testing.T, objectFormat string) (*fixtureRepository, bool) {
	t.Helper()
	repository := &fixtureRepository{t: t, root: t.TempDir()}
	arguments := []string{"init", "-q", "--initial-branch=main"}
	if objectFormat == "sha256" {
		arguments = append(arguments, "--object-format=sha256")
	}
	command := exec.Command("git", arguments...)
	command.Dir = repository.root
	if output, err := command.CombinedOutput(); err != nil {
		t.Logf("git init %s unavailable: %v: %s", objectFormat, err, output)
		return nil, false
	}
	repository.git("config", "user.name", "Source Observation Test")
	repository.git("config", "user.email", "sourceobs@example.invalid")
	repository.git("config", "commit.gpgsign", "false")
	repository.git("config", "core.autocrlf", "false")
	return repository, true
}

func (repository *fixtureRepository) write(path string, contents []byte, mode os.FileMode) {
	repository.t.Helper()
	absolute := filepath.Join(repository.root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
		repository.t.Fatalf("MkdirAll(%q): %v", path, err)
	}
	if err := os.WriteFile(absolute, contents, mode); err != nil {
		repository.t.Fatalf("WriteFile(%q): %v", path, err)
	}
	if err := os.Chmod(absolute, mode); err != nil {
		repository.t.Fatalf("Chmod(%q): %v", path, err)
	}
}

func (repository *fixtureRepository) remove(path string) {
	repository.t.Helper()
	if err := os.Remove(filepath.Join(repository.root, filepath.FromSlash(path))); err != nil {
		repository.t.Fatalf("Remove(%q): %v", path, err)
	}
}

func (repository *fixtureRepository) rename(oldPath, newPath string) {
	repository.t.Helper()
	oldAbsolute := filepath.Join(repository.root, filepath.FromSlash(oldPath))
	newAbsolute := filepath.Join(repository.root, filepath.FromSlash(newPath))
	if err := os.MkdirAll(filepath.Dir(newAbsolute), 0o755); err != nil {
		repository.t.Fatalf("MkdirAll(%q): %v", newPath, err)
	}
	if err := os.Rename(oldAbsolute, newAbsolute); err != nil {
		repository.t.Fatalf("Rename(%q, %q): %v", oldPath, newPath, err)
	}
}

func (repository *fixtureRepository) symlink(target, path string) {
	repository.t.Helper()
	absolute := filepath.Join(repository.root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
		repository.t.Fatalf("MkdirAll(%q): %v", path, err)
	}
	if err := os.Symlink(target, absolute); err != nil {
		repository.t.Fatalf("Symlink(%q): %v", path, err)
	}
}

func (repository *fixtureRepository) commit(message string) string {
	repository.t.Helper()
	repository.git("add", "-A")
	repository.git("commit", "-q", "-m", message)
	return strings.TrimSpace(repository.git("rev-parse", "HEAD"))
}

func (repository *fixtureRepository) git(arguments ...string) string {
	repository.t.Helper()
	return repository.gitAllowed([]int{0}, arguments...)
}

func (repository *fixtureRepository) gitAllowed(accepted []int, arguments ...string) string {
	repository.t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = repository.root
	command.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	output, err := command.CombinedOutput()
	exitCode := 0
	if err != nil {
		var exitError *exec.ExitError
		if !errors.As(err, &exitError) {
			repository.t.Fatalf("git %v: %v", arguments, err)
		}
		exitCode = exitError.ExitCode()
	}
	for _, allowed := range accepted {
		if exitCode == allowed {
			return string(output)
		}
	}
	repository.t.Fatalf("git %v exited %d: %s", arguments, exitCode, output)
	return ""
}

func (repository *fixtureRepository) observe(baseline string) error {
	repository.t.Helper()
	_, err := Observe(Request{CWD: repository.root, Baseline: baseline, Scope: []string{"src"}})
	return err
}

func assertErrorKind(t *testing.T, err error, want ErrorKind, text string) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want %v", want)
	}
	got, ok := KindOf(err)
	if !ok || got != want {
		t.Fatalf("KindOf(%v) = %v, %v; want %v", err, got, ok, want)
	}
	if text != "" && !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(text)) {
		t.Fatalf("error %q does not contain %q", err, text)
	}
}

func assertLayerOrder(t *testing.T, changes []Change) {
	t.Helper()
	rank := map[string]int{"committed": 0, "staged": 1, "unstaged": 2, "untracked": 3}
	previous := -1
	seen := make(map[string]bool)
	for _, change := range changes {
		current, ok := rank[change.Source]
		if !ok || current < previous {
			t.Fatalf("change layers are out of order: %#v", changes)
		}
		previous = current
		seen[change.Source] = true
	}
	for _, source := range []string{"committed", "staged", "unstaged", "untracked"} {
		if !seen[source] {
			t.Fatalf("missing %s layer in %#v", source, changes)
		}
	}
}

func findChange(t *testing.T, changes []Change, source, status, path string) Change {
	t.Helper()
	for _, change := range changes {
		if change.Source == source && change.Status == status && change.Path == path {
			return change
		}
	}
	t.Fatalf("change (%s, %s, %s) not found in %#v", source, status, path, changes)
	return Change{}
}

func snapshotEntry(entries []Entry, path string) *Entry {
	index := sort.Search(len(entries), func(index int) bool { return entries[index].Path >= path })
	if index == len(entries) || entries[index].Path != path {
		return nil
	}
	entry := entries[index]
	return &entry
}
