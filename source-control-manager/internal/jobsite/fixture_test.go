package jobsite

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
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

	for _, workerPath := range []string{result.WorkerA.Path, result.WorkerB.Path} {
		wRepo, err := openGit(workerPath)
		if err != nil {
			t.Fatal(err)
		}
		res, _ := wRepo.run("fetch", result.Integration.Path)
		if res.OK() {
			t.Fatalf("worker repo at %s unexpectedly succeeded fetching integration repo", workerPath)
		}
		wRepo.Close()
	}

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

	auditDir := filepath.Join(out, auditDirName)
	acceptedAuditPath := filepath.Join(auditDir, "patch-accepted.json")
	acceptedData, err := os.ReadFile(acceptedAuditPath)
	if err != nil {
		t.Fatalf("failed to read patch-accepted.json: %v", err)
	}
	var acceptedAudit AuditRecord
	if err := json.Unmarshal(acceptedData, &acceptedAudit); err != nil {
		t.Fatalf("failed to unmarshal patch-accepted.json: %v", err)
	}
	if acceptedAudit.EventType != EventPatch || acceptedAudit.Decision != DecisionAccept || acceptedAudit.ResultingCommit != accepted.Commit {
		t.Fatalf("accepted audit record mismatch: %#v", acceptedAudit)
	}

	// Accepted create of a new role-allowlisted file
	newFilePath := "tests/canonical/new_test.go"
	acceptedCreate, err := applyRolePatch(t, result, RoleWorkerA, []Operation{{
		Path:    newFilePath,
		Action:  ActionCreate,
		Mode:    ModeFile,
		Content: []byte("package canonical\n"),
	}})
	if err != nil {
		t.Fatalf("accepted create rejected: %v", err)
	}
	if acceptedCreate.Commit == "" {
		t.Fatal("accepted create did not create a commit")
	}
	assertWorkingTree(t, result.Integration.Path, true, newFilePath)

	// Accepted create of an empty file
	emptyFilePath := "tests/canonical/empty_test.go"
	acceptedEmptyCreate, err := applyRolePatch(t, result, RoleWorkerA, []Operation{{
		Path:    emptyFilePath,
		Action:  ActionCreate,
		Mode:    ModeFile,
		Content: []byte{},
	}})
	if err != nil {
		t.Fatalf("accepted empty create rejected: %v", err)
	}
	if acceptedEmptyCreate.Commit == "" {
		t.Fatal("accepted empty create did not create a commit")
	}
	assertWorkingTree(t, result.Integration.Path, true, emptyFilePath)

	// Accepted update of an empty file
	acceptedEmptyUpdate, err := applyRolePatch(t, result, RoleWorkerA, []Operation{{
		Path:    emptyFilePath,
		Action:  ActionUpdate,
		Mode:    ModeFile,
		Content: nil,
	}})
	if err != nil {
		t.Fatalf("accepted empty update rejected: %v", err)
	}
	if acceptedEmptyUpdate.Commit == "" {
		t.Fatal("accepted empty update did not create a commit")
	}

	// Accepted create with mode 100755
	execFilePath := "tests/canonical/exec_test.sh"
	acceptedExec, err := applyRolePatch(t, result, RoleWorkerA, []Operation{{
		Path:    execFilePath,
		Action:  ActionCreate,
		Mode:    ModeExec,
		Content: []byte("#!/bin/sh\nexit 0\n"),
	}})
	if err != nil {
		t.Fatalf("accepted mode 100755 rejected: %v", err)
	}
	if acceptedExec.Commit == "" {
		t.Fatal("accepted mode 100755 did not create a commit")
	}
	assertWorkingTree(t, result.Integration.Path, true, execFilePath)

	// Accepted delete
	acceptedDelete, err := applyRolePatch(t, result, RoleWorkerA, []Operation{{
		Path:   newFilePath,
		Action: ActionDelete,
	}})
	if err != nil {
		t.Fatalf("accepted delete rejected: %v", err)
	}
	if acceptedDelete.Commit == "" {
		t.Fatal("accepted delete did not create a commit")
	}
	assertWorkingTree(t, result.Integration.Path, false, newFilePath)

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
		{name: "nested .git create", role: RoleWorkerA, ops: []Operation{{Path: "tests/canonical/.git/config", Action: ActionCreate, Mode: ModeFile, Content: []byte("no\n")}}, code: CodePatchRejected},
		{name: "nested .git update", role: RoleWorkerA, ops: []Operation{{Path: "tests/canonical/sub/.git/hooks/pre-commit", Action: ActionCreate, Mode: ModeFile, Content: []byte("no\n")}}, code: CodePatchRejected},
		{name: "case-colliding create", role: RoleWorkerA, ops: []Operation{{Path: "tests/canonical/APP_TEST.GO", Action: ActionCreate, Mode: ModeFile, Content: []byte("no\n")}}, code: CodePatchRejected},
		{name: "worker-b prefix collision with existing file", role: RoleWorkerB, ops: []Operation{{Path: "src", Action: ActionCreate, Mode: ModeFile, Content: []byte("no\n")}}, code: CodePatchRejected},
		{name: "worker-a descendant collision with existing file", role: RoleWorkerA, ops: []Operation{{Path: "tests/canonical/app_test.go/nested.go", Action: ActionCreate, Mode: ModeFile, Content: []byte("no\n")}}, code: CodePatchRejected},
		{name: "bundle prefix collision between operations", role: RoleWorkerA, ops: []Operation{
			{Path: "tests/canonical/newdir", Action: ActionCreate, Mode: ModeFile, Content: []byte("a\n")},
			{Path: "tests/canonical/newdir/child.go", Action: ActionCreate, Mode: ModeFile, Content: []byte("b\n")},
		}, code: CodePatchRejected},
		{name: "nested .git with trailing dot", role: RoleWorkerA, ops: []Operation{{Path: "tests/canonical/.git./config", Action: ActionCreate, Mode: ModeFile, Content: []byte("no\n")}}, code: CodePatchRejected},
		{name: "nested .git with trailing space", role: RoleWorkerA, ops: []Operation{{Path: "tests/canonical/.git /config", Action: ActionCreate, Mode: ModeFile, Content: []byte("no\n")}}, code: CodePatchRejected},
		{name: "nested 8.3 gitdir", role: RoleWorkerA, ops: []Operation{{Path: "tests/canonical/GIT~1/config", Action: ActionCreate, Mode: ModeFile, Content: []byte("no\n")}}, code: CodePatchRejected},
		{name: "nested hfs dotgit", role: RoleWorkerA, ops: []Operation{{Path: "tests/canonical/.\u200cgit/config", Action: ActionCreate, Mode: ModeFile, Content: []byte("no\n")}}, code: CodePatchRejected},
		{name: "mixed", role: RoleWorkerA, ops: []Operation{
			{Path: "tests/canonical/valid_new_test.go", Action: ActionCreate, Mode: ModeFile, Content: []byte("ok\n")},
			{Path: currentImplPath, Action: ActionUpdate, Mode: ModeFile, Content: []byte("no\n")},
		}, code: CodePatchRejected},
	}
	before, err := integrationHead(result.Integration.Path)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := applyRolePatch(t, result, tc.role, tc.ops)
			if errorCode(err) != tc.code {
				t.Fatalf("err = %v, want %s", err, tc.code)
			}
			if res == nil || res.Decision.Decision != DecisionReject || res.Decision.RejectionReason == "" {
				t.Fatalf("expected reject decision on PatchResult, got: %#v", res)
			}
			rejData, err := os.ReadFile(filepath.Join(out, auditDirName, "patch-rejected.json"))
			if err != nil {
				t.Fatalf("failed to read patch-rejected.json: %v", err)
			}
			var rejAudit AuditRecord
			if err := json.Unmarshal(rejData, &rejAudit); err != nil {
				t.Fatalf("failed to unmarshal patch-rejected.json: %v", err)
			}
			if rejAudit.EventType != EventPatch || rejAudit.Decision != DecisionReject || rejAudit.RejectionReason == "" {
				t.Fatalf("patch-rejected.json content mismatch: %#v", rejAudit)
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

func TestExportUnsafeRejectsNestedGitBlob(t *testing.T) {
	for _, gitdir := range []string{
		"src/.git/config",
		"src/.git./config",
		"src/.git /config",
		"src/GIT~1/config",
		"src/.\u200cgit/config",
	} {
		t.Run(gitdir, func(t *testing.T) {
			sourceRoot := t.TempDir()
			source, err := BuildSourceFixture(sourceRoot)
			if err != nil {
				t.Fatal(err)
			}
			repo, err := openGit(sourceRoot)
			if err != nil {
				t.Fatal(err)
			}
			defer repo.Close()
			blobs, err := repo.listBlobs(source.SourceCommit)
			if err != nil {
				t.Fatal(err)
			}
			oid, err := repo.writeBlob([]byte("unsafe\n"))
			if err != nil {
				t.Fatal(err)
			}
			blobs = append(blobs, blobEntry{Path: gitdir, Mode: ModeFile, OID: oid})
			sort.Slice(blobs, func(i, j int) bool { return blobs[i].Path < blobs[j].Path })
			tree, err := repo.writeTree(blobs)
			if err != nil {
				t.Fatal(err)
			}
			commit, err := repo.commitTreeWithParent(tree, source.SourceCommit, "source with nested gitdir")
			if err != nil {
				t.Fatal(err)
			}

			out := t.TempDir()
			_, err = Export(ExportRequest{
				SourceRoot:   sourceRoot,
				SourceCommit: commit,
				Policy:       source.Policy,
				OutputDir:    out,
				WorkItem:     fixtureWorkItem,
			})
			if errorCode(err) != CodeExportUnsafe {
				t.Fatalf("path %s err = %v, want %s", gitdir, err, CodeExportUnsafe)
			}
		})
	}
}

func TestTreeNodeInsertRejectsFileDirectoryCollisions(t *testing.T) {
	node := newTreeNode()
	if err := node.insert("src/app.go", ModeFile, "oid1"); err != nil {
		t.Fatal(err)
	}
	if err := node.insert("src", ModeFile, "oid2"); err == nil {
		t.Fatal("expected error inserting file over directory")
	}

	node2 := newTreeNode()
	if err := node2.insert("src", ModeFile, "oid1"); err != nil {
		t.Fatal(err)
	}
	if err := node2.insert("src/app.go", ModeFile, "oid2"); err == nil {
		t.Fatal("expected error inserting directory over file")
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
	if strings.Contains(cfg, "protocol.file.allow=always") {
		t.Fatalf("config leaked protocol.file.allow=always grant:\n%s", cfg)
	}
	if !strings.Contains(cfg, "protocol.file.allow=never") {
		t.Fatalf("config missing protocol.file.allow=never:\n%s", cfg)
	}
	if !strings.Contains(cfg, "protocol.ext.allow=never") {
		t.Fatalf("config missing protocol.ext.allow=never:\n%s", cfg)
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

func TestApplyPatchEmptyOperationsDoesNotMoveIntegrationHEAD(t *testing.T) {
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

	before, err := integrationHead(result.Integration.Path)
	if err != nil {
		t.Fatal(err)
	}

	emptyBundle := PatchBundle{
		Version:         1,
		Role:            RoleWorkerA,
		WorkItem:        fixtureWorkItem,
		Cycle:           1,
		PolicyDigest:    result.Policy.Digest,
		SourceCommit:    result.SourceCommit,
		WorkerRoot:      result.WorkerA.RootCommit,
		IntegrationBase: before,
		Operations:      []Operation{},
	}
	digest, err := bundleDigest(emptyBundle)
	if err != nil {
		t.Fatal(err)
	}
	emptyBundle.BundleDigest = digest

	res, err := ApplyPatch(result.Integration, result.Policy, emptyBundle)
	if errorCode(err) != CodePatchRejected {
		t.Fatalf("err = %v, want %s", err, CodePatchRejected)
	}
	if res == nil || res.Decision.Decision != DecisionReject {
		t.Fatalf("expected reject decision on PatchResult, got: %#v", res)
	}
	if !strings.Contains(res.Decision.RejectionReason, "must contain at least one operation") {
		t.Fatalf("unexpected rejection reason: %q", res.Decision.RejectionReason)
	}

	after, err := integrationHead(result.Integration.Path)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("empty bundle moved integration HEAD: %s -> %s", before, after)
	}
}
