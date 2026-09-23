package memory

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/redhat-et/protobot/wms/validation"
)

const (
	outcomeRead     = "read"
	outcomeApplied  = "applied"
	outcomeReplayed = "replayed"
	outcomeRejected = "rejected"

	mutationNone          = "none"
	mutationApplied       = "applied"
	mutationNotApplicable = "not-applicable"

	idempotencyNew      = "new"
	idempotencyReplayed = "replayed"
)

// Execute runs one operation against the in-memory WMS. Gate identity comes
// from actorContextRef; authorization claims are never accepted from call.
func (memory *Memory) Execute(call CallRequest) Result {
	memory.mu.Lock()
	defer memory.mu.Unlock()

	if !containsOperation(adapterOperations, call.Operation) {
		return rejectedResult(call.Operation, unauthorizedRejection(call.Operation, call.WorkItemID, ""))
	}
	authorization, ok := memory.gate.Resolve(call.ActorContextRef)
	if !ok {
		return rejectedResult(call.Operation, unauthorizedRejection(call.Operation, call.WorkItemID, ""))
	}
	if authorization.ProjectID == "" {
		// The in-memory adapter is already bound to this trusted project. The
		// golden Gate fixture stores that binding once at its base-state level.
		authorization.ProjectID = memory.projectID
	}
	if call.Operation == string(validation.OperationLifecyclePreflight) {
		return memory.executePreflightLocked(call, authorization)
	}
	if isLifecycleOperation(call.Operation) {
		return memory.executeLifecycleLocked(call, authorization)
	}
	if !roleHasWMSOperation(authorization.Role, call.Operation) {
		return rejectedResult(call.Operation, unauthorizedRejection(call.Operation, call.WorkItemID, authorization.PolicyVersion))
	}
	if rejection := validation.AuthorizeContext(
		authorization,
		validation.Operation(call.Operation),
		memory.projectID,
		call.WorkItemID,
		"",
		[]string{"project:" + memory.projectID},
		memory.now(),
	); rejection != nil {
		return rejectedResult(call.Operation, rejection)
	}
	if call.PolicyVersion != "" && call.PolicyVersion != authorization.PolicyVersion {
		return rejectedResult(call.Operation, unauthorizedRejection(call.Operation, call.WorkItemID, authorization.PolicyVersion))
	}
	if call.Operation == "finding.create" {
		return rejectedResult(call.Operation, wmsRejection(
			"WMS_UNAVAILABLE",
			"The in-memory Drafting Table adapter does not implement the Finding Ledger.",
			map[string]any{},
			"retry",
		))
	}
	return memory.executeRequestLocked(call, authorization)
}

func (memory *Memory) executePreflightLocked(call CallRequest, authorization validation.AuthorizationContext) Result {
	var payload struct {
		Operation      validation.Operation `json:"operation"`
		ResolutionKind string               `json:"resolution_kind"`
		ChangeSetID    string               `json:"change_set_id"`
	}
	if err := decodePayload(call.Payload, &payload); err != nil {
		return rejectedResult(call.Operation, invalidRequest("payload", err.Error()))
	}
	item, exists := memory.workItems[call.WorkItemID]
	var current *validation.WorkItem
	if exists {
		copy := cloneWorkItem(item)
		current = &copy
	}
	request := validation.Request{
		Operation:               payload.Operation,
		ProjectID:               memory.projectID,
		WorkItemID:              call.WorkItemID,
		ExpectedState:           call.ExpectedState,
		ExpectedContractVersion: call.ExpectedContractVersion,
		Payload: validation.Payload{
			ResolutionKind: payload.ResolutionKind,
			ChangeSetID:    payload.ChangeSetID,
		},
		Authorization: authorization,
	}
	preview := &validation.ResolutionPreview{
		Kind:        payload.ResolutionKind,
		ChangeSetID: payload.ChangeSetID,
	}
	if payload.ResolutionKind == "add-requirement" {
		preview.PlannedDependencyComplete = memory.plannedDependencyComplete(payload.ChangeSetID)
	}
	evaluation := validation.EvaluationContext{
		Authority:         validation.AuthorityPreflight,
		EvaluationTime:    memory.now(),
		PreviewResolution: preview,
	}
	decision := validation.Evaluate(request, current, evaluation)
	if call.PolicyVersion != "" && call.PolicyVersion != authorization.PolicyVersion &&
		validation.AuthorizePreflight(
			authorization,
			memory.projectID,
			call.WorkItemID,
			payload.ChangeSetID,
			[]string{"project:" + memory.projectID},
			evaluation.EvaluationTime,
		) == nil {
		decision = validation.RejectionDecision(request, validation.AuthorityPreflight,
			unauthorizedRejection(string(payload.Operation), call.WorkItemID, authorization.PolicyVersion))
	}
	return resultFromDecision(call.Operation, decision)
}

func (memory *Memory) executeLifecycleLocked(call CallRequest, authorization validation.AuthorizationContext) Result {
	request, rejection := memory.normalizeLifecycleRequest(call, authorization)
	if rejection != nil {
		return rejectedResult(call.Operation, rejection)
	}
	evaluation := validation.EvaluationContext{
		Authority:             validation.AuthorityAuthoritative,
		EvaluationTime:        memory.now(),
		LeaseDuration:         memory.leaseDuration,
		NextFencingToken:      fmt.Sprintf("fence-%d", memory.nextFencingToken+1),
		Approvals:             memory.approvalSnapshotLocked(),
		ResolutionSubmissions: memory.resolutionSnapshotLocked(),
	}
	if validation.ValidateAuthorizationContext(
		request.Authorization,
		request.Operation,
		request.ProjectID,
		request.WorkItemID,
		request.Payload.ChangeSetID,
		evaluation.EvaluationTime,
	) != nil {
		return resultFromDecision(call.Operation, validation.Evaluate(request, nil, evaluation))
	}
	var current *validation.WorkItem
	if request.Operation != validation.OperationMaterialize {
		item, exists := memory.workItems[request.WorkItemID]
		if !exists {
			decision := validation.Evaluate(request, nil, evaluation)
			result := resultFromDecision(call.Operation, decision)
			if request.IdempotencyKey != "" {
				memory.rememberIdempotencyLocked(request.IdempotencyKey, validation.RequestFingerprint(request), result)
			}
			return result
		}
		copy := cloneWorkItem(item)
		copy.ActiveResolutionSubmissionID = memory.activeSubmissions[request.WorkItemID]
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
	if result, handled := memory.replayLifecycleRequest(call, request, fingerprint); handled {
		return result
	}
	return memory.applyLifecycleRequest(call.Operation, request, current, authorization, fingerprint, evaluation)
}

func (memory *Memory) normalizeLifecycleRequest(call CallRequest, authorization validation.AuthorizationContext) (validation.Request, *validation.Rejection) {
	var payload validation.Payload
	if err := decodePayload(call.Payload, &payload); err != nil {
		return validation.Request{}, invalidRequest("payload", err.Error())
	}
	workItemID := call.WorkItemID
	if call.Operation == string(validation.OperationMaterialize) && workItemID == "" && payload.WorkItem != nil {
		workItemID = payload.WorkItem.ID
	}
	if payload.ResolutionSubmissionID != "" && payload.ChangeSetID == "" {
		if submission, exists := memory.submissions[payload.ResolutionSubmissionID]; exists {
			payload.ChangeSetID = submission.ChangeSetID
		}
	}
	if call.HumanApprovalID != "" {
		payload.HumanApprovalID = call.HumanApprovalID
	}
	request := validation.Request{
		Operation:               validation.Operation(call.Operation),
		ProjectID:               memory.projectID,
		WorkItemID:              workItemID,
		MaterializationKey:      requestMaterializationKey(call, payload),
		IdempotencyKey:          call.IdempotencyKey,
		ExpectedState:           call.ExpectedState,
		ExpectedContractVersion: call.ExpectedContractVersion,
		FencingToken:            callFencingToken(payload, call),
		Payload:                 payload,
		Authorization:           authorization,
	}
	return request, nil
}

func (memory *Memory) replayLifecycleRequest(call CallRequest, request validation.Request, fingerprint string) (Result, bool) {
	if replay, conflict := memory.checkIdempotencyLocked(call.Operation, request.IdempotencyKey, fingerprint); replay != nil {
		return *replay, true
	} else if conflict != nil {
		decision := validation.RejectionDecision(request, validation.AuthorityAuthoritative, conflict)
		return resultFromDecision(call.Operation, decision), true
	}
	if call.Operation == string(validation.OperationMaterialize) {
		if replay, conflict := memory.checkMaterializationLocked(request); replay != nil {
			memory.rememberIdempotencyLocked(request.IdempotencyKey, fingerprint, *replay)
			return *replay, true
		} else if conflict != nil {
			decision := validation.RejectionDecision(request, validation.AuthorityAuthoritative, conflict)
			result := resultFromDecision(call.Operation, decision)
			memory.rememberIdempotencyLocked(request.IdempotencyKey, fingerprint, result)
			return result, true
		}
	}
	return Result{}, false
}

func (memory *Memory) applyLifecycleRequest(
	operation string,
	request validation.Request,
	current *validation.WorkItem,
	authorization validation.AuthorizationContext,
	fingerprint string,
	evaluation validation.EvaluationContext,
) Result {
	decision := validation.Evaluate(request, current, evaluation)
	result := resultFromDecision(operation, decision)
	if decision.Outcome == validation.OutcomeAllowed {
		if request.Operation == validation.OperationMaterialize {
			item := cloneWorkItem(*request.Payload.WorkItem)
			item.State = decision.After.State
			item.ContractVersion = decision.After.ContractVersion
			item.MaterializationKey = request.MaterializationKey
			item.ChangeType = request.Payload.WorkItem.ChangeType
			if item.ChangeType == "" {
				item.ChangeType = request.Payload.ChangeType
			}
			item.Owner = ""
			item.Lease = nil
			memory.workItems[item.ID] = item
			result.WorkItemID = item.ID
			result.WorkItemState = item.State
			result.ContractVersion = item.ContractVersion
			result.Resource = cloneWorkItem(item)
		} else {
			updated := cloneWorkItem(*current)
			memory.applyLifecycleMutationLocked(&updated, request, authorization, decision, evaluation)
			memory.workItems[updated.ID] = updated
			result.WorkItemID = updated.ID
			result.WorkItemState = updated.State
			result.ContractVersion = updated.ContractVersion
		}
		memory.nextFencingToken += boolToUint64(decision.FencingTokenIssued != "")
		memory.events = append(memory.events, AuditEvent{
			Operation:              string(request.Operation),
			Subject:                authorization.Subject,
			AuthorizedHumanSubject: memory.approvalSubject(request.Payload.HumanApprovalID, request.Operation),
			WorkItemID:             request.WorkItemID,
			RuleVersion:            decision.RuleVersion,
			PolicyVersion:          decision.PolicyVersion,
			Before:                 cloneStateVersion(decision.Before),
			After:                  cloneStateVersion(decision.After),
		})
	} else if decision.Outcome == validation.OutcomeOmitted {
		result.Submission = "omitted"
		memory.events = append(memory.events, AuditEvent{
			Operation:              string(request.Operation),
			Subject:                authorization.Subject,
			AuthorizedHumanSubject: memory.approvalSubject(request.Payload.HumanApprovalID, request.Operation),
			RuleVersion:            decision.RuleVersion,
			PolicyVersion:          decision.PolicyVersion,
			Before:                 cloneStateVersion(decision.Before),
		})
	}
	if request.Operation == validation.OperationResolveBlock && decision.Outcome == validation.OutcomeAllowed {
		memory.consumeResolutionApprovalLocked(request)
		result.ApprovalStatus = "consumed"
	} else if request.Operation == validation.OperationResolveBlock && request.Payload.HumanApprovalID != "" {
		if approval, exists := memory.approvals[request.Payload.HumanApprovalID]; exists {
			result.ApprovalStatus = approval.Status
		}
	}
	if request.Operation == validation.OperationMaterialize && decision.Outcome != validation.OutcomeRejected {
		memory.materializations[request.MaterializationKey] = materializationEntry{
			fingerprint: decision.MaterializationReservation.SourceFingerprint,
			result:      cloneResult(result),
		}
	}
	memory.rememberIdempotencyLocked(request.IdempotencyKey, fingerprint, result)
	return result
}

func isLifecycleOperation(operation string) bool {
	switch validation.Operation(operation) {
	case validation.OperationMaterialize, validation.OperationRefreshDependencies,
		validation.OperationRevalidate, validation.OperationResolveBlock,
		validation.OperationClaim, validation.OperationRenewLease,
		validation.OperationTestsPass, validation.OperationRaiseSpecQuestion,
		validation.OperationRefreshActive, validation.OperationReturnToBuilding,
		validation.OperationBeginMerge, validation.OperationMergeConflict,
		validation.OperationMergeNotApplied, validation.OperationRecordMerge,
		validation.OperationRecoverLease, validation.OperationAbandon:
		return true
	default:
		return false
	}
}

func requestMaterializationKey(call CallRequest, payload validation.Payload) string {
	if call.MaterializationKey != "" {
		return call.MaterializationKey
	}
	if payload.MaterializationKey != "" {
		return payload.MaterializationKey
	}
	var envelope struct {
		MaterializationKey string `json:"materialization_key"`
	}
	if len(call.Payload) > 0 {
		_ = json.Unmarshal(call.Payload, &envelope)
	}
	if envelope.MaterializationKey != "" {
		return envelope.MaterializationKey
	}
	if payload.WorkItem != nil {
		return payload.WorkItem.MaterializationKey
	}
	return ""
}

func callFencingToken(_ validation.Payload, call CallRequest) string {
	return call.FencingToken
}

func (memory *Memory) plannedDependencyComplete(changeSetID string) bool {
	changeSet, exists := memory.changeSets[changeSetID]
	if !exists || changeSet.BuildWorkItemID == "" {
		return false
	}
	workItem, exists := memory.workItems[changeSet.BuildWorkItemID]
	return exists && workItem.State == validation.StateCompleted
}

func decodePayload(data []byte, target any) error {
	if len(data) == 0 {
		data = []byte("{}")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("payload contains more than one JSON value")
		}
		return err
	}
	return nil
}

func newResult(operation string) Result {
	return Result{
		OK:          true,
		Operation:   operation,
		Outcome:     outcomeRead,
		Diagnostics: []Diagnostic{},
		Mutation:    mutationNone,
	}
}

func rejectedResult(operation string, rejection *validation.Rejection) Result {
	return Result{
		OK:          false,
		Operation:   operation,
		Outcome:     outcomeRejected,
		Diagnostics: []Diagnostic{},
		Mutation:    mutationNone,
		Error:       rejection,
	}
}

func resultFromDecision(operation string, decision validation.Decision) Result {
	result := newResult(operation)
	result.Decision = &decision
	if decision.Authority == validation.AuthorityPreflight {
		return result
	}
	switch decision.Outcome {
	case validation.OutcomeRejected:
		result.OK = false
		result.Outcome = outcomeRejected
		result.Error = decision.Rejection
	case validation.OutcomeOmitted:
		result.Outcome = outcomeApplied
		result.Mutation = mutationApplied
		result.Idempotency = idempotencyNew
	default:
		result.Outcome = outcomeApplied
		result.Mutation = mutationApplied
		result.Idempotency = idempotencyNew
	}
	return result
}

func (memory *Memory) approvalSubject(approvalID string, operation validation.Operation) string {
	switch operation {
	case validation.OperationResolveBlock, "blocked-work.submit-resolution", "blocked-work.acknowledge", "request.refine":
		if approval, exists := memory.approvals[approvalID]; exists {
			return approval.ApprovedSubject
		}
	}
	return ""
}

func invalidRequest(field, message string) *validation.Rejection {
	return wmsRejection(
		"INVALID_REQUEST",
		"The request has a field that is missing or invalid.",
		map[string]any{"field": field, "reason": message},
		"revise",
	)
}

func wmsRejection(code, message string, details map[string]any, retry string) *validation.Rejection {
	return &validation.Rejection{Code: code, Message: message, Details: details, Retry: retry}
}

func unauthorizedRejection(operation, workItemID, policyVersion string) *validation.Rejection {
	targetType := "project"
	if workItemID != "" {
		targetType = "work-item"
	}
	return wmsRejection(
		validation.CodeUnauthorizedAction,
		"The trusted authorization context does not permit this action or target.",
		map[string]any{
			"required_action": operation,
			"target_type":     targetType,
			"policy_version":  policyVersion,
		},
		validation.RetryAuthorize,
	)
}

func validationNotFound(targetType string) *validation.Rejection {
	return wmsRejection(
		validation.CodeNotFound,
		"The target is not visible in the trusted project context.",
		map[string]any{"target_type": targetType, "visibility_scope": "project"},
		validation.RetryRefresh,
	)
}

func (memory *Memory) idempotencyScope(key string) string {
	return memory.projectID + "\x00" + key
}

func (memory *Memory) checkIdempotencyLocked(operation, key, fingerprint string) (*Result, *validation.Rejection) {
	entry, exists := memory.idempotency[memory.idempotencyScope(key)]
	if !exists {
		return nil, nil
	}
	if entry.fingerprint != fingerprint {
		return nil, wmsRejection(
			validation.CodeIdempotencyConflict,
			"The idempotency key was reused with a different request.",
			map[string]any{
				"conflict_kind":               "request-fingerprint",
				"key_scope":                   "project",
				"request_fingerprint_digest":  fingerprint,
				"recorded_fingerprint_digest": entry.fingerprint,
			},
			validation.RetryNewKey,
		)
	}
	result := cloneResult(entry.result)
	result.Outcome = outcomeReplayed
	result.Mutation = mutationNone
	result.Idempotency = idempotencyReplayed
	if result.Decision != nil {
		result.Decision.Replayed = true
	}
	return &result, nil
}

func (memory *Memory) checkMaterializationLocked(request validation.Request) (*Result, *validation.Rejection) {
	if request.Payload.WorkItem == nil {
		return nil, nil
	}
	entry, exists := memory.materializations[request.MaterializationKey]
	if !exists {
		return nil, nil
	}
	fingerprint := validation.SourceFingerprint(*request.Payload.WorkItem)
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
	result := cloneResult(entry.result)
	result.Outcome = outcomeReplayed
	result.Mutation = mutationNone
	result.Idempotency = idempotencyReplayed
	if result.Decision != nil {
		result.Decision.Replayed = true
	}
	return &result, nil
}

func (memory *Memory) rememberIdempotencyLocked(key, fingerprint string, result Result) {
	memory.idempotency[memory.idempotencyScope(key)] = idempotencyEntry{
		fingerprint: fingerprint,
		result:      cloneResult(result),
	}
}

func (memory *Memory) approvalSnapshotLocked() map[string]validation.ApprovalRecord {
	result := make(map[string]validation.ApprovalRecord, len(memory.approvals))
	for id, approval := range memory.approvals {
		result[id] = cloneApproval(approval)
	}
	return result
}

func (memory *Memory) resolutionSnapshotLocked() map[string]validation.ResolutionSubmission {
	result := make(map[string]validation.ResolutionSubmission, len(memory.submissions))
	for id, submission := range memory.submissions {
		result[id] = submission.ResolutionSubmission
	}
	return result
}

func (memory *Memory) consumeResolutionApprovalLocked(request validation.Request) {
	approval := memory.approvals[request.Payload.HumanApprovalID]
	approval.ID = request.Payload.HumanApprovalID
	approval.Status = "consumed"
	memory.approvals[approval.ID] = approval
	submission := memory.submissions[request.Payload.ResolutionSubmissionID]
	submission.Status = "consumed"
	memory.submissions[submission.ID] = submission
	memory.activeSubmissions[request.WorkItemID] = ""
}

func cloneWorkItem(item validation.WorkItem) validation.WorkItem {
	item.Dependencies = append([]validation.Dependency(nil), item.Dependencies...)
	item.Readiness.UnresolvedReasons = append([]string(nil), item.Readiness.UnresolvedReasons...)
	if item.Lease != nil {
		lease := *item.Lease
		item.Lease = &lease
	}
	item.ExpectedMerge = cloneMergeEnvelope(item.ExpectedMerge)
	item.Reconciliation.MergeEnvelope = cloneMergeEnvelope(item.Reconciliation.MergeEnvelope)
	return item
}

func cloneMergeEnvelope(envelope *validation.MergeEnvelope) *validation.MergeEnvelope {
	if envelope == nil {
		return nil
	}
	copy := *envelope
	return &copy
}

func cloneApproval(approval validation.ApprovalRecord) validation.ApprovalRecord {
	if approval.ExpectedContractVersion != nil {
		version := *approval.ExpectedContractVersion
		approval.ExpectedContractVersion = &version
	}
	return approval
}

func cloneRequest(request RequestRecord) RequestRecord {
	request.AffectedInterfaces = append([]string(nil), request.AffectedInterfaces...)
	request.AffectedScopes = append([]string(nil), request.AffectedScopes...)
	request.Relationships = append([]RequestRelationship(nil), request.Relationships...)
	return request
}

func cloneSubmission(submission Submission) Submission {
	return submission
}

func cloneResult(result Result) Result {
	encoded, err := json.Marshal(result)
	if err != nil {
		panic(err)
	}
	var copy Result
	if err := json.Unmarshal(encoded, &copy); err != nil {
		panic(err)
	}
	return copy
}

func cloneStateVersion(version *validation.StateVersion) *validation.StateVersion {
	if version == nil {
		return nil
	}
	copy := *version
	return &copy
}

func boolToUint64(value bool) uint64 {
	if value {
		return 1
	}
	return 0
}

func (memory *Memory) nextRequestIDLocked() string {
	memory.nextRequestID++
	return fmt.Sprintf("request-%03d", memory.nextRequestID)
}

func (memory *Memory) nextSubmissionIDLocked(acknowledgement bool) string {
	prefix := "resolution-submission"
	if acknowledgement {
		prefix = "acknowledgement"
		memory.nextAcknowledgementID++
		return fmt.Sprintf("%s-%03d", prefix, memory.nextAcknowledgementID)
	}
	memory.nextResolutionSubmissionID++
	return fmt.Sprintf("%s-%03d", prefix, memory.nextResolutionSubmissionID)
}

func isBlank(value string) bool {
	return strings.TrimSpace(value) == ""
}
