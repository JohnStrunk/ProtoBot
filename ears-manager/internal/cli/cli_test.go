package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/redhat-et/protobot/ears-manager/internal/records"
	"github.com/redhat-et/protobot/ears-manager/internal/specvalidation"
	"github.com/redhat-et/protobot/ears-manager/internal/storage"
)

func TestCLICommandFlowAndDeterministicJSON(t *testing.T) {
	root := newFixtureProject(t)
	t.Chdir(root)

	code, stdout, stderr := runCLI(nil, "--output", "json", "change-set", "create", "--intent", "Add CLI records", "--implementation-required", "true", "--created", "2026-09-18T12:00:00Z")
	assertSuccess(t, code, stdout, stderr)
	changeSetID := jsonString(t, stdout, "data", "change_set", "id")
	if changeSetID != "CS-00001" {
		t.Fatalf("change set ID = %q, want CS-00001", changeSetID)
	}

	code, stdout, stderr = runCLI(nil, "--output", "json", "interface", "add", "--change-set", changeSetID, "--id", "cli-main", "--name", "CLI", "--type", "cli", "--spec-approach", "prose", "--created", "2026-09-18T12:01:00Z")
	assertSuccess(t, code, stdout, stderr)
	if jsonString(t, stdout, "data", "operation", "interface_id") != "cli-main" {
		t.Fatalf("interface operation was not returned: %s", stdout)
	}

	code, stdout, stderr = runCLI(nil, "--output", "json", "requirement", "add", "--change-set", changeSetID, "--id", "REQ-CLI-00001", "--type", "event-driven", "--text", "When a user asks, the CLI shall print help.", "--interface", "cli-main", "--scope", "cli", "--verification-mode", "isolated-interface", "--provenance", "user-authored", "--created", "2026-09-18T12:02:00Z")
	assertSuccess(t, code, stdout, stderr)
	if jsonString(t, stdout, "data", "requirement", "id") != "REQ-CLI-00001" {
		t.Fatalf("requirement was not returned: %s", stdout)
	}

	code, firstList, stderr := runCLI(nil, "--output", "json", "requirement", "list", "--interface", "cli-main")
	assertSuccess(t, code, firstList, stderr)
	code, secondList, stderr := runCLI(nil, "--output", "json", "requirement", "list", "--interface", "cli-main")
	assertSuccess(t, code, secondList, stderr)
	if firstList != secondList {
		t.Fatalf("repeated list output differed:\n%s\n---\n%s", firstList, secondList)
	}

	code, stdout, stderr = runCLI(nil, "--output", "json", "check")
	assertSuccess(t, code, stdout, stderr)
	if !jsonBool(t, stdout, "data", "valid") {
		t.Fatalf("check did not report valid: %s", stdout)
	}

	code, stdout, stderr = runCLI(nil, "--output", "json", "requirement", "add", "--change-set", changeSetID, "--id", "REQ-CLI-00002", "--type", "event-driven", "--text", "This is not an event-driven statement.", "--interface", "cli-main", "--scope", "cli", "--verification-mode", "isolated-interface", "--provenance", "user-authored", "--created", "2026-09-18T12:03:00Z")
	if code != 4 || stderr != "" || jsonString(t, stdout, "error", "code") != "validation.failed" {
		t.Fatalf("invalid requirement result = code %d, stdout %s, stderr %s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "requirement.ears_pattern_mismatch") {
		t.Fatalf("invalid requirement omitted EARS diagnostic: %s", stdout)
	}
	code, stdout, stderr = runCLI(nil, "--output", "json", "requirement", "update", "--change-set", changeSetID, "--id", "REQ-CLI-00001", "--status", "retired")
	if code != 4 || stderr != "" || !strings.Contains(stdout, "requirement.use_retire") {
		t.Fatalf("status retirement result = code %d stdout %s stderr %s", code, stdout, stderr)
	}

	code, stdout, stderr = runCLI(nil, "--output", "json", "requirement", "show", "--id", "REQ-CLI-00002")
	if code != 4 || stderr != "" || !strings.Contains(stdout, "requirement.not_found") {
		t.Fatalf("failed mutation left an unexpected record: code=%d stdout=%s stderr=%s", code, stdout, stderr)
	}

	code, stdout, stderr = runCLI(nil, "--output", "json", "change-set", "create", "--intent", "Retire CLI record", "--implementation-required", "false", "--implementation-rationale", "Documentation-only retirement", "--created", "2026-09-18T12:04:00Z")
	assertSuccess(t, code, stdout, stderr)
	retirementChangeSet := jsonString(t, stdout, "data", "change_set", "id")
	code, stdout, stderr = runCLI(nil, "--output", "json", "requirement", "retire", "--change-set", retirementChangeSet, "--id", "REQ-CLI-00001")
	assertSuccess(t, code, stdout, stderr)
	if jsonString(t, stdout, "data", "after", "status") != "retired" {
		t.Fatalf("retirement did not return retired status: %s", stdout)
	}
}

func TestCLIArtifactPutGetAndAtomicInvalidWrite(t *testing.T) {
	root := newFixtureProject(t)
	t.Chdir(root)

	code, stdout, stderr := runCLI(nil, "--output", "json", "change-set", "create", "--intent", "Update artifacts", "--implementation-required", "true", "--created", "2026-09-18T13:00:00Z")
	assertSuccess(t, code, stdout, stderr)
	changeSetID := jsonString(t, stdout, "data", "change_set", "id")

	code, stdout, stderr = runCLI([]byte("# Updated Vision\r\n"), "--output", "json", "artifact", "put", "--change-set", changeSetID, "--id", "vision", "--kind", "vision", "--path", "docs/vision.md", "--owner", "user", "--content-stdin")
	assertSuccess(t, code, stdout, stderr)
	if jsonString(t, stdout, "data", "artifact", "digest") == "" {
		t.Fatalf("artifact digest was empty: %s", stdout)
	}

	code, stdout, stderr = runCLI(nil, "--output", "json", "artifact", "get", "--id", "vision")
	assertSuccess(t, code, stdout, stderr)
	if jsonString(t, stdout, "data", "content") != "# Updated Vision\n" {
		t.Fatalf("artifact content = %q", jsonString(t, stdout, "data", "content"))
	}
	code, stdout, stderr = runCLI([]byte("# Reowned Vision\n"), "--output", "json", "artifact", "put", "--change-set", changeSetID, "--id", "vision", "--kind", "vision", "--path", "docs/vision.md", "--owner", "ears-manager", "--content-stdin")
	if code != 5 || stderr != "" || !strings.Contains(stdout, "artifact.owner_immutable") {
		t.Fatalf("owner reassignment result = code %d stdout %s stderr %s", code, stdout, stderr)
	}

	manifestPath := filepath.Join(root, ".protobot", "change-sets", "cs-00001.yaml")
	manifestBefore, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = runCLI(nil, "--output", "json", "requirement", "add", "--change-set", changeSetID, "--id", "REQ-CLI-00001", "--type", "event-driven", "--text", "Invalid EARS form", "--interface", "missing-interface", "--verification-mode", "isolated-interface", "--provenance", "user-authored", "--created", "2026-09-18T13:01:00Z")
	if code != 4 || stderr != "" || !strings.Contains(stdout, "validation.failed") {
		t.Fatalf("invalid mutation result = code %d stdout %s stderr %s", code, stdout, stderr)
	}
	manifestAfter, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(manifestBefore) != string(manifestAfter) {
		t.Fatal("invalid mutation changed the change-set manifest")
	}

	code, stdout, stderr = runCLI(nil, "--output", "json", "requirement", "add", "--change-set", changeSetID, "--id", "REQ-CLI-00002", "--type", "ubiquitous", "--text", "The CLI shall preserve records.", "--interface", "missing-interface", "--verification-mode", "isolated-interface", "--provenance", "user-authored", "--created", "2026-09-18T13:02:00Z", "--relationship", "depends-on=REQ-MISSING-00001")
	if code != 4 || stderr != "" || !strings.Contains(stdout, "reference.not_found") {
		t.Fatalf("dangling relationship result = code %d stdout %s stderr %s", code, stdout, stderr)
	}
	manifestAfter, err = os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(manifestBefore) != string(manifestAfter) {
		t.Fatal("dangling relationship changed the change-set manifest")
	}
}

func TestCLIRejectsStaleChangeSetBase(t *testing.T) {
	root := newFixtureProject(t)
	t.Chdir(root)
	code, stdout, stderr := runCLI(nil, "--output", "json", "change-set", "create", "--intent", "Stale base", "--implementation-required", "true", "--created", "2026-09-18T14:00:00Z")
	assertSuccess(t, code, stdout, stderr)
	changeSetID := jsonString(t, stdout, "data", "change_set", "id")
	if err := os.WriteFile(filepath.Join(root, "unrelated.txt"), []byte("new commit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", "unrelated.txt")
	git(t, root, "commit", "-m", "advance base")

	code, stdout, stderr = runCLI(nil, "--output", "json", "interface", "add", "--change-set", changeSetID, "--id", "stale", "--name", "Stale", "--type", "cli", "--created", "2026-09-18T14:01:00Z")
	if code != 5 || stderr != "" || !strings.Contains(stdout, "change_set.base_mismatch") {
		t.Fatalf("stale base result = code %d stdout %s stderr %s", code, stdout, stderr)
	}
}

func TestCLIRejectsReservedPathSymlinkAlias(t *testing.T) {
	root := newFixtureProject(t)
	t.Chdir(root)
	if err := os.Symlink(".protobot", filepath.Join(root, "alias")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	code, stdout, stderr := runCLI(nil, "--output", "json", "change-set", "create", "--intent", "Reject alias", "--implementation-required", "true", "--created", "2026-09-18T15:00:00Z")
	assertSuccess(t, code, stdout, stderr)
	changeSetID := jsonString(t, stdout, "data", "change_set", "id")
	code, stdout, stderr = runCLI([]byte("secret\n"), "--output", "json", "artifact", "put", "--change-set", changeSetID, "--id", "alias", "--kind", "interface-prose", "--path", "alias/policy.yaml", "--owner", "user", "--content-stdin")
	if code != 4 || stderr != "" || !strings.Contains(stdout, "artifact.invalid_path") {
		t.Fatalf("symlink alias result = code %d stdout %s stderr %s", code, stdout, stderr)
	}
}

func TestCLIMaintainsSymmetricRelationships(t *testing.T) {
	root := newFixtureProject(t)
	t.Chdir(root)

	code, stdout, stderr := runCLI(nil, "--output", "json", "change-set", "create", "--intent", "Add related requirements", "--implementation-required", "true", "--created", "2026-09-18T16:00:00Z")
	assertSuccess(t, code, stdout, stderr)
	changeSetID := jsonString(t, stdout, "data", "change_set", "id")
	addRequirement := func(id string) {
		t.Helper()
		code, stdout, stderr = runCLI(nil, "--output", "json", "requirement", "add", "--change-set", changeSetID, "--id", id, "--type", "ubiquitous", "--text", "The system shall preserve relationships.", "--scope", "cli", "--verification-mode", "isolated-interface", "--provenance", "user-authored", "--created", "2026-09-18T16:01:00Z")
		assertSuccess(t, code, stdout, stderr)
	}
	addRequirement("REQ-REL-00001")
	addRequirement("REQ-REL-00002")

	code, stdout, stderr = runCLI(nil, "--output", "json", "requirement", "update", "--change-set", changeSetID, "--id", "REQ-REL-00001", "--relationship", "conflicts-with=REQ-REL-00002")
	assertSuccess(t, code, stdout, stderr)

	var target records.Requirement
	if err := storage.ReadFile(filepath.Join(root, ".protobot", "requirements", "REQ-REL-00002.yaml"), &target); err != nil {
		t.Fatal(err)
	}
	if !hasRelationship(target.Relationships, "conflicts-with") {
		t.Fatalf("target requirement did not receive inverse relationship: %#v", target.Relationships)
	}
}

func TestCLIAllowsAddThenRetireInOneChangeSet(t *testing.T) {
	root := newFixtureProject(t)
	t.Chdir(root)

	code, stdout, stderr := runCLI(nil, "--output", "json", "change-set", "create", "--intent", "Add then retire", "--implementation-required", "false", "--implementation-rationale", "The proposed record is withdrawn.", "--created", "2026-09-18T17:00:00Z")
	assertSuccess(t, code, stdout, stderr)
	changeSetID := jsonString(t, stdout, "data", "change_set", "id")
	code, stdout, stderr = runCLI(nil, "--output", "json", "requirement", "add", "--change-set", changeSetID, "--id", "REQ-RET-00001", "--type", "ubiquitous", "--text", "The system shall preserve withdrawn records.", "--scope", "cli", "--verification-mode", "isolated-interface", "--provenance", "user-authored", "--created", "2026-09-18T17:01:00Z")
	assertSuccess(t, code, stdout, stderr)
	code, stdout, stderr = runCLI(nil, "--output", "json", "requirement", "retire", "--change-set", changeSetID, "--id", "REQ-RET-00001")
	assertSuccess(t, code, stdout, stderr)
	if jsonString(t, stdout, "data", "after", "status") != "retired" {
		t.Fatalf("retirement did not return retired status: %s", stdout)
	}

	var changeSet records.ChangeSet
	if err := storage.ReadFile(filepath.Join(root, ".protobot", "change-sets", "cs-00001.yaml"), &changeSet); err != nil {
		t.Fatal(err)
	}
	if len(changeSet.Operations) != 1 || changeSet.Operations[0].Action != "retire" {
		t.Fatalf("same-change-set add/retire operations = %#v", changeSet.Operations)
	}
}

func TestApplyTransactionRejectsConcurrentCreation(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "records"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := "records/new.yaml"
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(path)), []byte("concurrent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, failure := applyTransaction(root, []fileWrite{{path: path, data: []byte("replacement\n")}}, map[string]fileExpectation{path: {}}, nil)
	if failure == nil || failure.Code != "change_set.concurrent_update" {
		t.Fatalf("concurrent creation failure = %#v", failure)
	}
}

func TestApplyTransactionRollsBackSharedNewDirectory(t *testing.T) {
	root := t.TempDir()
	_, failure := applyTransaction(root, []fileWrite{
		{path: "records/one.yaml", data: []byte("one\n")},
		{path: "records/two.yaml", data: []byte("two\n")},
	}, map[string]fileExpectation{
		"records/one.yaml": {},
		"records/two.yaml": {},
	}, func() *commandFailure {
		return validationFailure("validation.failed", "forced rollback", nil)
	})
	if failure == nil || failure.Code != "validation.failed" || failure.Mutation != "none" {
		t.Fatalf("rollback failure = %#v", failure)
	}
	if _, err := os.Stat(filepath.Join(root, "records")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rollback directory stat error = %v", err)
	}
}

func TestApplyTransactionRejectsSymlinkTarget(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "records"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.yaml")
	if err := os.WriteFile(outside, []byte("outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "records", "target.yaml")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	_, failure := applyTransaction(root, []fileWrite{{path: "records/target.yaml", data: []byte("replacement\n")}}, map[string]fileExpectation{"records/target.yaml": {present: true, data: []byte("outside\n")}}, nil)
	if failure == nil || failure.Code != "storage.write_not_allowed" || failure.ExitCode != 4 || failure.Mutation != "none" {
		t.Fatalf("symlink target failure = %#v", failure)
	}
}

func TestIOFailureMutationClassification(t *testing.T) {
	if failure := ioFailure("storage.read_failed", "read failed"); failure.Mutation != "none" {
		t.Fatalf("read failure mutation = %q, want none", failure.Mutation)
	}
	if failure := unknownIOFailure("storage.write_unknown", "unknown"); failure.Mutation != "unknown" {
		t.Fatalf("unknown write failure mutation = %q, want unknown", failure.Mutation)
	}
}

func newFixtureProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, directory := range []string{
		".protobot/requirements",
		".protobot/interfaces",
		".protobot/change-sets",
		"docs",
	} {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(directory)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	vision := []byte("# Fixture Vision\n")
	if err := os.WriteFile(filepath.Join(root, "docs", "vision.md"), vision, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "architecture.md"), []byte("# Fixture Architecture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	digest, err := specvalidation.CanonicalTextDigest(vision)
	if err != nil {
		t.Fatal(err)
	}
	emptyStoreDigest, err := specvalidation.CanonicalTextDigest(nil)
	if err != nil {
		t.Fatal(err)
	}
	config := records.ProjectConfig{
		Project:    records.ProjectIdentity{ID: "fixture", Name: "CLI fixture"},
		Repository: records.RepositoryConfig{CanonicalRemote: "https://example.invalid/fixture.git", DefaultBranch: "main", ReviewMode: "single-player", BranchPrefix: "cs/"},
		SchemaVersions: records.SchemaVersions{
			Project:       records.CurrentProjectSchemaVersion,
			Specification: records.CurrentSpecificationSchemaVersion,
		},
		Stores: records.StorePaths{Requirements: ".protobot/requirements", Interfaces: ".protobot/interfaces", ChangeSets: ".protobot/change-sets"},
		StoreDigests: records.StoreDigests{
			Requirements: emptyStoreDigest,
			Interfaces:   emptyStoreDigest,
			ChangeSets:   emptyStoreDigest,
		},
		Artifacts: []records.ArtifactEntry{
			{ID: "vision", Kind: records.ArtifactVision, Path: "docs/vision.md", Digest: digest, Owner: "user"},
		},
	}
	data, err := storage.Encode(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".protobot", "project.yaml"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, root, "init", "-b", "main")
	git(t, root, "config", "user.email", "test@example.invalid")
	git(t, root, "config", "user.name", "Test User")
	git(t, root, "add", ".")
	git(t, root, "commit", "-m", "fixture")
	return root
}

func git(t *testing.T, root string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, output)
	}
}

func runCLI(stdin []byte, args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := Run(args, bytes.NewReader(stdin), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func assertSuccess(t *testing.T, code int, stdout, stderr string) {
	t.Helper()
	if code != 0 || stderr != "" {
		t.Fatalf("command failed: code=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, `"ok":true`) {
		t.Fatalf("command did not return success envelope: %s", stdout)
	}
}

func jsonString(t *testing.T, data string, path ...string) string {
	t.Helper()
	value := decodeJSONPath(t, data, path...)
	result, ok := value.(string)
	if !ok {
		t.Fatalf("JSON path %v was %T, want string: %s", path, value, data)
	}
	return result
}

func jsonBool(t *testing.T, data string, path ...string) bool {
	t.Helper()
	value := decodeJSONPath(t, data, path...)
	result, ok := value.(bool)
	if !ok {
		t.Fatalf("JSON path %v was %T, want bool: %s", path, value, data)
	}
	return result
}

func decodeJSONPath(t *testing.T, data string, path ...string) any {
	t.Helper()
	var value any
	if err := json.Unmarshal([]byte(data), &value); err != nil {
		t.Fatalf("invalid JSON %q: %v", data, err)
	}
	for _, part := range path {
		object, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("JSON path %v crossed non-object %T", path, value)
		}
		value = object[part]
	}
	return value
}
