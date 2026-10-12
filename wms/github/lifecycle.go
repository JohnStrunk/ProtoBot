package github

import (
	"errors"
	"time"

	"github.com/redhat-et/protobot/wms/adapter"
	"github.com/redhat-et/protobot/wms/validation"
)

func (a *Adapter) executeLifecycleLocked(call adapter.CallRequest, authorization validation.AuthorizationContext) adapter.Result {
	request, rejection := a.normalizeLifecycleRequest(call, authorization)
	if rejection != nil {
		return rejectedResult(call.Operation, rejection)
	}
	evaluation := validation.EvaluationContext{
		Authority:        validation.AuthorityAuthoritative,
		EvaluationTime:   a.now(),
		LeaseDuration:    a.leaseDuration,
		NextFencingToken: a.nextFenceTokenLocked(),
		Approvals:        a.approvalSnapshotLocked(),
	}

	var current *validation.WorkItem
	item, existsErr := a.tryLoadWorkItem(request.WorkItemID)
	if existsErr != nil && !errors.Is(existsErr, ErrNotFound) {
		return rejectedResult(call.Operation, wmsUnavailableRejection())
	}
	exists := existsErr == nil
	if request.Operation == validation.OperationMaterialize {
		if exists {
			copy := cloneWorkItem(item)
			current = &copy
		}
		if request.Payload.WorkItem != nil {
			dependencies, rejection := a.liveDependenciesLocked(*request.Payload.WorkItem)
			if rejection != nil {
				return rejectedResult(call.Operation, rejection)
			}
			evaluation.RefreshDependencies = &dependencies
		}
	} else {
		if !exists {
			authorized := validation.AuthorizeLifecycle(
				request.Authorization,
				request.Operation,
				request.ProjectID,
				request.WorkItemID,
				request.Payload.ChangeSetID,
				request.References,
				evaluation.EvaluationTime,
			) == nil && (call.PolicyVersion == "" || call.PolicyVersion == authorization.PolicyVersion)
			if authorized && request.IdempotencyKey != "" {
				fingerprint := validation.RequestFingerprint(request)
				if result, handled := a.replayLifecycleRequest(call, request, fingerprint); handled {
					return result
				}
				result := resultFromDecision(call.Operation, validation.Evaluate(request, nil, evaluation))
				if shouldRememberIdempotency(result) {
					a.rememberIdempotencyLocked(request.IdempotencyKey, fingerprint, result)
				}
				return result
			}
			return resultFromDecision(call.Operation, validation.Evaluate(request, nil, evaluation))
		}
		copy := cloneWorkItem(item)
		current = &copy
	}

	if validation.AuthorizeLifecycle(
		request.Authorization,
		request.Operation,
		request.ProjectID,
		request.WorkItemID,
		request.Payload.ChangeSetID,
		request.References,
		evaluation.EvaluationTime,
	) != nil {
		return resultFromDecision(call.Operation, validation.Evaluate(request, current, evaluation))
	}
	if call.PolicyVersion != "" && call.PolicyVersion != authorization.PolicyVersion {
		decision := validation.RejectionDecision(request, validation.AuthorityAuthoritative,
			unauthorizedRejection(call.Operation, call.WorkItemID, authorization.PolicyVersion))
		return resultFromDecision(call.Operation, decision)
	}
	if request.IdempotencyKey == "" {
		return rejectedResult(call.Operation, invalidRequest("idempotency_key", "authoritative mutations require an idempotency key"))
	}
	fingerprint := validation.RequestFingerprint(request)
	if result, handled := a.replayLifecycleRequest(call, request, fingerprint); handled {
		return result
	}
	if current != nil && request.Operation == validation.OperationClaim {
		if result, handled := a.reconcileAppliedClaim(call, request, fingerprint, *current); handled {
			return result
		}
	}
	if current != nil && request.Operation == validation.OperationMaterialize {
		dependencies, rejection := a.liveDependenciesLocked(*current)
		if rejection != nil {
			return rejectedResult(call.Operation, rejection)
		}
		evaluation.RefreshDependencies = &dependencies
	}
	return a.applyLifecycleRequest(call.Operation, request, current, authorization, fingerprint, evaluation)
}

func (a *Adapter) normalizeLifecycleRequest(call adapter.CallRequest, authorization validation.AuthorizationContext) (validation.Request, *validation.Rejection) {
	var payload validation.Payload
	if err := decodePayload(call.Payload, &payload); err != nil {
		return validation.Request{}, invalidRequest("payload", err.Error())
	}
	operation := validation.Operation(call.Operation)
	workItemID := call.WorkItemID
	if operation == validation.OperationMaterialize && workItemID == "" && payload.WorkItem != nil {
		workItemID = payload.WorkItem.ID
	}
	if call.HumanApprovalID != "" {
		payload.HumanApprovalID = call.HumanApprovalID
	}
	return validation.Request{
		Operation:               operation,
		ProjectID:               a.projectID,
		WorkItemID:              workItemID,
		MaterializationKey:      requestMaterializationKey(call, payload),
		IdempotencyKey:          call.IdempotencyKey,
		ExpectedState:           call.ExpectedState,
		ExpectedContractVersion: call.ExpectedContractVersion,
		FencingToken:            call.FencingToken,
		References:              lifecycleReferences(operation, payload),
		Payload:                 payload,
		Authorization:           authorization,
	}, nil
}

func lifecycleReferences(operation validation.Operation, payload validation.Payload) []string {
	var envelope *validation.MergeEnvelope
	if operation == validation.OperationMaterialize && payload.WorkItem != nil {
		envelope = payload.WorkItem.ExpectedMerge
	}
	if envelope == nil {
		return nil
	}
	refs := make([]string, 0, 4)
	for _, ref := range []string{envelope.Target, envelope.IntegrationHead, envelope.MergeCommit, envelope.InspectionRunID} {
		if ref != "" {
			refs = append(refs, ref)
		}
	}
	return refs
}

func requestMaterializationKey(call adapter.CallRequest, payload validation.Payload) string {
	if call.MaterializationKey != "" {
		return call.MaterializationKey
	}
	if payload.MaterializationKey != "" {
		return payload.MaterializationKey
	}
	if payload.WorkItem != nil {
		return payload.WorkItem.MaterializationKey
	}
	return ""
}

// reconcileAppliedClaim rebuilds the applied result a claim must have
// returned the first time, from the durable LastClaim binding on the
// work-item issue, when call is an exact idempotency-key-and-fingerprint
// retry of it. Without this check, a retry after an ambiguous (lost
// response) UpdateIssue would load the already-claimed item and freeze a
// determinate DUPLICATE_CLAIM under the claim key, which the Job Site can
// never reconcile. handled is true whenever the caller must return result
// immediately.
func (a *Adapter) reconcileAppliedClaim(call adapter.CallRequest, request validation.Request, fingerprint string, current validation.WorkItem) (adapter.Result, bool) {
	number, ok := a.idx.workItemIssue[current.ID]
	if !ok {
		return adapter.Result{}, false
	}
	issue, err := a.client.GetIssue(a.ctx, number)
	if err != nil {
		return rejectedResult(call.Operation, wmsUnavailableRejection()), true
	}
	doc, err := decodeBody(issue.Body)
	if err != nil || doc.Kind != kindWorkItem || doc.LastClaim == nil {
		return adapter.Result{}, false
	}
	claim := doc.LastClaim
	if claim.Operation != string(request.Operation) || claim.IdempotencyKey != request.IdempotencyKey || claim.Fingerprint != fingerprint {
		return adapter.Result{}, false
	}
	result := newResult(call.Operation)
	result.Outcome = adapter.OutcomeApplied
	result.Mutation = adapter.MutationApplied
	result.Idempotency = adapter.IdempotencyNew
	result.WorkItemID = current.ID
	result.WorkItemState = current.State
	result.ContractVersion = current.ContractVersion
	result.Resource = cloneWorkItem(current)
	if claim.FencingToken != "" {
		result.Decision = &validation.Decision{
			Outcome:            validation.OutcomeAllowed,
			Authority:          validation.AuthorityAuthoritative,
			Operation:          request.Operation,
			Before:             &validation.StateVersion{State: request.ExpectedState, ContractVersion: derefVersion(request.ExpectedContractVersion)},
			After:              &validation.StateVersion{State: current.State, ContractVersion: current.ContractVersion},
			FencingTokenIssued: claim.FencingToken,
		}
	}
	return markReplayed(result), true
}

func derefVersion(version *uint64) uint64 {
	if version == nil {
		return 0
	}
	return *version
}

func (a *Adapter) replayLifecycleRequest(call adapter.CallRequest, request validation.Request, fingerprint string) (adapter.Result, bool) {
	if replay, conflict := a.checkIdempotencyLocked(request.IdempotencyKey, fingerprint); replay != nil {
		return *replay, true
	} else if conflict != nil {
		decision := validation.RejectionDecision(request, validation.AuthorityAuthoritative, conflict)
		return resultFromDecision(call.Operation, decision), true
	}
	if request.Operation == validation.OperationMaterialize {
		if replay, rejection := a.checkMaterializationLocked(request); replay != nil {
			a.rememberIdempotencyLocked(request.IdempotencyKey, fingerprint, *replay)
			return *replay, true
		} else if rejection != nil {
			decision := validation.RejectionDecision(request, validation.AuthorityAuthoritative, rejection)
			result := resultFromDecision(call.Operation, decision)
			a.rememberIdempotencyLocked(request.IdempotencyKey, fingerprint, result)
			return result, true
		}
	}
	return adapter.Result{}, false
}

func (a *Adapter) checkMaterializationLocked(request validation.Request) (*adapter.Result, *validation.Rejection) {
	if request.Payload.WorkItem == nil {
		return nil, nil
	}
	entry, rejection := a.loadMaterializationEntryLocked(request.MaterializationKey)
	if rejection != nil {
		return nil, rejection
	}
	if entry == nil {
		return nil, nil
	}
	if rejection := validation.MaterializationTargetRejection(request, request.Payload.WorkItem); rejection != nil {
		return nil, rejection
	}
	source := validation.CanonicalMaterializationSource(*request.Payload.WorkItem, request.Payload.ChangeType)
	if rejection := validation.MaterializationSourceMergeRejection(source); rejection != nil {
		return nil, rejection
	}
	fingerprint := validation.SourceFingerprint(source)
	if fingerprint != entry.fingerprint {
		return nil, wmsRejection(
			validation.CodeIdempotencyConflict,
			"The materialization key is already bound to a different source contract.",
			map[string]any{
				"conflict_kind":          "materialization-source",
				"key_scope":              "project",
				"materialization_key":    request.MaterializationKey,
				"recorded_source_digest": entry.fingerprint,
			},
			validation.RetryReconcile,
		)
	}
	result := markReplayed(cloneResult(entry.result))
	return &result, nil
}

// loadMaterializationEntryLocked returns the in-process binding, or rebinds from
// a durable GitHub scan when the process-local index has no entry. The binding
// may be a materialized work item or an omitted reservation: both are
// create-or-return reservations under the materialization_key (VR-039), so a
// cached entry is a hit regardless of whether it carries a work-item ID. Only
// a definitive ErrNotFound establishes that no durable binding exists; any
// other scan error is inconclusive and must be reported as WMS_UNAVAILABLE,
// or materialize could create a second issue under the same
// materialization_key.
func (a *Adapter) loadMaterializationEntryLocked(key string) (*materializationEntry, *validation.Rejection) {
	if entry, ok := a.idx.materializationKey[key]; ok {
		copied := entry
		return &copied, nil
	}
	binding, err := a.findMaterializationBinding(key)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, nil
		}
		return nil, wmsUnavailableRejection()
	}
	a.idx.materializationKey[key] = materializationEntryForBinding(binding)
	entry := a.idx.materializationKey[key]
	return &entry, nil
}

func (a *Adapter) applyLifecycleRequest(
	operation string,
	request validation.Request,
	current *validation.WorkItem,
	authorization validation.AuthorizationContext,
	fingerprint string,
	evaluation validation.EvaluationContext,
) adapter.Result {
	decision := validation.Evaluate(request, current, evaluation)
	result := resultFromDecision(operation, decision)
	switch decision.Outcome {
	case validation.OutcomeAllowed:
		switch request.Operation {
		case validation.OperationMaterialize:
			item := cloneWorkItem(validation.CanonicalMaterializationSource(*request.Payload.WorkItem, request.Payload.ChangeType))
			item.State = decision.After.State
			item.ContractVersion = decision.After.ContractVersion
			item.MaterializationKey = request.MaterializationKey
			item.Owner = ""
			item.Lease = nil
			if evaluation.RefreshDependencies != nil {
				item.Dependencies = append([]validation.Dependency(nil), (*evaluation.RefreshDependencies)...)
			}
			item.Reconciliation = validation.ReconciliationEvidence{}
			item.InspectionRunSealed = false
			item.FindingsTerminal = false
			item.FinalTestsPassed = false
			if item.State == validation.StateBlocked {
				item.BlockReason = validation.ReadinessFailure(item.Readiness)
			}
			sourceFingerprint := ""
			if decision.MaterializationReservation != nil {
				sourceFingerprint = decision.MaterializationReservation.SourceFingerprint
			}
			issue, err := a.persistWorkItemIssue(item, sourceFingerprint)
			if err != nil {
				if errorsIsTransient(err) {
					if !a.rebindMaterializationAfterUnknown(request.MaterializationKey, sourceFingerprint, &result) {
						return unknownMutationResult(operation)
					}
					break
				}
				return rejectedResult(operation, wmsUnavailableRejection())
			}
			result.WorkItemID = item.ID
			result.WorkItemState = item.State
			result.ContractVersion = item.ContractVersion
			result.Resource = cloneWorkItem(item)
			a.bindWorkItemLocked(item, storedDocument{
				Kind:               kindWorkItem,
				ProjectID:          a.projectID,
				WorkItem:           &item,
				MaterializationKey: request.MaterializationKey,
				SourceFingerprint:  sourceFingerprint,
			}, issue.Number, result)
		case validation.OperationClaim:
			updated := cloneWorkItem(*current)
			// Evaluate already enforced the expected state/contract version
			// against the snapshot read at the top of executeLifecycleLocked,
			// and the Coordinator lock held for that whole call serializes this
			// read-evaluate-write sequence against every other claim that
			// shares the same Coordinator. GitHub Issues has no conditional-
			// update primitive of its own (UpdateIssueInput carries no
			// precondition), so this guarantee holds only across Adapter
			// instances that share one Coordinator (see Config.Coordinator) and
			// does not detect an issue edited outside the adapter.
			updated.State = decision.After.State
			updated.ContractVersion = decision.After.ContractVersion
			updated.Reconciliation = validation.ReconciliationEvidence{}
			a.startLease(&updated, authorization.Subject, decision.FencingTokenIssued, evaluation.EvaluationTime)
			number := a.idx.workItemIssue[updated.ID]
			mutation := &lastMutationRecord{
				Operation:      string(request.Operation),
				IdempotencyKey: request.IdempotencyKey,
				Fingerprint:    fingerprint,
				FencingToken:   decision.FencingTokenIssued,
			}
			if err := a.updateWorkItemIssue(number, updated, workItemIssueUpdateOptions{mutation: mutation}); err != nil {
				if errorsIsTransient(err) {
					return unknownMutationResult(operation)
				}
				return rejectedResult(operation, wmsUnavailableRejection())
			}
			result.WorkItemID = updated.ID
			result.WorkItemState = updated.State
			result.ContractVersion = updated.ContractVersion
			result.Resource = cloneWorkItem(updated)
		}
	case validation.OutcomeOmitted:
		result.Submission = "omitted"
		if request.Operation == validation.OperationMaterialize && request.Payload.WorkItem != nil {
			source := validation.CanonicalMaterializationSource(*request.Payload.WorkItem, request.Payload.ChangeType)
			fingerprint := validation.SourceFingerprint(source)
			// The omitted outcome is still a create-or-return reservation
			// under the materialization_key (VR-039): persist it durably so
			// a later same-key materialize replays this result (or conflicts
			// on a different source) across command keys and restarts, not
			// only under the original idempotency key.
			issue, err := a.persistOmittedReservation(request.MaterializationKey, fingerprint)
			if err != nil {
				if errorsIsTransient(err) {
					if !a.rebindOmittedReservationAfterUnknown(request.MaterializationKey, fingerprint) {
						return unknownMutationResult(operation)
					}
					break
				}
				return rejectedResult(operation, wmsUnavailableRejection())
			}
			a.idx.materializationKey[request.MaterializationKey] = materializationEntry{
				fingerprint: fingerprint,
				result:      cloneResult(result),
				issueNumber: issue.Number,
			}
		}
	}
	if shouldRememberIdempotency(result) {
		a.rememberIdempotencyLocked(request.IdempotencyKey, fingerprint, result)
	}
	return result
}

func (a *Adapter) persistWorkItemIssue(item validation.WorkItem, sourceFingerprint string) (Issue, error) {
	body, err := encodeBody("ProtoBot build work-item record.", storedDocument{
		Kind:               kindWorkItem,
		ProjectID:          a.projectID,
		WorkItem:           &item,
		MaterializationKey: item.MaterializationKey,
		SourceFingerprint:  sourceFingerprint,
	})
	if err != nil {
		return Issue{}, err
	}
	return a.client.CreateIssue(a.ctx, CreateIssueInput{
		Title:  workItemTitle(item),
		Body:   body,
		Labels: []string{LabelWorkItem},
	})
}

// persistOmittedReservation durably records that a materialization_key is
// bound to an omitted source (VR-039). It shares the work-item label and kind
// so the binding scan finds both reservation forms under one label query.
func (a *Adapter) persistOmittedReservation(key, sourceFingerprint string) (Issue, error) {
	body, err := encodeBody("ProtoBot build omitted-materialization record.", storedDocument{
		Kind:               kindWorkItem,
		ProjectID:          a.projectID,
		Omitted:            true,
		MaterializationKey: key,
		SourceFingerprint:  sourceFingerprint,
	})
	if err != nil {
		return Issue{}, err
	}
	return a.client.CreateIssue(a.ctx, CreateIssueInput{
		Title:  omittedReservationTitle(key),
		Body:   body,
		Labels: []string{LabelWorkItem},
	})
}

// rebindOmittedReservationAfterUnknown scans for a durable omitted
// reservation that may have been created despite a transient CreateIssue
// failure. On a fingerprint match it restores the in-process binding so the
// caller's already-computed omitted result stands; returns false when no
// matching reservation is found, leaving the caller to report
// UNKNOWN_MUTATION.
func (a *Adapter) rebindOmittedReservationAfterUnknown(key, wantFingerprint string) bool {
	binding, err := a.findMaterializationBinding(key)
	if err != nil || binding.item != nil {
		return false
	}
	stored := storedSourceFingerprint(binding.doc)
	if wantFingerprint != "" && stored != "" && wantFingerprint != stored {
		return false
	}
	a.idx.materializationKey[key] = materializationEntryForBinding(binding)
	return true
}

// rebindMaterializationAfterUnknown scans for a durable issue that may have been
// created despite a transient CreateIssue failure. On match with the expected
// source fingerprint it binds indexes and fills result; returns false when no
// matching reservation is found.
func (a *Adapter) rebindMaterializationAfterUnknown(key, wantFingerprint string, result *adapter.Result) bool {
	binding, err := a.findMaterializationBinding(key)
	if err != nil || binding.item == nil {
		return false
	}
	stored := storedSourceFingerprint(binding.doc)
	if wantFingerprint != "" && stored != "" && wantFingerprint != stored {
		return false
	}
	result.WorkItemID = binding.item.ID
	result.WorkItemState = binding.item.State
	result.ContractVersion = binding.item.ContractVersion
	result.Resource = cloneWorkItem(*binding.item)
	a.bindWorkItemLocked(*binding.item, binding.doc, binding.number, *result)
	return true
}

func (a *Adapter) tryLoadWorkItem(id string) (validation.WorkItem, error) {
	if id == "" {
		return validation.WorkItem{}, ErrNotFound
	}
	item, _, err := a.loadWorkItem(id)
	return item, err
}

// liveDependenciesLocked observes the current state of item's dependencies.
// A definitive miss (not found, or an item belonging to another project)
// leaves StateInitial. Any other load failure is inconclusive and must abort
// with WMS_UNAVAILABLE: a transient error observed as a missing dependency
// would materialize the item as waiting and freeze that outcome under the
// materialization key, and this slice has no refresh-dependencies operation
// to correct it later. The target load at the top of executeLifecycleLocked
// and backendLoadRejection already refuse to collapse inconclusive loads
// into not-found; dependencies must not either.
func (a *Adapter) liveDependenciesLocked(item validation.WorkItem) ([]validation.Dependency, *validation.Rejection) {
	dependencies := make([]validation.Dependency, 0, len(item.Dependencies))
	for _, dependency := range item.Dependencies {
		state := validation.StateInitial
		observed, err := a.tryLoadWorkItem(dependency.ID)
		switch {
		case errors.Is(err, ErrNotFound):
			// Definitive miss.
		case err != nil:
			return nil, wmsUnavailableRejection()
		case observed.ProjectID != a.projectID:
			// An off-project dependency is not visible in this project.
		default:
			state = observed.State
		}
		dependencies = append(dependencies, validation.Dependency{ID: dependency.ID, State: state})
	}
	return dependencies, nil
}

func (a *Adapter) approvalSnapshotLocked() map[string]validation.ApprovalRecord {
	result := make(map[string]validation.ApprovalRecord, len(a.idx.approvals))
	for id, approval := range a.idx.approvals {
		result[id] = approval
	}
	return result
}

func (a *Adapter) startLease(item *validation.WorkItem, owner, token string, now time.Time) {
	item.Owner = owner
	item.Lease = &validation.Lease{
		Owner:        owner,
		FencingToken: token,
		ExpiresAt:    now.Add(a.leaseDuration),
	}
}

// WorkItemIssueNumber returns the GitHub issue number for tests.
func (a *Adapter) WorkItemIssueNumber(id string) (int, bool) {
	var number int
	var ok bool
	a.coord.WithLock(func() {
		number, ok = a.idx.workItemIssue[id]
	})
	return number, ok
}

// RequestIssueNumber returns the GitHub issue number for tests.
func (a *Adapter) RequestIssueNumber(id string) (int, bool) {
	var number int
	var ok bool
	a.coord.WithLock(func() {
		number, ok = a.idx.requestIssue[id]
	})
	return number, ok
}

// LoadWorkItemForTest returns the durable work item for assertions.
func (a *Adapter) LoadWorkItemForTest(id string) (validation.WorkItem, bool) {
	var item validation.WorkItem
	var ok bool
	a.coord.WithLock(func() {
		loaded, err := a.tryLoadWorkItem(id)
		if err != nil {
			return
		}
		item = loaded
		ok = true
	})
	return item, ok
}
