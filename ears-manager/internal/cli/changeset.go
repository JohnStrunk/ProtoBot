package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"
	"syscall"

	"github.com/redhat-et/protobot/ears-manager/internal/records"
	"github.com/redhat-et/protobot/ears-manager/internal/specvalidation"
)

func runChangeSetCreate(args []string) (any, Mutation, *commandFailure) {
	parsed, failure := parseOptions(args, valueOptions("intent", "affected-interface", "affected-scope", "implementation-required", "implementation-rationale", "created"))
	if failure != nil {
		return nil, Mutation{}, failure
	}
	intent, failure := requireOption(parsed, "intent")
	if failure != nil {
		return nil, Mutation{}, failure
	}
	implementationRequired, failure := parseBoolOption(parsed, "implementation-required")
	if failure != nil {
		return nil, Mutation{}, failure
	}
	created, failure := requireOption(parsed, "created")
	if failure != nil {
		return nil, Mutation{}, failure
	}
	if !implementationRequired && strings.TrimSpace(parsed.one("implementation-rationale")) == "" {
		return nil, Mutation{}, usageFailure("option --implementation-rationale is required when implementation is false")
	}
	state, failure := loadState()
	if failure != nil {
		return nil, Mutation{}, failure
	}
	baseCommit, failure := currentCommit(state.root)
	if failure != nil {
		return nil, Mutation{}, failure
	}
	id, failure := nextChangeSetID(state.snapshot)
	if failure != nil {
		return nil, Mutation{}, failure
	}
	changeSet := records.ChangeSet{
		ID:                      id,
		BaseCommit:              baseCommit,
		Intent:                  intent,
		Operations:              []records.RequirementOperation{},
		AffectedInterfaces:      append([]string{}, parsed.list("affected-interface")...),
		AffectedScopes:          append([]string(nil), parsed.list("affected-scope")...),
		ImplementationRequired:  implementationRequired,
		ImplementationRationale: parsed.one("implementation-rationale"),
		Created:                 created,
	}
	staged := cloneSnapshot(state.snapshot)
	changeSetPath, err := upsertChangeSet(&staged, changeSet)
	if err != nil {
		return nil, Mutation{}, internalFailure("the change-set path could not be determined")
	}
	if failure := validateCandidateForChangeSet(staged, id, true); failure != nil {
		return nil, Mutation{}, failure
	}
	writes := []fileWrite{}
	if err := addWrite(&writes, changeSetPath, changeSet); err != nil {
		return nil, Mutation{}, internalFailure("the change set could not be serialized")
	}
	if err := addConfigWrite(state.root, &staged, &writes, state.observed); err != nil {
		return nil, Mutation{}, configWriteFailure(err, "the project configuration could not be serialized")
	}
	mutation, failure := applyStateTransaction(state, writes, func() *commandFailure { return persistedValidation(state.root, true, id) })
	if failure != nil {
		return nil, Mutation{}, failure
	}
	return changeSetCreateData{ChangeSet: changeSetCreateRecord{
		ID: id, BaseCommit: baseCommit, ManifestPath: changeSetPath,
	}}, mutation, nil
}

type changeSetCreateData struct {
	ChangeSet changeSetCreateRecord `json:"change_set"`
}

type changeSetCreateRecord struct {
	ID           string `json:"id"`
	BaseCommit   string `json:"base_commit"`
	ManifestPath string `json:"manifest_path"`
}

func runChangeSetList(args []string) (any, Mutation, *commandFailure) {
	parsed, failure := parseOptions(args, valueOptions("status", "interface", "scope"))
	if failure != nil {
		return nil, Mutation{}, failure
	}
	if parsed.has("status") && !validChangeSetStatus(parsed.one("status")) {
		return nil, Mutation{}, validationFailure("change_set.invalid_status", fmt.Sprintf("Unsupported change-set status %q.", parsed.one("status")), nil)
	}
	if parsed.has("interface") {
		if err := records.ValidateInterfaceID(parsed.one("interface")); err != nil {
			return nil, Mutation{}, invalidIDFailure("interface.invalid_id", "Interface", parsed.one("interface"))
		}
	}
	state, failure := loadState()
	if failure != nil {
		return nil, Mutation{}, failure
	}
	items := make([]changeSetListItem, 0)
	var refs []string
	for _, document := range state.snapshot.ChangeSets {
		value := records.CanonicalChangeSet(document.Value)
		if refs == nil {
			if refs, failure = resolveDefaultBranchRefs(state.root, state.snapshot.Config.Repository); failure != nil {
				return nil, Mutation{}, failure
			}
		}
		status, failure := derivedChangeSetStatus(state, refs, document.Path)
		if failure != nil {
			return nil, Mutation{}, failure
		}
		if parsed.has("status") && status != parsed.one("status") {
			continue
		}
		if parsed.has("interface") && !slices.Contains(value.AffectedInterfaces, parsed.one("interface")) {
			continue
		}
		if parsed.has("scope") && !slices.Contains(value.AffectedScopes, parsed.one("scope")) {
			continue
		}
		item := changeSetListItem{changeSetJSON: toChangeSetJSON(value), Status: status}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return struct {
		ChangeSets []changeSetListItem `json:"change_sets"`
	}{ChangeSets: items}, Mutation{}, nil
}

type changeSetListItem struct {
	changeSetJSON
	Status string `json:"status"`
}

func runChangeSetShow(args []string) (any, Mutation, *commandFailure) {
	parsed, failure := parseOptions(args, valueOptions("change-set", "at"))
	if failure != nil {
		return nil, Mutation{}, failure
	}
	id, failure := requireOption(parsed, "change-set")
	if failure != nil {
		return nil, Mutation{}, failure
	}
	if err := records.ValidateChangeSetID(id); err != nil {
		return nil, Mutation{}, invalidIDFailure("change_set.invalid_id", "Change-set", id)
	}
	if parsed.has("at") {
		return runChangeSetShowAt(id, parsed.one("at"))
	}
	state, failure := loadState()
	if failure != nil {
		return nil, Mutation{}, failure
	}
	index, _, exists := findChangeSet(state.snapshot, id)
	if !exists {
		return nil, Mutation{}, validationFailure("change_set.not_found", fmt.Sprintf("Change set %s was not found.", id), nil)
	}
	refs, failure := resolveDefaultBranchRefs(state.root, state.snapshot.Config.Repository)
	if failure != nil {
		return nil, Mutation{}, failure
	}
	status, failure := derivedChangeSetStatus(state, refs, state.snapshot.ChangeSets[index].Path)
	if failure != nil {
		return nil, Mutation{}, failure
	}
	return changeSetShowResult(state.snapshot, index, status), Mutation{}, nil
}

// runChangeSetShowAt reads the manifest from the tree of one commit. The
// working tree is not read, so an uncommitted edit does not change the result.
// A commit without the project configuration holds no manifest either.
func runChangeSetShowAt(id, at string) (any, Mutation, *commandFailure) {
	commit, failure := parseRevisionOption(at)
	if failure != nil {
		return nil, Mutation{}, failure
	}
	root, failure := resolveRoot()
	if failure != nil {
		return nil, Mutation{}, failure
	}
	if failure := requireRevisionCommit(root, commit); failure != nil {
		return nil, Mutation{}, failure
	}
	notFound := validationFailure("change_set.not_found", fmt.Sprintf("Change set %s was not found at commit %s.", id, commit), nil)
	state, cleanup, failure := loadRevisionState(root, commit)
	if failure != nil {
		if failure.Code == "project.not_initialized" {
			return nil, Mutation{}, notFound
		}
		return nil, Mutation{}, failure
	}
	defer cleanup()
	index, _, exists := findChangeSet(state.snapshot, id)
	if !exists {
		return nil, Mutation{}, notFound
	}
	status := "proposed"
	approved, failure := commitOnDefaultBranch(root, state.snapshot.Config.Repository, commit)
	if failure != nil {
		return nil, Mutation{}, failure
	}
	if approved {
		status = "approved"
	}
	return changeSetShowResult(state.snapshot, index, status), Mutation{}, nil
}

func changeSetShowResult(snapshot specvalidation.Snapshot, index int, status string) changeSetShowData {
	value := records.CanonicalChangeSet(snapshot.ChangeSets[index].Value)
	manifestPath := snapshot.ChangeSets[index].Path
	return changeSetShowData{
		ChangeSet:       toChangeSetJSON(value),
		Status:          status,
		ChangedCount:    len(value.Operations) + len(value.InterfaceOperations) + len(value.ArtifactOperations),
		ApplicableCount: applicableCount(value.ImpactAssessment),
		ManifestPath:    manifestPath,
		Paths:           changeSetTouchedPaths(snapshot, value, manifestPath),
	}
}

type changeSetShowData struct {
	ChangeSet       changeSetJSON `json:"change_set"`
	Status          string        `json:"status"`
	ChangedCount    int           `json:"changed_count"`
	ApplicableCount int           `json:"applicable_count"`
	ManifestPath    string        `json:"manifest_path"`
	Paths           []string      `json:"paths"`
}

func runChangeSetUpdate(args []string, stdin io.Reader) (any, Mutation, *commandFailure) {
	parsed, failure := parseOptions(args, valueOptions(
		"change-set", "intent", "affected-interface", "affected-scope", "base-commit",
		"implementation-required", "implementation-rationale", "impact-file",
	))
	if failure != nil {
		return nil, Mutation{}, failure
	}
	id, failure := requireOption(parsed, "change-set")
	if failure != nil {
		return nil, Mutation{}, failure
	}
	if !hasChangeSetUpdate(parsed) {
		return nil, Mutation{}, usageFailure("change-set update requires at least one field to update")
	}
	state, failure := loadState()
	if failure != nil {
		return nil, Mutation{}, failure
	}
	index, current, failure := proposedChangeSetForUpdate(state, id, parsed.has("base-commit"))
	if failure != nil {
		return nil, Mutation{}, failure
	}
	before := records.CanonicalChangeSet(current)
	updated := cloneChangeSet(before)
	if parsed.has("intent") {
		updated.Intent = parsed.one("intent")
	}
	if parsed.has("affected-interface") {
		updated.AffectedInterfaces = append([]string{}, parsed.list("affected-interface")...)
	}
	if parsed.has("affected-scope") {
		updated.AffectedScopes = append([]string{}, parsed.list("affected-scope")...)
	}
	if parsed.has("base-commit") {
		baseCommit, failure := parseCommitOption(parsed.one("base-commit"))
		if failure != nil {
			return nil, Mutation{}, failure
		}
		if !commitExists(state.root, baseCommit) {
			return nil, Mutation{}, validationFailure("change_set.invalid_base", "The base commit must be the full ID of a commit in the local repository, not of a tag or another object.", nil)
		}
		onHead, failure := isAncestor(state.root, baseCommit, state.head)
		if failure != nil {
			return nil, Mutation{}, failure
		}
		if !onHead {
			return nil, Mutation{}, conflictFailure("change_set.base_mismatch", fmt.Sprintf("Change set %s cannot refresh to %s, because the working tree at %s does not descend from it.", id, baseCommit, state.head), nil)
		}
		forward, failure := isAncestor(state.root, before.BaseCommit, baseCommit)
		if failure != nil {
			return nil, Mutation{}, failure
		}
		if !forward {
			return nil, Mutation{}, conflictFailure("change_set.base_mismatch", fmt.Sprintf("Change set %s cannot refresh to %s, because it does not descend from the recorded base %s.", id, baseCommit, before.BaseCommit), nil)
		}
		updated.BaseCommit = baseCommit
	}
	if parsed.has("implementation-required") {
		required, failure := parseBoolOption(parsed, "implementation-required")
		if failure != nil {
			return nil, Mutation{}, failure
		}
		updated.ImplementationRequired = required
	}
	if parsed.has("implementation-rationale") {
		updated.ImplementationRationale = parsed.one("implementation-rationale")
	}
	if parsed.has("impact-file") {
		assessment, failure := readImpactFile(parsed.one("impact-file"), stdin)
		if failure != nil {
			return nil, Mutation{}, failure
		}
		updated.ImpactAssessment = assessment
	}
	updated = records.CanonicalChangeSet(updated)
	staged := cloneSnapshot(state.snapshot)
	staged.ChangeSets[index].Value = updated
	allowDraft := !parsed.has("impact-file")
	if failure := validateCandidateForChangeSet(staged, id, allowDraft); failure != nil {
		return nil, Mutation{}, failure
	}
	writes := []fileWrite{}
	if err := addWrite(&writes, staged.ChangeSets[index].Path, updated); err != nil {
		return nil, Mutation{}, internalFailure("the change set could not be serialized")
	}
	if err := addConfigWrite(state.root, &staged, &writes, state.observed); err != nil {
		return nil, Mutation{}, configWriteFailure(err, "the project configuration could not be serialized")
	}
	mutation, failure := applyStateTransaction(state, writes, func() *commandFailure { return persistedValidation(state.root, allowDraft, id) })
	if failure != nil {
		return nil, Mutation{}, failure
	}
	beforeStatus := specvalidation.ImpactForChangeSet(before, before.BaseCommit, staged).AssessmentStatus
	afterStatus := specvalidation.ImpactForChangeSet(updated, updated.BaseCommit, staged).AssessmentStatus
	changedPaths := append([]string(nil), mutation.Paths...)
	sort.Strings(changedPaths)
	return changeSetUpdateData{
		ChangeSetID:      id,
		Before:           changeSetUpdateSummaryJSON(before, beforeStatus, parsed),
		After:            changeSetUpdateSummaryJSON(updated, afterStatus, parsed),
		AssessmentStatus: afterStatus,
		ChangedPaths:     changedPaths,
	}, mutation, nil
}

type changeSetUpdateData struct {
	ChangeSetID      string                 `json:"change_set_id"`
	Before           changeSetUpdateSummary `json:"before"`
	After            changeSetUpdateSummary `json:"after"`
	AssessmentStatus string                 `json:"assessment_status"`
	ChangedPaths     []string               `json:"changed_paths"`
}

type changeSetUpdateSummary struct {
	Intent                  string                 `json:"intent,omitempty"`
	BaseCommit              string                 `json:"base_commit,omitempty"`
	AffectedInterfaces      []string               `json:"affected_interfaces"`
	AffectedScopes          []string               `json:"affected_scopes"`
	ImplementationRequired  *bool                  `json:"implementation_required,omitempty"`
	ImplementationRationale string                 `json:"implementation_rationale,omitempty"`
	AssessmentStatus        string                 `json:"assessment_status"`
	ImpactAssessment        []impactAssessmentJSON `json:"impact_assessment,omitempty"`
}

func runChangeSetCompare(args []string) (any, Mutation, *commandFailure) {
	parsed, failure := parseOptions(args, valueOptions("change-set", "against"))
	if failure != nil {
		return nil, Mutation{}, failure
	}
	id, failure := requireOption(parsed, "change-set")
	if failure != nil {
		return nil, Mutation{}, failure
	}
	if err := records.ValidateChangeSetID(id); err != nil {
		return nil, Mutation{}, invalidIDFailure("change_set.invalid_id", "Change-set", id)
	}
	state, failure := loadState()
	if failure != nil {
		return nil, Mutation{}, failure
	}
	_, value, exists := findChangeSet(state.snapshot, id)
	if !exists {
		return nil, Mutation{}, validationFailure("change_set.not_found", fmt.Sprintf("Change set %s was not found.", id), nil)
	}
	if parsed.has("against") {
		return nil, Mutation{}, validationFailure("change_set.invalid_base", "Comparison against arbitrary revisions via --against is not supported.", nil)
	}
	if strings.TrimSpace(value.BaseCommit) == "" || !commitExists(state.root, value.BaseCommit) {
		return nil, Mutation{}, validationFailure("change_set.invalid_base", "The comparison base commit is not present in the local repository.", nil)
	}
	return specvalidation.CompareChangeSet(value, value.BaseCommit, state.snapshot), Mutation{}, nil
}

func hasChangeSetUpdate(parsed options) bool {
	for _, name := range []string{"intent", "affected-interface", "affected-scope", "base-commit", "implementation-required", "implementation-rationale", "impact-file"} {
		if parsed.has(name) {
			return true
		}
	}
	return false
}

func readImpactFile(path string, stdin io.Reader) ([]records.ImpactAssessment, *commandFailure) {
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(stdin)
		if err != nil {
			return nil, validationFailure("input.invalid_source", "The impact file could not be read from stdin.", nil)
		}
	} else {
		data, err = openAndReadRegularFile(func() (*os.File, error) {
			return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
		})
		if err != nil {
			return nil, validationFailure("input.invalid_source", "The impact file must be an existing regular file.", nil)
		}
	}
	var entries []impactAssessmentJSON
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, validationFailure("change_set.invalid_impact", "The impact file is not a JSON list of complete impact assessments.", nil)
	}
	result := make([]records.ImpactAssessment, 0, len(entries))
	for _, entry := range entries {
		if strings.TrimSpace(entry.RequirementID) == "" || strings.TrimSpace(entry.Disposition) == "" || strings.TrimSpace(entry.Rationale) == "" || strings.TrimSpace(entry.Origin) == "" {
			return nil, validationFailure("change_set.invalid_impact", "Each impact assessment requires requirement_id, disposition, rationale, and origin.", nil)
		}
		result = append(result, records.ImpactAssessment{
			RequirementID: entry.RequirementID,
			Disposition:   entry.Disposition,
			Rationale:     entry.Rationale,
			Origin:        entry.Origin,
		})
	}
	return result, nil
}

func changeSetUpdateSummaryJSON(value records.ChangeSet, status string, parsed options) changeSetUpdateSummary {
	summary := changeSetUpdateSummary{
		AffectedInterfaces: append([]string{}, value.AffectedInterfaces...),
		AffectedScopes:     append([]string{}, value.AffectedScopes...),
		AssessmentStatus:   status,
	}
	if summary.AffectedInterfaces == nil {
		summary.AffectedInterfaces = []string{}
	}
	if summary.AffectedScopes == nil {
		summary.AffectedScopes = []string{}
	}
	if parsed.has("intent") {
		summary.Intent = value.Intent
	}
	if parsed.has("base-commit") {
		summary.BaseCommit = value.BaseCommit
	}
	if parsed.has("implementation-required") {
		required := value.ImplementationRequired
		summary.ImplementationRequired = &required
	}
	if parsed.has("implementation-rationale") {
		summary.ImplementationRationale = value.ImplementationRationale
	}
	if parsed.has("impact-file") || len(value.ImpactAssessment) > 0 {
		summary.ImpactAssessment = make([]impactAssessmentJSON, 0, len(value.ImpactAssessment))
		for _, assessment := range value.ImpactAssessment {
			summary.ImpactAssessment = append(summary.ImpactAssessment, toImpactAssessmentJSON(assessment))
		}
	}
	return summary
}

func derivedChangeSetStatus(state projectState, refs []string, manifestPath string) (string, *commandFailure) {
	approved, failure := changeSetApprovedAt(state.root, refs, manifestPath)
	if failure != nil {
		return "", failure
	}
	if approved {
		return "approved", nil
	}
	return "proposed", nil
}

func changeSetTouchedPaths(snapshot specvalidation.Snapshot, changeSet records.ChangeSet, manifestPath string) []string {
	paths := []string{manifestPath}
	for _, operation := range changeSet.Operations {
		path, err := storePath(snapshot.Config, records.RequirementStore, operation.RequirementID)
		if err == nil {
			paths = append(paths, path)
		}
	}
	for _, operation := range changeSet.InterfaceOperations {
		path, err := storePath(snapshot.Config, records.InterfaceStore, operation.InterfaceID)
		if err == nil {
			paths = append(paths, path)
		}
	}
	artifacts := make(map[string]string, len(snapshot.Config.Artifacts))
	for _, artifact := range snapshot.Config.Artifacts {
		artifacts[artifact.ID] = artifact.Path
	}
	for _, operation := range changeSet.ArtifactOperations {
		if path := artifacts[operation.ArtifactID]; path != "" {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	return uniqueStrings(paths)
}

func applicableCount(values []records.ImpactAssessment) int {
	count := 0
	for _, assessment := range values {
		if assessment.Disposition == "applicable" {
			count++
		}
	}
	return count
}

func parseCommitOption(value string) (string, *commandFailure) {
	value = strings.TrimSpace(value)
	if len(value) != 40 {
		return "", validationFailure("change_set.invalid_base", "Comparison commits must be a full 40-character hexadecimal Git object ID.", nil)
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdefABCDEF", character) {
			return "", validationFailure("change_set.invalid_base", "Comparison commits must be a full 40-character hexadecimal Git object ID.", nil)
		}
	}
	return strings.ToLower(value), nil
}

func validChangeSetStatus(value string) bool {
	return value == "proposed" || value == "approved"
}
