// Package sourceobs collects one deterministic, baseline-relative observation
// of repository source. It is read-only: it does not run checks, publish
// Evidence, or decide completion.
package sourceobs

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"unicode/utf8"
)

// ErrorKind classifies a source-observation failure for a later CLI boundary.
type ErrorKind uint8

const (
	// InvalidRequest identifies malformed baseline or Scope input.
	InvalidRequest ErrorKind = iota + 1
	// RepositoryState identifies an unavailable or unsupported Git/filesystem
	// state. The collector always fails closed for this category.
	RepositoryState
	// UnstableSource identifies disagreement between the two bounded source
	// observations. Collection is never retried automatically.
	UnstableSource
)

// Error is a classified source-observation error.
type Error struct {
	kind    ErrorKind
	message string
	cause   error
}

func (e *Error) Error() string { return e.message }
func (e *Error) Unwrap() error { return e.cause }

// KindOf returns the stable category carried by err.
func KindOf(err error) (ErrorKind, bool) {
	var observationError *Error
	if !errors.As(err, &observationError) {
		return 0, false
	}
	return observationError.kind, true
}

// Request identifies the saved Task source boundary to observe. Baseline must
// be a full SHA-1 or SHA-256 commit object id. Scope must already use the
// normalized, repository-relative Task representation.
type Request struct {
	CWD      string
	Baseline string
	Scope    []string
}

// SnapshotRequest identifies only the immutable Task source boundary. It has
// no Scope because S0, S1, and Complete S2 must not collect or classify Git
// changes while binding final source bytes.
type SnapshotRequest struct {
	CWD      string
	Baseline string
}

// Entry is one baseline-relative final-source entry.
type Entry struct {
	Path      string
	State     string
	Mode      *string
	SizeBytes *int64
	SHA256    *string
}

// Snapshot is the canonical final-source identity used for Verify S0/S1 and
// Complete S2. Entries contain product source outside Scope as well as inside
// it; Scope is a separate changed-file decision.
type Snapshot struct {
	SchemaVersion int
	Baseline      string
	Entries       []Entry
	SHA256        string
}

// Change is one Git-layer change record.
type Change struct {
	Source       string
	Status       string
	Path         string
	PreviousPath *string
	OldMode      *string
	NewMode      *string
	ModeChanged  bool
	Binary       bool
	InScope      bool
}

// ChangeSet preserves committed, staged, unstaged, and untracked records in
// that order. ProductChanges excludes only Seal-owned metadata paths; it does
// not exclude out-of-Scope product source.
type ChangeSet struct {
	Baseline          string
	Scope             []string
	Changes           []Change
	ProductChanges    []Change
	OutOfScopeChanges []Change
}

// ScopePassed reports whether every product change is within the saved Task
// Scope. A rename is in Scope only when both its old and new paths are in Scope.
func (changes ChangeSet) ScopePassed() bool {
	return len(changes.OutOfScopeChanges) == 0
}

// SnapshotResult is one detached, snapshot-only S0/S1/S2 result. It exposes no
// layered changes or patch so callers can preserve the frozen phase order.
type SnapshotResult struct {
	snapshot     Snapshot
	snapshotJSON []byte
}

// SnapshotJSON returns exact, newline-terminated Source Snapshot artifact bytes.
func (result SnapshotResult) SnapshotJSON() []byte {
	return append([]byte(nil), result.snapshotJSON...)
}

// SnapshotSHA256 returns the digest of the canonical compact Snapshot payload.
func (result SnapshotResult) SnapshotSHA256() string { return result.snapshot.SHA256 }

// ChangeResult is one detached, post-S1 layered change result. It exposes no
// Source Snapshot so source binding remains a separate bounded observation.
type ChangeResult struct {
	changes          ChangeSet
	changedFilesJSON []byte
	diffPatch        []byte
}

// Changes returns a detached layered changed-file model.
func (result ChangeResult) Changes() ChangeSet { return cloneChangeSet(result.changes) }

// ChangedFilesJSON returns exact, newline-terminated changed-files/v1 bytes.
func (result ChangeResult) ChangedFilesJSON() []byte {
	return append([]byte(nil), result.changedFilesJSON...)
}

// DiffPatch returns a detached raw binary patch for all product change layers.
func (result ChangeResult) DiffPatch() []byte { return append([]byte(nil), result.diffPatch...) }

// ObserveSnapshot collects only one stable Source Snapshot for Verify S0/S1 or
// Complete S2. It does not inspect Scope, collect layered changes, or run a
// patch-producing Git command.
func ObserveSnapshot(request SnapshotRequest) (SnapshotResult, error) {
	return ObserveSnapshotContext(context.Background(), request)
}

// ObserveSnapshotContext is ObserveSnapshot with cooperative cancellation for
// Git subprocesses and source scanning and hashing work.
func ObserveSnapshotContext(ctx context.Context, request SnapshotRequest) (SnapshotResult, error) {
	if err := contextFailure(ctx); err != nil {
		return SnapshotResult{}, err
	}
	repository, err := resolveContext(ctx, request.CWD, request.Baseline)
	if err != nil {
		if contextErr := contextFailure(ctx); contextErr != nil {
			return SnapshotResult{}, contextErr
		}
		return SnapshotResult{}, err
	}
	baselineBlobs := make(map[string]blobIdentity)
	first, err := collectSnapshotObservation(ctx, repository, baselineBlobs)
	if err != nil {
		if contextErr := contextFailure(ctx); contextErr != nil {
			return SnapshotResult{}, contextErr
		}
		return SnapshotResult{}, err
	}
	second, err := collectSnapshotObservation(ctx, repository, baselineBlobs)
	if err != nil {
		if contextErr := contextFailure(ctx); contextErr != nil {
			return SnapshotResult{}, contextErr
		}
		return SnapshotResult{}, err
	}
	if err := contextFailure(ctx); err != nil {
		return SnapshotResult{}, err
	}
	if !reflect.DeepEqual(first, second) {
		return SnapshotResult{}, unstable("Product source changed while the source snapshot was being collected.", nil)
	}

	snapshot, snapshotJSON, err := buildSnapshot(repository.baseline, second.entries)
	if err != nil {
		return SnapshotResult{}, err
	}
	if err := contextFailure(ctx); err != nil {
		return SnapshotResult{}, err
	}
	return SnapshotResult{snapshot: snapshot, snapshotJSON: snapshotJSON}, nil
}

// ObserveChanges collects layered changed-files state and the raw binary diff
// after Verify S1. It does not collect a Source Snapshot.
func ObserveChanges(request Request) (ChangeResult, error) {
	return ObserveChangesContext(context.Background(), request)
}

// ObserveChangesContext is ObserveChanges with cooperative cancellation for
// Git subprocesses and source-change iteration.
func ObserveChangesContext(ctx context.Context, request Request) (ChangeResult, error) {
	if err := contextFailure(ctx); err != nil {
		return ChangeResult{}, err
	}
	scope, err := validateScope(request.Scope)
	if err != nil {
		return ChangeResult{}, err
	}
	repository, err := resolveContext(ctx, request.CWD, request.Baseline)
	if err != nil {
		if contextErr := contextFailure(ctx); contextErr != nil {
			return ChangeResult{}, contextErr
		}
		return ChangeResult{}, err
	}
	changes, err := collectChanges(ctx, repository, scope)
	if err != nil {
		if contextErr := contextFailure(ctx); contextErr != nil {
			return ChangeResult{}, contextErr
		}
		return ChangeResult{}, err
	}
	changedFilesJSON, err := renderChangedFiles(changes)
	if err != nil {
		return ChangeResult{}, repositoryFailure("Could not render changed-files JSON.", err)
	}
	if err := contextFailure(ctx); err != nil {
		return ChangeResult{}, err
	}
	diffPatch, err := collectDiffPatch(ctx, repository, changes)
	if err != nil {
		if contextErr := contextFailure(ctx); contextErr != nil {
			return ChangeResult{}, contextErr
		}
		return ChangeResult{}, err
	}
	if err := contextFailure(ctx); err != nil {
		return ChangeResult{}, err
	}
	return ChangeResult{changes: changes, changedFilesJSON: changedFilesJSON, diffPatch: diffPatch}, nil
}

func validateScope(scope []string) ([]string, error) {
	if len(scope) == 0 {
		return nil, invalidRequest("Task scope must be a non-empty array.", nil)
	}
	validated := make([]string, len(scope))
	for index, path := range scope {
		context := fmt.Sprintf("Task scope[%d]", index)
		if !utf8.ValidString(path) || path == "" {
			return nil, invalidRequest(context+" must be a non-empty UTF-8 path.", nil)
		}
		if path == "." {
			validated[index] = path
			continue
		}
		if strings.Contains(path, `\`) || strings.HasPrefix(path, "/") || len(path) >= 2 && path[1] == ':' {
			return nil, invalidRequest(context+" must be a normalized repository-relative POSIX path.", nil)
		}
		for _, part := range strings.Split(path, "/") {
			if part == "" || part == "." || part == ".." {
				return nil, invalidRequest(context+" must be a normalized repository-relative POSIX path.", nil)
			}
		}
		validated[index] = path
	}
	return validated, nil
}

func cloneEntries(source []Entry) []Entry {
	result := make([]Entry, len(source))
	for index, entry := range source {
		result[index] = Entry{
			Path:      entry.Path,
			State:     entry.State,
			Mode:      cloneString(entry.Mode),
			SizeBytes: cloneInt64(entry.SizeBytes),
			SHA256:    cloneString(entry.SHA256),
		}
	}
	return result
}

func cloneChangeSet(source ChangeSet) ChangeSet {
	return ChangeSet{
		Baseline:          source.Baseline,
		Scope:             append([]string(nil), source.Scope...),
		Changes:           cloneChanges(source.Changes),
		ProductChanges:    cloneChanges(source.ProductChanges),
		OutOfScopeChanges: cloneChanges(source.OutOfScopeChanges),
	}
}

func cloneChanges(source []Change) []Change {
	result := make([]Change, len(source))
	for index, change := range source {
		result[index] = change
		result[index].PreviousPath = cloneString(change.PreviousPath)
		result[index].OldMode = cloneString(change.OldMode)
		result[index].NewMode = cloneString(change.NewMode)
	}
	return result
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func invalidRequest(message string, cause error) error {
	return &Error{kind: InvalidRequest, message: message, cause: cause}
}

func repositoryFailure(message string, cause error) error {
	return &Error{kind: RepositoryState, message: message, cause: cause}
}

func unstable(message string, cause error) error {
	return &Error{kind: UnstableSource, message: message, cause: cause}
}

func contextFailure(ctx context.Context) error {
	if ctx == nil {
		return repositoryFailure("Source observation context must not be nil.", nil)
	}
	err := ctx.Err()
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return repositoryFailure("Source observation deadline was exceeded.", err)
	}
	return repositoryFailure("Source observation was canceled.", err)
}
