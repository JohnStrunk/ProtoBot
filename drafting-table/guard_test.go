package guard

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type guardVector struct {
	Name      string         `json:"name"`
	Role      string         `json:"role"`
	ToolName  string         `json:"tool_name"`
	ToolInput map[string]any `json:"tool_input"`
	Allow     bool           `json:"allow"`
	Reason    string         `json:"reason"`
	NoProject bool           `json:"no_project"`
}

func TestGuardVectors(t *testing.T) {
	data, err := os.ReadFile("testdata/guard-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []guardVector
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, vector := range vectors {
		t.Run(vector.Name, func(t *testing.T) {
			result := newFixture(t, !vector.NoProject, false).call(vector.Role, vector.ToolName, vector.ToolInput)
			if result.allow != vector.Allow {
				t.Fatalf("decision = %+v; allow=%v", result, vector.Allow)
			}
			if vector.Reason != "" && !strings.Contains(result.reason, vector.Reason) {
				t.Fatalf("reason %q does not contain %q", result.reason, vector.Reason)
			}
		})
	}
}

func TestApplyPatchUsesOpenCodeToolNameAndTargets(t *testing.T) {
	fixture := newFixture(t, true, false)
	patch := "*** Begin Patch\n*** Update File: docs/vision.md\n@@\n-old\n+new\n*** End Patch"
	result := fixture.call(roleOther, "apply_patch", map[string]any{"patchText": patch})
	if result.allow || !strings.Contains(result.reason, "registered artifact") {
		t.Fatalf("decision = %+v", result)
	}
	patch = "*** Begin Patch\n*** Add File: notes/new.txt\n+note\n*** End Patch"
	if result := fixture.call(roleOther, "apply_patch", map[string]any{"patchText": patch}); !result.allow {
		t.Fatalf("unregistered patch decision = %+v", result)
	}
	if result := fixture.call(roleDraftingTable, "apply_patch", map[string]any{"patchText": patch}); result.allow {
		t.Fatalf("Drafting Table patch decision = %+v", result)
	}
}

func TestApplyPatchPathsUseProjectRootFromSubdirectory(t *testing.T) {
	fixture := newFixture(t, true, false)
	subdirectory := filepath.Join(fixture.root, "packages", "nested")
	if err := os.MkdirAll(subdirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	patch := map[string]any{"patchText": "*** Begin Patch\n*** Update File: docs/vision.md\n@@\n-old\n+new\n*** End Patch"}
	result := fixture.callAt(subdirectory, roleOther, "apply_patch", patch)
	if result.allow || !strings.Contains(result.reason, "registered artifact") {
		t.Fatalf("subdirectory apply_patch decision = %+v", result)
	}
	result = fixture.callAt(subdirectory, roleOther, "write", map[string]any{"filePath": "docs/vision.md"})
	if !result.allow {
		t.Fatalf("cwd-relative write decision = %+v", result)
	}

	// artifact list succeeds, then change-set list fails. The known registered
	// artifact must remain guarded for other roles during fail-closed handling.
	t.Setenv("FAKE_EARS_CHANGESET_FAIL", "1")
	result = fixture.callAt(subdirectory, roleOther, "apply_patch", patch)
	if result.allow || !strings.Contains(result.reason, "registered artifact") {
		t.Fatalf("inspection-failure apply_patch decision = %+v", result)
	}
}

func TestQuotedTildeRedirectionFailsClosedForRegisteredRelativePath(t *testing.T) {
	fixture := newFixture(t, true, false)
	t.Setenv("FAKE_ARTIFACT_TILDE", "1")
	registered := filepath.Join(fixture.root, "~", "docs")
	if err := os.MkdirAll(registered, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(registered, "vision.md"), []byte("vision"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := fixture.call(roleOther, "bash", map[string]any{"command": "echo x > '~/docs/vision.md'"})
	if result.allow || !strings.Contains(result.reason, "redirection target could not be inspected safely") {
		t.Fatalf("quoted tilde redirect decision = %+v", result)
	}
}

func TestEarsManagerArtifactPathCannotEscapeThroughSymlink(t *testing.T) {
	fixture := newFixture(t, true, false)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(fixture.root, "docs", "escape")); err != nil {
		t.Fatal(err)
	}
	command := "ears-manager --output json artifact put --change-set CS-00003 --id vision --kind vision --path docs/escape/content.md --owner user --content-stdin <<'EOF'\ncontent\nEOF"
	result := fixture.call(roleDraftingTable, "bash", map[string]any{"command": command})
	if result.allow || !strings.Contains(result.reason, "escape the project root") {
		t.Fatalf("artifact symlink escape decision = %+v", result)
	}
}

func TestRegisteredDirectoryProtectsDescendants(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "specification")
	state := projectState{
		root:      root,
		artifacts: []artifact{{ID: "bundle", Path: directory, IsDirectory: true}},
	}
	if !guardedWritePath(state, filepath.Join(directory, "nested", "file.md")) {
		t.Fatal("registered directory descendant was not guarded")
	}
}

func TestCredentialsSymlinkEscapeAndSearch(t *testing.T) {
	fixture := newFixture(t, true, false)
	for _, path := range []string{".env", ".env.local", ".netrc", ".npmrc", ".pypirc", ".git-credentials", "id_ed25519", "a.pem", "a.key", "a.p12", "a.pfx", "a.jks", "a-secret", ".aws/credentials", ".kube/config", ".docker/config.json", ".ssh/config", ".gnupg/private"} {
		full := filepath.Join(fixture.root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("secret"), 0o600); err != nil {
			t.Fatal(err)
		}
		result := fixture.call(roleDraftingTable, "read", map[string]any{"filePath": path})
		if result.allow || !strings.Contains(result.reason, "credential") {
			t.Errorf("%s: %+v", path, result)
		}
	}
	outside := t.TempDir()
	external := filepath.Join(outside, "ordinary.txt")
	if err := os.WriteFile(external, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(fixture.root, "docs", "outside")); err != nil {
		t.Fatal(err)
	}
	result := fixture.call(roleDraftingTable, "read", map[string]any{"filePath": "docs/outside"})
	if result.allow || !strings.Contains(result.reason, "outside the project") {
		t.Fatalf("escape = %+v", result)
	}
	for _, path := range []string{".git/config", ".protobot/change-sets/cs-00002.yaml"} {
		result = fixture.call(roleDraftingTable, "read", map[string]any{"filePath": path})
		if result.allow {
			t.Errorf("denied project read %q = %+v", path, result)
		}
	}
	result = fixture.call(roleDraftingTable, "glob", map[string]any{"path": fixture.root, "pattern": "**/*"})
	if result.allow {
		t.Fatalf("search at project root = %+v", result)
	}
	ghCredentials := filepath.Join(fixture.home, ".config", "gh")
	if err := os.MkdirAll(ghCredentials, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ghCredentials, "hosts.yml"), []byte("token"), 0o600); err != nil {
		t.Fatal(err)
	}
	result = fixture.call(roleDraftingTable, "read", map[string]any{"filePath": "~/.config/gh/hosts.yml"})
	if result.allow || !strings.Contains(result.reason, "outside the project") {
		t.Fatalf("Git host credential path = %+v", result)
	}
	if err := os.Symlink(filepath.Join(fixture.root, "docs", "vision.md"), filepath.Join(fixture.root, "alias")); err != nil {
		t.Fatal(err)
	}
	result = fixture.call(roleOther, "write", map[string]any{"filePath": "alias"})
	if result.allow || !strings.Contains(result.reason, "registered artifact") {
		t.Fatalf("alias write = %+v", result)
	}
	if err := os.MkdirAll(filepath.Join(fixture.root, "docs", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(fixture.root, "docs", "sub"), filepath.Join(fixture.root, "aliasdir")); err != nil {
		t.Fatal(err)
	}
	result = fixture.call(roleOther, "write", map[string]any{"filePath": "aliasdir/../vision.md"})
	if result.allow || !strings.Contains(result.reason, "registered artifact") {
		t.Fatalf("symlink/parent write = %+v", result)
	}
	result = fixture.call(roleOther, "write", map[string]any{"filePath": "docs/vision.md/../notes.txt"})
	if result.allow {
		t.Fatalf("non-directory traversal write = %+v", result)
	}
	if err := os.MkdirAll(filepath.Join(fixture.root, "docs", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.root, "docs", "nested", ".netrc"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	result = fixture.call(roleDraftingTable, "glob", map[string]any{"path": "docs", "pattern": "**/*.md"})
	if result.allow {
		t.Fatalf("sensitive search = %+v", result)
	}
	unknownSkill := filepath.Join(fixture.root, ".agents", "skills", "pull-request")
	if err := os.MkdirAll(unknownSkill, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unknownSkill, "SKILL.md"), []byte("not a Toolkit skill"), 0o600); err != nil {
		t.Fatal(err)
	}
	result = fixture.call(roleDraftingTable, "glob", map[string]any{"path": filepath.Join(fixture.root, ".agents", "skills"), "pattern": "**/*"})
	if result.allow {
		t.Fatalf("search including an unlisted skill = %+v", result)
	}
}

func TestInspectionFailureClosesWriteAndShellRoutes(t *testing.T) {
	fixture := newFixture(t, true, true)
	result := fixture.call(roleDraftingTable, "bash", map[string]any{"command": "ears-manager --version"})
	if result.allow || !strings.Contains(result.reason, "project inspection failed") {
		t.Fatalf("shell = %+v", result)
	}
	result = fixture.call(roleDraftingTable, "apply_patch", map[string]any{"patchText": "*** Begin Patch\n*** Add File: notes/new.txt\n+x\n*** End Patch"})
	if result.allow || !strings.Contains(result.reason, "project inspection failed") {
		t.Fatalf("write = %+v", result)
	}
	result = fixture.call(roleOther, "write", map[string]any{"filePath": ".protobot/project.yaml"})
	if result.allow || !strings.Contains(result.reason, ".protobot/") {
		t.Fatalf("control write = %+v", result)
	}
	if result := fixture.call(roleOther, "write", map[string]any{"filePath": "notes.txt"}); !result.allow {
		t.Fatalf("unregistered write = %+v", result)
	}
	malformed := newFixture(t, true, false)
	if err := os.Remove(filepath.Join(malformed.root, ".protobot", "project.yaml")); err != nil {
		t.Fatal(err)
	}
	result = malformed.call(roleDraftingTable, "write", map[string]any{"filePath": "notes.txt"})
	if result.allow || !strings.Contains(result.reason, "project inspection failed") {
		t.Fatalf("missing project.yaml write decision = %+v", result)
	}
}

func TestGitInspectionErrorFailsClosedInsteadOfAssumingNoProject(t *testing.T) {
	fixture := newFixture(t, true, false)
	emptyPath := t.TempDir()
	t.Setenv("PATH", emptyPath)
	result := fixture.call(roleOther, "write", map[string]any{"filePath": "notes.txt"})
	if result.allow || !strings.Contains(result.reason, "Git working-tree root could not be inspected") {
		t.Fatalf("Git inspection failure decision = %+v", result)
	}
}

func TestMultilineStorePathAndUserManifestFallback(t *testing.T) {
	fixture := newFixture(t, true, false)
	project := strings.Join([]string{
		"project:", "  id: fixture-project", "repository:", "  canonical_remote: https://example.invalid/repo.git",
		"  default_branch: main", "  branch_prefix: cs/", "  review_mode: single-player", "stores:",
		"    requirements: >-", "      .protobot/records/requirements", "    interfaces: .protobot/records/interfaces",
		"    change_sets: .protobot/records/changes", "artifacts:", "  - id: vision", "    path: docs/vision.md", "",
	}, "\n")
	if err := os.MkdirAll(filepath.Join(fixture.root, ".protobot", "records", "requirements"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.root, ".protobot", "project.yaml"), []byte(project), 0o600); err != nil {
		t.Fatal(err)
	}
	result := fixture.call(roleOther, "write", map[string]any{"filePath": ".protobot/records/requirements/new.yaml"})
	if result.allow || !strings.Contains(result.reason, "configured store") {
		t.Fatalf("store write = %+v", result)
	}

	userFixture := newFixture(t, false, false)
	manifestPath := filepath.Join(userFixture.home, ".agents", "drafting-table.yaml")
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, []byte(manifestYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	if result := userFixture.call(roleDraftingTable, "scm_repo_state", map[string]any{}); !result.allow {
		t.Fatalf("fallback = %+v", result)
	}
	skillRoot := filepath.Join(userFixture.home, ".agents", "skills", "drafting-specifications")
	if err := os.MkdirAll(skillRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillRoot, "SKILL.md"), []byte("skill"), 0o600); err != nil {
		t.Fatal(err)
	}
	result = userFixture.call(roleDraftingTable, "read", map[string]any{"filePath": filepath.Join(skillRoot, "SKILL.md")})
	if !result.allow {
		t.Fatalf("user-scope Toolkit skill read = %+v", result)
	}
	result = userFixture.call(roleDraftingTable, "glob", map[string]any{"path": skillRoot, "pattern": "**/*"})
	if !result.allow {
		t.Fatalf("user-scope Toolkit skill search = %+v", result)
	}
}

func TestNoProjectRoleAndMCPRules(t *testing.T) {
	fixture := newFixture(t, false, false)
	if result := fixture.call(roleDraftingTable, "scm_branch_init", map[string]any{}); !result.allow {
		t.Fatalf("branch_init = %+v", result)
	}
	if result := fixture.call(roleDraftingTable, "bash", map[string]any{"command": "true"}); result.allow {
		t.Fatalf("true = %+v", result)
	}
	if err := runGit(fixture.root, "branch", "-M", "cs/00001-project-init"); err != nil {
		t.Fatal(err)
	}
	command := "ears-manager --output json project init --id fixture --name Fixture --canonical-remote https://example.invalid/repo.git --review-mode single-player"
	if result := fixture.call(roleDraftingTable, "bash", map[string]any{"command": command}); !result.allow {
		t.Fatalf("init = %+v", result)
	}

	fixture = newFixture(t, true, false)
	if result := fixture.call(roleDraftingTable, "scm_repo_state", map[string]any{}); !result.allow {
		t.Fatalf("listed SCM = %+v", result)
	}
	if result := fixture.call(roleDraftingTable, "scm_publish", map[string]any{}); result.allow {
		t.Fatalf("unlisted SCM = %+v", result)
	}
	if result := fixture.call(roleOther, "wms_request_query", map[string]any{}); result.allow {
		t.Fatalf("outside-role WMS = %+v", result)
	}
}

func TestRunStatusAndSingleLineContract(t *testing.T) {
	fixture := newFixture(t, false, false)
	input := `{"hook_event_name":"PreToolUse","session_id":"test","cwd":"` + fixture.root + `","tool_name":"bash","tool_input":{"command":"true"}}`
	var stderr strings.Builder
	if status := Run([]string{"--harness", "opencode", "--role", roleDraftingTable}, strings.NewReader(input), &stderr); status != 2 || strings.Count(stderr.String(), "\n") != 1 {
		t.Fatalf("refusal: status=%d stderr=%q", status, stderr.String())
	}
	stderr.Reset()
	input = strings.Replace(input, `"command":"true"`, `"command":"date -u +%Y-%m-%dT%H:%M:%SZ"`, 1)
	if status := Run([]string{"--harness", "opencode", "--role", roleDraftingTable}, strings.NewReader(input), &stderr); status != 0 || stderr.Len() != 0 {
		t.Fatalf("allow: status=%d stderr=%q", status, stderr.String())
	}
}

func TestGuardExecutableRefusesControlFlowGovernedWrite(t *testing.T) {
	fixture := newFixture(t, true, false)
	binary := filepath.Join(t.TempDir(), "drafting-table-guard")
	build := exec.Command("go", "build", "-o", binary, "./cmd/drafting-table-guard")
	build.Env = safeCommandEnv()
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build guard executable: %v: %s", err, output)
	}
	input, err := json.Marshal(hookInput{
		HookEventName: "PreToolUse",
		SessionID:     "fixture-session",
		CWD:           fixture.root,
		ToolName:      "bash",
		ToolInput: map[string]any{
			"command": "bash -c 'for i in x; do ears-manager --output json requirement add --change-set CS-00001; done'",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary, "--harness", "opencode", "--role", roleOther)
	command.Env = safeCommandEnv()
	command.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err = command.Run()
	if err == nil {
		t.Fatal("guard executable allowed the control-flow governed write")
	}
	exitError, ok := err.(*exec.ExitError)
	if !ok || exitError.ExitCode() != 2 {
		t.Fatalf("guard exit error = %v; stderr=%q", err, stderr.String())
	}
	if stdout.Len() != 0 || strings.Count(stderr.String(), "\n") != 1 || !strings.Contains(stderr.String(), "ears-manager writes are reserved") {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	input, err = json.Marshal(hookInput{
		HookEventName: "PreToolUse",
		SessionID:     "fixture-session",
		CWD:           fixture.root,
		ToolName:      "bash",
		ToolInput: map[string]any{
			"command": "mkdir 1 && echo x >&1/../docs/vision.md",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	command = exec.Command(binary, "--harness", "opencode", "--role", roleOther)
	command.Env = safeCommandEnv()
	command.Stdin = bytes.NewReader(input)
	stdout.Reset()
	stderr.Reset()
	command.Stdout = &stdout
	command.Stderr = &stderr
	err = command.Run()
	if err == nil {
		t.Fatal("guard executable allowed the digit-prefixed combined redirection")
	}
	exitError, ok = err.(*exec.ExitError)
	if !ok || exitError.ExitCode() != 2 {
		t.Fatalf("guard exit error = %v; stderr=%q", err, stderr.String())
	}
	if stdout.Len() != 0 || strings.Count(stderr.String(), "\n") != 1 || !strings.Contains(stderr.String(), "output redirection targets") {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

type fixture struct{ root, home string }

const manifestYAML = "adapter_layout: 1\nentry_point: drafting-table\ntoolkit_skills:\n  - drafting-specifications\n  - eliciting-requirements\ngoverned_commands:\n  - ears-manager\ngoverned_mcp_servers:\n  wms:\n    tools:\n      - request_create\n      - request_query\n  scm:\n    tools:\n      - repo_state\n      - branch_init\n"

func newFixture(t *testing.T, initialized, earsFail bool) fixture {
	t.Helper()
	root, home := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("FAKE_EARS_FAIL", "0")
	t.Setenv("FAKE_EARS_CHANGESET_FAIL", "0")
	t.Setenv("FAKE_ARTIFACT_TILDE", "0")
	if earsFail {
		t.Setenv("FAKE_EARS_FAIL", "1")
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is required")
	}
	fakeBin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fakeBin, "ears-manager"), []byte(fakeEarsManager), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+filepath.Dir(git)+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := runGit(root, "init", "-q"); err != nil {
		t.Fatal(err)
	}
	if err := runGit(root, "branch", "-M", "cs/00003-fixture"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".agents", "drafting-table.yaml"), []byte(manifestYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "vision.md"), []byte("vision"), 0o600); err != nil {
		t.Fatal(err)
	}
	if initialized {
		if err := os.MkdirAll(filepath.Join(root, ".protobot", "change-sets"), 0o755); err != nil {
			t.Fatal(err)
		}
		project := "project:\n  id: fixture-project\nrepository:\n  canonical_remote: https://example.invalid/repo.git\n  default_branch: main\n  branch_prefix: cs/\n  review_mode: single-player\nstores:\n  requirements: .protobot/requirements\n  interfaces: .protobot/interfaces\n  change_sets: .protobot/change-sets\nartifacts:\n  - id: vision\n    path: docs/vision.md\n"
		if err := os.WriteFile(filepath.Join(root, ".protobot", "project.yaml"), []byte(project), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := runGit(root, "config", "user.name", "Guard Fixture"); err != nil {
		t.Fatal(err)
	}
	if err := runGit(root, "config", "user.email", "guard-fixture@example.invalid"); err != nil {
		t.Fatal(err)
	}
	if err := runGit(root, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	if err := runGit(root, "commit", "-qm", "fixture"); err != nil {
		t.Fatal(err)
	}
	return fixture{root: root, home: home}
}

func (f fixture) call(role, tool string, toolInput map[string]any) decision {
	return f.callAt(f.root, role, tool, toolInput)
}

func (f fixture) callAt(cwd, role, tool string, toolInput map[string]any) decision {
	return evaluate("opencode", role, hookInput{HookEventName: "PreToolUse", SessionID: "fixture", CWD: cwd, ToolName: tool, ToolInput: toolInput})
}

func runGit(root string, args ...string) error {
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	command.Env = safeCommandEnv()
	if output, err := command.CombinedOutput(); err != nil {
		return &gitTestError{output: string(output), err: err}
	}
	return nil
}

type gitTestError struct {
	output string
	err    error
}

func (err *gitTestError) Error() string { return err.err.Error() + ": " + err.output }

const fakeEarsManager = "#!/bin/sh\nif [ \"$FAKE_EARS_FAIL\" = \"1\" ]; then exit 9; fi\ncase \"$3:$4\" in\n  artifact:list) if [ \"$FAKE_ARTIFACT_TILDE\" = \"1\" ]; then printf '%s\\n' '{\"schema_version\":1,\"ok\":true,\"command\":\"artifact list\",\"data\":{\"artifacts\":[{\"id\":\"vision\",\"path\":\"~/docs/vision.md\"}]},\"diagnostics\":[],\"mutation\":{\"applied\":false,\"paths\":[]}}'; else printf '%s\\n' '{\"schema_version\":1,\"ok\":true,\"command\":\"artifact list\",\"data\":{\"artifacts\":[{\"id\":\"vision\",\"path\":\"docs/vision.md\"}]},\"diagnostics\":[],\"mutation\":{\"applied\":false,\"paths\":[]}}'; fi ;;\n  change-set:list) if [ \"$FAKE_EARS_CHANGESET_FAIL\" = \"1\" ]; then exit 7; fi; printf '%s\\n' '{\"schema_version\":1,\"ok\":true,\"command\":\"change-set list\",\"data\":{\"change_sets\":[{\"id\":\"CS-00003\",\"status\":\"proposed\"}]},\"diagnostics\":[],\"mutation\":{\"applied\":false,\"paths\":[]}}' ;;\n  *) exit 8 ;;\nesac\n"
