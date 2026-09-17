package specvalidation

import (
	"fmt"
	"strings"

	"github.com/redhat-et/protobot/ears-manager/internal/records"
)

func validateChangeSets(result *Result, documents []Document[records.ChangeSet], requirements map[string]records.Requirement, interfaces map[string]records.InterfaceRecord, artifacts map[string]records.ArtifactEntry) {
	seen := make(map[string]bool, len(documents))
	for _, document := range sortChangeSetDocuments(documents) {
		validateChangeSet(result, document, requirements, interfaces, artifacts, seen)
	}
}

func validateChangeSet(result *Result, document Document[records.ChangeSet], requirements map[string]records.Requirement, interfaces map[string]records.InterfaceRecord, artifacts map[string]records.ArtifactEntry, seen map[string]bool) {
	value := records.CanonicalChangeSet(document.Value)
	path := safePath(document.Path)
	validateRecordPath(result, document.Path, records.ChangeSetStore, value.ID)
	validateChangeSetIdentity(result, document, value, seen)
	validateChangeSetMetadata(result, document, value)
	validateOperations(result, path, value, requirements, interfaces, artifacts)
	validateAffectedInterfaces(result, path, value, interfaces)
	validateChangeSetPolicy(result, document, path, value)
	validateImpactAssessment(result, path, value, requirements)
	validateCreated(result, path, value.ID, "created", value.Created)
}

func validateChangeSetIdentity(result *Result, document Document[records.ChangeSet], value records.ChangeSet, seen map[string]bool) {
	path := safePath(document.Path)
	validateRequiredString(result, document.Fields, path, value.ID, "id", value.ID, "change_set.missing_field")
	if value.ID == "" {
		return
	}
	if err := records.ValidateChangeSetID(value.ID); err != nil {
		result.add(diagnostic("change_set.invalid_id", path, value.ID, "id", err.Error(), "Use CS-<NNNNN> for change-set IDs."))
	}
	if seen[value.ID] {
		result.add(diagnostic("change_set.duplicate_id", path, value.ID, "id", fmt.Sprintf("Change-set ID %q is declared more than once.", value.ID), "Use a unique stable change-set ID."))
	}
	seen[value.ID] = true
}

func validateChangeSetMetadata(result *Result, document Document[records.ChangeSet], value records.ChangeSet) {
	path := safePath(document.Path)
	validateRequiredString(result, document.Fields, path, value.ID, "base_commit", value.BaseCommit, "change_set.missing_field")
	if value.BaseCommit != "" && !baseCommitPattern.MatchString(value.BaseCommit) {
		result.add(diagnostic("change_set.invalid_base_commit", path, value.ID, "base_commit", "Base commit must be a full 40-character hexadecimal Git object ID.", "Use the full commit SHA, not a branch name or short SHA."))
	}
	validateRequiredString(result, document.Fields, path, value.ID, "intent", value.Intent, "change_set.missing_field")
	validateRequiredList(result, document.Fields, path, value.ID, "operations", value.Operations != nil)
	validateRequiredList(result, document.Fields, path, value.ID, "affected_interfaces", value.AffectedInterfaces != nil)
}

func validateAffectedInterfaces(result *Result, path string, value records.ChangeSet, interfaces map[string]records.InterfaceRecord) {
	for _, interfaceID := range value.AffectedInterfaces {
		if err := records.ValidateInterfaceID(interfaceID); err != nil {
			result.add(diagnostic("change_set.invalid_interface", path, value.ID, "affected_interfaces", err.Error(), "Use a registered interface ID."))
			continue
		}
		if _, exists := interfaces[interfaceID]; !exists {
			result.add(diagnostic("reference.not_found", path, value.ID, "affected_interfaces", fmt.Sprintf("Interface %q is not registered.", interfaceID), "Register the interface or remove it from affected_interfaces."))
		}
	}
	for _, scope := range value.AffectedScopes {
		if strings.TrimSpace(scope) == "" {
			result.add(diagnostic("change_set.invalid_scope", path, value.ID, "affected_scopes", "Affected scopes must not be empty.", "Use non-empty project-defined scopes."))
		}
	}
}

func validateChangeSetPolicy(result *Result, document Document[records.ChangeSet], path string, value records.ChangeSet) {
	if document.Fields != nil && !document.Fields["implementation_required"] {
		result.add(diagnostic("change_set.missing_field", path, value.ID, "implementation_required", "Required field \"implementation_required\" is missing.", "Declare whether the change set requires implementation work."))
	}
	if !value.ImplementationRequired && strings.TrimSpace(value.ImplementationRationale) == "" {
		result.add(diagnostic("change_set.missing_field", path, value.ID, "implementation_rationale", "A change set that does not require implementation must provide a rationale.", "Explain why no implementation work is needed."))
	}
}

func validateRequiredList(result *Result, fields map[string]bool, path, recordID, field string, inferred bool) {
	if fields != nil && !fields[field] {
		result.add(diagnostic("change_set.missing_field", path, recordID, field, fmt.Sprintf("Required field %q is missing.", field), fmt.Sprintf("Provide %q, even when it is an empty list.", field)))
		return
	}
	if fields == nil && !inferred {
		result.add(diagnostic("change_set.missing_field", path, recordID, field, fmt.Sprintf("Required field %q is missing.", field), fmt.Sprintf("Provide %q.", field)))
	}
}

func validateOperations(result *Result, path string, value records.ChangeSet, requirements map[string]records.Requirement, interfaces map[string]records.InterfaceRecord, artifacts map[string]records.ArtifactEntry) {
	validateRequirementOperations(result, path, value, requirements)
	validateInterfaceOperations(result, path, value, interfaces)
	validateArtifactOperations(result, path, value, artifacts)
}

func validateRequirementOperations(result *Result, path string, value records.ChangeSet, requirements map[string]records.Requirement) {
	seenRequirements := make(map[string]bool, len(value.Operations))
	for index, operation := range value.Operations {
		field := fmt.Sprintf("operations[%d]", index)
		if !changeSetActions[operation.Action] {
			result.add(diagnostic("change_set.invalid_operation", path, value.ID, field+".action", fmt.Sprintf("Unsupported requirement operation %q.", operation.Action), "Use add, revise, or retire."))
		}
		if err := records.ValidateRequirementID(operation.RequirementID); err != nil {
			result.add(diagnostic("change_set.invalid_reference", path, value.ID, field+".requirement_id", err.Error(), "Use a valid requirement ID."))
		} else if _, exists := requirements[operation.RequirementID]; !exists {
			result.add(diagnostic("reference.not_found", path, value.ID, field+".requirement_id", fmt.Sprintf("Requirement %q is not registered.", operation.RequirementID), "Add the requirement or correct the operation reference."))
		}
		if seenRequirements[operation.RequirementID] {
			result.add(diagnostic("change_set.duplicate_operation", path, value.ID, field+".requirement_id", fmt.Sprintf("Requirement %q appears in more than one operation.", operation.RequirementID), "Use one operation per changed requirement."))
		}
		seenRequirements[operation.RequirementID] = true
	}

}

func validateInterfaceOperations(result *Result, path string, value records.ChangeSet, interfaces map[string]records.InterfaceRecord) {
	seenInterfaces := make(map[string]bool, len(value.InterfaceOperations))
	for index, operation := range value.InterfaceOperations {
		field := fmt.Sprintf("interface_operations[%d]", index)
		if !interfaceActions[operation.Action] {
			result.add(diagnostic("change_set.invalid_operation", path, value.ID, field+".action", fmt.Sprintf("Unsupported interface operation %q.", operation.Action), "Use add or revise."))
		}
		if err := records.ValidateInterfaceID(operation.InterfaceID); err != nil {
			result.add(diagnostic("change_set.invalid_reference", path, value.ID, field+".interface_id", err.Error(), "Use a valid interface ID."))
		} else if _, exists := interfaces[operation.InterfaceID]; !exists {
			result.add(diagnostic("reference.not_found", path, value.ID, field+".interface_id", fmt.Sprintf("Interface %q is not registered.", operation.InterfaceID), "Add the interface or correct the operation reference."))
		}
		if seenInterfaces[operation.InterfaceID] {
			result.add(diagnostic("change_set.duplicate_operation", path, value.ID, field+".interface_id", fmt.Sprintf("Interface %q appears in more than one operation.", operation.InterfaceID), "Use one operation per changed interface."))
		}
		seenInterfaces[operation.InterfaceID] = true
	}

}

func validateArtifactOperations(result *Result, path string, value records.ChangeSet, artifacts map[string]records.ArtifactEntry) {
	seenArtifacts := make(map[string]bool, len(value.ArtifactOperations))
	for index, operation := range value.ArtifactOperations {
		field := fmt.Sprintf("artifact_operations[%d]", index)
		if !artifactActions[operation.Action] {
			result.add(diagnostic("change_set.invalid_operation", path, value.ID, field+".action", fmt.Sprintf("Unsupported artifact operation %q.", operation.Action), "Use add or revise."))
		}
		if err := records.ValidateArtifactID(operation.ArtifactID); err != nil {
			result.add(diagnostic("change_set.invalid_reference", path, value.ID, field+".artifact_id", err.Error(), "Use a valid artifact ID."))
		} else if _, exists := artifacts[operation.ArtifactID]; !exists {
			result.add(diagnostic("reference.not_found", path, value.ID, field+".artifact_id", fmt.Sprintf("Artifact %q is not registered.", operation.ArtifactID), "Register the artifact or correct the operation reference."))
		}
		if seenArtifacts[operation.ArtifactID] {
			result.add(diagnostic("change_set.duplicate_operation", path, value.ID, field+".artifact_id", fmt.Sprintf("Artifact %q appears in more than one operation.", operation.ArtifactID), "Use one operation per changed artifact."))
		}
		seenArtifacts[operation.ArtifactID] = true
	}
}

func validateImpactAssessment(result *Result, path string, value records.ChangeSet, requirements map[string]records.Requirement) {
	changed := make(map[string]bool, len(value.Operations))
	for _, operation := range value.Operations {
		changed[operation.RequirementID] = true
	}
	seen := make(map[string]bool, len(value.ImpactAssessment))
	for index, assessment := range value.ImpactAssessment {
		field := fmt.Sprintf("impact_assessment[%d]", index)
		if err := records.ValidateRequirementID(assessment.RequirementID); err != nil {
			result.add(diagnostic("change_set.invalid_impact", path, value.ID, field+".requirement_id", err.Error(), "Use a valid unchanged requirement ID."))
		} else if _, exists := requirements[assessment.RequirementID]; !exists {
			result.add(diagnostic("reference.not_found", path, value.ID, field+".requirement_id", fmt.Sprintf("Impact requirement %q is not registered.", assessment.RequirementID), "Reference an existing unchanged requirement."))
		} else if changed[assessment.RequirementID] {
			result.add(diagnostic("change_set.invalid_impact", path, value.ID, field+".requirement_id", fmt.Sprintf("Impact requirement %q is also changed by this set.", assessment.RequirementID), "Record impact only for unchanged requirements."))
		}
		if seen[assessment.RequirementID] {
			result.add(diagnostic("change_set.duplicate_impact", path, value.ID, field+".requirement_id", fmt.Sprintf("Impact requirement %q is repeated.", assessment.RequirementID), "Record each impact candidate once."))
		}
		seen[assessment.RequirementID] = true
		if !impactDispositions[assessment.Disposition] {
			result.add(diagnostic("change_set.invalid_impact", path, value.ID, field+".disposition", fmt.Sprintf("Unsupported impact disposition %q.", assessment.Disposition), "Use applicable or not-applicable."))
		}
		if strings.TrimSpace(assessment.Rationale) == "" {
			result.add(diagnostic("change_set.missing_field", path, value.ID, field+".rationale", "Impact assessment rationale is required.", "Explain the disposition."))
		}
		if !impactOrigins[assessment.Origin] {
			result.add(diagnostic("change_set.invalid_impact", path, value.ID, field+".origin", fmt.Sprintf("Unsupported impact origin %q.", assessment.Origin), "Use mechanical or semantic."))
		}
	}
}
