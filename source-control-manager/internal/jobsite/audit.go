package jobsite

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// FixtureVersion identifies this reference fixture.
const FixtureVersion = "jobsite-projection/v1"

const (
	EventExport    = "export"
	EventPatch     = "patch"
	DecisionAccept = "accept"
	DecisionReject = "reject"
)

// AuditRecord is the deterministic private audit record for an export or
// patch decision. It does not carry credentials, remote URLs, peer source,
// raw Worker logs, or raw Inspector findings.
type AuditRecord struct {
	EventType         string `json:"event_type"`
	PolicyVersion     int    `json:"policy_version"`
	PolicyDigest      string `json:"policy_digest"`
	SourceCommit      string `json:"source_commit"`
	Role              string `json:"role,omitempty"`
	WorkerRoot        string `json:"worker_root,omitempty"`
	VisiblePathDigest string `json:"visible_path_digest,omitempty"`
	DeniedPathDigest  string `json:"denied_path_digest,omitempty"`
	BundleDigest      string `json:"bundle_digest,omitempty"`
	IntegrationBase   string `json:"integration_base,omitempty"`
	Cycle             int    `json:"cycle"`
	WorkItem          string `json:"work_item,omitempty"`
	Decision          string `json:"decision"`
	RejectionReason   string `json:"rejection_reason,omitempty"`
	ResultingCommit   string `json:"resulting_commit,omitempty"`
	FixtureVersion    string `json:"fixture_version"`
	BackendVersion    string `json:"backend_version"`
}

func pathDigest(paths []string) string {
	sorted := append([]string(nil), paths...)
	sort.Strings(sorted)
	sum := sha256.Sum256([]byte(strings.Join(sorted, "\n")))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func writeAudit(dir string, name string, record AuditRecord) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(filepath.Join(dir, name), data, 0o600)
}

func newExportAudit(policy Policy, sourceCommit, role, workerRoot string, visible, denied []string, cycle int, workItem, decision, reason, resulting string) AuditRecord {
	return AuditRecord{
		EventType:         EventExport,
		PolicyVersion:     policy.Version,
		PolicyDigest:      policy.Digest,
		SourceCommit:      sourceCommit,
		Role:              role,
		WorkerRoot:        workerRoot,
		VisiblePathDigest: pathDigest(visible),
		DeniedPathDigest:  pathDigest(denied),
		Cycle:             cycle,
		WorkItem:          workItem,
		Decision:          decision,
		RejectionReason:   reason,
		ResultingCommit:   resulting,
		FixtureVersion:    FixtureVersion,
		BackendVersion:    FixtureVersion,
	}
}

func newPatchAudit(policy Policy, sourceCommit, role, workerRoot, bundleDigest, integrationBase string, cycle int, workItem, decision, reason, resulting string) AuditRecord {
	return AuditRecord{
		EventType:       EventPatch,
		PolicyVersion:   policy.Version,
		PolicyDigest:    policy.Digest,
		SourceCommit:    sourceCommit,
		Role:            role,
		WorkerRoot:      workerRoot,
		BundleDigest:    bundleDigest,
		IntegrationBase: integrationBase,
		Cycle:           cycle,
		WorkItem:        workItem,
		Decision:        decision,
		RejectionReason: reason,
		ResultingCommit: resulting,
		FixtureVersion:  FixtureVersion,
		BackendVersion:  FixtureVersion,
	}
}
