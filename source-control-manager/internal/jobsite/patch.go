package jobsite

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// PatchBundle actions.
const (
	ActionCreate = "create"
	ActionUpdate = "update"
	ActionDelete = "delete"
)

// PatchBundle file modes.
const (
	ModeFile = "100644"
	ModeExec = "100755"
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
// bundle to Integration.
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
// and forbidden bundles are rejected atomically. When a bundle is rejected,
// ApplyPatch writes the rejection audit record and returns a non-nil *PatchResult
// carrying Decision alongside the rejection error. Infrastructure errors (such
// as Git open failures or apply failures) return a nil result and non-nil error.
func ApplyPatch(integration Repository, policy Policy, bundle PatchBundle) (*PatchResult, error) {
	auditDir := filepath.Join(filepath.Dir(integration.Path), auditDirName)
	reject := func(code, reason string) (*PatchResult, error) {
		record := newPatchAudit(policy, bundle.SourceCommit, bundle.Role, bundle.WorkerRoot, bundle.BundleDigest, bundle.IntegrationBase, bundle.Cycle, bundle.WorkItem, DecisionReject, reason, "")
		uniqueName, err := uniqueAuditName(auditDir, "rejected", bundle.BundleDigest)
		if err != nil {
			return nil, err
		}
		if err := writeAuditExcl(auditDir, uniqueName, record); err != nil {
			return nil, err
		}
		if err := writeAudit(auditDir, "patch-rejected.json", record); err != nil {
			_ = os.Remove(filepath.Join(auditDir, uniqueName))
			return nil, err
		}
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
	if len(bundle.Operations) == 0 {
		return reject(CodePatchRejected, "PatchBundle must contain at least one operation.")
	}
	if reason := verifyOperationContents(bundle.Operations); reason != "" {
		return reject(CodePatchTampered, reason)
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

	commit, err := buildCommit(repo, head, bundle)
	if err != nil {
		return nil, err
	}

	if err := repo.updateRefCAS("refs/heads/"+branchMain, commit, head); err != nil {
		if repo.isCASMismatch(err, head) {
			return reject(CodePatchStale, "PatchBundle integration base is stale.")
		}
		return nil, err
	}

	var writtenUnique string
	handleApplyError := func(origErr error) error {
		if rbErr := rollbackIntegration(repo, auditDir, writtenUnique, head); rbErr != nil {
			return fail(CodePatchRollbackFailed, fmt.Sprintf("Failed to restore Integration HEAD to %s during rollback: %v.", head, rbErr))
		}
		return origErr
	}

	if err := repo.checkout(branchMain); err != nil {
		return nil, handleApplyError(err)
	}
	if err := repo.scrubMeta(); err != nil {
		return nil, handleApplyError(err)
	}

	record := newPatchAudit(policy, bundle.SourceCommit, bundle.Role, bundle.WorkerRoot, bundle.BundleDigest, bundle.IntegrationBase, bundle.Cycle, bundle.WorkItem, DecisionAccept, "", commit)
	uniqueName, err := uniqueAuditName(auditDir, "accepted", bundle.BundleDigest)
	if err != nil {
		return nil, handleApplyError(err)
	}
	if err := writeAuditExcl(auditDir, uniqueName, record); err != nil {
		return nil, handleApplyError(err)
	}
	writtenUnique = uniqueName
	if err := writeAudit(auditDir, "patch-accepted.json", record); err != nil {
		return nil, handleApplyError(err)
	}
	return &PatchResult{Decision: record, Commit: commit}, nil
}

func rollbackIntegration(repo *gitRepo, auditDir, writtenUnique, head string) error {
	if writtenUnique != "" {
		_ = os.Remove(filepath.Join(auditDir, writtenUnique))
	}
	var errs []error
	if err := repo.updateRef("refs/heads/"+branchMain, head); err != nil {
		errs = append(errs, err)
	}
	if err := repo.checkout(branchMain); err != nil {
		errs = append(errs, err)
	}
	currentHead, err := repo.head()
	if err != nil {
		errs = append(errs, err)
	} else if currentHead != head {
		errs = append(errs, fmt.Errorf("integration HEAD is %s, expected %s", currentHead, head))
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func normalizeOperations(ops []Operation) ([]Operation, error) {
	if len(ops) == 0 {
		return nil, fail(CodePatchRejected, "PatchBundle must contain at least one operation.")
	}
	out := append([]Operation(nil), ops...)
	for i := range out {
		if out[i].Action != ActionDelete && out[i].ContentDigest == "" {
			out[i].ContentDigest = digestBytes(out[i].Content)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func verifyOperationContents(ops []Operation) string {
	for _, op := range ops {
		if op.Action == ActionDelete {
			continue
		}
		if digestBytes(op.Content) != op.ContentDigest {
			return fmt.Sprintf("Patch bundle content digest does not match operation %s.", op.Path)
		}
	}
	return ""
}

func isPrefixOrDescendant(a, b string) bool {
	af := strings.ToLower(a)
	bf := strings.ToLower(b)
	return strings.HasPrefix(af, bf+"/") || strings.HasPrefix(bf, af+"/")
}

func validateOperations(policy Policy, bundle PatchBundle, present map[string]blobEntry) string {
	if len(bundle.Operations) == 0 {
		return "PatchBundle must contain at least one operation."
	}
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
		if _, err := canonicalPath(op.Path); err != nil || hasGitComponent(op.Path) {
			return "PatchBundle path is unsafe."
		}
		if op.Action == ActionCreate || op.Action == ActionUpdate {
			for existingPath := range present {
				if isPrefixOrDescendant(op.Path, existingPath) {
					return "PatchBundle path collides with an existing path."
				}
			}
			for _, other := range bundle.Operations {
				if other.Path != op.Path && other.Action != ActionDelete {
					if isPrefixOrDescendant(op.Path, other.Path) {
						return "PatchBundle path collides with another operation."
					}
				}
			}
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

func buildCommit(repo *gitRepo, head string, bundle PatchBundle) (string, error) {
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
	return repo.commitTreeWithParent(tree, head, message)
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
