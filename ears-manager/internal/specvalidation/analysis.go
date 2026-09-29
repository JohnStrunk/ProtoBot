package specvalidation

import (
	"slices"
	"sort"
	"strings"

	"github.com/redhat-et/protobot/ears-manager/internal/records"
)

const (
	// AssessmentComplete means every mechanical candidate has a final
	// recorded disposition and every recorded mechanical entry is still a
	// current candidate.
	AssessmentComplete = "complete"
	// AssessmentIncomplete means current mechanical candidates lack a final
	// recorded disposition.
	AssessmentIncomplete = "incomplete"
	// AssessmentStale means the recorded assessment was computed from a
	// different mechanical candidate set, or recorded against a different
	// base commit.
	AssessmentStale = "stale"
)

// ChangedItem is one stable-ID operation in a change-set comparison.
type ChangedItem struct {
	Action        string `json:"action"`
	ArtifactID    string `json:"artifact_id,omitempty"`
	InterfaceID   string `json:"interface_id,omitempty"`
	RequirementID string `json:"requirement_id,omitempty"`
}

// DuplicateGroup is a set of active requirements that share identical text.
type DuplicateGroup struct {
	RequirementIDs []string `json:"requirement_ids"`
	Text           string   `json:"text"`
}

// ConflictPair is a declared conflicts-with relationship.
type ConflictPair struct {
	RequirementIDs []string `json:"requirement_ids"`
}

// Supersession is a declared supersedes relationship.
type Supersession struct {
	RequirementID string `json:"requirement_id"`
	Supersedes    string `json:"supersedes"`
}

// DependencyCycle is a directed relationship cycle that involves a changed
// requirement.
type DependencyCycle struct {
	Relationship string   `json:"relationship"`
	Cycle        []string `json:"cycle"`
}

// CompareReport is the deterministic change-set comparison result.
type CompareReport struct {
	ChangeSetID            string            `json:"change_set_id"`
	AgainstCommit          string            `json:"against_commit"`
	Changed                []ChangedItem     `json:"changed"`
	ExactDuplicates        []DuplicateGroup  `json:"exact_duplicates"`
	DeclaredConflicts      []ConflictPair    `json:"declared_conflicts"`
	Supersession           []Supersession    `json:"supersession"`
	DependencyCycles       []DependencyCycle `json:"dependency_cycles"`
	ImplementationRequired bool              `json:"implementation_required"`
}

// ImpactCandidate is one mechanical impact candidate.
type ImpactCandidate struct {
	RequirementID          string   `json:"requirement_id"`
	Origin                 string   `json:"origin"`
	MatchedBy              []string `json:"matched_by"`
	RecommendedDisposition string   `json:"recommended_disposition"`
	RecordedDisposition    *string  `json:"recorded_disposition"`
	Rationale              *string  `json:"rationale"`
}

// ImpactReport is the deterministic impact analysis result.
type ImpactReport struct {
	ChangeSetID      string            `json:"change_set_id"`
	AgainstCommit    string            `json:"against_commit"`
	Candidates       []ImpactCandidate `json:"candidates"`
	AssessmentStatus string            `json:"assessment_status"`
}

type impactMatch struct {
	requirementID string
	matchedBy     []string
}

// CompareChangeSet reports stable-ID operations and relationship findings
// for a change set against the supplied comparison revision.
func CompareChangeSet(changeSet records.ChangeSet, against string, snapshot Snapshot) CompareReport {
	changeSet = records.CanonicalChangeSet(changeSet)
	changedIDs := changedRequirements(changeSet.Operations)
	changed := make([]ChangedItem, 0, len(changeSet.ArtifactOperations)+len(changeSet.InterfaceOperations)+len(changeSet.Operations))
	for _, operation := range changeSet.ArtifactOperations {
		changed = append(changed, ChangedItem{Action: operation.Action, ArtifactID: operation.ArtifactID})
	}
	for _, operation := range changeSet.InterfaceOperations {
		changed = append(changed, ChangedItem{Action: operation.Action, InterfaceID: operation.InterfaceID})
	}
	for _, operation := range changeSet.Operations {
		changed = append(changed, ChangedItem{Action: operation.Action, RequirementID: operation.RequirementID})
	}
	if changed == nil {
		changed = []ChangedItem{}
	}
	return CompareReport{
		ChangeSetID:            changeSet.ID,
		AgainstCommit:          strings.ToLower(against),
		Changed:                changed,
		ExactDuplicates:        exactDuplicateGroups(snapshot.Requirements, changedIDs),
		DeclaredConflicts:      declaredConflicts(snapshot.Requirements, changedIDs),
		Supersession:           supersessionEntries(snapshot.Requirements, changedIDs),
		DependencyCycles:       dependencyCycles(snapshot.Requirements, changedIDs),
		ImplementationRequired: changeSet.ImplementationRequired,
	}
}

// ImpactForChangeSet returns mechanical impact candidates and the current
// assessment status for a change set.
func ImpactForChangeSet(changeSet records.ChangeSet, against string, snapshot Snapshot) ImpactReport {
	changeSet = records.CanonicalChangeSet(changeSet)
	requirements := requirementIndex(snapshot.Requirements)
	changed := changedRequirements(changeSet.Operations)
	matches := impactMatches(changeSet, snapshot.Requirements, requirements, changed)
	candidates := make(map[string]bool, len(matches))
	recorded := recordedAssessments(changeSet.ImpactAssessment)
	items := make([]ImpactCandidate, 0, len(matches))
	for _, match := range matches {
		candidates[match.requirementID] = true
		item := ImpactCandidate{
			RequirementID:          match.requirementID,
			Origin:                 "mechanical",
			MatchedBy:              append([]string(nil), match.matchedBy...),
			RecommendedDisposition: "applicable",
		}
		if assessment, exists := recorded[match.requirementID]; exists {
			disposition := assessment.Disposition
			rationale := assessment.Rationale
			item.RecordedDisposition = &disposition
			item.Rationale = &rationale
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].RequirementID < items[j].RequirementID })
	if items == nil {
		items = []ImpactCandidate{}
	}
	return ImpactReport{
		ChangeSetID:      changeSet.ID,
		AgainstCommit:    strings.ToLower(against),
		Candidates:       items,
		AssessmentStatus: ChangeSetAssessmentStatus(changeSet, candidates, requirements),
	}
}

// ChangeSetAssessmentStatus classifies a recorded impact assessment against
// the current mechanical candidate set and the current base commit.
func ChangeSetAssessmentStatus(changeSet records.ChangeSet, candidates map[string]bool, requirements map[string]records.Requirement) string {
	changed := changedRequirements(changeSet.Operations)
	stale := !assessmentBaseCurrent(changeSet)
	incomplete := false
	seen := make(map[string]int, len(changeSet.ImpactAssessment))
	final := make(map[string]bool, len(changeSet.ImpactAssessment))
	for _, assessment := range changeSet.ImpactAssessment {
		seen[assessment.RequirementID]++
		if seen[assessment.RequirementID] > 1 {
			incomplete = true
		}
		if !impactDispositions[assessment.Disposition] || strings.TrimSpace(assessment.Rationale) == "" {
			incomplete = true
		} else {
			final[assessment.RequirementID] = true
		}
		switch assessment.Origin {
		case "mechanical":
			if !candidates[assessment.RequirementID] {
				stale = true
			}
		case "semantic":
			if candidates[assessment.RequirementID] {
				stale = true
			}
			requirement, exists := requirements[assessment.RequirementID]
			if !exists || changed[assessment.RequirementID] || records.CanonicalRequirement(requirement).Status != records.StatusActive {
				stale = true
			}
		default:
			incomplete = true
		}
	}
	for candidate := range candidates {
		if !final[candidate] {
			incomplete = true
		}
	}
	if stale {
		return AssessmentStale
	}
	if incomplete {
		return AssessmentIncomplete
	}
	return AssessmentComplete
}

// assessmentBaseCurrent reports whether the impact assessment was recorded
// against the manifest's current base commit. A manifest that does not
// record the base of its assessment is not current.
func assessmentBaseCurrent(changeSet records.ChangeSet) bool {
	return changeSet.ImpactAssessmentBaseCommit != "" && strings.EqualFold(changeSet.ImpactAssessmentBaseCommit, changeSet.BaseCommit)
}

func impactMatches(value records.ChangeSet, documents []Document[records.Requirement], requirements map[string]records.Requirement, changed map[string]bool) []impactMatch {
	projectBoundary := hasProjectScope(value.AffectedScopes)
	for changedID := range changed {
		if requirement, exists := requirements[changedID]; exists && hasProjectScope(records.CanonicalRequirement(requirement).AppliesTo.Scopes) {
			projectBoundary = true
		}
	}
	matches := make([]impactMatch, 0)
	for _, document := range documents {
		requirement := records.CanonicalRequirement(document.Value)
		if requirement.ID == "" || records.ValidateRequirementID(requirement.ID) != nil || changed[requirement.ID] || requirement.Status != records.StatusActive {
			continue
		}
		reasons := matchReasons(value, requirement, documents, changed, projectBoundary)
		if len(reasons) == 0 {
			continue
		}
		matches = append(matches, impactMatch{requirementID: requirement.ID, matchedBy: reasons})
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].requirementID < matches[j].requirementID })
	return matches
}

func matchReasons(changeSet records.ChangeSet, requirement records.Requirement, documents []Document[records.Requirement], changed map[string]bool, projectBoundary bool) []string {
	reasons := make([]string, 0)
	seen := make(map[string]bool)
	add := func(reason string) {
		if reason == "" || seen[reason] {
			return
		}
		seen[reason] = true
		reasons = append(reasons, reason)
	}
	for _, interfaceID := range requirement.AppliesTo.Interfaces {
		if slices.Contains(changeSet.AffectedInterfaces, interfaceID) {
			add("interface:" + interfaceID)
		}
	}
	for _, scope := range requirement.AppliesTo.Scopes {
		if slices.Contains(changeSet.AffectedScopes, scope) {
			add("scope:" + scope)
		}
	}
	if projectBoundary && hasProjectScope(requirement.AppliesTo.Scopes) {
		add("scope:project")
	}
	for _, relationship := range requirement.Relationships {
		if changed[relationship.Target] {
			add("relationship:" + relationship.Type + ":" + relationship.Target)
		}
	}
	for _, document := range documents {
		if !changed[document.Value.ID] {
			continue
		}
		for _, relationship := range document.Value.Relationships {
			if relationship.Target == requirement.ID {
				add("relationship:" + relationship.Type + ":" + document.Value.ID)
			}
		}
	}
	sort.Strings(reasons)
	return reasons
}

func exactDuplicateGroups(documents []Document[records.Requirement], changed map[string]bool) []DuplicateGroup {
	byText := make(map[string][]string)
	texts := make(map[string]string)
	for _, document := range documents {
		requirement := records.CanonicalRequirement(document.Value)
		if requirement.ID == "" || requirement.Status != records.StatusActive {
			continue
		}
		byText[requirement.Text] = append(byText[requirement.Text], requirement.ID)
		texts[requirement.Text] = requirement.Text
	}
	groups := make([]DuplicateGroup, 0)
	for text, ids := range byText {
		if len(ids) < 2 {
			continue
		}
		sort.Strings(ids)
		involvesChanged := false
		for _, id := range ids {
			if changed[id] {
				involvesChanged = true
				break
			}
		}
		if !involvesChanged {
			continue
		}
		groups = append(groups, DuplicateGroup{RequirementIDs: ids, Text: texts[text]})
	}
	sort.Slice(groups, func(i, j int) bool {
		return groups[i].RequirementIDs[0] < groups[j].RequirementIDs[0]
	})
	return emptyDuplicateGroups(groups)
}

func declaredConflicts(documents []Document[records.Requirement], changed map[string]bool) []ConflictPair {
	seen := make(map[string]bool)
	pairs := make([]ConflictPair, 0)
	for _, document := range documents {
		requirement := records.CanonicalRequirement(document.Value)
		for _, relationship := range requirement.Relationships {
			if relationship.Type != relationshipConflictsWith {
				continue
			}
			if !changed[requirement.ID] && !changed[relationship.Target] {
				continue
			}
			ids := []string{requirement.ID, relationship.Target}
			sort.Strings(ids)
			key := ids[0] + "\x00" + ids[1]
			if seen[key] {
				continue
			}
			seen[key] = true
			pairs = append(pairs, ConflictPair{RequirementIDs: ids})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].RequirementIDs[0] == pairs[j].RequirementIDs[0] {
			return pairs[i].RequirementIDs[1] < pairs[j].RequirementIDs[1]
		}
		return pairs[i].RequirementIDs[0] < pairs[j].RequirementIDs[0]
	})
	return emptyConflictPairs(pairs)
}

func supersessionEntries(documents []Document[records.Requirement], changed map[string]bool) []Supersession {
	entries := make([]Supersession, 0)
	for _, document := range documents {
		requirement := records.CanonicalRequirement(document.Value)
		for _, relationship := range requirement.Relationships {
			if relationship.Type != relationshipSupersedes {
				continue
			}
			if !changed[requirement.ID] && !changed[relationship.Target] {
				continue
			}
			entries = append(entries, Supersession{RequirementID: requirement.ID, Supersedes: relationship.Target})
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].RequirementID == entries[j].RequirementID {
			return entries[i].Supersedes < entries[j].Supersedes
		}
		return entries[i].RequirementID < entries[j].RequirementID
	})
	return emptySupersessions(entries)
}

func dependencyCycles(documents []Document[records.Requirement], changed map[string]bool) []DependencyCycle {
	cycles := make([]DependencyCycle, 0)
	for _, relationType := range []string{relationshipDependsOn, relationshipSupersedes} {
		graph := make(map[string][]string)
		for _, document := range documents {
			requirement := records.CanonicalRequirement(document.Value)
			for _, relationship := range requirement.Relationships {
				if relationship.Type == relationType {
					graph[requirement.ID] = append(graph[requirement.ID], relationship.Target)
				}
			}
		}
		for _, cycle := range findDirectedCycles(graph) {
			involvesChanged := false
			for _, id := range cycle {
				if changed[id] {
					involvesChanged = true
					break
				}
			}
			if !involvesChanged {
				continue
			}
			cycles = append(cycles, DependencyCycle{Relationship: relationType, Cycle: cycle})
		}
	}
	sort.Slice(cycles, func(i, j int) bool {
		if cycles[i].Relationship == cycles[j].Relationship {
			return strings.Join(cycles[i].Cycle, "->") < strings.Join(cycles[j].Cycle, "->")
		}
		return cycles[i].Relationship < cycles[j].Relationship
	})
	return emptyDependencyCycles(cycles)
}

func findDirectedCycles(graph map[string][]string) [][]string {
	for source, targets := range graph {
		sort.Strings(targets)
		graph[source] = uniqueSortedStrings(targets)
	}
	for source := range graph {
		for _, target := range graph[source] {
			if _, exists := graph[target]; !exists {
				graph[target] = nil
			}
		}
	}
	vertices := make([]string, 0, len(graph))
	for vertex := range graph {
		vertices = append(vertices, vertex)
	}
	vertices = uniqueSortedStrings(vertices)

	indexOf := make(map[string]int, len(vertices))
	for i, v := range vertices {
		indexOf[v] = i
	}

	reported := make(map[string]bool)
	cycles := make([][]string, 0)

	for startIndex, start := range vertices {
		blocked := make(map[string]bool)
		blockMap := make(map[string][]string)
		stack := make([]string, 0)

		var unblock func(string)
		unblock = func(u string) {
			blocked[u] = false
			neighbors := blockMap[u]
			delete(blockMap, u)
			for _, w := range neighbors {
				if blocked[w] {
					unblock(w)
				}
			}
		}

		var findCycles func(string) bool
		findCycles = func(u string) bool {
			foundCycle := false
			stack = append(stack, u)
			blocked[u] = true

			for _, v := range graph[u] {
				if indexOf[v] < startIndex {
					continue
				}
				if v == start {
					cycle := append(append([]string{}, stack...), start)
					cycle = rotateCycle(cycle)
					key := strings.Join(cycle, "->")
					if !reported[key] {
						reported[key] = true
						cycles = append(cycles, cycle)
					}
					foundCycle = true
				} else if !blocked[v] {
					if findCycles(v) {
						foundCycle = true
					}
				}
			}

			if foundCycle {
				unblock(u)
			} else {
				for _, v := range graph[u] {
					if indexOf[v] < startIndex {
						continue
					}
					if !slices.Contains(blockMap[v], u) {
						blockMap[v] = append(blockMap[v], u)
					}
				}
			}

			stack = stack[:len(stack)-1]
			return foundCycle
		}

		findCycles(start)
	}

	sort.Slice(cycles, func(i, j int) bool {
		return strings.Join(cycles[i], "->") < strings.Join(cycles[j], "->")
	})
	return cycles
}

func rotateCycle(cycle []string) []string {
	if len(cycle) < 2 {
		return append([]string(nil), cycle...)
	}
	body := cycle[:len(cycle)-1]
	minIndex := 0
	for index, id := range body {
		if id < body[minIndex] {
			minIndex = index
		}
	}
	rotated := append(append([]string{}, body[minIndex:]...), body[:minIndex]...)
	return append(rotated, rotated[0])
}

func requirementIndex(documents []Document[records.Requirement]) map[string]records.Requirement {
	result := make(map[string]records.Requirement, len(documents))
	for _, document := range documents {
		if document.Value.ID == "" {
			continue
		}
		result[document.Value.ID] = records.CanonicalRequirement(document.Value)
	}
	return result
}

func recordedAssessments(values []records.ImpactAssessment) map[string]records.ImpactAssessment {
	result := make(map[string]records.ImpactAssessment, len(values))
	for _, assessment := range values {
		if _, exists := result[assessment.RequirementID]; exists {
			continue
		}
		result[assessment.RequirementID] = assessment
	}
	return result
}

func uniqueSortedStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	sorted := append([]string(nil), values...)
	sort.Strings(sorted)
	result := make([]string, 0, len(sorted))
	for _, value := range sorted {
		if len(result) > 0 && result[len(result)-1] == value {
			continue
		}
		result = append(result, value)
	}
	return result
}

func emptyDuplicateGroups(values []DuplicateGroup) []DuplicateGroup {
	if values == nil {
		return []DuplicateGroup{}
	}
	return values
}

func emptyConflictPairs(values []ConflictPair) []ConflictPair {
	if values == nil {
		return []ConflictPair{}
	}
	return values
}

func emptySupersessions(values []Supersession) []Supersession {
	if values == nil {
		return []Supersession{}
	}
	return values
}

func emptyDependencyCycles(values []DependencyCycle) []DependencyCycle {
	if values == nil {
		return []DependencyCycle{}
	}
	return values
}
