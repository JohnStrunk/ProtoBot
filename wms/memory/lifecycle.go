package memory

import (
	"time"

	"github.com/redhat-et/protobot/wms/validation"
)

func (memory *Memory) applyLifecycleMutationLocked(
	item *validation.WorkItem,
	request validation.Request,
	authorization validation.AuthorizationContext,
	decision validation.Decision,
	evaluation validation.EvaluationContext,
) {
	item.State = decision.After.State
	item.ContractVersion = decision.After.ContractVersion
	switch request.Operation {
	case validation.OperationClaim:
		memory.startLease(item, authorization.Subject, decision.FencingTokenIssued, evaluation.EvaluationTime)
	case validation.OperationRenewLease:
		if item.Lease != nil {
			item.Lease.ExpiresAt = evaluation.EvaluationTime.Add(memory.leaseDuration)
		}
	case validation.OperationRaiseSpecQuestion:
		item.BlockReason = request.Payload.Question
		memory.releaseLease(item)
	case validation.OperationRevalidate:
		item.BlockReason = readinessFailure(item.Readiness)
	case validation.OperationRefreshDependencies:
		memory.applyRefresh(item, evaluation)
	case validation.OperationRefreshActive:
		memory.applyRefresh(item, evaluation)
		if item.State == validation.StateBuilding {
			memory.startLease(item, authorization.Subject, decision.FencingTokenIssued, evaluation.EvaluationTime)
		} else {
			item.BlockReason = refreshedBlockReason(item, evaluation)
			memory.releaseLease(item)
		}
	case validation.OperationReturnToBuilding:
		item.BlockReason = ""
		memory.startLease(item, authorization.Subject, decision.FencingTokenIssued, evaluation.EvaluationTime)
	case validation.OperationBeginMerge:
		item.InspectionRunSealed = true
		item.FindingsTerminal = true
		item.FinalTestsPassed = true
	case validation.OperationMergeConflict:
		item.Reconciliation = validation.ReconciliationEvidence{}
		if item.State == validation.StateBuilding {
			memory.startLease(item, authorization.Subject, decision.FencingTokenIssued, evaluation.EvaluationTime)
		} else {
			item.BlockReason = ""
			memory.releaseLease(item)
		}
	case validation.OperationMergeNotApplied, validation.OperationRecoverLease:
		item.BlockReason = ""
		memory.releaseLease(item)
	case validation.OperationRecordMerge:
		mergeEnvelope := request.Payload.MergeEnvelope
		if authorization.Role == validation.RoleReconciler {
			mergeEnvelope = item.Reconciliation.MergeEnvelope
		}
		item.Reconciliation = validation.ReconciliationEvidence{
			Status:        "merge-recorded",
			GitMutation:   "merged",
			MergeEnvelope: cloneMergeEnvelope(mergeEnvelope),
		}
		item.BlockReason = ""
		memory.releaseLease(item)
	case validation.OperationAbandon:
		item.BlockReason = ""
		memory.releaseLease(item)
	case validation.OperationResolveBlock:
		item.BlockReason = ""
		memory.applyRefresh(item, evaluation)
	}
}

func (memory *Memory) startLease(item *validation.WorkItem, owner, token string, now time.Time) {
	item.Owner = owner
	item.Lease = &validation.Lease{
		Owner:        owner,
		FencingToken: token,
		ExpiresAt:    now.Add(memory.leaseDuration),
	}
}

func (memory *Memory) releaseLease(item *validation.WorkItem) {
	item.Owner = ""
	item.Lease = nil
}

func (memory *Memory) applyRefresh(item *validation.WorkItem, evaluation validation.EvaluationContext) {
	if evaluation.RefreshReadiness != nil {
		item.Readiness = *evaluation.RefreshReadiness
		item.Readiness.UnresolvedReasons = append([]string(nil), evaluation.RefreshReadiness.UnresolvedReasons...)
	}
	if evaluation.RefreshDependencies != nil {
		item.Dependencies = append([]validation.Dependency(nil), (*evaluation.RefreshDependencies)...)
	}
}

func refreshedBlockReason(item *validation.WorkItem, evaluation validation.EvaluationContext) string {
	readiness := item.Readiness
	if evaluation.RefreshReadiness != nil {
		readiness = *evaluation.RefreshReadiness
	}
	if reason := readinessFailure(readiness); reason != "" {
		return reason
	}
	dependencies := item.Dependencies
	if evaluation.RefreshDependencies != nil {
		dependencies = *evaluation.RefreshDependencies
	}
	if dependency := firstIncompleteDependency(dependencies); dependency != "" {
		return "dependency " + dependency + " is incomplete"
	}
	return "refresh found unresolved work"
}

func readinessFailure(readiness validation.Readiness) string {
	if !readiness.ContractComplete {
		return "the complete work-item contract is missing"
	}
	if !readiness.SourceImmutable {
		return "the source specification is not immutable"
	}
	if !readiness.SpecificationValidated {
		return "the specification has not been validated"
	}
	if !readiness.RequirementReferencesValid {
		return "requirement references do not resolve"
	}
	if !readiness.PipelineEntrySatisfied {
		return "the pipeline entry conditions are incomplete"
	}
	if !readiness.ImpactDispositioned {
		return "impact candidates remain undispositioned"
	}
	if !readiness.PolicyCompatible {
		return "current project policy is incompatible"
	}
	if len(readiness.UnresolvedReasons) > 0 {
		return readiness.UnresolvedReasons[0]
	}
	return ""
}

func firstIncompleteDependency(dependencies []validation.Dependency) string {
	blocking := ""
	for _, dependency := range dependencies {
		if dependency.State != validation.StateCompleted && (blocking == "" || dependency.ID < blocking) {
			blocking = dependency.ID
		}
	}
	return blocking
}
