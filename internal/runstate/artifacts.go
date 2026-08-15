package runstate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func safeRunPath(value any, context string) (string, error) {
	path, ok := value.(string)
	if !ok || path == "" {
		return "", &EvidenceError{message: context + " must be a non-empty relative POSIX path."}
	}
	if strings.Contains(path, `\`) || strings.ContainsRune(path, 0) {
		return "", &EvidenceError{message: context + " must use a relative POSIX path."}
	}
	if strings.HasPrefix(path, "/") || len(path) >= 2 && path[1] == ':' {
		return "", &EvidenceError{message: context + " must stay inside the Run directory."}
	}
	parts := strings.Split(path, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", &EvidenceError{message: context + " must stay inside the Run directory."}
		}
	}
	return strings.Join(parts, "/"), nil
}

func readArtifact(runDirectory, relativePath string) ([]byte, error) {
	filesystemRelative, err := surrogateEscapeFilesystemPath(relativePath)
	if err != nil {
		return nil, &EvidenceError{message: "unsafe"}
	}
	parts := strings.Split(filesystemRelative, "/")
	candidate := filepath.Join(append([]string{runDirectory}, parts...)...)
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return nil, &EvidenceError{message: "missing"}
	}
	inside, err := pathInside(runDirectory, resolved)
	if err != nil || !inside {
		return nil, &EvidenceError{message: "unsafe"}
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return nil, &EvidenceError{message: "missing"}
	}
	if !info.Mode().IsRegular() {
		return nil, &EvidenceError{message: "unsafe"}
	}
	contents, err := os.ReadFile(resolved)
	if err != nil {
		return nil, &EvidenceError{message: "unreadable"}
	}
	return contents, nil
}

func surrogateEscapeFilesystemPath(logical string) (string, error) {
	var output strings.Builder
	output.Grow(len(logical))
	for index := 0; index < len(logical); {
		if unit, width, ok := encodedSurrogate(logical[index:]); ok {
			if unit < 0xdc80 || unit > 0xdcff {
				return "", fmt.Errorf("unsupported lone surrogate")
			}
			output.WriteByte(byte(unit - 0xdc00))
			index += width
			continue
		}
		output.WriteByte(logical[index])
		index++
	}
	return output.String(), nil
}

func readRequiredArtifact(runDirectory, relativePath string) ([]byte, error) {
	contents, err := readArtifact(runDirectory, relativePath)
	if err == nil {
		return contents, nil
	}
	var evidence *EvidenceError
	if !errorsAsEvidence(err, &evidence) {
		return nil, err
	}
	switch evidence.message {
	case "missing":
		return nil, &EvidenceError{message: fmt.Sprintf("Required evidence file is missing: %s.", relativePath)}
	case "unsafe":
		return nil, &EvidenceError{message: fmt.Sprintf("Required evidence file is unsafe or missing: %s.", relativePath)}
	default:
		return nil, &EvidenceError{message: fmt.Sprintf("Could not read evidence file: %s.", relativePath)}
	}
}

func errorsAsEvidence(err error, target **EvidenceError) bool {
	value, ok := err.(*EvidenceError)
	if ok {
		*target = value
	}
	return ok
}
