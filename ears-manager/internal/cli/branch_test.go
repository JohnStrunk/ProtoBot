package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/redhat-et/protobot/ears-manager/internal/records"
	"github.com/redhat-et/protobot/ears-manager/internal/storage"
)

func TestChangeSetCreateCutsTheBranchFromTheDefaultHead(t *testing.T) {
	root := newFixtureProject(t)
	t.Chdir(root)
	defaultHead := gitOutput(t, root, "rev-parse", "main")
	// HEAD is on another branch, one commit ahead of main. The new branch
	// starts at main, not at HEAD.
	git(t, root, "switch", "-c", "feature")
	if err := os.WriteFile(filepath.Join(root, "feature.txt"), []byte("feature work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commitAll(t, root, "feature work")
	feature := gitOutput(t, root, "rev-parse", "HEAD")

	code, stdout, stderr := runCLI(nil, "--output", "json", "change-set", "create", "--intent", "Add the initial Sketch", "--implementation-required", "false", "--implementation-rationale", "The Sketch changes no interface yet.", "--created", "2026-09-30T09:00:00Z")
	assertSuccess(t, code, stdout, stderr)
	want := map[string]string{
		"id":            "CS-00001",
		"base_commit":   defaultHead,
		"branch":        "cs/00001-add-the-initial-sketch",
		"manifest_path": ".protobot/change-sets/cs-00001.yaml",
	}
	for field, value := range want {
		if got := jsonString(t, stdout, "data", "change_set", field); got != value {
			t.Fatalf("change_set.%s = %q, want %q: %s", field, got, value, stdout)
		}
	}
	if paths := jsonArray(t, stdout, "mutation", "paths"); len(paths) != 2 || paths[0] != ".protobot/change-sets/cs-00001.yaml" || paths[1] != ".protobot/project.yaml" {
		t.Fatalf("mutation paths = %s", stdout)
	}
	if head := gitOutput(t, root, "symbolic-ref", "--short", "HEAD"); head != want["branch"] {
		t.Fatalf("HEAD is on %s, want %s", head, want["branch"])
	}
	if tip := gitOutput(t, root, "rev-parse", want["branch"]); tip != defaultHead {
		t.Fatalf("the branch starts at %s, want the default head %s", tip, defaultHead)
	}
	if tip := gitOutput(t, root, "rev-parse", "feature"); tip != feature {
		t.Fatalf("branch feature moved to %s", tip)
	}
	if _, err := os.Stat(filepath.Join(root, "feature.txt")); !os.IsNotExist(err) {
		t.Fatalf("the working tree still holds the file of branch feature: %v", err)
	}
	code, stdout, stderr = runCLI(nil, "--output", "json", "change-set", "show", "--change-set", "CS-00001")
	assertSuccess(t, code, stdout, stderr)
	if jsonString(t, stdout, "data", "change_set", "base_commit") != defaultHead || jsonString(t, stdout, "data", "change_set", "impact_assessment_base_commit") != defaultHead {
		t.Fatalf("the manifest does not record the default head: %s", stdout)
	}
	code, stdout, stderr = runCLI(nil, "--output", "json", "check", "--change-set", "CS-00001")
	assertSuccess(t, code, stdout, stderr)
}

// TestChangeSetCreateOnTheInitializationBranch follows steps 1 and 2 of the
// Source Control Manager golden fixture: CS-00001 on the initialization
// branch that exists, then CS-00002 on a new branch from the merge.
func TestChangeSetCreateOnTheInitializationBranch(t *testing.T) {
	root := newInitializationBranch(t)
	t.Chdir(root)
	m0 := gitOutput(t, root, "rev-parse", "main")

	code, stdout, stderr := runCLI(nil, "--output", "json", "change-set", "create", "--intent", "Initialize the fixture project", "--implementation-required", "false", "--implementation-rationale", "Project initialization only.", "--created", "2026-09-21T09:00:00Z")
	assertSuccess(t, code, stdout, stderr)
	if jsonString(t, stdout, "data", "change_set", "id") != "CS-00001" || jsonString(t, stdout, "data", "change_set", "base_commit") != m0 || jsonString(t, stdout, "data", "change_set", "branch") != "cs/00001-project-init" {
		t.Fatalf("1-change-set-create = %s", stdout)
	}
	assertLocalBranches(t, root, "cs/00001-project-init", "main")
	if head := gitOutput(t, root, "symbolic-ref", "--short", "HEAD"); head != "cs/00001-project-init" {
		t.Fatalf("HEAD is on %s", head)
	}
	commitAll(t, root, "spec(CS-00001): Initialize the fixture project")

	// The host merges the branch; HEAD stays on the initialization branch.
	mergeIntoMain(t, root, "cs/00001-project-init")
	m1 := gitOutput(t, root, "rev-parse", "main")
	git(t, root, "switch", "cs/00001-project-init")

	code, stdout, stderr = runCLI(nil, "--output", "json", "change-set", "create", "--intent", "Add the initial Sketch", "--implementation-required", "false", "--implementation-rationale", "The Sketch changes no interface yet.", "--created", "2026-09-21T09:10:00Z")
	assertSuccess(t, code, stdout, stderr)
	if jsonString(t, stdout, "data", "change_set", "id") != "CS-00002" || jsonString(t, stdout, "data", "change_set", "base_commit") != m1 || jsonString(t, stdout, "data", "change_set", "branch") != "cs/00002-add-the-initial-sketch" {
		t.Fatalf("2-change-set-create = %s", stdout)
	}
	if head := gitOutput(t, root, "symbolic-ref", "--short", "HEAD"); head != "cs/00002-add-the-initial-sketch" {
		t.Fatalf("HEAD is on %s", head)
	}
	if tip := gitOutput(t, root, "rev-parse", "cs/00002-add-the-initial-sketch"); tip != m1 {
		t.Fatalf("the branch starts at %s, want the merge %s", tip, m1)
	}
	assertLocalBranches(t, root, "cs/00001-project-init", "cs/00002-add-the-initial-sketch", "main")
}

func TestChangeSetCreateOnTheInitializationBranchRefusesAMovedDefaultBranch(t *testing.T) {
	root := newInitializationBranch(t)
	t.Chdir(root)
	// main gains a commit after the initialization branch was cut from it.
	later := gitOutput(t, root, "commit-tree", "main^{tree}", "-p", "main", "-m", "later default-branch change")
	git(t, root, "update-ref", "refs/heads/main", later)
	before := repositoryState(t, root)

	code, stdout, stderr := runCLI(nil, "--output", "json", "change-set", "create", "--intent", "Initialize the fixture project", "--implementation-required", "false", "--implementation-rationale", "Project initialization only.", "--created", "2026-09-21T09:00:00Z")
	assertRefusedCreate(t, code, stdout, stderr, 5, "change_set.base_mismatch")
	if after := repositoryState(t, root); after != before {
		t.Fatalf("the refused create changed the repository:\n%s\n---\n%s", before, after)
	}
}

func TestChangeSetCreateRefusesAnUnsafeRepositoryState(t *testing.T) {
	for _, test := range []struct {
		name     string
		setup    func(t *testing.T) string
		wantExit int
		wantCode string
	}{
		{
			name: "detached HEAD",
			setup: func(t *testing.T) string {
				root := newFixtureProject(t)
				git(t, root, "switch", "--detach", "main")
				return root
			},
			wantExit: 5,
			wantCode: "change_set.unexpected_branch",
		},
		{
			name: "project not on the default branch",
			setup: func(t *testing.T) string {
				root := newInitializationBranch(t)
				git(t, root, "switch", "-c", "elsewhere")
				return root
			},
			wantExit: 5,
			wantCode: "change_set.unexpected_branch",
		},
		{
			name: "initialization change set not merged",
			setup: func(t *testing.T) string {
				root := newInitializationBranch(t)
				t.Chdir(root)
				code, stdout, stderr := runCLI(nil, "--output", "json", "change-set", "create", "--intent", "Initialize the fixture project", "--implementation-required", "false", "--implementation-rationale", "Project initialization only.", "--created", "2026-09-21T09:00:00Z")
				assertSuccess(t, code, stdout, stderr)
				commitAll(t, root, "spec(CS-00001): Initialize the fixture project")
				return root
			},
			wantExit: 5,
			wantCode: "change_set.unexpected_branch",
		},
		{
			name: "uncommitted change to a tracked file",
			setup: func(t *testing.T) string {
				root := newFixtureProject(t)
				if err := os.WriteFile(filepath.Join(root, "docs", "architecture.md"), []byte("# Edited Architecture\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				return root
			},
			wantExit: 5,
			wantCode: "change_set.uncommitted_changes",
		},
		{
			name: "staged change",
			setup: func(t *testing.T) string {
				root := newFixtureProject(t)
				if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("staged\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				git(t, root, "add", "notes.txt")
				return root
			},
			wantExit: 5,
			wantCode: "change_set.uncommitted_changes",
		},
		{
			name: "untracked file that the default head would overwrite",
			setup: func(t *testing.T) string {
				root := newFixtureProject(t)
				git(t, root, "switch", "-c", "without-architecture")
				git(t, root, "rm", "-q", "docs/architecture.md")
				git(t, root, "commit", "-q", "-m", "remove the Architecture")
				if err := os.WriteFile(filepath.Join(root, "docs", "architecture.md"), []byte("# Untracked Architecture\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				return root
			},
			wantExit: 5,
			wantCode: "change_set.uncommitted_changes",
		},
		{
			name: "unresolved local default branch",
			setup: func(t *testing.T) string {
				root := newFixtureProject(t)
				git(t, root, "branch", "-m", "main", "trunk")
				return root
			},
			wantExit: 3,
			wantCode: "project.default_branch_unresolved",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := test.setup(t)
			t.Chdir(root)
			before := repositoryState(t, root)
			code, stdout, stderr := runCLI(nil, "--output", "json", "change-set", "create", "--intent", "Another change", "--implementation-required", "true", "--created", "2026-09-30T10:00:00Z")
			assertRefusedCreate(t, code, stdout, stderr, test.wantExit, test.wantCode)
			if after := repositoryState(t, root); after != before {
				t.Fatalf("the refused create changed the repository:\n%s\n---\n%s", before, after)
			}
		})
	}
}

// TestChangeSetCreateAllocatesAfterEveryKnownChangeSet checks the sources of
// the next number: the default-branch tree, and the change-set branches,
// local and of the canonical remote. A fork's branch does not count.
func TestChangeSetCreateAllocatesAfterEveryKnownChangeSet(t *testing.T) {
	root := newFixtureProject(t)
	t.Chdir(root)
	beforeMerge := gitOutput(t, root, "rev-parse", "main")
	code, stdout, stderr := runCLI(nil, "--output", "json", "change-set", "create", "--intent", "First change", "--implementation-required", "true", "--created", "2026-09-30T11:00:00Z")
	assertSuccess(t, code, stdout, stderr)
	commitAll(t, root, "spec(CS-00001): first change")
	mergeIntoMain(t, root, "cs/00001-first-change")
	git(t, root, "branch", "-D", "cs/00001-first-change")

	// HEAD holds no manifest, and no branch names CS-00001; main does.
	git(t, root, "switch", "-c", "side", beforeMerge)
	code, stdout, stderr = runCLI(nil, "--output", "json", "change-set", "create", "--intent", "Second change", "--implementation-required", "true", "--created", "2026-09-30T11:01:00Z")
	assertSuccess(t, code, stdout, stderr)
	if jsonString(t, stdout, "data", "change_set", "id") != "CS-00002" {
		t.Fatalf("create after a merged CS-00001 = %s", stdout)
	}
	commitAll(t, root, "spec(CS-00002): second change")

	head := gitOutput(t, root, "rev-parse", "HEAD")
	git(t, root, "branch", "cs/00004-local", head)
	git(t, root, "remote", "add", "upstream", "https://example.invalid/fixture.git")
	git(t, root, "update-ref", "refs/remotes/upstream/cs/00007-remote", head)
	git(t, root, "remote", "add", "origin", "https://example.invalid/fork.git")
	git(t, root, "update-ref", "refs/remotes/origin/cs/00009-fork", head)
	code, stdout, stderr = runCLI(nil, "--output", "json", "change-set", "create", "--intent", "Third change", "--implementation-required", "true", "--created", "2026-09-30T11:02:00Z")
	assertSuccess(t, code, stdout, stderr)
	if jsonString(t, stdout, "data", "change_set", "id") != "CS-00008" || jsonString(t, stdout, "data", "change_set", "branch") != "cs/00008-third-change" {
		t.Fatalf("create after branches up to CS-00007 = %s", stdout)
	}
}

func TestChangeSetCreateRefusesAnExistingBranch(t *testing.T) {
	root := newFixtureProject(t)
	head := gitOutput(t, root, "rev-parse", "main")
	git(t, root, "remote", "add", "upstream", "https://example.invalid/fixture.git")
	repository := loadFixtureRepository(t, root)
	branch := changeSetBranch{id: "CS-00002", name: "cs/00002-taken", base: head, original: "main", cut: true}
	for _, test := range []struct {
		ref, where string
	}{
		{ref: "refs/heads/cs/00002-taken", where: "locally"},
		{ref: "refs/remotes/upstream/cs/00002-taken", where: "on the canonical remote"},
	} {
		git(t, root, "update-ref", test.ref, head)
		before := repositoryState(t, root)
		failure := cutChangeSetBranch(root, repository, branch)
		if failure == nil || failure.Code != "change_set.branch_exists" || failure.ExitCode != 5 || failure.Mutation != "none" || !strings.HasSuffix(failure.Message, test.where+".") {
			t.Fatalf("cut beside %s = %#v", test.ref, failure)
		}
		if after := repositoryState(t, root); after != before {
			t.Fatalf("the refused cut changed the repository:\n%s\n---\n%s", before, after)
		}
		git(t, root, "update-ref", "-d", test.ref)
	}
}

// TestChangeSetCreateRestoresTheBranchAfterAFailedWrite makes the write on
// the new branch fail: the default-branch head holds a record that its store
// digest does not cover. The original branch named -f is one that git branch
// refuses and git switch would read as an option.
func TestChangeSetCreateRestoresTheBranchAfterAFailedWrite(t *testing.T) {
	for _, original := range []string{"valid", "-f"} {
		t.Run(original, func(t *testing.T) {
			root := newFixtureProject(t)
			t.Chdir(root)
			valid := gitOutput(t, root, "rev-parse", "HEAD")
			if err := os.WriteFile(filepath.Join(root, ".protobot", "requirements", "REQ-FIX-00001.yaml"), []byte("id: REQ-FIX-00001\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			commitAll(t, root, "a record outside ears-manager")
			git(t, root, "update-ref", "refs/heads/"+original, valid)
			git(t, root, "symbolic-ref", "HEAD", "refs/heads/"+original)
			git(t, root, "reset", "-q", "--hard")
			before := repositoryState(t, root)

			code, stdout, stderr := runCLI(nil, "--output", "json", "change-set", "create", "--intent", "Another change", "--implementation-required", "true", "--created", "2026-09-30T12:00:00Z")
			assertRefusedCreate(t, code, stdout, stderr, 4, "validation.failed")
			if after := repositoryState(t, root); after != before {
				t.Fatalf("the failed create left a change:\n%s\n---\n%s", before, after)
			}
			// The write failed after the cut, not before it.
			if reflog := gitOutput(t, root, "reflog", "--format=%gs", "HEAD"); !strings.Contains(reflog, "to cs/00001-another-change") {
				t.Fatalf("the create did not check out the new branch before it failed:\n%s", reflog)
			}
		})
	}
}

// TestChangeSetRollbackKeepsTheBranchWhenTheSwitchBackFails removes the
// original branch, so the switch back fails. The rollback must not delete the
// branch that is still checked out.
func TestChangeSetRollbackKeepsTheBranchWhenTheSwitchBackFails(t *testing.T) {
	root := newFixtureProject(t)
	base := gitOutput(t, root, "rev-parse", "main")
	git(t, root, "switch", "-q", "-c", "cs/00001-cut")
	git(t, root, "branch", "-q", "-D", "main")
	branch := changeSetBranch{id: "CS-00001", name: "cs/00001-cut", base: base, original: "main", cut: true}

	failure := restoreOriginalBranch(root, branch, validationFailure("validation.failed", "The specification is not valid.", nil))
	if failure == nil || failure.Code != "git.write_unknown" || failure.ExitCode != 6 || failure.Mutation != "unknown" {
		t.Fatalf("rollback after a failed switch back = %#v", failure)
	}
	if head := gitOutput(t, root, "symbolic-ref", "--short", "HEAD"); head != branch.name {
		t.Fatalf("HEAD is on %s, want %s", head, branch.name)
	}
	if tip := gitOutput(t, root, "rev-parse", "refs/heads/"+branch.name); tip != base {
		t.Fatalf("the checked-out branch is at %s, want %s", tip, base)
	}
}

// TestChangeSetCreateIgnoresTheGitVariablesOfTheCaller points GIT_DIR,
// GIT_WORK_TREE, and GIT_INDEX_FILE at another repository. A caller-supplied
// path never selects the project, so the branch is cut in the project and the
// other repository stays as it was.
func TestChangeSetCreateIgnoresTheGitVariablesOfTheCaller(t *testing.T) {
	root := newFixtureProject(t)
	other := newFixtureProject(t)
	before := repositoryState(t, other)
	t.Chdir(root)
	redirect := map[string]string{
		"GIT_DIR":        filepath.Join(other, ".git"),
		"GIT_WORK_TREE":  other,
		"GIT_INDEX_FILE": filepath.Join(other, ".git", "index"),
	}
	for name, value := range redirect {
		t.Setenv(name, value)
	}

	code, stdout, stderr := runCLI(nil, "--output", "json", "change-set", "create", "--intent", "Ignore the caller", "--implementation-required", "true", "--created", "2026-09-30T14:00:00Z")
	for name := range redirect {
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
	assertSuccess(t, code, stdout, stderr)
	if head := gitOutput(t, root, "symbolic-ref", "--short", "HEAD"); head != "cs/00001-ignore-the-caller" {
		t.Fatalf("HEAD of the project is on %s", head)
	}
	if _, err := os.Stat(filepath.Join(root, ".protobot", "change-sets", "cs-00001.yaml")); err != nil {
		t.Fatalf("the manifest is not in the project: %v", err)
	}
	if after := repositoryState(t, other); after != before {
		t.Fatalf("the create changed the other repository:\n%s\n---\n%s", before, after)
	}
}

// TestChangeSetCreateRunsNoRepositoryProgram plants hooks and an fsmonitor
// program that write a marker file, as scm-7 does for the Source Control
// Manager, and cuts a branch.
func TestChangeSetCreateRunsNoRepositoryProgram(t *testing.T) {
	root := newFixtureProject(t)
	t.Chdir(root)
	markers := t.TempDir()
	programs := t.TempDir()
	for _, hook := range []string{"post-checkout", "reference-transaction", "post-index-change"} {
		script := "#!/bin/sh\ntouch '" + filepath.Join(markers, hook) + "'\n"
		if err := os.WriteFile(filepath.Join(root, ".git", "hooks", hook), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	fsmonitor := filepath.Join(programs, "fsmonitor")
	if err := os.WriteFile(fsmonitor, []byte("#!/bin/sh\ntouch '"+filepath.Join(markers, "fsmonitor")+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, root, "config", "core.fsmonitor", fsmonitor)

	code, stdout, stderr := runCLI(nil, "--output", "json", "change-set", "create", "--intent", "No repository program", "--implementation-required", "true", "--created", "2026-09-30T13:00:00Z")
	assertSuccess(t, code, stdout, stderr)
	if entries, err := os.ReadDir(markers); err != nil || len(entries) != 0 {
		t.Fatalf("a repository program ran: %v %v", entries, err)
	}
	// The planted programs work: a plain switch runs the post-checkout hook.
	git(t, root, "switch", "main")
	if _, err := os.Stat(filepath.Join(markers, "post-checkout")); err != nil {
		t.Fatalf("the planted post-checkout hook did not run for a plain switch: %v", err)
	}
}

func TestChangeSetSlug(t *testing.T) {
	for _, test := range []struct{ intent, want string }{
		{"Add the initial Sketch", "add-the-initial-sketch"},
		{"  Fix: the CLI's --help output!  ", "fix-the-cli-s-help-output"},
		{"Ünïcödé only", "n-c-d-only"},
		{"!!!", "change-set"},
		{"", "change-set"},
		{"Define the command line interface for requirement exports", "define-the-command-line-interface-for"},
		{strings.Repeat("a", 45), strings.Repeat("a", 40)},
		{strings.Repeat("a", 40) + " b", strings.Repeat("a", 40)},
		{strings.Repeat("a", 39) + " b", strings.Repeat("a", 39)},
	} {
		if got := changeSetSlug(test.intent); got != test.want {
			t.Errorf("changeSetSlug(%q) = %q, want %q", test.intent, got, test.want)
		}
	}
}

// newInitializationBranch returns a project whose default branch holds no
// .protobot/, checked out on cs/00001-project-init with the uncommitted
// project configuration: the state after branch_init and project init.
func newInitializationBranch(t *testing.T) string {
	t.Helper()
	root := newFixtureProject(t)
	git(t, root, "rm", "-r", "-q", "--cached", ".protobot")
	git(t, root, "commit", "-q", "--amend", "-m", "Initial commit")
	git(t, root, "switch", "-q", "-c", "cs/00001-project-init")
	return root
}

// repositoryState captures HEAD, the local and remote-tracking branches, the
// index and working-tree status, and the project configuration.
func repositoryState(t *testing.T, root string) string {
	t.Helper()
	head := gitOutput(t, root, "rev-parse", "--symbolic-full-name", "HEAD")
	refs := gitOutput(t, root, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads/", "refs/remotes/")
	status := gitOutput(t, root, "status", "--porcelain=v1", "--untracked-files=all")
	config, err := os.ReadFile(filepath.Join(root, ".protobot", "project.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join([]string{head, refs, status, string(config)}, "\n--\n")
}

func assertRefusedCreate(t *testing.T, code int, stdout, stderr string, wantExit int, wantCode string) {
	t.Helper()
	if code != wantExit || stderr != "" || jsonString(t, stdout, "error", "code") != wantCode || jsonString(t, stdout, "error", "mutation") != "none" {
		t.Fatalf("create = code %d stdout %s stderr %s, want status %d and %s", code, stdout, stderr, wantExit, wantCode)
	}
}

func assertLocalBranches(t *testing.T, root string, want ...string) {
	t.Helper()
	got := strings.Fields(gitOutput(t, root, "for-each-ref", "--format=%(refname:short)", "refs/heads/"))
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("local branches %v, want %v", got, want)
	}
}

func loadFixtureRepository(t *testing.T, root string) records.RepositoryConfig {
	t.Helper()
	var config records.ProjectConfig
	if err := storage.ReadFile(filepath.Join(root, ".protobot", "project.yaml"), &config); err != nil {
		t.Fatal(err)
	}
	return config.Repository
}
