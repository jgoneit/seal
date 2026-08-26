package releasegate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var surfaceExcludedDocuments = map[string]struct{}{
	".gitignore":           {},
	"AGENTS.md":            {},
	"MIGRATION_CHARTER.md": {},
	"README.md":            {},
	"REFERENCE.md":         {},
	"RELEASING.md":         {},
	"TRUST_MODEL.md":       {},
}

type gitRepository struct {
	root string
}

type treeRecord struct {
	mode     string
	typeName string
	oid      string
	path     string
	contents []byte
}

// ReleaseResult reports whether a full stable gate or an RC no-report gate ran.
type ReleaseResult struct {
	ReleaseTag string
	RCTag      string
	ReportPath string
	RC         bool
}

// ValidateRelease validates the release tag. RC tags require no report. Stable
// tags require the highest same-base RC's complete report and an unchanged
// normalized acceptance surface.
func ValidateRelease(ctx context.Context, repository, releaseTag, reportsDirectory string) (ReleaseResult, error) {
	repository, reportsDirectory, err := resolveReleasePaths(repository, reportsDirectory)
	if err != nil {
		return ReleaseResult{}, err
	}
	parsed, err := parseReleaseTag(releaseTag)
	if err != nil {
		return ReleaseResult{}, err
	}
	git := gitRepository{root: repository}
	releaseCommit, err := git.annotatedTagCommit(ctx, releaseTag)
	if err != nil {
		return ReleaseResult{}, err
	}
	if parsed.rcNumber > 0 {
		if _, err := git.surfaceDigest(ctx, releaseCommit, strings.TrimPrefix(releaseTag, "v")); err != nil {
			return ReleaseResult{}, fmt.Errorf("validate RC acceptance surface: %w", err)
		}
		return ReleaseResult{ReleaseTag: releaseTag, RCTag: releaseTag, RC: true}, nil
	}

	rcTag, err := git.highestSameBaseRC(ctx, releaseTag)
	if err != nil {
		return ReleaseResult{}, err
	}
	reportPath := filepath.Join(reportsDirectory, rcTag+".json")
	reportRelativePath := "release/acceptance/" + rcTag + ".json"
	reportContents, err := git.regularBlobAt(ctx, releaseCommit, reportRelativePath)
	if err != nil {
		return ReleaseResult{}, fmt.Errorf("stable acceptance report %s: %w", filepath.Base(reportPath), err)
	}
	report, err := readReportContents(filepath.Base(reportPath), reportContents, true)
	if err != nil {
		return ReleaseResult{}, fmt.Errorf("stable acceptance report %s: %w", filepath.Base(reportPath), err)
	}
	if report.Candidate.RCTag != rcTag {
		return ReleaseResult{}, fmt.Errorf("report candidate RC %s does not match highest same-base RC %s", report.Candidate.RCTag, rcTag)
	}
	if report.Candidate.TargetStableTag != releaseTag {
		return ReleaseResult{}, fmt.Errorf("report target %s does not match stable tag %s", report.Candidate.TargetStableTag, releaseTag)
	}
	rcCommit, err := git.annotatedTagCommit(ctx, rcTag)
	if err != nil {
		return ReleaseResult{}, err
	}
	if report.Candidate.RCCommit != rcCommit {
		return ReleaseResult{}, fmt.Errorf("report candidate commit %s does not match %s peeled commit %s", report.Candidate.RCCommit, rcTag, rcCommit)
	}
	ancestor, err := git.isAncestor(ctx, rcCommit, releaseCommit)
	if err != nil {
		return ReleaseResult{}, err
	}
	if !ancestor {
		return ReleaseResult{}, fmt.Errorf("candidate RC %s is not an ancestor of stable tag %s", rcTag, releaseTag)
	}
	taggerTime, err := git.annotatedTaggerTime(ctx, rcTag)
	if err != nil {
		return ReleaseResult{}, err
	}
	if !report.Window.StartedAt.After(taggerTime) {
		return ReleaseResult{}, fmt.Errorf("report window must begin after candidate RC %s was tagged", rcTag)
	}
	stableTaggerTime, err := git.annotatedTaggerTime(ctx, releaseTag)
	if err != nil {
		return ReleaseResult{}, err
	}
	if report.Window.EndedAt.After(stableTaggerTime) {
		return ReleaseResult{}, fmt.Errorf("report window must end no later than stable tag %s was tagged", releaseTag)
	}

	rcDigest, err := git.surfaceDigest(ctx, rcCommit, strings.TrimPrefix(rcTag, "v"))
	if err != nil {
		return ReleaseResult{}, fmt.Errorf("compute candidate acceptance surface: %w", err)
	}
	stableDigest, err := git.surfaceDigest(ctx, releaseCommit, strings.TrimPrefix(releaseTag, "v"))
	if err != nil {
		return ReleaseResult{}, fmt.Errorf("compute stable acceptance surface: %w", err)
	}
	if stableDigest != rcDigest {
		return ReleaseResult{}, fmt.Errorf("stable acceptance surface %s differs from candidate surface %s; cut a new RC and reset the report", stableDigest, rcDigest)
	}
	return ReleaseResult{ReleaseTag: releaseTag, RCTag: rcTag, ReportPath: reportPath}, nil
}

func (git gitRepository) regularBlobAt(ctx context.Context, commit, path string) ([]byte, error) {
	output, err := git.run(ctx, "ls-tree", "-z", "--full-tree", commit, "--", path)
	if err != nil {
		return nil, fmt.Errorf("inspect tagged path %s: %w", path, err)
	}
	entries := bytes.Split(output, []byte{0})
	if len(entries) != 2 || len(entries[0]) == 0 || len(entries[1]) != 0 {
		if len(output) == 0 {
			return nil, fmt.Errorf("is absent from the stable tag")
		}
		return nil, fmt.Errorf("tagged path has an unsupported tree representation")
	}
	entry := entries[0]
	tab := bytes.IndexByte(entry, '\t')
	if tab < 0 {
		return nil, fmt.Errorf("tagged path has an unsupported tree representation")
	}
	header := strings.Fields(string(entry[:tab]))
	if len(header) != 3 || string(entry[tab+1:]) != path {
		return nil, fmt.Errorf("tagged path has an unsupported tree representation")
	}
	mode, typeName, objectID := header[0], header[1], header[2]
	if typeName != "blob" || (mode != "100644" && mode != "100755") {
		return nil, fmt.Errorf("must be a regular blob, got mode %s type %s", mode, typeName)
	}
	sizeText, err := git.runText(ctx, "cat-file", "-s", objectID)
	if err != nil {
		return nil, fmt.Errorf("read tagged path size for %s: %w", path, err)
	}
	size, err := strconv.ParseInt(sizeText, 10, 64)
	if err != nil || size < 0 {
		return nil, fmt.Errorf("tagged path %s has an invalid blob size", path)
	}
	if size > maximumReportBytes {
		return nil, fmt.Errorf("exceeds the %d-byte safety limit", maximumReportBytes)
	}
	contents, err := git.run(ctx, "cat-file", "blob", objectID)
	if err != nil {
		return nil, fmt.Errorf("read tagged path %s: %w", path, err)
	}
	return contents, nil
}

type parsedReleaseTag struct {
	rcNumber int
}

func parseReleaseTag(value string) (parsedReleaseTag, error) {
	if stableTagPattern.MatchString(value) {
		return parsedReleaseTag{}, nil
	}
	_, number, ok := parseRCTag(value)
	if !ok {
		return parsedReleaseTag{}, fmt.Errorf("release tag must be vMAJOR.MINOR.PATCH or vMAJOR.MINOR.PATCH-rc.N")
	}
	return parsedReleaseTag{rcNumber: number}, nil
}

func resolveReleasePaths(repository, reportsDirectory string) (string, string, error) {
	root, err := filepath.Abs(repository)
	if err != nil {
		return "", "", fmt.Errorf("resolve repository: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return "", "", fmt.Errorf("repository must be an existing directory")
	}
	reports := reportsDirectory
	if !filepath.IsAbs(reports) {
		reports = filepath.Join(root, reports)
	}
	reports, err = filepath.Abs(reports)
	if err != nil {
		return "", "", fmt.Errorf("resolve acceptance report directory: %w", err)
	}
	relative, err := filepath.Rel(root, reports)
	if err != nil || filepath.ToSlash(relative) != "release/acceptance" {
		return "", "", fmt.Errorf("acceptance reports must be read from release/acceptance inside the repository")
	}
	return root, reports, nil
}

func (git gitRepository) annotatedTagCommit(ctx context.Context, tag string) (string, error) {
	ref := "refs/tags/" + tag
	typeName, err := git.runText(ctx, "cat-file", "-t", ref)
	if err != nil {
		return "", fmt.Errorf("inspect release tag %s: %w", tag, err)
	}
	if typeName != "tag" {
		return "", fmt.Errorf("release tag %s must be annotated", tag)
	}
	commit, err := git.runText(ctx, "rev-parse", ref+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("resolve release tag %s: %w", tag, err)
	}
	if !lowerHexCommitPattern.MatchString(commit) {
		return "", fmt.Errorf("release tag %s resolved to unsupported object id %q", tag, commit)
	}
	return commit, nil
}

func (git gitRepository) annotatedTaggerTime(ctx context.Context, tag string) (time.Time, error) {
	value, err := git.runText(ctx, "for-each-ref", "--format=%(taggerdate:unix)", "refs/tags/"+tag)
	if err != nil {
		return time.Time{}, fmt.Errorf("read tagger time for %s: %w", tag, err)
	}
	seconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("release tag %s has no canonical tagger time", tag)
	}
	return time.Unix(seconds, 0).UTC(), nil
}

func (git gitRepository) highestSameBaseRC(ctx context.Context, stableTag string) (string, error) {
	output, err := git.run(ctx, "tag", "--list", stableTag+"-rc.*")
	if err != nil {
		return "", fmt.Errorf("list same-base RC tags: %w", err)
	}
	highest := 0
	highestTag := ""
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if line == "" {
			continue
		}
		base, number, ok := parseRCTag(line)
		if !ok || base != stableTag {
			return "", fmt.Errorf("same-base tag %q does not use the canonical RC spelling", line)
		}
		if number > highest {
			highest = number
			highestTag = line
		}
	}
	if highestTag == "" {
		return "", fmt.Errorf("stable tag %s has no same-base RC candidate", stableTag)
	}
	return highestTag, nil
}

func (git gitRepository) isAncestor(ctx context.Context, ancestor, descendant string) (bool, error) {
	command := exec.CommandContext(ctx, "git", "-C", git.root, "merge-base", "--is-ancestor", ancestor, descendant)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	err := command.Run()
	if err == nil {
		return true, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("check Git ancestry: %s", strings.TrimSpace(stderr.String()))
}

func (git gitRepository) surfaceDigest(ctx context.Context, commit, expectedVersion string) (string, error) {
	records, err := git.treeRecords(ctx, commit)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	coreVersionSeen := false
	pluginVersionSeen := false
	for _, record := range records {
		if surfacePathExcluded(record.path) {
			continue
		}
		contents := record.contents
		switch record.path {
		case "cmd/seal/main.go":
			coreVersionSeen = true
			contents, err = normalizeGoVersion(contents, expectedVersion)
		case ".codex-plugin/plugin.json":
			pluginVersionSeen = true
			contents, err = normalizePluginVersion(contents, expectedVersion)
		}
		if err != nil {
			return "", fmt.Errorf("normalize %s: %w", record.path, err)
		}
		writeDigestField(hash, []byte(record.mode))
		writeDigestField(hash, []byte(record.typeName))
		writeDigestField(hash, []byte(record.path))
		writeDigestField(hash, contents)
	}
	if !coreVersionSeen {
		return "", fmt.Errorf("acceptance surface is missing cmd/seal/main.go")
	}
	if !pluginVersionSeen {
		return "", fmt.Errorf("acceptance surface is missing .codex-plugin/plugin.json")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func surfacePathExcluded(path string) bool {
	if _, excluded := surfaceExcludedDocuments[path]; excluded {
		return true
	}
	if strings.HasPrefix(path, "release/acceptance/") {
		name := strings.TrimPrefix(path, "release/acceptance/")
		return reportNamePattern.MatchString(name)
	}
	return false
}

func (git gitRepository) treeRecords(ctx context.Context, commit string) ([]treeRecord, error) {
	output, err := git.run(ctx, "ls-tree", "-rz", "--full-tree", commit)
	if err != nil {
		return nil, fmt.Errorf("list Git tree %s: %w", commit, err)
	}
	entries := bytes.Split(output, []byte{0})
	records := make([]treeRecord, 0, len(entries))
	for _, entry := range entries {
		if len(entry) == 0 {
			continue
		}
		tab := bytes.IndexByte(entry, '\t')
		if tab < 0 {
			return nil, fmt.Errorf("Git tree entry omitted its path")
		}
		header := strings.Fields(string(entry[:tab]))
		pathBytes := entry[tab+1:]
		if len(header) != 3 || !utf8.Valid(pathBytes) {
			return nil, fmt.Errorf("Git tree entry has an unsupported representation")
		}
		record := treeRecord{mode: header[0], typeName: header[1], oid: header[2], path: string(pathBytes)}
		switch record.typeName {
		case "blob":
			record.contents, err = git.run(ctx, "cat-file", "blob", record.oid)
		case "commit":
			record.contents = []byte(record.oid)
		default:
			err = fmt.Errorf("unsupported Git tree object type %q", record.typeName)
		}
		if err != nil {
			return nil, fmt.Errorf("read Git object for %s: %w", record.path, err)
		}
		records = append(records, record)
	}
	sort.Slice(records, func(left, right int) bool { return records[left].path < records[right].path })
	return records, nil
}

func normalizeGoVersion(contents []byte, expectedVersion string) ([]byte, error) {
	set := token.NewFileSet()
	file, err := parser.ParseFile(set, "cmd/seal/main.go", contents, 0)
	if err != nil {
		return nil, err
	}
	var literal *ast.BasicLit
	count := 0
	for _, declaration := range file.Decls {
		general, ok := declaration.(*ast.GenDecl)
		if !ok || general.Tok != token.CONST {
			continue
		}
		for _, specification := range general.Specs {
			value, ok := specification.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for index, name := range value.Names {
				if name.Name != "version" {
					continue
				}
				count++
				if index >= len(value.Values) {
					return nil, fmt.Errorf("version constant has no literal value")
				}
				literal, ok = value.Values[index].(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					return nil, fmt.Errorf("version constant must be a string literal")
				}
			}
		}
	}
	if count != 1 || literal == nil {
		return nil, fmt.Errorf("expected exactly one version constant, found %d", count)
	}
	value, err := strconv.Unquote(literal.Value)
	if err != nil || value != expectedVersion {
		return nil, fmt.Errorf("version constant %q does not match tag version %q", value, expectedVersion)
	}
	start := set.Position(literal.Pos()).Offset
	end := set.Position(literal.End()).Offset
	if start < 0 || end < start || end > len(contents) {
		return nil, fmt.Errorf("version literal offsets are invalid")
	}
	normalized := make([]byte, 0, len(contents)-end+start+len(`"__SEAL_VERSION__"`))
	normalized = append(normalized, contents[:start]...)
	normalized = append(normalized, `"__SEAL_VERSION__"`...)
	normalized = append(normalized, contents[end:]...)
	return normalized, nil
}

func normalizePluginVersion(contents []byte, expectedVersion string) ([]byte, error) {
	if !utf8.Valid(contents) {
		return nil, fmt.Errorf("plugin manifest is not valid UTF-8")
	}
	if err := rejectDuplicateJSONKeys(contents); err != nil {
		return nil, err
	}
	start, end, version, err := topLevelJSONStringSpan(contents, "version")
	if err != nil {
		return nil, fmt.Errorf("plugin manifest version: %w", err)
	}
	baseVersion := strings.SplitN(version, "+", 2)[0]
	if baseVersion != expectedVersion {
		return nil, fmt.Errorf("plugin version %q does not match tag version %q", version, expectedVersion)
	}
	normalized := make([]byte, 0, len(contents)-(end-start)+len(`"__SEAL_VERSION__"`))
	normalized = append(normalized, contents[:start]...)
	normalized = append(normalized, `"__SEAL_VERSION__"`...)
	normalized = append(normalized, contents[end:]...)
	return normalized, nil
}

func topLevelJSONStringSpan(contents []byte, field string) (int, int, string, error) {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil {
		return 0, 0, "", err
	}
	if token != json.Delim('{') {
		return 0, 0, "", fmt.Errorf("top-level value must be an object")
	}
	found := false
	start := 0
	end := 0
	value := ""
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return 0, 0, "", err
		}
		key, ok := keyToken.(string)
		if !ok {
			return 0, 0, "", fmt.Errorf("top-level key must be a string")
		}
		valueSearchStart := int(decoder.InputOffset())
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return 0, 0, "", err
		}
		if key != field {
			continue
		}
		segmentEnd := int(decoder.InputOffset())
		relative := bytes.LastIndex(contents[valueSearchStart:segmentEnd], raw)
		if relative < 0 {
			return 0, 0, "", fmt.Errorf("could not locate encoded value")
		}
		start = valueSearchStart + relative
		end = start + len(raw)
		if err := json.Unmarshal(raw, &value); err != nil {
			return 0, 0, "", fmt.Errorf("must be a string")
		}
		found = true
	}
	closing, err := decoder.Token()
	if err != nil {
		return 0, 0, "", err
	}
	if closing != json.Delim('}') {
		return 0, 0, "", fmt.Errorf("top-level object is not closed")
	}
	if err := requireJSONEOF(decoder); err != nil {
		return 0, 0, "", err
	}
	if !found {
		return 0, 0, "", fmt.Errorf("is required")
	}
	return start, end, value, nil
}

func writeDigestField(hash interface{ Write([]byte) (int, error) }, value []byte) {
	_, _ = hash.Write([]byte(strconv.Itoa(len(value))))
	_, _ = hash.Write([]byte{':'})
	_, _ = hash.Write(value)
	_, _ = hash.Write([]byte{0})
}

func (git gitRepository) runText(ctx context.Context, arguments ...string) (string, error) {
	output, err := git.run(ctx, arguments...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func (git gitRepository) run(ctx context.Context, arguments ...string) ([]byte, error) {
	commandArguments := append([]string{"-C", git.root}, arguments...)
	command := exec.CommandContext(ctx, "git", commandArguments...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return nil, fmt.Errorf("git %s: %s", strings.Join(arguments, " "), message)
	}
	return stdout.Bytes(), nil
}
