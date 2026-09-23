package project

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

func writeConfigText(t *testing.T, dir, text string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "project.yaml"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeConfig(t *testing.T, dir string) {
	t.Helper()
	writeConfigText(t, dir, validConfig)
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

// The persisted configuration follows the same branch-namespace rules as
// the branch_init request: a default branch that reads as a change-set
// branch would let the ref policy permit a write to it.
func TestResolveRefusesABranchNamespaceClash(t *testing.T) {
	cases := map[string]string{
		"the default branch is inside the change-set prefix": "cs/00001-main|cs/",
		"the default branch is the reserved namespace":       "wi|cs/",
		"the default branch is under the reserved namespace": "wi/main|cs/",
		"the branch prefix is the reserved namespace":        "main|wi/",
		"the branch prefix is under the reserved namespace":  "main|wi/cs/",
	}
	for name, fields := range cases {
		t.Run(name, func(t *testing.T) {
			branch, prefix, _ := strings.Cut(fields, "|")
			config := strings.Replace(validConfig, "default_branch: main", "default_branch: "+branch, 1)
			config = strings.Replace(config, "branch_prefix: cs/", "branch_prefix: "+prefix, 1)
			root := gitInit(t)
			writeConfigText(t, filepath.Join(root, ".protobot"), config)
			_, failure := Resolve(root)
			if failure == nil || failure.Code != result.ProjectUnreadable {
				t.Fatalf("Resolve = %v, want %s", failure, result.ProjectUnreadable)
			}
		})
	}
}

// A branch that only starts with the same letters is not inside the
// prefix, and stays valid.
func TestResolveTakesANeighbourOfThePrefix(t *testing.T) {
	root := gitInit(t)
	config := strings.Replace(validConfig, "default_branch: main", "default_branch: csmain", 1)
	writeConfigText(t, filepath.Join(root, ".protobot"), config)
	p, failure := Resolve(root)
	if failure != nil || !p.Initialized {
		t.Fatalf("Resolve = %+v, %v", p, failure)
	}
}
