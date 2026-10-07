package jobsite

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

const (
	ActionCreate = "create"
	ActionUpdate = "update"
	ActionDelete = "delete"
	ModeFile     = "100644"
	ModeExec     = "100755"
)

// Operation is one path-restricted PatchBundle v1 change.
type Operation struct {
	Path          string `json:"path"`
	Action        string `json:"action"`
	Mode          string `json:"mode,omitempty"`
	ContentDigest string `json:"content_digest,omitempty"`
	Content       []byte `json:"-"`
}

// PatchBundle is the version-1 content-addressed patch format.
type PatchBundle struct {
	Version         int         `json:"version"`
	Role            string      `json:"role"`
	WorkItem        string      `json:"work_item"`
	Cycle           int         `json:"cycle"`
	PolicyDigest    string      `json:"policy_digest"`
	SourceCommit    string      `json:"source_commit"`
	WorkerRoot      string      `json:"worker_root"`
	IntegrationBase string      `json:"integration_base"`
	Operations      []Operation `json:"operations"`
	BundleDigest    string      `json:"bundle_digest"`
}

// PatchResult is the outcome of validating and, when accepted, applying a
// bundle in a temporary Integration worktree.
type PatchResult struct {
	Decision AuditRecord
	Commit   string
}

// NewPatchBundle builds a version-1 bundle and fills its digest.
func NewPatchBundle(role, workItem string, cycle int, policyDigest, sourceCommit, workerRoot, integrationBase string, ops []Operation) (PatchBundle, error) {
	normalized, err := normalizeOperations(ops)
	if err != nil {
		return PatchBundle{}, err
	}
	bundle := PatchBundle{
		Version:         1,
		Role:            role,
		WorkItem:        workItem,
		Cycle:           cycle,
		PolicyDigest:    policyDigest,
		SourceCommit:    sourceCommit,
		WorkerRoot:      workerRoot,
		IntegrationBase: integrationBase,
		Operations:      normalized,
	}
	digest, err := bundleDigest(bundle)
	if err != nil {
		return PatchBundle{}, err
	}
	bundle.BundleDigest = digest
	return bundle, nil
}

// ApplyPatch validates a PatchBundle v1 and, if every operation is allowed,
// applies it to Integration without importing Worker objects. Mixed allowed
// and forbidden bundles are rejected atomically.
func ApplyPatch(integration Repository, policy Policy, bundle PatchBundle) (*PatchResult, error) {
	auditDir := filepath.Join(filepath.Dir(integration.Path), auditDirName)
	reject := func(code, reason string) (*PatchResult, error) {
		record := newPatchAudit(policy, bundle.SourceCommit, bundle.Role, bundle.WorkerRoot, bundle.BundleDigest, bundle.IntegrationBase, bundle.Cycle, bundle.WorkItem, DecisionReject, reason, "")
		_ = writeAudit(auditDir, "patch-rejected.json", record)
		return &PatchResult{Decision: record}, fail(code, reason)
	}

	if bundle.Version != 1 {
		return reject(CodePatchRejected, "PatchBundle version is unsupported.")
	}
	if bundle.Role != RoleWorkerA && bundle.Role != RoleWorkerB {
		return reject(CodePatchRejected, "PatchBundle role is not a Worker role.")
	}
	if bundle.PolicyDigest != policy.Digest {
		return reject(CodePatchTampered, "PatchBundle policy digest does not match the export policy.")
	}
	computed, err := bundleDigest(bundle)
	if err != nil {
		return reject(CodePatchTampered, "PatchBundle digest could not be computed.")
	}
	if computed != bundle.BundleDigest {
		return reject(CodePatchTampered, "PatchBundle digest does not match the canonical payload.")
	}
	if err := verifyOperationContents(bundle.Operations); err != nil {
		return reject(CodePatchTampered, err.Error())
	}

	repo, err := openGit(integration.Path)
	if err != nil {
		return nil, err
	}
	defer repo.Close()
	head, err := repo.head()
	if err != nil {
		return nil, err
	}
	if bundle.IntegrationBase != head {
		return reject(CodePatchStale, "PatchBundle integration base is stale.")
	}

	current, err := repo.listBlobs("HEAD")
	if err != nil {
		return nil, err
	}
	present := map[string]blobEntry{}
	for _, blob := range current {
		present[blob.Path] = blob
	}
	if reason := validateOperations(policy, bundle, present); reason != "" {
		return reject(CodePatchRejected, reason)
	}

	commit, err := applyInWorktree(repo, head, bundle)
	if err != nil {
		return reject(CodePatchRejected, err.Error())
	}
	record := newPatchAudit(policy, bundle.SourceCommit, bundle.Role, bundle.WorkerRoot, bundle.BundleDigest, bundle.IntegrationBase, bundle.Cycle, bundle.WorkItem, DecisionAccept, "", commit)
	if err := writeAudit(auditDir, "patch-accepted.json", record); err != nil {
		return nil, err
	}
	return &PatchResult{Decision: record, Commit: commit}, nil
}

func normalizeOperations(ops []Operation) ([]Operation, error) {
	if len(ops) == 0 {
		return nil, fail(CodePatchRejected, "PatchBundle must contain at least one operation.")
	}
	out := append([]Operation(nil), ops...)
	for i := range out {
		if out[i].Action != ActionDelete && len(out[i].Content) > 0 && out[i].ContentDigest == "" {
			out[i].ContentDigest = digestBytes(out[i].Content)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func verifyOperationContents(ops []Operation) error {
	for _, op := range ops {
		if op.Action == ActionDelete {
			continue
		}
		if digestBytes(op.Content) != op.ContentDigest {
			return fmt.Errorf("patch bundle content digest does not match operation %s", op.Path)
		}
	}
	return nil
}

func validateOperations(policy Policy, bundle PatchBundle, present map[string]blobEntry) string {
	seen := map[string]bool{}
	folded := map[string]string{}
	for path := range present {
		folded[strings.ToLower(path)] = path
	}
	for _, op := range bundle.Operations {
		if _, dup := seen[op.Path]; dup {
			return "PatchBundle path is listed more than once."
		}
		seen[op.Path] = true
		if _, err := canonicalPath(op.Path); err != nil {
			return "PatchBundle path is unsafe."
		}
		if strings.HasPrefix(op.Path, ".git/") || op.Path == ".git" {
			return "PatchBundle path is unsafe."
		}
		if existing, ok := folded[strings.ToLower(op.Path)]; ok && existing != op.Path && op.Action != ActionDelete {
			return "PatchBundle path collides by case with an existing path."
		}
		if previous, ok := folded[strings.ToLower(op.Path)]; ok && previous != op.Path && seen[previous] {
			return "PatchBundle path collides by case with another operation."
		}
		folded[strings.ToLower(op.Path)] = op.Path
		if !policy.WritableBy(bundle.Role, op.Path) {
			switch policy.Classify(op.Path) {
			case ClassShared:
				return "PatchBundle path is shared and read-only to Workers."
			case ClassUnclassified:
				return "PatchBundle path is unclassified and denied by default."
			default:
				return "PatchBundle path is outside the Worker role allowlist."
			}
		}
		switch op.Action {
		case ActionCreate:
			if _, exists := present[op.Path]; exists {
				return "PatchBundle create targets a path that already exists."
			}
			if op.Mode != ModeFile && op.Mode != ModeExec {
				return "PatchBundle mode must be 100644 or 100755."
			}
		case ActionUpdate:
			if _, exists := present[op.Path]; !exists {
				return "PatchBundle update targets a path that does not exist."
			}
			if op.Mode != ModeFile && op.Mode != ModeExec {
				return "PatchBundle mode must be 100644 or 100755."
			}
		case ActionDelete:
			if _, exists := present[op.Path]; !exists {
				return "PatchBundle delete targets a path that does not exist."
			}
			if op.Mode != "" {
				return "PatchBundle delete must not set a file mode."
			}
		default:
			return "PatchBundle action must be create, update, or delete."
		}
	}
	return ""
}

func applyInWorktree(repo *gitRepo, head string, bundle PatchBundle) (string, error) {
	current, err := repo.listBlobs(head)
	if err != nil {
		return "", err
	}
	files := map[string]blobEntry{}
	for _, blob := range current {
		files[blob.Path] = blob
	}
	for _, op := range bundle.Operations {
		switch op.Action {
		case ActionDelete:
			delete(files, op.Path)
		case ActionCreate, ActionUpdate:
			oid, err := repo.writeBlob(op.Content)
			if err != nil {
				return "", err
			}
			files[op.Path] = blobEntry{Path: op.Path, Mode: op.Mode, OID: oid}
		}
	}
	entries := make([]blobEntry, 0, len(files))
	for _, blob := range files {
		entries = append(entries, blob)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	tree, err := repo.writeTree(entries)
	if err != nil {
		return "", err
	}
	message := fmt.Sprintf("Apply %s patch bundle\n\nProjection-Role: %s\nWorker-Root: %s\nPatch-Digest: %s\nCycle: %d\nWork-Item: %s\nPolicy-Digest: %s\nSource-Commit: %s\n",
		bundle.Role, bundle.Role, bundle.WorkerRoot, bundle.BundleDigest, bundle.Cycle, bundle.WorkItem, bundle.PolicyDigest, bundle.SourceCommit)
	commit, err := repo.commitTreeWithParent(tree, head, message)
	if err != nil {
		return "", err
	}
	if err := repo.updateRef("refs/heads/"+branchMain, commit); err != nil {
		return "", err
	}
	if err := repo.checkout(branchMain); err != nil {
		return "", err
	}
	if err := repo.scrubMeta(); err != nil {
		return "", err
	}
	return commit, nil
}

type canonicalBundle struct {
	Version         int                  `json:"version"`
	Role            string               `json:"role"`
	WorkItem        string               `json:"work_item"`
	Cycle           int                  `json:"cycle"`
	PolicyDigest    string               `json:"policy_digest"`
	SourceCommit    string               `json:"source_commit"`
	WorkerRoot      string               `json:"worker_root"`
	IntegrationBase string               `json:"integration_base"`
	Operations      []canonicalOperation `json:"operations"`
}

type canonicalOperation struct {
	Path          string `json:"path"`
	Action        string `json:"action"`
	Mode          string `json:"mode,omitempty"`
	ContentDigest string `json:"content_digest,omitempty"`
}

func bundleDigest(bundle PatchBundle) (string, error) {
	ops := make([]canonicalOperation, 0, len(bundle.Operations))
	for _, op := range bundle.Operations {
		ops = append(ops, canonicalOperation{
			Path:          op.Path,
			Action:        op.Action,
			Mode:          op.Mode,
			ContentDigest: op.ContentDigest,
		})
	}
	sort.Slice(ops, func(i, j int) bool { return ops[i].Path < ops[j].Path })
	payload := canonicalBundle{
		Version:         bundle.Version,
		Role:            bundle.Role,
		WorkItem:        bundle.WorkItem,
		Cycle:           bundle.Cycle,
		PolicyDigest:    bundle.PolicyDigest,
		SourceCommit:    bundle.SourceCommit,
		WorkerRoot:      bundle.WorkerRoot,
		IntegrationBase: bundle.IntegrationBase,
		Operations:      ops,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
