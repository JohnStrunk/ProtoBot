package github

import (
	"fmt"

	"github.com/redhat-et/protobot/wms/adapter"
	"github.com/redhat-et/protobot/wms/validation"
)

type scannedDocument struct {
	number int
	doc    storedDocument
}

// listStoredDocuments scans the issues labeled label for documents of kind.
// It fails closed: a labeled issue that cannot be decoded, or whose kind does
// not match its label, makes the scan inconclusive and is returned as an
// error rather than skipped, so no caller can treat a durable record as
// absent (the indexed GetIssue loads in queries.go fail the same way). Only
// an issue belonging to a different project is skipped: that is legitimate
// multi-project filtering, not a scan failure.
func (a *Adapter) listStoredDocuments(label, kind string) ([]scannedDocument, error) {
	issues, err := a.client.ListIssues(a.ctx, ListIssuesFilter{Labels: []string{label}})
	if err != nil {
		return nil, err
	}
	out := make([]scannedDocument, 0, len(issues))
	for _, issue := range issues {
		doc, err := decodeBody(issue.Body)
		if err != nil {
			return nil, fmt.Errorf("issue #%d: undecodable document: %w", issue.Number, err)
		}
		if doc.Kind != kind {
			return nil, fmt.Errorf("issue #%d: document kind %q does not match label %q", issue.Number, doc.Kind, label)
		}
		if doc.ProjectID != a.projectID {
			continue
		}
		out = append(out, scannedDocument{number: issue.Number, doc: doc})
	}
	return out, nil
}

// materializationBinding is the durable record a materialization_key is
// bound to: a materialized work item (item != nil) or an omitted reservation
// (item == nil with Omitted set), which reserves the key for a source that
// did not require implementation (VR-039).
type materializationBinding struct {
	item   *validation.WorkItem
	doc    storedDocument
	number int
}

// findMaterializationBinding scans labeled work-item issues for the durable
// materialization_key binding: a materialized work item or an omitted
// reservation. Used by loadMaterializationEntryLocked and the
// UNKNOWN_MUTATION reconcilers when in-process indexes have no entry.
func (a *Adapter) findMaterializationBinding(key string) (materializationBinding, error) {
	if key == "" {
		return materializationBinding{}, ErrNotFound
	}
	docs, err := a.listStoredDocuments(LabelWorkItem, kindWorkItem)
	if err != nil {
		return materializationBinding{}, err
	}
	for _, scanned := range docs {
		if scanned.doc.MaterializationKey != key {
			continue
		}
		if scanned.doc.WorkItem != nil {
			item := cloneWorkItem(*scanned.doc.WorkItem)
			return materializationBinding{item: &item, doc: scanned.doc, number: scanned.number}, nil
		}
		if scanned.doc.Omitted {
			return materializationBinding{doc: scanned.doc, number: scanned.number}, nil
		}
		// A work-item document that claims this key but carries neither a
		// work item nor the omitted flag is malformed: fail closed instead
		// of treating the key as free.
		return materializationBinding{}, fmt.Errorf("issue #%d: work-item document has no work item and is not an omitted reservation", scanned.number)
	}
	return materializationBinding{}, ErrNotFound
}

// findWorkItemByID scans labeled work-item issues for a durable work-item id.
func (a *Adapter) findWorkItemByID(id string) (validation.WorkItem, storedDocument, int, error) {
	if id == "" {
		return validation.WorkItem{}, storedDocument{}, 0, ErrNotFound
	}
	docs, err := a.listStoredDocuments(LabelWorkItem, kindWorkItem)
	if err != nil {
		return validation.WorkItem{}, storedDocument{}, 0, err
	}
	for _, scanned := range docs {
		if scanned.doc.WorkItem == nil {
			continue
		}
		if scanned.doc.WorkItem.ID != id {
			continue
		}
		return cloneWorkItem(*scanned.doc.WorkItem), scanned.doc, scanned.number, nil
	}
	return validation.WorkItem{}, storedDocument{}, 0, ErrNotFound
}

// findRequestByID scans labeled request issues for a durable request_id.
func (a *Adapter) findRequestByID(id string) (adapter.RequestRecord, int, error) {
	if id == "" {
		return adapter.RequestRecord{}, 0, ErrNotFound
	}
	docs, err := a.listStoredDocuments(LabelRequest, kindRequest)
	if err != nil {
		return adapter.RequestRecord{}, 0, err
	}
	for _, scanned := range docs {
		if scanned.doc.Request == nil {
			continue
		}
		if scanned.doc.Request.ID != id {
			continue
		}
		return cloneRequest(*scanned.doc.Request), scanned.number, nil
	}
	return adapter.RequestRecord{}, 0, ErrNotFound
}

func storedSourceFingerprint(doc storedDocument) string {
	if doc.SourceFingerprint != "" {
		return doc.SourceFingerprint
	}
	if doc.WorkItem == nil {
		return ""
	}
	source := validation.CanonicalMaterializationSource(*doc.WorkItem, doc.WorkItem.ChangeType)
	return validation.SourceFingerprint(source)
}

func (a *Adapter) bindWorkItemLocked(item validation.WorkItem, doc storedDocument, number int, result adapter.Result) {
	a.idx.workItemIssue[item.ID] = number
	if doc.MaterializationKey == "" {
		return
	}
	a.idx.materializationKey[doc.MaterializationKey] = materializationEntry{
		fingerprint: storedSourceFingerprint(doc),
		result:      cloneResult(result),
		issueNumber: number,
	}
}

func (a *Adapter) bindRequestLocked(request adapter.RequestRecord, number int, semanticKey string) {
	a.idx.requestIssue[request.ID] = number
	if semanticKey != "" {
		a.idx.semanticRequests[semanticKey] = request.ID
	}
}

func materializationReplayResult(item validation.WorkItem) adapter.Result {
	result := newResult(string(validation.OperationMaterialize))
	result.Outcome = adapter.OutcomeApplied
	result.Mutation = adapter.MutationApplied
	result.Idempotency = adapter.IdempotencyNew
	result.WorkItemID = item.ID
	result.WorkItemState = item.State
	result.ContractVersion = item.ContractVersion
	result.Resource = cloneWorkItem(item)
	return markReplayed(result)
}

// omittedReplayResult is the envelope rebuilt from a durable omitted
// reservation: the applied omitted result checkMaterializationLocked replays
// (markReplayed) for a later same-key materialize (VR-039).
func omittedReplayResult() adapter.Result {
	result := newResult(string(validation.OperationMaterialize))
	result.Outcome = adapter.OutcomeApplied
	result.Mutation = adapter.MutationApplied
	result.Idempotency = adapter.IdempotencyNew
	result.Submission = "omitted"
	return result
}

// materializationEntryForBinding builds the in-process cache entry for a
// durable binding. The entry must carry the replay envelope (same as
// bindWorkItemLocked), not just the fingerprint: checkMaterializationLocked
// replays entry.result, so a zero Result would freeze an empty rejection
// under the caller's idempotency key on the next materialize.
func materializationEntryForBinding(binding materializationBinding) materializationEntry {
	entry := materializationEntry{
		fingerprint: storedSourceFingerprint(binding.doc),
		issueNumber: binding.number,
	}
	if binding.item != nil {
		entry.result = materializationReplayResult(*binding.item)
	} else {
		entry.result = omittedReplayResult()
	}
	return entry
}

func requestCreateAppliedResult(operation string, request adapter.RequestRecord) adapter.Result {
	result := newResult(operation)
	result.Outcome = adapter.OutcomeApplied
	result.Mutation = adapter.MutationApplied
	result.Idempotency = adapter.IdempotencyNew
	result.Resource = cloneRequest(request)
	result.RequestID = request.ID
	result.RequestRevision = request.Revision
	return result
}

// hydrateRequestsLocked rebuilds request indexes from durable request issues so
// a restarted adapter neither re-issues an existing request ID nor misses an
// existing semantic key. It returns the scanned request documents.
func (a *Adapter) hydrateRequestsLocked() ([]scannedDocument, error) {
	docs, err := a.listStoredDocuments(LabelRequest, kindRequest)
	if err != nil {
		return nil, err
	}
	for _, scanned := range docs {
		request := scanned.doc.Request
		if scanned.doc.Kind != kindRequest || request == nil {
			continue
		}
		if _, ok := a.idx.requestIssue[request.ID]; !ok {
			a.idx.requestIssue[request.ID] = scanned.number
		}
		semanticKey := requestSemanticKeyFor(request.Intent, request.AffectedInterfaces, request.AffectedScopes)
		if _, ok := a.idx.semanticRequests[semanticKey]; !ok {
			a.idx.semanticRequests[semanticKey] = request.ID
		}
		var seq uint64
		if _, err := fmt.Sscanf(request.ID, "REQ-%d", &seq); err == nil && seq > a.idx.nextRequestSeq {
			a.idx.nextRequestSeq = seq
		}
	}
	return docs, nil
}

// hydrateWorkItemsLocked rebuilds work-item indexes from durable work-item
// issues so a restarted adapter sees every durable work item instead of an
// empty in-process map. It returns an error for any scan failure: only a
// completed scan establishes the durable set, and a failed scan must not be
// served as an empty result.
func (a *Adapter) hydrateWorkItemsLocked() error {
	docs, err := a.listStoredDocuments(LabelWorkItem, kindWorkItem)
	if err != nil {
		return err
	}
	for _, scanned := range docs {
		item := scanned.doc.WorkItem
		if item == nil {
			// An omitted reservation carries no work item but still binds
			// its materialization_key: VR-039's replay/conflict decision
			// must survive a restart.
			if scanned.doc.Omitted && scanned.doc.MaterializationKey != "" {
				if _, ok := a.idx.materializationKey[scanned.doc.MaterializationKey]; !ok {
					a.idx.materializationKey[scanned.doc.MaterializationKey] = materializationEntryForBinding(
						materializationBinding{doc: scanned.doc, number: scanned.number},
					)
				}
			}
			continue
		}
		if _, ok := a.idx.workItemIssue[item.ID]; !ok {
			a.idx.workItemIssue[item.ID] = scanned.number
		}
		if scanned.doc.MaterializationKey != "" {
			if _, ok := a.idx.materializationKey[scanned.doc.MaterializationKey]; !ok {
				a.idx.materializationKey[scanned.doc.MaterializationKey] = materializationEntryForBinding(
					materializationBinding{item: item, doc: scanned.doc, number: scanned.number},
				)
			}
		}
	}
	return nil
}

// ensureRequestsHydratedLocked runs the one-time durable request scan a cold
// adapter needs before serving a request operation. After the first success
// the in-process indexes are trusted (they are updated on every write), so
// repeated operations do not rescan. A scan failure leaves the adapter
// unhydrated so a later call can retry.
func (a *Adapter) ensureRequestsHydratedLocked() error {
	if a.idx.requestsHydrated {
		return nil
	}
	if _, err := a.hydrateRequestsLocked(); err != nil {
		return err
	}
	a.idx.requestsHydrated = true
	return nil
}

// ensureWorkItemsHydratedLocked is the work-item analogue of
// ensureRequestsHydratedLocked.
func (a *Adapter) ensureWorkItemsHydratedLocked() error {
	if a.idx.workItemsHydrated {
		return nil
	}
	if err := a.hydrateWorkItemsLocked(); err != nil {
		return err
	}
	a.idx.workItemsHydrated = true
	return nil
}

// replayDurableRequestCreate replays or rejects a request.create whose
// idempotency key is already bound to a durable request issue.
func (a *Adapter) replayDurableRequestCreate(
	call adapter.CallRequest,
	authorization validation.AuthorizationContext,
	docs []scannedDocument,
) (adapter.Result, bool) {
	for _, scanned := range docs {
		doc := scanned.doc
		if doc.Kind != kindRequest || doc.Request == nil || doc.CreateIdempotencyKey != call.IdempotencyKey {
			continue
		}
		fingerprint := requestFingerprint(call, authorization)
		if doc.CreateFingerprint != fingerprint {
			return rejectedResult(call.Operation, wmsRejection(
				validation.CodeIdempotencyConflict,
				"The idempotency key was reused with a different request.",
				map[string]any{
					"conflict_kind":               "request-fingerprint",
					"key_scope":                   "project",
					"request_fingerprint_digest":  fingerprint,
					"recorded_fingerprint_digest": doc.CreateFingerprint,
				},
				validation.RetryNewKey,
			)), true
		}
		delete(a.idx.pendingRequestCreate, call.IdempotencyKey)
		return markReplayed(requestCreateAppliedResult(call.Operation, cloneRequest(*doc.Request))), true
	}
	return adapter.Result{}, false
}
