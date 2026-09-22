package project

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/redhat-et/protobot/source-control-manager/internal/result"
)

const validConfig = `project:
  id: fixture
repository:
  canonical_remote: https://github.com/protobot-fixture/fixture.git
  default_branch: main
  review_mode: single-player
  branch_prefix: cs/
`

func gitInit(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if out, err := exec.Command("git", "init", "--quiet", root).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	return root
}

func writeConfig(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "project.yaml"), []byte(validConfig), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolveReadsAControlDirectory(t *testing.T) {
	root := gitInit(t)
	writeConfig(t, filepath.Join(root, ".protobot"))
	p, failure := Resolve(root)
	if failure != nil {
		t.Fatalf("Resolve: %v", failure)
	}
	if !p.Initialized || p.ID() != "fixture" {
		t.Fatalf("Resolve = %+v", p)
	}
}

func TestResolveRefusesASymlinkedControlDirectory(t *testing.T) {
	root := gitInit(t)
	// A valid project file outside the working tree, behind the link.
	outside := filepath.Join(t.TempDir(), "control")
	writeConfig(t, outside)
	if err := os.Symlink(outside, filepath.Join(root, ".protobot")); err != nil {
		t.Fatal(err)
	}
	_, failure := Resolve(root)
	if failure == nil || failure.Code != result.ProjectUnreadable {
		t.Fatalf("Resolve through a symlinked .protobot = %v, want %s", failure, result.ProjectUnreadable)
	}
}

func TestResolveRefusesAControlFile(t *testing.T) {
	root := gitInit(t)
	if err := os.WriteFile(filepath.Join(root, ".protobot"), []byte("not a directory\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, failure := Resolve(root)
	if failure == nil || failure.Code != result.ProjectUnreadable {
		t.Fatalf("Resolve with a .protobot file = %v, want %s", failure, result.ProjectUnreadable)
	}
}
