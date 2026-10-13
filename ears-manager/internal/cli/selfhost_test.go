package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
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
	source := selfHostingRoot(t)
	csRels := selfHostingChangeSetRelPaths(t, source)
	reqRels, ifaceRels := referencedRecordsForChangeSets(t, source, csRels)
	wantChangeSets := len(csRels)
	wantRequirements := len(reqRels)
	wantInterfaces := len(ifaceRels)
	if got := result.Data.RecordCounts; got.Requirements != wantRequirements || got.Interfaces != wantInterfaces || got.ChangeSets != wantChangeSets || got.Artifacts != 2 {
		t.Fatalf("seeded record counts = %#v, want %d requirement(s), %d interface(s), %d change set(s), and two artifacts", got, wantRequirements, wantInterfaces, wantChangeSets)
	}
}

func TestSelfHostingProposedChangeSetBehindDefaultBranchFailsCheck(t *testing.T) {
	root := newSelfHostingCheckout(t)
	t.Chdir(root)
	baseCommit := gitOutput(t, root, "rev-parse", "refs/heads/main")
	branch := gitOutput(t, root, "symbolic-ref", "--short", "HEAD")

	git(t, root, "checkout", "main")
	writeTestFile(t, root, "unrelated.txt", "default branch moved\n")
	commitAll(t, root, "unrelated default-branch change")
	defaultHead := gitOutput(t, root, "rev-parse", "HEAD")
	git(t, root, "checkout", branch)

	// CI fetches the default branch, detaches HEAD at the proposed commit,
	// and points the local default-branch ref at the fetched head.
	git(t, root, "checkout", "--detach", "HEAD")
	git(t, root, "update-ref", "refs/heads/main", defaultHead)

	code, stdout, stderr := runCLI(nil, "--output", "json", "check")
	if code != 5 || stderr != "" || jsonString(t, stdout, "error", "code") != "DEFAULT_MOVED" {
		t.Fatalf("stale proposed base = code %d stdout %s stderr %s", code, stdout, stderr)
	}
	if !hasDiagnostic(t, stdout, "DEFAULT_MOVED", "base_commit") {
		t.Fatalf("check omitted DEFAULT_MOVED on base_commit: %s", stdout)
	}
	if !strings.Contains(stdout, baseCommit) || !strings.Contains(stdout, defaultHead) {
		t.Fatalf("diagnostic did not name recorded base %s and default head %s: %s", baseCommit, defaultHead, stdout)
	}

	humanCode, _, humanStderr := runCLI(nil, "check")
	if humanCode != 5 || !strings.Contains(humanStderr, "error[DEFAULT_MOVED]:") {
		t.Fatalf("human check = exit %d stderr %q", humanCode, humanStderr)
	}

	git(t, root, "checkout", branch)
	git(t, root, "merge", "--no-ff", "--no-edit", "main")
	code, stdout, stderr = runCLI(nil, "--output", "json", "check")
	if code != 5 || stderr != "" || jsonString(t, stdout, "error", "code") != "BASE_COMMIT_STALE" {
		t.Fatalf("merged stale base = code %d stdout %s stderr %s", code, stdout, stderr)
	}
	if !hasDiagnostic(t, stdout, "BASE_COMMIT_STALE", "base_commit") {
		t.Fatalf("check omitted BASE_COMMIT_STALE on base_commit: %s", stdout)
	}
	if !strings.Contains(stdout, baseCommit) || !strings.Contains(stdout, defaultHead) {
		t.Fatalf("BASE_COMMIT_STALE did not name recorded base %s and default head %s: %s", baseCommit, defaultHead, stdout)
	}

	// Additional proposed change sets copied from the seed would also be
	// stale after the merge. Pin them so this test can exercise CS-00001's
	// refresh workflow in isolation.
	pinSelfHostingChangeSetsExcept(t, root, "CS-00001")

	code, stdout, stderr = runCLI(nil, "--output", "json", "change-set", "update", "--change-set", "CS-00001", "--base-commit", defaultHead)
	assertSuccess(t, code, stdout, stderr)
	code, stdout, stderr = runCLI(nil, "--output", "json", "check")
	if code != 5 || stderr != "" || jsonString(t, stdout, "error", "code") != "change_set.assessment_incomplete" {
		t.Fatalf("base-only update = code %d stdout %s stderr %s", code, stdout, stderr)
	}

	code, stdout, stderr = runCLI([]byte("[]"), "--output", "json", "change-set", "update", "--change-set", "CS-00001", "--impact-file", "-")
	assertSuccess(t, code, stdout, stderr)
	code, stdout, stderr = runCLI(nil, "--output", "json", "check")
	assertSuccess(t, code, stdout, stderr)
	code, stdout, stderr = runCLI(nil, "--output", "json", "check", "--change-set", "CS-00001")
	assertSuccess(t, code, stdout, stderr)
}

func TestSelfHostingProposedChangeSetBaseNotOnDefaultFailsCheck(t *testing.T) {
	root := newSelfHostingCheckout(t)
	t.Chdir(root)
	head := gitOutput(t, root, "rev-parse", "HEAD")
	defaultHead := gitOutput(t, root, "rev-parse", "refs/heads/main")
	path := filepath.Join(root, ".protobot", "change-sets", "cs-00001.yaml")
	var changeSet records.ChangeSet
	if err := storage.ReadFile(path, &changeSet); err != nil {
		t.Fatal(err)
	}
	changeSet.BaseCommit = head
	changeSet.ImpactAssessmentBaseCommit = head
	data, err := storage.Encode(changeSet)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	refreshProjectDigests(t, root)

	code, stdout, stderr := runCLI(nil, "--output", "json", "check")
	if code != 4 || stderr != "" || jsonString(t, stdout, "error", "code") != "BASE_NOT_ON_DEFAULT" {
		t.Fatalf("base not on default = code %d stdout %s stderr %s", code, stdout, stderr)
	}
	if !hasDiagnostic(t, stdout, "BASE_NOT_ON_DEFAULT", "base_commit") {
		t.Fatalf("check omitted BASE_NOT_ON_DEFAULT on base_commit: %s", stdout)
	}
	if !strings.Contains(stdout, head) || !strings.Contains(stdout, defaultHead) {
		t.Fatalf("diagnostic did not name recorded base %s and default head %s: %s", head, defaultHead, stdout)
	}
}

func TestSelfHostingApprovedChangeSetIgnoresStaleBase(t *testing.T) {
	root := newSelfHostingCheckout(t)
	t.Chdir(root)
	branch := gitOutput(t, root, "symbolic-ref", "--short", "HEAD")
	mergeIntoMain(t, root, branch)

	writeTestFile(t, root, "unrelated.txt", "default branch moved after approval\n")
	commitAll(t, root, "unrelated default-branch change")
	git(t, root, "checkout", "-b", "pr/unrelated")

	code, stdout, stderr := runCLI(nil, "--output", "json", "check")
	assertSuccess(t, code, stdout, stderr)
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
	// Store digests change when later proposed change sets are added to
	// the committed seed. Differing base commits also make store digests
	// differ: the committed seed's CS-00001 records base commits in
	// .protobot/change-sets/cs-00001.yaml while governed initialization
	// creates a temporary-repository base. Store digests include canonical
	// manifest contents, so they can differ even when the store contains
	// only the seed manifest. Governed initialization still has to reproduce
	// CS-00001 and the rest of the project configuration.
	gotProject = isolateSeedProject(gotProject)
	wantProject = isolateSeedProject(wantProject)
	if !reflect.DeepEqual(gotProject, wantProject) {
		t.Fatalf("project configuration from governed init differs:\ngot: %#v\nwant: %#v", gotProject, wantProject)
	}

	assertSeedChangeSetMatches(t,
		filepath.Join(root, ".protobot", "change-sets", "cs-00001.yaml"),
		filepath.Join(source, ".protobot", "change-sets", "cs-00001.yaml"),
		"change-set record from governed writes differs",
	)
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

func TestSelfHostingMultipleProposedChangeSets(t *testing.T) {
	source, extra := selfHostingSourceWithExtraChangeSet(t)
	root := newSelfHostingCheckoutFrom(t, source)
	t.Chdir(root)

	// Approved seed manifests remain on the fixture's default branch and
	// preserve approved historical status.
	code, stdout, stderr := runCLI(nil, "--output", "json", "change-set", "show", "--change-set", "CS-00001")
	assertSuccess(t, code, stdout, stderr)
	if status := jsonString(t, stdout, "data", "status"); status != "approved" {
		t.Fatalf("CS-00001 status = %q, want approved", status)
	}

	var extraCS records.ChangeSet
	if err := storage.ReadFile(filepath.Join(root, filepath.FromSlash(extra)), &extraCS); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = runCLI(nil, "--output", "json", "change-set", "show", "--change-set", extraCS.ID)
	assertSuccess(t, code, stdout, stderr)
	if status := jsonString(t, stdout, "data", "status"); status != "proposed" {
		t.Fatalf("%s status = %q, want proposed", extraCS.ID, status)
	}

	copied := selfHostingChangeSetRelPaths(t, root)
	if !containsString(copied, extra) {
		t.Fatalf("newSelfHostingCheckout omitted %s from %v", extra, copied)
	}
	if len(copied) < 2 {
		t.Fatalf("copied change-set manifests = %v, want at least two change sets", copied)
	}

	// Active proposals have base commits pinned to the fixture default branch head.
	base := gitOutput(t, root, "rev-parse", "refs/heads/main")
	if extraCS.BaseCommit != base || extraCS.ImpactAssessmentBaseCommit != base {
		t.Fatalf("%s base_commit=%q impact_assessment_base_commit=%q, want both %q", extra, extraCS.BaseCommit, extraCS.ImpactAssessmentBaseCommit, base)
	}

	code, stdout, stderr = runCLI(nil, "--output", "json", "check")
	assertSuccess(t, code, stdout, stderr)
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
	reqRels, ifaceRels := referencedRecordsForChangeSets(t, source, copied)
	if got := result.Data.RecordCounts; got.Requirements != len(reqRels) || got.Interfaces != len(ifaceRels) || got.ChangeSets != len(copied) || got.Artifacts != 2 {
		t.Fatalf("check record counts = %#v, want %d requirement(s), %d interface(s), %d change set(s), and two artifacts", got, len(reqRels), len(ifaceRels), len(copied))
	}

	assertSeedChangeSetMatches(t,
		filepath.Join(root, ".protobot", "change-sets", "cs-00001.yaml"),
		filepath.Join(selfHostingRoot(t), ".protobot", "change-sets", "cs-00001.yaml"),
		"CS-00001 from a multi-change-set checkout differs from the committed seed",
	)
}

func newSelfHostingCheckout(t *testing.T) string {
	t.Helper()
	return buildSelfHostingCheckout(t, selfHostingRoot(t), false)
}

func newSelfHostingCheckoutFrom(t *testing.T, source string) string {
	t.Helper()
	return buildSelfHostingCheckout(t, source, true)
}

func buildSelfHostingCheckout(t *testing.T, source string, splitApproved bool) string {
	t.Helper()
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

	allManifests := selfHostingChangeSetRelPaths(t, source)
	var approvedManifests, proposedManifests []string
	if splitApproved {
		for _, rel := range allManifests {
			if isChangeSetApprovedInSource(source, rel) {
				approvedManifests = append(approvedManifests, rel)
			} else {
				proposedManifests = append(proposedManifests, rel)
			}
		}
	} else {
		// The default fixture keeps CS-00001 and any extra change sets on the
		// proposed PR branch so lifecycle tests can exercise CS-00001's proposal workflow.
		proposedManifests = allManifests
	}

	// Write project and projection configs
	for _, rel := range []string{".protobot/project.yaml", ".protobot/projection.yaml"} {
		writeTestFile(t, root, rel, readTestFile(t, source, rel))
	}

	if len(approvedManifests) > 0 {
		appReqs, appIfaces := referencedRecordsForChangeSets(t, source, approvedManifests)
		for _, rel := range append(append(approvedManifests, appReqs...), appIfaces...) {
			writeTestFile(t, root, rel, readTestFile(t, source, rel))
		}
		refreshProjectDigests(t, root)
		git(t, root, "add", ".protobot")
		git(t, root, "commit", "-m", "approved specification records")
	}

	git(t, root, "checkout", "-b", "pr/self-hosting-seed")
	if len(proposedManifests) > 0 {
		propReqs, propIfaces := referencedRecordsForChangeSets(t, source, proposedManifests)
		for _, rel := range append(append(proposedManifests, propReqs...), propIfaces...) {
			writeTestFile(t, root, rel, readTestFile(t, source, rel))
		}
		pinSelfHostingSeedBase(t, root)
		git(t, root, "add", ".protobot")
		git(t, root, "commit", "-m", "propose self-hosting change sets")
	}
	return root
}

func isChangeSetApprovedInSource(source, relPath string) bool {
	if isChangeSetApprovedInTree(source, "refs/heads/main", relPath) {
		return true
	}
	if isChangeSetApprovedInTree(source, "refs/remotes/origin/main", relPath) {
		return true
	}
	return false
}

func referencedRecordsForChangeSets(t *testing.T, source string, changeSetRels []string) ([]string, []string) {
	t.Helper()
	seenReqs := make(map[string]bool)
	seenIfaces := make(map[string]bool)
	reqRels := make([]string, 0)
	ifaceRels := make([]string, 0)

	for _, rel := range changeSetRels {
		manifestPath := filepath.Join(source, filepath.FromSlash(rel))
		var cs records.ChangeSet
		if err := storage.ReadFile(manifestPath, &cs); err != nil {
			t.Fatal(err)
		}
		for _, op := range cs.Operations {
			if op.RequirementID != "" && !seenReqs[op.RequirementID] {
				seenReqs[op.RequirementID] = true
				filename, err := records.FilenameFor(records.RequirementStore, op.RequirementID)
				if err != nil {
					t.Fatal(err)
				}
				p := filepath.ToSlash(filepath.Join(".protobot", "requirements", filename))
				if _, err := os.Stat(filepath.Join(source, filepath.FromSlash(p))); err == nil {
					reqRels = append(reqRels, p)
				}
			}
		}
		for _, op := range cs.InterfaceOperations {
			if op.InterfaceID != "" && !seenIfaces[op.InterfaceID] {
				seenIfaces[op.InterfaceID] = true
				filename, err := records.FilenameFor(records.InterfaceStore, op.InterfaceID)
				if err != nil {
					t.Fatal(err)
				}
				p := filepath.ToSlash(filepath.Join(".protobot", "interfaces", filename))
				if _, err := os.Stat(filepath.Join(source, filepath.FromSlash(p))); err == nil {
					ifaceRels = append(ifaceRels, p)
				}
			}
		}
		for _, ifaceID := range cs.AffectedInterfaces {
			if ifaceID != "" && !seenIfaces[ifaceID] {
				seenIfaces[ifaceID] = true
				filename, err := records.FilenameFor(records.InterfaceStore, ifaceID)
				if err != nil {
					t.Fatal(err)
				}
				p := filepath.ToSlash(filepath.Join(".protobot", "interfaces", filename))
				if _, err := os.Stat(filepath.Join(source, filepath.FromSlash(p))); err == nil {
					ifaceRels = append(ifaceRels, p)
				}
			}
		}
	}
	sort.Strings(reqRels)
	sort.Strings(ifaceRels)
	return reqRels, ifaceRels
}

func assertSeedChangeSetMatches(t *testing.T, gotPath, wantPath, failureContext string) {
	t.Helper()
	var gotChangeSet, wantChangeSet records.ChangeSet
	if err := storage.ReadFile(gotPath, &gotChangeSet); err != nil {
		t.Fatal(err)
	}
	if err := storage.ReadFile(wantPath, &wantChangeSet); err != nil {
		t.Fatal(err)
	}
	gotChangeSet.BaseCommit, wantChangeSet.BaseCommit = "", ""
	gotChangeSet.ImpactAssessmentBaseCommit, wantChangeSet.ImpactAssessmentBaseCommit = "", ""
	if !reflect.DeepEqual(gotChangeSet, wantChangeSet) {
		t.Fatalf("%s:\ngot: %#v\nwant: %#v", failureContext, gotChangeSet, wantChangeSet)
	}
}

func selfHostingChangeSetRelPaths(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, ".protobot", "change-sets"))
	if err != nil {
		t.Fatal(err)
	}
	relPaths := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || strings.HasPrefix(name, ".") || filepath.Ext(name) != ".yaml" {
			continue
		}
		relPaths = append(relPaths, filepath.ToSlash(filepath.Join(".protobot", "change-sets", name)))
	}
	if len(relPaths) == 0 {
		t.Fatal("no change-set manifests matching .protobot/change-sets/*.yaml")
	}
	sort.Strings(relPaths)
	return relPaths
}

// pinSelfHostingSeedBase rewrites every copied proposed change-set
// base_commit and impact_assessment_base_commit to the local
// default-branch head so check can evaluate Git freshness in the temp repo.
func pinSelfHostingSeedBase(t *testing.T, root string) {
	t.Helper()
	pinSelfHostingChangeSetsExcept(t, root)
}

func pinSelfHostingChangeSetsExcept(t *testing.T, root string, skipIDs ...string) {
	t.Helper()
	base := gitOutput(t, root, "rev-parse", "refs/heads/main")
	skip := make(map[string]bool, len(skipIDs))
	for _, id := range skipIDs {
		skip[id] = true
	}
	pinned := false
	for _, rel := range selfHostingChangeSetRelPaths(t, root) {
		path := filepath.Join(root, filepath.FromSlash(rel))
		var changeSet records.ChangeSet
		if err := storage.ReadFile(path, &changeSet); err != nil {
			t.Fatal(err)
		}
		if skip[changeSet.ID] {
			continue
		}
		if isChangeSetApprovedInTree(root, "refs/heads/main", rel) {
			continue
		}
		changeSet.BaseCommit = base
		changeSet.ImpactAssessmentBaseCommit = base
		data, err := storage.Encode(changeSet)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		pinned = true
	}
	if pinned {
		refreshProjectDigests(t, root)
	}
}

func isChangeSetApprovedInTree(root, ref, relPath string) bool {
	cmd := gitCommand("-C", root, "cat-file", "-e", ref+":"+filepath.ToSlash(relPath))
	return cmd.Run() == nil
}

func selfHostingSourceWithExtraChangeSet(t *testing.T) (string, string) {
	t.Helper()
	origin := selfHostingRoot(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	git(t, root, "init", "-b", "main")
	git(t, root, "config", "user.email", "test@example.invalid")
	git(t, root, "config", "user.name", "Test User")

	for _, rel := range []string{"docs/vision.md", "docs/architecture.md"} {
		writeTestFile(t, root, rel, readTestFile(t, origin, rel))
	}
	for _, rel := range []string{
		".protobot/project.yaml",
		".protobot/projection.yaml",
		".protobot/change-sets/cs-00001.yaml",
		".protobot/interfaces/protobot-cli.yaml",
		".protobot/requirements/REQ-CLI-00001.yaml",
	} {
		writeTestFile(t, root, rel, readTestFile(t, origin, rel))
	}
	git(t, root, "add", ".")
	git(t, root, "commit", "-m", "approved specification records")

	id, rel := nextSelfHostingChangeSetRel(t, root)
	reqID := "REQ-CLI-00002"
	reqFilename, err := records.FilenameFor(records.RequirementStore, reqID)
	if err != nil {
		t.Fatal(err)
	}
	reqRel := filepath.ToSlash(filepath.Join(".protobot", "requirements", reqFilename))
	req := records.Requirement{
		ID:   reqID,
		Type: "event-driven",
		Text: "When an engineer runs the self-hosting test suite, the CLI shall support multiple proposed change sets.",
		AppliesTo: records.Applicability{
			Interfaces: []string{},
			Scopes:     []string{"ears-manager"},
		},
		Verification: records.Verification{
			Mode: "isolated-interface",
		},
		Provenance: "user-authored",
		Created:    "2026-10-13T16:01:00Z",
		Status:     records.StatusActive,
	}
	reqData, err := storage.Encode(req)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, root, reqRel, string(reqData))

	changeSet := records.ChangeSet{
		ID:         id,
		BaseCommit: strings.Repeat("a", 40),
		Intent:     "Document an additional proposed change set",
		Operations: []records.RequirementOperation{
			{
				Action:        "add",
				RequirementID: reqID,
			},
		},
		AffectedInterfaces:         []string{},
		AffectedScopes:             []string{},
		ImplementationRequired:     false,
		ImplementationRationale:    "The additional change set exists only to exercise self-hosting fixtures.",
		ImpactAssessmentBaseCommit: strings.Repeat("a", 40),
		Created:                    "2026-10-13T16:00:00Z",
	}
	data, err := storage.Encode(changeSet)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, root, rel, string(data))
	refreshProjectDigests(t, root)

	return root, rel
}

func nextSelfHostingChangeSetRel(t *testing.T, root string) (string, string) {
	t.Helper()
	used := make(map[string]bool)
	for _, rel := range selfHostingChangeSetRelPaths(t, root) {
		used[filepath.Base(rel)] = true
	}
	for number := 2; number <= lastChangeSetNumber; number++ {
		id := fmt.Sprintf("CS-%05d", number)
		filename, err := records.FilenameFor(records.ChangeSetStore, id)
		if err != nil {
			t.Fatal(err)
		}
		if used[filename] {
			continue
		}
		return id, filepath.ToSlash(filepath.Join(".protobot", "change-sets", filename))
	}
	t.Fatal("no change-set sequence number is available")
	return "", ""
}

func isolateSeedProject(config records.ProjectConfig) records.ProjectConfig {
	config.StoreDigests = records.StoreDigests{}
	return config
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
