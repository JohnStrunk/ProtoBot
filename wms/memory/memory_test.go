package memory

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/redhat-et/protobot/wms/validation"
)

var memoryTestTime = time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

func TestConcurrentClaimsOnlyCommitOnce(t *testing.T) {
	gate := StaticGate{
		"job-site-a": testAuthorization("job-site-a", validation.RoleJobSite, validation.OperationClaim),
		"job-site-b": testAuthorization("job-site-b", validation.RoleJobSite, validation.OperationClaim),
	}
	memory := newTestMemory(t, gate, "")
	item := testWorkItem("wi-claim", validation.StateReadyForBuilding, 4)
	if err := memory.SeedWorkItem(item); err != nil {
		t.Fatal(err)
	}
	version := uint64(4)
	results := make(chan Result, 2)
	var group sync.WaitGroup
	for _, actor := range []string{"job-site-a", "job-site-b"} {
		group.Add(1)
		go func(actor string) {
			defer group.Done()
			results <- memory.Execute(CallRequest{
				Operation:               string(validation.OperationClaim),
				ActorContextRef:         actor,
				WorkItemID:              item.ID,
				ExpectedState:           validation.StateReadyForBuilding,
				ExpectedContractVersion: &version,
				IdempotencyKey:          "claim-" + actor,
			})
		}(actor)
	}
	group.Wait()
	close(results)

	var allowed, duplicate int
	for result := range results {
		if result.OK {
			allowed++
			if result.Mutation != mutationApplied {
				t.Errorf("successful claim mutation = %q, want applied", result.Mutation)
			}
			continue
		}
		if result.Error == nil || result.Error.Code != validation.CodeDuplicateClaim {
			t.Errorf("losing claim result = %#v, want DUPLICATE_CLAIM", result)
		}
		if result.Mutation != mutationNone {
			t.Errorf("rejected claim mutation = %q, want none", result.Mutation)
		}
		duplicate++
	}
	if allowed != 1 || duplicate != 1 {
		t.Fatalf("claim results: allowed=%d duplicate=%d, want 1/1", allowed, duplicate)
	}
	stored, ok := memory.WorkItem(item.ID)
	if !ok || stored.State != validation.StateBuilding || stored.ContractVersion != 5 || stored.Lease == nil {
		t.Fatalf("stored work item after concurrent claims = %#v, want one building lease at version 5", stored)
	}
	if len(memory.Events()) != 1 {
		t.Fatalf("audit events = %d, want exactly one accepted claim", len(memory.Events()))
	}
}

func TestStaleLifecycleWriteDoesNotMutateWorkItem(t *testing.T) {
	gate := StaticGate{
		"job-site": testAuthorization("job-site", validation.RoleJobSite, validation.OperationClaim),
	}
	memory := newTestMemory(t, gate, "")
	item := testWorkItem("wi-stale", validation.StateReadyForBuilding, 5)
	if err := memory.SeedWorkItem(item); err != nil {
		t.Fatal(err)
	}
	staleVersion := uint64(4)
	result := memory.Execute(CallRequest{
		Operation:               string(validation.OperationClaim),
		ActorContextRef:         "job-site",
		WorkItemID:              item.ID,
		ExpectedState:           validation.StateReadyForBuilding,
		ExpectedContractVersion: &staleVersion,
		IdempotencyKey:          "stale-claim",
	})
	if result.OK || result.Error == nil || result.Error.Code != validation.CodeStaleContractVersion {
		t.Fatalf("stale write result = %#v, want STALE_CONTRACT_VERSION", result)
	}
	stored, _ := memory.WorkItem(item.ID)
	if stored.State != item.State || stored.ContractVersion != item.ContractVersion || stored.Lease != nil {
		t.Fatalf("stale write mutated work item: %#v", stored)
	}
	if len(memory.Events()) != 0 {
		t.Fatalf("stale write recorded %d lifecycle events, want none", len(memory.Events()))
	}
}

func TestLifecycleTargetVisibilityPrecedesOperationAuthorization(t *testing.T) {
	gate := StaticGate{
		"drafting-table": testAuthorization(
			"drafting-agent",
			validation.RoleDraftingTable,
			validation.OperationLifecyclePreflight,
		),
	}
	memory := newTestMemory(t, gate, "")
	version := uint64(4)
	result := memory.Execute(CallRequest{
		Operation:               string(validation.OperationClaim),
		ActorContextRef:         "drafting-table",
		WorkItemID:              "not-visible",
		ExpectedState:           validation.StateReadyForBuilding,
		ExpectedContractVersion: &version,
		IdempotencyKey:          "missing-target-claim",
	})
	if result.OK || result.Error == nil || result.Error.Code != validation.CodeNotFound || result.Decision == nil {
		t.Fatalf("missing target result = %#v, want a structured NOT_FOUND decision", result)
	}
	if result.Decision.Authority != validation.AuthorityAuthoritative ||
		result.Decision.RuleVersion != validation.RuleVersion ||
		result.Decision.PolicyVersion != "wms-policy/v1" {
		t.Fatalf("missing target decision metadata = %#v", result.Decision)
	}
	if len(memory.Events()) != 0 {
		t.Fatalf("missing target recorded %d lifecycle events, want none", len(memory.Events()))
	}
}

func TestMaterializationReplaysOriginalResultAndRejectsSourceConflict(t *testing.T) {
	gate := StaticGate{
		"materializer": testAuthorization("materializer", validation.RoleMaterializer, validation.OperationMaterialize),
	}
	memory := newTestMemory(t, gate, "materializer")
	candidate := testWorkItem("wi-materialize", validation.StateInitial, 0)
	candidate.ChangeType = "undefined"
	call := materializeCall(candidate, "materialize-command-1", "logical-work-1")

	first := memory.Execute(call)
	if !first.OK || first.Outcome != outcomeApplied || first.ContractVersion != 1 || first.WorkItemState != validation.StateReadyForBuilding {
		t.Fatalf("materialization result = %#v, want applied ready item at version 1", first)
	}
	firstItem, _ := memory.WorkItem(candidate.ID)

	replay := memory.Execute(call)
	if !replay.OK || replay.Outcome != outcomeReplayed || replay.Mutation != mutationNone || replay.Decision == nil || !replay.Decision.Replayed {
		t.Fatalf("same-key materialization replay = %#v, want original result replay", replay)
	}
	if replay.WorkItemState != first.WorkItemState || replay.ContractVersion != first.ContractVersion {
		t.Fatalf("replay result = (%q, %d), want original (%q, %d)", replay.WorkItemState, replay.ContractVersion, first.WorkItemState, first.ContractVersion)
	}

	newCommand := materializeCall(candidate, "materialize-command-2", "logical-work-1")
	logicalReplay := memory.Execute(newCommand)
	if !logicalReplay.OK || logicalReplay.Outcome != outcomeReplayed || logicalReplay.Decision == nil || !logicalReplay.Decision.Replayed {
		t.Fatalf("same-source materialization replay = %#v, want create-or-return replay", logicalReplay)
	}
	if len(memory.Events()) != 1 {
		t.Fatalf("materialization replays recorded %d events, want one", len(memory.Events()))
	}

	changedSource := candidate
	changedSource.Readiness.PolicyCompatible = false
	conflict := memory.Execute(materializeCall(changedSource, "materialize-command-3", "logical-work-1"))
	if conflict.OK || conflict.Error == nil || conflict.Error.Code != validation.CodeIdempotencyConflict || conflict.Mutation != mutationNone {
		t.Fatalf("different source reuse = %#v, want mutation-free IDEMPOTENCY_CONFLICT", conflict)
	}
	after, _ := memory.WorkItem(candidate.ID)
	if after.ContractVersion != firstItem.ContractVersion || after.State != firstItem.State || len(memory.Events()) != 1 {
		t.Fatalf("source conflict mutated the materialized item: %#v", after)
	}
}

func TestCompletionReplayReturnsOriginalResult(t *testing.T) {
	gate := StaticGate{
		"job-site": testAuthorization("job-site", validation.RoleJobSite, validation.OperationRecordMerge),
	}
	memory := newTestMemory(t, gate, "")
	expected := &validation.MergeEnvelope{
		ProductTreeDigest: "tree-digest",
		InspectionRunID:   "inspection-1",
		IntegrationHead:   "integration-head",
		Target:            "main",
		ContractVersion:   9,
	}
	item := testWorkItem("wi-complete", validation.StateMerging, 9)
	item.ExpectedMerge = expected
	item.InspectionRunSealed = true
	item.FindingsTerminal = true
	item.FinalTestsPassed = true
	item.Owner = "job-site"
	item.Lease = &validation.Lease{Owner: "job-site", FencingToken: "fence-9", ExpiresAt: memoryTestTime.Add(time.Hour)}
	if err := memory.SeedWorkItem(item); err != nil {
		t.Fatal(err)
	}
	version := uint64(9)
	merged := *expected
	merged.MergeCommit = "merge-commit-1"
	call := CallRequest{
		Operation:               string(validation.OperationRecordMerge),
		ActorContextRef:         "job-site",
		WorkItemID:              item.ID,
		ExpectedState:           validation.StateMerging,
		ExpectedContractVersion: &version,
		FencingToken:            "fence-9",
		IdempotencyKey:          "complete-1",
		Payload:                 jsonPayload(t, validation.Payload{MergeEnvelope: &merged}),
	}

	first := memory.Execute(call)
	if !first.OK || first.Outcome != outcomeApplied || first.WorkItemState != validation.StateCompleted || first.ContractVersion != 10 {
		t.Fatalf("completion result = %#v, want completed version 10", first)
	}
	replay := memory.Execute(call)
	if !replay.OK || replay.Outcome != outcomeReplayed || replay.Mutation != mutationNone || replay.Decision == nil || !replay.Decision.Replayed {
		t.Fatalf("completion replay = %#v, want original completion result", replay)
	}
	if replay.WorkItemState != first.WorkItemState || replay.ContractVersion != first.ContractVersion {
		t.Fatalf("completion replay = (%q, %d), want original (%q, %d)", replay.WorkItemState, replay.ContractVersion, first.WorkItemState, first.ContractVersion)
	}
	if len(memory.Events()) != 1 {
		t.Fatalf("completion replay recorded %d events, want one", len(memory.Events()))
	}
}

func TestDraftingTableOperationSetIsDisjointFromJobSiteExecution(t *testing.T) {
	drafting := stringSet(DraftingTableOperations())
	adapter := stringSet(AdapterOperations())
	jobSite := stringSet(JobSiteOperations())
	if len(drafting) == 0 || len(drafting) >= len(adapter) {
		t.Fatalf("Drafting Table set size=%d adapter set size=%d, want a proper subset", len(drafting), len(adapter))
	}
	for operation := range drafting {
		if _, exists := adapter[operation]; !exists {
			t.Errorf("Drafting Table operation %q is absent from the adapter API", operation)
		}
		if _, exists := jobSite[operation]; exists {
			t.Errorf("Drafting Table operation %q overlaps Job Site execution", operation)
		}
	}
	if _, exists := drafting[string(validation.OperationClaim)]; exists {
		t.Fatal("Drafting Table operation set contains claim")
	}
	if _, exists := drafting["finding.create"]; exists {
		t.Fatal("Drafting Table operation set contains Finding Ledger mutation")
	}
}

func newTestMemory(t *testing.T, gate Gate, materializer string) *Memory {
	t.Helper()
	memory, err := New(Config{
		ProjectID:           "fixture-project",
		Gate:                gate,
		Now:                 func() time.Time { return memoryTestTime },
		LeaseDuration:       15 * time.Minute,
		MaterializerSubject: materializer,
	})
	if err != nil {
		t.Fatal(err)
	}
	return memory
}

func testAuthorization(subject string, role validation.Role, operations ...validation.Operation) validation.AuthorizationContext {
	return validation.AuthorizationContext{
		Subject:        subject,
		Role:           role,
		ProjectID:      "fixture-project",
		AllowedActions: append([]validation.Operation(nil), operations...),
		AllowedRefs:    []string{"project:fixture-project"},
		ExpiresAt:      memoryTestTime.Add(time.Hour),
		PolicyVersion:  "wms-policy/v1",
	}
}

func testWorkItem(id string, state validation.State, version uint64) validation.WorkItem {
	return validation.WorkItem{
		ID:                     id,
		ProjectID:              "fixture-project",
		State:                  state,
		ContractVersion:        version,
		ImplementationRequired: true,
		Readiness: validation.Readiness{
			ContractComplete:           true,
			SourceImmutable:            true,
			SpecificationValidated:     true,
			RequirementReferencesValid: true,
			PipelineEntrySatisfied:     true,
			ImpactDispositioned:        true,
			PolicyCompatible:           true,
		},
	}
}

func materializeCall(item validation.WorkItem, idempotencyKey, materializationKey string) CallRequest {
	version := uint64(0)
	return CallRequest{
		Operation:               string(validation.OperationMaterialize),
		ActorContextRef:         "materializer",
		ExpectedState:           validation.StateInitial,
		ExpectedContractVersion: &version,
		IdempotencyKey:          idempotencyKey,
		MaterializationKey:      materializationKey,
		Payload:                 jsonPayloadWithoutError(validation.Payload{WorkItem: &item}),
	}
}

func jsonPayload(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func jsonPayloadWithoutError(value any) []byte {
	encoded, _ := json.Marshal(value)
	return encoded
}

func stringSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}
