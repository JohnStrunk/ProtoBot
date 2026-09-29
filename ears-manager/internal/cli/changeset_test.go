package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/redhat-et/protobot/ears-manager/internal/records"
	"github.com/redhat-et/protobot/ears-manager/internal/specvalidation"
	"github.com/redhat-et/protobot/ears-manager/internal/storage"
)

func TestChangeSetAnalysisFlowAndDeterminism(t *testing.T) {
	root := newAnalysisFixture(t)
	t.Chdir(root)

	code, stdout, stderr := runCLI(nil, "--output", "json", "change-set", "create", "--intent", "Define the CLI contract", "--affected-scope", "cli", "--implementation-required", "true", "--created", "2026-09-15T12:01:00Z")
	assertSuccess(t, code, stdout, stderr)
	changeSetID := jsonString(t, stdout, "data", "change_set", "id")
	baseCommit := jsonString(t, stdout, "data", "change_set", "base_commit")

	code, stdout, stderr = runCLI(nil, "--output", "json", "interface", "add", "--change-set", changeSetID, "--id", "cli-main", "--name", "Fixture CLI", "--type", "cli", "--spec-approach", "prose", "--created", "2026-09-15T12:02:00Z")
	assertSuccess(t, code, stdout, stderr)

	code, stdout, stderr = runCLI(nil, "--output", "json", "change-set", "update", "--change-set", changeSetID, "--affected-interface", "cli-main")
	assertSuccess(t, code, stdout, stderr)
	if jsonString(t, stdout, "data", "assessment_status") != specvalidation.AssessmentIncomplete {
		t.Fatalf("scope update assessment = %s", stdout)
	}

	code, stdout, stderr = runCLI(nil, "--output", "json", "requirement", "add", "--change-set", changeSetID, "--id", "REQ-CLI-00001", "--type", "event-driven", "--text", "When the user asks for help, the CLI shall print usage.", "--interface", "cli-main", "--scope", "cli", "--verification-mode", "isolated-interface", "--provenance", "user-authored", "--created", "2026-09-15T12:03:00Z")
	assertSuccess(t, code, stdout, stderr)

	code, firstCompare, stderr := runCLI(nil, "--output", "json", "change-set", "compare", "--change-set", changeSetID)
	assertSuccess(t, code, firstCompare, stderr)
	code, secondCompare, stderr := runCLI(nil, "--output", "json", "change-set", "compare", "--change-set", changeSetID)
	assertSuccess(t, code, secondCompare, stderr)
	if firstCompare != secondCompare {
		t.Fatalf("repeated compare output differed:\n%s\n---\n%s", firstCompare, secondCompare)
	}
	if jsonString(t, firstCompare, "data", "against_commit") != baseCommit {
		t.Fatalf("compare against_commit = %s", firstCompare)
	}
	changed := jsonArray(t, firstCompare, "data", "changed")
	if len(changed) != 2 {
		t.Fatalf("compare changed = %s", firstCompare)
	}

	code, firstImpact, stderr := runCLI(nil, "--output", "json", "impact", "--change-set", changeSetID)
	assertSuccess(t, code, firstImpact, stderr)
	code, secondImpact, stderr := runCLI(nil, "--output", "json", "impact", "--change-set", changeSetID)
	assertSuccess(t, code, secondImpact, stderr)
	if firstImpact != secondImpact {
		t.Fatalf("repeated impact output differed:\n%s\n---\n%s", firstImpact, secondImpact)
	}
	if jsonString(t, firstImpact, "data", "assessment_status") != specvalidation.AssessmentIncomplete {
		t.Fatalf("impact status = %s", firstImpact)
	}
	candidates := jsonArray(t, firstImpact, "data", "candidates")
	if len(candidates) != 1 {
		t.Fatalf("impact candidates = %s", firstImpact)
	}
	if jsonStringFromValue(t, candidates[0], "requirement_id") != "REQ-CLI-00002" {
		t.Fatalf("mechanical candidate = %s", firstImpact)
	}
	matchedBy := jsonArrayFromValue(t, candidates[0], "matched_by")
	if len(matchedBy) != 1 || matchedBy[0] != "scope:cli" {
		t.Fatalf("matched_by = %s", firstImpact)
	}

	code, stdout, stderr = runCLI(nil, "--output", "json", "check", "--change-set", changeSetID)
	if code != 5 || stderr != "" || !strings.Contains(stdout, "change_set.assessment_incomplete") {
		t.Fatalf("incomplete check result = code %d stdout %s stderr %s", code, stdout, stderr)
	}

	impactPath := filepath.Join(root, "impact-review.json")
	writeImpactFile(t, impactPath, []impactAssessmentJSON{
		{RequirementID: "REQ-CLI-00002", Disposition: "applicable", Rationale: "The existing help obligation constrains this CLI change.", Origin: "mechanical"},
		{RequirementID: "REQ-CLI-00004", Disposition: "not-applicable", Rationale: "The agent considered administration logging semantically and the user excluded it from this change.", Origin: "semantic"},
	})
	code, stdout, stderr = runCLI(nil, "--output", "json", "change-set", "update", "--change-set", changeSetID, "--impact-file", impactPath)
	assertSuccess(t, code, stdout, stderr)
	if jsonString(t, stdout, "data", "assessment_status") != specvalidation.AssessmentComplete {
		t.Fatalf("reviewed assessment = %s", stdout)
	}

	code, stdout, stderr = runCLI(nil, "--output", "json", "check", "--change-set", changeSetID)
	assertSuccess(t, code, stdout, stderr)

	code, stdout, stderr = runCLI(nil, "--output", "json", "change-set", "show", "--change-set", changeSetID)
	assertSuccess(t, code, stdout, stderr)
	if jsonString(t, stdout, "data", "status") != "proposed" {
		t.Fatalf("show status = %s", stdout)
	}
	if jsonInt(t, stdout, "data", "applicable_count") != 1 {
		t.Fatalf("applicable count = %s", stdout)
	}

	code, stdout, stderr = runCLI(nil, "--output", "json", "change-set", "list", "--status", "proposed", "--scope", "cli")
	assertSuccess(t, code, stdout, stderr)
	listed := jsonArray(t, stdout, "data", "change_sets")
	if len(listed) != 1 || jsonStringFromValue(t, listed[0], "id") != changeSetID {
		t.Fatalf("list = %s", stdout)
	}

	code, postCompare, stderr := runCLI(nil, "--output", "json", "change-set", "compare", "--change-set", changeSetID)
	assertSuccess(t, code, postCompare, stderr)
	if postCompare != firstCompare {
		t.Fatalf("compare changed after impact review:\n%s\n---\n%s", firstCompare, postCompare)
	}
}

func TestApprovedChangeSetCannotBeUpdated(t *testing.T) {
	root := newFixtureProject(t)
	t.Chdir(root)
	code, stdout, stderr := runCLI(nil, "--output", "json", "change-set", "create", "--intent", "Approve me", "--implementation-required", "true", "--created", "2026-09-18T18:00:00Z")
	assertSuccess(t, code, stdout, stderr)
	changeSetID := jsonString(t, stdout, "data", "change_set", "id")
	git(t, root, "add", ".")
	git(t, root, "commit", "-m", "approve change set")

	code, stdout, stderr = runCLI(nil, "--output", "json", "change-set", "update", "--change-set", changeSetID, "--intent", "Rewrite history")
	if code != 5 || stderr != "" || !strings.Contains(stdout, "change_set.not_proposed") {
		t.Fatalf("approved update result = code %d stdout %s stderr %s", code, stdout, stderr)
	}
	code, stdout, stderr = runCLI(nil, "--output", "json", "change-set", "show", "--change-set", changeSetID)
	assertSuccess(t, code, stdout, stderr)
	if jsonString(t, stdout, "data", "status") != "approved" {
		t.Fatalf("approved show status = %s", stdout)
	}
}

func TestCompareReportsRelationshipFindings(t *testing.T) {
	root := newFixtureProject(t)
	t.Chdir(root)
	code, stdout, stderr := runCLI(nil, "--output", "json", "change-set", "create", "--intent", "Relationship findings", "--implementation-required", "true", "--created", "2026-09-18T19:00:00Z")
	assertSuccess(t, code, stdout, stderr)
	changeSetID := jsonString(t, stdout, "data", "change_set", "id")
	add := func(id, text string) {
		t.Helper()
		code, stdout, stderr = runCLI(nil, "--output", "json", "requirement", "add", "--change-set", changeSetID, "--id", id, "--type", "ubiquitous", "--text", text, "--scope", "cli", "--verification-mode", "isolated-interface", "--provenance", "user-authored", "--created", "2026-09-18T19:01:00Z")
		assertSuccess(t, code, stdout, stderr)
	}
	add("REQ-DUP-00001", "The system shall preserve duplicate text.")
	add("REQ-DUP-00002", "The system shall preserve duplicate text.")
	add("REQ-CON-00001", "The system shall preserve the first exclusive mode.")
	add("REQ-CON-00002", "The system shall preserve the second exclusive mode.")
	code, stdout, stderr = runCLI(nil, "--output", "json", "requirement", "update", "--change-set", changeSetID, "--id", "REQ-CON-00001", "--relationship", "conflicts-with=REQ-CON-00002")
	assertSuccess(t, code, stdout, stderr)
	add("REQ-SUP-00001", "The system shall preserve the replacement behavior.")
	add("REQ-SUP-00002", "The system shall preserve the retired behavior.")
	code, stdout, stderr = runCLI(nil, "--output", "json", "requirement", "retire", "--change-set", changeSetID, "--id", "REQ-SUP-00002")
	assertSuccess(t, code, stdout, stderr)
	code, stdout, stderr = runCLI(nil, "--output", "json", "requirement", "update", "--change-set", changeSetID, "--id", "REQ-SUP-00001", "--relationship", "supersedes=REQ-SUP-00002")
	assertSuccess(t, code, stdout, stderr)
	add("REQ-CYC-00001", "The system shall preserve the first cyclic behavior.")
	add("REQ-CYC-00002", "The system shall preserve the second cyclic behavior.")
	code, stdout, stderr = runCLI(nil, "--output", "json", "requirement", "update", "--change-set", changeSetID, "--id", "REQ-CYC-00001", "--relationship", "depends-on=REQ-CYC-00002")
	assertSuccess(t, code, stdout, stderr)

	code, stdout, stderr = runCLI(nil, "--output", "json", "change-set", "compare", "--change-set", changeSetID)
	assertSuccess(t, code, stdout, stderr)
	if len(jsonArray(t, stdout, "data", "exact_duplicates")) != 1 {
		t.Fatalf("duplicates = %s", stdout)
	}
	if len(jsonArray(t, stdout, "data", "declared_conflicts")) != 1 {
		t.Fatalf("conflicts = %s", stdout)
	}
	if len(jsonArray(t, stdout, "data", "supersession")) != 1 {
		t.Fatalf("supersession = %s", stdout)
	}

	code, stdout, stderr = runCLI(nil, "--output", "json", "requirement", "update", "--change-set", changeSetID, "--id", "REQ-CYC-00002", "--relationship", "depends-on=REQ-CYC-00001")
	if code != 4 || !strings.Contains(stdout, "relationship.cycle") {
		t.Fatalf("cycle write result = code %d stdout %s stderr %s", code, stdout, stderr)
	}
}

func TestChangeSetUpdateRejectsInvalidImpactFile(t *testing.T) {
	root := newAnalysisFixture(t)
	t.Chdir(root)
	code, stdout, stderr := runCLI(nil, "--output", "json", "change-set", "create", "--intent", "Invalid impact", "--affected-scope", "cli", "--implementation-required", "true", "--created", "2026-09-18T20:00:00Z")
	assertSuccess(t, code, stdout, stderr)
	changeSetID := jsonString(t, stdout, "data", "change_set", "id")
	path := filepath.Join(root, "bad-impact.json")
	if err := os.WriteFile(path, []byte(`{"requirement_id":"REQ-CLI-00002"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = runCLI(nil, "--output", "json", "change-set", "update", "--change-set", changeSetID, "--impact-file", path)
	if code != 4 || stderr != "" || !strings.Contains(stdout, "change_set.invalid_impact") {
		t.Fatalf("invalid impact result = code %d stdout %s stderr %s", code, stdout, stderr)
	}
}

func TestChangeSetCompareRejectsAgainstOption(t *testing.T) {
	root := newFixtureProject(t)
	t.Chdir(root)
	code, stdout, stderr := runCLI(nil, "--output", "json", "change-set", "create", "--intent", "Compare against test", "--implementation-required", "true", "--created", "2026-09-18T18:00:00Z")
	assertSuccess(t, code, stdout, stderr)
	changeSetID := jsonString(t, stdout, "data", "change_set", "id")

	// Passing --against is refused with change_set.invalid_base
	code, stdout, stderr = runCLI(nil, "--output", "json", "change-set", "compare", "--change-set", changeSetID, "--against", strings.Repeat("0", 40))
	if code != 4 || !strings.Contains(stdout, "change_set.invalid_base") {
		t.Fatalf("expected change_set.invalid_base when --against passed, got code %d stdout %s stderr %s", code, stdout, stderr)
	}

	// Compare usage does not advertise deferred --against option
	code, stdout, _ = runCLI(nil, "change-set", "compare", "--help")
	if code != 0 || strings.Contains(stdout, "--against") {
		t.Fatalf("compare help advertised deferred --against: %s", stdout)
	}
	if stdout != "Usage: ears-manager change-set compare --change-set CS-ID\n" {
		t.Fatalf("compare help usage = %q", stdout)
	}
}

func TestChangeSetCompareFailsOnUnreadableBaseCommit(t *testing.T) {
	root := newFixtureProject(t)
	t.Chdir(root)
	code, stdout, stderr := runCLI(nil, "--output", "json", "change-set", "create", "--intent", "Missing base commit test", "--implementation-required", "true", "--created", "2026-09-18T18:00:00Z")
	assertSuccess(t, code, stdout, stderr)
	changeSetID := jsonString(t, stdout, "data", "change_set", "id")

	// Corrupt the base_commit in the stored change set manifest to a non-existent commit
	manifestPath := filepath.Join(root, ".protobot", "change-sets", strings.ToLower(changeSetID)+".yaml")
	var cs records.ChangeSet
	if err := storage.ReadFile(manifestPath, &cs); err != nil {
		t.Fatal(err)
	}
	cs.BaseCommit = strings.Repeat("f", 40)
	data, err := storage.Encode(cs)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	refreshProjectDigests(t, root)

	code, stdout, stderr = runCLI(nil, "--output", "json", "change-set", "compare", "--change-set", changeSetID)
	if code != 4 || !strings.Contains(stdout, "change_set.invalid_base") {
		t.Fatalf("expected change_set.invalid_base for unreadable base, got code %d stdout %s stderr %s", code, stdout, stderr)
	}
}

func TestChangeSetUpdateFailsWithNotProposedWhenMissing(t *testing.T) {
	root := newFixtureProject(t)
	t.Chdir(root)
	code, stdout, stderr := runCLI(nil, "--output", "json", "change-set", "update", "--change-set", "CS-99999", "--intent", "Nonexistent")
	if code != 4 || !strings.Contains(stdout, "change_set.not_proposed") {
		t.Fatalf("expected change_set.not_proposed for missing change-set, got code %d stdout %s stderr %s", code, stdout, stderr)
	}
}

func TestChangeSetMutationFailsClosedWhenDefaultBranchRefMissing(t *testing.T) {
	root := newFixtureProject(t)
	t.Chdir(root)
	code, stdout, stderr := runCLI(nil, "--output", "json", "change-set", "create", "--intent", "Unresolved default branch test", "--implementation-required", "true", "--created", "2026-09-18T18:00:00Z")
	assertSuccess(t, code, stdout, stderr)
	changeSetID := jsonString(t, stdout, "data", "change_set", "id")

	// Set default_branch in project.yaml to a branch that does not exist in git
	configPath := filepath.Join(root, ".protobot", "project.yaml")
	var config records.ProjectConfig
	if err := storage.ReadFile(configPath, &config); err != nil {
		t.Fatal(err)
	}
	config.Repository.DefaultBranch = "nonexistent-branch"
	data, err := storage.Encode(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	// Any mutation (e.g. change-set update) must fail closed rather than treating manifests as proposed
	code, stdout, stderr = runCLI(nil, "--output", "json", "change-set", "update", "--change-set", changeSetID, "--intent", "Should fail closed")
	if code == 0 || !strings.Contains(stdout, "project.default_branch_unresolved") {
		t.Fatalf("expected failure when default branch is unresolved, got code %d stdout %s stderr %s", code, stdout, stderr)
	}
}

func TestChangeSetWritesAfterCommitsOnChangeSetBranch(t *testing.T) {
	root := newFixtureProject(t)
	t.Chdir(root)
	git(t, root, "checkout", "-b", "cs/00001-several-commits")
	code, stdout, stderr := runCLI(nil, "--output", "json", "change-set", "create", "--intent", "Several commits", "--implementation-required", "true", "--created", "2026-09-29T10:00:00Z")
	assertSuccess(t, code, stdout, stderr)
	changeSetID := jsonString(t, stdout, "data", "change_set", "id")
	baseCommit := jsonString(t, stdout, "data", "change_set", "base_commit")
	commitAll(t, root, "spec(CS-00001): create the change set")

	runAll := func(commands ...[]string) {
		t.Helper()
		for _, args := range commands {
			code, stdout, stderr := runCLI(nil, append([]string{"--output", "json"}, args...)...)
			if code != 0 || stderr != "" {
				t.Fatalf("%v result = code %d stdout %s stderr %s", args, code, stdout, stderr)
			}
		}
	}
	runAll(
		[]string{"interface", "add", "--change-set", changeSetID, "--id", "cli-main", "--name", "CLI", "--type", "cli", "--spec-approach", "prose", "--created", "2026-09-29T10:01:00Z"},
		[]string{"requirement", "add", "--change-set", changeSetID, "--id", "REQ-CLI-00001", "--type", "ubiquitous", "--text", "The CLI shall print help.", "--interface", "cli-main", "--scope", "cli", "--verification-mode", "isolated-interface", "--provenance", "user-authored", "--created", "2026-09-29T10:02:00Z"},
		[]string{"requirement", "add", "--change-set", changeSetID, "--id", "REQ-CLI-00002", "--type", "ubiquitous", "--text", "The CLI shall print a banner.", "--scope", "cli", "--verification-mode", "isolated-interface", "--provenance", "user-authored", "--created", "2026-09-29T10:03:00Z"},
	)
	commitAll(t, root, "spec(CS-00001): add the CLI records")

	runAll(
		[]string{"requirement", "update", "--change-set", changeSetID, "--id", "REQ-CLI-00001", "--text", "The CLI shall print usage."},
		[]string{"requirement", "retire", "--change-set", changeSetID, "--id", "REQ-CLI-00002"},
		[]string{"change-set", "update", "--change-set", changeSetID, "--intent", "Several commits on one branch"},
	)
	code, stdout, stderr = runCLI([]byte("# Vision after two commits\n"), "--output", "json", "artifact", "put", "--change-set", changeSetID, "--id", "vision", "--kind", "vision", "--path", "docs/vision.md", "--owner", "user", "--content-stdin")
	assertSuccess(t, code, stdout, stderr)

	if head := gitOutput(t, root, "rev-parse", "HEAD"); head == baseCommit {
		t.Fatalf("HEAD %s did not advance past the base commit", head)
	}
	code, stdout, stderr = runCLI(nil, "--output", "json", "change-set", "show", "--change-set", changeSetID)
	assertSuccess(t, code, stdout, stderr)
	if jsonString(t, stdout, "data", "change_set", "base_commit") != baseCommit {
		t.Fatalf("writes changed the base commit: %s", stdout)
	}
}

func TestChangeSetRefreshAfterDefaultBranchMerge(t *testing.T) {
	root := newAnalysisFixture(t)
	commitAll(t, root, "fixture requirements")
	t.Chdir(root)
	git(t, root, "checkout", "-b", "cs/00001-refresh")
	code, stdout, stderr := runCLI(nil, "--output", "json", "change-set", "create", "--intent", "Refresh from main", "--affected-scope", "cli", "--implementation-required", "true", "--created", "2026-09-29T11:00:00Z")
	assertSuccess(t, code, stdout, stderr)
	changeSetID := jsonString(t, stdout, "data", "change_set", "id")
	code, stdout, stderr = runCLI(nil, "--output", "json", "requirement", "add", "--change-set", changeSetID, "--id", "REQ-CLI-00001", "--type", "ubiquitous", "--text", "The fixture CLI shall print usage.", "--scope", "cli", "--verification-mode", "isolated-interface", "--provenance", "user-authored", "--created", "2026-09-29T11:01:00Z")
	assertSuccess(t, code, stdout, stderr)
	review, err := json.Marshal([]impactAssessmentJSON{
		{RequirementID: "REQ-CLI-00002", Disposition: "applicable", Rationale: "The existing help obligation constrains this CLI change.", Origin: "mechanical"},
	})
	if err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = runCLI(review, "--output", "json", "change-set", "update", "--change-set", changeSetID, "--impact-file", "-")
	assertSuccess(t, code, stdout, stderr)
	commitAll(t, root, "spec(CS-00001): refresh from main")

	git(t, root, "checkout", "main")
	if err := os.WriteFile(filepath.Join(root, "unrelated.txt"), []byte("default branch change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commitAll(t, root, "unrelated default-branch change")
	defaultHead := gitOutput(t, root, "rev-parse", "HEAD")
	git(t, root, "checkout", "cs/00001-refresh")
	git(t, root, "merge", "--no-ff", "--no-edit", "main")

	// A write after the merge and before the base update is accepted.
	code, stdout, stderr = runCLI(nil, "--output", "json", "requirement", "add", "--change-set", changeSetID, "--id", "REQ-CLI-00003", "--type", "ubiquitous", "--text", "The fixture CLI shall print its version.", "--scope", "cli", "--verification-mode", "isolated-interface", "--provenance", "user-authored", "--created", "2026-09-29T11:02:00Z")
	assertSuccess(t, code, stdout, stderr)

	code, stdout, stderr = runCLI(nil, "--output", "json", "change-set", "update", "--change-set", changeSetID, "--base-commit", defaultHead)
	assertSuccess(t, code, stdout, stderr)
	if jsonString(t, stdout, "data", "after", "base_commit") != defaultHead {
		t.Fatalf("refreshed base = %s, want %s", stdout, defaultHead)
	}
	code, stdout, stderr = runCLI(nil, "--output", "json", "impact", "--change-set", changeSetID)
	assertSuccess(t, code, stdout, stderr)
	if jsonString(t, stdout, "data", "against_commit") != defaultHead {
		t.Fatalf("impact against_commit = %s", stdout)
	}
	code, stdout, stderr = runCLI(review, "--output", "json", "change-set", "update", "--change-set", changeSetID, "--impact-file", "-")
	assertSuccess(t, code, stdout, stderr)
	if jsonString(t, stdout, "data", "assessment_status") != specvalidation.AssessmentComplete {
		t.Fatalf("refreshed assessment = %s", stdout)
	}
	code, stdout, stderr = runCLI(nil, "--output", "json", "check", "--change-set", changeSetID)
	assertSuccess(t, code, stdout, stderr)
	code, stdout, stderr = runCLI(nil, "--output", "json", "check")
	assertSuccess(t, code, stdout, stderr)
}

func TestChangeSetBaseRefreshRefusesCommitOutsideBranchHistory(t *testing.T) {
	root := newFixtureProject(t)
	t.Chdir(root)
	firstCommit := gitOutput(t, root, "rev-parse", "HEAD")
	git(t, root, "checkout", "-b", "cs/00001-refuse")
	code, stdout, stderr := runCLI(nil, "--output", "json", "change-set", "create", "--intent", "Refuse bad bases", "--implementation-required", "true", "--created", "2026-09-29T12:00:00Z")
	assertSuccess(t, code, stdout, stderr)
	changeSetID := jsonString(t, stdout, "data", "change_set", "id")
	commitAll(t, root, "spec(CS-00001): create the change set")
	git(t, root, "checkout", "main")
	if err := os.WriteFile(filepath.Join(root, "unrelated.txt"), []byte("default branch change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commitAll(t, root, "unrelated default-branch change")
	defaultHead := gitOutput(t, root, "rev-parse", "HEAD")
	git(t, root, "checkout", "cs/00001-refuse")

	assertBaseMismatch := func(baseCommit string) {
		t.Helper()
		code, stdout, stderr := runCLI(nil, "--output", "json", "change-set", "update", "--change-set", changeSetID, "--base-commit", baseCommit)
		if code != 5 || stderr != "" || jsonString(t, stdout, "error", "code") != "change_set.base_mismatch" {
			t.Fatalf("--base-commit %s result = code %d stdout %s stderr %s", baseCommit, code, stdout, stderr)
		}
	}
	// The default head is not merged, so HEAD does not descend from it.
	assertBaseMismatch(defaultHead)

	git(t, root, "merge", "--no-ff", "--no-edit", "main")
	code, stdout, stderr = runCLI(nil, "--output", "json", "change-set", "update", "--change-set", changeSetID, "--base-commit", defaultHead)
	assertSuccess(t, code, stdout, stderr)
	// The first commit is an ancestor of HEAD but not a descendant of the
	// recorded base, so the base cannot move back to it.
	assertBaseMismatch(firstCommit)
}

func commitAll(t *testing.T, root, message string) {
	t.Helper()
	git(t, root, "add", ".")
	git(t, root, "commit", "-m", message)
}

func newAnalysisFixture(t *testing.T) string {
	t.Helper()
	root := newFixtureProject(t)
	writeFixtureRequirement(t, root, records.Requirement{
		ID:           "REQ-CLI-00002",
		Type:         records.EARSUbiquitous,
		Text:         "The fixture CLI shall preserve existing help behavior.",
		AppliesTo:    records.Applicability{Scopes: []string{"cli"}},
		Verification: records.Verification{Mode: records.VerificationIsolatedInterface},
		Provenance:   records.ProvenanceUserAuthored,
		Created:      "2026-09-14T12:00:00Z",
		Status:       records.StatusActive,
	})
	writeFixtureRequirement(t, root, records.Requirement{
		ID:           "REQ-CLI-00004",
		Type:         records.EARSUbiquitous,
		Text:         "The fixture CLI shall record administration events.",
		AppliesTo:    records.Applicability{Scopes: []string{"cli-administration"}},
		Verification: records.Verification{Mode: records.VerificationIsolatedInterface},
		Provenance:   records.ProvenanceUserAuthored,
		Created:      "2026-09-14T12:00:00Z",
		Status:       records.StatusActive,
	})
	refreshProjectDigests(t, root)
	return root
}

func writeFixtureRequirement(t *testing.T, root string, requirement records.Requirement) {
	t.Helper()
	data, err := storage.Encode(records.CanonicalRequirement(requirement))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".protobot", "requirements", requirement.ID+".yaml")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func refreshProjectDigests(t *testing.T, root string) {
	t.Helper()
	path := filepath.Join(root, ".protobot", "project.yaml")
	var config records.ProjectConfig
	if err := storage.ReadFile(path, &config); err != nil {
		t.Fatal(err)
	}
	stores := config.Stores.WithDefaults()
	var err error
	if config.StoreDigests.Requirements, err = specvalidation.CanonicalStoreDigestWithOverrides(root, stores.Requirements, nil); err != nil {
		t.Fatal(err)
	}
	if config.StoreDigests.Interfaces, err = specvalidation.CanonicalStoreDigestWithOverrides(root, stores.Interfaces, nil); err != nil {
		t.Fatal(err)
	}
	if config.StoreDigests.ChangeSets, err = specvalidation.CanonicalStoreDigestWithOverrides(root, stores.ChangeSets, nil); err != nil {
		t.Fatal(err)
	}
	data, err := storage.Encode(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeImpactFile(t *testing.T, path string, entries []impactAssessmentJSON) {
	t.Helper()
	data, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func jsonArray(t *testing.T, data, key1, key2 string) []any {
	t.Helper()
	value := decodeJSONPath(t, data, key1, key2)
	result, ok := value.([]any)
	if !ok {
		t.Fatalf("JSON path %s.%s was %T, want array: %s", key1, key2, value, data)
	}
	return result
}

func jsonInt(t *testing.T, data string, path ...string) int {
	t.Helper()
	value := decodeJSONPath(t, data, path...)
	number, ok := value.(float64)
	if !ok {
		t.Fatalf("JSON path %v was %T, want number: %s", path, value, data)
	}
	return int(number)
}

func jsonStringFromValue(t *testing.T, value any, key string) string {
	t.Helper()
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("JSON value was %T, want object", value)
	}
	result, ok := object[key].(string)
	if !ok {
		t.Fatalf("JSON key %s was %T, want string", key, object[key])
	}
	return result
}

func jsonArrayFromValue(t *testing.T, value any, key string) []any {
	t.Helper()
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("JSON value was %T, want object", value)
	}
	result, ok := object[key].([]any)
	if !ok {
		t.Fatalf("JSON key %s was %T, want array", key, object[key])
	}
	return result
}
