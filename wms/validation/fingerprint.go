package validation

import (
	"crypto/sha3"
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"
	"time"
)

// RequestFingerprint returns the canonical project-scoped idempotency
// fingerprint. Authorization expiry is intentionally excluded so an exact
// retry may use a refreshed Gate context with the same principal and scope.
func RequestFingerprint(request Request) string {
	request.IdempotencyKey = ""
	request.Authorization.ExpiresAt = time.Time{}
	request.Authorization.AllowedActions = slices.Clone(request.Authorization.AllowedActions)
	slices.Sort(request.Authorization.AllowedActions)
	request.Authorization.AllowedRefs = slices.Clone(request.Authorization.AllowedRefs)
	slices.Sort(request.Authorization.AllowedRefs)
	request.References = slices.Clone(request.References)
	slices.Sort(request.References)
	return fingerprint(struct {
		RuleVersion string
		Request     Request
	}{RuleVersion: RuleVersion, Request: request})
}

// SourceFingerprint identifies the complete source contract bound to a
// materialization key. Mutable lifecycle state, lease fields, and observed
// dependency states are excluded; only dependency identities participate.
// The item is fingerprinted by value: the caller's dependency slice and
// merge envelope are not modified.
func SourceFingerprint(item WorkItem) string {
	item.State = ""
	item.ContractVersion = 0
	item.MaterializationKey = ""
	item.Priority = ""
	item.Owner = ""
	item.Lease = nil
	item.BlockReason = ""
	item.ActiveResolutionSubmissionID = ""
	item.InspectionRunSealed = false
	item.FindingsTerminal = false
	item.FinalTestsPassed = false
	// The source contract may name only the merge target — the tested
	// candidate is recorded at begin-merge and does not participate in the
	// source binding — and an envelope that names no target binds like an
	// absent one.
	if item.ExpectedMerge != nil {
		if item.ExpectedMerge.Target == "" {
			item.ExpectedMerge = nil
		} else {
			item.ExpectedMerge = &MergeEnvelope{Target: item.ExpectedMerge.Target}
		}
	}
	item.Reconciliation = ReconciliationEvidence{}
	item.Dependencies = slices.Clone(item.Dependencies)
	for index := range item.Dependencies {
		item.Dependencies[index].State = ""
	}
	return fingerprint(item)
}

// RefinementDigest returns the canonical digest of refined request content.
// A Gate approval bound to this digest authorizes exactly this content. Nil
// and empty slices digest identically.
func RefinementDigest(content RefinementContent) string {
	if len(content.AffectedInterfaces) == 0 {
		content.AffectedInterfaces = nil
	} else {
		content.AffectedInterfaces = sortedCopy(content.AffectedInterfaces)
	}
	if len(content.AffectedScopes) == 0 {
		content.AffectedScopes = nil
	} else {
		content.AffectedScopes = sortedCopy(content.AffectedScopes)
	}
	if len(content.Relationships) == 0 {
		content.Relationships = nil
	} else {
		content.Relationships = append([]RefinementRelationship(nil), content.Relationships...)
		slices.SortFunc(content.Relationships, func(a, b RefinementRelationship) int {
			if a.Type != b.Type {
				return strings.Compare(a.Type, b.Type)
			}
			return strings.Compare(a.Target, b.Target)
		})
	}
	return fingerprint(struct {
		RuleVersion string
		Content     RefinementContent
	}{RuleVersion: RuleVersion, Content: content})
}

func sortedCopy(values []string) []string {
	result := append([]string(nil), values...)
	slices.Sort(result)
	return result
}

// CanonicalMaterializationSource applies the payload-level change-type
// fallback used by materialization before a source contract is fingerprinted.
func CanonicalMaterializationSource(item WorkItem, fallbackChangeType string) WorkItem {
	if item.ChangeType == "" {
		item.ChangeType = fallbackChangeType
	}
	return item
}

func fingerprint(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		// All values used here are closed structs without unsupported JSON
		// members, so this branch indicates a programmer error rather than
		// untrusted request input.
		panic(err)
	}
	stream := sha3.New256()
	if _, err := stream.Write(encoded); err != nil {
		panic(err)
	}
	return "sha3-256:" + base64.RawURLEncoding.EncodeToString(stream.Sum(nil))
}
