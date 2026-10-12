package github

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/redhat-et/protobot/wms/adapter"
	"github.com/redhat-et/protobot/wms/validation"
)

// TestRequestVocabularyMatchesContract pins validClassification and
// validRefinementState to the exact request vocabulary defined by
// docs/architecture/drafting-table-wms.md:171-172, matching the memory
// adapter (wms/memory/requests.go) so both backends accept and reject the
// same values. The GitHub adapter previously accepted an invented alias
// ("clarification", "ready", "linked") and rejected the canonical
// "ready-for-dimensioning" and "closed" values.
func TestRequestVocabularyMatchesContract(t *testing.T) {
	t.Parallel()
	classificationCases := map[string]bool{
		"undefined":     true,
		"changes":       true,
		"contradicts":   true,
		"clarification": false,
		"":              false,
	}
	for value, want := range classificationCases {
		if got := validClassification(value); got != want {
			t.Errorf("validClassification(%q) = %v, want %v", value, got, want)
		}
	}
	refinementCases := map[string]bool{
		"unrefined":              true,
		"refining":               true,
		"ready-for-dimensioning": true,
		"closed":                 true,
		"ready":                  false,
		"linked":                 false,
		"":                       false,
	}
	for value, want := range refinementCases {
		if got := validRefinementState(value); got != want {
			t.Errorf("validRefinementState(%q) = %v, want %v", value, got, want)
		}
	}
}

// TestRefineRequestRejectsLegacyRefinementStateAlias is an end-to-end check
// that the handler itself (not just the standalone validator) enforces the
// contract vocabulary.
func TestRefineRequestRejectsLegacyRefinementStateAlias(t *testing.T) {
	fake := NewFakeClient()
	wms := newTestAdapter(t, fake)
	request := createGithubTestRequest(t, wms, "Legacy Alias Request", "exercise vocabulary rejection")

	result := wms.Execute(refineGithubCall(request.ID, request.Revision, "refine-legacy-alias", refineRequestPayload{
		Classification:  "undefined",
		RefinementState: "ready",
	}))
	if result.OK || result.Error == nil || result.Error.Code != adapter.CodeInvalidRequest {
		t.Fatalf("refine with legacy refinement_state alias = %#v, want INVALID_REQUEST", result)
	}
}

// TestRefineRequestRejectsBlankSuppliedIntentOrRationale mirrors the memory
// adapter's contract (wms/memory/requests.go): a supplied (non-omitted)
// intent or rationale must not be whitespace-only.
func TestRefineRequestRejectsBlankSuppliedIntentOrRationale(t *testing.T) {
	for _, field := range []string{"intent", "rationale"} {
		t.Run(field, func(t *testing.T) {
			fake := NewFakeClient()
			wms := newTestAdapter(t, fake)
			request := createGithubTestRequest(t, wms, "Keep this request", "Keep this rationale")

			payload := refineRequestPayload{
				Classification:  "changes",
				RefinementState: "ready-for-dimensioning",
			}
			if field == "intent" {
				payload.Intent = "   "
			} else {
				payload.Rationale = "   "
			}
			result := wms.Execute(refineGithubCall(request.ID, request.Revision, "blank-refine-"+field, payload))
			if result.OK || result.Error == nil || result.Error.Code != adapter.CodeInvalidRequest || result.Mutation != adapter.MutationNone {
				t.Fatalf("blank %s refinement = %#v, want mutation-free INVALID_REQUEST", field, result)
			}
			unchanged, _, _, err := wms.loadRequest(request.ID)
			if err != nil || unchanged.Revision != request.Revision {
				t.Fatalf("request mutated by rejected blank refinement: %#v, err=%v", unchanged, err)
			}
		})
	}
}

// TestRefineRequestRejectsInvalidRelationships mirrors the memory adapter's
// relationship contract (wms/memory/requests.go): type, target existence,
// self-reference, and duplicates must all be rejected. Previously the
// GitHub adapter copied relationships without validating any of this.
func TestRefineRequestRejectsInvalidRelationships(t *testing.T) {
	fake := NewFakeClient()
	wms := newTestAdapter(t, fake)
	target := createGithubTestRequest(t, wms, "Relationship Target", "exists for relationship checks")

	cases := []struct {
		name          string
		relationships func(selfID string) []adapter.RequestRelationship
	}{
		{
			name: "invalid-type",
			relationships: func(string) []adapter.RequestRelationship {
				return []adapter.RequestRelationship{{Type: "blocks", Target: target.ID}}
			},
		},
		{
			name: "missing-target",
			relationships: func(string) []adapter.RequestRelationship {
				return []adapter.RequestRelationship{{Type: "depends-on", Target: "REQ-missing"}}
			},
		},
		{
			name: "self-reference",
			relationships: func(selfID string) []adapter.RequestRelationship {
				return []adapter.RequestRelationship{{Type: "depends-on", Target: selfID}}
			},
		},
		{
			name: "duplicate",
			relationships: func(string) []adapter.RequestRelationship {
				return []adapter.RequestRelationship{
					{Type: "depends-on", Target: target.ID},
					{Type: "depends-on", Target: target.ID},
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := createGithubTestRequest(t, wms, "Relationship Subject "+tc.name, "exercises relationship validation")
			approval := seedGithubApprovalForRequest(t, wms, "relationship-approval-"+tc.name, request.ID, "placeholder-digest")
			result := wms.Execute(refineGithubCall(request.ID, request.Revision, "refine-relationship-"+tc.name, refineRequestPayload{
				Classification:           "changes",
				RefinementState:          "ready-for-dimensioning",
				Relationships:            tc.relationships(request.ID),
				HumanApprovalID:          approval.ID,
				ApprovalRefinementDigest: approval.Digest,
			}))
			if result.OK {
				t.Fatalf("refine with invalid relationship (%s) = %#v, want rejection", tc.name, result)
			}
			unchanged, _, _, err := wms.loadRequest(request.ID)
			if err != nil || unchanged.Revision != request.Revision {
				t.Fatalf("request mutated by rejected relationship (%s): %#v, err=%v", tc.name, unchanged, err)
			}
		})
	}
}

// TestLinkChangeSetRejectsDuplicateAndNonMutableRevision mirrors the memory
// adapter's link contract (wms/memory/requests.go:377-381) and
// docs/architecture/drafting-table-wms.md:175 ("Optional link to a proposed
// or approved change set."). Previously linkChangeSetLocked only checked
// change-set existence and an optional target_revision match, so relinking
// an already-linked change set, or linking one whose revision is neither
// "proposed" nor "approved", applied and bumped request_revision.
func TestLinkChangeSetRejectsDuplicateAndNonMutableRevision(t *testing.T) {
	fake := NewFakeClient()
	wms := newTestAdapter(t, fake)

	t.Run("duplicate", func(t *testing.T) {
		request := createGithubTestRequest(t, wms, "Link Duplicate Request", "second link under the same change set is rejected")
		changeSet := adapter.ChangeSet{ID: "CS-link-duplicate", Revision: "proposed"}
		if err := wms.SeedChangeSet(changeSet); err != nil {
			t.Fatal(err)
		}
		revision := request.Revision
		first := wms.Execute(adapter.CallRequest{
			Operation:               string(validation.OperationRequestLinkChangeSet),
			ActorContextRef:         "drafting-table",
			RequestID:               request.ID,
			ExpectedRequestRevision: &revision,
			IdempotencyKey:          "link-dup-1",
			Payload:                 jsonPayload(t, map[string]any{"change_set_id": "CS-link-duplicate"}),
		})
		if !first.OK {
			t.Fatalf("first link = %#v, want applied", first)
		}
		linked := first.Resource.(adapter.RequestRecord)
		second := wms.Execute(adapter.CallRequest{
			Operation:               string(validation.OperationRequestLinkChangeSet),
			ActorContextRef:         "drafting-table",
			RequestID:               request.ID,
			ExpectedRequestRevision: &linked.Revision,
			IdempotencyKey:          "link-dup-2",
			Payload:                 jsonPayload(t, map[string]any{"change_set_id": "CS-link-duplicate"}),
		})
		if second.OK || second.Error == nil || second.Error.Code != adapter.CodeInvalidRequest {
			t.Fatalf("duplicate link = %#v, want INVALID_REQUEST", second)
		}
		unchanged, _, _, err := wms.loadRequest(request.ID)
		if err != nil || unchanged.Revision != linked.Revision {
			t.Fatalf("request mutated by rejected duplicate link: %#v, err=%v", unchanged, err)
		}
	})

	t.Run("non-mutable-revision", func(t *testing.T) {
		request := createGithubTestRequest(t, wms, "Link Non-Mutable Request", "a merged change set cannot be linked")
		changeSet := adapter.ChangeSet{ID: "CS-link-merged", Revision: "merged"}
		if err := wms.SeedChangeSet(changeSet); err != nil {
			t.Fatal(err)
		}
		revision := request.Revision
		result := wms.Execute(adapter.CallRequest{
			Operation:               string(validation.OperationRequestLinkChangeSet),
			ActorContextRef:         "drafting-table",
			RequestID:               request.ID,
			ExpectedRequestRevision: &revision,
			IdempotencyKey:          "link-non-mutable",
			Payload:                 jsonPayload(t, map[string]any{"change_set_id": "CS-link-merged"}),
		})
		if result.OK || result.Error == nil || result.Error.Code != adapter.CodeInvalidRequest {
			t.Fatalf("link to a merged change set = %#v, want INVALID_REQUEST", result)
		}
		unchanged, _, _, err := wms.loadRequest(request.ID)
		if err != nil || unchanged.Revision != request.Revision {
			t.Fatalf("request mutated by rejected non-mutable link: %#v, err=%v", unchanged, err)
		}
	})
}

// TestLinkWorkItemRejectsEmptyAndDuplicate mirrors the memory adapter's link
// contract (wms/memory/requests.go:424-425). Previously linkWorkItemLocked
// only checked that the work item exists, so an empty build_work_item_id (a
// no-op loadWorkItem("") miss aside) or relinking the same work item a
// second time applied and bumped request_revision.
func TestLinkWorkItemRejectsEmptyAndDuplicate(t *testing.T) {
	fake := NewFakeClient()
	wms := newTestAdapter(t, fake)
	request := createGithubTestRequest(t, wms, "Link Work Item Duplicate Request", "second link under the same work item is rejected")
	candidate := testWorkItem("wi-link-duplicate", validation.StateInitial, 0)
	if result := wms.Execute(materializeCall(candidate, "mat-link-duplicate", "mat-link-duplicate-key")); !result.OK {
		t.Fatalf("materialize = %#v", result)
	}

	revision := request.Revision
	first := wms.Execute(adapter.CallRequest{
		Operation:               string(validation.OperationRequestLinkBuildWorkItem),
		ActorContextRef:         "drafting-table",
		RequestID:               request.ID,
		ExpectedRequestRevision: &revision,
		IdempotencyKey:          "link-wi-dup-1",
		Payload:                 jsonPayload(t, map[string]any{"build_work_item_id": candidate.ID}),
	})
	if !first.OK {
		t.Fatalf("first link = %#v, want applied", first)
	}
	linked := first.Resource.(adapter.RequestRecord)
	second := wms.Execute(adapter.CallRequest{
		Operation:               string(validation.OperationRequestLinkBuildWorkItem),
		ActorContextRef:         "drafting-table",
		RequestID:               request.ID,
		ExpectedRequestRevision: &linked.Revision,
		IdempotencyKey:          "link-wi-dup-2",
		Payload:                 jsonPayload(t, map[string]any{"build_work_item_id": candidate.ID}),
	})
	if second.OK || second.Error == nil || second.Error.Code != adapter.CodeInvalidRequest {
		t.Fatalf("duplicate work-item link = %#v, want INVALID_REQUEST", second)
	}
	unchanged, _, _, err := wms.loadRequest(request.ID)
	if err != nil || unchanged.Revision != linked.Revision {
		t.Fatalf("request mutated by rejected duplicate work-item link: %#v, err=%v", unchanged, err)
	}
}

// TestRefineRequestDurableApprovalConsumptionBlocksReuseAfterRestart covers
// the fix for the GitHub adapter consuming approvals only in its in-process
// index. Previously, after a restart the in-process "consumed" marker was
// gone and the same approval ID could authorize a second refinement. The
// fix persists ConsumedApprovalIDs on the request's GitHub issue in the
// same write as the request revision, so the durable record - not the
// volatile in-process approval index - is what blocks reuse.
func TestRefineRequestDurableApprovalConsumptionBlocksReuseAfterRestart(t *testing.T) {
	fake := NewFakeClient()
	wms1 := newTestAdapter(t, fake)
	request := createGithubTestRequest(t, wms1, "Durable Approval Request", "exercise durable approval consumption")

	content := validation.RefinementContent{
		Intent:          "Durable Approval Request",
		Rationale:       "exercise durable approval consumption",
		Classification:  "changes",
		RefinementState: "ready-for-dimensioning",
	}
	approval := seedGithubRefinementApproval(t, wms1, "durable-approval-1", request.ID, content)
	refine := wms1.Execute(refineGithubCall(request.ID, request.Revision, "refine-durable-1", refineRequestPayload{
		Classification:           "changes",
		RefinementState:          "ready-for-dimensioning",
		HumanApprovalID:          approval.ID,
		ApprovalRefinementDigest: approval.Digest,
	}))
	if !refine.OK || refine.ApprovalStatus != validation.ApprovalStatusConsumed {
		t.Fatalf("refine = %#v, want applied and consumed", refine)
	}
	refined, ok := refine.Resource.(adapter.RequestRecord)
	if !ok || refined.Revision != request.Revision+1 {
		t.Fatalf("refine resource = %#v, want revision %d", refine.Resource, request.Revision+1)
	}

	// Simulate a process restart: a fresh adapter instance over the same
	// durable GitHub store starts with empty in-process indexes. A
	// request.create call hydrates its request index from durable issues,
	// the same way every create does.
	wms2 := newTestAdapter(t, fake)
	hydrate := wms2.Execute(adapter.CallRequest{
		Operation:       string(validation.OperationRequestCreate),
		ActorContextRef: "drafting-table",
		IdempotencyKey:  "req-durable-approval-hydrate",
		Payload: jsonPayload(t, map[string]any{
			"intent":    "Unrelated Hydration Request",
			"rationale": "trigger index hydration after restart",
		}),
	})
	if !hydrate.OK {
		t.Fatalf("hydrate create = %#v", hydrate)
	}

	// The Gate reissues the same approval ID as unused; the WMS's own
	// durable record must still refuse to let it authorize another
	// refinement of the same request.
	if err := wms2.SeedApproval(validation.ApprovalRecord{
		ID:                 approval.ID,
		ApprovedSubject:    approval.ApprovedSubject,
		DelegatedPrincipal: approval.DelegatedPrincipal,
		ProjectID:          approval.ProjectID,
		RequestID:          approval.RequestID,
		Digest:             approval.Digest,
		Action:             approval.Action,
		PolicyVersion:      approval.PolicyVersion,
		ExpiresAt:          approval.ExpiresAt,
		Status:             validation.ApprovalStatusUnused,
	}); err != nil {
		t.Fatalf("reseed approval: %v", err)
	}

	reuse := wms2.Execute(refineGithubCall(request.ID, refined.Revision, "refine-durable-2", refineRequestPayload{
		Classification:           "changes",
		RefinementState:          "ready-for-dimensioning",
		HumanApprovalID:          approval.ID,
		ApprovalRefinementDigest: approval.Digest,
	}))
	if reuse.OK || reuse.Error == nil || reuse.Error.Code != validation.CodeUnauthorizedAction {
		t.Fatalf("reused approval after restart = %#v, want UNAUTHORIZED_ACTION", reuse)
	}

	final, _, _, err := wms2.loadRequest(request.ID)
	if err != nil {
		t.Fatalf("load request after rejected reuse: %v", err)
	}
	if final.Revision != refined.Revision {
		t.Fatalf("revision after rejected reuse = %d, want unchanged %d", final.Revision, refined.Revision)
	}
}

// TestRefineRequestReconcilesAfterAmbiguousUpdate covers the fix for an
// ambiguous (exhausted-transient) UpdateIssue call: GitHub applies the
// write, but the adapter sees an error and reports UNKNOWN_MUTATION.
// Previously a retry under the same idempotency key saw the already
// incremented revision and froze a spurious STALE_REQUEST_REVISION under
// that key. The fix persists a last-mutation binding in the same write as
// the revision bump, so the retry reconciles to the original applied
// result instead.
func TestRefineRequestReconcilesAfterAmbiguousUpdate(t *testing.T) {
	fake := NewFakeClient()
	wms := newTestAdapter(t, fake)
	request := createGithubTestRequest(t, wms, "Ambiguous Refine Request", "exercise lost-response reconciliation")

	content := validation.RefinementContent{
		Intent:          "Ambiguous Refine Request",
		Rationale:       "exercise lost-response reconciliation",
		Classification:  "changes",
		RefinementState: "ready-for-dimensioning",
	}
	approval := seedGithubRefinementApproval(t, wms, "ambiguous-approval-1", request.ID, content)
	call := refineGithubCall(request.ID, request.Revision, "refine-ambiguous-1", refineRequestPayload{
		Classification:           "changes",
		RefinementState:          "ready-for-dimensioning",
		HumanApprovalID:          approval.ID,
		ApprovalRefinementDigest: approval.Digest,
	})

	fake.InjectUpdateSucceedThenTransient(1)
	ambiguous := wms.Execute(call)
	if ambiguous.Mutation != adapter.MutationUnknown {
		t.Fatalf("ambiguous refine = %#v, want UNKNOWN_MUTATION", ambiguous)
	}

	retry := wms.Execute(call)
	if !retry.OK || retry.Outcome != adapter.OutcomeReplayed {
		t.Fatalf("retry after ambiguous update = %#v, want replayed applied result", retry)
	}
	if retry.Error != nil {
		t.Fatalf("retry after ambiguous update returned an error: %#v", retry.Error)
	}
	if retry.ApprovalStatus != validation.ApprovalStatusConsumed {
		t.Fatalf("retry approval status = %q, want consumed", retry.ApprovalStatus)
	}
	refined, ok := retry.Resource.(adapter.RequestRecord)
	if !ok || refined.Revision != request.Revision+1 {
		t.Fatalf("retry resource = %#v, want revision %d", retry.Resource, request.Revision+1)
	}
}

// TestUpdatePriorityReconcilesAfterAmbiguousUpdate covers the same
// reconciliation fix for request.update-priority.
func TestUpdatePriorityReconcilesAfterAmbiguousUpdate(t *testing.T) {
	fake := NewFakeClient()
	wms := newTestAdapter(t, fake)
	request := createGithubTestRequest(t, wms, "Ambiguous Priority Request", "exercise lost-response reconciliation")

	revision := request.Revision
	call := adapter.CallRequest{
		Operation:               string(validation.OperationRequestUpdatePriority),
		ActorContextRef:         "human-maintainer",
		RequestID:               request.ID,
		ExpectedRequestRevision: &revision,
		IdempotencyKey:          "priority-ambiguous-1",
		Payload:                 jsonPayload(t, map[string]any{"business_priority": "urgent"}),
	}

	fake.InjectUpdateSucceedThenTransient(1)
	ambiguous := wms.Execute(call)
	if ambiguous.Mutation != adapter.MutationUnknown {
		t.Fatalf("ambiguous update-priority = %#v, want UNKNOWN_MUTATION", ambiguous)
	}

	retry := wms.Execute(call)
	if !retry.OK || retry.Outcome != adapter.OutcomeReplayed {
		t.Fatalf("retry after ambiguous update-priority = %#v, want replayed applied result", retry)
	}
	if retry.AuditEvent != "priority-updated" {
		t.Fatalf("retry audit event = %q, want priority-updated", retry.AuditEvent)
	}
	refreshed, ok := retry.Resource.(adapter.RequestRecord)
	if !ok || refreshed.BusinessPriority != "urgent" || refreshed.Revision != request.Revision+1 {
		t.Fatalf("retry resource = %#v, want priority urgent at revision %d", retry.Resource, request.Revision+1)
	}
}

// TestUpdatePriorityResultIncludesAuditEvent covers the fix for
// request.update-priority omitting the contract-required audit-event result
// field (docs/architecture/drafting-table-wms.md:233), matching the memory
// adapter (wms/memory/requests.go).
func TestUpdatePriorityResultIncludesAuditEvent(t *testing.T) {
	fake := NewFakeClient()
	wms := newTestAdapter(t, fake)
	request := createGithubTestRequest(t, wms, "Audit Event Request", "exercise audit event result field")

	revision := request.Revision
	result := wms.Execute(adapter.CallRequest{
		Operation:               string(validation.OperationRequestUpdatePriority),
		ActorContextRef:         "human-maintainer",
		RequestID:               request.ID,
		ExpectedRequestRevision: &revision,
		IdempotencyKey:          "priority-audit-event-1",
		Payload:                 jsonPayload(t, map[string]any{"business_priority": "high"}),
	})
	if !result.OK {
		t.Fatalf("update-priority = %#v", result)
	}
	if result.AuditEvent != "priority-updated" {
		t.Fatalf("audit event = %q, want %q", result.AuditEvent, "priority-updated")
	}
}

// TestRequestSemanticKeyDistinguishesAmbiguousJoin covers the fix for the
// GitHub adapter's delimiter-joined semantic key, which let
// affected_interfaces: ["a,b"] collide with affected_interfaces: ["a", "b"].
// The fix hashes a canonical structured value instead, matching the memory
// adapter (wms/memory/requests.go).
func TestRequestSemanticKeyDistinguishesAmbiguousJoin(t *testing.T) {
	t.Parallel()
	joined := requestSemanticKeyFor("intent", []string{"a,b"}, nil)
	split := requestSemanticKeyFor("intent", []string{"a", "b"}, nil)
	if joined == split {
		t.Fatalf("semantic key collided for [%q] and [%q, %q]: %q", "a,b", "a", "b", joined)
	}
}

// TestRequestCreateDoesNotFalselyFlagDuplicateAcrossAmbiguousJoin is an
// end-to-end check that the fixed semantic key no longer produces a false
// DUPLICATE_REQUEST for genuinely distinct requests.
func TestRequestCreateDoesNotFalselyFlagDuplicateAcrossAmbiguousJoin(t *testing.T) {
	fake := NewFakeClient()
	wms := newTestAdapter(t, fake)

	first := wms.Execute(adapter.CallRequest{
		Operation:       string(validation.OperationRequestCreate),
		ActorContextRef: "drafting-table",
		IdempotencyKey:  "req-ambiguous-join-1",
		Payload: jsonPayload(t, map[string]any{
			"intent":              "Ambiguous Join Request",
			"rationale":           "first interfaces element contains a comma",
			"affected_interfaces": []string{"a,b"},
		}),
	})
	if !first.OK {
		t.Fatalf("first create = %#v", first)
	}

	second := wms.Execute(adapter.CallRequest{
		Operation:       string(validation.OperationRequestCreate),
		ActorContextRef: "drafting-table",
		IdempotencyKey:  "req-ambiguous-join-2",
		Payload: jsonPayload(t, map[string]any{
			"intent":              "Ambiguous Join Request",
			"rationale":           "two distinct interfaces",
			"affected_interfaces": []string{"a", "b"},
		}),
	})
	if !second.OK || second.Error != nil {
		t.Fatalf("second create = %#v, want a distinct request, not DUPLICATE_REQUEST", second)
	}
}

// scanFailOnceAfterClient lets the first `skip` ListIssues calls through,
// fails exactly the next one with a permanent (non-transient, non-ErrNotFound)
// error, then passes every later call through normally. It targets one
// specific scan without disturbing other list calls made in the same or
// later operations.
type scanFailOnceAfterClient struct {
	inner    Client
	skip     int
	consumed bool
}

func (c *scanFailOnceAfterClient) CreateIssue(ctx context.Context, input CreateIssueInput) (Issue, error) {
	return c.inner.CreateIssue(ctx, input)
}

func (c *scanFailOnceAfterClient) GetIssue(ctx context.Context, number int) (Issue, error) {
	return c.inner.GetIssue(ctx, number)
}

func (c *scanFailOnceAfterClient) UpdateIssue(ctx context.Context, number int, input UpdateIssueInput) (Issue, error) {
	return c.inner.UpdateIssue(ctx, number, input)
}

func (c *scanFailOnceAfterClient) ListIssues(ctx context.Context, filter ListIssuesFilter) ([]Issue, error) {
	if !c.consumed {
		if c.skip > 0 {
			c.skip--
		} else {
			c.consumed = true
			return nil, errors.New("permanent github scan failure (not transient)")
		}
	}
	return c.inner.ListIssues(ctx, filter)
}

// TestReconcilePendingRequestCreateScanFailureDoesNotDuplicate covers the
// fix for reconcilePendingRequestCreate treating any non-transient scan
// error the same as a clean no-match. Previously that let the caller
// proceed to CreateIssue again under the same reserved request ID,
// producing a second GitHub issue for one logical request. The fix
// distinguishes a definitive ErrNotFound (safe to proceed) from every other
// scan error (must not proceed).
func TestReconcilePendingRequestCreateScanFailureDoesNotDuplicate(t *testing.T) {
	fake := NewFakeClient()
	client := &scanFailOnceAfterClient{inner: fake, skip: 1} // let hydrate succeed; fail the reconcile scan
	wms := newTestAdapterWithClient(t, client)

	// Seed a pending request-create reservation directly, as persistNewRequestLocked
	// would after a prior ambiguous CreateIssue failure for this key.
	wms.idx.pendingRequestCreate["req-scan-failure"] = "REQ-00001"

	call := adapter.CallRequest{
		Operation:       string(validation.OperationRequestCreate),
		ActorContextRef: "drafting-table",
		IdempotencyKey:  "req-scan-failure",
		Payload: jsonPayload(t, map[string]any{
			"intent":    "Scan Failure Request",
			"rationale": "reconcile scan failure must not create twice",
		}),
	}

	result := wms.Execute(call)
	if result.OK {
		t.Fatalf("create during scan failure = %#v, want rejection", result)
	}
	if result.Error == nil || result.Error.Code != adapter.CodeWMSUnavailable {
		t.Fatalf("create during scan failure error = %#v, want WMS_UNAVAILABLE", result.Error)
	}
	if fake.IssueCount() != 0 {
		t.Fatalf("issues after scan failure = %d, want 0 (must not create a duplicate)", fake.IssueCount())
	}

	retry := wms.Execute(call)
	if !retry.OK || retry.Outcome != adapter.OutcomeApplied {
		t.Fatalf("retry after recovered scan = %#v, want applied", retry)
	}
	if fake.IssueCount() != 1 {
		t.Fatalf("issues after recovery = %d, want 1", fake.IssueCount())
	}
}

func createGithubTestRequest(t *testing.T, wms *Adapter, intent, rationale string) adapter.RequestRecord {
	t.Helper()
	result := wms.Execute(adapter.CallRequest{
		Operation:       string(validation.OperationRequestCreate),
		ActorContextRef: "drafting-table",
		IdempotencyKey:  "create-" + intent,
		Payload: jsonPayload(t, map[string]any{
			"intent":    intent,
			"rationale": rationale,
		}),
	})
	if !result.OK {
		t.Fatalf("create request %q: %#v", intent, result)
	}
	request, ok := result.Resource.(adapter.RequestRecord)
	if !ok {
		t.Fatalf("create request %q resource = %#v", intent, result.Resource)
	}
	return request
}

func seedGithubApprovalForRequest(t *testing.T, wms *Adapter, id, requestID, digest string) validation.ApprovalRecord {
	t.Helper()
	approval := validation.ApprovalRecord{
		ID:                 id,
		ApprovedSubject:    "human-reviewer",
		DelegatedPrincipal: "drafting-agent",
		ProjectID:          "fixture-project",
		RequestID:          requestID,
		Digest:             digest,
		Action:             validation.OperationRequestRefine,
		PolicyVersion:      "wms-policy/v1",
		ExpiresAt:          testTime.Add(time.Hour),
		Status:             validation.ApprovalStatusUnused,
	}
	if err := wms.SeedApproval(approval); err != nil {
		t.Fatal(err)
	}
	return approval
}

func seedGithubRefinementApproval(t *testing.T, wms *Adapter, id, requestID string, content validation.RefinementContent) validation.ApprovalRecord {
	t.Helper()
	return seedGithubApprovalForRequest(t, wms, id, requestID, validation.RefinementDigest(content))
}

func refineGithubCall(requestID string, revision uint64, key string, payload refineRequestPayload) adapter.CallRequest {
	return adapter.CallRequest{
		Operation:               string(validation.OperationRequestRefine),
		ActorContextRef:         "drafting-table",
		RequestID:               requestID,
		ExpectedRequestRevision: &revision,
		IdempotencyKey:          key,
		Payload:                 mustJSON(payload),
	}
}

// TestRequestOperationsAfterRestartServeDurableRecords covers the fix for
// request reads and mutations trusting only the in-process requestIssue map.
// Previously, after a restart, request.get/query/refine and the link
// operations returned NOT_FOUND (or an empty query result) for durable
// requests until some unrelated request.create happened to hydrate the
// index. The fix hydrates the request indexes from the durable store at the
// start of every request operation.
func TestRequestOperationsAfterRestartServeDurableRecords(t *testing.T) {
	fake := NewFakeClient()
	first := newTestAdapter(t, fake)
	request := createGithubTestRequest(t, first, "Restart Serve Request", "durable after restart")

	// Fresh adapter process over the same durable store.
	second := newTestAdapter(t, fake)

	got := second.Execute(adapter.CallRequest{
		Operation:       string(validation.OperationRequestGet),
		ActorContextRef: "drafting-table",
		RequestID:       request.ID,
	})
	if !got.OK || got.RequestID != request.ID {
		t.Fatalf("request.get after restart = %#v, want durable request", got)
	}

	query := second.Execute(adapter.CallRequest{
		Operation:       string(validation.OperationRequestQuery),
		ActorContextRef: "drafting-table",
		Payload:         jsonPayload(t, map[string]any{}),
	})
	if !query.OK || len(query.Requests) != 1 || query.Requests[0].ID != request.ID {
		t.Fatalf("request.query after restart = %#v, want the durable request, not an empty set", query)
	}

	// A mutating operation must not freeze a spurious NOT_FOUND under its
	// idempotency key on a restarted adapter.
	revision := request.Revision
	changeSet := adapter.ChangeSet{ID: "CS-Restart", Revision: "proposed"}
	if err := second.SeedChangeSet(changeSet); err != nil {
		t.Fatal(err)
	}
	link := second.Execute(adapter.CallRequest{
		Operation:               string(validation.OperationRequestLinkChangeSet),
		ActorContextRef:         "drafting-table",
		RequestID:               request.ID,
		ExpectedRequestRevision: &revision,
		IdempotencyKey:          "link-restart-1",
		Payload:                 jsonPayload(t, map[string]any{"change_set_id": "CS-Restart", "target_revision": "proposed"}),
	})
	if !link.OK {
		t.Fatalf("request link after restart = %#v, want applied", link)
	}
}

// TestRequestOperationsHydrationScanFailureReturnsWMSUnavailable verifies a
// failed hydration scan is reported as WMS_UNAVAILABLE, never served as an
// empty success or a NOT_FOUND frozen under a mutation's idempotency key.
func TestRequestOperationsHydrationScanFailureReturnsWMSUnavailable(t *testing.T) {
	fake := NewFakeClient()
	first := newTestAdapter(t, fake)
	createGithubTestRequest(t, first, "Hydration Failure Request", "scan must not look empty")

	client := &scanFailOnceAfterClient{inner: fake, skip: 0}
	second := newTestAdapterWithClient(t, client)

	query := second.Execute(adapter.CallRequest{
		Operation:       string(validation.OperationRequestQuery),
		ActorContextRef: "drafting-table",
		Payload:         jsonPayload(t, map[string]any{}),
	})
	if query.OK || query.Error == nil || query.Error.Code != adapter.CodeWMSUnavailable {
		t.Fatalf("request query during scan failure = %#v, want WMS_UNAVAILABLE (not an empty list)", query)
	}

	// Recovery: the same adapter serves the durable set once the scan works.
	recovered := second.Execute(adapter.CallRequest{
		Operation:       string(validation.OperationRequestQuery),
		ActorContextRef: "drafting-table",
		Payload:         jsonPayload(t, map[string]any{}),
	})
	if !recovered.OK || len(recovered.Requests) != 1 {
		t.Fatalf("request query after scan recovery = %#v, want the durable request", recovered)
	}
}
