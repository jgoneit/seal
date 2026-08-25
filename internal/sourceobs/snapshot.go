package sourceobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"unicode/utf8"
)

const sourceHashChunkSize = 1024 * 1024

type snapshotCandidate struct {
	path           string
	baselineMode   *string
	baselineOID    *string
	currentMode    *string
	currentPresent bool
}

type snapshotObservation struct {
	entries      []Entry
	fingerprints []pathFingerprint
}

type pathFingerprint struct {
	path        string
	fingerprint fsFingerprint
}

type fsFingerprint struct {
	mode       fs.FileMode
	size       int64
	mtimeNanos int64
	identity   string
	changeTime string
	links      string
}

type observedSource struct {
	mode        string
	size        int64
	sha256      string
	fingerprint fsFingerprint
}

func collectSnapshotObservation(ctx context.Context, repository repositoryContext, baselineBlobs map[string]blobIdentity) (snapshotObservation, error) {
	if err := contextFailure(ctx); err != nil {
		return snapshotObservation{}, err
	}
	index, err := readIndexState(ctx, repository.root)
	if err != nil {
		return snapshotObservation{}, err
	}
	rawOutput, err := gitOutput(ctx, repository.root,
		"diff", "--raw", "-z", "--no-abbrev", "--no-renames", "--no-ext-diff", "--no-textconv",
		"--ignore-submodules=all", repository.baseline, "--",
	)
	if err != nil {
		return snapshotObservation{}, repositoryFailure("Could not compare the final source tree with the Task baseline.", err)
	}
	rawEntries, err := parseRawDiff(rawOutput)
	if err != nil {
		return snapshotObservation{}, err
	}
	for _, entry := range rawEntries {
		if err := contextFailure(ctx); err != nil {
			return snapshotObservation{}, err
		}
		if entry.gitStatus == "" || !strings.ContainsRune("ADMT", rune(entry.gitStatus[0])) {
			return snapshotObservation{}, repositoryFailure("Unsupported final-tree Git status '"+entry.gitStatus+"'.", nil)
		}
		if rawEntryTouchesGitlink(entry) {
			return snapshotObservation{}, repositoryFailure("Gitlink mutations are unsupported.", nil)
		}
	}

	untrackedRaw, err := gitOutput(ctx, repository.root, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return snapshotObservation{}, repositoryFailure("Could not collect final untracked source paths.", err)
	}
	untracked := make(map[string]struct{})
	for _, rawPath := range splitNUL(untrackedRaw) {
		if err := contextFailure(ctx); err != nil {
			return snapshotObservation{}, err
		}
		path, err := decodeGitPath(rawPath)
		if err != nil {
			return snapshotObservation{}, err
		}
		untracked[path] = struct{}{}
	}

	gitlinks, err := validateGitlinks(repository.baselineEntries, index.entries)
	if err != nil {
		return snapshotObservation{}, err
	}
	if err := validateGitlinkWorktree(ctx, repository.root, gitlinks); err != nil {
		return snapshotObservation{}, err
	}
	discovered, err := scanCurrentSourcePaths(ctx, repository.root, index.entries, gitlinks)
	if err != nil {
		return snapshotObservation{}, err
	}
	for path := range untracked {
		discovered[path] = struct{}{}
	}

	candidatePaths := make(map[string]struct{})
	for path := range repository.baselineEntries {
		candidatePaths[path] = struct{}{}
	}
	for path := range index.entries {
		candidatePaths[path] = struct{}{}
	}
	for path := range discovered {
		candidatePaths[path] = struct{}{}
	}
	for _, entry := range rawEntries {
		if err := contextFailure(ctx); err != nil {
			return snapshotObservation{}, err
		}
		if entry.oldPath != nil {
			candidatePaths[*entry.oldPath] = struct{}{}
		}
		if entry.newPath != nil {
			candidatePaths[*entry.newPath] = struct{}{}
		}
	}

	root, err := os.OpenRoot(repository.root)
	if err != nil {
		return snapshotObservation{}, repositoryFailure("Could not open the source worktree.", err)
	}
	defer root.Close()

	entries := make([]Entry, 0)
	fingerprints := make([]pathFingerprint, 0)
	for _, path := range sortedPaths(candidatePaths) {
		if err := contextFailure(ctx); err != nil {
			return snapshotObservation{}, err
		}
		if isMetadataPath(path) {
			continue
		}
		if _, gitlink := gitlinks[path]; gitlink {
			continue
		}
		candidate := snapshotCandidate{path: path}
		if baseline, ok := repository.baselineEntries[path]; ok {
			candidate.baselineMode = stringPointer(baseline.mode)
			candidate.baselineOID = stringPointer(baseline.oid)
		}
		if current, ok := index.entries[path]; ok {
			candidate.currentMode = stringPointer(current.mode)
			candidate.currentPresent = true
		}
		if _, ok := discovered[path]; ok {
			candidate.currentPresent = true
		}
		if err := validateCandidateModes(candidate); err != nil {
			return snapshotObservation{}, err
		}

		var observed *observedSource
		if candidate.currentPresent {
			observed, err = observeCurrentSource(ctx, root, candidate)
			if err != nil {
				return snapshotObservation{}, err
			}
		}
		if observed == nil {
			if candidate.baselineMode != nil {
				entries = append(entries, Entry{Path: path, State: "deleted"})
			}
			continue
		}
		fingerprints = append(fingerprints, pathFingerprint{path: path, fingerprint: observed.fingerprint})

		unchanged := false
		if candidate.baselineMode != nil {
			if candidate.baselineOID == nil {
				return snapshotObservation{}, repositoryFailure("Git baseline metadata is missing for '"+path+"'.", nil)
			}
			identity, ok := baselineBlobs[*candidate.baselineOID]
			if !ok {
				identity, err = readBlobIdentity(ctx, repository, *candidate.baselineOID)
				if err != nil {
					return snapshotObservation{}, err
				}
				baselineBlobs[*candidate.baselineOID] = identity
			}
			unchanged = observed.mode == *candidate.baselineMode && observed.size == identity.size && observed.sha256 == identity.sha256
		}
		if unchanged {
			continue
		}
		mode, size, digest := observed.mode, observed.size, observed.sha256
		entries = append(entries, Entry{
			Path: path, State: "present", Mode: &mode, SizeBytes: &size, SHA256: &digest,
		})
	}
	sort.Slice(entries, func(left, right int) bool { return entries[left].Path < entries[right].Path })
	sort.Slice(fingerprints, func(left, right int) bool { return fingerprints[left].path < fingerprints[right].path })
	return snapshotObservation{
		entries:      entries,
		fingerprints: fingerprints,
	}, nil
}

func validateGitlinks(baseline, current map[string]treeEntry) (map[string]struct{}, error) {
	paths := make(map[string]struct{})
	for path, entry := range baseline {
		if entry.mode == "160000" {
			paths[path] = struct{}{}
		}
	}
	for path, entry := range current {
		if entry.mode == "160000" {
			paths[path] = struct{}{}
		}
	}
	for path := range paths {
		before, hadBefore := baseline[path]
		after, hasAfter := current[path]
		if !hadBefore || !hasAfter || before.mode != "160000" || after.mode != "160000" || before.oid != after.oid {
			return nil, repositoryFailure("Gitlink mutations are unsupported: '"+path+"'.", nil)
		}
	}
	return paths, nil
}

func validateGitlinkWorktree(ctx context.Context, rootPath string, gitlinks map[string]struct{}) error {
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return repositoryFailure("Could not open the source worktree.", err)
	}
	defer root.Close()
	for _, path := range sortedPaths(gitlinks) {
		if err := contextFailure(ctx); err != nil {
			return err
		}
		parts := strings.Split(path, "/")
		for index := 1; index <= len(parts); index++ {
			if err := contextFailure(ctx); err != nil {
				return err
			}
			current := strings.Join(parts[:index], "/")
			info, err := root.Lstat(current)
			if errors.Is(err, fs.ErrNotExist) {
				break
			}
			if err != nil {
				return repositoryFailure("Could not inspect Gitlink worktree path '"+current+"'.", err)
			}
			if isReparsePoint(info) || !info.IsDir() {
				return repositoryFailure("Gitlink worktree mutation is unsupported: '"+current+"'.", nil)
			}
		}
	}
	return nil
}

func validateCandidateModes(candidate snapshotCandidate) error {
	for _, value := range []struct {
		label string
		mode  *string
	}{
		{label: "baseline", mode: candidate.baselineMode},
		{label: "current", mode: candidate.currentMode},
	} {
		if value.mode != nil && *value.mode != "100644" && *value.mode != "100755" && *value.mode != "120000" {
			return repositoryFailure("Unsupported "+value.label+" Git mode '"+*value.mode+"' for '"+candidate.path+"'.", nil)
		}
	}
	return nil
}

func scanCurrentSourcePaths(ctx context.Context, rootPath string, tracked map[string]treeEntry, gitlinks map[string]struct{}) (map[string]struct{}, error) {
	discovered := make(map[string]struct{})
	trackedDirectories := make(map[string]struct{})
	for path := range tracked {
		if err := contextFailure(ctx); err != nil {
			return nil, err
		}
		parts := strings.Split(path, "/")
		for index := 1; index < len(parts); index++ {
			trackedDirectories[strings.Join(parts[:index], "/")] = struct{}{}
		}
	}
	type pendingDirectory struct {
		absolute string
		relative string
	}
	pending := []pendingDirectory{{absolute: rootPath}}
	for len(pending) != 0 {
		if err := contextFailure(ctx); err != nil {
			return nil, err
		}
		directory := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		entries, err := os.ReadDir(directory.absolute)
		if err != nil {
			return nil, repositoryFailure("Could not scan source directory '"+directory.relative+"'.", err)
		}
		sort.Slice(entries, func(left, right int) bool { return entries[left].Name() < entries[right].Name() })
		for _, directoryEntry := range entries {
			if err := contextFailure(ctx); err != nil {
				return nil, err
			}
			name := directoryEntry.Name()
			if !utf8.ValidString(name) {
				return nil, repositoryFailure("Source filename is not valid UTF-8.", nil)
			}
			relative := name
			if directory.relative != "" {
				relative = directory.relative + "/" + name
			}
			if directory.relative == "" && name == ".git" {
				continue
			}
			if _, err := decodeGitPath([]byte(relative)); err != nil {
				return nil, err
			}
			if isMetadataPath(relative) {
				continue
			}
			info, err := directoryEntry.Info()
			if err != nil {
				return nil, repositoryFailure("Could not inspect source path '"+relative+"'.", err)
			}
			if _, gitlink := gitlinks[relative]; gitlink {
				if isReparsePoint(info) || !info.IsDir() {
					return nil, repositoryFailure("Gitlink worktree mutation is unsupported: '"+relative+"'.", nil)
				}
				continue
			}
			if isReparsePoint(info) {
				return nil, repositoryFailure("Unsupported Windows reparse point at '"+relative+"'.", nil)
			}
			_, trackedPath := tracked[relative]
			_, trackedDirectory := trackedDirectories[relative]
			if info.IsDir() {
				ignored := false
				if !trackedDirectory {
					ignored, err = gitIgnored(ctx, rootPath, relative)
					if err != nil {
						return nil, err
					}
				}
				if trackedDirectory || !ignored {
					pending = append(pending, pendingDirectory{absolute: filepath.Join(directory.absolute, name), relative: relative})
				}
				continue
			}
			if info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
				ignored := false
				if !trackedPath {
					ignored, err = gitIgnored(ctx, rootPath, relative)
					if err != nil {
						return nil, err
					}
				}
				if trackedPath || !ignored {
					discovered[relative] = struct{}{}
				}
				continue
			}
			ignored := false
			if !trackedPath {
				ignored, err = gitIgnored(ctx, rootPath, relative)
				if err != nil {
					return nil, err
				}
			}
			if trackedPath || !ignored {
				return nil, unsupportedFileType(relative, info.Mode())
			}
		}
	}
	return discovered, nil
}

func gitIgnored(ctx context.Context, root, path string) (bool, error) {
	result, err := gitResult(ctx, root, []int{0, 1}, "check-ignore", "--quiet", "--", path)
	if err != nil {
		return false, repositoryFailure("Could not inspect Git ignore state for '"+path+"'.", err)
	}
	return result.exitCode == 0, nil
}

func observeCurrentSource(ctx context.Context, root *os.Root, candidate snapshotCandidate) (*observedSource, error) {
	parts := strings.Split(candidate.path, "/")
	for index := 1; index < len(parts); index++ {
		if err := contextFailure(ctx); err != nil {
			return nil, err
		}
		parent := strings.Join(parts[:index], "/")
		info, err := root.Lstat(parent)
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		if err != nil {
			return nil, repositoryFailure("Could not inspect a parent of '"+candidate.path+"'.", err)
		}
		if isReparsePoint(info) {
			return nil, repositoryFailure("Unsupported Windows reparse point at '"+parent+"'.", nil)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return nil, nil
		}
	}
	before, err := root.Lstat(candidate.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, repositoryFailure("Could not inspect source path '"+candidate.path+"'.", err)
	}
	if isReparsePoint(before) {
		return nil, repositoryFailure("Unsupported Windows reparse point at '"+candidate.path+"'.", nil)
	}
	if before.Mode()&os.ModeSymlink != 0 {
		if err := contextFailure(ctx); err != nil {
			return nil, err
		}
		target, err := root.Readlink(candidate.path)
		if err != nil {
			return nil, repositoryFailure("Could not read symlink source path '"+candidate.path+"'.", err)
		}
		after, err := root.Lstat(candidate.path)
		if err != nil || fingerprint(before) != fingerprint(after) || !os.SameFile(before, after) {
			return nil, unstable("Source path '"+candidate.path+"' changed while it was being hashed.", err)
		}
		contents := []byte(target)
		digest := sha256.Sum256(contents)
		return &observedSource{
			mode: "120000", size: int64(len(contents)), sha256: hex.EncodeToString(digest[:]), fingerprint: fingerprint(after),
		}, nil
	}
	if before.IsDir() {
		return nil, nil
	}
	if !before.Mode().IsRegular() {
		return nil, unsupportedFileType(candidate.path, before.Mode())
	}
	file, err := root.Open(candidate.path)
	if err != nil {
		return nil, repositoryFailure("Could not open source file '"+candidate.path+"'.", err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) || fingerprint(before) != fingerprint(opened) {
		return nil, unstable("Source path '"+candidate.path+"' changed before it could be hashed.", err)
	}
	hash := sha256.New()
	buffer := make([]byte, sourceHashChunkSize)
	var size int64
	for {
		if err := contextFailure(ctx); err != nil {
			return nil, err
		}
		read, readErr := file.Read(buffer)
		if read > 0 {
			_, _ = hash.Write(buffer[:read])
			size += int64(read)
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, repositoryFailure("Could not hash source file '"+candidate.path+"'.", readErr)
		}
	}
	if err := contextFailure(ctx); err != nil {
		return nil, err
	}
	afterOpen, openErr := file.Stat()
	afterPath, pathErr := root.Lstat(candidate.path)
	beforeFingerprint := fingerprint(before)
	if openErr != nil || pathErr != nil || !os.SameFile(before, afterOpen) || !os.SameFile(before, afterPath) ||
		beforeFingerprint != fingerprint(afterOpen) || beforeFingerprint != fingerprint(afterPath) || size != before.Size() {
		return nil, unstable("Source path '"+candidate.path+"' changed while it was being hashed.", errors.Join(openErr, pathErr))
	}
	mode := regularMode(before, candidate)
	return &observedSource{
		mode: mode, size: size, sha256: hex.EncodeToString(hash.Sum(nil)), fingerprint: beforeFingerprint,
	}, nil
}

func regularMode(info fs.FileInfo, candidate snapshotCandidate) string {
	if runtime.GOOS != "windows" {
		if info.Mode().Perm()&0100 != 0 {
			return "100755"
		}
		return "100644"
	}
	if candidate.currentMode != nil && (*candidate.currentMode == "100644" || *candidate.currentMode == "100755" || *candidate.currentMode == "120000") {
		return *candidate.currentMode
	}
	if candidate.baselineMode != nil && (*candidate.baselineMode == "100644" || *candidate.baselineMode == "100755" || *candidate.baselineMode == "120000") {
		return *candidate.baselineMode
	}
	return "100644"
}

func fingerprint(info fs.FileInfo) fsFingerprint {
	return fsFingerprint{
		mode:       info.Mode(),
		size:       info.Size(),
		mtimeNanos: info.ModTime().UnixNano(),
		identity:   reflectedFields(info.Sys(), "Dev", "Ino", "VolumeSerialNumber", "FileIndexHigh", "FileIndexLow"),
		changeTime: reflectedFields(info.Sys(), "Ctim", "Ctimespec", "Ctime", "ChangeTime"),
		links:      reflectedFields(info.Sys(), "Nlink", "NumberOfLinks"),
	}
}

func reflectedFields(value any, names ...string) string {
	if value == nil {
		return ""
	}
	reflected := reflect.ValueOf(value)
	for reflected.Kind() == reflect.Pointer {
		if reflected.IsNil() {
			return ""
		}
		reflected = reflected.Elem()
	}
	if reflected.Kind() != reflect.Struct {
		return ""
	}
	var result strings.Builder
	for _, name := range names {
		field := reflected.FieldByName(name)
		if !field.IsValid() || !field.CanInterface() {
			continue
		}
		fmt.Fprintf(&result, "%s=%v;", name, field.Interface())
	}
	return result.String()
}

func unsupportedFileType(path string, mode fs.FileMode) error {
	kind := "special filesystem node"
	switch {
	case mode&os.ModeNamedPipe != 0:
		kind = "FIFO"
	case mode&os.ModeSocket != 0:
		kind = "socket"
	case mode&os.ModeCharDevice != 0:
		kind = "character device"
	case mode&os.ModeDevice != 0:
		kind = "block device"
	case mode.IsDir():
		kind = "directory"
	}
	return repositoryFailure("Unsupported "+kind+" source path: '"+path+"'.", nil)
}

func stringPointer(value string) *string { return &value }
