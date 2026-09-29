package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/redhat-et/protobot/ears-manager/internal/records"
	"github.com/redhat-et/protobot/ears-manager/internal/storage"
)

func TestChangeSetShowAtReadsTheManifestOfACommit(t *testing.T) {
	_, changeSetID, tip := newCommittedChangeSet(t)

	code, working, stderr := runCLI(nil, "--output", "json", "change-set", "show", "--change-set", changeSetID)
	assertSuccess(t, code, working, stderr)
	code, atTip, stderr := runCLI(nil, "--output", "json", "change-set", "show", "--change-set", changeSetID, "--at", tip)
	assertSuccess(t, code, atTip, stderr)
	if atTip != working {
		t.Fatalf("show --at differs from the working-tree read of the same commit:\n%s\n---\n%s", atTip, working)
	}
	if jsonString(t, atTip, "data", "status") != "proposed" {
		t.Fatalf("status at the change-set branch tip = %s", atTip)
	}
	if jsonInt(t, atTip, "data", "changed_count") != 1 {
		t.Fatalf("changed_count at the tip = %s", atTip)
	}
	paths := jsonArray(t, atTip, "data", "paths")
	if len(paths) != 2 || paths[0] != ".protobot/change-sets/cs-00001.yaml" || paths[1] != ".protobot/requirements/REQ-CLI-00001.yaml" {
		t.Fatalf("paths at the tip = %s", atTip)
	}

	upper := strings.ToUpper(tip)
	code, stdout, stderr := runCLI(nil, "--output", "json", "change-set", "show", "--change-set", changeSetID, "--at", upper)
	assertSuccess(t, code, stdout, stderr)
	if stdout != atTip {
		t.Fatalf("an uppercase hash read a different result:\n%s", stdout)
	}

	code, stdout, stderr = runCLI(nil, "change-set", "show", "--change-set", changeSetID, "--at", tip)
	if code != 0 || stderr != "" || !strings.HasPrefix(stdout, "change-set show: ok\n") || !strings.Contains(stdout, `"status": "proposed"`) {
		t.Fatalf("human output = code %d stdout %q stderr %q", code, stdout, stderr)
	}
}

func TestChangeSetShowAtReportsAMissingManifest(t *testing.T) {
	root, changeSetID, _ := newCommittedChangeSet(t)
	base := gitOutput(t, root, "rev-parse", "main")

	code, stdout, stderr := runCLI(nil, "--output", "json", "change-set", "show", "--change-set", changeSetID, "--at", base)
	assertFailureCode(t, code, stdout, stderr, 4, "change_set.not_found")

	code, stdout, stderr = runCLI(nil, "--output", "json", "change-set", "show", "--change-set", "CS-00002", "--at", gitOutput(t, root, "rev-parse", "HEAD"))
	assertFailureCode(t, code, stdout, stderr, 4, "change_set.not_found")

	emptyTree := gitOutput(t, root, "mktree")
	uninitialized := gitOutput(t, root, "commit-tree", emptyTree, "-m", "no project")
	code, stdout, stderr = runCLI(nil, "--output", "json", "change-set", "show", "--change-set", changeSetID, "--at", uninitialized)
	assertFailureCode(t, code, stdout, stderr, 4, "change_set.not_found")
}

func TestChangeSetShowAtDerivesApprovalFromTheDefaultBranch(t *testing.T) {
	root, changeSetID, tip := newCommittedChangeSet(t)
	base := gitOutput(t, root, "rev-parse", "main")
	show := func(at string) string {
		t.Helper()
		code, stdout, stderr := runCLI(nil, "--output", "json", "change-set", "show", "--change-set", changeSetID, "--at", at)
		assertSuccess(t, code, stdout, stderr)
		return jsonString(t, stdout, "data", "status")
	}
	if status := show(tip); status != "proposed" {
		t.Fatalf("status at the tip before the merge = %s", status)
	}

	git(t, root, "switch", "main")
	git(t, root, "merge", "--no-ff", "-m", "Merge change set", "cs/00001-show-at")
	merge := gitOutput(t, root, "rev-parse", "HEAD")
	if status := show(merge); status != "approved" {
		t.Fatalf("status at the merge commit = %s", status)
	}
	if status := show(tip); status != "approved" {
		t.Fatalf("status at the merged tip = %s", status)
	}

	// The Source Control Manager reads the default head after a fetch, when
	// the local default branch can still be behind its remote-tracking branch.
	git(t, root, "update-ref", "refs/remotes/origin/main", merge)
	git(t, root, "switch", "--detach", merge)
	git(t, root, "branch", "-f", "main", base)
	if status := show(merge); status != "approved" {
		t.Fatalf("status at a merge only origin/main holds = %s", status)
	}

	git(t, root, "switch", "-c", "cs/00002-later")
	if err := os.WriteFile(filepath.Join(root, "docs", "architecture.md"), []byte("# Later Architecture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, root, "commit", "-am", "later work")
	if status := show(gitOutput(t, root, "rev-parse", "HEAD")); status != "proposed" {
		t.Fatalf("status at a commit on no default branch = %s", status)
	}
}

func TestChangeSetShowAtFailsWhenTheDefaultBranchIsUnresolved(t *testing.T) {
	root, changeSetID, tip := newCommittedChangeSet(t)
	git(t, root, "branch", "-m", "main", "trunk")

	code, stdout, stderr := runCLI(nil, "--output", "json", "change-set", "show", "--change-set", changeSetID, "--at", tip)
	assertFailureCode(t, code, stdout, stderr, 3, "project.default_branch_unresolved")
}

func TestChangeSetShowRefusesADefaultBranchThatIsNotABranchName(t *testing.T) {
	root, changeSetID, _ := newCommittedChangeSet(t)
	configPath := filepath.Join(root, ".protobot", "project.yaml")
	var config records.ProjectConfig
	if err := storage.ReadFile(configPath, &config); err != nil {
		t.Fatal(err)
	}
	// Git resolves this revision to the change-set branch tip itself, so the
	// unreviewed tip would read as approved.
	config.Repository.DefaultBranch = "cs/00001-show-at~0"
	data, err := storage.Encode(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, root, "commit", "-am", "default branch as revision syntax")

	code, stdout, stderr := runCLI(nil, "--output", "json", "change-set", "show", "--change-set", changeSetID, "--at", gitOutput(t, root, "rev-parse", "HEAD"))
	assertFailureCode(t, code, stdout, stderr, 3, "project.invalid_configuration")
	code, stdout, stderr = runCLI(nil, "--output", "json", "change-set", "show", "--change-set", changeSetID)
	assertFailureCode(t, code, stdout, stderr, 3, "project.invalid_configuration")
}

func TestChangeSetShowAtRefusesAnythingButAFullLocalCommitHash(t *testing.T) {
	root := newFixtureProject(t)
	t.Chdir(root)
	head := gitOutput(t, root, "rev-parse", "HEAD")
	hexBranch := strings.Repeat("a", 40)
	git(t, root, "branch", hexBranch)
	git(t, root, "tag", "-a", "v1", "-m", "annotated tag")
	tagObject := gitOutput(t, root, "rev-parse", "v1")
	if tagObject == head {
		t.Fatalf("annotated tag object %s is the commit itself", tagObject)
	}
	for _, test := range []struct {
		name  string
		value string
		code  string
	}{
		{name: "short hash", value: head[:12], code: "revision.invalid"},
		{name: "branch name", value: "main", code: "revision.invalid"},
		{name: "symbolic ref", value: "HEAD", code: "revision.invalid"},
		{name: "revision expression", value: head[:38] + "~1", code: "revision.invalid"},
		{name: "longer than a hash", value: head + "0", code: "revision.invalid"},
		{name: "unknown commit", value: strings.Repeat("f", 40), code: "revision.not_found"},
		{name: "branch named like a hash", value: hexBranch, code: "revision.not_found"},
		{name: "tree object", value: gitOutput(t, root, "rev-parse", "HEAD^{tree}"), code: "revision.not_found"},
		{name: "annotated tag object", value: tagObject, code: "revision.not_found"},
	} {
		t.Run(test.name, func(t *testing.T) {
			code, stdout, stderr := runCLI(nil, "--output", "json", "change-set", "show", "--change-set", "CS-00001", "--at", test.value)
			assertFailureCode(t, code, stdout, stderr, 4, test.code)
		})
	}

	code, stdout, _ := runCLI(nil, "change-set", "show", "--help")
	if code != 0 || stdout != "Usage: ears-manager change-set show --change-set CS-ID [--at FULL-SHA]\n" {
		t.Fatalf("show help = code %d %q", code, stdout)
	}
}

func TestChangeSetShowAtIgnoresTheWorkingTree(t *testing.T) {
	root, changeSetID, tip := newCommittedChangeSet(t)
	code, before, stderr := runCLI(nil, "--output", "json", "change-set", "show", "--change-set", changeSetID, "--at", tip)
	assertSuccess(t, code, before, stderr)

	manifestPath := filepath.Join(root, ".protobot", "change-sets", "cs-00001.yaml")
	var manifest records.ChangeSet
	if err := storage.ReadFile(manifestPath, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Intent = "Uncommitted intent"
	data, err := storage.Encode(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	refreshProjectDigests(t, root)
	code, stdout, stderr := runCLI(nil, "--output", "json", "change-set", "show", "--change-set", changeSetID)
	assertSuccess(t, code, stdout, stderr)
	if jsonString(t, stdout, "data", "change_set", "intent") != "Uncommitted intent" {
		t.Fatalf("the working-tree read missed the uncommitted edit: %s", stdout)
	}
	if err := os.Remove(filepath.Join(root, ".protobot", "requirements", "REQ-CLI-00001.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".protobot", "change-sets", "cs-00002.yaml"), []byte("not: a manifest\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = runCLI(nil, "--output", "json", "change-set", "show", "--change-set", changeSetID, "--at", tip)
	assertSuccess(t, code, stdout, stderr)
	if stdout != before {
		t.Fatalf("an uncommitted edit changed show --at:\n%s\n---\n%s", stdout, before)
	}

	if err := os.RemoveAll(filepath.Join(root, ".protobot")); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = runCLI(nil, "--output", "json", "change-set", "show", "--change-set", changeSetID, "--at", tip)
	assertSuccess(t, code, stdout, stderr)
	if stdout != before {
		t.Fatalf("a deleted working-tree project changed show --at:\n%s\n---\n%s", stdout, before)
	}
}

func TestChangeSetShowAtIgnoresReplaceRefs(t *testing.T) {
	root, changeSetID, tip := newCommittedChangeSet(t)
	code, before, stderr := runCLI(nil, "--output", "json", "change-set", "show", "--change-set", changeSetID, "--at", tip)
	assertSuccess(t, code, before, stderr)

	// With the replace ref, Git shows the base commit, which has no manifest,
	// in place of the tip.
	git(t, root, "replace", tip, gitOutput(t, root, "rev-parse", "main"))
	code, stdout, stderr := runCLI(nil, "--output", "json", "change-set", "show", "--change-set", changeSetID, "--at", tip)
	assertSuccess(t, code, stdout, stderr)
	if stdout != before {
		t.Fatalf("a replace ref changed show --at:\n%s\n---\n%s", stdout, before)
	}
}

func TestChangeSetShowAtRefusesASymlinkedStoreEntryAsTheWorkingTreeDoes(t *testing.T) {
	root, changeSetID, _ := newCommittedChangeSet(t)
	if err := os.Symlink("cs-00001.yaml", filepath.Join(root, ".protobot", "change-sets", "cs-00002.yaml")); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", ".")
	git(t, root, "commit", "-m", "symlinked manifest")
	head := gitOutput(t, root, "rev-parse", "HEAD")

	code, working, stderr := runCLI(nil, "--output", "json", "change-set", "show", "--change-set", changeSetID)
	if code == 0 || stderr != "" {
		t.Fatalf("working-tree read of a symlinked store entry = code %d stdout %s stderr %s", code, working, stderr)
	}
	codeAt, atHead, stderr := runCLI(nil, "--output", "json", "change-set", "show", "--change-set", changeSetID, "--at", head)
	if codeAt != code || stderr != "" || atHead != working {
		t.Fatalf("show --at refused the symlink differently:\ncode %d %s\n---\ncode %d %s", codeAt, atHead, code, working)
	}
	if !strings.Contains(atHead, "storage.unexpected_file") {
		t.Fatalf("show --at did not name the symlinked entry: %s", atHead)
	}
}

// newCommittedChangeSet commits change set CS-00001 with one requirement on
// branch cs/00001-show-at, cut from main. It returns the project root, the
// change-set ID, and the branch tip.
func newCommittedChangeSet(t *testing.T) (string, string, string) {
	t.Helper()
	root := newFixtureProject(t)
	t.Chdir(root)
	git(t, root, "switch", "-c", "cs/00001-show-at")
	code, stdout, stderr := runCLI(nil, "--output", "json", "change-set", "create", "--intent", "Read a manifest at a commit", "--affected-scope", "cli", "--implementation-required", "true", "--created", "2026-09-29T10:00:00Z")
	assertSuccess(t, code, stdout, stderr)
	changeSetID := jsonString(t, stdout, "data", "change_set", "id")
	code, stdout, stderr = runCLI(nil, "--output", "json", "requirement", "add", "--change-set", changeSetID, "--id", "REQ-CLI-00001", "--type", "ubiquitous", "--text", "The CLI shall read a manifest at a named commit.", "--scope", "cli", "--verification-mode", "isolated-interface", "--provenance", "user-authored", "--created", "2026-09-29T10:01:00Z")
	assertSuccess(t, code, stdout, stderr)
	git(t, root, "add", ".")
	git(t, root, "commit", "-m", "change set")
	return root, changeSetID, gitOutput(t, root, "rev-parse", "HEAD")
}

func assertFailureCode(t *testing.T, code int, stdout, stderr string, wantStatus int, wantCode string) {
	t.Helper()
	if code != wantStatus || stderr != "" || jsonString(t, stdout, "error", "code") != wantCode {
		t.Fatalf("result = code %d stdout %s stderr %s, want status %d and %s", code, stdout, stderr, wantStatus, wantCode)
	}
}
