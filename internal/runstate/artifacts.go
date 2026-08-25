package runstate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
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
	return readArtifactContext(context.Background(), runDirectory, relativePath)
}

func readArtifactContext(ctx context.Context, runDirectory, relativePath string) ([]byte, error) {
	if err := artifactContextError(ctx); err != nil {
		return nil, err
	}
	file, err := openArtifact(runDirectory, relativePath)
	if err != nil {
		return nil, err
	}
	var contents bytes.Buffer
	buffer := make([]byte, 64*1024)
	var readError error
	for {
		if err := artifactContextError(ctx); err != nil {
			readError = err
			break
		}
		count, err := file.Read(buffer)
		if count > 0 {
			_, _ = contents.Write(buffer[:count])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			readError = &EvidenceError{message: "unreadable"}
			break
		}
	}
	closeError := file.Close()
	if readError != nil {
		return nil, readError
	}
	if closeError != nil {
		return nil, &EvidenceError{message: "unreadable"}
	}
	if err := artifactContextError(ctx); err != nil {
		return nil, err
	}
	return contents.Bytes(), nil
}

func openArtifact(runDirectory, relativePath string) (*os.File, error) {
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
	file, err := os.Open(resolved)
	if err != nil {
		return nil, &EvidenceError{message: "unreadable"}
	}
	return file, nil
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
	return readRequiredArtifactContext(context.Background(), runDirectory, relativePath)
}

func readRequiredArtifactContext(ctx context.Context, runDirectory, relativePath string) ([]byte, error) {
	contents, err := readArtifactContext(ctx, runDirectory, relativePath)
	if err == nil {
		return contents, nil
	}
	evidence, ok := err.(*EvidenceError)
	if !ok {
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

func artifactContextError(ctx context.Context) error {
	if ctx == nil {
		return context.Canceled
	}
	return ctx.Err()
}

func hashArtifactContext(ctx context.Context, runDirectory, relativePath string) (int64, string, error) {
	if err := artifactContextError(ctx); err != nil {
		return 0, "", err
	}
	file, err := openArtifact(runDirectory, relativePath)
	if err != nil {
		return 0, "", err
	}
	digest := sha256.New()
	buffer := make([]byte, 64*1024)
	var size int64
	var readError error
	for {
		if err := artifactContextError(ctx); err != nil {
			readError = err
			break
		}
		count, err := file.Read(buffer)
		if count > 0 {
			_, _ = digest.Write(buffer[:count])
			size += int64(count)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			readError = &EvidenceError{message: "unreadable"}
			break
		}
	}
	closeError := file.Close()
	if readError != nil {
		return 0, "", readError
	}
	if closeError != nil {
		return 0, "", &EvidenceError{message: "unreadable"}
	}
	if err := artifactContextError(ctx); err != nil {
		return 0, "", err
	}
	return size, hex.EncodeToString(digest.Sum(nil)), nil
}

func hashBytesContext(ctx context.Context, contents []byte) (string, error) {
	digest := sha256.New()
	const chunkSize = 64 * 1024
	for len(contents) > 0 {
		if err := artifactContextError(ctx); err != nil {
			return "", err
		}
		chunk := contents
		if len(chunk) > chunkSize {
			chunk = chunk[:chunkSize]
		}
		_, _ = digest.Write(chunk)
		contents = contents[len(chunk):]
	}
	if err := artifactContextError(ctx); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}
