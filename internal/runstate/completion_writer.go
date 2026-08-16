package runstate

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
)

const completionTempAttempts = 100

type completionStore struct {
	repository *os.Root
	parent     *os.Root
	run        *os.Root
	runInfo    fs.FileInfo
	runID      string
	fault      func(string) error
	name       func() (string, error)
	tempHooks  completionTempHooks
}

func openCompletionStore(validated *ValidatedRun, hooks completeHooks) (*completionStore, error) {
	repository, err := os.OpenRoot(validated.repository)
	if err != nil {
		return nil, completionEvidenceError("Could not open the repository for completion publication.", err)
	}
	failed := true
	defer func() {
		if failed {
			_ = repository.Close()
		}
	}()
	parent, err := repository.OpenRoot(".seal/evidence/" + validated.taskID)
	if err != nil {
		return nil, completionEvidenceError("Could not open the Task Evidence directory for completion publication.", err)
	}
	defer func() {
		if failed {
			_ = parent.Close()
		}
	}()
	run, err := parent.OpenRoot(validated.runID)
	if err != nil {
		return nil, completionEvidenceError("Could not open the Evidence Run directory for completion publication.", err)
	}
	store := &completionStore{
		repository: repository,
		parent:     parent,
		run:        run,
		runInfo:    validated.runDirectoryInfo,
		runID:      validated.runID,
		fault:      hooks.writerFault,
		name:       hooks.tempNameGenerator,
		tempHooks:  hooks.tempHooks,
	}
	if err := store.validateBinding(); err != nil {
		_ = run.Close()
		return nil, err
	}
	failed = false
	return store, nil
}

func (store *completionStore) close() {
	if store.run != nil {
		_ = store.run.Close()
		store.run = nil
	}
	if store.parent != nil {
		_ = store.parent.Close()
		store.parent = nil
	}
	if store.repository != nil {
		_ = store.repository.Close()
		store.repository = nil
	}
}

func (store *completionStore) validateBinding() error {
	if store.run == nil || store.parent == nil || store.runInfo == nil {
		return &EvidenceError{message: "Completion publication lost its validated Evidence Run binding."}
	}
	opened, err := store.run.Stat(".")
	if err != nil || !opened.IsDir() || !os.SameFile(store.runInfo, opened) {
		return completionEvidenceError("Validated Evidence Run directory changed before completion publication.", err)
	}
	named, err := store.parent.Lstat(store.runID)
	if err != nil || named.Mode()&os.ModeSymlink != 0 || !named.IsDir() || !os.SameFile(store.runInfo, named) {
		return completionEvidenceError("Validated Evidence Run destination changed before completion publication.", err)
	}
	return nil
}

func (store *completionStore) readExisting(validated *ValidatedRun) ([]byte, error) {
	if err := store.validateBinding(); err != nil {
		return nil, err
	}
	info, err := store.run.Lstat("completion.json")
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, completionEvidenceError("Could not inspect completion.json.", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, &EvidenceError{message: "completion.json must be an existing regular file or be absent."}
	}
	file, err := store.run.OpenFile("completion.json", os.O_RDONLY, 0)
	if err != nil {
		return nil, completionEvidenceError("Could not open completion.json.", err)
	}
	opened, statErr := file.Stat()
	if statErr != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		_ = file.Close()
		return nil, completionEvidenceError("completion.json changed while it was being opened.", statErr)
	}
	contents, readErr := io.ReadAll(file)
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return nil, completionEvidenceError("Could not read completion.json.", errors.Join(readErr, closeErr))
	}
	if err := validateCompletionRecord(contents, validated); err != nil {
		return nil, err
	}
	return contents, nil
}

func (store *completionStore) publish(validated *ValidatedRun, contents []byte) (winner []byte, resultErr error) {
	nameGenerator := store.name
	if nameGenerator == nil {
		nameGenerator = generateCompletionTempName
	}
	var tempName string
	var tempInfo fs.FileInfo
	defer func() {
		if tempName == "" {
			return
		}
		cleanupErr := store.removeTemp(tempName, tempInfo)
		if cleanupErr == nil {
			return
		}
		if resultErr == nil {
			resultErr = completionEvidenceError("Could not clean private completion staging residue.", cleanupErr)
			winner = nil
			return
		}
		resultErr = completionEvidenceError(
			"Completion publication failed and private staging cleanup also failed.",
			errors.Join(resultErr, cleanupErr),
		)
		winner = nil
	}()

	var file *os.File
	for attempt := 0; attempt < completionTempAttempts; attempt++ {
		generated, err := nameGenerator()
		if err != nil || !validCompletionTempName(generated) {
			return nil, completionEvidenceError("Could not allocate a private completion staging name.", err)
		}
		var createdInfo fs.FileInfo
		file, createdInfo, err = createPrivateCompletionTempWithHooks(store.run, generated, store.tempHooks)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return nil, completionEvidenceError("Could not create a private completion staging file.", err)
		}
		tempName = generated
		tempInfo = createdInfo
		break
	}
	if file == nil {
		return nil, &EvidenceError{message: "Could not allocate a private completion staging file after 100 collisions."}
	}
	if tempInfo == nil || !tempInfo.Mode().IsRegular() {
		_ = file.Close()
		return nil, &EvidenceError{message: "Could not bind the private completion staging file identity."}
	}
	if err := store.inject("temp-created"); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := store.inject("write"); err != nil {
		_ = file.Close()
		return nil, err
	}
	if _, err := file.Write(contents); err != nil {
		_ = file.Close()
		return nil, completionEvidenceError("Could not write the private completion staging file.", err)
	}
	if err := store.inject("sync-file"); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return nil, completionEvidenceError("Could not synchronize the private completion staging file.", err)
	}
	if err := file.Close(); err != nil {
		return nil, completionEvidenceError("Could not close the private completion staging file.", err)
	}
	if err := store.validateBinding(); err != nil {
		return nil, err
	}
	if err := store.inject("sync-directory"); err != nil {
		return nil, err
	}
	if err := syncDirectory(store.run); err != nil {
		return nil, completionEvidenceError("Could not synchronize the Evidence Run directory before completion publication.", err)
	}
	if err := store.inject("publish"); err != nil {
		return nil, err
	}
	if err := publishCompletionNoReplace(store.run, tempName, "completion.json", tempInfo); err != nil {
		if errors.Is(err, fs.ErrExist) {
			winner, readErr := store.readExisting(validated)
			if readErr != nil {
				return nil, readErr
			}
			if winner == nil {
				return nil, &EvidenceError{message: "Concurrent completion publication winner disappeared before validation."}
			}
			return winner, nil
		}
		return nil, completionEvidenceError("Could not atomically publish completion.json without replacement.", err)
	}
	tempName = ""
	_ = syncDirectory(store.run)
	return contents, nil
}

func (store *completionStore) removeTemp(name string, expected fs.FileInfo) error {
	if expected == nil {
		return errors.New("completion staging identity is unavailable")
	}
	named, err := store.run.Lstat(name)
	if err != nil {
		return err
	}
	if named.Mode()&os.ModeSymlink != 0 || !named.Mode().IsRegular() || !os.SameFile(expected, named) {
		return errors.New("completion staging destination identity changed before cleanup")
	}
	return store.run.Remove(name)
}

func (store *completionStore) inject(point string) error {
	if store.fault == nil {
		return nil
	}
	if err := store.fault(point); err != nil {
		return completionEvidenceError("Injected completion writer failure at "+point+".", err)
	}
	return nil
}

func completionEvidenceError(message string, cause error) error {
	if cause == nil {
		return &EvidenceError{message: message}
	}
	return &EvidenceError{message: message + " " + cause.Error()}
}

func generateCompletionTempName() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return ".completion-" + hex.EncodeToString(value) + ".tmp", nil
}

func validCompletionTempName(value string) bool {
	const prefix = ".completion-"
	const suffix = ".tmp"
	if len(value) != len(prefix)+32+len(suffix) || value[:len(prefix)] != prefix || value[len(value)-len(suffix):] != suffix {
		return false
	}
	for _, character := range value[len(prefix) : len(value)-len(suffix)] {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	return true
}

func completionTempIdentity(root *os.Root, name string, expected fs.FileInfo) error {
	if err := validateRelativeName(name); err != nil {
		return err
	}
	actual, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if expected == nil || actual.Mode()&os.ModeSymlink != 0 || !actual.Mode().IsRegular() || !os.SameFile(expected, actual) {
		return fmt.Errorf("completion staging file identity changed")
	}
	return nil
}
