package jobsite

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkerProjectionExportAndNegativeIsolation(t *testing.T) {
	sourceRoot := t.TempDir()
	source, err := BuildSourceFixture(sourceRoot)
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	result, err := Export(ExportRequest{
		SourceRoot:   source.Root,
		SourceCommit: source.SourceCommit,
		Policy:       source.Policy,
		PolicyDigest: source.PolicyDigest,
		OutputDir:    out,
		WorkItem:     fixtureWorkItem,
		Cycle:        1,
	})
	if err != nil {
		t.Fatal(err)
	}

	assertContains(t, result.Visible[RoleWorkerA], currentTestPath, nestedSharedPath, sharedDocPath, fileOnlySharedPath)
	assertNotContains(t, result.Visible[RoleWorkerA], currentImplPath, unclassifiedPath, integrationOnlyPath, attestationPath)
	assertContains(t, result.Visible[RoleWorkerB], currentImplPath, nestedSharedPath, sharedDocPath)
	assertNotContains(t, result.Visible[RoleWorkerB], currentTestPath, unclassifiedPath, integrationOnlyPath)

	assertWorkingTree(t, result.WorkerA.Path, true, currentTestPath, nestedSharedPath)
	assertWorkingTree(t, result.WorkerA.Path, false, currentImplPath, unclassifiedPath, laterWorkItemPath, historicalImplPath)
	assertWorkingTree(t, result.WorkerB.Path, true, currentImplPath, nestedSharedPath)
	assertWorkingTree(t, result.WorkerB.Path, false, currentTestPath, unclassifiedPath, laterWorkItemPath, historicalImplPath)
	assertWorkingTree(t, result.Integration.Path, true, unclassifiedPath, currentImplPath, currentTestPath, nestedSharedPath)
	assertWorkingTree(t, result.Integration.Path, false, laterWorkItemPath)

	assertObject(t, result.WorkerA.Path, source.CurrentImplBlob, false)
	assertObject(t, result.WorkerA.Path, source.HistoricalImplBlob, false)
	assertObject(t, result.WorkerA.Path, source.LaterWorkItemBlob, false)
	assertObject(t, result.WorkerA.Path, source.UnclassifiedBlob, false)
	assertObject(t, result.WorkerA.Path, source.CurrentTestBlob, true)
	assertObject(t, result.WorkerA.Path, source.NestedSharedBlob, true)

	assertObject(t, result.WorkerB.Path, source.CurrentTestBlob, false)
	assertObject(t, result.WorkerB.Path, source.HistoricalImplBlob, false)
	assertObject(t, result.WorkerB.Path, source.LaterWorkItemBlob, false)
	assertObject(t, result.WorkerB.Path, source.UnclassifiedBlob, false)
	assertObject(t, result.WorkerB.Path, source.CurrentImplBlob, true)

	assertObject(t, result.Integration.Path, source.HistoricalImplBlob, true)
	assertObject(t, result.Integration.Path, source.UnclassifiedBlob, true)
	assertObject(t, result.Integration.Path, source.LaterWorkItemBlob, false)
	assertObject(t, result.WorkerA.Path, result.WorkerB.RootCommit, false)
	assertObject(t, result.WorkerB.Path, result.WorkerA.RootCommit, false)
	assertObject(t, result.WorkerA.Path, result.Integration.RootCommit, false)
	assertObject(t, result.WorkerB.Path, result.Integration.RootCommit, false)
	assertObject(t, result.Integration.Path, result.WorkerA.RootCommit, false)
	assertObject(t, result.Integration.Path, result.WorkerB.RootCommit, false)

	for _, repo := range []Repository{result.WorkerA, result.WorkerB, result.Integration} {
		assertIsolatedGit(t, repo.Path)
		assertNoCredential(t, repo.Path)
	}
	assertSyntheticRoot(t, result.WorkerA.Path, result.WorkerA.RootCommit)
	assertSyntheticRoot(t, result.WorkerB.Path, result.WorkerB.RootCommit)

	if result.Integration.RootCommit != source.SourceCommit {
		t.Fatalf("integration root %s, want source %s", result.Integration.RootCommit, source.SourceCommit)
	}
	if len(result.Audits) != 3 {
		t.Fatalf("audits = %d", len(result.Audits))
	}
	for _, audit := range result.Audits {
		if audit.Decision != DecisionAccept || audit.PolicyDigest != source.PolicyDigest {
			t.Fatalf("audit = %#v", audit)
		}
		if strings.Contains(audit.RejectionReason, credentialSentinel) {
			t.Fatalf("audit leaked credential: %#v", audit)
		}
	}

	head := result.Integration.RootCommit
	accepted, err := applyRolePatch(t, result, RoleWorkerA, []Operation{{
		Path:    currentTestPath,
		Action:  ActionUpdate,
		Mode:    ModeFile,
		Content: []byte("updated test\n"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Commit == "" || accepted.Commit == head {
		t.Fatal("accepted patch did not create an integration commit")
	}
	assertObject(t, result.Integration.Path, result.WorkerA.RootCommit, false)
	assertWorkingTree(t, result.Integration.Path, true, currentTestPath)

	afterAccept, err := openGit(result.Integration.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer afterAccept.Close()
	got, err := afterAccept.readFileAt("HEAD", currentTestPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "updated test\n" {
		t.Fatalf("integration test file = %q", got)
	}

	cases := []struct {
		name string
		role string
		ops  []Operation
		code string
	}{
		{name: "worker-a implementation", role: RoleWorkerA, ops: []Operation{{Path: currentImplPath, Action: ActionUpdate, Mode: ModeFile, Content: []byte("no\n")}}, code: CodePatchRejected},
		{name: "worker-b test", role: RoleWorkerB, ops: []Operation{{Path: currentTestPath, Action: ActionUpdate, Mode: ModeFile, Content: []byte("no\n")}}, code: CodePatchRejected},
		{name: "unclassified", role: RoleWorkerA, ops: []Operation{{Path: unclassifiedPath, Action: ActionUpdate, Mode: ModeFile, Content: []byte("no\n")}}, code: CodePatchRejected},
		{name: "shared", role: RoleWorkerB, ops: []Operation{{Path: sharedDocPath, Action: ActionUpdate, Mode: ModeFile, Content: []byte("no\n")}}, code: CodePatchRejected},
		{name: "traversal", role: RoleWorkerA, ops: []Operation{{Path: "../escape", Action: ActionCreate, Mode: ModeFile, Content: []byte("no\n")}}, code: CodePatchRejected},
		{name: "mixed", role: RoleWorkerA, ops: []Operation{
			{Path: "tests/canonical/new_test.go", Action: ActionCreate, Mode: ModeFile, Content: []byte("ok\n")},
			{Path: currentImplPath, Action: ActionUpdate, Mode: ModeFile, Content: []byte("no\n")},
		}, code: CodePatchRejected},
	}
	before, err := integrationHead(result.Integration.Path)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := applyRolePatch(t, result, tc.role, tc.ops)
			if errorCode(err) != tc.code {
				t.Fatalf("err = %v, want %s", err, tc.code)
			}
			head, err := integrationHead(result.Integration.Path)
			if err != nil {
				t.Fatal(err)
			}
			if head != before {
				t.Fatalf("rejected patch mutated integration: %s -> %s", before, head)
			}
		})
	}

	stale, err := NewPatchBundle(RoleWorkerA, fixtureWorkItem, 1, result.Policy.Digest, result.SourceCommit, result.WorkerA.RootCommit, "deadbeef", []Operation{{
		Path: currentTestPath, Action: ActionUpdate, Mode: ModeFile, Content: []byte("stale\n"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyPatch(result.Integration, result.Policy, stale); errorCode(err) != CodePatchStale {
		t.Fatalf("stale = %v", err)
	}
	tampered, err := NewPatchBundle(RoleWorkerA, fixtureWorkItem, 1, result.Policy.Digest, result.SourceCommit, result.WorkerA.RootCommit, before, []Operation{{
		Path: currentTestPath, Action: ActionUpdate, Mode: ModeFile, Content: []byte("tamper\n"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	tampered.BundleDigest = "sha256:" + strings.Repeat("0", 64)
	if _, err := ApplyPatch(result.Integration, result.Policy, tampered); errorCode(err) != CodePatchTampered {
		t.Fatalf("tampered = %v", err)
	}
	head, err = integrationHead(result.Integration.Path)
	if err != nil {
		t.Fatal(err)
	}
	if head != before {
		t.Fatal("tampered or stale patch mutated integration")
	}
}

func TestExportFailsClosedBeforeCreatingRepositories(t *testing.T) {
	sourceRoot := t.TempDir()
	source, err := BuildSourceFixture(sourceRoot)
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	_, err = Export(ExportRequest{
		SourceRoot:   source.Root,
		SourceCommit: source.SourceCommit,
		Policy:       []byte("version: 2\npaths: []\n"),
		OutputDir:    out,
		WorkItem:     fixtureWorkItem,
	})
	if errorCode(err) != CodePolicyUnsupported {
		t.Fatalf("err = %v", err)
	}
	for _, name := range []string{workerADir, workerBDir, integrationDir} {
		if _, err := os.Lstat(filepath.Join(out, name)); !os.IsNotExist(err) {
			t.Fatalf("%s exists after failed export: %v", name, err)
		}
	}
}

func TestExportDigestMismatchFailsClosed(t *testing.T) {
	sourceRoot := t.TempDir()
	source, err := BuildSourceFixture(sourceRoot)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Export(ExportRequest{
		SourceRoot:   source.Root,
		SourceCommit: source.SourceCommit,
		Policy:       source.Policy,
		PolicyDigest: "sha256:" + strings.Repeat("0", 64),
		OutputDir:    t.TempDir(),
	})
	if errorCode(err) != CodePolicyDigest {
		t.Fatalf("err = %v", err)
	}
}

func applyRolePatch(t *testing.T, result *ExportResult, role string, ops []Operation) (*PatchResult, error) {
	t.Helper()
	root := result.WorkerA.RootCommit
	if role == RoleWorkerB {
		root = result.WorkerB.RootCommit
	}
	head, err := integrationHead(result.Integration.Path)
	if err != nil {
		return nil, err
	}
	bundle, err := NewPatchBundle(role, fixtureWorkItem, 1, result.Policy.Digest, result.SourceCommit, root, head, ops)
	if err != nil {
		return nil, err
	}
	return ApplyPatch(result.Integration, result.Policy, bundle)
}

func integrationHead(path string) (string, error) {
	repo, err := openGit(path)
	if err != nil {
		return "", err
	}
	defer repo.Close()
	return repo.head()
}

func assertContains(t *testing.T, got []string, want ...string) {
	t.Helper()
	set := map[string]bool{}
	for _, item := range got {
		set[item] = true
	}
	for _, item := range want {
		if !set[item] {
			t.Fatalf("missing %s in %v", item, got)
		}
	}
}

func assertNotContains(t *testing.T, got []string, banned ...string) {
	t.Helper()
	set := map[string]bool{}
	for _, item := range got {
		set[item] = true
	}
	for _, item := range banned {
		if set[item] {
			t.Fatalf("unexpected %s in %v", item, got)
		}
	}
}

func assertWorkingTree(t *testing.T, root string, want bool, paths ...string) {
	t.Helper()
	for _, projectPath := range paths {
		_, err := os.Lstat(filepath.Join(root, filepath.FromSlash(projectPath)))
		exists := err == nil
		if exists != want {
			t.Fatalf("%s exist=%v want %v: %v", projectPath, exists, want, err)
		}
	}
}

func assertObject(t *testing.T, repoPath, oid string, want bool) {
	t.Helper()
	repo, err := openGit(repoPath)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	got, err := repo.catExists(oid)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("object %s exist=%v want %v in %s", oid, got, want, repoPath)
	}
}

func assertIsolatedGit(t *testing.T, repoPath string) {
	t.Helper()
	repo, err := openGit(repoPath)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	remotes, err := repo.remotes()
	if err != nil {
		t.Fatal(err)
	}
	if len(remotes) != 0 {
		t.Fatalf("remotes = %v", remotes)
	}
	has, err := repo.hasAlternates()
	if err != nil {
		t.Fatal(err)
	}
	if has {
		t.Fatal("alternates present")
	}
	format, err := repo.objectFormat()
	if err != nil {
		t.Fatal(err)
	}
	if format != objectFormatSHA1 {
		t.Fatalf("object format = %s", format)
	}
	dir, err := repo.gitDir()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "logs")); err == nil {
		t.Fatal("reflogs present")
	}
}

func assertNoCredential(t *testing.T, repoPath string) {
	t.Helper()
	repo, err := openGit(repoPath)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	cfg, err := repo.configList()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(cfg, credentialSentinel) || strings.Contains(cfg, "example.invalid") {
		t.Fatalf("config leaked source remote or credential:\n%s", cfg)
	}
	if strings.Contains(cfg, "credential.helper") {
		t.Fatalf("config leaked credential helper:\n%s", cfg)
	}
}

func assertSyntheticRoot(t *testing.T, repoPath, commit string) {
	t.Helper()
	repo, err := openGit(repoPath)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	parents, err := repo.text("rev-list", "--parents", "-n", "1", commit)
	if err != nil {
		t.Fatal(err)
	}
	if fields := strings.Fields(parents); len(fields) != 1 || fields[0] != commit {
		t.Fatalf("synthetic root parents = %q", parents)
	}
	count, err := repo.text("rev-list", "--count", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if count != "1" {
		t.Fatalf("worker history length = %s", count)
	}
	branch, err := repo.text("symbolic-ref", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if branch != "refs/heads/"+branchMain {
		t.Fatalf("HEAD = %s", branch)
	}
}
