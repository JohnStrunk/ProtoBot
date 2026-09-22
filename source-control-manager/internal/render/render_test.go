package render

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/redhat-et/protobot/source-control-manager/internal/ears"
)

func TestActsOnGitHub(t *testing.T) {
	acts := []string{
		"Fixes #1", "fixes #12", "FIXED: #3", "closes GH-4", "Resolve owner/repo#5", "resolved: https://github.com/o/r/issues/6",
		"close #7", "closed #8", "fix #9", "resolves #10", "fixes#11", "Fixes  :  #12",
		"@alice", "ping @org/team please", "(@bob)", "-@octocat", ".@octocat", "+@octocat",
		"Draft [skip ci]", "[CI SKIP] later", "[no ci]", "[skip actions]", "body\nskip-checks: true",
		// Unicode spaces and invisible format characters, which OneLine or
		// GitHub turn into plain text.
		"Fixes\u00a0#12", "fixes\u2009#3", "Closes\u3000GH-4", "Fix\u200bes #1", "[skip\u00a0ci]",
		"Fixes\v#12", "Resolves\u2003#5", "Adds a tag [skip\u2003ci]",
		"cc\u00a0@octocat", "body\nskip-checks:\u00a0true",
	}
	for _, text := range acts {
		if !ActsOnGitHub(text) {
			t.Errorf("ActsOnGitHub(%q) = false, want true", text)
		}
	}
	safe := []string{
		"Add the initial Sketch", "prefixes #1", "fixture #1 of 3", "mail user@example.com", "#1 is the first",
		"Fix the parser", "resolve the conflict",
	}
	for _, text := range safe {
		if ActsOnGitHub(text) {
			t.Errorf("ActsOnGitHub(%q) = true, want false", text)
		}
	}
}

func TestHasTrailerLine(t *testing.T) {
	for _, text := range []string{"Change-Set: CS-00009", "prose\nchange-set: CS-1", "  Change-Set : x", "Change-Set\u00a0: x", "\u00a0Change-Set: x"} {
		if !HasTrailerLine(text) {
			t.Errorf("HasTrailerLine(%q) = false", text)
		}
	}
	if HasTrailerLine("The Change-Set: trailer is added by the SCM.") {
		t.Error("a mid-line mention is not a trailer line")
	}
}

func TestCommitMessage(t *testing.T) {
	got := CommitMessage("CS-00002", "  Add the\n initial\tSketch ", "\n\nWhy it changes.\n\n")
	want := "spec(CS-00002): Add the initial Sketch\n\nWhy it changes.\n\nChange-Set: CS-00002\n"
	if got != want {
		t.Fatalf("CommitMessage = %q, want %q", got, want)
	}
	if got := CommitMessage("CS-00001", "Init", ""); got != "spec(CS-00001): Init\n\nChange-Set: CS-00001\n" {
		t.Fatalf("CommitMessage without a body = %q", got)
	}
	if got := RefreshMessage("origin/main", "cs/00002-x", "CS-00002"); got != "Merge origin/main into cs/00002-x\n\nChange-Set: CS-00002" {
		t.Fatalf("RefreshMessage = %q", got)
	}
	// A Unicode space in the intent becomes an ASCII space in the subject,
	// so the check must see the same keyword in both.
	intent := "Fixes\u00a0#12"
	if subject := Subject("CS-00002", intent); subject != "spec(CS-00002): Fixes #12" || !ActsOnGitHub(intent) || !ActsOnGitHub(subject) {
		t.Fatalf("Subject(%q) = %q, and the check does not refuse both", intent, subject)
	}
}

func TestCode(t *testing.T) {
	cases := map[string]string{
		"plain":      "`plain`",
		"a`b":        "``a`b``",
		"``x":        "``` ``x ```",
		"x`":         "`` x` ``",
		" padded ":   "`  padded  `",
		"":           "` `",
		"<script>":   "`<script>`",
		"[l](http:)": "`[l](http:)`",
	}
	for in, want := range cases {
		if got := Code(in); got != want {
			t.Errorf("Code(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFenced(t *testing.T) {
	got := Fenced("line one\n```\nline three", "  ")
	want := "  ````\n  line one\n  ```\n  line three\n  ````\n"
	if got != want {
		t.Fatalf("Fenced = %q, want %q", got, want)
	}
	// A lone CR is a line ending in CommonMark: every line gets the indent,
	// or the rest would fall out of the list item and the fence.
	got = Fenced("Not affected.\rFixes #12 cc @octocat\r\n<img src=x>", "    ")
	want = "    ```\n    Not affected.\n    Fixes #12 cc @octocat\n    <img src=x>\n    ```\n"
	if got != want {
		t.Fatalf("Fenced with CR = %q, want %q", got, want)
	}
}

func TestCodeLineBreaks(t *testing.T) {
	for _, in := range []string{"a\rb", "a\nb", "a\r\nb"} {
		if got := Code(in); strings.ContainsAny(got, "\r\n") {
			t.Errorf("Code(%q) = %q keeps a line ending", in, got)
		}
	}
}

func TestBody(t *testing.T) {
	after := json.RawMessage(`{"text":"When the user asks for help,\nthe CLI shall print usage."}`)
	before := json.RawMessage(`"The CLI shall print ` + "`usage`" + `."`)
	disposition := "applicable\rFixes #3"
	rationale := "It constrains @nobody #1.\rFixes #12 cc @octocat\r<img src=x>"
	required := false
	in := BodyInput{
		ChangeSetID: "CS-00005",
		Manifest: ears.Manifest{
			Intent: "Fixes #1 in `code`", BaseCommit: strings.Repeat("a", 40),
			ImplementationRationale: "Docs only.",
			ImpactAssessment:        []ears.Assessment{{RequirementID: "REQ-CLI-00009", Disposition: "not-applicable", Rationale: "Unrelated.", Origin: "semantic"}},
		},
		Compare: &ears.Compare{
			Changed: []ears.Changed{
				{Action: "revise", RequirementID: "REQ-CLI-00001", Before: before, After: after},
				{Action: "add", InterfaceID: "cli-main"},
			},
			ImplementationRequired: &required,
		},
		Impact: &ears.Impact{Candidates: []ears.Candidate{
			{RequirementID: "REQ-CLI-00002", Origin: "mechanical", RecordedDisposition: &disposition, Rationale: &rationale},
			{RequirementID: "REQ-CLI-00003", Origin: "mechanical"},
		}},
		Files: []string{"docs/vision.md"},
	}
	body := Body(in)
	if body != Body(in) {
		t.Fatal("the body is not deterministic")
	}
	for _, part := range []string{
		Header,
		"## Change set `CS-00005`",
		"- Intent: `` Fixes #1 in `code` ``",
		"- `revise` `REQ-CLI-00001`",
		"  - Before: ``The CLI shall print `usage`.``",
		"  - After:\n\n    ```\n    When the user asks for help,\n    the CLI shall print usage.\n    ```",
		"- Interface `add` `cli-main`",
		"- `REQ-CLI-00002` (`mechanical`): `applicable Fixes #3`",
		"  - Rationale:\n\n    ```\n    It constrains @nobody #1.\n    Fixes #12 cc @octocat\n    <img src=x>\n    ```",
		"- `REQ-CLI-00003` (`mechanical`): no disposition yet",
		"- `REQ-CLI-00009` (`semantic`): `not-applicable`",
		"- Implementation required: `false`",
		"- Rationale: `Docs only.`",
		"- `docs/vision.md`",
	} {
		if !strings.Contains(body, part) {
			t.Errorf("the body lacks %q:\n%s", part, body)
		}
	}
	// Outside code spans and fences, the body holds no text that GitHub
	// acts on, whatever the records hold, and no line ending but LF.
	if strings.Contains(body, "\r") {
		t.Fatal("the body holds a CR")
	}
	var outside []string
	inFence := false
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if !inFence {
			outside = append(outside, stripCode(line))
		}
	}
	if text := strings.Join(outside, "\n"); ActsOnGitHub(text) {
		t.Fatalf("the body acts on GitHub outside code:\n%s", text)
	}
}

// stripCode removes code spans from a line.
func stripCode(line string) string {
	var out strings.Builder
	for i := 0; i < len(line); {
		if line[i] != '`' {
			out.WriteByte(line[i])
			i++
			continue
		}
		run := 0
		for i+run < len(line) && line[i+run] == '`' {
			run++
		}
		fence := line[i : i+run]
		end := strings.Index(line[i+run:], fence)
		if end < 0 {
			out.WriteString(line[i:])
			break
		}
		i += run + end + run
	}
	return out.String()
}
