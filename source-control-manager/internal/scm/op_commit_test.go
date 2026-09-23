package scm

import (
	"strings"
	"testing"

	"github.com/redhat-et/protobot/source-control-manager/internal/ears"
	"github.com/redhat-et/protobot/source-control-manager/internal/project"
)

func TestClassifyDigests(t *testing.T) {
	config := &project.Config{
		Artifacts: []project.Artifact{
			{ID: "vision", Path: "docs/vision.md", Owner: "user"},
			{ID: "architecture", Path: "docs/architecture.md", Owner: "user"},
		},
		Stores: project.Stores{
			Requirements: ".protobot/requirements",
			Interfaces:   ".protobot/interfaces",
			ChangeSets:   ".protobot/change-sets",
		},
	}
	fileSet := []string{".protobot/change-sets/cs-00002.yaml", ".protobot/project.yaml", "docs/vision.md"}
	cases := []struct {
		name       string
		diagnostic ears.Diagnostic
		inSet      string
		store      bool
		outside    string
		otherError bool
	}{
		{
			// The validator of #108 names project.yaml, where the digest
			// lives, and the artifact by its ID.
			name: "artifact by record_id, in the file set",
			diagnostic: ears.Diagnostic{Code: "artifact.digest_mismatch", Severity: "error", Path: ".protobot/project.yaml",
				RecordID: "vision", Field: "artifacts[id=vision].digest"},
			inSet: "docs/vision.md",
		},
		{
			name: "artifact by record_id, outside the file set",
			diagnostic: ears.Diagnostic{Code: "artifact.digest_mismatch", Severity: "error", Path: ".protobot/project.yaml",
				RecordID: "architecture", Field: "artifacts[id=architecture].digest"},
			outside: "docs/architecture.md",
		},
		{
			name:       "artifact by an unknown record_id",
			diagnostic: ears.Diagnostic{Code: "artifact.digest_mismatch", Severity: "error", Path: ".protobot/project.yaml", RecordID: "gone"},
			otherError: true,
		},
		{
			name:       "artifact by its own path, the older shape",
			diagnostic: ears.Diagnostic{Code: "artifact.digest_mismatch", Severity: "error", Path: "docs/vision.md"},
			inSet:      "docs/vision.md",
		},
		{
			// project.yaml alone never names the mismatching file, so it
			// is never offered for a discard.
			name:       "artifact by project.yaml and no record_id",
			diagnostic: ears.Diagnostic{Code: "artifact.digest_mismatch", Severity: "error", Path: ".protobot/project.yaml"},
			otherError: true,
		},
		{
			name: "the change-set store, which holds the manifest",
			diagnostic: ears.Diagnostic{Code: "project.store_digest_mismatch", Severity: "error", Path: ".protobot/project.yaml",
				Field: "store_digests.change_sets"},
			inSet: ".protobot/change-sets",
			store: true,
		},
		{
			// #34 compares every structured store, not only those that
			// the change set touches.
			name: "the requirement store, which holds no path of the file set",
			diagnostic: ears.Diagnostic{Code: "project.store_digest_mismatch", Severity: "error", Path: ".protobot/project.yaml",
				Field: "store_digests.requirements"},
			inSet: ".protobot/requirements",
			store: true,
		},
		{
			name: "a store field that names no store",
			diagnostic: ears.Diagnostic{Code: "project.store_digest_mismatch", Severity: "error", Path: ".protobot/project.yaml",
				Field: "store_digests.other"},
			otherError: true,
		},
		{
			name:       "another error",
			diagnostic: ears.Diagnostic{Code: "requirement.ears_pattern_mismatch", Severity: "error", Path: ".protobot/requirements/REQ-X-00001.yaml"},
			otherError: true,
		},
		{
			name:       "a warning",
			diagnostic: ears.Diagnostic{Code: "artifact.digest_mismatch", Severity: "warning", Path: ".protobot/project.yaml", RecordID: "vision"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			inSet, stores, outside, otherError := classifyDigests([]ears.Diagnostic{c.diagnostic}, config, fileSet)
			wantStores := ""
			if c.store {
				wantStores = c.inSet
			}
			if strings.Join(inSet, ",") != c.inSet || strings.Join(stores, ",") != wantStores ||
				strings.Join(outside, ",") != c.outside || otherError != c.otherError {
				t.Fatalf("classifyDigests = %v, %v, %v, %v; want %q, %q, %q, %v",
					inSet, stores, outside, otherError, c.inSet, wantStores, c.outside, c.otherError)
			}
		})
	}
}
