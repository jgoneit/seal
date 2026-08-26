package releasegate

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	testRCTag     = "v1.2.3-rc.4"
	testStableTag = "v1.2.3"
)

func TestNormalizePluginVersionPreservesEveryOtherByte(t *testing.T) {
	contents := []byte("{\n  \"name\": \"seal\", \"version\" : \"1.2.3-rc.4+codex.test\", \"nested\": {\"version\": \"leave-me\"}\n}\n")
	want := []byte("{\n  \"name\": \"seal\", \"version\" : \"__SEAL_VERSION__\", \"nested\": {\"version\": \"leave-me\"}\n}\n")
	normalized, err := normalizePluginVersion(contents, "1.2.3-rc.4")
	if err != nil {
		t.Fatalf("normalizePluginVersion() error = %v", err)
	}
	if !bytes.Equal(normalized, want) {
		t.Fatalf("normalizePluginVersion() = %q, want %q", normalized, want)
	}

	reformatted := []byte("{\"version\":\"1.2.3-rc.4\",\"name\":\"seal\",\"nested\":{\"version\":\"leave-me\"}}\n")
	reformattedNormalized, err := normalizePluginVersion(reformatted, "1.2.3-rc.4")
	if err != nil {
		t.Fatalf("normalizePluginVersion(reformatted) error = %v", err)
	}
	if bytes.Equal(normalized, reformattedNormalized) {
		t.Fatal("non-version manifest formatting and key-order changes must remain visible to the surface digest")
	}
}

func TestNormalizePluginVersionRejectsDuplicateAndWrongVersions(t *testing.T) {
	tests := []struct {
		name     string
		contents string
		want     string
	}{
		{
			name:     "duplicate",
			contents: `{"version":"1.2.3-rc.4","version":"1.2.3-rc.4"}`,
			want:     "duplicate JSON key",
		},
		{
			name:     "wrong version",
			contents: `{"version":"1.2.3-rc.3"}`,
			want:     "does not match tag version",
		},
		{
			name:     "nested only",
			contents: `{"plugin":{"version":"1.2.3-rc.4"}}`,
			want:     "is required",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := normalizePluginVersion([]byte(test.contents), "1.2.3-rc.4")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("normalizePluginVersion() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestValidateReleaseAllowsAnnotatedRCWithoutReport(t *testing.T) {
	fixture := newReleaseFixture(t)
	result, err := ValidateRelease(testContext(t), fixture.root, testRCTag, "release/acceptance")
	if err != nil {
		t.Fatalf("ValidateRelease(RC) error = %v", err)
	}
	if !result.RC || result.RCTag != testRCTag || result.ReportPath != "" {
		t.Fatalf("ValidateRelease(RC) = %#v", result)
	}
}

func TestValidateReleaseRejectsRCVersionMismatch(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, releaseFixture)
		want   string
	}{
		{
			name: "Core version",
			mutate: func(t *testing.T, fixture releaseFixture) {
				writeTestFile(t, fixture.root, ".codex-plugin/plugin.json", "{\n  \"name\": \"seal\",\n  \"version\": \"1.2.3-rc.5+codex.test\",\n  \"description\": \"test\"\n}\n")
			},
			want: "version constant \"1.2.3-rc.4\" does not match tag version \"1.2.3-rc.5\"",
		},
		{
			name: "Plugin version",
			mutate: func(t *testing.T, fixture releaseFixture) {
				writeTestFile(t, fixture.root, "cmd/seal/main.go", "package main\n\nconst version = \"1.2.3-rc.5\"\n\nfunc main() {}\n")
			},
			want: "plugin version \"1.2.3-rc.4+codex.test\" does not match tag version \"1.2.3-rc.5\"",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newReleaseFixture(t)
			test.mutate(t, fixture)
			fixture.git(t, "add", "--all")
			fixture.git(t, "commit", "-q", "-m", "mismatched RC version")
			fixture.git(t, "tag", "-a", "v1.2.3-rc.5", "-m", "mismatched candidate")
			_, err := ValidateRelease(testContext(t), fixture.root, "v1.2.3-rc.5", "release/acceptance")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateRelease() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestValidateReleaseRequiresCanonicalVersionFiles(t *testing.T) {
	tests := []struct {
		name       string
		remove     string
		updatePeer func(*testing.T, releaseFixture)
	}{
		{
			name:   "Core",
			remove: "cmd/seal/main.go",
			updatePeer: func(t *testing.T, fixture releaseFixture) {
				writeTestFile(t, fixture.root, ".codex-plugin/plugin.json", "{\n  \"name\": \"seal\",\n  \"version\": \"1.2.3-rc.5+codex.test\",\n  \"description\": \"test\"\n}\n")
			},
		},
		{
			name:   "Plugin",
			remove: ".codex-plugin/plugin.json",
			updatePeer: func(t *testing.T, fixture releaseFixture) {
				writeTestFile(t, fixture.root, "cmd/seal/main.go", "package main\n\nconst version = \"1.2.3-rc.5\"\n\nfunc main() {}\n")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newReleaseFixture(t)
			if err := os.Remove(filepath.Join(fixture.root, filepath.FromSlash(test.remove))); err != nil {
				t.Fatal(err)
			}
			test.updatePeer(t, fixture)
			fixture.git(t, "add", "--all")
			fixture.git(t, "commit", "-q", "-m", "remove canonical version file")
			fixture.git(t, "tag", "-a", "v1.2.3-rc.5", "-m", "incomplete candidate")

			_, err := ValidateRelease(testContext(t), fixture.root, "v1.2.3-rc.5", "release/acceptance")
			if err == nil || !strings.Contains(err.Error(), "acceptance surface is missing "+test.remove) {
				t.Fatalf("ValidateRelease() error = %v, want missing canonical file %s", err, test.remove)
			}
		})
	}
}

func TestValidateReleaseRejectsLightweightRCAndStableTags(t *testing.T) {
	t.Run("RC", func(t *testing.T) {
		fixture := newReleaseFixture(t)
		fixture.git(t, "tag", "v1.2.3-rc.5")
		_, err := ValidateRelease(testContext(t), fixture.root, "v1.2.3-rc.5", "release/acceptance")
		if err == nil || !strings.Contains(err.Error(), "must be annotated") {
			t.Fatalf("ValidateRelease(lightweight RC) error = %v", err)
		}
	})

	t.Run("stable", func(t *testing.T) {
		fixture := newReleaseFixture(t)
		fixture.writeCompleteReport(t, fixture.tagTime.Add(time.Second), minimumTaskCount)
		fixture.prepareStable(t, false)
		fixture.git(t, "tag", "-d", testStableTag)
		fixture.git(t, "tag", testStableTag)
		_, err := ValidateRelease(testContext(t), fixture.root, testStableTag, "release/acceptance")
		if err == nil || !strings.Contains(err.Error(), "must be annotated") {
			t.Fatalf("ValidateRelease(lightweight stable) error = %v", err)
		}
	})
}

func TestValidateStableReleaseAllowsVersionOnlyAndExcludedDocumentChanges(t *testing.T) {
	fixture := newReleaseFixture(t)
	fixture.writeCompleteReport(t, fixture.tagTime.Add(time.Second), minimumTaskCount+1)
	fixture.prepareStable(t, false)

	result, err := ValidateRelease(testContext(t), fixture.root, testStableTag, "release/acceptance")
	if err != nil {
		t.Fatalf("ValidateRelease(stable) error = %v", err)
	}
	if result.RC || result.RCTag != testRCTag || filepath.Base(result.ReportPath) != testRCTag+".json" {
		t.Fatalf("ValidateRelease(stable) = %#v", result)
	}
}

func TestValidateStableReleaseReadsReportFromStableTagTree(t *testing.T) {
	fixture := newReleaseFixture(t)
	fixture.writeCompleteReport(t, fixture.tagTime.Add(time.Second), minimumTaskCount)
	fixture.prepareStable(t, false)

	writeTestFile(t, fixture.root, "release/acceptance/"+testRCTag+".json", "{}\n")
	if _, err := ValidateRelease(testContext(t), fixture.root, testStableTag, "release/acceptance"); err != nil {
		t.Fatalf("ValidateRelease() used modified working-tree report: %v", err)
	}
}

func TestValidateStableReleaseRequiresTaggedReportEvenWhenWorktreeHasOne(t *testing.T) {
	for _, writeUntracked := range []bool{false, true} {
		name := "absent"
		if writeUntracked {
			name = "untracked"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newReleaseFixture(t)
			fixture.prepareStable(t, false)
			if writeUntracked {
				fixture.writeCompleteReport(t, fixture.tagTime.Add(time.Second), minimumTaskCount)
			}
			_, err := ValidateRelease(testContext(t), fixture.root, testStableTag, "release/acceptance")
			if err == nil || !strings.Contains(err.Error(), "is absent from the stable tag") {
				t.Fatalf("ValidateRelease() error = %v, want tagged-report absence", err)
			}
		})
	}
}

func TestValidateStableReleaseRejectsSymlinkedReportInStableTag(t *testing.T) {
	fixture := newReleaseFixture(t)
	link := filepath.Join(fixture.root, "release", "acceptance", testRCTag+".json")
	if err := os.Symlink("README.md", link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	fixture.prepareStable(t, false)

	_, err := ValidateRelease(testContext(t), fixture.root, testStableTag, "release/acceptance")
	if err == nil || !strings.Contains(err.Error(), "must be a regular blob") {
		t.Fatalf("ValidateRelease() error = %v, want tagged-symlink rejection", err)
	}
}

func TestValidateStableReleaseRejectsOversizedTaggedReportBeforeParsing(t *testing.T) {
	fixture := newReleaseFixture(t)
	path := filepath.Join(fixture.root, "release", "acceptance", testRCTag+".json")
	if err := os.WriteFile(path, make([]byte, maximumReportBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.prepareStable(t, false)

	_, err := ValidateRelease(testContext(t), fixture.root, testStableTag, "release/acceptance")
	if err == nil || !strings.Contains(err.Error(), "1048576-byte safety limit") {
		t.Fatalf("ValidateRelease() error = %v, want tagged-report size rejection", err)
	}
}

func TestValidateStableReleaseRejectsWindowAtRCTagTime(t *testing.T) {
	fixture := newReleaseFixture(t)
	fixture.writeCompleteReport(t, fixture.tagTime, minimumTaskCount)
	fixture.prepareStable(t, false)

	_, err := ValidateRelease(testContext(t), fixture.root, testStableTag, "release/acceptance")
	if err == nil || !strings.Contains(err.Error(), "must begin after candidate RC") {
		t.Fatalf("ValidateRelease() error = %v, want strict post-tag window failure", err)
	}
}

func TestValidateStableReleaseRejectsWindowAfterStableTagTime(t *testing.T) {
	fixture := newReleaseFixture(t)
	fixture.writeCompleteReport(t, fixture.tagTime.Add(2*time.Hour), minimumTaskCount)
	fixture.prepareStable(t, false)

	_, err := ValidateRelease(testContext(t), fixture.root, testStableTag, "release/acceptance")
	if err == nil || !strings.Contains(err.Error(), "must end no later than stable tag") {
		t.Fatalf("ValidateRelease() error = %v, want post-stable window failure", err)
	}
}

func TestValidateStableReleaseRejectsCandidateCommitMismatch(t *testing.T) {
	fixture := newReleaseFixture(t)
	fixture.writeCompleteReport(t, fixture.tagTime.Add(time.Second), minimumTaskCount)
	path := filepath.Join(fixture.root, "release", "acceptance", testRCTag+".json")
	document := validReportDocument(minimumTaskCount)
	configureReport(document, strings.Repeat("f", 40), fixture.tagTime.Add(time.Second))
	writeReportDocument(t, path, document)
	fixture.prepareStable(t, false)

	_, err := ValidateRelease(testContext(t), fixture.root, testStableTag, "release/acceptance")
	if err == nil || !strings.Contains(err.Error(), "does not match v1.2.3-rc.4 peeled commit") {
		t.Fatalf("ValidateRelease() error = %v, want candidate-commit failure", err)
	}
}

func TestValidateStableReleaseRejectsEveryAcceptanceSurfaceMutation(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*testing.T, releaseFixture)
		afterAdd func(*testing.T, releaseFixture)
	}{
		{
			name: "addition",
			mutate: func(t *testing.T, fixture releaseFixture) {
				writeTestFile(t, fixture.root, "internal/added.txt", "added after acceptance\n")
			},
		},
		{
			name: "deletion",
			mutate: func(t *testing.T, fixture releaseFixture) {
				if err := os.Remove(filepath.Join(fixture.root, "internal", "behavior.txt")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "mode",
			afterAdd: func(t *testing.T, fixture releaseFixture) {
				fixture.git(t, "update-index", "--chmod=+x", "internal/behavior.txt")
			},
		},
		{
			name: "content",
			mutate: func(t *testing.T, fixture releaseFixture) {
				writeTestFile(t, fixture.root, "internal/behavior.txt", "changed after acceptance\n")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newReleaseFixture(t)
			fixture.writeCompleteReport(t, fixture.tagTime.Add(time.Second), minimumTaskCount)
			if test.mutate != nil {
				test.mutate(t, fixture)
			}
			fixture.prepareStableWithIndexMutation(t, false, test.afterAdd)

			_, err := ValidateRelease(testContext(t), fixture.root, testStableTag, "release/acceptance")
			if err == nil || !strings.Contains(err.Error(), "differs from candidate surface") {
				t.Fatalf("ValidateRelease() error = %v, want acceptance-surface failure", err)
			}
		})
	}
}

func TestValidateStableReleaseTreatsManifestNonVersionBytesAsSurface(t *testing.T) {
	fixture := newReleaseFixture(t)
	fixture.writeCompleteReport(t, fixture.tagTime.Add(time.Second), minimumTaskCount)
	fixture.prepareStable(t, true)

	_, err := ValidateRelease(testContext(t), fixture.root, testStableTag, "release/acceptance")
	if err == nil || !strings.Contains(err.Error(), "differs from candidate surface") {
		t.Fatalf("ValidateRelease() error = %v, want manifest-byte surface failure", err)
	}
}

func TestValidateStableReleaseRequiresHighestSameBaseRCReport(t *testing.T) {
	fixture := newReleaseFixture(t)
	fixture.writeCompleteReport(t, fixture.tagTime.Add(time.Second), minimumTaskCount)
	fixture.prepareStable(t, false)
	fixture.git(t, "tag", "-a", "v1.2.3-rc.5", "-m", "newer candidate")

	_, err := ValidateRelease(testContext(t), fixture.root, testStableTag, "release/acceptance")
	if err == nil || !strings.Contains(err.Error(), "v1.2.3-rc.5.json") {
		t.Fatalf("ValidateRelease() error = %v, want highest-RC report failure", err)
	}
}

func TestValidateStableReleaseRejectsHigherSameBaseRCOnNonAncestorBranch(t *testing.T) {
	fixture := newReleaseFixture(t)

	const higherRC = "v1.2.3-rc.5"
	fixture.git(t, "switch", "--quiet", "--detach", fixture.commit)
	writeTestFile(t, fixture.root, "cmd/seal/main.go", "package main\n\nconst version = \"1.2.3-rc.5\"\n\nfunc main() {}\n")
	writeTestFile(t, fixture.root, ".codex-plugin/plugin.json", "{\n  \"name\": \"seal\",\n  \"version\": \"1.2.3-rc.5+codex.test\",\n  \"description\": \"test\"\n}\n")
	writeTestFile(t, fixture.root, "internal/nonancestor.txt", "newer divergent candidate\n")
	fixture.git(t, "add", "--all")
	fixture.git(t, "commit", "-q", "-m", "divergent candidate")
	fixture.git(t, "tag", "-a", higherRC, "-m", "divergent candidate tag")
	higherCommit := fixture.git(t, "rev-parse", higherRC+"^{commit}")
	higherTagTime := fixture.parsedTagTime(t, higherRC)
	fixture.git(t, "switch", "--quiet", "--detach", fixture.commit)
	document := validReportDocument(minimumTaskCount)
	candidate := document["candidate"].(map[string]any)
	candidate["rc_tag"] = higherRC
	candidate["rc_commit"] = higherCommit
	configureReport(document, higherCommit, higherTagTime.Add(time.Second))
	writeReportDocument(t, filepath.Join(fixture.root, "release", "acceptance", higherRC+".json"), document)
	fixture.prepareStable(t, false)

	_, err := ValidateRelease(testContext(t), fixture.root, testStableTag, "release/acceptance")
	if err == nil || !strings.Contains(err.Error(), "is not an ancestor of stable tag") {
		t.Fatalf("ValidateRelease() error = %v, want highest-RC ancestry failure", err)
	}
}

type releaseFixture struct {
	root    string
	commit  string
	tagTime time.Time
}

func newReleaseFixture(t *testing.T) releaseFixture {
	t.Helper()
	root := t.TempDir()
	fixture := releaseFixture{root: root}
	fixture.git(t, "init", "-q")
	fixture.git(t, "config", "user.name", "Seal Release Gate Test")
	fixture.git(t, "config", "user.email", "seal-release-gate@example.invalid")
	fixture.git(t, "config", "core.autocrlf", "false")
	writeTestFile(t, root, "cmd/seal/main.go", "package main\n\nconst version = \"1.2.3-rc.4\"\n\nfunc main() {}\n")
	writeTestFile(t, root, ".codex-plugin/plugin.json", "{\n  \"name\": \"seal\",\n  \"version\": \"1.2.3-rc.4+codex.test\",\n  \"description\": \"test\"\n}\n")
	writeTestFile(t, root, "release/acceptance/report-v1.schema.json", "{}\n")
	writeTestFile(t, root, "release/acceptance/README.md", "acceptance report instructions\n")
	writeTestFile(t, root, "skills/seal/SKILL.md", "candidate Skill behavior\n")
	writeTestFile(t, root, "internal/behavior.txt", "candidate behavior\n")
	writeTestFile(t, root, "README.md", "candidate overview\n")
	fixture.git(t, "add", "--all")
	fixture.git(t, "commit", "-q", "-m", "candidate")
	fixture.git(t, "tag", "-a", testRCTag, "-m", "candidate tag")
	fixture.commit = fixture.git(t, "rev-parse", testRCTag+"^{commit}")
	fixture.tagTime = fixture.parsedTagTime(t, testRCTag)
	return fixture
}

func (fixture releaseFixture) writeCompleteReport(t *testing.T, started time.Time, taskCount int) {
	t.Helper()
	document := validReportDocument(taskCount)
	configureReport(document, fixture.commit, started)
	writeReportDocument(t, filepath.Join(fixture.root, "release", "acceptance", testRCTag+".json"), document)
}

func configureReport(document map[string]any, commit string, started time.Time) {
	document["candidate"].(map[string]any)["rc_commit"] = commit
	window := document["window"].(map[string]any)
	window["started_at"] = started.UTC().Format(reportTimeLayout)
	window["ended_at"] = started.Add(2 * time.Second).UTC().Format(reportTimeLayout)
	for _, rawTask := range document["tasks"].([]any) {
		rawTask.(map[string]any)["observed_at"] = started.Add(time.Second).UTC().Format(reportTimeLayout)
	}
}

func (fixture releaseFixture) prepareStable(t *testing.T, reformatManifest bool) {
	fixture.prepareStableWithIndexMutation(t, reformatManifest, nil)
}

func (fixture releaseFixture) prepareStableWithIndexMutation(
	t *testing.T,
	reformatManifest bool,
	afterAdd func(*testing.T, releaseFixture),
) {
	t.Helper()
	writeTestFile(t, fixture.root, "cmd/seal/main.go", "package main\n\nconst version = \"1.2.3\"\n\nfunc main() {}\n")
	manifest := "{\n  \"name\": \"seal\",\n  \"version\": \"1.2.3+codex.test\",\n  \"description\": \"test\"\n}\n"
	if reformatManifest {
		manifest = "{\"description\":\"test\",\"version\":\"1.2.3+codex.test\",\"name\":\"seal\"}\n"
	}
	writeTestFile(t, fixture.root, ".codex-plugin/plugin.json", manifest)
	writeTestFile(t, fixture.root, "README.md", "stable overview may change\n")
	writeTestFile(t, fixture.root, ".gitignore", "local-only\n")
	writeTestFile(t, fixture.root, "RELEASING.md", "stable operational instructions\n")
	writeTestFile(t, fixture.root, "TRUST_MODEL.md", "stable operational trust notes\n")
	fixture.git(t, "add", "--all")
	if afterAdd != nil {
		afterAdd(t, fixture)
	}
	fixture.git(t, "commit", "-q", "-m", "stable release")
	fixture.gitAt(t, fixture.tagTime.Add(time.Hour), "tag", "-a", testStableTag, "-m", "stable tag")
}

func (fixture releaseFixture) parsedTagTime(t *testing.T, tag string) time.Time {
	t.Helper()
	value := fixture.git(t, "for-each-ref", "--format=%(taggerdate:unix)", "refs/tags/"+tag)
	seconds, err := time.ParseDuration(value + "s")
	if err != nil {
		t.Fatalf("parse tag time %q: %v", value, err)
	}
	return time.Unix(int64(seconds/time.Second), 0).UTC()
}

func (fixture releaseFixture) git(t *testing.T, arguments ...string) string {
	t.Helper()
	return fixture.gitWithEnvironment(t, nil, arguments...)
}

func (fixture releaseFixture) gitAt(t *testing.T, when time.Time, arguments ...string) string {
	t.Helper()
	return fixture.gitWithEnvironment(
		t,
		[]string{"GIT_COMMITTER_DATE=" + when.UTC().Format(time.RFC3339)},
		arguments...,
	)
}

func (fixture releaseFixture) gitWithEnvironment(t *testing.T, environment []string, arguments ...string) string {
	t.Helper()
	commandArguments := append([]string{"-C", fixture.root}, arguments...)
	command := exec.Command("git", commandArguments...)
	command.Env = append(os.Environ(), environment...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func writeTestFile(t *testing.T, root, relative, contents string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}
