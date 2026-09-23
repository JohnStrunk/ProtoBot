package memory

import (
	"reflect"
	"testing"
	"time"

	"github.com/redhat-et/protobot/wms/validation"
)

func TestValidationRulesConformanceMatrix(t *testing.T) {
	tests := []struct {
		id  string
		run func(*testing.T)
	}{
		{"VR-001", func(t *testing.T) {
			memory, _ := newConformanceMemory(t)
			item := testWorkItem("wi-001", validation.StateReadyForBuilding, 4)
			seedConformanceItem(t, memory, item)

			result := memory.Execute(conformanceCall(validation.OperationClaim, "job-site", item, "vr001-claim"))
			decision := assertAllowedDecision(t, result, validation.AuthorityAuthoritative)
			if decision.After.State != validation.StateBuilding || decision.After.ContractVersion != 5 || decision.FencingTokenIssued == "" {
				t.Fatalf("claim decision = %#v, want building v5 and a new fence", decision)
			}
			assertEventCount(t, memory, 1)
		}},
		{"VR-002", func(t *testing.T) {
			memory, _ := newConformanceMemory(t)
			item := testWorkItem("wi-002", validation.StateReadyForBuilding, 4)
			seedConformanceItem(t, memory, item)
			version := item.ContractVersion
			preflight := memory.Execute(CallRequest{
				Operation:               "lifecycle.preflight",
				ActorContextRef:         "job-site-a",
				WorkItemID:              item.ID,
				ExpectedState:           item.State,
				ExpectedContractVersion: &version,
				Payload:                 jsonPayload(t, map[string]any{"operation": "claim"}),
			})
			assertPreflightDecision(t, preflight, validation.OutcomeAllowed)
			if len(memory.Events()) != 0 {
				t.Fatal("preflight mutated WMS state")
			}

			winner := memory.Execute(conformanceCall(validation.OperationClaim, "job-site-b", item, "vr002-winner"))
			assertAllowedDecision(t, winner, validation.AuthorityAuthoritative)
			loser := memory.Execute(conformanceCall(validation.OperationClaim, "job-site-a", item, "vr002-loser"))
			assertRejectedDecision(t, loser, validation.AuthorityAuthoritative, validation.CodeDuplicateClaim)
			assertEventCount(t, memory, 1)
		}},
		{"VR-003", func(t *testing.T) {
			memory, _ := newConformanceMemory(t)
			item := testWorkItem("wi-003", validation.StateBlocked, 4)
			seedConformanceItem(t, memory, item)
			call := conformanceCall(validation.OperationClaim, "job-site", item, "vr003-stale-state")
			call.ExpectedState = validation.StateReadyForBuilding
			result := memory.Execute(call)
			assertRejectedDecision(t, result, validation.AuthorityAuthoritative, validation.CodeStaleState)
			assertItemUnchanged(t, memory, item)
			assertEventCount(t, memory, 0)
		}},
		{"VR-004", func(t *testing.T) {
			memory, _ := newConformanceMemory(t)
			item := testWorkItem("wi-004", validation.StateReadyForBuilding, 5)
			seedConformanceItem(t, memory, item)
			call := conformanceCall(validation.OperationClaim, "job-site", item, "vr004-stale-version")
			staleVersion := uint64(4)
			call.ExpectedContractVersion = &staleVersion
			result := memory.Execute(call)
			assertRejectedDecision(t, result, validation.AuthorityAuthoritative, validation.CodeStaleContractVersion)
			assertItemUnchanged(t, memory, item)
			assertEventCount(t, memory, 0)
		}},
		{"VR-005", func(t *testing.T) {
			memory, _ := newConformanceMemory(t)
			item := testWorkItem("wi-005", validation.StateReadyForBuilding, 4)
			seedConformanceItem(t, memory, item)
			first := memory.Execute(conformanceCall(validation.OperationClaim, "job-site-a", item, "vr005-a"))
			assertAllowedDecision(t, first, validation.AuthorityAuthoritative)
			second := memory.Execute(conformanceCall(validation.OperationClaim, "job-site-b", item, "vr005-b"))
			assertRejectedDecision(t, second, validation.AuthorityAuthoritative, validation.CodeDuplicateClaim)
			stored, _ := memory.WorkItem(item.ID)
			if stored.Lease == nil || stored.Lease.Owner != "job-site-a" || stored.ContractVersion != 5 {
				t.Fatalf("claim winner record = %#v, want original owner at version 5", stored)
			}
			assertEventCount(t, memory, 1)
		}},
		{"VR-006", func(t *testing.T) {
			memory, _ := newConformanceMemory(t)
			ready := testWorkItem("wi-006-claim", validation.StateReadyForBuilding, 4)
			merging := testWorkItem("wi-006-merge", validation.StateMerging, 4)
			seedConformanceItem(t, memory, ready)
			seedConformanceItem(t, memory, merging)
			claim := memory.Execute(conformanceCall(validation.OperationClaim, "drafting-table", ready, "vr006-claim"))
			assertRejectedDecision(t, claim, validation.AuthorityAuthoritative, validation.CodeUnauthorizedAction)
			merge := memory.Execute(conformanceCall(validation.OperationRecordMerge, "drafting-table", merging, "vr006-record-merge"))
			assertRejectedDecision(t, merge, validation.AuthorityAuthoritative, validation.CodeUnauthorizedAction)
			assertItemUnchanged(t, memory, ready)
			assertItemUnchanged(t, memory, merging)
			assertEventCount(t, memory, 0)
		}},
		{"VR-007", func(t *testing.T) {
			memory, _ := newConformanceMemory(t)
			item := testWorkItem("wi-007", validation.StateBuilding, 4)
			setConformanceLease(&item, "job-site", "fence-current", memoryTestTime.Add(time.Hour))
			seedConformanceItem(t, memory, item)
			call := conformanceCall(validation.OperationRenewLease, "job-site", item, "vr007-stale-fence")
			call.FencingToken = "fence-old"
			result := memory.Execute(call)
			assertRejectedDecision(t, result, validation.AuthorityAuthoritative, validation.CodeStaleFencingToken)
			assertItemUnchanged(t, memory, item)
			assertEventCount(t, memory, 0)
		}},
		{"VR-008", func(t *testing.T) {
			memory, _ := newConformanceMemory(t)
			item := testWorkItem("wi-008", validation.StateReadyForBuilding, 4)
			seedConformanceItem(t, memory, item)
			call := conformanceCall(validation.OperationClaim, "job-site", item, "vr008-claim")
			first := memory.Execute(call)
			firstDecision := assertAllowedDecision(t, first, validation.AuthorityAuthoritative)
			replay := memory.Execute(call)
			assertReplayedDecision(t, replay, validation.AuthorityAuthoritative)
			if !reflect.DeepEqual(firstDecision.After, replay.Decision.After) {
				t.Fatalf("replay after = %#v, original after = %#v", replay.Decision.After, firstDecision.After)
			}
			assertEventCount(t, memory, 1)
		}},
		{"VR-009", func(t *testing.T) {
			memory, _ := newConformanceMemory(t)
			firstItem := testWorkItem("wi-009-a", validation.StateReadyForBuilding, 4)
			secondItem := testWorkItem("wi-009-b", validation.StateReadyForBuilding, 4)
			seedConformanceItem(t, memory, firstItem)
			seedConformanceItem(t, memory, secondItem)
			firstCall := conformanceCall(validation.OperationClaim, "job-site", firstItem, "vr009-shared-key")
			first := memory.Execute(firstCall)
			assertAllowedDecision(t, first, validation.AuthorityAuthoritative)
			secondCall := conformanceCall(validation.OperationClaim, "job-site", secondItem, "vr009-shared-key")
			second := memory.Execute(secondCall)
			assertRejectedDecision(t, second, validation.AuthorityAuthoritative, validation.CodeIdempotencyConflict)
			assertItemUnchanged(t, memory, secondItem)
			assertEventCount(t, memory, 1)
		}},
		{"VR-010", func(t *testing.T) {
			memory, _ := newConformanceMemory(t)
			item := testWorkItem("wi-010", validation.StateReadyForBuilding, 4)
			seedConformanceItem(t, memory, item)
			first := memory.Execute(conformanceCall(validation.OperationClaim, "job-site-a", item, "vr010-shared-key"))
			assertAllowedDecision(t, first, validation.AuthorityAuthoritative)
			second := memory.Execute(conformanceCall(validation.OperationClaim, "job-site-b", item, "vr010-shared-key"))
			assertRejectedDecision(t, second, validation.AuthorityAuthoritative, validation.CodeIdempotencyConflict)
			assertEventCount(t, memory, 1)
		}},
		{"VR-011", func(t *testing.T) {
			memory, _ := newConformanceMemory(t)
			item := testWorkItem("wi-011", validation.StateBlocked, 4)
			seedConformanceItem(t, memory, item)
			result := memory.Execute(conformanceCall(validation.OperationClaim, "job-site", item, "vr011-blocked-claim"))
			assertRejectedDecision(t, result, validation.AuthorityAuthoritative, validation.CodeInvalidTransition)
			assertItemUnchanged(t, memory, item)
			assertEventCount(t, memory, 0)
		}},
		{"VR-012", func(t *testing.T) {
			memory, _ := newConformanceMemory(t)
			item := testWorkItem("wi-012", validation.StateWaiting, 4)
			seedConformanceItem(t, memory, item)
			call := conformanceCall(validation.OperationClaim, "job-site", item, "vr012-rejected-claim")
			first := memory.Execute(call)
			assertRejectedDecision(t, first, validation.AuthorityAuthoritative, validation.CodeInvalidTransition)
			replay := memory.Execute(call)
			assertReplayedDecision(t, replay, validation.AuthorityAuthoritative)
			if replay.Error == nil || replay.Error.Code != validation.CodeInvalidTransition {
				t.Fatalf("rejected replay error = %#v, want INVALID_TRANSITION", replay.Error)
			}
			assertItemUnchanged(t, memory, item)
			assertEventCount(t, memory, 0)
		}},
		{"VR-013", func(t *testing.T) {
			memory, _ := newConformanceMemory(t)
			candidate := testWorkItem("wi-013", validation.StateInitial, 0)
			candidate.ChangeType = "undefined"
			candidate.Readiness.ImpactDispositioned = false
			result := memory.Execute(materializeCall(candidate, "vr013-materialize", "vr013-key"))
			decision := assertAllowedDecision(t, result, validation.AuthorityAuthoritative)
			if decision.After.State != validation.StateBlocked || decision.After.ContractVersion != 1 {
				t.Fatalf("materialized decision = %#v, want blocked v1", decision)
			}
			stored, exists := memory.WorkItem(candidate.ID)
			if !exists || stored.State != validation.StateBlocked {
				t.Fatalf("stored blocked work item = %#v, exists=%t", stored, exists)
			}
			assertEventCount(t, memory, 1)
		}},
		{"VR-013B", func(t *testing.T) {
			memory, _ := newConformanceMemory(t)
			candidate := testWorkItem("wi-013b", validation.StateInitial, 0)
			candidate.ChangeType = "changes"
			candidate.Readiness.SourceImmutable = false
			result := memory.Execute(materializeCall(candidate, "vr013b-materialize", "vr013b-key"))
			assertRejectedDecision(t, result, validation.AuthorityAuthoritative, validation.CodePreconditionFailed)
			if _, exists := memory.WorkItem(candidate.ID); exists {
				t.Fatal("incomplete materialization created a work item")
			}
			assertEventCount(t, memory, 0)
		}},
		{"VR-014", func(t *testing.T) {
			memory, _ := newConformanceMemory(t)
			dependency := testWorkItem("wi-014-dependency", validation.StateMerging, 9)
			expectedMerge := &validation.MergeEnvelope{
				ProductTreeDigest: "tree-014",
				InspectionRunID:   "inspection-014",
				IntegrationHead:   "integration-014",
				Target:            "main",
				ContractVersion:   9,
			}
			dependency.ExpectedMerge = expectedMerge
			setConformanceLease(&dependency, "job-site", "fence-014", memoryTestTime.Add(time.Hour))
			seedConformanceItem(t, memory, dependency)

			candidate := testWorkItem("wi-014-target", validation.StateInitial, 0)
			candidate.ChangeType = "undefined"
			candidate.Dependencies = []validation.Dependency{{ID: dependency.ID, State: validation.StateWaiting}}
			materialized := memory.Execute(materializeCall(candidate, "vr014-materialize", "vr014-key"))
			materializedDecision := assertAllowedDecision(t, materialized, validation.AuthorityAuthoritative)
			if materializedDecision.After.State != validation.StateWaiting {
				t.Fatalf("initial materialization state = %q, want waiting", materializedDecision.After.State)
			}

			merge := *expectedMerge
			merge.MergeCommit = "merge-014"
			completeDependency := conformanceCall(validation.OperationRecordMerge, "job-site", dependency, "vr014-complete-dependency")
			completeDependency.FencingToken = "fence-014"
			completeDependency.Payload = jsonPayload(t, validation.Payload{MergeEnvelope: &merge})
			completed := memory.Execute(completeDependency)
			assertAllowedDecision(t, completed, validation.AuthorityAuthoritative)

			// A dependency refresh is WMS-observed record state, not a separate
			// lifecycle mutation on the item that is waiting for that dependency.
			stored, _ := memory.WorkItem(candidate.ID)
			stored.Dependencies[0].State = validation.StateCompleted
			replaceObservedWorkItem(memory, stored)
			refresh := conformanceCall(validation.OperationRefreshDependencies, "materializer", stored, "vr014-refresh")
			refreshed := memory.Execute(refresh)
			decision := assertAllowedDecision(t, refreshed, validation.AuthorityAuthoritative)
			if decision.After.State != validation.StateReadyForBuilding || decision.After.ContractVersion != 2 {
				t.Fatalf("dependency refresh decision = %#v, want ready v2", decision)
			}
			assertEventCount(t, memory, 3)
		}},
		{"VR-015", func(t *testing.T) {
			memory, _ := newConformanceMemory(t)
			item := testWorkItem("wi-015", validation.StateBuilding, 4)
			setConformanceLease(&item, "job-site", "fence-015", memoryTestTime.Add(time.Hour))
			seedConformanceItem(t, memory, item)
			testsPass := conformanceCall(validation.OperationTestsPass, "job-site", item, "vr015-tests-pass")
			testsPass.FencingToken = "fence-015"
			testsPass.Payload = jsonPayload(t, validation.Payload{BuildTestsPassed: true})
			passed := memory.Execute(testsPass)
			decision := assertAllowedDecision(t, passed, validation.AuthorityAuthoritative)
			if decision.After.State != validation.StateInspecting {
				t.Fatalf("tests-pass after = %#v, want inspecting", decision.After)
			}

			inspecting, _ := memory.WorkItem(item.ID)
			inspecting.InspectionRunSealed = true
			inspecting.FindingsTerminal = true
			inspecting.FinalTestsPassed = true
			replaceObservedWorkItem(memory, inspecting)
			beginMerge := conformanceCall(validation.OperationBeginMerge, "job-site", inspecting, "vr015-begin-merge")
			beginMerge.FencingToken = "fence-015"
			merged := memory.Execute(beginMerge)
			mergeDecision := assertAllowedDecision(t, merged, validation.AuthorityAuthoritative)
			if mergeDecision.After.State != validation.StateMerging {
				t.Fatalf("begin-merge after = %#v, want merging", mergeDecision.After)
			}
			assertEventCount(t, memory, 2)
		}},
		{"VR-016", func(t *testing.T) {
			memory, _ := newConformanceMemory(t)
			item := testWorkItem("wi-016", validation.StateMerging, 9)
			setConformanceLease(&item, "job-site", "fence-016", memoryTestTime.Add(time.Hour))
			item.Reconciliation = validation.ReconciliationEvidence{Status: "conflict", GitMutation: "conflict"}
			seedConformanceItem(t, memory, item)
			call := conformanceCall(validation.OperationMergeConflict, "job-site", item, "vr016-conflict")
			call.FencingToken = "fence-016"
			result := memory.Execute(call)
			decision := assertAllowedDecision(t, result, validation.AuthorityAuthoritative)
			if decision.After.State != validation.StateBuilding || decision.FencingTokenIssued == "" {
				t.Fatalf("merge-conflict decision = %#v, want new building lease", decision)
			}
			stored, _ := memory.WorkItem(item.ID)
			if stored.Lease == nil || stored.Lease.FencingToken == "fence-016" {
				t.Fatalf("merge-conflict did not rotate the fence: %#v", stored.Lease)
			}
			assertEventCount(t, memory, 1)
		}},
		{"VR-017", func(t *testing.T) {
			memory, _ := newConformanceMemory(t)
			item := testWorkItem("wi-017", validation.StateMerging, 9)
			item.Reconciliation = validation.ReconciliationEvidence{Status: "not-applied", GitMutation: "none"}
			seedConformanceItem(t, memory, item)
			result := memory.Execute(conformanceCall(validation.OperationMergeNotApplied, "reconciler", item, "vr017-not-applied"))
			decision := assertAllowedDecision(t, result, validation.AuthorityAuthoritative)
			if decision.After.State != validation.StateReadyForBuilding {
				t.Fatalf("reconciled not-applied state = %q, want ready-for-building", decision.After.State)
			}
			assertEventCount(t, memory, 1)
		}},
		{"VR-018", func(t *testing.T) {
			memory, _ := newConformanceMemory(t)
			item := testWorkItem("wi-018", validation.StateMerging, 9)
			item.ExpectedMerge = &validation.MergeEnvelope{
				ProductTreeDigest: "tree-018",
				InspectionRunID:   "inspection-018",
				IntegrationHead:   "integration-018",
				Target:            "main",
				ContractVersion:   9,
			}
			setConformanceLease(&item, "job-site", "fence-018", memoryTestTime.Add(time.Hour))
			seedConformanceItem(t, memory, item)
			merged := *item.ExpectedMerge
			merged.MergeCommit = "merge-018"
			call := conformanceCall(validation.OperationRecordMerge, "job-site", item, "vr018-record-merge")
			call.FencingToken = "fence-018"
			call.Payload = jsonPayload(t, validation.Payload{MergeEnvelope: &merged})
			first := memory.Execute(call)
			firstDecision := assertAllowedDecision(t, first, validation.AuthorityAuthoritative)
			replay := memory.Execute(call)
			assertReplayedDecision(t, replay, validation.AuthorityAuthoritative)
			if !reflect.DeepEqual(firstDecision.After, replay.Decision.After) || replay.WorkItemState != first.WorkItemState {
				t.Fatalf("completion replay = %#v, original result = %#v", replay, first)
			}
			assertEventCount(t, memory, 1)
		}},
		{"VR-019", func(t *testing.T) {
			for _, state := range []validation.State{validation.StateCompleted, validation.StateAbandoned} {
				t.Run(string(state), func(t *testing.T) {
					memory, _ := newConformanceMemory(t)
					item := testWorkItem("wi-019-"+string(state), state, 9)
					seedConformanceItem(t, memory, item)
					result := memory.Execute(conformanceCall(validation.OperationClaim, "job-site", item, "vr019-"+string(state)))
					assertRejectedDecision(t, result, validation.AuthorityAuthoritative, validation.CodeAlreadyTerminal)
					assertItemUnchanged(t, memory, item)
					assertEventCount(t, memory, 0)
				})
			}
		}},
		{"VR-020", func(t *testing.T) {
			memory, _ := newConformanceMemory(t)
			item := testWorkItem("wi-020", validation.StateBuilding, 4)
			setConformanceLease(&item, "job-site", "fence-020-old", memoryTestTime.Add(time.Hour))
			seedConformanceItem(t, memory, item)
			refresh := conformanceCall(validation.OperationRefreshActive, "job-site", item, "vr020-refresh")
			refresh.FencingToken = "fence-020-old"
			result := memory.Execute(refresh)
			decision := assertAllowedDecision(t, result, validation.AuthorityAuthoritative)
			if decision.After.State != validation.StateBuilding || decision.After.ContractVersion != 5 || decision.FencingTokenIssued == "" {
				t.Fatalf("active refresh decision = %#v, want building v5 and replacement fence", decision)
			}
			refreshed, _ := memory.WorkItem(item.ID)
			if refreshed.Lease == nil || refreshed.Lease.FencingToken == "fence-020-old" {
				t.Fatalf("active refresh retained old fence: %#v", refreshed.Lease)
			}
			stale := conformanceCall(validation.OperationRenewLease, "job-site", refreshed, "vr020-old-fence")
			stale.FencingToken = "fence-020-old"
			staleResult := memory.Execute(stale)
			assertRejectedDecision(t, staleResult, validation.AuthorityAuthoritative, validation.CodeStaleFencingToken)
			assertEventCount(t, memory, 1)
		}},
		{"VR-021", func(t *testing.T) {
			memory, _ := newConformanceMemory(t)
			item := testWorkItem("wi-021", validation.StateInspecting, 4)
			setConformanceLease(&item, "job-site", "fence-021-old", memoryTestTime.Add(time.Hour))
			seedConformanceItem(t, memory, item)
			returnToBuilding := conformanceCall(validation.OperationReturnToBuilding, "job-site", item, "vr021-rework")
			returnToBuilding.FencingToken = "fence-021-old"
			returnToBuilding.Payload = jsonPayload(t, validation.Payload{InContractDefect: true})
			result := memory.Execute(returnToBuilding)
			decision := assertAllowedDecision(t, result, validation.AuthorityAuthoritative)
			if decision.After.State != validation.StateBuilding || decision.FencingTokenIssued == "" {
				t.Fatalf("return-to-building decision = %#v, want a new building fence", decision)
			}
			returned, _ := memory.WorkItem(item.ID)
			stale := conformanceCall(validation.OperationRenewLease, "job-site", returned, "vr021-old-fence")
			stale.FencingToken = "fence-021-old"
			staleResult := memory.Execute(stale)
			assertRejectedDecision(t, staleResult, validation.AuthorityAuthoritative, validation.CodeStaleFencingToken)
			assertEventCount(t, memory, 1)
		}},
		{"VR-022", func(t *testing.T) {
			for _, test := range []struct {
				name       string
				approvalID string
			}{
				{name: "missing approval"},
				{name: "forged approval", approvalID: "forged-approval"},
			} {
				t.Run(test.name, func(t *testing.T) {
					memory, _ := newConformanceMemory(t)
					item := testWorkItem("wi-022", validation.StateBlocked, 7)
					seedConformanceItem(t, memory, item)
					version := item.ContractVersion
					result := memory.Execute(CallRequest{
						Operation:               "blocked-work.submit-resolution",
						ActorContextRef:         "drafting-table",
						WorkItemID:              item.ID,
						ExpectedState:           validation.StateBlocked,
						ExpectedContractVersion: &version,
						HumanApprovalID:         test.approvalID,
						IdempotencyKey:          "vr022-" + test.name,
						Payload: jsonPayload(t, blockedResolutionPayload{
							ResolutionKind:           "out-of-scope",
							ApprovalResolutionDigest: "vr022-digest",
						}),
					})
					assertRequestRejection(t, result, validation.CodeUnauthorizedAction)
					assertItemUnchanged(t, memory, item)
					assertEventCount(t, memory, 0)
				})
			}
		}},
		{"VR-023", func(t *testing.T) {
			memory, _ := newConformanceMemory(t)
			item := testWorkItem("wi-023", validation.StateBlocked, 7)
			seedConformanceItem(t, memory, item)
			seedCompletedDependencyAndChangeSet(t, memory, "vr023-dependency", "CS-023")
			approval := conformanceApproval(item, "vr023-approval", "human-023", "materializer-1", "add-requirement")
			if err := memory.SeedApproval(approval); err != nil {
				t.Fatal(err)
			}
			submitted := submitConformanceResolution(t, memory, item, approval.ID, approval.Digest, "CS-023", "vr023-submit")
			if !submitted.OK || submitted.Outcome != outcomeApplied || submitted.Submission != "accepted" {
				t.Fatalf("resolution submission = %#v, want accepted", submitted)
			}
			resolve := conformanceResolveCall(t, item, approval.ID, submitted.ResolutionSubmissionID, approval.Digest, "vr023-resolve")
			resolved := memory.Execute(resolve)
			decision := assertAllowedDecision(t, resolved, validation.AuthorityAuthoritative)
			if decision.After.State != validation.StateReadyForBuilding || decision.After.ContractVersion != 8 || resolved.ApprovalStatus != "consumed" {
				t.Fatalf("resolve-block result = %#v, want ready v8 and consumed approval", resolved)
			}
			events := memory.Events()
			if len(events) != 2 || events[1].AuthorizedHumanSubject != "human-023" {
				t.Fatalf("resolution audit events = %#v, want approval subject preserved before consumption", events)
			}
		}},
		{"VR-024", func(t *testing.T) {
			memory, _ := newConformanceMemory(t)
			candidate := testWorkItem("wi-024", validation.StateInitial, 0)
			candidate.ChangeType = "undefined"
			first := memory.Execute(materializeCall(candidate, "vr024-command-1", "vr024-logical-key"))
			assertAllowedDecision(t, first, validation.AuthorityAuthoritative)
			replay := memory.Execute(materializeCall(candidate, "vr024-command-2", "vr024-logical-key"))
			assertReplayedDecision(t, replay, validation.AuthorityAuthoritative)
			if replay.WorkItemID != first.WorkItemID || replay.ContractVersion != first.ContractVersion {
				t.Fatalf("materialization replay = %#v, original = %#v", replay, first)
			}
			assertEventCount(t, memory, 1)
		}},
		{"VR-025", func(t *testing.T) {
			memory, _ := newConformanceMemory(t)
			candidate := testWorkItem("wi-025", validation.StateInitial, 0)
			candidate.ChangeType = "undefined"
			first := memory.Execute(materializeCall(candidate, "vr025-command-1", "vr025-logical-key"))
			assertAllowedDecision(t, first, validation.AuthorityAuthoritative)
			changed := candidate
			changed.Readiness.PolicyCompatible = false
			conflict := memory.Execute(materializeCall(changed, "vr025-command-2", "vr025-logical-key"))
			assertRejectedDecision(t, conflict, validation.AuthorityAuthoritative, validation.CodeIdempotencyConflict)
			stored, _ := memory.WorkItem(candidate.ID)
			if stored.State != first.WorkItemState || stored.ContractVersion != first.ContractVersion {
				t.Fatalf("materialization source conflict changed item: %#v", stored)
			}
			assertEventCount(t, memory, 1)
		}},
		{"VR-026", func(t *testing.T) {
			memory, _ := newConformanceMemory(t)
			item := testWorkItem("wi-026", validation.StateBuilding, 9)
			setConformanceLease(&item, "job-site-old", "fence-expired", memoryTestTime.Add(-time.Second))
			item.Reconciliation = validation.ReconciliationEvidence{Status: "lease-recovered", GitMutation: "none"}
			seedConformanceItem(t, memory, item)
			result := memory.Execute(conformanceCall(validation.OperationRecoverLease, "reconciler", item, "vr026-recover"))
			decision := assertAllowedDecision(t, result, validation.AuthorityAuthoritative)
			if decision.After.State != validation.StateReadyForBuilding || decision.FencingTokenIssued != "" {
				t.Fatalf("recover-lease decision = %#v, want ready without issuing a lease", decision)
			}
			stored, _ := memory.WorkItem(item.ID)
			if stored.Lease != nil {
				t.Fatalf("recovery retained expired lease: %#v", stored.Lease)
			}
			assertEventCount(t, memory, 1)
		}},
		{"VR-027", func(t *testing.T) {
			memory, _ := newConformanceMemory(t)
			item := testWorkItem("wi-027", validation.StateBuilding, 9)
			setConformanceLease(&item, "job-site", "fence-027", memoryTestTime.Add(time.Hour))
			item.Reconciliation = validation.ReconciliationEvidence{Status: "not-integrated", GitMutation: "none"}
			seedConformanceItem(t, memory, item)
			call := conformanceCall(validation.OperationAbandon, "human-maintainer", item, "vr027-abandon")
			call.Payload = jsonPayload(t, validation.Payload{CancellationConfirmed: true})
			result := memory.Execute(call)
			decision := assertAllowedDecision(t, result, validation.AuthorityAuthoritative)
			if decision.After.State != validation.StateAbandoned || decision.FencingTokenIssued != "" {
				t.Fatalf("abandon decision = %#v, want abandoned without a lease", decision)
			}
			assertEventCount(t, memory, 1)
		}},
		{"VR-028", func(t *testing.T) {
			for _, test := range []struct {
				name   string
				change func(*validation.AuthorizationContext)
			}{
				{name: "missing subject", change: func(auth *validation.AuthorizationContext) { auth.Subject = "" }},
				{name: "unknown role", change: func(auth *validation.AuthorizationContext) { auth.Role = "worker" }},
				{name: "expired context", change: func(auth *validation.AuthorizationContext) { auth.ExpiresAt = memoryTestTime }},
			} {
				t.Run(test.name, func(t *testing.T) {
					gate := conformanceGate()
					authorization := gate["job-site"]
					test.change(&authorization)
					gate["invalid-context"] = authorization
					memory := newConformanceMemoryWithGate(t, gate)
					item := testWorkItem("wi-028", validation.StateReadyForBuilding, 4)
					seedConformanceItem(t, memory, item)
					result := memory.Execute(conformanceCall(validation.OperationClaim, "invalid-context", item, "vr028-"+test.name))
					assertRejectedDecision(t, result, validation.AuthorityAuthoritative, validation.CodeUnauthorizedAction)
					assertItemUnchanged(t, memory, item)
					assertEventCount(t, memory, 0)
				})
			}
		}},
		{"VR-029", func(t *testing.T) {
			for _, test := range []struct {
				name   string
				change func(*validation.AuthorizationContext)
			}{
				{name: "empty actions", change: func(auth *validation.AuthorizationContext) { auth.AllowedActions = nil }},
				{name: "wildcard action", change: func(auth *validation.AuthorizationContext) { auth.AllowedActions = []validation.Operation{"*"} }},
				{name: "wildcard ref", change: func(auth *validation.AuthorizationContext) { auth.AllowedRefs = []string{"all"} }},
			} {
				t.Run(test.name, func(t *testing.T) {
					gate := conformanceGate()
					authorization := gate["job-site"]
					test.change(&authorization)
					gate["invalid-policy-context"] = authorization
					memory := newConformanceMemoryWithGate(t, gate)
					item := testWorkItem("wi-029", validation.StateReadyForBuilding, 4)
					seedConformanceItem(t, memory, item)
					result := memory.Execute(conformanceCall(validation.OperationClaim, "invalid-policy-context", item, "vr029-"+test.name))
					assertRejectedDecision(t, result, validation.AuthorityAuthoritative, validation.CodeUnauthorizedAction)
					assertItemUnchanged(t, memory, item)
					assertEventCount(t, memory, 0)
				})
			}
			memory, _ := newConformanceMemory(t)
			item := testWorkItem("wi-029-downgrade", validation.StateReadyForBuilding, 4)
			seedConformanceItem(t, memory, item)
			call := conformanceCall(validation.OperationClaim, "job-site", item, "vr029-downgrade")
			call.PolicyVersion = "wms-policy/older"
			result := memory.Execute(call)
			assertRejectedDecision(t, result, validation.AuthorityAuthoritative, validation.CodeUnauthorizedAction)
			assertItemUnchanged(t, memory, item)
			assertEventCount(t, memory, 0)

			// An exact echo is accepted but never selects the rules: the
			// decision still records the version supplied by the trusted Gate.
			memory, _ = newConformanceMemory(t)
			item = testWorkItem("wi-029-version", validation.StateReadyForBuilding, 4)
			seedConformanceItem(t, memory, item)
			call = conformanceCall(validation.OperationClaim, "job-site", item, "vr029-version-source")
			call.PolicyVersion = "wms-policy/v1"
			result = memory.Execute(call)
			decision := assertAllowedDecision(t, result, validation.AuthorityAuthoritative)
			if decision.PolicyVersion != "wms-policy/v1" {
				t.Fatalf("policy version = %q, want trusted Gate version", decision.PolicyVersion)
			}
		}},
		{"VR-030", func(t *testing.T) {
			for _, test := range []struct {
				name   string
				mutate func(*validation.ApprovalRecord)
			}{
				{name: "cross-item approval", mutate: func(approval *validation.ApprovalRecord) { approval.WorkItemID = "another-work-item" }},
				{name: "wrong resolution binding", mutate: func(approval *validation.ApprovalRecord) { approval.ResolutionKind = "out-of-scope" }},
			} {
				t.Run(test.name, func(t *testing.T) {
					memory, _ := newConformanceMemory(t)
					item := testWorkItem("wi-030", validation.StateBlocked, 7)
					seedConformanceItem(t, memory, item)
					approval := conformanceApproval(item, "vr030-approval", "human-030", "materializer-1", "add-requirement")
					test.mutate(&approval)
					seedActiveResolution(t, memory, item, "vr030-submission", approval, "add-requirement", "CS-030", true)
					call := conformanceResolveCall(t, item, approval.ID, "vr030-submission", approval.Digest, "vr030-resolve")
					result := memory.Execute(call)
					assertRejectedDecision(t, result, validation.AuthorityAuthoritative, validation.CodeUnauthorizedAction)
					assertItemUnchanged(t, memory, item)
					storedApproval, _ := memory.Approval(approval.ID)
					if storedApproval.Status != "unused" {
						t.Fatalf("approval status = %q, want unused", storedApproval.Status)
					}
					assertEventCount(t, memory, 0)
				})
			}
		}},
		{"VR-031", func(t *testing.T) {
			for _, test := range []struct {
				name   string
				mutate func(*validation.ApprovalRecord)
			}{
				{name: "expired", mutate: func(approval *validation.ApprovalRecord) { approval.ExpiresAt = memoryTestTime }},
				{name: "revoked", mutate: func(approval *validation.ApprovalRecord) { approval.Status = "revoked" }},
				{name: "consumed", mutate: func(approval *validation.ApprovalRecord) { approval.Status = "consumed" }},
				{name: "digest mismatch", mutate: func(approval *validation.ApprovalRecord) { approval.Digest = "other-digest" }},
			} {
				t.Run(test.name, func(t *testing.T) {
					memory, _ := newConformanceMemory(t)
					item := testWorkItem("wi-031", validation.StateBlocked, 7)
					seedConformanceItem(t, memory, item)
					approval := conformanceApproval(item, "vr031-approval", "human-031", "materializer-1", "add-requirement")
					test.mutate(&approval)
					seedActiveResolution(t, memory, item, "vr031-submission", approval, "add-requirement", "CS-031", true)
					call := conformanceResolveCall(t, item, approval.ID, "vr031-submission", "vr031-approval-digest", "vr031-resolve")
					result := memory.Execute(call)
					assertRejectedDecision(t, result, validation.AuthorityAuthoritative, validation.CodeUnauthorizedAction)
					assertItemUnchanged(t, memory, item)
					assertEventCount(t, memory, 0)
				})
			}
		}},
		{"VR-032", func(t *testing.T) {
			for _, test := range []string{"missing approval", "missing digest", "missing active submission"} {
				t.Run(test, func(t *testing.T) {
					memory, _ := newConformanceMemory(t)
					item := testWorkItem("wi-032", validation.StateBlocked, 7)
					seedConformanceItem(t, memory, item)
					approval := conformanceApproval(item, "vr032-approval", "human-032", "materializer-1", "add-requirement")
					seedActiveResolution(t, memory, item, "vr032-submission", approval, "add-requirement", "CS-032", true)
					call := conformanceResolveCall(t, item, approval.ID, "vr032-submission", approval.Digest, "vr032-resolve-"+test)
					switch test {
					case "missing approval":
						call.HumanApprovalID = ""
						var payload validation.Payload
						if err := decodePayload(call.Payload, &payload); err != nil {
							t.Fatal(err)
						}
						payload.HumanApprovalID = ""
						call.Payload = jsonPayload(t, payload)
					case "missing digest":
						var payload validation.Payload
						if err := decodePayload(call.Payload, &payload); err != nil {
							t.Fatal(err)
						}
						payload.ApprovalDigest = ""
						call.Payload = jsonPayload(t, payload)
					case "missing active submission":
						var payload validation.Payload
						if err := decodePayload(call.Payload, &payload); err != nil {
							t.Fatal(err)
						}
						payload.ResolutionSubmissionID = ""
						call.Payload = jsonPayload(t, payload)
					}
					result := memory.Execute(call)
					assertRejectedDecision(t, result, validation.AuthorityAuthoritative, validation.CodeUnauthorizedAction)
					assertItemUnchanged(t, memory, item)
					assertEventCount(t, memory, 0)
				})
			}
		}},
		{"VR-033", func(t *testing.T) {
			memory, _ := newConformanceMemory(t)
			item := testWorkItem("wi-033", validation.StateBlocked, 7)
			seedConformanceItem(t, memory, item)
			seedCompletedDependencyAndChangeSet(t, memory, "vr033-dependency", "CS-033")
			approval := conformanceApproval(item, "vr033-approval", "human-033", "materializer-1", "add-requirement")
			if err := memory.SeedApproval(approval); err != nil {
				t.Fatal(err)
			}
			submitted := submitConformanceResolution(t, memory, item, approval.ID, approval.Digest, "CS-033", "vr033-submit")
			call := conformanceResolveCall(t, item, approval.ID, submitted.ResolutionSubmissionID, approval.Digest, "vr033-resolve")
			first := memory.Execute(call)
			firstDecision := assertAllowedDecision(t, first, validation.AuthorityAuthoritative)
			approvalAfterFirst, _ := memory.Approval(approval.ID)
			if approvalAfterFirst.Status != "consumed" {
				t.Fatalf("approval status after resolve = %q, want consumed", approvalAfterFirst.Status)
			}
			replay := memory.Execute(call)
			assertReplayedDecision(t, replay, validation.AuthorityAuthoritative)
			if !reflect.DeepEqual(firstDecision.After, replay.Decision.After) {
				t.Fatalf("resolve-block replay after = %#v, original after = %#v", replay.Decision.After, firstDecision.After)
			}
			assertEventCount(t, memory, 2)
		}},
		{"VR-034", func(t *testing.T) {
			memory, _ := newConformanceMemory(t)
			item := testWorkItem("wi-034", validation.StateMerging, 9)
			item.Reconciliation = validation.ReconciliationEvidence{Status: "conflict", GitMutation: "conflict"}
			seedConformanceItem(t, memory, item)
			result := memory.Execute(conformanceCall(validation.OperationMergeConflict, "reconciler", item, "vr034-conflict"))
			decision := assertAllowedDecision(t, result, validation.AuthorityAuthoritative)
			if decision.After.State != validation.StateReadyForBuilding || decision.FencingTokenIssued != "" {
				t.Fatalf("reconciled conflict decision = %#v, want ready without issuing a lease", decision)
			}
			assertEventCount(t, memory, 1)
		}},
		{"VR-035", func(t *testing.T) {
			memory, _ := newConformanceMemory(t)
			item := testWorkItem("wi-035", validation.StateMerging, 9)
			envelope := &validation.MergeEnvelope{
				ProductTreeDigest: "tree-035",
				InspectionRunID:   "inspection-035",
				IntegrationHead:   "integration-035",
				Target:            "main",
				ContractVersion:   9,
				MergeCommit:       "merge-035",
			}
			item.Reconciliation = validation.ReconciliationEvidence{Status: "merge-recorded", GitMutation: "merged", MergeEnvelope: envelope}
			seedConformanceItem(t, memory, item)
			call := conformanceCall(validation.OperationRecordMerge, "reconciler", item, "vr035-record-merge")
			call.Payload = jsonPayload(t, validation.Payload{MergeEnvelope: envelope})
			first := memory.Execute(call)
			firstDecision := assertAllowedDecision(t, first, validation.AuthorityAuthoritative)
			if firstDecision.After.State != validation.StateCompleted {
				t.Fatalf("reconciled completion state = %q, want completed", firstDecision.After.State)
			}
			replay := memory.Execute(call)
			assertReplayedDecision(t, replay, validation.AuthorityAuthoritative)
			assertEventCount(t, memory, 1)
		}},
		{"VR-036", func(t *testing.T) {
			for _, test := range []struct {
				name      string
				operation validation.Operation
			}{
				{name: "merge conflict missing fence", operation: validation.OperationMergeConflict},
				{name: "record merge stale fence", operation: validation.OperationRecordMerge},
			} {
				t.Run(test.name, func(t *testing.T) {
					memory, _ := newConformanceMemory(t)
					item := testWorkItem("wi-036-"+test.name, validation.StateMerging, 9)
					setConformanceLease(&item, "job-site", "fence-036", memoryTestTime.Add(time.Hour))
					call := conformanceCall(test.operation, "job-site", item, "vr036-"+test.name)
					if test.operation == validation.OperationMergeConflict {
						item.Reconciliation = validation.ReconciliationEvidence{Status: "conflict", GitMutation: "conflict"}
					} else {
						item.ExpectedMerge = &validation.MergeEnvelope{ProductTreeDigest: "tree-036", InspectionRunID: "inspect-036", IntegrationHead: "head-036", Target: "main", ContractVersion: 9}
						actual := *item.ExpectedMerge
						actual.MergeCommit = "merge-036"
						call.Payload = jsonPayload(t, validation.Payload{MergeEnvelope: &actual})
						call.FencingToken = "fence-old"
					}
					seedConformanceItem(t, memory, item)
					result := memory.Execute(call)
					assertRejectedDecision(t, result, validation.AuthorityAuthoritative, validation.CodeStaleFencingToken)
					assertItemUnchanged(t, memory, item)
					assertEventCount(t, memory, 0)
				})
			}
		}},
		{"VR-037", func(t *testing.T) {
			for _, test := range []validation.Operation{validation.OperationMergeConflict, validation.OperationMergeNotApplied, validation.OperationRecordMerge} {
				t.Run(string(test), func(t *testing.T) {
					memory, _ := newConformanceMemory(t)
					item := testWorkItem("wi-037-"+string(test), validation.StateMerging, 9)
					item.Reconciliation = validation.ReconciliationEvidence{Status: "conflict", GitMutation: "none"}
					seedConformanceItem(t, memory, item)
					result := memory.Execute(conformanceCall(test, "reconciler", item, "vr037-"+string(test)))
					assertRejectedDecision(t, result, validation.AuthorityAuthoritative, validation.CodePreconditionFailed)
					assertItemUnchanged(t, memory, item)
					assertEventCount(t, memory, 0)
				})
			}
		}},
		{"VR-038", func(t *testing.T) {
			memory, _ := newConformanceMemory(t)
			item := testWorkItem("wi-038", validation.StateMerging, 9)
			item.Reconciliation = validation.ReconciliationEvidence{Status: "conflict", GitMutation: "conflict"}
			seedConformanceItem(t, memory, item)
			call := conformanceCall(validation.OperationMergeConflict, "reconciler", item, "vr038-forged-proof")
			call.Payload = jsonPayload(t, validation.Payload{MergeEnvelope: &validation.MergeEnvelope{
				ProductTreeDigest: "forged-tree",
				InspectionRunID:   "forged-inspection",
				IntegrationHead:   "forged-head",
				Target:            "other",
				ContractVersion:   999,
				MergeCommit:       "forged-merge",
			}})
			result := memory.Execute(call)
			decision := assertAllowedDecision(t, result, validation.AuthorityAuthoritative)
			if decision.After.State != validation.StateReadyForBuilding {
				t.Fatalf("trusted reconciliation decision = %#v, want current-record outcome", decision)
			}
			assertEventCount(t, memory, 1)
		}},
		{"VR-039", func(t *testing.T) {
			memory, _ := newConformanceMemory(t)
			candidate := testWorkItem("wi-039", validation.StateInitial, 0)
			candidate.ChangeType = "changes"
			candidate.ImplementationRequired = false
			first := memory.Execute(materializeCall(candidate, "vr039-command-1", "vr039-logical-key"))
			firstDecision := assertAllowedDecision(t, first, validation.AuthorityAuthoritative)
			if firstDecision.Outcome != validation.OutcomeOmitted || firstDecision.After != nil || firstDecision.MaterializationReservation == nil {
				t.Fatalf("omitted materialization decision = %#v", firstDecision)
			}
			replay := memory.Execute(materializeCall(candidate, "vr039-command-2", "vr039-logical-key"))
			assertReplayedDecision(t, replay, validation.AuthorityAuthoritative)
			if replay.Decision.Outcome != validation.OutcomeOmitted || replay.Mutation != mutationNone {
				t.Fatalf("omitted replay = %#v", replay)
			}
			changed := candidate
			changed.Priority = "high"
			changed.Readiness.PolicyCompatible = false
			conflict := memory.Execute(materializeCall(changed, "vr039-command-3", "vr039-logical-key"))
			assertRejectedDecision(t, conflict, validation.AuthorityAuthoritative, validation.CodeIdempotencyConflict)
			if _, exists := memory.WorkItem(candidate.ID); exists {
				t.Fatal("omitted materialization created an executable work item")
			}
			assertEventCount(t, memory, 1)
		}},
		{"VR-040", func(t *testing.T) {
			for _, test := range []validation.Operation{validation.OperationRecoverLease, validation.OperationAbandon} {
				t.Run(string(test), func(t *testing.T) {
					memory, _ := newConformanceMemory(t)
					item := testWorkItem("wi-040-"+string(test), validation.StateBuilding, 9)
					if test == validation.OperationRecoverLease {
						setConformanceLease(&item, "job-site-old", "fence-expired", memoryTestTime.Add(-time.Second))
						item.Reconciliation = validation.ReconciliationEvidence{Status: "lease-recovered", GitMutation: "conflict"}
					} else {
						item.Reconciliation = validation.ReconciliationEvidence{Status: "not-integrated", GitMutation: "merged"}
					}
					seedConformanceItem(t, memory, item)
					actor := "reconciler"
					call := conformanceCall(test, actor, item, "vr040-"+string(test))
					if test == validation.OperationAbandon {
						call.ActorContextRef = "human-maintainer"
						call.Payload = jsonPayload(t, validation.Payload{CancellationConfirmed: true})
					}
					result := memory.Execute(call)
					assertRejectedDecision(t, result, validation.AuthorityAuthoritative, validation.CodePreconditionFailed)
					assertItemUnchanged(t, memory, item)
					assertEventCount(t, memory, 0)
				})
			}
		}},
		{"VR-041", func(t *testing.T) {
			memory, _ := newConformanceMemory(t)
			item := testWorkItem("wi-041", validation.StateBlocked, 7)
			seedConformanceItem(t, memory, item)
			result := memory.Execute(conformanceCall(validation.OperationClaim, "job-site", item, "vr041-current-blocked"))
			assertRejectedDecision(t, result, validation.AuthorityAuthoritative, validation.CodeInvalidTransition)
			assertItemUnchanged(t, memory, item)
			assertEventCount(t, memory, 0)
		}},
		{"VR-042", func(t *testing.T) {
			for _, test := range []struct {
				name     string
				mutate   func(*validation.ApprovalRecord)
				humanRef string
			}{
				{name: "delegated principal mismatch", mutate: func(approval *validation.ApprovalRecord) { approval.DelegatedPrincipal = "another-materializer" }, humanRef: "human-042"},
				{name: "approved human mismatch", mutate: func(approval *validation.ApprovalRecord) { approval.ApprovedSubject = "different-human" }, humanRef: "human-042"},
			} {
				t.Run(test.name, func(t *testing.T) {
					memory, _ := newConformanceMemory(t)
					item := testWorkItem("wi-042", validation.StateBlocked, 7)
					seedConformanceItem(t, memory, item)
					approval := conformanceApproval(item, "vr042-approval", test.humanRef, "materializer-1", "add-requirement")
					test.mutate(&approval)
					seedActiveResolution(t, memory, item, "vr042-submission", approval, "add-requirement", "CS-042", true)
					// The durable submission records which human the Gate-bound
					// approval authorized; changing that identity must fail closed.
					if test.name == "approved human mismatch" {
						submission := memory.submissions["vr042-submission"]
						submission.ApprovedHumanSubject = "human-042"
						memory.submissions["vr042-submission"] = submission
					}
					call := conformanceResolveCall(t, item, approval.ID, "vr042-submission", approval.Digest, "vr042-resolve")
					result := memory.Execute(call)
					assertRejectedDecision(t, result, validation.AuthorityAuthoritative, validation.CodeUnauthorizedAction)
					assertItemUnchanged(t, memory, item)
					stored, _ := memory.Approval(approval.ID)
					if stored.Status != "unused" {
						t.Fatalf("mismatched approval status = %q, want unused", stored.Status)
					}
					assertEventCount(t, memory, 0)
				})
			}
		}},
		{"VR-043", func(t *testing.T) {
			t.Run("superseded submission approval", func(t *testing.T) {
				memory, _ := newConformanceMemory(t)
				item := testWorkItem("wi-043", validation.StateBlocked, 7)
				seedConformanceItem(t, memory, item)
				seedCompletedDependencyAndChangeSet(t, memory, "vr043-dependency", "CS-043")
				firstApproval := conformanceApproval(item, "vr043-approval-1", "human-043", "materializer-1", "add-requirement")
				secondApproval := conformanceApproval(item, "vr043-approval-2", "human-043", "materializer-1", "add-requirement")
				if err := memory.SeedApproval(firstApproval); err != nil {
					t.Fatal(err)
				}
				if err := memory.SeedApproval(secondApproval); err != nil {
					t.Fatal(err)
				}
				first := submitConformanceResolution(t, memory, item, firstApproval.ID, firstApproval.Digest, "CS-043", "vr043-submit-1")
				second := submitConformanceResolution(t, memory, item, secondApproval.ID, secondApproval.Digest, "CS-043", "vr043-submit-2")
				oldApproval, _ := memory.Approval(firstApproval.ID)
				if oldApproval.Status != "revoked" || second.ResolutionSubmissionID == first.ResolutionSubmissionID {
					t.Fatalf("superseded resolution state: old approval=%#v, submissions=%#v/%#v", oldApproval, first, second)
				}
				stale := conformanceResolveCall(t, item, firstApproval.ID, first.ResolutionSubmissionID, firstApproval.Digest, "vr043-stale-resolution")
				result := memory.Execute(stale)
				assertRejectedDecision(t, result, validation.AuthorityAuthoritative, validation.CodeUnauthorizedAction)
				assertItemUnchanged(t, memory, item)
				assertEventCount(t, memory, 2)
			})
			t.Run("non-active submission ID", func(t *testing.T) {
				memory, _ := newConformanceMemory(t)
				item := testWorkItem("wi-043-inactive", validation.StateBlocked, 7)
				seedConformanceItem(t, memory, item)
				approval := conformanceApproval(item, "vr043-inactive-approval", "human-043", "materializer-1", "add-requirement")
				seedActiveResolution(t, memory, item, "vr043-active", approval, "add-requirement", "CS-043", true)
				call := conformanceResolveCall(t, item, approval.ID, "vr043-other-submission", approval.Digest, "vr043-inactive-id")
				result := memory.Execute(call)
				assertRejectedDecision(t, result, validation.AuthorityAuthoritative, validation.CodeUnauthorizedAction)
				assertItemUnchanged(t, memory, item)
				assertEventCount(t, memory, 0)
			})
		}},
		{"VR-044", func(t *testing.T) {
			memory, _ := newConformanceMemory(t)
			item := testWorkItem("wi-044", validation.StateBlocked, 7)
			item.Dependencies = []validation.Dependency{{ID: "existing-dependency", State: validation.StateCompleted}}
			seedConformanceItem(t, memory, item)
			incomplete := testWorkItem("wi-044-planned", validation.StateWaiting, 1)
			seedConformanceItem(t, memory, incomplete)
			if err := memory.SeedChangeSet(ChangeSet{ID: "CS-044", Revision: "approved", BuildWorkItemID: incomplete.ID}); err != nil {
				t.Fatal(err)
			}
			approval := conformanceApproval(item, "vr044-approval", "human-044", "materializer-1", "add-requirement")
			if err := memory.SeedApproval(approval); err != nil {
				t.Fatal(err)
			}
			submitted := submitConformanceResolution(t, memory, item, approval.ID, approval.Digest, "CS-044", "vr044-submit")
			result := memory.Execute(conformanceResolveCall(t, item, approval.ID, submitted.ResolutionSubmissionID, approval.Digest, "vr044-resolve"))
			assertRejectedDecision(t, result, validation.AuthorityAuthoritative, validation.CodePreconditionFailed)
			stored, _ := memory.WorkItem(item.ID)
			if stored.State != validation.StateBlocked || stored.ContractVersion != item.ContractVersion || !reflect.DeepEqual(stored.Dependencies, item.Dependencies) {
				t.Fatalf("failed planned dependency check mutated item: %#v", stored)
			}
			approvalAfter, _ := memory.Approval(approval.ID)
			if approvalAfter.Status != "unused" {
				t.Fatalf("incomplete dependency consumed approval: %#v", approvalAfter)
			}
			assertEventCount(t, memory, 1)
		}},
	}

	for _, test := range tests {
		t.Run(test.id, test.run)
	}
}

func newConformanceMemory(t *testing.T) (*Memory, StaticGate) {
	t.Helper()
	gate := conformanceGate()
	return newConformanceMemoryWithGate(t, gate), gate
}

func newConformanceMemoryWithGate(t *testing.T, gate StaticGate) *Memory {
	t.Helper()
	memory, err := New(Config{
		ProjectID:           "fixture-project",
		Gate:                gate,
		Now:                 func() time.Time { return memoryTestTime },
		LeaseDuration:       15 * time.Minute,
		MaterializerSubject: "materializer-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	return memory
}

func conformanceGate() StaticGate {
	return StaticGate{
		"drafting-table": testAuthorization("drafting-agent", validation.RoleDraftingTable,
			validation.OperationLifecyclePreflight,
			validation.Operation("request.create"),
			validation.Operation("request.refine"),
			validation.Operation("request.link-change-set"),
			validation.Operation("request.link-build-work-item"),
			validation.Operation("request.get"),
			validation.Operation("request.query"),
			validation.Operation("work-item.get"),
			validation.Operation("work-item.query"),
			validation.Operation("blocked-work.query"),
			validation.Operation("blocked-work.submit-resolution"),
			validation.Operation("blocked-work.acknowledge")),
		"job-site": testAuthorization("job-site", validation.RoleJobSite,
			validation.OperationLifecyclePreflight,
			validation.OperationClaim,
			validation.OperationRenewLease,
			validation.OperationTestsPass,
			validation.OperationRaiseSpecQuestion,
			validation.OperationRefreshActive,
			validation.OperationReturnToBuilding,
			validation.OperationBeginMerge,
			validation.OperationMergeConflict,
			validation.OperationRecordMerge),
		"job-site-a": testAuthorization("job-site-a", validation.RoleJobSite,
			validation.OperationLifecyclePreflight,
			validation.OperationClaim,
			validation.OperationRenewLease,
			validation.OperationTestsPass,
			validation.OperationRaiseSpecQuestion,
			validation.OperationRefreshActive,
			validation.OperationReturnToBuilding,
			validation.OperationBeginMerge,
			validation.OperationMergeConflict,
			validation.OperationRecordMerge),
		"job-site-b": testAuthorization("job-site-b", validation.RoleJobSite,
			validation.OperationLifecyclePreflight,
			validation.OperationClaim,
			validation.OperationRenewLease,
			validation.OperationTestsPass,
			validation.OperationRaiseSpecQuestion,
			validation.OperationRefreshActive,
			validation.OperationReturnToBuilding,
			validation.OperationBeginMerge,
			validation.OperationMergeConflict,
			validation.OperationRecordMerge),
		"materializer": testAuthorization("materializer-1", validation.RoleMaterializer,
			validation.OperationLifecyclePreflight,
			validation.OperationMaterialize,
			validation.OperationRefreshDependencies,
			validation.OperationRevalidate,
			validation.OperationResolveBlock),
		"reconciler": testAuthorization("reconciler-1", validation.RoleReconciler,
			validation.OperationRecoverLease,
			validation.OperationMergeConflict,
			validation.OperationMergeNotApplied,
			validation.OperationRecordMerge),
		"human-maintainer": testAuthorization("human-1", validation.RoleHumanMaintainer,
			validation.OperationAbandon,
			validation.Operation("request.update-priority")),
	}
}

func conformanceCall(operation validation.Operation, actor string, item validation.WorkItem, key string) CallRequest {
	version := item.ContractVersion
	return CallRequest{
		Operation:               string(operation),
		ActorContextRef:         actor,
		WorkItemID:              item.ID,
		ExpectedState:           item.State,
		ExpectedContractVersion: &version,
		IdempotencyKey:          key,
	}
}

func seedConformanceItem(t *testing.T, memory *Memory, item validation.WorkItem) {
	t.Helper()
	if err := memory.SeedWorkItem(item); err != nil {
		t.Fatal(err)
	}
}

func setConformanceLease(item *validation.WorkItem, owner, token string, expires time.Time) {
	item.Owner = owner
	item.Lease = &validation.Lease{Owner: owner, FencingToken: token, ExpiresAt: expires}
}

func replaceObservedWorkItem(memory *Memory, item validation.WorkItem) {
	memory.mu.Lock()
	defer memory.mu.Unlock()
	memory.workItems[item.ID] = cloneWorkItem(item)
}

func assertAllowedDecision(t *testing.T, result Result, authority validation.Authority) *validation.Decision {
	t.Helper()
	if !result.OK || result.Error != nil || result.Mutation != mutationApplied || result.Outcome != outcomeApplied {
		t.Fatalf("allowed result = %#v, want applied", result)
	}
	decision := assertDecisionMetadata(t, result.Decision, authority)
	if decision.Outcome != validation.OutcomeAllowed && decision.Outcome != validation.OutcomeOmitted {
		t.Fatalf("decision outcome = %q, want allowed or omitted: %#v", decision.Outcome, decision)
	}
	return decision
}

func assertRejectedDecision(t *testing.T, result Result, authority validation.Authority, code string) *validation.Decision {
	t.Helper()
	if result.OK || result.Mutation != mutationNone || result.Outcome != outcomeRejected {
		t.Fatalf("rejected result = %#v, want rejected without mutation", result)
	}
	decision := assertDecisionMetadata(t, result.Decision, authority)
	if result.Error == nil || result.Error.Code != code || decision.Rejection == nil || decision.Rejection.Code != code {
		t.Fatalf("rejection = %#v / decision=%#v, want %s", result.Error, decision.Rejection, code)
	}
	if decision.After != nil {
		t.Fatalf("rejected decision proposed after-state %#v", decision.After)
	}
	return decision
}

func assertReplayedDecision(t *testing.T, result Result, authority validation.Authority) *validation.Decision {
	t.Helper()
	if result.Outcome != outcomeReplayed || result.Mutation != mutationNone || result.Idempotency != idempotencyReplayed {
		t.Fatalf("replay result = %#v, want replayed without mutation", result)
	}
	decision := assertDecisionMetadata(t, result.Decision, authority)
	if !decision.Replayed {
		t.Fatalf("replay decision = %#v, want replayed=true", decision)
	}
	return decision
}

func assertPreflightDecision(t *testing.T, result Result, expected validation.Outcome) *validation.Decision {
	t.Helper()
	if !result.OK || result.Outcome != outcomeRead || result.Mutation != mutationNone || result.Error != nil {
		t.Fatalf("preflight result = %#v, want successful read envelope", result)
	}
	decision := assertDecisionMetadata(t, result.Decision, validation.AuthorityPreflight)
	if decision.Outcome != expected {
		t.Fatalf("preflight decision outcome = %q, want %q", decision.Outcome, expected)
	}
	return decision
}

func assertDecisionMetadata(t *testing.T, decision *validation.Decision, authority validation.Authority) *validation.Decision {
	t.Helper()
	if decision == nil {
		t.Fatal("Validation Rules decision is missing")
	}
	if decision.RuleVersion != validation.RuleVersion || decision.PolicyVersion != "wms-policy/v1" || decision.Authority != authority {
		t.Fatalf("decision metadata = rule %q, policy %q, authority %q", decision.RuleVersion, decision.PolicyVersion, decision.Authority)
	}
	return decision
}

func assertRequestRejection(t *testing.T, result Result, code string) {
	t.Helper()
	if result.OK || result.Error == nil || result.Error.Code != code || result.Mutation != mutationNone {
		t.Fatalf("request rejection = %#v, want %s without mutation", result, code)
	}
}

func assertItemUnchanged(t *testing.T, memory *Memory, want validation.WorkItem) {
	t.Helper()
	got, exists := memory.WorkItem(want.ID)
	if !exists || !reflect.DeepEqual(got, want) {
		t.Fatalf("work item after rejected operation = %#v, exists=%t; want unchanged %#v", got, exists, want)
	}
}

func assertEventCount(t *testing.T, memory *Memory, want int) {
	t.Helper()
	if got := len(memory.Events()); got != want {
		t.Fatalf("audit event count = %d, want %d: %#v", got, want, memory.Events())
	}
}

func seedCompletedDependencyAndChangeSet(t *testing.T, memory *Memory, dependencyID, changeSetID string) {
	t.Helper()
	dependency := testWorkItem(dependencyID, validation.StateCompleted, 2)
	seedConformanceItem(t, memory, dependency)
	if err := memory.SeedChangeSet(ChangeSet{ID: changeSetID, Revision: "approved", BuildWorkItemID: dependencyID}); err != nil {
		t.Fatal(err)
	}
}

func conformanceApproval(item validation.WorkItem, id, human, delegated, kind string) validation.ApprovalRecord {
	version := item.ContractVersion
	return validation.ApprovalRecord{
		ID:                      id,
		ApprovedSubject:         human,
		DelegatedPrincipal:      delegated,
		ProjectID:               item.ProjectID,
		WorkItemID:              item.ID,
		Digest:                  id + "-digest",
		Action:                  validation.OperationResolveBlock,
		ResolutionKind:          kind,
		ExpectedState:           item.State,
		ExpectedContractVersion: &version,
		PolicyVersion:           "wms-policy/v1",
		ExpiresAt:               memoryTestTime.Add(time.Hour),
		Status:                  "unused",
	}
}

func submitConformanceResolution(t *testing.T, memory *Memory, item validation.WorkItem, approvalID, digest, changeSetID, key string) Result {
	t.Helper()
	version := item.ContractVersion
	return memory.Execute(CallRequest{
		Operation:               "blocked-work.submit-resolution",
		ActorContextRef:         "drafting-table",
		WorkItemID:              item.ID,
		ExpectedState:           validation.StateBlocked,
		ExpectedContractVersion: &version,
		HumanApprovalID:         approvalID,
		IdempotencyKey:          key,
		Payload: jsonPayload(t, blockedResolutionPayload{
			ResolutionKind:           "add-requirement",
			ChangeSetID:              changeSetID,
			ApprovalResolutionDigest: digest,
		}),
	})
}

func conformanceResolveCall(t *testing.T, item validation.WorkItem, approvalID, submissionID, digest, key string) CallRequest {
	t.Helper()
	call := conformanceCall(validation.OperationResolveBlock, "materializer", item, key)
	call.HumanApprovalID = approvalID
	call.Payload = jsonPayload(t, validation.Payload{
		ResolutionKind:         "add-requirement",
		HumanApprovalID:        approvalID,
		ApprovalDigest:         digest,
		ResolutionSubmissionID: submissionID,
	})
	return call
}

func seedActiveResolution(
	t *testing.T,
	memory *Memory,
	item validation.WorkItem,
	submissionID string,
	approval validation.ApprovalRecord,
	kind, changeSetID string,
	plannedDependencyComplete bool,
) {
	t.Helper()
	if err := memory.SeedApproval(approval); err != nil {
		t.Fatal(err)
	}
	memory.submissions[submissionID] = Submission{
		ResolutionSubmission: validation.ResolutionSubmission{
			ID:                        submissionID,
			WorkItemID:                item.ID,
			Kind:                      kind,
			ChangeSetID:               changeSetID,
			ApprovalID:                approval.ID,
			ApprovalDigest:            approval.Digest,
			ApprovedHumanSubject:      approval.ApprovedSubject,
			Status:                    "pending",
			PlannedDependencyComplete: plannedDependencyComplete,
		},
		Revision: 1,
	}
	memory.activeSubmissions[item.ID] = submissionID
}
