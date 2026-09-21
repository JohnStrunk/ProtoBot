package specvalidation

import "github.com/redhat-et/protobot/ears-manager/internal/records"

// Document carries a typed record and the source fields that were present in
// its YAML document. Fields let validation distinguish an omitted required
// value from an explicitly supplied zero value.
type Document[T any] struct {
	Path   string
	Value  T
	Fields map[string]bool
}

// ValidationContext identifies the change sets that are still proposed in the
// snapshot. Approved manifests remain immutable historical records and are not
// re-evaluated against later specification state.
type ValidationContext struct {
	ProposedChangeSets map[string]bool
}

func (c ValidationContext) isProposed(changeSetID string) bool {
	if c.ProposedChangeSets == nil {
		return true
	}
	return c.ProposedChangeSets[changeSetID]
}

// Snapshot is the complete specification state needed for project-level
// validation. It contains no writable handles and validation never mutates it.
type Snapshot struct {
	Root         string
	Config       records.ProjectConfig
	ConfigPath   string
	ConfigFields map[string]bool
	Context      ValidationContext
	Requirements []Document[records.Requirement]
	Interfaces   []Document[records.InterfaceRecord]
	ChangeSets   []Document[records.ChangeSet]
}
