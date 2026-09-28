package specvalidation

import (
	"errors"
	"strings"
	"testing"

	"github.com/redhat-et/protobot/ears-manager/internal/records"
)

func TestParseProjectionAcceptsPolicyAndDirectories(t *testing.T) {
	projection := ParseProjection([]byte("# reviewed policy\nversion: 1\npaths:\n  - path: docs/vision.md\n    class: shared\n  - path: src/\n    class: implementation\n"))
	if len(projection.Diagnostics) > 0 {
		t.Fatalf("diagnostics = %#v", projection.Diagnostics)
	}
	if projection.Classes["docs/vision.md"] != "shared" || projection.Classes["src"] != "implementation" {
		t.Fatalf("classes = %#v", projection.Classes)
	}
	for _, blank := range []string{"", "\n", "# only a comment\n"} {
		if parsed := ParseProjection([]byte(blank)); len(parsed.Diagnostics) > 0 || len(parsed.Classes) != 0 {
			t.Fatalf("blank manifest %q = %#v", blank, parsed)
		}
	}
}

func TestParseProjectionRejectsInvalidManifests(t *testing.T) {
	cases := map[string]string{
		"not a mapping":   "- docs/vision.md\n",
		"paths not list":  "paths: docs/vision.md\n",
		"bare entry":      "paths:\n  - docs/vision.md\n",
		"extra key":       "paths:\n  - path: docs/vision.md\n    class: shared\n    owner: user\n",
		"unknown class":   "paths:\n  - path: docs/vision.md\n    class: public\n",
		"unsafe path":     "paths:\n  - path: ../vision.md\n    class: shared\n",
		"absolute path":   "paths:\n  - path: /etc/passwd\n    class: shared\n",
		"duplicate path":  "paths:\n  - path: docs/\n    class: shared\n  - path: docs\n    class: test\n",
		"alias":           "base: &b shared\npaths:\n  - path: docs/vision.md\n    class: *b\n",
		"duplicate key":   "paths: []\npaths: []\n",
		"non-string path": "paths:\n  - path: 12\n    class: shared\n",
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			projection := ParseProjection([]byte(data))
			if len(projection.Diagnostics) == 0 {
				t.Fatalf("manifest was accepted: %q", data)
			}
			for _, diagnostic := range projection.Diagnostics {
				if diagnostic.Code != "projection.invalid" || diagnostic.Path != ProjectionPath {
					t.Fatalf("diagnostic = %#v", diagnostic)
				}
			}
			if _, _, err := AddSharedClassifications([]byte(data), []string{"docs/new.md"}); !errors.Is(err, ErrProjectionInvalid) {
				t.Fatalf("AddSharedClassifications error = %v, want ErrProjectionInvalid", err)
			}
		})
	}
}

func TestAddSharedClassificationsCreatesSCMCompatibleManifest(t *testing.T) {
	data, changed, err := AddSharedClassifications(nil, []string{"docs/vision.md", "docs/architecture.md", "docs/vision.md"})
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	want := "paths:\n  - path: docs/architecture.md\n    class: shared\n  - path: docs/vision.md\n    class: shared\n"
	if string(data) != want {
		t.Fatalf("manifest =\n%s\nwant\n%s", data, want)
	}
	// The Source Control Manager fixture appends a hand-written entry to the
	// end of the list; the result must still parse.
	appended := string(data) + "  - path: README.md\n    class: implementation\n"
	if parsed := ParseProjection([]byte(appended)); len(parsed.Diagnostics) > 0 || parsed.Classes["README.md"] != "implementation" {
		t.Fatalf("appended manifest = %#v", parsed)
	}
}

func TestAddSharedClassificationsPreservesPolicy(t *testing.T) {
	original := "# reviewed projection policy\nversion: 1\npaths:\n  # implementation code\n  - path: src\n    class: implementation\n  - path: docs/vision.md\n    class: test\n"
	data, changed, err := AddSharedClassifications([]byte(original), []string{"docs/vision.md", "docs/cli.md"})
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	parsed := ParseProjection(data)
	if len(parsed.Diagnostics) > 0 {
		t.Fatalf("diagnostics = %#v", parsed.Diagnostics)
	}
	if parsed.Classes["src"] != "implementation" || parsed.Classes["docs/cli.md"] != "shared" {
		t.Fatalf("classes = %#v", parsed.Classes)
	}
	if parsed.Classes["docs/vision.md"] != "test" {
		t.Fatalf("an existing policy class was overwritten: %#v", parsed.Classes)
	}
	for _, kept := range []string{"# reviewed projection policy", "version: 1", "# implementation code"} {
		if !strings.Contains(string(data), kept) {
			t.Fatalf("manifest lost %q:\n%s", kept, data)
		}
	}

	unchanged, changed, err := AddSharedClassifications(data, []string{"docs/cli.md"})
	if err != nil || changed || string(unchanged) != string(data) {
		t.Fatalf("classified path rewrote the manifest: changed=%v err=%v", changed, err)
	}

	flow, changed, err := AddSharedClassifications([]byte("paths: []\n"), []string{"docs/cli.md"})
	if err != nil || !changed || string(flow) != "paths:\n  - path: docs/cli.md\n    class: shared\n" {
		t.Fatalf("empty flow list update = %q changed=%v err=%v", flow, changed, err)
	}
}

func TestValidateProjectionRequiresSharedClass(t *testing.T) {
	artifacts := []records.ArtifactEntry{
		{ID: "architecture", Kind: records.ArtifactArchitecture, Path: "docs/architecture.md"},
		{ID: "vision", Kind: records.ArtifactVision, Path: "docs/vision.md"},
		{ID: "workflow", Kind: records.ArtifactInterfaceProse, Path: ".github/workflow.md"},
	}
	projection := ParseProjection([]byte("paths:\n  - path: docs/vision.md\n    class: implementation\n"))
	result := Result{}
	validateProjection(&result, Snapshot{Projection: &projection}, artifacts)
	if len(result.Diagnostics) != 2 {
		t.Fatalf("diagnostics = %#v", result.Diagnostics)
	}
	for _, diagnostic := range result.Diagnostics {
		if diagnostic.Code != "projection.unclassified" || !strings.Contains(diagnostic.Message, "shared") {
			t.Fatalf("diagnostic = %#v", diagnostic)
		}
		if strings.Contains(diagnostic.Message, ".github") {
			t.Fatalf("a reserved path was echoed: %#v", diagnostic)
		}
		if diagnostic.Path == "docs/vision.md" && !strings.Contains(diagnostic.Hint, "reviewed .protobot/projection.yaml") {
			t.Fatalf("non-shared class hint = %q", diagnostic.Hint)
		}
		if diagnostic.Path == "docs/architecture.md" && !strings.Contains(diagnostic.Hint, "Register the path through ears-manager") {
			t.Fatalf("unlisted path hint = %q", diagnostic.Hint)
		}
	}

	skipped := Result{}
	validateProjection(&skipped, Snapshot{}, artifacts)
	if len(skipped.Diagnostics) != 0 {
		t.Fatalf("in-memory snapshot without a projection was checked: %#v", skipped.Diagnostics)
	}
}
