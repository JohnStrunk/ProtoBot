package memory

import (
	"sort"
	"time"

	"github.com/redhat-et/protobot/wms/validation"
)

func (m *Memory) applyLifecycleMutationLocked(
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
		// A lease handoff clears stale reconciliation evidence: the new lease
		// holder must produce fresh WMS observations.
		item.Reconciliation = validation.ReconciliationEvidence{}
		m.startLease(item, authorization.Subject, decision.FencingTokenIssued, evaluation.EvaluationTime)
	case validation.OperationRenewLease:
		if item.Lease != nil {
			item.Lease.ExpiresAt = evaluation.EvaluationTime.Add(m.leaseDuration)
		}
	case validation.OperationRaiseSpecQuestion:
		item.BlockReason = request.Payload.Question
		m.releaseLease(item)
	case validation.OperationRevalidate:
		m.applyRefresh(item, evaluation)
		item.BlockReason = validation.ReadinessFailure(effectiveReadiness(item, evaluation))
	case validation.OperationRefreshDependencies:
		m.applyRefresh(item, evaluation)
	case validation.OperationRefreshActive:
		m.applyRefresh(item, evaluation)
		if item.State == validation.StateBuilding {
			m.startLease(item, authorization.Subject, decision.FencingTokenIssued, evaluation.EvaluationTime)
		} else {
			item.BlockReason = refreshedBlockReason(item, evaluation)
			m.releaseLease(item)
		}
	case validation.OperationReturnToBuilding:
		item.BlockReason = ""
		m.startLease(item, authorization.Subject, decision.FencingTokenIssued, evaluation.EvaluationTime)
	case validation.OperationBeginMerge:
		// The tested candidate exists only after Building and Inspecting:
		// the Job Site supplies it at begin-merge and the WMS records it as
		// the expected merge envelope record-merge compares against.
		item.ExpectedMerge = cloneMergeEnvelope(request.Payload.MergeEnvelope)
	case validation.OperationMergeConflict:
		item.Reconciliation = validation.ReconciliationEvidence{}
		if item.State == validation.StateBuilding {
			m.startLease(item, authorization.Subject, decision.FencingTokenIssued, evaluation.EvaluationTime)
		} else {
			item.BlockReason = ""
			m.releaseLease(item)
		}
	case validation.OperationMergeNotApplied, validation.OperationRecoverLease:
		// Leaving merging or recovering a lease invalidates the reconciliation
		// evidence that authorized it; the next transition needs a fresh WMS
		// observation.
		item.Reconciliation = validation.ReconciliationEvidence{}
		item.BlockReason = ""
		m.releaseLease(item)
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
		m.releaseLease(item)
	case validation.OperationAbandon:
		item.BlockReason = ""
		m.releaseLease(item)
	case validation.OperationResolveBlock:
		item.BlockReason = ""
		m.applyRefresh(item, evaluation)
	}
}

func (m *Memory) startLease(item *validation.WorkItem, owner, token string, now time.Time) {
	item.Owner = owner
	item.Lease = &validation.Lease{
		Owner:        owner,
		FencingToken: token,
		ExpiresAt:    now.Add(m.leaseDuration),
	}
}

func (m *Memory) releaseLease(item *validation.WorkItem) {
	item.Owner = ""
	item.Lease = nil
}

func (m *Memory) applyRefresh(item *validation.WorkItem, evaluation validation.EvaluationContext) {
	if evaluation.RefreshReadiness != nil {
		item.Readiness = *evaluation.RefreshReadiness
		item.Readiness.UnresolvedReasons = append([]string(nil), evaluation.RefreshReadiness.UnresolvedReasons...)
		sort.Strings(item.Readiness.UnresolvedReasons)
	}
	if evaluation.RefreshDependencies != nil {
		item.Dependencies = append([]validation.Dependency(nil), (*evaluation.RefreshDependencies)...)
	}
}

func refreshedBlockReason(item *validation.WorkItem, evaluation validation.EvaluationContext) string {
	readiness := effectiveReadiness(item, evaluation)
	if reason := validation.ReadinessFailure(readiness); reason != "" {
		return reason
	}
	dependencies := item.Dependencies
	if evaluation.RefreshDependencies != nil {
		dependencies = *evaluation.RefreshDependencies
	}
	if dependency, incomplete := validation.FirstIncompleteDependency(dependencies); incomplete {
		if dependency == "" {
			return "an unnamed dependency is incomplete"
		}
		return "dependency " + dependency + " is incomplete"
	}
	return "refresh found unresolved work"
}

func effectiveReadiness(item *validation.WorkItem, evaluation validation.EvaluationContext) validation.Readiness {
	if evaluation.RefreshReadiness != nil {
		return *evaluation.RefreshReadiness
	}
	return item.Readiness
}
