package jobsite

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/redhat-et/protobot/source-control-manager/internal/gitx"
)

const (
	fixtureWorkItem       = "WI-78"
	currentImplPath       = "src/app.go"
	historicalImplPath    = "src/historical_secret.go"
	nestedSharedPath      = "src/generated/README.md"
	currentTestPath       = "tests/canonical/app_test.go"
	unclassifiedPath      = "unclassified/secret.txt"
	integrationOnlyPath   = "integration/secrets/token.txt"
	attestationPath       = ".protobot/attestations/note.txt"
	laterWorkItemPath     = "src/later.go"
	sharedDocPath         = "docs/architecture.md"
	fileOnlySharedPath    = "NOTICE"
	credentialSentinel    = "PROTOBOT-FIXTURE-TOKEN"
	historicalSentinel    = "historical-impl-sentinel-78"
	currentImplSentinel   = "current-impl-sentinel-78"
	currentTestSentinel   = "current-test-sentinel-78"
	unclassifiedSentinel  = "unclassified-sentinel-78"
	laterWorkItemSentinel = "later-work-item-sentinel-78"
	nestedSharedSentinel  = "nested-shared-sentinel-78"
)

// SourceFixture is the reference fixture-project used to prove Worker
// isolation before a real execution backend is integrated.
type SourceFixture struct {
	Root               string
	SourceCommit       string
	LaterCommit        string
	Policy             []byte
	PolicyDigest       string
	CurrentImplBlob    string
	HistoricalImplBlob string
	CurrentTestBlob    string
	UnclassifiedBlob   string
	LaterWorkItemBlob  string
	NestedSharedBlob   string
}

var fixturePolicy = []byte(`version: 1
paths:
  - path: .protobot/requirements/
    class: shared
  - path: .protobot/interfaces/
    class: shared
  - path: docs/
    class: shared
  - path: NOTICE
    class: shared
  - path: tests/canonical/
    class: test
  - path: src/
    class: implementation
  - path: src/generated/README.md
    class: shared
  - path: integration/secrets/
    class: integration-only
  - path: .protobot/attestations/
    class: attestation-only
`)

// BuildSourceFixture creates a SHA-1 source repository with current and
// historical forbidden sentinels, an unclassified path, nested policy
// overrides, later-work-item history, and planted credentials that must
// not appear in Worker or Integration configuration.
func BuildSourceFixture(root string) (*SourceFixture, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	files := map[string]string{
		".protobot/projection.yaml":         string(fixturePolicy),
		".protobot/requirements/REQ-1.yaml": "id: REQ-1\n",
		".protobot/interfaces/IFACE-1.yaml": "id: IFACE-1\n",
		sharedDocPath:                       "# architecture\n",
		fileOnlySharedPath:                  "fixture notice\n",
		currentTestPath:                     currentTestSentinel + "\n",
		currentImplPath:                     currentImplSentinel + "\n",
		historicalImplPath:                  historicalSentinel + "\n",
		nestedSharedPath:                    nestedSharedSentinel + "\n",
		unclassifiedPath:                    unclassifiedSentinel + "\n",
		integrationOnlyPath:                 "integration-only-secret\n",
		attestationPath:                     "attestation-only\n",
		"tests/canonical/run.sh":            "#!/bin/sh\necho test\n",
	}
	for projectPath, content := range files {
		full := filepath.Join(root, filepath.FromSlash(projectPath))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return nil, err
		}
		mode := os.FileMode(0o644)
		if strings.HasSuffix(projectPath, ".sh") {
			mode = 0o755
		}
		if err := os.WriteFile(full, []byte(content), mode); err != nil {
			return nil, err
		}
	}

	repo, err := initRepo(root)
	if err != nil {
		return nil, err
	}
	defer repo.Close()

	historical, err := commitAll(repo, "historical forbidden sentinel")
	if err != nil {
		return nil, err
	}
	historicalBlob, err := blobAt(repo, historical, historicalImplPath)
	if err != nil {
		return nil, err
	}

	if err := os.Remove(filepath.Join(root, filepath.FromSlash(historicalImplPath))); err != nil {
		return nil, err
	}
	sourceCommit, err := commitAll(repo, "source commit for export")
	if err != nil {
		return nil, err
	}

	laterFull := filepath.Join(root, filepath.FromSlash(laterWorkItemPath))
	if err := os.MkdirAll(filepath.Dir(laterFull), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(laterFull, []byte(laterWorkItemSentinel+"\n"), 0o644); err != nil {
		return nil, err
	}
	laterCommit, err := commitAll(repo, "later work item history")
	if err != nil {
		return nil, err
	}
	laterBlob, err := blobAt(repo, laterCommit, laterWorkItemPath)
	if err != nil {
		return nil, err
	}

	currentImplBlob, err := blobAt(repo, sourceCommit, currentImplPath)
	if err != nil {
		return nil, err
	}
	currentTestBlob, err := blobAt(repo, sourceCommit, currentTestPath)
	if err != nil {
		return nil, err
	}
	unclassifiedBlob, err := blobAt(repo, sourceCommit, unclassifiedPath)
	if err != nil {
		return nil, err
	}
	nestedSharedBlob, err := blobAt(repo, sourceCommit, nestedSharedPath)
	if err != nil {
		return nil, err
	}

	res, err := repo.run("config", "remote.origin.url", "https://user:"+credentialSentinel+"@example.invalid/repo.git")
	if err := repo.check(res, err, "config"); err != nil {
		return nil, err
	}
	res, err = repo.run("config", "credential.helper", "store")
	if err := repo.check(res, err, "config"); err != nil {
		return nil, err
	}

	policy, err := ParsePolicy(fixturePolicy)
	if err != nil {
		return nil, err
	}
	return &SourceFixture{
		Root:               root,
		SourceCommit:       sourceCommit,
		LaterCommit:        laterCommit,
		Policy:             append([]byte(nil), fixturePolicy...),
		PolicyDigest:       policy.Digest,
		CurrentImplBlob:    currentImplBlob,
		HistoricalImplBlob: historicalBlob,
		CurrentTestBlob:    currentTestBlob,
		UnclassifiedBlob:   unclassifiedBlob,
		LaterWorkItemBlob:  laterBlob,
		NestedSharedBlob:   nestedSharedBlob,
	}, nil
}

func commitAll(repo *gitRepo, message string) (string, error) {
	res, err := repo.run("add", "-A")
	if err := repo.check(res, err, "add"); err != nil {
		return "", err
	}
	res, err = repo.runner.Run(gitx.Opts{Env: fixtureIdentity}, "commit", "--quiet", "--allow-empty", "-m", message)
	if err := repo.check(res, err, "commit"); err != nil {
		return "", err
	}
	return repo.head()
}

func blobAt(repo *gitRepo, commit, projectPath string) (string, error) {
	entries, err := repo.runner.TreeEntries(commit, []string{projectPath})
	if err != nil {
		return "", err
	}
	entry, ok := entries[projectPath]
	if !ok {
		return "", fail(CodeExportSource, "Fixture blob is missing.")
	}
	return entry.Object, nil
}
