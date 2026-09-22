package scm

import (
	"strings"
	"testing"

	"github.com/redhat-et/protobot/source-control-manager/internal/result"
)

func TestValidate(t *testing.T) {
	cases := []struct {
		name    string
		op      string
		args    map[string]any
		field   string
		message string
	}{
		{"unknown field", OpPublish, map[string]any{"force": true}, "force", "does not take"},
		{"first unknown field by name", OpCommit, map[string]any{"paths": []any{"x"}, "amend": true}, "amend", "does not take"},
		{"repo field", OpPublish, map[string]any{"repo": "other-owner/fixture"}, "repo", "does not take"},
		{"missing change set", OpBranchResume, map[string]any{}, "change_set_id", "required"},
		{"wrong change set", OpBranchResume, map[string]any{"change_set_id": "WI-00042"}, "change_set_id", "rule"},
		{"change set type", OpApprovedMerge, map[string]any{"change_set_id": 42.0}, "change_set_id", "rule"},
		{"prefix rule", OpBranchInit, map[string]any{"branch_prefix": "team/cs/"}, "branch_prefix", "rule"},
		{"prefix case", OpBranchInit, map[string]any{"branch_prefix": "CS/"}, "branch_prefix", "rule"},
		{"default under prefix", OpBranchInit, map[string]any{"default_branch": "cs/main"}, "default_branch", "rule"},
		{"default under wi", OpBranchInit, map[string]any{"default_branch": "wi/main"}, "default_branch", "rule"},
		{"default invalid", OpBranchInit, map[string]any{"default_branch": "a..b"}, "default_branch", "rule"},
		{"body trailer", OpCommit, map[string]any{"body": "Change-Set: CS-00009"}, "body", "Change-Set:"},
		{"body keyword", OpCommit, map[string]any{"body": "Fixes #1"}, "body", "GitHub acts on"},
		{"body mention", OpCommit, map[string]any{"body": "Thanks @alice"}, "body", "GitHub acts on"},
		{"body keyword with a Unicode space", OpCommit, map[string]any{"body": "Fixes\u00a0#1"}, "body", "GitHub acts on"},
		{"body CI skip with a Unicode space", OpCommit, map[string]any{"body": "[skip\u3000ci]"}, "body", "GitHub acts on"},
		{"body too long", OpCommit, map[string]any{"body": strings.Repeat("x", MaxBodyLength+1)}, "body", "rule"},
		{"body control", OpCommit, map[string]any{"body": "a\x00b"}, "body", "rule"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, failure := validate(c.op, c.args)
			if failure == nil || failure.Code != result.InvalidRequest {
				t.Fatalf("validate = %v, want INVALID_REQUEST", failure)
			}
			if field, _ := failure.Details.Get("field"); field != c.field {
				t.Fatalf("field %v, want %s", field, c.field)
			}
			if !strings.Contains(failure.Message, c.message) {
				t.Fatalf("message %q lacks %q", failure.Message, c.message)
			}
		})
	}

	req, failure := validate(OpBranchInit, map[string]any{"branch_prefix": "wi/"})
	if failure != nil || req.branchPrefix != "wi/" || req.defaultBranch != "main" {
		t.Fatalf("a reserved prefix is the operation's refusal, not the request's: %+v %v", req, failure)
	}
	req, failure = validate(OpBranchInit, nil)
	if failure != nil || req.branchPrefix != DefaultBranchPrefix || req.defaultBranch != DefaultDefaultBranch {
		t.Fatalf("defaults: %+v %v", req, failure)
	}
	req, failure = validate(OpCommit, map[string]any{"body": nil})
	if failure != nil || req.body != "" {
		t.Fatalf("a null body is no body: %+v %v", req, failure)
	}
}
