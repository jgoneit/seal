package sourceobs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const gitPipeDrainLimit = time.Second

type repositoryContext struct {
	root            string
	baseline        string
	baselineEntries map[string]treeEntry
}

type treeEntry struct {
	mode string
	oid  string
}

type indexState struct {
	entries map[string]treeEntry
}

type rawDiffEntry struct {
	gitStatus string
	oldPath   *string
	newPath   *string
	oldMode   *string
	newMode   *string
	oldOID    *string
	newOID    *string
}

type blobIdentity struct {
	size   int64
	sha256 string
}

func resolveContext(ctx context.Context, cwd, baseline string) (repositoryContext, error) {
	if err := contextFailure(ctx); err != nil {
		return repositoryContext{}, err
	}
	if cwd == "" {
		cwd = "."
	}
	if !utf8.ValidString(cwd) {
		return repositoryContext{}, invalidRequest("Source observation CWD must be valid UTF-8.", nil)
	}
	if !fullObjectID(baseline) {
		return repositoryContext{}, invalidRequest("Task baseline must be a full lowercase SHA-1 or SHA-256 commit object id.", nil)
	}
	if value := os.Getenv("GIT_REPLACE_REF_BASE"); value != "" {
		return repositoryContext{}, repositoryFailure("Active custom Git replacement refs are unsupported.", nil)
	}

	rootOutput, err := gitOutput(ctx, cwd, "rev-parse", "--path-format=absolute", "--show-toplevel")
	if err != nil {
		return repositoryContext{}, repositoryFailure("Source observation must run inside an initialized Git worktree.", err)
	}
	root, err := oneGitLine(rootOutput, "Git worktree root")
	if err != nil {
		return repositoryContext{}, err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return repositoryContext{}, repositoryFailure("Could not resolve the Git worktree root.", err)
	}

	inside, err := gitText(ctx, root, "rev-parse", "--is-inside-work-tree")
	if err != nil || inside != "true" {
		return repositoryContext{}, repositoryFailure("Source observation requires a non-bare Git worktree.", err)
	}
	bare, err := gitText(ctx, root, "rev-parse", "--is-bare-repository")
	if err != nil || bare != "false" {
		return repositoryContext{}, repositoryFailure("Source observation requires a non-bare Git worktree.", err)
	}
	if _, err := resolveCommit(ctx, root, "HEAD"); err != nil {
		return repositoryContext{}, repositoryFailure("Git HEAD must resolve to a commit.", err)
	}
	resolvedBaseline, err := resolveCommit(ctx, root, baseline)
	if err != nil || resolvedBaseline != baseline {
		return repositoryContext{}, repositoryFailure("Task baseline does not resolve to its exact saved commit.", err)
	}
	if err := checkRepositoryGuards(ctx, root); err != nil {
		return repositoryContext{}, err
	}
	if _, err := readIndexState(ctx, root); err != nil {
		return repositoryContext{}, err
	}
	baselineEntries, err := readBaselineTree(ctx, root, baseline)
	if err != nil {
		return repositoryContext{}, err
	}
	return repositoryContext{
		root:            root,
		baseline:        baseline,
		baselineEntries: baselineEntries,
	}, nil
}

func resolveCommit(ctx context.Context, root, reference string) (string, error) {
	output, err := gitOutput(ctx, root, "rev-parse", "--verify", "--end-of-options", reference+"^{commit}")
	if err != nil {
		return "", err
	}
	value, err := oneGitLine(output, "Git commit object id")
	if err != nil {
		return "", err
	}
	if !fullObjectID(value) {
		return "", repositoryFailure("Git returned an unsupported commit object id.", nil)
	}
	return value, nil
}

func checkRepositoryGuards(ctx context.Context, root string) error {
	replacements, err := gitOutput(ctx, root, "for-each-ref", "--format=%(refname)", "refs/replace")
	if err != nil {
		return repositoryFailure("Could not inspect Git replacement refs.", err)
	}
	if len(bytes.TrimSpace(replacements)) != 0 {
		return repositoryFailure("Active Git replacement refs are unsupported.", nil)
	}
	for _, key := range []string{"core.sparseCheckout", "core.sparseCheckoutCone"} {
		result, err := gitResult(ctx, root, []int{0, 1}, "config", "--bool", "--get", key)
		if err != nil {
			return repositoryFailure("Could not inspect Git sparse-checkout state.", err)
		}
		if result.exitCode == 0 && strings.TrimSpace(string(result.stdout)) == "true" {
			return repositoryFailure("Sparse Git worktrees are unsupported.", nil)
		}
	}
	return nil
}

func readIndexState(ctx context.Context, root string) (indexState, error) {
	if err := checkRepositoryGuards(ctx, root); err != nil {
		return indexState{}, err
	}
	flags, err := gitOutput(ctx, root, "ls-files", "-v", "-z")
	if err != nil {
		return indexState{}, repositoryFailure("Could not inspect Git index flags.", err)
	}
	for _, record := range splitNUL(flags) {
		if err := contextFailure(ctx); err != nil {
			return indexState{}, err
		}
		if len(record) < 3 || record[1] != ' ' {
			return indexState{}, repositoryFailure("Could not parse Git index flags.", nil)
		}
		path, err := decodeGitPath(record[2:])
		if err != nil {
			return indexState{}, err
		}
		tag := record[0]
		if tag == 'S' {
			return indexState{}, repositoryFailure("Git skip-worktree entries are unsupported: '"+path+"'.", nil)
		}
		if tag >= 'a' && tag <= 'z' {
			return indexState{}, repositoryFailure("Git assume-unchanged entries are unsupported: '"+path+"'.", nil)
		}
	}

	stage, err := gitOutput(ctx, root, "ls-files", "--stage", "-z")
	if err != nil {
		return indexState{}, repositoryFailure("Could not inspect staged-file metadata.", err)
	}
	entries := make(map[string]treeEntry)
	for _, record := range splitNUL(stage) {
		if err := contextFailure(ctx); err != nil {
			return indexState{}, err
		}
		header, rawPath, ok := bytes.Cut(record, []byte{'\t'})
		if !ok {
			return indexState{}, repositoryFailure("Could not parse staged-file metadata.", nil)
		}
		parts := bytes.Fields(header)
		if len(parts) != 3 {
			return indexState{}, repositoryFailure("Could not parse staged-file metadata.", nil)
		}
		path, err := decodeGitPath(rawPath)
		if err != nil {
			return indexState{}, err
		}
		stageNumber, err := strconv.Atoi(string(parts[2]))
		if err != nil || stageNumber != 0 {
			return indexState{}, repositoryFailure("Unmerged Git index entries are unsupported: '"+path+"'.", err)
		}
		mode, err := decodeMode(parts[0])
		if err != nil {
			return indexState{}, err
		}
		oid, err := decodeOID(parts[1], false)
		if err != nil || oid == nil {
			return indexState{}, repositoryFailure("Git index contains an invalid object id for '"+path+"'.", err)
		}
		if _, duplicate := entries[path]; duplicate {
			return indexState{}, repositoryFailure("Git index contains duplicate path '"+path+"'.", nil)
		}
		entries[path] = treeEntry{mode: mode, oid: *oid}
	}
	return indexState{entries: entries}, nil
}

func readBaselineTree(ctx context.Context, root, baseline string) (map[string]treeEntry, error) {
	output, err := gitOutput(ctx, root, "ls-tree", "-r", "-z", "--full-tree", baseline)
	if err != nil {
		return nil, repositoryFailure("Could not read the Task baseline tree.", err)
	}
	entries := make(map[string]treeEntry)
	for _, record := range splitNUL(output) {
		if err := contextFailure(ctx); err != nil {
			return nil, err
		}
		header, rawPath, ok := bytes.Cut(record, []byte{'\t'})
		if !ok {
			return nil, repositoryFailure("Could not parse Git baseline-tree metadata.", nil)
		}
		parts := bytes.Fields(header)
		if len(parts) != 3 {
			return nil, repositoryFailure("Could not parse Git baseline-tree metadata.", nil)
		}
		mode, err := decodeMode(parts[0])
		if err != nil {
			return nil, err
		}
		objectType := string(parts[1])
		if objectType != "blob" && objectType != "commit" {
			return nil, repositoryFailure("Git baseline tree contains unsupported object type '"+objectType+"'.", nil)
		}
		oid, err := decodeOID(parts[2], false)
		if err != nil || oid == nil {
			return nil, repositoryFailure("Git baseline tree contains an invalid object id.", err)
		}
		path, err := decodeGitPath(rawPath)
		if err != nil {
			return nil, err
		}
		if _, duplicate := entries[path]; duplicate {
			return nil, repositoryFailure("Git baseline tree contains duplicate path '"+path+"'.", nil)
		}
		entries[path] = treeEntry{mode: mode, oid: *oid}
	}
	return entries, nil
}

func collectChanges(ctx context.Context, repository repositoryContext, scope []string) (ChangeSet, error) {
	if _, err := readIndexState(ctx, repository.root); err != nil {
		return ChangeSet{}, err
	}
	changes := make([]Change, 0)
	for _, source := range []string{"committed", "staged", "unstaged"} {
		if err := contextFailure(ctx); err != nil {
			return ChangeSet{}, err
		}
		arguments := trackedDiffArguments("--raw", source, repository.baseline, true)
		raw, err := gitOutput(ctx, repository.root, arguments...)
		if err != nil {
			return ChangeSet{}, repositoryFailure("Could not collect "+source+" Git changes.", err)
		}
		entries, err := parseRawDiff(raw)
		if err != nil {
			return ChangeSet{}, err
		}
		numstatArguments := trackedDiffArguments("--numstat", source, repository.baseline, false)
		numstat, err := gitOutput(ctx, repository.root, numstatArguments...)
		if err != nil {
			return ChangeSet{}, repositoryFailure("Could not classify "+source+" binary changes.", err)
		}
		binaryPaths, err := parseBinaryPaths(numstat)
		if err != nil {
			return ChangeSet{}, err
		}
		for _, entry := range entries {
			if err := contextFailure(ctx); err != nil {
				return ChangeSet{}, err
			}
			if rawEntryTouchesGitlink(entry) {
				return ChangeSet{}, repositoryFailure("Gitlink mutations are unsupported.", nil)
			}
			change, err := toChange(entry, source, binaryPaths, scope)
			if err != nil {
				return ChangeSet{}, err
			}
			changes = append(changes, change)
		}
	}

	untrackedOutput, err := gitOutput(ctx, repository.root, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return ChangeSet{}, repositoryFailure("Could not collect untracked Git paths.", err)
	}
	for _, rawPath := range splitNUL(untrackedOutput) {
		if err := contextFailure(ctx); err != nil {
			return ChangeSet{}, err
		}
		path, err := decodeGitPath(rawPath)
		if err != nil {
			return ChangeSet{}, err
		}
		binary, err := untrackedIsBinary(ctx, repository.root, path)
		if err != nil {
			return ChangeSet{}, err
		}
		changes = append(changes, Change{
			Source:      "untracked",
			Status:      "untracked",
			Path:        path,
			ModeChanged: false,
			Binary:      binary,
			InScope:     pathWithinScope(path, scope),
		})
	}

	collection := ChangeSet{
		Baseline: repository.baseline,
		Scope:    append([]string(nil), scope...),
		Changes:  cloneChanges(changes),
	}
	for _, change := range changes {
		if err := contextFailure(ctx); err != nil {
			return ChangeSet{}, err
		}
		if isMetadataChange(change) {
			continue
		}
		collection.ProductChanges = append(collection.ProductChanges, cloneChanges([]Change{change})[0])
		if !change.InScope {
			collection.OutOfScopeChanges = append(collection.OutOfScopeChanges, cloneChanges([]Change{change})[0])
		}
	}
	return collection, nil
}

func trackedDiffArguments(format, source, baseline string, raw bool) []string {
	ignoreSubmodules := "--ignore-submodules=dirty"
	if source == "unstaged" {
		// An uninitialized clean submodule has no worktree node. Git can report
		// that as a worktree deletion even though its gitlink metadata is
		// unchanged; the baseline/index metadata gate remains authoritative.
		ignoreSubmodules = "--ignore-submodules=all"
	}
	arguments := []string{
		"diff", "--no-ext-diff", "--no-textconv", format, "-z", "--find-renames",
		ignoreSubmodules,
	}
	if raw {
		arguments = append(arguments, "--no-abbrev")
	}
	switch source {
	case "committed":
		arguments = append(arguments, baseline, "HEAD")
	case "staged":
		arguments = append(arguments, "--cached")
	case "unstaged":
	default:
		panic("unsupported tracked change source")
	}
	return append(arguments, "--")
}

func toChange(entry rawDiffEntry, source string, binaryPaths map[pathPair]struct{}, scope []string) (Change, error) {
	path := entry.newPath
	if path == nil {
		path = entry.oldPath
	}
	if path == nil {
		return Change{}, repositoryFailure("Git diff record is missing both paths.", nil)
	}
	var previous *string
	if entry.oldPath != nil && entry.newPath != nil && *entry.oldPath != *entry.newPath {
		previous = cloneString(entry.oldPath)
	}
	status, err := statusName(entry.gitStatus)
	if err != nil {
		return Change{}, err
	}
	pair := pathPair{old: *path, new: *path}
	if entry.oldPath != nil {
		pair.old = *entry.oldPath
	}
	if entry.newPath != nil {
		pair.new = *entry.newPath
	}
	_, binary := binaryPaths[pair]
	modeChanged := entry.oldMode != nil && entry.newMode != nil && *entry.oldMode != *entry.newMode
	return Change{
		Source:       source,
		Status:       status,
		Path:         *path,
		PreviousPath: previous,
		OldMode:      cloneString(entry.oldMode),
		NewMode:      cloneString(entry.newMode),
		ModeChanged:  modeChanged,
		Binary:       binary,
		InScope:      changeWithinScope(status, *path, previous, scope),
	}, nil
}

func parseRawDiff(output []byte) ([]rawDiffEntry, error) {
	fields := bytes.Split(output, []byte{0})
	entries := make([]rawDiffEntry, 0)
	for index := 0; index < len(fields); {
		header := fields[index]
		index++
		if len(header) == 0 {
			continue
		}
		if header[0] != ':' {
			return nil, repositoryFailure("Could not parse Git raw diff output.", nil)
		}
		parts := bytes.Fields(header[1:])
		if len(parts) != 5 {
			return nil, repositoryFailure("Could not parse Git raw diff metadata.", nil)
		}
		oldMode, err := decodeOptionalMode(parts[0])
		if err != nil {
			return nil, err
		}
		newMode, err := decodeOptionalMode(parts[1])
		if err != nil {
			return nil, err
		}
		oldOID, err := decodeOID(parts[2], true)
		if err != nil {
			return nil, err
		}
		newOID, err := decodeOID(parts[3], true)
		if err != nil {
			return nil, err
		}
		status := string(parts[4])
		if len(status) == 0 || status[0] < 'A' || status[0] > 'Z' {
			return nil, repositoryFailure("Git returned an invalid raw diff status.", nil)
		}
		if index >= len(fields) || len(fields[index]) == 0 {
			return nil, repositoryFailure("Git raw diff record is missing a path.", nil)
		}
		first, err := decodeGitPath(fields[index])
		if err != nil {
			return nil, err
		}
		index++
		entry := rawDiffEntry{
			gitStatus: status,
			oldMode:   oldMode,
			newMode:   newMode,
			oldOID:    oldOID,
			newOID:    newOID,
		}
		switch status[0] {
		case 'R', 'C':
			if index >= len(fields) || len(fields[index]) == 0 {
				return nil, repositoryFailure("Git rename or copy record is missing its destination path.", nil)
			}
			second, err := decodeGitPath(fields[index])
			if err != nil {
				return nil, err
			}
			index++
			entry.oldPath = &first
			entry.newPath = &second
		case 'A':
			entry.newPath = &first
		case 'D':
			entry.oldPath = &first
		default:
			entry.oldPath = &first
			entry.newPath = &first
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

type pathPair struct {
	old string
	new string
}

func parseBinaryPaths(output []byte) (map[pathPair]struct{}, error) {
	fields := bytes.Split(output, []byte{0})
	result := make(map[pathPair]struct{})
	for index := 0; index < len(fields); {
		record := fields[index]
		index++
		if len(record) == 0 {
			continue
		}
		parts := bytes.SplitN(record, []byte{'\t'}, 3)
		if len(parts) != 3 {
			return nil, repositoryFailure("Could not parse Git numstat output.", nil)
		}
		binary := bytes.Equal(parts[0], []byte("-")) || bytes.Equal(parts[1], []byte("-"))
		var oldPath, newPath string
		var err error
		if len(parts[2]) != 0 {
			oldPath, err = decodeGitPath(parts[2])
			newPath = oldPath
		} else {
			if index+1 >= len(fields) || len(fields[index]) == 0 || len(fields[index+1]) == 0 {
				return nil, repositoryFailure("Git numstat rename or copy record is missing a path.", nil)
			}
			oldPath, err = decodeGitPath(fields[index])
			if err == nil {
				newPath, err = decodeGitPath(fields[index+1])
			}
			index += 2
		}
		if err != nil {
			return nil, err
		}
		if binary {
			result[pathPair{old: oldPath, new: newPath}] = struct{}{}
		}
	}
	return result, nil
}

func untrackedIsBinary(ctx context.Context, root, path string) (bool, error) {
	result, err := gitResult(ctx, root, []int{0, 1},
		"diff", "--no-index", "--no-ext-diff", "--no-textconv", "--numstat", "-z", "--", "/dev/null", path,
	)
	if err != nil {
		return false, repositoryFailure("Could not classify untracked path '"+path+"'.", err)
	}
	parts := bytes.SplitN(result.stdout, []byte{'\t'}, 3)
	return len(parts) >= 2 && (bytes.Equal(parts[0], []byte("-")) || bytes.Equal(parts[1], []byte("-"))), nil
}

func rawEntryTouchesGitlink(entry rawDiffEntry) bool {
	return entry.oldMode != nil && *entry.oldMode == "160000" || entry.newMode != nil && *entry.newMode == "160000"
}

func statusName(status string) (string, error) {
	if status == "" {
		return "", repositoryFailure("Git diff status is empty.", nil)
	}
	switch status[0] {
	case 'A':
		return "added", nil
	case 'C':
		return "copied", nil
	case 'D':
		return "deleted", nil
	case 'M':
		return "modified", nil
	case 'R':
		return "renamed", nil
	case 'T':
		return "type_changed", nil
	case 'U':
		return "", repositoryFailure("Unmerged Git changes are unsupported.", nil)
	default:
		return "", repositoryFailure("Unsupported Git diff status '"+status+"'.", nil)
	}
}

func changeWithinScope(status, path string, previous *string, scope []string) bool {
	if !pathWithinScope(path, scope) {
		return false
	}
	return status != "renamed" || previous == nil || pathWithinScope(*previous, scope)
}

func pathWithinScope(path string, scope []string) bool {
	for _, boundary := range scope {
		if boundary == "." || path == boundary || strings.HasPrefix(path, boundary+"/") {
			return true
		}
	}
	return false
}

func isMetadataPath(path string) bool {
	if path == ".seal/runs.jsonl" || path == ".seal/lessons.md" || path == ".seal/config.json" {
		return true
	}
	return path == ".seal/tasks" || strings.HasPrefix(path, ".seal/tasks/") ||
		path == ".seal/evidence" || strings.HasPrefix(path, ".seal/evidence/")
}

func isMetadataChange(change Change) bool {
	if !isMetadataPath(change.Path) {
		return false
	}
	return change.PreviousPath == nil || isMetadataPath(*change.PreviousPath)
}

func readBlobIdentity(ctx context.Context, repository repositoryContext, oid string) (blobIdentity, error) {
	command := newGitCommand(ctx, repository.root, "cat-file", "blob", oid)
	hash := sha256.New()
	counter := &countingWriter{writer: hash}
	var stderr bytes.Buffer
	command.Stdout = counter
	command.Stderr = &stderr
	if err := runGitCommand(command); err != nil {
		if contextErr := contextFailure(ctx); contextErr != nil {
			return blobIdentity{}, contextErr
		}
		return blobIdentity{}, repositoryFailure("Could not read baseline blob '"+oid+"'.", gitCommandError(err, stderr.Bytes()))
	}
	if err := contextFailure(ctx); err != nil {
		return blobIdentity{}, err
	}
	return blobIdentity{size: counter.count, sha256: hex.EncodeToString(hash.Sum(nil))}, nil
}

type countingWriter struct {
	writer io.Writer
	count  int64
}

func (writer *countingWriter) Write(value []byte) (int, error) {
	written, err := writer.writer.Write(value)
	writer.count += int64(written)
	return written, err
}

func collectDiffPatch(ctx context.Context, repository repositoryContext, changes ChangeSet) ([]byte, error) {
	if err := checkRepositoryGuards(ctx, repository.root); err != nil {
		return nil, err
	}
	prefix := []string{"diff", "--binary", "--no-ext-diff", "--no-textconv", "--ignore-submodules=dirty"}
	pathspecs := []string{
		"--", ".", ":(exclude).seal/tasks/**", ":(exclude).seal/evidence/**",
		":(exclude).seal/config.json", ":(exclude).seal/lessons.md", ":(exclude).seal/runs.jsonl",
	}
	commands := [][]string{
		append(append(append([]string(nil), prefix...), repository.baseline, "HEAD"), pathspecs...),
		append(append(append([]string(nil), prefix...), "--cached"), pathspecs...),
		append(append(append([]string(nil), prefix...), "--ignore-submodules=all"), pathspecs...),
	}
	accepted := [][]int{{0}, {0}, {0}}
	for _, change := range changes.Changes {
		if err := contextFailure(ctx); err != nil {
			return nil, err
		}
		if change.Source != "untracked" || isMetadataPath(change.Path) {
			continue
		}
		commands = append(commands, append(append([]string(nil), prefix...), "--no-index", "--", "/dev/null", change.Path))
		accepted = append(accepted, []int{0, 1})
	}
	var patch []byte
	for index, arguments := range commands {
		result, err := gitResult(ctx, repository.root, accepted[index], arguments...)
		if err != nil {
			return nil, repositoryFailure("Could not collect the raw binary source diff.", err)
		}
		if len(result.stdout) == 0 {
			continue
		}
		if len(patch) != 0 {
			patch = append(patch, '\n')
		}
		patch = append(patch, result.stdout...)
	}
	return patch, nil
}

func decodeGitPath(raw []byte) (string, error) {
	if !utf8.Valid(raw) {
		return "", repositoryFailure("Git path is not valid UTF-8.", nil)
	}
	path := string(raw)
	if path == "" || strings.HasPrefix(path, "/") || strings.Contains(path, `\`) || strings.ContainsRune(path, 0) || len(path) >= 2 && path[1] == ':' {
		return "", repositoryFailure("Git path must be a canonical repository-relative POSIX path.", nil)
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." {
			return "", repositoryFailure("Git path must be a canonical repository-relative POSIX path.", nil)
		}
	}
	return path, nil
}

func decodeMode(raw []byte) (string, error) {
	if len(raw) != 6 {
		return "", repositoryFailure("Git returned an invalid file mode.", nil)
	}
	for _, value := range raw {
		if value < '0' || value > '7' {
			return "", repositoryFailure("Git returned an invalid file mode.", nil)
		}
	}
	return string(raw), nil
}

func decodeOptionalMode(raw []byte) (*string, error) {
	mode, err := decodeMode(raw)
	if err != nil {
		return nil, err
	}
	if mode == "000000" {
		return nil, nil
	}
	return &mode, nil
}

func decodeOID(raw []byte, zeroAllowed bool) (*string, error) {
	value := string(raw)
	if zeroAllowed && (len(value) == 40 || len(value) == 64) && strings.Trim(value, "0") == "" {
		return nil, nil
	}
	if !fullObjectID(value) {
		return nil, repositoryFailure("Git returned an invalid object id.", nil)
	}
	return &value, nil
}

func fullObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	return true
}

func splitNUL(output []byte) [][]byte {
	fields := bytes.Split(output, []byte{0})
	result := fields[:0]
	for _, field := range fields {
		if len(field) != 0 {
			result = append(result, field)
		}
	}
	return result
}

func oneGitLine(output []byte, context string) (string, error) {
	output = bytes.TrimSuffix(output, []byte{'\n'})
	output = bytes.TrimSuffix(output, []byte{'\r'})
	if len(output) == 0 || bytes.ContainsAny(output, "\r\n") || !utf8.Valid(output) {
		return "", repositoryFailure("Could not decode "+context+".", nil)
	}
	return string(output), nil
}

func gitText(ctx context.Context, root string, arguments ...string) (string, error) {
	output, err := gitOutput(ctx, root, arguments...)
	if err != nil {
		return "", err
	}
	return oneGitLine(output, "Git output")
}

func gitOutput(ctx context.Context, root string, arguments ...string) ([]byte, error) {
	result, err := gitResult(ctx, root, []int{0}, arguments...)
	if err != nil {
		return nil, err
	}
	return result.stdout, nil
}

type commandResult struct {
	stdout   []byte
	exitCode int
}

func gitResult(ctx context.Context, root string, accepted []int, arguments ...string) (commandResult, error) {
	if err := contextFailure(ctx); err != nil {
		return commandResult{}, err
	}
	command := newGitCommand(ctx, root, arguments...)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := runGitCommand(command)
	if contextErr := contextFailure(ctx); contextErr != nil {
		return commandResult{}, contextErr
	}
	exitCode := 0
	if err != nil {
		var exitError *exec.ExitError
		if !errorsAs(err, &exitError) {
			return commandResult{}, gitCommandError(err, stderr.Bytes())
		}
		exitCode = exitError.ExitCode()
	}
	for _, allowed := range accepted {
		if exitCode == allowed {
			return commandResult{stdout: append([]byte(nil), stdout.Bytes()...), exitCode: exitCode}, nil
		}
	}
	return commandResult{}, gitCommandError(fmt.Errorf("git exited with status %d", exitCode), stderr.Bytes())
}

// errorsAs is isolated to keep all process error handling at this boundary.
func errorsAs(err error, target any) bool {
	return errors.As(err, target)
}

func newGitCommand(ctx context.Context, root string, arguments ...string) *exec.Cmd {
	argv := make([]string, 0, len(arguments)+4)
	argv = append(argv, "--no-replace-objects", "-C", root)
	argv = append(argv, arguments...)
	command := exec.CommandContext(ctx, "git", argv...)
	command.Env = gitEnvironment()
	// A deliberately detached descendant can outlive the managed process tree
	// while retaining Git's output handles. Bound exec's pipe-copy wait so
	// context cancellation still returns to Verify deterministically.
	command.WaitDelay = gitPipeDrainLimit
	return command
}

func gitEnvironment() []string {
	blocked := map[string]struct{}{
		"GIT_DIR": {}, "GIT_WORK_TREE": {}, "GIT_COMMON_DIR": {}, "GIT_INDEX_FILE": {},
		"GIT_OBJECT_DIRECTORY": {}, "GIT_ALTERNATE_OBJECT_DIRECTORIES": {}, "GIT_NAMESPACE": {},
		"GIT_REPLACE_REF_BASE": {}, "GIT_NO_REPLACE_OBJECTS": {}, "GIT_CEILING_DIRECTORIES": {},
		"GIT_DISCOVERY_ACROSS_FILESYSTEM": {}, "GIT_GLOB_PATHSPECS": {}, "GIT_NOGLOB_PATHSPECS": {},
		"GIT_LITERAL_PATHSPECS": {}, "GIT_ICASE_PATHSPECS": {}, "GIT_EXTERNAL_DIFF": {}, "GIT_DIFF_OPTS": {},
		"LC_ALL": {}, "LANG": {}, "GIT_OPTIONAL_LOCKS": {}, "GIT_PAGER": {},
	}
	environment := make([]string, 0, len(os.Environ())+6)
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		upper := strings.ToUpper(key)
		if _, denied := blocked[upper]; denied || strings.HasPrefix(upper, "GIT_CONFIG_") {
			continue
		}
		environment = append(environment, item)
	}
	return append(environment,
		"GIT_NO_REPLACE_OBJECTS=1", "GIT_OPTIONAL_LOCKS=0", "GIT_PAGER=cat", "LC_ALL=C", "LANG=C",
	)
}

func gitCommandError(commandError error, stderr []byte) error {
	detail := strings.TrimSpace(string(bytes.ToValidUTF8(stderr, []byte("?"))))
	if detail == "" {
		return commandError
	}
	return fmt.Errorf("%s: %w", detail, commandError)
}

func sortedPaths(values map[string]struct{}) []string {
	paths := make([]string, 0, len(values))
	for path := range values {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}
