package jobsite

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	workerADir       = "worker-a"
	workerBDir       = "worker-b"
	integrationDir   = "integration"
	auditDirName     = "audit"
	projectionPath   = ".protobot/projection.yaml"
	syntheticMessage = "projection snapshot"
)

// ExportRequest is the input of a source-commit projection export.
type ExportRequest struct {
	SourceRoot   string
	SourceCommit string
	Policy       []byte
	PolicyDigest string
	OutputDir    string
	WorkItem     string
	Cycle        int
}

// Repository is one exported Git repository.
type Repository struct {
	Path       string
	RootCommit string
	Role       string
}

// ExportResult is a successful export of Worker A, Worker B, and the
// private Integration baseline.
type ExportResult struct {
	Policy       Policy
	SourceCommit string
	WorkerA      Repository
	WorkerB      Repository
	Integration  Repository
	Visible      map[string][]string
	Denied       map[string][]string
	Audits       []AuditRecord
}

// Export creates Worker A, Worker B, and Integration repositories from a
// source commit and a version-1 projection policy. Invalid policies fail
// closed before any repository is created or replaced.
func Export(req ExportRequest) (*ExportResult, error) {
	source, err := openGit(req.SourceRoot)
	if err != nil {
		return nil, err
	}
	defer source.Close()

	sourceCommit, err := source.resolveCommit(req.SourceCommit)
	if err != nil {
		return nil, err
	}
	policyData := req.Policy
	if len(policyData) == 0 {
		policyData, err = source.readFileAt(sourceCommit, projectionPath)
		if err != nil {
			return nil, fail(CodePolicyInvalid, "Projection policy is missing from the source commit.")
		}
	}
	policy, err := ParsePolicy(policyData)
	if err != nil {
		return nil, err
	}
	if req.PolicyDigest != "" && req.PolicyDigest != policy.Digest {
		return nil, fail(CodePolicyDigest, "Projection policy digest does not match the supplied policy.")
	}

	blobs, err := source.listBlobs(sourceCommit)
	if err != nil {
		return nil, err
	}
	if err := validateSourceTree(blobs, policy); err != nil {
		return nil, err
	}

	if err := os.MkdirAll(req.OutputDir, 0o755); err != nil {
		return nil, err
	}
	for _, name := range []string{workerADir, workerBDir, integrationDir} {
		target := filepath.Join(req.OutputDir, name)
		if _, err := os.Lstat(target); err == nil {
			return nil, fail(CodeExportExists, fmt.Sprintf("Export path %s already exists.", name))
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}

	visible := map[string][]string{}
	denied := map[string][]string{}
	selected := map[string][]blobEntry{}
	for _, role := range []string{RoleWorkerA, RoleWorkerB} {
		var keep []blobEntry
		var seen, omitted []string
		for _, blob := range blobs {
			if policy.VisibleTo(role, blob.Path) {
				seen = append(seen, blob.Path)
				keep = append(keep, blob)
				continue
			}
			omitted = append(omitted, blob.Path)
		}
		visible[role] = seen
		denied[role] = omitted
		selected[role] = keep
	}

	integrationPath := filepath.Join(req.OutputDir, integrationDir)
	integration, err := exportIntegration(source.path, sourceCommit, integrationPath)
	if err != nil {
		cleanupExport(req.OutputDir)
		return nil, err
	}

	workerA, err := exportWorker(source, selected[RoleWorkerA], filepath.Join(req.OutputDir, workerADir), RoleWorkerA)
	if err != nil {
		cleanupExport(req.OutputDir)
		return nil, err
	}
	workerB, err := exportWorker(source, selected[RoleWorkerB], filepath.Join(req.OutputDir, workerBDir), RoleWorkerB)
	if err != nil {
		cleanupExport(req.OutputDir)
		return nil, err
	}

	audits := []AuditRecord{
		newExportAudit(policy, sourceCommit, RoleWorkerA, workerA.RootCommit, visible[RoleWorkerA], denied[RoleWorkerA], req.Cycle, req.WorkItem, DecisionAccept, "", workerA.RootCommit),
		newExportAudit(policy, sourceCommit, RoleWorkerB, workerB.RootCommit, visible[RoleWorkerB], denied[RoleWorkerB], req.Cycle, req.WorkItem, DecisionAccept, "", workerB.RootCommit),
		newExportAudit(policy, sourceCommit, RoleIntegration, integration.RootCommit, nil, nil, req.Cycle, req.WorkItem, DecisionAccept, "", integration.RootCommit),
	}
	auditDir := filepath.Join(req.OutputDir, auditDirName)
	for _, record := range audits {
		name := fmt.Sprintf("export-%s.json", record.Role)
		if err := writeAudit(auditDir, name, record); err != nil {
			cleanupExport(req.OutputDir)
			return nil, err
		}
	}

	return &ExportResult{
		Policy:       policy,
		SourceCommit: sourceCommit,
		WorkerA:      *workerA,
		WorkerB:      *workerB,
		Integration:  *integration,
		Visible:      visible,
		Denied:       denied,
		Audits:       audits,
	}, nil
}

func validateSourceTree(blobs []blobEntry, policy Policy) error {
	for _, blob := range blobs {
		if _, err := canonicalPath(blob.Path); err != nil {
			return fail(CodeExportUnsafe, "Source tree contains an unsafe path.")
		}
		if !policy.VisibleTo(RoleWorkerA, blob.Path) && !policy.VisibleTo(RoleWorkerB, blob.Path) {
			continue
		}
		if blob.Mode != "100644" && blob.Mode != "100755" {
			return fail(CodeExportUnsafe, fmt.Sprintf("Visible path %s is not a regular file.", blob.Path))
		}
	}
	return nil
}

func exportIntegration(sourcePath, sourceCommit, dest string) (*Repository, error) {
	repo, err := initRepo(dest)
	if err != nil {
		return nil, err
	}
	defer repo.Close()
	if err := repo.fetchCommit(sourcePath, sourceCommit, "refs/heads/"+branchMain); err != nil {
		return nil, fail(CodeExportSource, "Integration fetch of the source commit failed.")
	}
	if err := repo.checkout(branchMain); err != nil {
		return nil, err
	}
	if err := repo.scrubMeta(); err != nil {
		return nil, err
	}
	head, err := repo.head()
	if err != nil {
		return nil, err
	}
	if remotes, err := repo.remotes(); err != nil {
		return nil, err
	} else if len(remotes) != 0 {
		return nil, fail(CodeExportUnsafe, "Integration repository has a remote.")
	}
	if has, err := repo.hasAlternates(); err != nil {
		return nil, err
	} else if has {
		return nil, fail(CodeExportUnsafe, "Integration repository has Git alternates.")
	}
	return &Repository{Path: dest, RootCommit: head, Role: RoleIntegration}, nil
}

func exportWorker(source *gitRepo, files []blobEntry, dest, role string) (*Repository, error) {
	repo, err := initRepo(dest)
	if err != nil {
		return nil, err
	}
	defer repo.Close()

	copied := make([]blobEntry, 0, len(files))
	for _, file := range files {
		content, err := source.readBlob(file.OID)
		if err != nil {
			return nil, err
		}
		oid, err := repo.writeBlob(content)
		if err != nil {
			return nil, err
		}
		copied = append(copied, blobEntry{Path: file.Path, Mode: file.Mode, OID: oid})
	}
	sort.Slice(copied, func(i, j int) bool { return copied[i].Path < copied[j].Path })
	tree, err := repo.writeTree(copied)
	if err != nil {
		return nil, err
	}
	commit, err := repo.commitTree(tree, syntheticMessage)
	if err != nil {
		return nil, err
	}
	if err := repo.updateRef("refs/heads/"+branchMain, commit); err != nil {
		return nil, err
	}
	if err := repo.checkout(branchMain); err != nil {
		return nil, err
	}
	if err := repo.scrubMeta(); err != nil {
		return nil, err
	}
	if remotes, err := repo.remotes(); err != nil {
		return nil, err
	} else if len(remotes) != 0 {
		return nil, fail(CodeExportUnsafe, "Worker repository has a remote.")
	}
	if has, err := repo.hasAlternates(); err != nil {
		return nil, err
	} else if has {
		return nil, fail(CodeExportUnsafe, "Worker repository has Git alternates.")
	}
	parents, err := repo.text("rev-list", "--parents", "-n", "1", commit)
	if err != nil {
		return nil, err
	}
	if fields := strings.Fields(parents); len(fields) != 1 {
		return nil, fail(CodeExportUnsafe, "Worker repository root is not a parentless synthetic commit.")
	}
	return &Repository{Path: dest, RootCommit: commit, Role: role}, nil
}

func cleanupExport(outputDir string) {
	for _, name := range []string{workerADir, workerBDir, integrationDir, auditDirName} {
		_ = os.RemoveAll(filepath.Join(outputDir, name))
	}
}
