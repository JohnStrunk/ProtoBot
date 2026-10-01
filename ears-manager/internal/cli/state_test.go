package cli

import (
	"testing"
)

func TestChangeSetApprovedAtIgnoresReplaceRefs(t *testing.T) {
	root := newFixtureProject(t)
	t.Chdir(root)

	code, stdout, stderr := runCLI(nil, "--output", "json", "change-set", "create", "--intent", "Ignore replace refs", "--implementation-required", "true", "--created", "2026-09-30T15:00:00Z")
	assertSuccess(t, code, stdout, stderr)
	changeSetID := jsonString(t, stdout, "data", "change_set", "id")
	manifest := ".protobot/change-sets/cs-00001.yaml"

	approved, failure := changeSetApprovedAt(root, []string{"refs/heads/main"}, manifest)
	if failure != nil {
		t.Fatalf("unexpected failure: %v", failure)
	}
	if approved {
		t.Fatal("canonical main already holds the new manifest")
	}

	code, beforeCheck, stderr := runCLI(nil, "--output", "json", "check", "--change-set", changeSetID)
	assertSuccess(t, code, beforeCheck, stderr)
	code, beforeShow, stderr := runCLI(nil, "--output", "json", "change-set", "show", "--change-set", changeSetID)
	assertSuccess(t, code, beforeShow, stderr)
	if jsonString(t, beforeShow, "data", "status") != "proposed" {
		t.Fatalf("status = %s, want proposed", beforeShow)
	}

	// Replace the default-branch head with the change-set tip, which holds
	// the new manifest. Approval must still read canonical history.
	git(t, root, "replace", gitOutput(t, root, "rev-parse", "refs/heads/main"), gitOutput(t, root, "rev-parse", "HEAD"))

	approved, failure = changeSetApprovedAt(root, []string{"refs/heads/main"}, manifest)
	if failure != nil {
		t.Fatalf("unexpected failure after replace: %v", failure)
	}
	if approved {
		t.Fatal("approval followed a Git replace ref")
	}

	code, afterCheck, stderr := runCLI(nil, "--output", "json", "check", "--change-set", changeSetID)
	assertSuccess(t, code, afterCheck, stderr)
	if afterCheck != beforeCheck {
		t.Fatalf("a replace ref changed check:\n%s\n---\n%s", afterCheck, beforeCheck)
	}
	code, afterShow, stderr := runCLI(nil, "--output", "json", "change-set", "show", "--change-set", changeSetID)
	assertSuccess(t, code, afterShow, stderr)
	if afterShow != beforeShow {
		t.Fatalf("a replace ref changed show:\n%s\n---\n%s", afterShow, beforeShow)
	}
}
