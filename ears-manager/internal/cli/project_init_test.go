package cli

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/redhat-et/protobot/ears-manager/internal/specvalidation"
)

var initArgs = []string{
	"--output", "json", "project", "init",
	"--id", "fixture",
	"--name", "CLI fixture",
	"--canonical-remote", "https://example.invalid/fixture.git",
	"--review-mode", "single-player",
}

func initArgsWith(extra ...string) []string {
	return append(append([]string(nil), initArgs...), extra...)
}

// newUninitializedRepository creates a Git working tree with one commit on
// main that holds the default Vision and Architecture files.
func newUninitializedRepository(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, root, "docs/vision.md", "# Existing Fixture Vision\r\n")
	writeTestFile(t, root, "docs/architecture.md", "# Fixture Architecture\n")
	git(t, root, "init", "-b", "main")
	git(t, root, "config", "user.email", "test@example.invalid")
	git(t, root, "config", "user.name", "Test User")
	git(t, root, "config", "core.autocrlf", "false")
	git(t, root, "add", ".")
	git(t, root, "commit", "-m", "existing repository")
	return root
}

func writeTestFile(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readTestFile(t *testing.T, root, relative string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func gitOutput(t *testing.T, root string, args ...string) string {
	t.Helper()
	output, err := exec.Command("git", append([]string{"-C", root}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %v failed: %v", args, err)
	}
	return strings.TrimSpace(string(output))
}

func assertNotInitialized(t *testing.T, root string) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(root, ".protobot")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf(".protobot exists after a failed initialization: %v", err)
	}
}

func assertFailure(t *testing.T, code int, stdout, stderr string, wantExit int, wantCode string) {
	t.Helper()
	if code != wantExit || stderr != "" || jsonString(t, stdout, "error", "code") != wantCode {
		t.Fatalf("result = exit %d stdout %s stderr %s; want exit %d code %s", code, stdout, stderr, wantExit, wantCode)
	}
	if jsonString(t, stdout, "error", "mutation") != "none" {
		t.Fatalf("failure mutation was not none: %s", stdout)
	}
}

func TestProjectInitCreatesProjectThatPassesCheck(t *testing.T) {
	root := newUninitializedRepository(t)
	t.Chdir(root)
	head := gitOutput(t, root, "rev-parse", "HEAD")

	code, stdout, stderr := runCLI(nil, initArgs...)
	assertSuccess(t, code, stdout, stderr)
	for path, want := range map[string]string{
		"id":               "fixture",
		"name":             "CLI fixture",
		"canonical_remote": "https://example.invalid/fixture.git",
		"default_branch":   "main",
		"review_mode":      "single-player",
		"branch_prefix":    "cs/",
	} {
		if got := jsonString(t, stdout, "data", "project", path); got != want {
			t.Fatalf("data.project.%s = %q, want %q", path, got, want)
		}
	}
	if !strings.Contains(stdout, `"schema_versions":{"specification":1,"project":1}`) {
		t.Fatalf("schema versions missing or out of contract order: %s", stdout)
	}
	if !strings.Contains(stdout, `"stores":{"requirements":".protobot/requirements","interfaces":".protobot/interfaces","change_sets":".protobot/change-sets"}`) {
		t.Fatalf("default stores missing: %s", stdout)
	}
	emptyDigest, err := specvalidation.CanonicalTextDigest(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := jsonString(t, stdout, "data", "project", "store_digests", "requirements"); got != emptyDigest {
		t.Fatalf("requirement store digest = %q, want %q", got, emptyDigest)
	}
	if !strings.Contains(stdout, `"registered_paths":["docs/architecture.md","docs/vision.md"]`) {
		t.Fatalf("registered paths missing: %s", stdout)
	}
	if !strings.Contains(stdout, `"mutation":{"applied":true,"paths":[".protobot/project.yaml",".protobot/projection.yaml"]}`) {
		t.Fatalf("mutation paths = %s", stdout)
	}

	// Existing files are registered in place: bytes, including CRLF line
	// endings, are untouched, and the digest uses the canonical LF form.
	if got := readTestFile(t, root, "docs/vision.md"); got != "# Existing Fixture Vision\r\n" {
		t.Fatalf("initialization rewrote the Vision: %q", got)
	}
	projectYAML := readTestFile(t, root, ".protobot/project.yaml")
	lfDigest, err := specvalidation.CanonicalTextDigest([]byte("# Existing Fixture Vision\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(projectYAML, lfDigest) || !strings.Contains(projectYAML, "owner: user") {
		t.Fatalf("project.yaml registry = %s", projectYAML)
	}
	projection := specvalidation.ParseProjection([]byte(readTestFile(t, root, ".protobot/projection.yaml")))
	if len(projection.Diagnostics) > 0 || projection.Classes["docs/vision.md"] != "shared" || projection.Classes["docs/architecture.md"] != "shared" || len(projection.Classes) != 2 {
		t.Fatalf("projection = %#v", projection)
	}

	// Initialization neither commits nor touches unrelated history.
	if got := gitOutput(t, root, "rev-parse", "HEAD"); got != head {
		t.Fatalf("HEAD moved from %s to %s", head, got)
	}
	if status := gitOutput(t, root, "status", "--porcelain", "--untracked-files=all"); status != "?? .protobot/project.yaml\n?? .protobot/projection.yaml" {
		t.Fatalf("git status after init = %q", status)
	}

	code, stdout, stderr = runCLI(nil, "--output", "json", "check")
	assertSuccess(t, code, stdout, stderr)
	if !jsonBool(t, stdout, "data", "valid") || !strings.Contains(stdout, `".protobot/projection.yaml"`) {
		t.Fatalf("check after init = %s", stdout)
	}
}

func TestProjectInitHonorsSelectedPathsAndDefaults(t *testing.T) {
	root := newUninitializedRepository(t)
	writeTestFile(t, root, "spec/product.md", "# Product\n")
	writeTestFile(t, root, "spec/system.md", "# System\n")
	git(t, root, "add", ".")
	git(t, root, "commit", "-m", "custom layout")
	git(t, root, "branch", "trunk")
	if err := os.MkdirAll(filepath.Join(root, "nested", "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(filepath.Join(root, "nested", "dir"))

	code, stdout, stderr := runCLI(nil, initArgsWith(
		"--review-mode", "multi-player",
		"--default-branch", "trunk",
		"--branch-prefix", "spec/",
		"--vision", "spec/product.md",
		"--architecture", "spec/system.md",
		"--canonical-remote", "git@example.invalid:org/fixture.git",
	)...)
	assertSuccess(t, code, stdout, stderr)
	if jsonString(t, stdout, "data", "project", "default_branch") != "trunk" || jsonString(t, stdout, "data", "project", "branch_prefix") != "spec/" || jsonString(t, stdout, "data", "project", "review_mode") != "multi-player" {
		t.Fatalf("selected repository settings were not applied: %s", stdout)
	}
	if !strings.Contains(stdout, `"registered_paths":["spec/product.md","spec/system.md"]`) {
		t.Fatalf("selected paths were not registered: %s", stdout)
	}
	if _, err := os.Stat(filepath.Join(root, ".protobot", "project.yaml")); err != nil {
		t.Fatalf("project.yaml was not written at the working-tree root: %v", err)
	}
	code, stdout, stderr = runCLI(nil, "--output", "json", "check")
	assertSuccess(t, code, stdout, stderr)
}

func TestProjectInitThenArtifactPutClassifiesNewPath(t *testing.T) {
	root := newUninitializedRepository(t)
	t.Chdir(root)
	code, stdout, stderr := runCLI(nil, initArgs...)
	assertSuccess(t, code, stdout, stderr)
	git(t, root, "add", ".")
	git(t, root, "commit", "-m", "initialize")

	// A person adds a policy entry; ears-manager must preserve it.
	projectionPath := filepath.Join(root, ".protobot", "projection.yaml")
	file, err := os.OpenFile(projectionPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("  - path: README.md\n    class: implementation\n"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr = runCLI(nil, "--output", "json", "change-set", "create", "--intent", "Add CLI prose", "--implementation-required", "true", "--created", "2026-09-28T12:00:00Z")
	assertSuccess(t, code, stdout, stderr)
	changeSetID := jsonString(t, stdout, "data", "change_set", "id")

	code, stdout, stderr = runCLI([]byte("# CLI\n"), "--output", "json", "artifact", "put", "--change-set", changeSetID, "--id", "cli-prose", "--kind", "interface-prose", "--path", "docs/interfaces/cli.md", "--owner", "user", "--content-stdin")
	assertSuccess(t, code, stdout, stderr)
	if !strings.Contains(stdout, `".protobot/projection.yaml"`) {
		t.Fatalf("artifact put did not report the projection write: %s", stdout)
	}
	projection := specvalidation.ParseProjection([]byte(readTestFile(t, root, ".protobot/projection.yaml")))
	if projection.Classes["docs/interfaces/cli.md"] != "shared" || projection.Classes["README.md"] != "implementation" {
		t.Fatalf("projection after artifact put = %#v", projection.Classes)
	}

	// Revising an already classified path leaves the manifest untouched.
	before := readTestFile(t, root, ".protobot/projection.yaml")
	code, stdout, stderr = runCLI([]byte("# CLI v2\n"), "--output", "json", "artifact", "put", "--change-set", changeSetID, "--id", "cli-prose", "--kind", "interface-prose", "--path", "docs/interfaces/cli.md", "--owner", "user", "--content-stdin")
	assertSuccess(t, code, stdout, stderr)
	if strings.Contains(stdout, `".protobot/projection.yaml"`) || readTestFile(t, root, ".protobot/projection.yaml") != before {
		t.Fatalf("revision rewrote the projection manifest: %s", stdout)
	}

	code, stdout, stderr = runCLI(nil, "--output", "json", "check")
	assertSuccess(t, code, stdout, stderr)
}

func TestProjectInitRejectsExistingControlNamespace(t *testing.T) {
	root := newUninitializedRepository(t)
	t.Chdir(root)
	code, stdout, stderr := runCLI(nil, initArgs...)
	assertSuccess(t, code, stdout, stderr)
	before := readTestFile(t, root, ".protobot/project.yaml")

	code, stdout, stderr = runCLI(nil, initArgsWith("--name", "Other")...)
	assertFailure(t, code, stdout, stderr, 5, "project.already_initialized")
	if readTestFile(t, root, ".protobot/project.yaml") != before {
		t.Fatal("re-initialization changed project.yaml")
	}

	other := newUninitializedRepository(t)
	if err := os.WriteFile(filepath.Join(other, ".protobot"), []byte("not a directory\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(other)
	code, stdout, stderr = runCLI(nil, initArgs...)
	assertFailure(t, code, stdout, stderr, 5, "project.already_initialized")
}

func TestProjectInitRejectsEmptyRepository(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, root, "docs/vision.md", "# Vision\n")
	writeTestFile(t, root, "docs/architecture.md", "# Architecture\n")
	git(t, root, "init", "-b", "main")
	t.Chdir(root)

	code, stdout, stderr := runCLI(nil, initArgs...)
	assertFailure(t, code, stdout, stderr, 3, "project.invalid_configuration")
	assertNotInitialized(t, root)
}

func TestProjectInitRejectsOutsideGitWorkingTree(t *testing.T) {
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(t.TempDir()))
	root := t.TempDir()
	t.Chdir(root)
	code, stdout, stderr := runCLI(nil, initArgs...)
	assertFailure(t, code, stdout, stderr, 3, "project.not_git_root")
	assertNotInitialized(t, root)
}

func TestProjectInitRejectsMisplacedProjectFile(t *testing.T) {
	root := newUninitializedRepository(t)
	writeTestFile(t, root, "service/.protobot/project.yaml", "project:\n  id: misplaced\n")

	t.Chdir(filepath.Join(root, "service"))
	code, stdout, stderr := runCLI(nil, initArgs...)
	assertFailure(t, code, stdout, stderr, 3, "project.not_git_root")
	if !strings.Contains(stdout, `"path":"service/.protobot/project.yaml"`) || strings.Contains(stdout, root) {
		t.Fatalf("misplaced diagnostic must name the relative location only: %s", stdout)
	}
	assertNotInitialized(t, root)

	t.Chdir(root)
	code, stdout, stderr = runCLI(nil, initArgs...)
	assertFailure(t, code, stdout, stderr, 3, "project.not_git_root")
	assertNotInitialized(t, root)
	if readTestFile(t, root, "service/.protobot/project.yaml") != "project:\n  id: misplaced\n" {
		t.Fatal("initialization moved or rewrote the misplaced project file")
	}
}

func TestProjectInitRejectsUnsafeOrMissingPaths(t *testing.T) {
	root := newUninitializedRepository(t)
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("# Outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	symlinks := true
	if err := os.Symlink(outside, filepath.Join(root, "docs", "linked.md")); err != nil {
		symlinks = false
	}
	writeTestFile(t, root, ".github/vision.md", "# Workflow\n")
	writeTestFile(t, root, "AGENTS.md", "# Agents\n")
	t.Chdir(root)

	cases := []struct {
		name    string
		args    []string
		echo    bool
		skipped bool
	}{
		{name: "parent escape", args: []string{"--vision", "../outside.md"}},
		{name: "absolute", args: []string{"--vision", outside}},
		{name: "workflow path", args: []string{"--vision", ".github/vision.md"}},
		{name: "agent instructions", args: []string{"--architecture", "AGENTS.md"}},
		{name: "control namespace", args: []string{"--vision", ".protobot/vision.md"}},
		{name: "symlink escape", args: []string{"--vision", "docs/linked.md"}, skipped: !symlinks},
		{name: "missing", args: []string{"--vision", "docs/missing.md"}, echo: true},
		{name: "directory", args: []string{"--architecture", "docs"}, echo: true},
		{name: "same path twice", args: []string{"--vision", "docs/architecture.md"}, echo: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.skipped {
				t.Skip("symlinks unavailable")
			}
			code, stdout, stderr := runCLI(nil, initArgsWith(tc.args...)...)
			assertFailure(t, code, stdout, stderr, 4, "project.invalid_path")
			if !tc.echo && strings.Contains(stdout, tc.args[1]) {
				t.Fatalf("diagnostic echoed an unsafe path: %s", stdout)
			}
			if strings.Contains(stdout, root) {
				t.Fatalf("diagnostic included an absolute path: %s", stdout)
			}
			assertNotInitialized(t, root)
		})
	}
}

func TestProjectInitRejectsRemoteCredentials(t *testing.T) {
	root := newUninitializedRepository(t)
	t.Chdir(root)
	userinfo := "fixture" + "-review-only-value"
	for _, remote := range []string{
		"https://" + "user:" + userinfo + "@example.invalid/fixture.git",
		"https://" + userinfo + "@example.invalid/fixture.git",
		"ssh://" + userinfo + "@example.invalid/fixture.git",
		userinfo + "@example.invalid:fixture.git",
	} {
		code, stdout, stderr := runCLI(nil, initArgsWith("--canonical-remote", remote)...)
		assertFailure(t, code, stdout, stderr, 4, "project.remote_credentials")
		if strings.Contains(stdout, userinfo) {
			t.Fatalf("credential-bearing remote was echoed: %s", stdout)
		}
		code, stdout, stderr = runCLI(nil, "project", "init", "--id", "fixture", "--name", "CLI fixture", "--canonical-remote", remote, "--review-mode", "single-player")
		if code != 4 || stdout != "" || !strings.Contains(stderr, "error[project.remote_credentials]") || strings.Contains(stderr, userinfo) {
			t.Fatalf("human credential failure = exit %d stdout %q stderr %q", code, stdout, stderr)
		}
		assertNotInitialized(t, root)
	}
}

func TestProjectInitRejectsInvalidConfiguration(t *testing.T) {
	root := newUninitializedRepository(t)
	t.Chdir(root)
	cases := []struct {
		name string
		args []string
		exit int
	}{
		{name: "reserved prefix", args: []string{"--branch-prefix", "wi/"}, exit: 4},
		{name: "prefix below reserved namespace", args: []string{"--branch-prefix", "wi/cs/"}, exit: 4},
		{name: "prefix without slash", args: []string{"--branch-prefix", "cs"}, exit: 4},
		{name: "unknown review mode", args: []string{"--review-mode", "solo"}, exit: 4},
		{name: "unsupported remote scheme", args: []string{"--canonical-remote", "http://example.invalid/fixture.git"}, exit: 4},
		{name: "invalid default branch", args: []string{"--default-branch", "bad..branch"}, exit: 4},
		{name: "unresolved default branch", args: []string{"--default-branch", "trunk"}, exit: 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := runCLI(nil, initArgsWith(tc.args...)...)
			assertFailure(t, code, stdout, stderr, tc.exit, "project.invalid_configuration")
			assertNotInitialized(t, root)
		})
	}

	code, stdout, stderr := runCLI(nil, "--output", "json", "project", "init", "--id", "fixture", "--name", "CLI fixture", "--review-mode", "single-player")
	assertFailure(t, code, stdout, stderr, 2, "usage.invalid_request")
	code, stdout, stderr = runCLI(nil, initArgsWith("--content-stdin")...)
	assertFailure(t, code, stdout, stderr, 2, "usage.invalid_request")
	assertNotInitialized(t, root)
}

func TestProjectInitRejectsInvalidArtifactContent(t *testing.T) {
	root := newUninitializedRepository(t)
	writeTestFile(t, root, "docs/vision.md", "\xef\xbb\xbf# BOM Vision\n")
	t.Chdir(root)
	code, stdout, stderr := runCLI(nil, initArgs...)
	assertFailure(t, code, stdout, stderr, 4, "project.invalid_configuration")
	if !strings.Contains(stdout, "artifact.invalid_content") {
		t.Fatalf("content diagnostic missing: %s", stdout)
	}
	assertNotInitialized(t, root)
}

func TestProjectInitRollsBackAndAllowsRetryAfterFailure(t *testing.T) {
	root := newUninitializedRepository(t)
	t.Chdir(root)
	projectInitAfterWrite = func() error { return errors.New("injected failure") }
	t.Cleanup(func() { projectInitAfterWrite = nil })

	code, stdout, stderr := runCLI(nil, initArgs...)
	if code != 6 || stderr != "" || jsonString(t, stdout, "error", "mutation") != "none" {
		t.Fatalf("injected failure = exit %d stdout %s stderr %s", code, stdout, stderr)
	}
	assertNotInitialized(t, root)

	projectInitAfterWrite = nil
	code, stdout, stderr = runCLI(nil, initArgs...)
	assertSuccess(t, code, stdout, stderr)
	code, stdout, stderr = runCLI(nil, "--output", "json", "check")
	assertSuccess(t, code, stdout, stderr)
}

func TestCheckAfterInitReportsTampering(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func(t *testing.T, root string)
		wantExit int
		wantCode string
	}{
		{
			name: "newer project schema",
			mutate: func(t *testing.T, root string) {
				data := strings.Replace(readTestFile(t, root, ".protobot/project.yaml"), "project: 1", "project: 2", 1)
				writeTestFile(t, root, ".protobot/project.yaml", data)
			},
			wantExit: 3,
			wantCode: "schema.unsupported_version",
		},
		{
			name: "artifact digest mismatch",
			mutate: func(t *testing.T, root string) {
				writeTestFile(t, root, "docs/vision.md", "# Edited outside ears-manager\n")
			},
			wantExit: 4,
			wantCode: "artifact.digest_mismatch",
		},
		{
			name: "store digest mismatch",
			mutate: func(t *testing.T, root string) {
				data := readTestFile(t, root, ".protobot/project.yaml")
				emptyDigest, err := specvalidation.CanonicalTextDigest(nil)
				if err != nil {
					t.Fatal(err)
				}
				other, err := specvalidation.CanonicalTextDigest([]byte("other"))
				if err != nil {
					t.Fatal(err)
				}
				writeTestFile(t, root, ".protobot/project.yaml", strings.Replace(data, emptyDigest, other, 1))
			},
			wantExit: 3,
			wantCode: "project.store_digest_mismatch",
		},
		{
			name: "missing projection manifest",
			mutate: func(t *testing.T, root string) {
				if err := os.Remove(filepath.Join(root, ".protobot", "projection.yaml")); err != nil {
					t.Fatal(err)
				}
			},
			wantExit: 4,
			wantCode: "projection.unclassified",
		},
		{
			name: "registered path reclassified",
			mutate: func(t *testing.T, root string) {
				writeTestFile(t, root, ".protobot/projection.yaml", "paths:\n  - path: docs/architecture.md\n    class: shared\n  - path: docs/vision.md\n    class: implementation\n")
			},
			wantExit: 4,
			wantCode: "projection.unclassified",
		},
		{
			name: "malformed projection manifest",
			mutate: func(t *testing.T, root string) {
				writeTestFile(t, root, ".protobot/projection.yaml", "paths:\n  - docs/vision.md\n")
			},
			wantExit: 4,
			wantCode: "projection.invalid",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := newUninitializedRepository(t)
			t.Chdir(root)
			code, stdout, stderr := runCLI(nil, initArgs...)
			assertSuccess(t, code, stdout, stderr)
			tc.mutate(t, root)
			code, stdout, stderr = runCLI(nil, "--output", "json", "check")
			if code != tc.wantExit || stderr != "" || !strings.Contains(stdout, `"code":"`+tc.wantCode+`"`) {
				t.Fatalf("check = exit %d stdout %s stderr %s; want exit %d with %s", code, stdout, stderr, tc.wantExit, tc.wantCode)
			}
		})
	}
}
