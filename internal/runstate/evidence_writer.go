package runstate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const runAllocationAttempts = 100

type evidenceWriter struct {
	repository  string
	taskID      string
	runID       string
	stagingName string
	root        *os.Root
	parent      *os.Root
	staging     *os.Root
	stagingInfo fs.FileInfo
	fault       func(point string) error
}

func newEvidenceWriter(repository, taskID string, hooks verifyHooks) (*evidenceWriter, error) {
	root, err := os.OpenRoot(repository)
	if err != nil {
		return nil, &RepositoryError{message: "Could not open the repository for Evidence publication."}
	}
	failed := true
	defer func() {
		if failed {
			_ = root.Close()
		}
	}()

	for _, directory := range []struct {
		path string
		mode fs.FileMode
	}{
		{path: ".seal", mode: 0o755},
		{path: ".seal/evidence", mode: 0o700},
		{path: ".seal/evidence/" + taskID, mode: 0o700},
	} {
		if err := ensureRealDirectory(root, directory.path, directory.mode); err != nil {
			return nil, err
		}
	}
	parent, err := root.OpenRoot(".seal/evidence/" + taskID)
	if err != nil {
		return nil, &RepositoryError{message: "Could not open the Task Evidence directory."}
	}

	writer := &evidenceWriter{
		repository: repository,
		taskID:     taskID,
		root:       root,
		parent:     parent,
		fault:      hooks.writerFault,
	}
	for attempt := 0; attempt < runAllocationAttempts; attempt++ {
		generator := hooks.runIDGenerator
		if generator == nil {
			generator = generateRunID
		}
		runID, err := generator()
		if err != nil {
			_ = parent.Close()
			return nil, &RepositoryError{message: "Could not allocate a verification Run id."}
		}
		if !validGeneratedRunID(runID) {
			_ = parent.Close()
			return nil, &RepositoryError{message: "Generated verification Run id was not 32 lowercase hexadecimal characters."}
		}
		stagingName := ".tmp-" + runID
		runExists, err := pathExists(parent, runID)
		if err != nil {
			_ = parent.Close()
			return nil, &RepositoryError{message: "Could not inspect an Evidence Run destination."}
		}
		stagingExists, err := pathExists(parent, stagingName)
		if err != nil {
			_ = parent.Close()
			return nil, &RepositoryError{message: "Could not inspect an Evidence staging destination."}
		}
		if runExists || stagingExists {
			continue
		}
		if err := createPrivateStagingDirectory(parent, stagingName); err != nil {
			if errors.Is(err, fs.ErrExist) {
				continue
			}
			_ = parent.Close()
			return nil, &RepositoryError{message: "Could not create the private Evidence staging directory."}
		}
		staging, err := parent.OpenRoot(stagingName)
		if err != nil {
			_ = parent.RemoveAll(stagingName)
			_ = parent.Close()
			return nil, &RepositoryError{message: "Could not open the Evidence staging directory."}
		}
		stagingInfo, err := staging.Stat(".")
		if err != nil || !stagingInfo.IsDir() {
			_ = staging.Close()
			_ = parent.RemoveAll(stagingName)
			_ = parent.Close()
			return nil, &RepositoryError{message: "Could not bind the Evidence staging directory identity."}
		}
		writer.runID = runID
		writer.stagingName = stagingName
		writer.staging = staging
		writer.stagingInfo = stagingInfo
		failed = false
		return writer, nil
	}
	_ = parent.Close()
	return nil, &RepositoryError{message: "Could not allocate a unique verification Run id after 100 collisions."}
}

func ensureRealDirectory(root *os.Root, path string, mode fs.FileMode) error {
	info, err := root.Lstat(path)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return &RepositoryError{message: "Evidence writer path must be a real directory: " + path + "."}
		}
		return nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return &RepositoryError{message: "Could not inspect Evidence writer path: " + path + "."}
	}
	if err := root.Mkdir(path, mode); err != nil {
		if errors.Is(err, fs.ErrExist) {
			info, statErr := root.Lstat(path)
			if statErr == nil && info.Mode()&os.ModeSymlink == 0 && info.IsDir() {
				return nil
			}
		}
		return &RepositoryError{message: "Could not create Evidence writer path: " + path + "."}
	}
	if err := root.Chmod(path, mode); err != nil {
		return &RepositoryError{message: "Could not set Evidence writer directory permissions: " + path + "."}
	}
	return nil
}

func pathExists(root *os.Root, path string) (bool, error) {
	_, err := root.Lstat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func (writer *evidenceWriter) stagingPath() string {
	return filepath.Join(writer.repository, ".seal", "evidence", writer.taskID, writer.stagingName)
}

func (writer *evidenceWriter) validateStagingBinding() error {
	if writer.staging == nil || writer.stagingInfo == nil {
		return &RepositoryError{message: "Evidence staging directory is not open."}
	}
	opened, err := writer.staging.Stat(".")
	if err != nil || !os.SameFile(writer.stagingInfo, opened) {
		return &RepositoryError{message: "Evidence staging directory identity changed while verification was running."}
	}
	named, err := writer.parent.Lstat(writer.stagingName)
	if err != nil || named.Mode()&os.ModeSymlink != 0 || !named.IsDir() || !os.SameFile(writer.stagingInfo, named) {
		return &RepositoryError{message: "Evidence staging destination changed while verification was running."}
	}
	return nil
}

func (writer *evidenceWriter) write(path string, contents []byte) error {
	if _, err := safeRunPath(path, "Evidence output path"); err != nil {
		return &RepositoryError{message: err.Error()}
	}
	if err := writer.inject("write:" + path); err != nil {
		return err
	}
	file, err := writer.staging.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return &RepositoryError{message: "Could not create Evidence file: " + path + "."}
	}
	writeErr := error(nil)
	if err := file.Chmod(0o600); err != nil {
		writeErr = err
	} else if _, err := file.Write(contents); err != nil {
		writeErr = err
	} else if err := writer.inject("sync-file:" + path); err != nil {
		writeErr = err
	} else if err := file.Sync(); err != nil {
		writeErr = err
	}
	if err := file.Close(); writeErr == nil {
		writeErr = err
	}
	if writeErr != nil {
		return &RepositoryError{message: "Could not persist Evidence file: " + path + "."}
	}
	return nil
}

func (writer *evidenceWriter) writeManifest(evidenceFiles []string) error {
	if err := writer.inject("manifest"); err != nil {
		return err
	}
	paths := append([]string(nil), evidenceFiles...)
	sort.Strings(paths)
	records := make([]any, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for index, path := range paths {
		if _, duplicate := seen[path]; duplicate {
			return &RepositoryError{message: "Generated Evidence file list contains a duplicate: " + path + "."}
		}
		seen[path] = struct{}{}
		contents, err := writer.readRegular(path)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(contents)
		records[index] = map[string]any{
			"path":       path,
			"size_bytes": json.Number(strconv.Itoa(len(contents))),
			"sha256":     hex.EncodeToString(digest[:]),
		}
	}
	checksDirectory, err := writer.staging.OpenRoot("checks")
	if err != nil {
		return &RepositoryError{message: "Could not open the generated check log directory."}
	}
	checksSyncError := syncDirectory(checksDirectory)
	checksCloseError := checksDirectory.Close()
	if checksSyncError != nil || checksCloseError != nil {
		return &RepositoryError{message: "Could not synchronize the generated check log directory."}
	}
	payload := map[string]any{
		"schema_version": json.Number("1"),
		"task_id":        writer.taskID,
		"run_id":         writer.runID,
		"files":          records,
	}
	canonical, err := canonicalJSON(payload, false)
	if err != nil {
		return &RepositoryError{message: "Could not compute the Evidence manifest digest."}
	}
	digest := sha256.Sum256(canonical)
	manifest := map[string]any{
		"schema_version":  json.Number("1"),
		"task_id":         writer.taskID,
		"run_id":          writer.runID,
		"files":           records,
		"evidence_sha256": hex.EncodeToString(digest[:]),
		"created_at":      runTimestampNow(),
	}
	encoded, err := renderEvidenceJSON(manifest)
	if err != nil {
		return &RepositoryError{message: "Could not render the Evidence manifest."}
	}
	return writer.write("run-manifest.json", encoded)
}

func (writer *evidenceWriter) readRegular(path string) ([]byte, error) {
	if _, err := safeRunPath(path, "Evidence manifest path"); err != nil {
		return nil, &RepositoryError{message: err.Error()}
	}
	info, err := writer.staging.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, &RepositoryError{message: "Generated Evidence file is missing or unsafe: " + path + "."}
	}
	file, err := writer.staging.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, &RepositoryError{message: "Could not read generated Evidence file: " + path + "."}
	}
	if err := writer.inject("sync-manifest-file:" + path); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return nil, &RepositoryError{message: "Could not synchronize generated Evidence file: " + path + "."}
	}
	contents, readErr := io.ReadAll(file)
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return nil, &RepositoryError{message: "Could not read generated Evidence file: " + path + "."}
	}
	return contents, nil
}

func (writer *evidenceWriter) commit() error {
	if writer.staging == nil {
		return &RepositoryError{message: "Evidence staging directory is not open."}
	}
	if err := writer.validateStagingBinding(); err != nil {
		return err
	}
	if err := writer.inject("sync-staging-directory"); err != nil {
		return err
	}
	if err := syncDirectory(writer.staging); err != nil {
		return &RepositoryError{message: "Could not synchronize the Evidence staging directory."}
	}
	if err := writer.staging.Close(); err != nil {
		return &RepositoryError{message: "Could not close the Evidence staging directory."}
	}
	writer.staging = nil
	if err := writer.inject("sync-publication-parent"); err != nil {
		return err
	}
	if err := syncDirectory(writer.parent); err != nil {
		return &RepositoryError{message: "Could not synchronize the Evidence publication directory."}
	}
	if err := writer.inject("publish"); err != nil {
		return err
	}
	if err := publishDirectoryNoReplace(writer.parent, writer.stagingName, writer.runID, writer.stagingInfo); err != nil {
		return &RepositoryError{message: "Could not atomically publish the Evidence Run: " + err.Error()}
	}
	_ = syncDirectory(writer.parent)
	_ = writer.parent.Close()
	_ = writer.root.Close()
	writer.parent = nil
	writer.root = nil
	return nil
}

func (writer *evidenceWriter) inject(point string) error {
	if writer.fault == nil {
		return nil
	}
	if err := writer.fault(point); err != nil {
		return &RepositoryError{message: "Injected Evidence writer failure at " + point + ": " + err.Error()}
	}
	return nil
}

func (writer *evidenceWriter) abort() error {
	var result error
	if writer.staging != nil {
		result = errors.Join(result, writer.staging.Close())
		writer.staging = nil
	}
	if writer.parent != nil && writer.stagingName != "" {
		result = errors.Join(result, writer.inject("abort-remove"))
		named, err := writer.parent.Lstat(writer.stagingName)
		switch {
		case err != nil:
			result = errors.Join(result, fmt.Errorf("could not locate bound staging directory during cleanup: %w", err))
		case writer.stagingInfo == nil || named.Mode()&os.ModeSymlink != 0 || !named.IsDir() || !os.SameFile(writer.stagingInfo, named):
			result = errors.Join(result, errors.New("staging destination identity changed before cleanup"))
		default:
			result = errors.Join(result, writer.parent.RemoveAll(writer.stagingName))
		}
	}
	if writer.parent != nil {
		result = errors.Join(result, writer.parent.Close())
		writer.parent = nil
	}
	if writer.root != nil {
		result = errors.Join(result, writer.root.Close())
		writer.root = nil
	}
	return result
}

func runTimestampNow() string {
	return runTimestamp(timeNow())
}

var timeNow = func() time.Time { return time.Now() }

func validateRelativeName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\\`) {
		return fmt.Errorf("unsafe publication name")
	}
	return nil
}

func validGeneratedRunID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			if character < 'a' || character > 'f' {
				return false
			}
		}
	}
	return true
}
