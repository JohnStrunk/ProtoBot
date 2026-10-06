package cli

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/redhat-et/protobot/ears-manager/internal/records"
	"github.com/redhat-et/protobot/ears-manager/internal/specvalidation"
	"github.com/redhat-et/protobot/ears-manager/internal/storage"
)

func TestSelfHostingFixturePassesCheck(t *testing.T) {
	root := newSelfHostingCheckout(t)
	t.Chdir(root)

	code, stdout, stderr := runCLI(nil, "--output", "json", "check")
	assertSuccess(t, code, stdout, stderr)
	if !jsonBool(t, stdout, "data", "valid") {
		t.Fatalf("check did not report valid: %s", stdout)
	}
	if jsonBool(t, stdout, "mutation", "applied") {
		t.Fatalf("check mutated the store: %s", stdout)
	}
	var result struct {
		Data struct {
			RecordCounts struct {
				Requirements int `json:"requirements"`
				Interfaces   int `json:"interfaces"`
				ChangeSets   int `json:"change_sets"`
				Artifacts    int `json:"artifacts"`
			} `json:"record_counts"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("decode check result: %v", err)
	}
	if got := result.Data.RecordCounts; got.Requirements != 1 || got.Interfaces != 1 || got.ChangeSets != 1 || got.Artifacts != 2 {
		t.Fatalf("seeded record counts = %#v, want one requirement, interface, and change set plus two artifacts", got)
	}
}

func TestSelfHostingFixtureDetectsDirectArtifactEditWithoutStaging(t *testing.T) {
	root := newSelfHostingCheckout(t)
	t.Chdir(root)
	writeTestFile(t, root, "unrelated.txt", "not a specification record\n")
	writeTestFile(t, root, "docs/vision.md", "# Edited outside ears-manager\n")
	before := gitOutput(t, root, "status", "--porcelain", "--untracked-files=all")

	code, stdout, stderr := runCLI(nil, "--output", "json", "check")
	assertValidationFailure(t, code, stdout, stderr)
	if !strings.Contains(stdout, `"code":"artifact.digest_mismatch"`) {
		t.Fatalf("check omitted artifact.digest_mismatch: %s", stdout)
	}
	if jsonString(t, stdout, "error", "mutation") != "none" {
		t.Fatalf("check claimed a mutation: %s", stdout)
	}

	after := gitOutput(t, root, "status", "--porcelain", "--untracked-files=all")
	if after != before {
		t.Fatalf("check changed git status\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if !strings.Contains(after, "?? unrelated.txt") {
		t.Fatalf("unrelated untracked file was staged or removed: %q", after)
	}
	if staged := gitOutput(t, root, "diff", "--cached", "--name-only"); staged != "" {
		t.Fatalf("check staged files: %q", staged)
	}
}

func TestSelfHostingFixtureHumanAndJSONShareDiagnosticCode(t *testing.T) {
	root := newSelfHostingCheckout(t)
	t.Chdir(root)
	writeTestFile(t, root, "docs/vision.md", "# Edited outside ears-manager\n")

	code, stdout, stderr := runCLI(nil, "--output", "json", "check")
	assertValidationFailure(t, code, stdout, stderr)
	if jsonString(t, stdout, "error", "code") != "validation.failed" || jsonString(t, stdout, "error", "retry") != "revise-request" {
		t.Fatalf("JSON envelope drifted: %s", stdout)
	}

	humanCode, humanStdout, humanStderr := runCLI(nil, "check")
	if humanCode != 4 || humanStdout != "" {
		t.Fatalf("human check = exit %d stdout %q stderr %q", humanCode, humanStdout, humanStderr)
	}
	if !strings.Contains(humanStderr, "error[validation.failed]:") {
		t.Fatalf("human diagnostic omitted the JSON error code: %s", humanStderr)
	}
	if !strings.Contains(humanStderr, "artifacts[id=vision].digest") || !strings.Contains(humanStderr, "retry: revise-request") {
		t.Fatalf("human diagnostic omitted the path or retry: %s", humanStderr)
	}
}

func TestSelfHostingFixtureDetectsUngovernedStoreEdit(t *testing.T) {
	root := newSelfHostingCheckout(t)
	t.Chdir(root)
	writeTestFile(t, root, ".protobot/requirements/REQ-FIX-00001.yaml", "id: REQ-FIX-00001\n")

	code, stdout, stderr := runCLI(nil, "--output", "json", "check")
	assertValidationFailure(t, code, stdout, stderr)
	if !hasDiagnostic(t, stdout, "project.store_digest_mismatch", "store_digests.requirements") {
		t.Fatalf("check omitted the store digest mismatch: %s", stdout)
	}
}

func TestSelfHostingSeedMatchesGovernedInitialization(t *testing.T) {
	source := selfHostingRoot(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"docs/vision.md", "docs/architecture.md"} {
		writeTestFile(t, root, rel, readTestFile(t, source, rel))
	}
	git(t, root, "init", "-b", "main")
	git(t, root, "config", "user.email", "test@example.invalid")
	git(t, root, "config", "user.name", "Test User")
	git(t, root, "add", ".")
	git(t, root, "commit", "-m", "existing repository")
	git(t, root, "checkout", "-b", "cs/00001-project-init")
	t.Chdir(root)

	initializeSelfHostingProject(t)

	gotProjection := readTestFile(t, root, ".protobot/projection.yaml")
	wantProjection := readTestFile(t, source, ".protobot/projection.yaml")
	if gotProjection != wantProjection {
		t.Fatalf("projection.yaml from project init did not match the committed seed\ngot:\n%s\nwant:\n%s", gotProjection, wantProjection)
	}

	code, stdout, stderr := runCLI(nil, "--output", "json", "check")
	assertSuccess(t, code, stdout, stderr)

	var gotProject, wantProject records.ProjectConfig
	if err := storage.ReadFile(filepath.Join(root, ".protobot", "project.yaml"), &gotProject); err != nil {
		t.Fatal(err)
	}
	if err := storage.ReadFile(filepath.Join(source, ".protobot", "project.yaml"), &wantProject); err != nil {
		t.Fatal(err)
	}
	// Store digests depend on the generated change-set manifest's base commit;
	// all other project-init output must reproduce exactly.
	gotProject.StoreDigests = records.StoreDigests{}
	wantProject.StoreDigests = records.StoreDigests{}
	if !reflect.DeepEqual(gotProject, wantProject) {
		t.Fatalf("project configuration from governed init differs:\ngot: %#v\nwant: %#v", gotProject, wantProject)
	}

	var gotChangeSet, wantChangeSet records.ChangeSet
	if err := storage.ReadFile(filepath.Join(root, ".protobot", "change-sets", "cs-00001.yaml"), &gotChangeSet); err != nil {
		t.Fatal(err)
	}
	if err := storage.ReadFile(filepath.Join(source, ".protobot", "change-sets", "cs-00001.yaml"), &wantChangeSet); err != nil {
		t.Fatal(err)
	}
	gotChangeSet.BaseCommit, wantChangeSet.BaseCommit = "", ""
	gotChangeSet.ImpactAssessmentBaseCommit, wantChangeSet.ImpactAssessmentBaseCommit = "", ""
	if !reflect.DeepEqual(gotChangeSet, wantChangeSet) {
		t.Fatalf("change-set record from governed writes differs:\ngot: %#v\nwant: %#v", gotChangeSet, wantChangeSet)
	}
	for _, rel := range []string{
		"interfaces/protobot-cli.yaml",
		"requirements/REQ-CLI-00001.yaml",
	} {
		path := filepath.Join(".protobot", rel)
		if got, want := readTestFile(t, root, path), readTestFile(t, source, path); got != want {
			t.Fatalf("governed record %s differs from the committed seed\ngot:\n%s\nwant:\n%s", rel, got, want)
		}
	}

	committed := readTestFile(t, source, ".protobot/project.yaml")
	for _, rel := range []string{"docs/vision.md", "docs/architecture.md"} {
		digest, err := specvalidation.CanonicalTextDigest([]byte(readTestFile(t, source, rel)))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(committed, digest) || !strings.Contains(readTestFile(t, root, ".protobot/project.yaml"), digest) {
			t.Fatalf("artifact digest for %s is missing from the seed or from a fresh init: %s", rel, digest)
		}
	}
}

func TestCommittedSeedDigestsMatchRegisteredArtifacts(t *testing.T) {
	source := selfHostingRoot(t)
	projectYAML := readTestFile(t, source, ".protobot/project.yaml")
	for _, rel := range []string{"docs/vision.md", "docs/architecture.md"} {
		digest, err := specvalidation.CanonicalTextDigest([]byte(readTestFile(t, source, rel)))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(projectYAML, digest) {
			t.Fatalf("committed project.yaml does not record the digest of %s (%s)", rel, digest)
		}
	}
}

func TestSelfHostingSeedMatchesWorkflowTrustAnchor(t *testing.T) {
	source := selfHostingRoot(t)
	var project records.ProjectConfig
	if err := storage.ReadFile(filepath.Join(source, ".protobot", "project.yaml"), &project); err != nil {
		t.Fatal(err)
	}
	if project.Repository.CanonicalRemote != "https://github.com/redhat-et/ProtoBot.git" || project.Repository.DefaultBranch != "main" {
		t.Fatalf("workflow fetch target and project configuration diverged: remote=%q branch=%q", project.Repository.CanonicalRemote, project.Repository.DefaultBranch)
	}
	storePath := project.Stores.WithDefaults().ChangeSets
	if storePath != ".protobot/change-sets" {
		t.Fatalf("workflow manifest path and configured store diverged: %q", storePath)
	}
	projectYAML := readTestFile(t, source, ".protobot/project.yaml")
	if !strings.Contains(projectYAML, "\n    change_sets: "+storePath+"\n") {
		t.Fatalf("project.yaml store encoding no longer matches the CI parser for %q", storePath)
	}
	workflow := readTestFile(t, source, ".github/workflows/ci-workflow.yaml")
	fetch := "git fetch --no-tags " + project.Repository.CanonicalRemote + " refs/heads/" + project.Repository.DefaultBranch
	if !strings.Contains(workflow, fetch) || !strings.Contains(workflow, "main_store="+storePath) {
		t.Fatalf("workflow trust anchor does not match project configuration: fetch=%q store=%q", fetch, storePath)
	}
}

func newSelfHostingCheckout(t *testing.T) string {
	t.Helper()
	source := selfHostingRoot(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"docs/vision.md", "docs/architecture.md"} {
		writeTestFile(t, root, rel, readTestFile(t, source, rel))
	}
	git(t, root, "init", "-b", "main")
	git(t, root, "config", "user.email", "test@example.invalid")
	git(t, root, "config", "user.name", "Test User")
	git(t, root, "add", ".")
	git(t, root, "commit", "-m", "registered artifacts")
	git(t, root, "checkout", "-b", "pr/self-hosting-seed")
	for _, rel := range []string{
		".protobot/project.yaml",
		".protobot/projection.yaml",
		".protobot/change-sets/cs-00001.yaml",
		".protobot/interfaces/protobot-cli.yaml",
		".protobot/requirements/REQ-CLI-00001.yaml",
	} {
		writeTestFile(t, root, rel, readTestFile(t, source, rel))
	}
	git(t, root, "add", ".protobot")
	git(t, root, "commit", "-m", "propose self-hosting seed")
	return root
}

func initializeSelfHostingProject(t *testing.T) {
	t.Helper()

	code, stdout, stderr := runCLI(nil, "--output", "json", "project", "init",
		"--id", "protobot",
		"--name", "ProtoBot",
		"--canonical-remote", "https://github.com/redhat-et/ProtoBot.git",
		"--review-mode", "multi-player",
	)
	assertSuccess(t, code, stdout, stderr)

	code, stdout, stderr = runCLI(nil, "--output", "json", "change-set", "create",
		"--intent", "Seed ProtoBot's initial specification records",
		"--affected-scope", "ears-manager",
		"--implementation-required", "false",
		"--implementation-rationale", "This initial record set specifies the existing CLI boundary and does not change implementation behavior.",
		"--created", "2026-10-05T16:00:00Z",
	)
	assertSuccess(t, code, stdout, stderr)

	code, stdout, stderr = runCLI(nil, "--output", "json", "interface", "add",
		"--change-set", "CS-00001",
		"--id", "protobot-cli",
		"--name", "ProtoBot CLI",
		"--type", "cli",
		"--spec-approach", "prose",
		"--description", "Command-line interface for project governance and specification validation.",
		"--created", "2026-10-05T16:01:00Z",
	)
	assertSuccess(t, code, stdout, stderr)

	code, stdout, stderr = runCLI(nil, "--output", "json", "change-set", "update",
		"--change-set", "CS-00001",
		"--affected-interface", "protobot-cli",
		"--implementation-required", "false",
		"--implementation-rationale", "This initial record set specifies the existing CLI boundary and does not change implementation behavior.",
	)
	assertSuccess(t, code, stdout, stderr)

	code, stdout, stderr = runCLI(nil, "--output", "json", "requirement", "add",
		"--change-set", "CS-00001",
		"--id", "REQ-CLI-00001",
		"--type", "event-driven",
		"--text", "When a contributor requests a specification check, the CLI shall validate the registered artifacts and structured records.",
		"--interface", "protobot-cli",
		"--scope", "ears-manager",
		"--verification-mode", "isolated-interface",
		"--provenance", "user-authored",
		"--created", "2026-10-05T16:02:00Z",
	)
	assertSuccess(t, code, stdout, stderr)
}

func selfHostingRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", ".."))
}
