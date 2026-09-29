package specvalidation

import (
	"reflect"
	"strings"
	"testing"

	"github.com/redhat-et/protobot/ears-manager/internal/records"
)

func TestCompareChangeSetReportsFindings(t *testing.T) {
	added := validRequirement("REQ-NEW-00001", records.EARSUbiquitous, "The system shall provide authentication.")
	duplicate := validRequirement("REQ-A-00001", records.EARSUbiquitous, "The system shall provide authentication.")
	conflictA := validRequirement("REQ-NEW-00002", records.EARSUbiquitous, "The system shall provide the first exclusive mode.")
	conflictB := validRequirement("REQ-OLD-00001", records.EARSUbiquitous, "The system shall provide the second exclusive mode.")
	conflictA.Relationships = []records.Relationship{{Type: relationshipConflictsWith, Target: conflictB.ID}}
	conflictB.Relationships = []records.Relationship{{Type: relationshipConflictsWith, Target: conflictA.ID}}
	replacement := validRequirement("REQ-NEW-00003", records.EARSUbiquitous, "The system shall provide the replacement behavior.")
	retired := validRequirement("REQ-OLD-00002", records.EARSUbiquitous, "The system shall provide the retired behavior.")
	retired.Status = records.StatusRetired
	replacement.Relationships = []records.Relationship{{Type: relationshipSupersedes, Target: retired.ID}}
	cycleA := validRequirement("REQ-NEW-00004", records.EARSUbiquitous, "The system shall provide the first cyclic behavior.")
	cycleB := validRequirement("REQ-OLD-00003", records.EARSUbiquitous, "The system shall provide the second cyclic behavior.")
	cycleA.Relationships = []records.Relationship{{Type: relationshipDependsOn, Target: cycleB.ID}}
	cycleB.Relationships = []records.Relationship{{Type: relationshipDependsOn, Target: cycleA.ID}}
	snapshot := Snapshot{
		Requirements: []Document[records.Requirement]{
			{Value: added}, {Value: duplicate}, {Value: conflictA}, {Value: conflictB},
			{Value: replacement}, {Value: retired}, {Value: cycleA}, {Value: cycleB},
		},
	}
	changeSet := records.ChangeSet{
		ID:                     "CS-00007",
		ImplementationRequired: true,
		Operations: []records.RequirementOperation{
			{Action: "add", RequirementID: added.ID},
			{Action: "add", RequirementID: conflictA.ID},
			{Action: "add", RequirementID: replacement.ID},
			{Action: "add", RequirementID: cycleA.ID},
			{Action: "retire", RequirementID: retired.ID},
		},
		InterfaceOperations: []records.InterfaceOperation{{Action: "add", InterfaceID: "cli-main"}},
		ArtifactOperations:  []records.ArtifactOperation{{Action: "revise", ArtifactID: "vision"}},
	}

	first := CompareChangeSet(changeSet, "ABCDEF"+strings.Repeat("0", 34), snapshot)
	second := CompareChangeSet(changeSet, "ABCDEF"+strings.Repeat("0", 34), snapshot)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("compare output was not deterministic:\n%#v\n---\n%#v", first, second)
	}
	if first.AgainstCommit != "abcdef"+strings.Repeat("0", 34) {
		t.Fatalf("against commit = %q", first.AgainstCommit)
	}
	if len(first.Changed) != 7 || first.Changed[0].ArtifactID != "vision" || first.Changed[1].InterfaceID != "cli-main" {
		t.Fatalf("changed = %#v", first.Changed)
	}
	if len(first.ExactDuplicates) != 1 || !reflect.DeepEqual(first.ExactDuplicates[0].RequirementIDs, []string{duplicate.ID, added.ID}) {
		t.Fatalf("exact duplicates = %#v", first.ExactDuplicates)
	}
	if len(first.DeclaredConflicts) != 1 || first.DeclaredConflicts[0].RequirementIDs[0] != conflictA.ID {
		t.Fatalf("declared conflicts = %#v", first.DeclaredConflicts)
	}
	if len(first.Supersession) != 1 || first.Supersession[0].RequirementID != replacement.ID {
		t.Fatalf("supersession = %#v", first.Supersession)
	}
	if len(first.DependencyCycles) != 1 || first.DependencyCycles[0].Relationship != relationshipDependsOn {
		t.Fatalf("dependency cycles = %#v", first.DependencyCycles)
	}
}

func TestImpactForChangeSetPrefersFalsePositives(t *testing.T) {
	changed := validRequirement("REQ-NEW-00001", records.EARSUbiquitous, "The system shall change the CLI help text.")
	changed.AppliesTo = records.Applicability{Interfaces: []string{"cli-main"}, Scopes: []string{"cli"}}
	byInterface := validRequirement("REQ-OLD-00001", records.EARSUbiquitous, "The system shall keep CLI help stable.")
	byInterface.AppliesTo = records.Applicability{Interfaces: []string{"cli-main"}}
	byScope := validRequirement("REQ-OLD-00002", records.EARSUbiquitous, "The system shall keep CLI output stable.")
	byScope.AppliesTo = records.Applicability{Scopes: []string{"cli"}}
	byRelationship := validRequirement("REQ-OLD-00003", records.EARSUbiquitous, "The system shall keep related help text.")
	byRelationship.AppliesTo = records.Applicability{Scopes: []string{"docs"}}
	byRelationship.Relationships = []records.Relationship{{Type: relationshipDependsOn, Target: changed.ID}}
	changed.Relationships = []records.Relationship{{Type: relationshipRelatedTo, Target: byRelationship.ID}}
	byRelationship.Relationships = append(byRelationship.Relationships, records.Relationship{Type: relationshipRelatedTo, Target: changed.ID})
	projectWide := validRequirement("REQ-OLD-00004", records.EARSUbiquitous, "The system shall keep project-wide help.")
	projectWide.AppliesTo = records.Applicability{Scopes: []string{"project"}}
	unrelated := validRequirement("REQ-OLD-00005", records.EARSUbiquitous, "The system shall keep administration events.")
	unrelated.AppliesTo = records.Applicability{Scopes: []string{"cli-administration"}}
	retired := validRequirement("REQ-OLD-00006", records.EARSUbiquitous, "The system shall keep retired help.")
	retired.AppliesTo = records.Applicability{Interfaces: []string{"cli-main"}}
	retired.Status = records.StatusRetired
	snapshot := Snapshot{
		Requirements: []Document[records.Requirement]{
			{Value: changed}, {Value: byInterface}, {Value: byScope}, {Value: byRelationship},
			{Value: projectWide}, {Value: unrelated}, {Value: retired},
		},
	}
	changeSet := records.ChangeSet{
		ID:                         "CS-00008",
		BaseCommit:                 strings.Repeat("a", 40),
		AffectedInterfaces:         []string{"cli-main"},
		AffectedScopes:             []string{"cli", "project"},
		Operations:                 []records.RequirementOperation{{Action: "add", RequirementID: changed.ID}},
		ImpactAssessmentBaseCommit: strings.Repeat("a", 40),
	}

	first := ImpactForChangeSet(changeSet, strings.Repeat("a", 40), snapshot)
	second := ImpactForChangeSet(changeSet, strings.Repeat("a", 40), snapshot)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("impact output was not deterministic:\n%#v\n---\n%#v", first, second)
	}
	ids := make([]string, 0, len(first.Candidates))
	matched := map[string][]string{}
	for _, candidate := range first.Candidates {
		ids = append(ids, candidate.RequirementID)
		matched[candidate.RequirementID] = candidate.MatchedBy
		if candidate.Origin != "mechanical" || candidate.RecommendedDisposition != "applicable" {
			t.Fatalf("candidate = %#v", candidate)
		}
		if candidate.RequirementID == changed.ID || candidate.RequirementID == unrelated.ID || candidate.RequirementID == retired.ID {
			t.Fatalf("unexpected candidate %s", candidate.RequirementID)
		}
	}
	if !reflect.DeepEqual(ids, []string{byInterface.ID, byScope.ID, byRelationship.ID, projectWide.ID}) {
		t.Fatalf("candidates = %#v", ids)
	}
	if !reflect.DeepEqual(matched[byInterface.ID], []string{"interface:cli-main"}) {
		t.Fatalf("interface match = %#v", matched[byInterface.ID])
	}
	if !reflect.DeepEqual(matched[byScope.ID], []string{"scope:cli"}) {
		t.Fatalf("scope match = %#v", matched[byScope.ID])
	}
	if !reflect.DeepEqual(matched[byRelationship.ID], []string{"relationship:depends-on:" + changed.ID, "relationship:related-to:" + changed.ID}) {
		t.Fatalf("relationship match = %#v", matched[byRelationship.ID])
	}
	if !reflect.DeepEqual(matched[projectWide.ID], []string{"scope:project"}) {
		t.Fatalf("project match = %#v", matched[projectWide.ID])
	}
	if first.AssessmentStatus != AssessmentIncomplete {
		t.Fatalf("assessment status = %q", first.AssessmentStatus)
	}
}

func TestChangeSetAssessmentStatusCompleteIncompleteAndStale(t *testing.T) {
	candidate := validRequirement("REQ-OLD-00001", records.EARSUbiquitous, "The system shall keep CLI help stable.")
	requirements := map[string]records.Requirement{candidate.ID: candidate}
	candidates := map[string]bool{candidate.ID: true}
	changeSet := records.ChangeSet{
		BaseCommit:                 strings.Repeat("a", 40),
		Operations:                 []records.RequirementOperation{{Action: "add", RequirementID: "REQ-NEW-00001"}},
		ImpactAssessmentBaseCommit: strings.Repeat("a", 40),
	}
	if got := ChangeSetAssessmentStatus(changeSet, candidates, requirements); got != AssessmentIncomplete {
		t.Fatalf("missing assessment status = %q", got)
	}
	changeSet.ImpactAssessment = []records.ImpactAssessment{{
		RequirementID: candidate.ID, Disposition: "applicable", Rationale: "Help remains binding.", Origin: "mechanical",
	}}
	if got := ChangeSetAssessmentStatus(changeSet, candidates, requirements); got != AssessmentComplete {
		t.Fatalf("matching assessment status = %q", got)
	}
	changeSet.ImpactAssessment = []records.ImpactAssessment{{
		RequirementID: candidate.ID, Disposition: "applicable", Rationale: "Help remains binding.", Origin: "mechanical",
	}, {
		RequirementID: "REQ-OLD-00002", Disposition: "not-applicable", Rationale: "Semantic extra.", Origin: "semantic",
	}}
	requirements["REQ-OLD-00002"] = validRequirement("REQ-OLD-00002", records.EARSUbiquitous, "The system shall keep administration events.")
	if got := ChangeSetAssessmentStatus(changeSet, candidates, requirements); got != AssessmentComplete {
		t.Fatalf("semantic extra status = %q", got)
	}
	if got := ChangeSetAssessmentStatus(changeSet, map[string]bool{}, requirements); got != AssessmentStale {
		t.Fatalf("stale mechanical status = %q", got)
	}
}

func TestChangeSetAssessmentStatusFollowsTheBaseCommit(t *testing.T) {
	candidate := validRequirement("REQ-OLD-00001", records.EARSUbiquitous, "The system shall keep CLI help stable.")
	requirements := map[string]records.Requirement{candidate.ID: candidate}
	candidates := map[string]bool{candidate.ID: true}
	reviewed := func(base, assessmentBase string) records.ChangeSet {
		return records.ChangeSet{
			BaseCommit: base,
			Operations: []records.RequirementOperation{{Action: "add", RequirementID: "REQ-NEW-00001"}},
			ImpactAssessment: []records.ImpactAssessment{{
				RequirementID: candidate.ID, Disposition: "applicable", Rationale: "Help remains binding.", Origin: "mechanical",
			}},
			ImpactAssessmentBaseCommit: assessmentBase,
		}
	}
	// A change set with no candidates and no entries, like an
	// artifact-only change set.
	empty := func(base, assessmentBase string) records.ChangeSet {
		return records.ChangeSet{BaseCommit: base, Operations: []records.RequirementOperation{}, ImpactAssessmentBaseCommit: assessmentBase}
	}
	older, newer := strings.Repeat("a", 40), strings.Repeat("b", 40)
	for _, test := range []struct {
		name       string
		changeSet  records.ChangeSet
		candidates map[string]bool
		want       string
	}{
		{"same base in another case", reviewed(older, strings.ToUpper(older)), candidates, AssessmentComplete},
		{"moved base", reviewed(newer, older), candidates, AssessmentStale},
		{"no recorded base", reviewed(older, ""), candidates, AssessmentStale},
		{"empty assessment on the same base", empty(older, older), map[string]bool{}, AssessmentComplete},
		{"empty assessment on a moved base", empty(newer, older), map[string]bool{}, AssessmentStale},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := ChangeSetAssessmentStatus(test.changeSet, test.candidates, requirements); got != test.want {
				t.Fatalf("status = %q, want %q", got, test.want)
			}
		})
	}
}

func TestCompareChangeSetOverlappingCycles(t *testing.T) {
	// Diamond-plus-back-edge overlapping cycle structure:
	// A -> B -> C -> A
	// A -> D -> C -> A
	// If only D is changed in the proposal, cycle 2 must still be found even though
	// vertex C is shared with cycle 1.
	reqA := validRequirement("REQ-CYC-A", records.EARSUbiquitous, "Requirement A")
	reqB := validRequirement("REQ-CYC-B", records.EARSUbiquitous, "Requirement B")
	reqC := validRequirement("REQ-CYC-C", records.EARSUbiquitous, "Requirement C")
	reqD := validRequirement("REQ-CYC-D", records.EARSUbiquitous, "Requirement D")

	reqA.Relationships = []records.Relationship{
		{Type: relationshipDependsOn, Target: reqB.ID},
		{Type: relationshipDependsOn, Target: reqD.ID},
	}
	reqB.Relationships = []records.Relationship{
		{Type: relationshipDependsOn, Target: reqC.ID},
	}
	reqD.Relationships = []records.Relationship{
		{Type: relationshipDependsOn, Target: reqC.ID},
	}
	reqC.Relationships = []records.Relationship{
		{Type: relationshipDependsOn, Target: reqA.ID},
	}

	snapshot := Snapshot{
		Requirements: []Document[records.Requirement]{
			{Value: reqA}, {Value: reqB}, {Value: reqC}, {Value: reqD},
		},
	}

	// Case 1: only D is changed
	csOnlyD := records.ChangeSet{
		ID:                     "CS-00010",
		ImplementationRequired: true,
		Operations: []records.RequirementOperation{
			{Action: "revise", RequirementID: reqD.ID},
		},
	}
	reportD := CompareChangeSet(csOnlyD, strings.Repeat("0", 40), snapshot)
	if len(reportD.DependencyCycles) != 1 {
		t.Fatalf("expected 1 cycle involving changed requirement D, got %d: %#v", len(reportD.DependencyCycles), reportD.DependencyCycles)
	}
	expectedCycleD := []string{reqA.ID, reqD.ID, reqC.ID, reqA.ID}
	if !reflect.DeepEqual(reportD.DependencyCycles[0].Cycle, expectedCycleD) {
		t.Fatalf("cycle for changed D = %#v, want %#v", reportD.DependencyCycles[0].Cycle, expectedCycleD)
	}

	// Case 2: both A and D are changed (both cycles should be included)
	csBoth := records.ChangeSet{
		ID:                     "CS-00011",
		ImplementationRequired: true,
		Operations: []records.RequirementOperation{
			{Action: "revise", RequirementID: reqA.ID},
			{Action: "revise", RequirementID: reqD.ID},
		},
	}
	reportBoth := CompareChangeSet(csBoth, strings.Repeat("0", 40), snapshot)
	if len(reportBoth.DependencyCycles) != 2 {
		t.Fatalf("expected 2 overlapping cycles, got %d: %#v", len(reportBoth.DependencyCycles), reportBoth.DependencyCycles)
	}
	expectedCycle1 := []string{reqA.ID, reqB.ID, reqC.ID, reqA.ID}
	expectedCycle2 := []string{reqA.ID, reqD.ID, reqC.ID, reqA.ID}
	if !reflect.DeepEqual(reportBoth.DependencyCycles[0].Cycle, expectedCycle1) ||
		!reflect.DeepEqual(reportBoth.DependencyCycles[1].Cycle, expectedCycle2) {
		t.Fatalf("cycles = %#v, want %#v and %#v", reportBoth.DependencyCycles, expectedCycle1, expectedCycle2)
	}
}
