package installer_test

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const installerTestTag = "v1.2.3-rc.1"

func TestInstallShellCleanInstall(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("install.sh is supported only on Linux and macOS")
	}
	asset := unixAssetName(t)
	archive := unixArchive(t, strings.TrimPrefix(installerTestTag, "v"))
	checksums := checksumLine(archive, asset)
	server := releaseServer(t, installerTestTag, asset, archive, checksums)

	homeDirectory := t.TempDir()
	command := exec.Command("sh", repositoryPath(t, "install.sh"), "--version", installerTestTag)
	command.Env = installerEnvironment(server.URL, homeDirectory)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("install.sh failed: %v\n%s", err, output)
	}

	installDir := filepath.Join(homeDirectory, ".local", "bin")
	target := filepath.Join(installDir, "seal")
	versionOutput, err := exec.Command(target, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("installed binary failed: %v\n%s", err, versionOutput)
	}
	if got, want := string(versionOutput), "1.2.3-rc.1\n"; got != want {
		t.Fatalf("installed --version = %q, want %q", got, want)
	}
	if !strings.Contains(string(output), target) {
		t.Fatalf("installer output %q does not identify absolute target %q", output, target)
	}
	assertNoInstallerResidue(t, installDir)
}

func TestInstallShellRejectsUntrustedReleaseWithoutReplacingExistingBinary(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("install.sh is supported only on Linux and macOS")
	}
	asset := unixAssetName(t)
	matchingArchive := unixArchive(t, strings.TrimPrefix(installerTestTag, "v"))
	mismatchedArchive := unixArchive(t, "9.9.9")
	validChecksum := checksumLine(matchingArchive, asset)

	cases := []struct {
		name      string
		archive   []byte
		checksums string
		wantError string
	}{
		{name: "checksum mismatch", archive: matchingArchive, checksums: strings.Repeat("0", 64) + "  " + asset + "\n", wantError: "SHA-256 mismatch for " + asset},
		{name: "missing checksum", archive: matchingArchive, checksums: strings.Repeat("0", 64) + "  another-asset.tar.gz\n", wantError: "checksums.txt must contain exactly one entry for " + asset},
		{name: "duplicate checksum", archive: matchingArchive, checksums: validChecksum + validChecksum, wantError: "checksums.txt must contain exactly one entry for " + asset},
		{name: "malformed checksum", archive: matchingArchive, checksums: "not-a-sha256  " + asset + "\n", wantError: "checksums.txt has an invalid SHA-256 for " + asset},
		{name: "malformed duplicate", archive: matchingArchive, checksums: validChecksum + "not-a-sha256  " + asset + "\n", wantError: "checksums.txt must contain exactly one entry for " + asset},
		{name: "uppercase duplicate", archive: matchingArchive, checksums: validChecksum + strings.Repeat("A", 64) + "  " + asset + "\n", wantError: "checksums.txt must contain exactly one entry for " + asset},
		{name: "single-space duplicate", archive: matchingArchive, checksums: validChecksum + strings.Repeat("0", 64) + " " + asset + "\n", wantError: "checksums.txt must contain exactly one entry for " + asset},
		{name: "version mismatch", archive: mismatchedArchive, checksums: checksumLine(mismatchedArchive, asset), wantError: asset + " does not report requested version"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			server := releaseServer(t, installerTestTag, asset, testCase.archive, testCase.checksums)
			homeDirectory := t.TempDir()
			installDir := filepath.Join(homeDirectory, ".local", "bin")
			if err := os.MkdirAll(installDir, 0o700); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(installDir, "seal")
			original := []byte("existing-seal-binary\n")
			if err := os.WriteFile(target, original, 0o711); err != nil {
				t.Fatal(err)
			}

			command := exec.Command("sh", repositoryPath(t, "install.sh"), "--version", installerTestTag)
			command.Env = installerEnvironment(server.URL, homeDirectory)
			output, err := command.CombinedOutput()
			if err == nil {
				t.Fatalf("install.sh unexpectedly succeeded:\n%s", output)
			}
			if !strings.Contains(string(output), testCase.wantError) {
				t.Fatalf("install.sh error %q does not contain %q", output, testCase.wantError)
			}

			got, err := os.ReadFile(target)
			if err != nil {
				t.Fatalf("read preserved target: %v", err)
			}
			if !bytes.Equal(got, original) {
				t.Fatalf("existing target changed: got %q, want %q", got, original)
			}
			assertNoInstallerResidue(t, installDir)
		})
	}
}

func TestInstallShellInterruptAfterReplacementRestoresExistingBinary(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("install.sh is supported only on Linux and macOS")
	}
	asset := unixAssetName(t)
	archive := unixPostSmokeBlockingArchive(t, strings.TrimPrefix(installerTestTag, "v"))
	server := releaseServer(t, installerTestTag, asset, archive, checksumLine(archive, asset))
	homeDirectory := t.TempDir()
	installDir := filepath.Join(homeDirectory, ".local", "bin")
	if err := os.MkdirAll(installDir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(installDir, "seal")
	original := []byte("existing-seal-binary\n")
	if err := os.WriteFile(target, original, 0o711); err != nil {
		t.Fatal(err)
	}
	stateDirectory := t.TempDir()
	readyPath := filepath.Join(stateDirectory, "ready")
	releasePath := filepath.Join(stateDirectory, "release")

	command := exec.Command("sh", repositoryPath(t, "install.sh"), "--version", installerTestTag)
	command.Env = replaceEnvironment(
		installerEnvironment(server.URL, homeDirectory),
		map[string]string{
			"SEAL_TEST_POST_SMOKE_READY":   readyPath,
			"SEAL_TEST_POST_SMOKE_RELEASE": releasePath,
		},
	)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	waitForFile(t, readyPath, 10*time.Second)
	if err := command.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("signal installer: %v", err)
	}
	if err := os.WriteFile(releasePath, []byte("release\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	select {
	case err := <-wait:
		if err == nil {
			t.Fatal("interrupted installer unexpectedly succeeded")
		}
	case <-time.After(10 * time.Second):
		_ = command.Process.Kill()
		t.Fatal("interrupted installer did not exit")
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read restored target: %v", err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("interrupted install changed target: got %q, want %q", got, original)
	}
	assertNoInstallerResidue(t, installDir)
}

func TestInstallShellRequiresExactVersionOption(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh contract is tested on Unix")
	}
	command := exec.Command("sh", repositoryPath(t, "install.sh"), "--ver", installerTestTag)
	if output, err := command.CombinedOutput(); err == nil {
		t.Fatalf("install.sh accepted an abbreviated option:\n%s", output)
	}
}

func TestInstallPowerShellCleanInstall(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("install.ps1 requires native Windows")
	}
	powerShell := windowsPowerShell(t)
	asset := "seal_1.2.3-rc.1_windows_amd64.zip"
	binary := windowsFixtureBinary(t, "1.2.3-rc.1")
	archive := windowsArchive(t, binary)
	server := releaseServer(t, installerTestTag, asset, archive, checksumLine(archive, asset))

	localAppData := t.TempDir()
	command := exec.Command(powerShell, "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", repositoryPath(t, "install.ps1"), "-Version", installerTestTag)
	command.Env = installerEnvironment(server.URL, localAppData)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("install.ps1 failed: %v\n%s", err, output)
	}

	installDir := filepath.Join(localAppData, "Programs", "Seal", "bin")
	target := filepath.Join(installDir, "seal.exe")
	versionOutput, err := exec.Command(target, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("installed binary failed: %v\n%s", err, versionOutput)
	}
	if got, want := strings.TrimSpace(string(versionOutput)), "1.2.3-rc.1"; got != want {
		t.Fatalf("installed --version = %q, want %q", got, want)
	}
	if !strings.Contains(string(output), target) {
		t.Fatalf("installer output %q does not identify absolute target %q", output, target)
	}
	assertNoInstallerResidue(t, installDir)
}

func TestInstallPowerShellRejectsUntrustedReleaseWithoutReplacingExistingBinary(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("install.ps1 requires native Windows")
	}
	powerShell := windowsPowerShell(t)
	asset := "seal_1.2.3-rc.1_windows_amd64.zip"
	matchingArchive := windowsArchive(t, windowsFixtureBinary(t, "1.2.3-rc.1"))
	mismatchedArchive := windowsArchive(t, windowsFixtureBinary(t, "9.9.9"))
	validChecksum := checksumLine(matchingArchive, asset)

	cases := []struct {
		name      string
		archive   []byte
		checksums string
		wantError string
	}{
		{name: "checksum mismatch", archive: matchingArchive, checksums: strings.Repeat("0", 64) + "  " + asset + "\n", wantError: "SHA-256 mismatch for " + asset},
		{name: "missing checksum", archive: matchingArchive, checksums: strings.Repeat("0", 64) + "  another-asset.zip\n", wantError: "checksums.txt must contain exactly one entry for " + asset},
		{name: "duplicate checksum", archive: matchingArchive, checksums: validChecksum + validChecksum, wantError: "checksums.txt must contain exactly one entry for " + asset},
		{name: "malformed checksum", archive: matchingArchive, checksums: "not-a-sha256  " + asset + "\n", wantError: "checksums.txt has an invalid SHA-256 for " + asset},
		{name: "malformed duplicate", archive: matchingArchive, checksums: validChecksum + "not-a-sha256  " + asset + "\n", wantError: "checksums.txt must contain exactly one entry for " + asset},
		{name: "uppercase duplicate", archive: matchingArchive, checksums: validChecksum + strings.Repeat("A", 64) + "  " + asset + "\n", wantError: "checksums.txt must contain exactly one entry for " + asset},
		{name: "single-space duplicate", archive: matchingArchive, checksums: validChecksum + strings.Repeat("0", 64) + " " + asset + "\n", wantError: "checksums.txt must contain exactly one entry for " + asset},
		{name: "version mismatch", archive: mismatchedArchive, checksums: checksumLine(mismatchedArchive, asset), wantError: asset + " does not report requested version"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			server := releaseServer(t, installerTestTag, asset, testCase.archive, testCase.checksums)
			localAppData := t.TempDir()
			installDir := filepath.Join(localAppData, "Programs", "Seal", "bin")
			if err := os.MkdirAll(installDir, 0o700); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(installDir, "seal.exe")
			original := []byte("existing-seal-binary\r\n")
			if err := os.WriteFile(target, original, 0o711); err != nil {
				t.Fatal(err)
			}

			command := exec.Command(powerShell, "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", repositoryPath(t, "install.ps1"), "-Version", installerTestTag)
			command.Env = installerEnvironment(server.URL, localAppData)
			output, err := command.CombinedOutput()
			if err == nil {
				t.Fatalf("install.ps1 unexpectedly succeeded:\n%s", output)
			}
			if !strings.Contains(string(output), testCase.wantError) {
				t.Fatalf("install.ps1 error %q does not contain %q", output, testCase.wantError)
			}

			got, err := os.ReadFile(target)
			if err != nil {
				t.Fatalf("read preserved target: %v", err)
			}
			if !bytes.Equal(got, original) {
				t.Fatalf("existing target changed: got %q, want %q", got, original)
			}
			assertNoInstallerResidue(t, installDir)
		})
	}
}

func TestInstallPowerShellRejectsDriveRelativeLocalAppData(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("install.ps1 requires native Windows")
	}
	powerShell := windowsPowerShell(t)
	for _, localAppData := range []string{`C:relative`, `\relative`} {
		t.Run(localAppData, func(t *testing.T) {
			command := exec.Command(powerShell, "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", repositoryPath(t, "install.ps1"), "-Version", installerTestTag)
			command.Env = installerEnvironment("http://127.0.0.1:1", localAppData)
			output, err := command.CombinedOutput()
			if err == nil {
				t.Fatalf("install.ps1 accepted drive-relative LOCALAPPDATA %q:\n%s", localAppData, output)
			}
			if !strings.Contains(string(output), "must be an absolute path") {
				t.Fatalf("unexpected path rejection: %s", output)
			}
		})
	}
}

func TestInstallPowerShellFinalizerRestoresExistingBinaryAfterSmokeFailure(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("install.ps1 requires native Windows")
	}
	powerShell := windowsPowerShell(t)
	asset := "seal_1.2.3-rc.1_windows_amd64.zip"
	archive := windowsArchive(t, windowsPostSmokeFailingBinary(t, "1.2.3-rc.1"))
	server := releaseServer(t, installerTestTag, asset, archive, checksumLine(archive, asset))
	localAppData := t.TempDir()
	installDir := filepath.Join(localAppData, "Programs", "Seal", "bin")
	if err := os.MkdirAll(installDir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(installDir, "seal.exe")
	original := []byte("existing-seal-binary\r\n")
	if err := os.WriteFile(target, original, 0o711); err != nil {
		t.Fatal(err)
	}

	command := exec.Command(powerShell, "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", repositoryPath(t, "install.ps1"), "-Version", installerTestTag)
	command.Env = replaceEnvironment(
		installerEnvironment(server.URL, localAppData),
		map[string]string{"SEAL_TEST_POST_SMOKE_FAIL": "1"},
	)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("install.ps1 unexpectedly accepted failed installed smoke:\n%s", output)
	}
	if !strings.Contains(string(output), "absolute-path version smoke test") {
		t.Fatalf("install.ps1 failed outside the post-replacement smoke path:\n%s", output)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read restored target: %v", err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("post-smoke failure changed target: got %q, want %q", got, original)
	}
	assertNoInstallerResidue(t, installDir)
}

func unixAssetName(t *testing.T) string {
	t.Helper()
	arch := runtime.GOARCH
	if arch != "amd64" && arch != "arm64" {
		t.Skipf("unsupported test architecture %s", arch)
	}
	return fmt.Sprintf("seal_1.2.3-rc.1_%s_%s.tar.gz", runtime.GOOS, arch)
}

func unixArchive(t *testing.T, version string) []byte {
	t.Helper()
	script := []byte("#!/bin/sh\nif [ \"$#\" -eq 1 ] && [ \"$1\" = \"--version\" ]; then\n  printf '%s\\n' '" + version + "'\n  exit 0\nfi\nexit 2\n")
	var output bytes.Buffer
	gzipWriter := gzip.NewWriter(&output)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(&tar.Header{Name: "seal", Mode: 0o755, Size: int64(len(script)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(script); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func unixPostSmokeBlockingArchive(t *testing.T, version string) []byte {
	t.Helper()
	script := []byte("#!/bin/sh\nif [ \"$0\" = \"${HOME}/.local/bin/seal\" ]; then\n  : >\"${SEAL_TEST_POST_SMOKE_READY}\"\n  while [ ! -e \"${SEAL_TEST_POST_SMOKE_RELEASE}\" ]; do sleep 0.01; done\nfi\nprintf '%s\\n' '" + version + "'\n")
	var output bytes.Buffer
	gzipWriter := gzip.NewWriter(&output)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(&tar.Header{Name: "seal", Mode: 0o755, Size: int64(len(script)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(script); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func windowsFixtureBinary(t *testing.T, version string) []byte {
	t.Helper()
	temporaryDirectory := t.TempDir()
	sourcePath := filepath.Join(temporaryDirectory, "main.go")
	binaryPath := filepath.Join(temporaryDirectory, "seal.exe")
	source := fmt.Sprintf("package main\nimport (\"fmt\"; \"os\")\nfunc main() { if len(os.Args) == 2 && os.Args[1] == \"--version\" { fmt.Println(%q); return }; os.Exit(2) }\n", version)
	if err := os.WriteFile(sourcePath, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("go", "build", "-o", binaryPath, sourcePath)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build Windows fixture binary: %v\n%s", err, output)
	}
	binary, err := os.ReadFile(binaryPath)
	if err != nil {
		t.Fatal(err)
	}
	return binary
}

func windowsPostSmokeFailingBinary(t *testing.T, version string) []byte {
	t.Helper()
	temporaryDirectory := t.TempDir()
	sourcePath := filepath.Join(temporaryDirectory, "main.go")
	binaryPath := filepath.Join(temporaryDirectory, "seal.exe")
	source := fmt.Sprintf(`package main
import ("fmt"; "os"; "path/filepath"; "strings")
func main() {
	if len(os.Args) != 2 || os.Args[1] != "--version" { os.Exit(2) }
	target := filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "Seal", "bin", "seal.exe")
	if os.Getenv("SEAL_TEST_POST_SMOKE_FAIL") != "" && strings.EqualFold(os.Args[0], target) { os.Exit(70) }
	fmt.Println(%q)
}
`, version)
	if err := os.WriteFile(sourcePath, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("go", "build", "-o", binaryPath, sourcePath)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build Windows post-smoke fixture binary: %v\n%s", err, output)
	}
	binary, err := os.ReadFile(binaryPath)
	if err != nil {
		t.Fatal(err)
	}
	return binary
}

func windowsArchive(t *testing.T, binary []byte) []byte {
	t.Helper()
	var output bytes.Buffer
	zipWriter := zip.NewWriter(&output)
	header := &zip.FileHeader{Name: "seal.exe", Method: zip.Deflate}
	header.SetMode(0o755)
	entry, err := zipWriter.CreateHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write(binary); err != nil {
		t.Fatal(err)
	}
	if err := zipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func checksumLine(contents []byte, asset string) string {
	digest := sha256.Sum256(contents)
	return fmt.Sprintf("%x  %s\n", digest, asset)
}

func releaseServer(t *testing.T, tag, asset string, archive []byte, checksums string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/" + tag + "/" + asset:
			_, _ = writer.Write(archive)
		case "/" + tag + "/checksums.txt":
			_, _ = io.WriteString(writer, checksums)
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func installerEnvironment(releaseBase, userLocalRoot string) []string {
	rootKey := "HOME"
	if runtime.GOOS == "windows" {
		rootKey = "LOCALAPPDATA"
	}
	environment := make([]string, 0, len(os.Environ())+2)
	for _, entry := range os.Environ() {
		name, _, found := strings.Cut(entry, "=")
		if found && (strings.EqualFold(name, "SEAL_RELEASE_BASE_URL") || strings.EqualFold(name, rootKey)) {
			continue
		}
		environment = append(environment, entry)
	}
	return append(environment, "SEAL_RELEASE_BASE_URL="+releaseBase, rootKey+"="+userLocalRoot)
}

func replaceEnvironment(environment []string, replacements map[string]string) []string {
	filtered := environment[:0]
	for _, entry := range environment {
		name, _, found := strings.Cut(entry, "=")
		if found {
			if _, replace := replacements[strings.ToUpper(name)]; replace {
				continue
			}
		}
		filtered = append(filtered, entry)
	}
	for name, value := range replacements {
		filtered = append(filtered, name+"="+value)
	}
	return filtered
}

func repositoryPath(t *testing.T, name string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "..", name)
}

func windowsPowerShell(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Fatalf("powershell.exe is required on Windows: %v", err)
	}
	return path
}

func assertNoInstallerResidue(t *testing.T, installDirectory string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(installDirectory, ".seal-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("installer residue remains: %v", matches)
	}
}

func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}
