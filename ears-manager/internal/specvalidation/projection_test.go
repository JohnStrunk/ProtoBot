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
	if projection.Version != 1 || projection.Classes["docs/vision.md"] != "shared" || projection.Classes["src"] != "implementation" {
		t.Fatalf("projection = %#v", projection)
	}
}

func TestParseProjectionRejectsInvalidManifests(t *testing.T) {
	cases := map[string]string{
		"not a mapping":       "- docs/vision.md\n",
		"missing version":     "paths: []\n",
		"string version":      "version: \"1\"\npaths: []\n",
		"unsupported version": "version: 2\npaths: []\n",
		"paths not list":      "version: 1\npaths: docs/vision.md\n",
		"bare entry":          "version: 1\npaths:\n  - docs/vision.md\n",
		"extra key":           "version: 1\npaths:\n  - path: docs/vision.md\n    class: shared\n    owner: user\n",
		"unknown class":       "version: 1\npaths:\n  - path: docs/vision.md\n    class: public\n",
		"unsafe path":         "version: 1\npaths:\n  - path: ../vision.md\n    class: shared\n",
		"absolute path":       "version: 1\npaths:\n  - path: /etc/passwd\n    class: shared\n",
		"duplicate path":      "version: 1\npaths:\n  - path: docs/\n    class: shared\n  - path: docs\n    class: test\n",
		"alias":               "base: &b shared\nversion: 1\npaths:\n  - path: docs/vision.md\n    class: *b\n",
		"duplicate key":       "version: 1\npaths: []\npaths: []\n",
		"non-string path":     "version: 1\npaths:\n  - path: 12\n    class: shared\n",
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
	for _, blank := range []string{"", "\n", "# only a comment\n"} {
		if parsed := ParseProjection([]byte(blank)); len(parsed.Diagnostics) == 0 {
			t.Fatalf("blank manifest %q was accepted", blank)
		}
	}
}

func TestProjectionClassUsesMostSpecificEntry(t *testing.T) {
	projection := ParseProjection([]byte("version: 1\npaths:\n  - path: src/\n    class: implementation\n  - path: src/lib/\n    class: test\n  - path: src/lib/file.go\n    class: shared\n"))
	if len(projection.Diagnostics) > 0 {
		t.Fatalf("diagnostics = %#v", projection.Diagnostics)
	}
	cases := []struct {
		path  string
		want  string
		found bool
	}{
		{path: "src", want: "implementation", found: true},
		{path: "src/main.go", want: "implementation", found: true},
		{path: "src/lib", want: "test", found: true},
		{path: "src/lib/test.go", want: "test", found: true},
		{path: "src/lib/file.go", want: "shared", found: true},
		{path: "srcx/main.go", found: false},
		{path: "docs/other.md", found: false},
	}
	for _, tc := range cases {
		got, found := projectionClass(projection.Classes, tc.path)
		if got != tc.want || found != tc.found {
			t.Errorf("projectionClass(%q) = %q, %v; want %q, %v", tc.path, got, found, tc.want, tc.found)
		}
	}
}

func TestAddSharedClassificationsCreatesVersionedManifest(t *testing.T) {
	data, changed, err := AddSharedClassifications(nil, []string{"docs/vision.md", "docs/architecture.md", "docs/vision.md", "records/requirements/"})
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	want := "version: 1\npaths:\n  - path: docs/architecture.md\n    class: shared\n  - path: docs/vision.md\n    class: shared\n  - path: records/requirements/\n    class: shared\n"
	if string(data) != want {
		t.Fatalf("manifest =\n%s\nwant\n%s", data, want)
	}
	parsed := ParseProjection(data)
	if len(parsed.Diagnostics) > 0 || parsed.Version != 1 || parsed.Classes["records/requirements"] != "shared" {
		t.Fatalf("projection = %#v", parsed)
	}
	// The Source Control Manager fixture appends a hand-written entry to the
	// end of the list; the result must still parse.
	appended := string(data) + "  - path: README.md\n    class: implementation\n"
	if parsed := ParseProjection([]byte(appended)); len(parsed.Diagnostics) > 0 || parsed.Classes["README.md"] != "implementation" {
		t.Fatalf("appended manifest = %#v", parsed)
	}
}

func TestAddSharedClassificationsPreservesPolicyAndHeaderComments(t *testing.T) {
	original := "# reviewed projection policy\n\nversion: 1\npaths:\n  # implementation code\n  - path: src/\n    class: implementation\n  - path: docs/vision.md\n    class: test\n"
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
	if !strings.HasPrefix(string(data), "# reviewed projection policy\n") {
		t.Fatalf("manifest lost its header comment:\n%s", data)
	}
	for _, kept := range []string{"version: 1", "# implementation code"} {
		if !strings.Contains(string(data), kept) {
			t.Fatalf("manifest lost %q:\n%s", kept, data)
		}
	}

	unchanged, changed, err := AddSharedClassifications(data, []string{"docs/cli.md"})
	if err != nil || changed || string(unchanged) != string(data) {
		t.Fatalf("classified path rewrote the manifest: changed=%v err=%v", changed, err)
	}

	flow, changed, err := AddSharedClassifications([]byte("version: 1\npaths: []\n"), []string{"docs/cli.md"})
	if err != nil || !changed || string(flow) != "version: 1\npaths:\n  - path: docs/cli.md\n    class: shared\n" {
		t.Fatalf("empty flow list update = %q changed=%v err=%v", flow, changed, err)
	}
}

func TestAddSharedClassificationsSkipsPathsCoveredByDirectory(t *testing.T) {
	original := "version: 1\npaths:\n  - path: docs/\n    class: shared\n"
	data, changed, err := AddSharedClassifications([]byte(original), []string{"docs/vision.md", "docs/", "records/requirements/"})
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	want := original + "  - path: records/requirements/\n    class: shared\n"
	if string(data) != want {
		t.Fatalf("manifest =\n%s\nwant\n%s", data, want)
	}
}

func TestValidateProjectionRequiresSharedArtifactAndStorePaths(t *testing.T) {
	artifacts := []records.ArtifactEntry{
		{ID: "architecture", Kind: records.ArtifactArchitecture, Path: "docs/architecture.md"},
		{ID: "vision", Kind: records.ArtifactVision, Path: "docs/vision.md"},
		{ID: "workflow", Kind: records.ArtifactInterfaceProse, Path: ".github/workflow.md"},
	}
	stores := records.StorePaths{Requirements: "data/requirements", Interfaces: "data/interfaces", ChangeSets: "data/change-sets"}
	projection := ParseProjection([]byte("version: 1\npaths:\n  - path: docs/vision.md\n    class: implementation\n  - path: data/\n    class: shared\n  - path: data/interfaces/\n    class: implementation\n"))
	result := Result{}
	validateProjection(&result, Snapshot{Projection: &projection}, artifacts, stores)
	if len(result.Diagnostics) != 3 {
		t.Fatalf("diagnostics = %#v", result.Diagnostics)
	}
	for _, diagnostic := range result.Diagnostics {
		if diagnostic.Code != "projection.unclassified" || !strings.Contains(diagnostic.Message, "shared") {
			t.Fatalf("diagnostic = %#v", diagnostic)
		}
		if strings.Contains(diagnostic.Message, ".github") {
			t.Fatalf("a reserved path was echoed: %#v", diagnostic)
		}
		switch diagnostic.Path {
		case "docs/vision.md":
			if diagnostic.Hint != projectionReclassifyHint {
				t.Fatalf("non-shared class hint = %q", diagnostic.Hint)
			}
		case "docs/architecture.md":
			if diagnostic.Hint != projectionRestoreHint {
				t.Fatalf("unlisted path hint = %q", diagnostic.Hint)
			}
		case "data/interfaces/":
			if !strings.Contains(diagnostic.Message, "classified implementation") || diagnostic.Hint != projectionReclassifyHint {
				t.Fatalf("specific store policy did not win: %#v", diagnostic)
			}
		default:
			t.Fatalf("unexpected projection diagnostic: %#v", diagnostic)
		}
	}

	skipped := Result{}
	validateProjection(&skipped, Snapshot{}, artifacts, stores)
	if len(skipped.Diagnostics) != 0 {
		t.Fatalf("in-memory snapshot without a projection was checked: %#v", skipped.Diagnostics)
	}
}
