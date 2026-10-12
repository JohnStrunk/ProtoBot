package github

import (
	"context"
	"testing"

	"github.com/redhat-et/protobot/wms/adapter"
	"github.com/redhat-et/protobot/wms/validation"
)

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
	changeSet := adapter.ChangeSet{ID: "CS-Restart", Revision: "rev-1"}
	if err := second.SeedChangeSet(changeSet); err != nil {
		t.Fatal(err)
	}
	link := second.Execute(adapter.CallRequest{
		Operation:               string(validation.OperationRequestLinkChangeSet),
		ActorContextRef:         "drafting-table",
		RequestID:               request.ID,
		ExpectedRequestRevision: &revision,
		IdempotencyKey:          "link-restart-1",
		Payload:                 jsonPayload(t, map[string]any{"change_set_id": "CS-Restart", "target_revision": "rev-1"}),
	})
	if !link.OK {
		t.Fatalf("request link after restart = %#v, want applied", link)
	}
}

// TestWorkItemQueriesAfterRestartServeDurableRecords covers the work-item
// analogue: work-item.get and work-item.query previously iterated only the
// in-process workItemIssue map, so a restarted adapter returned an empty
// success instead of the durable set. The fix hydrates the work-item
// indexes from durable issues before serving queries.
func TestWorkItemQueriesAfterRestartServeDurableRecords(t *testing.T) {
	fake := NewFakeClient()
	first := newTestAdapter(t, fake)
	candidate := testWorkItem("wi-restart-query", validation.StateInitial, 0)
	if result := first.Execute(materializeCall(candidate, "mat-restart-query", "mat-restart-query-key")); !result.OK {
		t.Fatalf("materialize = %#v", result)
	}

	// Fresh adapter process over the same durable store.
	second := newTestAdapter(t, fake)

	got := second.Execute(adapter.CallRequest{
		Operation:       string(validation.OperationWorkItemGet),
		ActorContextRef: "drafting-table",
		WorkItemID:      candidate.ID,
	})
	if !got.OK || got.WorkItemID != candidate.ID {
		t.Fatalf("work-item.get after restart = %#v, want durable work item", got)
	}

	query := second.Execute(adapter.CallRequest{
		Operation:       string(validation.OperationWorkItemQuery),
		ActorContextRef: "drafting-table",
		Payload:         jsonPayload(t, map[string]any{}),
	})
	if !query.OK || len(query.Items) != 1 || query.Items[0].ID != candidate.ID {
		t.Fatalf("work-item.query after restart = %#v, want the durable work item, not an empty set", query)
	}
}

// TestClaimAfterRestartServesDurableWorkItem covers a claim issued by a
// restarted adapter against a work item materialized by a previous process:
// previously the in-process index miss made observeTarget treat the durable
// item as missing and could freeze that rejection under the claim key.
func TestClaimAfterRestartServesDurableWorkItem(t *testing.T) {
	fake := NewFakeClient()
	first := newTestAdapter(t, fake)
	candidate := testWorkItem("wi-restart-claim", validation.StateInitial, 0)
	if result := first.Execute(materializeCall(candidate, "mat-restart-claim", "mat-restart-claim-key")); !result.OK {
		t.Fatalf("materialize = %#v", result)
	}
	item, ok := first.LoadWorkItemForTest(candidate.ID)
	if !ok {
		t.Fatal("missing work item")
	}

	second := newTestAdapter(t, fake)
	version := item.ContractVersion
	result := second.Execute(adapter.CallRequest{
		Operation:               string(validation.OperationClaim),
		ActorContextRef:         "job-site",
		WorkItemID:              item.ID,
		ExpectedState:           item.State,
		ExpectedContractVersion: &version,
		IdempotencyKey:          "claim-restart-1",
	})
	if !result.OK || result.Outcome != adapter.OutcomeApplied {
		t.Fatalf("claim after restart = %#v, want applied", result)
	}
	if result.WorkItemState != validation.StateBuilding {
		t.Fatalf("claim state after restart = %q, want building", result.WorkItemState)
	}
}

// TestClaimLostResponseReconcilesToAppliedClaim covers the fix for an
// ambiguous (exhausted-transient) UpdateIssue during claim: GitHub applies
// the lease, but the adapter returns UNKNOWN_MUTATION with no durable
// binding. A retry under the same key previously loaded the already-claimed
// item, was rejected with DUPLICATE_CLAIM, and that determinate rejection
// was frozen under the claim key. The fix persists a last-claim binding in
// the same body write as the lease so the retry reconciles.
func TestClaimLostResponseReconcilesToAppliedClaim(t *testing.T) {
	fake := NewFakeClient()
	wms := newTestAdapter(t, fake)
	candidate := testWorkItem("wi-claim-lost", validation.StateInitial, 0)
	if result := wms.Execute(materializeCall(candidate, "mat-claim-lost", "mat-claim-lost-key")); !result.OK {
		t.Fatalf("materialize = %#v", result)
	}
	item, ok := wms.LoadWorkItemForTest(candidate.ID)
	if !ok {
		t.Fatal("missing work item")
	}

	version := item.ContractVersion
	call := adapter.CallRequest{
		Operation:               string(validation.OperationClaim),
		ActorContextRef:         "job-site",
		WorkItemID:              item.ID,
		ExpectedState:           item.State,
		ExpectedContractVersion: &version,
		IdempotencyKey:          "claim-lost-1",
	}

	fake.InjectUpdateSucceedThenTransient(1)
	ambiguous := wms.Execute(call)
	if ambiguous.Mutation != adapter.MutationUnknown {
		t.Fatalf("ambiguous claim = %#v, want UNKNOWN_MUTATION", ambiguous)
	}

	retry := wms.Execute(call)
	if !retry.OK || retry.Outcome != adapter.OutcomeReplayed {
		t.Fatalf("claim retry after ambiguous update = %#v, want replayed applied result", retry)
	}
	if retry.Error != nil {
		t.Fatalf("claim retry error = %#v, want none", retry.Error)
	}
	if retry.WorkItemState != validation.StateBuilding {
		t.Fatalf("claim retry state = %q, want building", retry.WorkItemState)
	}
	if retry.Decision == nil || retry.Decision.FencingTokenIssued == "" {
		t.Fatalf("claim retry decision = %#v, want the originally issued fencing token", retry.Decision)
	}
	if retry.Decision.Before.State != item.State || retry.Decision.Before.ContractVersion != item.ContractVersion {
		t.Fatalf("claim retry before = %#v, want the pre-claim snapshot", retry.Decision.Before)
	}
}

// TestClaimPreservesRequestAndChangeSetLinks covers the fix for
// updateWorkItemIssue always writing LinkedRequestID and LinkedChangeSetID
// from its arguments: a claim after request.link-build-work-item previously
// blanked the durable links, and subsequent work-item projections omitted
// request_id and change_set_id. The fix preserves the stored links unless a
// caller replaces them.
func TestClaimPreservesRequestAndChangeSetLinks(t *testing.T) {
	fake := NewFakeClient()
	wms := newTestAdapter(t, fake)
	request := createGithubTestRequest(t, wms, "Link Preservation Request", "claim must not blank links")
	changeSet := adapter.ChangeSet{ID: "CS-Preserve", Revision: "rev-1", BusinessPriority: "high"}
	if err := wms.SeedChangeSet(changeSet); err != nil {
		t.Fatal(err)
	}

	candidate := testWorkItem("wi-preserve-links", validation.StateInitial, 0)
	if result := wms.Execute(materializeCall(candidate, "mat-preserve-links", "mat-preserve-links-key")); !result.OK {
		t.Fatalf("materialize = %#v", result)
	}

	// Link the change set to the request first so the work item's durable
	// body carries both a request link and a change-set link before claim.
	linkChangeSet := wms.Execute(adapter.CallRequest{
		Operation:               string(validation.OperationRequestLinkChangeSet),
		ActorContextRef:         "drafting-table",
		RequestID:               request.ID,
		ExpectedRequestRevision: &request.Revision,
		IdempotencyKey:          "link-preserve-cs-1",
		Payload:                 jsonPayload(t, map[string]any{"change_set_id": "CS-Preserve", "target_revision": "rev-1"}),
	})
	if !linkChangeSet.OK {
		t.Fatalf("link change set = %#v", linkChangeSet)
	}
	linkedRequest, ok := linkChangeSet.Resource.(adapter.RequestRecord)
	if !ok {
		t.Fatalf("link change set resource = %T", linkChangeSet.Resource)
	}

	link := wms.Execute(adapter.CallRequest{
		Operation:               string(validation.OperationRequestLinkBuildWorkItem),
		ActorContextRef:         "drafting-table",
		RequestID:               request.ID,
		ExpectedRequestRevision: &linkedRequest.Revision,
		IdempotencyKey:          "link-preserve-1",
		Payload:                 jsonPayload(t, map[string]any{"build_work_item_id": candidate.ID}),
	})
	if !link.OK {
		t.Fatalf("link work item = %#v", link)
	}

	item, ok := wms.LoadWorkItemForTest(candidate.ID)
	if !ok {
		t.Fatal("missing work item")
	}
	version := item.ContractVersion
	claim := wms.Execute(adapter.CallRequest{
		Operation:               string(validation.OperationClaim),
		ActorContextRef:         "job-site",
		WorkItemID:              item.ID,
		ExpectedState:           item.State,
		ExpectedContractVersion: &version,
		IdempotencyKey:          "claim-preserve-1",
	})
	if !claim.OK {
		t.Fatalf("claim after link = %#v", claim)
	}

	number, ok := wms.WorkItemIssueNumber(candidate.ID)
	if !ok {
		t.Fatal("missing work item issue number")
	}
	issue, ok := fake.GetIssueRaw(number)
	if !ok {
		t.Fatal("missing issue")
	}
	doc, err := decodeBody(issue.Body)
	if err != nil {
		t.Fatal(err)
	}
	if doc.LinkedRequestID != request.ID {
		t.Fatalf("linked_request_id after claim = %q, want %q", doc.LinkedRequestID, request.ID)
	}
	if doc.LinkedChangeSetID != "CS-Preserve" {
		t.Fatalf("linked_change_set_id after claim = %q, want CS-Preserve", doc.LinkedChangeSetID)
	}
	if doc.SourceFingerprint == "" {
		t.Fatal("source fingerprint was blanked by claim")
	}

	got := wms.Execute(adapter.CallRequest{
		Operation:       string(validation.OperationWorkItemGet),
		ActorContextRef: "drafting-table",
		WorkItemID:      candidate.ID,
	})
	if !got.OK {
		t.Fatalf("work-item.get after claim = %#v", got)
	}
	projection, ok := got.Resource.(adapter.WorkItemProjection)
	if !ok {
		t.Fatalf("resource type = %T, want WorkItemProjection", got.Resource)
	}
	if projection.RequestID != request.ID || projection.ChangeSetID != "CS-Preserve" {
		t.Fatalf("projection after claim = %#v, want links preserved", projection)
	}
}

// TestBlockedWorkQueryMatchesGoldenFixtureShape covers the fix for the
// blocked-work.query projection diverging from the memory adapter and the
// golden fixture: ReasonKind must be the sanitized reason class (not
// "blocked"), NextAction must be "review-resolution" (a Drafting Table
// review operation, not the Materializer's "resolve-block"), and the
// resolution options must include "defer" and "acknowledge" for every
// blocked item, not only those with a non-empty BlockReason.
func TestBlockedWorkQueryMatchesGoldenFixtureShape(t *testing.T) {
	fake := NewFakeClient()
	wms := newTestAdapter(t, fake)

	// An unresolved reason containing "undefined" sanitizes to the
	// undefined-behavior class; a readiness blocker without it sanitizes
	// to unresolved-precondition.
	undefinedBlocked := testWorkItem("wi-blocked-undefined", validation.StateInitial, 0)
	undefinedBlocked.Readiness.UnresolvedReasons = []string{"undefined behavior observed in the source contract"}
	if result := wms.Execute(materializeCall(undefinedBlocked, "mat-blocked-undefined", "mat-blocked-undefined-key")); !result.OK {
		t.Fatalf("materialize undefined-behavior blocked = %#v", result)
	}
	preconditionBlocked := testWorkItem("wi-blocked-precondition", validation.StateInitial, 0)
	preconditionBlocked.Readiness.ImpactDispositioned = false
	if result := wms.Execute(materializeCall(preconditionBlocked, "mat-blocked-precondition", "mat-blocked-precondition-key")); !result.OK {
		t.Fatalf("materialize unresolved-precondition blocked = %#v", result)
	}

	query := wms.Execute(adapter.CallRequest{
		Operation:       string(validation.OperationBlockedWorkQuery),
		ActorContextRef: "drafting-table",
	})
	if !query.OK {
		t.Fatalf("blocked-work query = %#v", query)
	}
	if len(query.Items) != 2 {
		t.Fatalf("blocked items = %d, want 2", len(query.Items))
	}
	byID := map[string]adapter.WorkItemProjection{}
	for _, item := range query.Items {
		byID[item.ID] = item
	}
	undefined := byID["wi-blocked-undefined"]
	if undefined.ReasonKind != "undefined-behavior" {
		t.Fatalf("reason kind = %q, want undefined-behavior", undefined.ReasonKind)
	}
	precondition := byID["wi-blocked-precondition"]
	if precondition.ReasonKind != "unresolved-precondition" {
		t.Fatalf("reason kind = %q, want unresolved-precondition", precondition.ReasonKind)
	}
	for _, item := range query.Items {
		if item.NextAction != "review-resolution" {
			t.Fatalf("next action for %s = %q, want review-resolution", item.ID, item.NextAction)
		}
		wantOptions := []string{"add-requirement", "out-of-scope", "impact-amendment", "defer", "acknowledge"}
		if len(item.ResolutionOptions) != len(wantOptions) {
			t.Fatalf("resolution options for %s = %v, want %v", item.ID, item.ResolutionOptions, wantOptions)
		}
		for i, option := range wantOptions {
			if item.ResolutionOptions[i] != option {
				t.Fatalf("resolution option %d for %s = %q, want %q", i, item.ID, item.ResolutionOptions[i], option)
			}
		}
	}
}

// TestMaterializeAfterHydrationReplaysBoundWorkItem covers the composed
// path query-then-rematerialize on a cold adapter: hydration previously
// cached the materialization key with a zero replay envelope, so a later
// materialize with the same key replayed an empty rejection (OK=false, no
// work-item ID) and froze it under the caller's idempotency key. The fix
// stores the same replay envelope the durable-scan path builds.
func TestMaterializeAfterHydrationReplaysBoundWorkItem(t *testing.T) {
	fake := NewFakeClient()
	first := newTestAdapter(t, fake)
	candidate := testWorkItem("wi-hydrate-replay", validation.StateInitial, 0)
	created := first.Execute(materializeCall(candidate, "mat-hydrate-replay-1", "mat-hydrate-replay-key"))
	if !created.OK {
		t.Fatalf("materialize = %#v", created)
	}

	// Cold adapter: a query hydrates the work-item (and materialization-key)
	// indexes from durable issues before any materialize touches them.
	second := newTestAdapter(t, fake)
	query := second.Execute(adapter.CallRequest{
		Operation:       string(validation.OperationWorkItemQuery),
		ActorContextRef: "drafting-table",
		Payload:         jsonPayload(t, map[string]any{}),
	})
	if !query.OK || len(query.Items) != 1 || query.Items[0].ID != candidate.ID {
		t.Fatalf("work-item query after restart = %#v, want the durable work item", query)
	}

	// Rematerialize under the same key with a fresh idempotency key: the
	// hydrated cache entry must replay the bound work item, not an empty
	// envelope.
	replay := second.Execute(materializeCall(candidate, "mat-hydrate-replay-2", "mat-hydrate-replay-key"))
	if !replay.OK || replay.Outcome != adapter.OutcomeReplayed {
		t.Fatalf("rematerialize after hydration = %#v, want replayed applied result", replay)
	}
	if replay.WorkItemID != candidate.ID {
		t.Fatalf("rematerialize work item id = %q, want %q", replay.WorkItemID, candidate.ID)
	}
	item, ok := replay.Resource.(validation.WorkItem)
	if !ok || item.ID != candidate.ID {
		t.Fatalf("rematerialize resource = %#v, want the bound work item", replay.Resource)
	}
	if fake.CallCount("create") != 1 {
		t.Fatalf("create calls = %d, want 1 (rematerialize must not create a second issue)", fake.CallCount("create"))
	}
}

// TestOmittedMaterializationReservesKeyDurable covers VR-039: an
// implementation_required: false materialization returns omitted, then a
// repeat under the same materialization_key must replay the stored omitted
// result (same source) or reject with IDEMPOTENCY_CONFLICT (different
// source), across command keys — not only under the original idempotency
// key. Previously the omitted outcome only wrote the in-process index and
// was ignored even there (entries without a work-item ID were skipped), so a
// second command key re-evaluated and a different source could create an
// executable issue under the same key.
func TestOmittedMaterializationReservesKeyDurable(t *testing.T) {
	fake := NewFakeClient()
	wms := newTestAdapter(t, fake)
	candidate := testWorkItem("wi-omitted", validation.StateInitial, 0)
	candidate.ImplementationRequired = false

	first := wms.Execute(materializeCall(candidate, "vr039-1", "vr039-key"))
	if !first.OK || first.Decision == nil || first.Decision.Outcome != validation.OutcomeOmitted {
		t.Fatalf("first omitted materialize = %#v", first)
	}
	if first.Submission != "omitted" {
		t.Fatalf("first submission = %q, want omitted", first.Submission)
	}
	if fake.IssueCount() != 1 {
		t.Fatalf("issues after omitted materialize = %d, want 1 (the durable reservation)", fake.IssueCount())
	}

	reservation, ok := fake.GetIssueRaw(1)
	if !ok {
		t.Fatal("missing reservation issue")
	}
	doc, err := decodeBody(reservation.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !doc.Omitted || doc.WorkItem != nil || doc.MaterializationKey != "vr039-key" || doc.SourceFingerprint == "" {
		t.Fatalf("reservation document = %#v, want an omitted binding with key and fingerprint", doc)
	}

	replay := wms.Execute(materializeCall(candidate, "vr039-2", "vr039-key"))
	if !replay.OK || replay.Outcome != adapter.OutcomeReplayed || replay.Mutation != adapter.MutationNone {
		t.Fatalf("same-source replay under a new command key = %#v", replay)
	}
	if replay.Submission != "omitted" {
		t.Fatalf("replay submission = %q, want omitted", replay.Submission)
	}
	if fake.IssueCount() != 1 || fake.CallCount("create") != 1 {
		t.Fatalf("issues=%d creates=%d, want 1 and 1 (replay must not persist again)", fake.IssueCount(), fake.CallCount("create"))
	}

	changed := candidate
	changed.Priority = "high"
	changed.Readiness.PolicyCompatible = false
	conflict := wms.Execute(materializeCall(changed, "vr039-3", "vr039-key"))
	if conflict.OK || conflict.Error == nil || conflict.Error.Code != validation.CodeIdempotencyConflict {
		t.Fatalf("different-source materialize under the same key = %#v, want IDEMPOTENCY_CONFLICT", conflict)
	}
	if fake.IssueCount() != 1 {
		t.Fatalf("issues after conflict = %d, want 1 (no executable issue under the reserved key)", fake.IssueCount())
	}
	if _, exists := wms.LoadWorkItemForTest(candidate.ID); exists {
		t.Fatal("omitted materialization created an executable work item")
	}
}

// TestOmittedMaterializationReplaysAfterRestart covers the durable-rebind
// paths for an omitted reservation on a fresh adapter process: both the
// hydration path (a work-item query populates the materialization-key index)
// and the direct scan path (materialize with no prior hydration).
func TestOmittedMaterializationReplaysAfterRestart(t *testing.T) {
	fake := NewFakeClient()
	first := newTestAdapter(t, fake)
	candidate := testWorkItem("wi-omitted-restart", validation.StateInitial, 0)
	candidate.ImplementationRequired = false
	if result := first.Execute(materializeCall(candidate, "omat-restart-1", "omat-restart-key")); !result.OK {
		t.Fatalf("materialize = %#v", result)
	}

	// Fresh process: a work-item query hydrates the indexes (including the
	// omitted materialization-key binding) before any materialize.
	second := newTestAdapter(t, fake)
	query := second.Execute(adapter.CallRequest{
		Operation:       string(validation.OperationWorkItemQuery),
		ActorContextRef: "drafting-table",
		Payload:         jsonPayload(t, map[string]any{}),
	})
	if !query.OK {
		t.Fatalf("work-item query after restart = %#v", query)
	}

	replay := second.Execute(materializeCall(candidate, "omat-restart-2", "omat-restart-key"))
	if !replay.OK || replay.Outcome != adapter.OutcomeReplayed || replay.Mutation != adapter.MutationNone || replay.Submission != "omitted" {
		t.Fatalf("hydrated replay = %#v", replay)
	}

	// A third process skips hydration: the materialize scan itself must
	// rebind the omitted reservation.
	third := newTestAdapter(t, fake)
	direct := third.Execute(materializeCall(candidate, "omat-restart-3", "omat-restart-key"))
	if !direct.OK || direct.Outcome != adapter.OutcomeReplayed || direct.Submission != "omitted" {
		t.Fatalf("cold-scan replay = %#v", direct)
	}

	changed := candidate
	changed.Readiness.PolicyCompatible = false
	conflict := third.Execute(materializeCall(changed, "omat-restart-4", "omat-restart-key"))
	if conflict.OK || conflict.Error == nil || conflict.Error.Code != validation.CodeIdempotencyConflict {
		t.Fatalf("cold-scan conflict = %#v, want IDEMPOTENCY_CONFLICT", conflict)
	}
	if fake.IssueCount() != 1 || fake.CallCount("create") != 1 {
		t.Fatalf("issues=%d creates=%d, want 1 and 1", fake.IssueCount(), fake.CallCount("create"))
	}
}

// TestOmittedReservationLostCreateResponseReconciles covers an ambiguous
// (lost-response) CreateIssue while persisting an omitted reservation: the
// retry scan must rebind the durable reservation instead of persisting a
// second one.
func TestOmittedReservationLostCreateResponseReconciles(t *testing.T) {
	fake := NewFakeClient()
	wms := newTestAdapter(t, fake)
	candidate := testWorkItem("wi-omitted-lost", validation.StateInitial, 0)
	candidate.ImplementationRequired = false
	fake.InjectCreateSucceedThenTransient(1)

	first := wms.Execute(materializeCall(candidate, "omat-lost-1", "omat-lost-key"))
	if !first.OK || first.Decision == nil || first.Decision.Outcome != validation.OutcomeOmitted {
		t.Fatalf("omitted materialize after lost response = %#v, want applied via reconcile", first)
	}
	if fake.IssueCount() != 1 {
		t.Fatalf("issues after lost response = %d, want 1", fake.IssueCount())
	}

	replay := wms.Execute(materializeCall(candidate, "omat-lost-2", "omat-lost-key"))
	if !replay.OK || replay.Outcome != adapter.OutcomeReplayed || replay.Submission != "omitted" {
		t.Fatalf("replay after lost response = %#v", replay)
	}
	if fake.IssueCount() != 1 || fake.CallCount("create") != 1 {
		t.Fatalf("issues=%d creates=%d, want 1 and 1", fake.IssueCount(), fake.CallCount("create"))
	}
}

// TestOmittedReservationRestartAfterUnknownMutationReconciles covers a
// process that dies between persisting an omitted reservation and
// reconciling the ambiguous create: the reservation is durable, so a fresh
// process must reconcile the retry instead of persisting a second one.
func TestOmittedReservationRestartAfterUnknownMutationReconciles(t *testing.T) {
	fake := NewFakeClient()
	fake.InjectCreateSucceedThenTransient(1)
	// The list budget allows exactly the scans materialize makes before the
	// rebind attempt (target load, materialization-key check), so the
	// reservation persists but its reconcile scan fails: the process state
	// after an unreconciled UNKNOWN_MUTATION.
	first := newTestAdapterWithClient(t, &listBudgetClient{inner: fake, remaining: 2})
	candidate := testWorkItem("wi-omitted-unknown", validation.StateInitial, 0)
	candidate.ImplementationRequired = false

	unknown := first.Execute(materializeCall(candidate, "omat-unknown-1", "omat-unknown-key"))
	if unknown.Mutation != adapter.MutationUnknown {
		t.Fatalf("omitted materialize with unreconcilable scan = %#v, want UNKNOWN_MUTATION", unknown)
	}
	if fake.IssueCount() != 1 {
		t.Fatalf("issues after unknown mutation = %d, want 1 (the reservation persisted despite the lost response)", fake.IssueCount())
	}

	second := newTestAdapter(t, fake)
	replay := second.Execute(materializeCall(candidate, "omat-unknown-1", "omat-unknown-key"))
	if !replay.OK || replay.Outcome != adapter.OutcomeReplayed || replay.Submission != "omitted" {
		t.Fatalf("retry after restart = %#v, want the durable reservation replayed", replay)
	}
	if fake.IssueCount() != 1 || fake.CallCount("create") != 1 {
		t.Fatalf("issues=%d creates=%d, want 1 and 1", fake.IssueCount(), fake.CallCount("create"))
	}
}

// TestUndecodableLabeledIssueFailsClosed covers the fail-closed scan: a
// work-item-labeled issue whose body cannot be decoded must make every scan
// inconclusive (WMS_UNAVAILABLE), never an absence a materialize could fill
// with a second issue under the same materialization_key. Previously the
// scan skipped undecodable issues, contradicting the indexed GetIssue loads
// (which fail) and the hydration contract (a failed scan is not an empty
// result).
func TestUndecodableLabeledIssueFailsClosed(t *testing.T) {
	fake := NewFakeClient()
	if _, err := fake.CreateIssue(context.Background(), CreateIssueInput{
		Title:  "corrupt record",
		Body:   "<!-- protobot-wms-v1 -->\n{\"kind\":",
		Labels: []string{LabelWorkItem},
	}); err != nil {
		t.Fatal(err)
	}

	wms := newTestAdapter(t, fake)
	candidate := testWorkItem("wi-corrupt-scan", validation.StateInitial, 0)
	result := wms.Execute(materializeCall(candidate, "corrupt-1", "corrupt-key"))
	if result.OK || result.Error == nil || result.Error.Code != adapter.CodeWMSUnavailable {
		t.Fatalf("materialize over an undecodable labeled issue = %#v, want WMS_UNAVAILABLE", result)
	}
	if fake.IssueCount() != 1 {
		t.Fatalf("issues = %d, want 1 (no new issue during an inconclusive scan)", fake.IssueCount())
	}

	cold := newTestAdapter(t, fake)
	query := cold.Execute(adapter.CallRequest{
		Operation:       string(validation.OperationWorkItemQuery),
		ActorContextRef: "drafting-table",
		Payload:         jsonPayload(t, map[string]any{}),
	})
	if query.OK || query.Error == nil || query.Error.Code != adapter.CodeWMSUnavailable {
		t.Fatalf("work-item query over an undecodable labeled issue = %#v, want WMS_UNAVAILABLE (not an empty set)", query)
	}
}

// TestForeignProjectLabeledIssueIsSkippedNotFatal pins the other half of the
// scan contract: a decodable labeled issue belonging to a different project
// is legitimate multi-project filtering, not a scan failure, and must not
// block this project's materializations.
func TestForeignProjectLabeledIssueIsSkippedNotFatal(t *testing.T) {
	fake := NewFakeClient()
	foreign := testWorkItem("wi-foreign", validation.StateInitial, 0)
	foreign.ProjectID = "other-project"
	body, err := encodeBody("ProtoBot build work-item record.", storedDocument{
		Kind:               kindWorkItem,
		ProjectID:          "other-project",
		WorkItem:           &foreign,
		MaterializationKey: "mat-foreign-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fake.CreateIssue(context.Background(), CreateIssueInput{
		Title:  workItemTitle(foreign),
		Body:   body,
		Labels: []string{LabelWorkItem},
	}); err != nil {
		t.Fatal(err)
	}

	wms := newTestAdapter(t, fake)
	candidate := testWorkItem("wi-shared-repo", validation.StateInitial, 0)
	result := wms.Execute(materializeCall(candidate, "shared-1", "mat-shared-key"))
	if !result.OK || result.Outcome != adapter.OutcomeApplied {
		t.Fatalf("materialize beside a foreign-project record = %#v, want applied", result)
	}
}

// TestMaterializeTransientDependencyLoadReturnsWMSUnavailable covers the
// dependency observation path: a transient failure while loading a durable
// dependency must abort materialization with WMS_UNAVAILABLE, not substitute
// StateInitial and freeze a waiting work item under the materialization key
// (this slice has no refresh-dependencies operation to correct it later).
func TestMaterializeTransientDependencyLoadReturnsWMSUnavailable(t *testing.T) {
	fake := NewFakeClient()
	client := &toggledFailClient{inner: fake}
	wms := newTestAdapterWithClient(t, client)

	// Seed a durable completed dependency directly in the store: this slice
	// cannot produce one through the lifecycle operations it implements.
	dependency := testWorkItem("wi-dep-completed", validation.StateCompleted, 3)
	body, err := encodeBody("ProtoBot build work-item record.", storedDocument{
		Kind:               kindWorkItem,
		ProjectID:          "fixture-project",
		WorkItem:           &dependency,
		MaterializationKey: "mat-dep-completed-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	issue, err := fake.CreateIssue(context.Background(), CreateIssueInput{
		Title:  workItemTitle(dependency),
		Body:   body,
		Labels: []string{LabelWorkItem},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Hydrate so the dependency loads through GetIssue, where the transient
	// failure is injected.
	query := wms.Execute(adapter.CallRequest{
		Operation:       string(validation.OperationWorkItemQuery),
		ActorContextRef: "drafting-table",
		Payload:         jsonPayload(t, map[string]any{}),
	})
	if !query.OK || len(query.Items) != 1 || query.Items[0].ID != dependency.ID {
		t.Fatalf("dependency query = %#v", query)
	}

	candidate := testWorkItem("wi-dep-candidate", validation.StateInitial, 0)
	candidate.Dependencies = []validation.Dependency{{ID: dependency.ID}}

	client.arm(issue.Number)
	result := wms.Execute(materializeCall(candidate, "mat-dep-transient-1", "mat-dep-transient-key"))
	if result.OK || result.Error == nil || result.Error.Code != adapter.CodeWMSUnavailable {
		t.Fatalf("materialize with transient dependency load = %#v, want WMS_UNAVAILABLE (not a missing dependency)", result)
	}
	if fake.IssueCount() != 1 {
		t.Fatalf("issues = %d, want 1 (a mis-observed dependency must not freeze a waiting work item)", fake.IssueCount())
	}

	// Once the load succeeds the completed dependency is observed and the
	// candidate materializes ready-for-building, not waiting.
	retry := wms.Execute(materializeCall(candidate, "mat-dep-transient-2", "mat-dep-transient-key"))
	if !retry.OK || retry.Outcome != adapter.OutcomeApplied {
		t.Fatalf("materialize after dependency recovery = %#v", retry)
	}
	if retry.WorkItemState != validation.StateReadyForBuilding {
		t.Fatalf("materialized state = %q, want ready-for-building (completed dependency observed)", retry.WorkItemState)
	}
}

// loadMaterializationEntryLocked treating every non-transient scan error as
// "no binding": a permanent ListIssues failure is inconclusive, and
// proceeding to CreateIssue could orphan a second work-item issue under the
// same materialization_key. The fix only treats a definitive ErrNotFound as
// a clean no-match; every other scan error rejects WMS_UNAVAILABLE.
func TestMaterializeScanFailureDoesNotCreateDuplicate(t *testing.T) {
	fake := NewFakeClient()
	client := &scanFailOnceAfterClient{inner: fake, skip: 0}
	wms := newTestAdapterWithClient(t, client)
	candidate := testWorkItem("wi-scan-failure", validation.StateInitial, 0)

	result := wms.Execute(materializeCall(candidate, "mat-scan-failure-1", "mat-scan-failure-key"))
	if result.OK {
		t.Fatalf("materialize during scan failure = %#v, want rejection", result)
	}
	if result.Error == nil || result.Error.Code != adapter.CodeWMSUnavailable {
		t.Fatalf("materialize scan failure error = %#v, want WMS_UNAVAILABLE", result.Error)
	}
	if fake.IssueCount() != 0 {
		t.Fatalf("issues after scan failure = %d, want 0 (must not create a duplicate)", fake.IssueCount())
	}

	// The rejection must not be frozen: after the scan recovers, the same
	// call must apply normally.
	retry := wms.Execute(materializeCall(candidate, "mat-scan-failure-2", "mat-scan-failure-key"))
	if !retry.OK || retry.Outcome != adapter.OutcomeApplied {
		t.Fatalf("materialize after scan recovery = %#v, want applied", retry)
	}
	if fake.IssueCount() != 1 {
		t.Fatalf("issues after recovery = %d, want 1", fake.IssueCount())
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
