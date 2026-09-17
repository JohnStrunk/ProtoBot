# ProtoBot: Drafting Table WMS Integration Contract

> Interface contract — issue #31 — September 2026
>
> Defines the backend-neutral WMS operations available to the Drafting
> Table during backlog refinement and blocked-work resolution.

**Contents:**

- [Purpose and scope](#purpose-and-scope)
- [Boundary and ownership](#boundary-and-ownership)
- [Resources and revisions](#resources-and-revisions)
- [Operation matrix](#operation-matrix)
- [Request and result protocol](#request-and-result-protocol)
- [Blocked-work resolution](#blocked-work-resolution)
- [Deployment, security, and state](#deployment-security-and-state)
- [Fake adapter fixture](#fake-adapter-fixture)
- [Out-of-scope decisions](#out-of-scope-decisions)
- [Related Documents](#related-documents)

---

## Purpose and scope

This contract answers the question posed by issue #31: how does the
Drafting Table use the WMS Adapter without becoming a work-management
backend or a Job Site executor?

The Drafting Table WMS surface covers:

- creating and refining request backlog records;
- updating business priority when authorized by a human maintainer;
- linking requests to proposed change sets and existing build work items;
- reading request, work-item, dependency, and blocked-work state; and
- submitting a reviewed blocked-work resolution with optimistic concurrency.

The contract is a subset of the WMS Adapter API. It is stable across WMS
backends and does not expose GitHub, GitLab, Jira, Beads, or Trello
concepts to the Drafting Table.

### Relationship to sibling contracts

- [Drafting Table UX](drafting-table-ux.md) defines what the user sees,
  chooses, and approves.
- [Validation Rules](validation-rules.md) defines lifecycle authorization,
  expected state/version checks, and authoritative rejection semantics.
- [`ears-manager` CLI Integration Contract](ears-manager-cli.md) defines
  specification reads and writes; the WMS contract never edits specs.
- [Git and Project-Repository Integration](git-integration.md) defines
  branches, commits, pull requests, and approval registration.
- [System Components — WMS Adapter](components.md#wms-adapter) defines the
  complete adapter surface, including Job Site operations outside this
  contract.

### Non-goals

The Drafting Table WMS surface does not:

- materialize build work items from approved change sets;
- claim work or renew execution leases;
- run Building or Inspecting transitions;
- create or mutate Finding Ledger events;
- record completion or merge code;
- schedule work, assign builders, or change WIP policy; or
- parse or write specification files.

The Drafting Table may submit a request-to-build-item link only when the
target build item already exists and the WMS authorizes that link. It never
creates the target item as a side effect.

---

## Boundary and ownership

| Actor or component | WMS responsibility | Authoritative lifecycle mutation |
| --- | --- | --- |
| Drafting Table / Toolkit | Invoke the operations in this contract, display results, and preserve user decisions. | No direct work-item transition. |
| Human maintainer | Own business priority and explicit blocked-work decisions. | May authorize priority and resolution submissions; does not bypass the WMS boundary. |
| WMS Adapter | Expose this contract, load current records, apply Validation Rules, and translate to the selected backend. | Yes, inside the authoritative write boundary. |
| Backend translator | Map the validated operation to the configured WMS backend. | No. It must not reimplement policy or lifecycle rules. |
| Materializer / Job Site | Create complete build-work contracts and perform execution lifecycle operations. | Yes, for operations outside this contract. |
| Validation Rules | Provide preflight and authoritative lifecycle decisions. | No independent store write; the WMS boundary commits allowed results. |

The Drafting Table may call preflight for better diagnostics. A preflight
result is never proof that an authoritative mutation succeeded. The WMS
Adapter rechecks the fresh record and the trusted authorization context
before every mutation.

The contract has two authorization namespaces. Request/backlog operations
use the WMS Adapter's project-scoped request policy: the Drafting Table may
create/refine/link within its project, while only a `human-maintainer` may
change business priority. Lifecycle operations use the Validation Rules
role family and rejection contract from #32. A request operation must not
be rejected merely because it is not a lifecycle transition, and a
lifecycle operation must not bypass Validation Rules by using request
policy alone.

The request namespace is closed to the operations in the matrix above:
unknown request names and unknown lifecycle names are rejected with
`UNAUTHORIZED_ACTION`. Its Gate context is subject to the same fail-closed
requirements as Validation Rules: a known role, non-empty actions and refs,
valid policy version, unexpired context, no wildcards, and payload refs
within the allowed set. Request idempotency keys are bound to the trusted
subject; untrusted actor fields cannot widen them.

---

## Resources and revisions

### Request record

A request captures intent before or alongside specification refinement. Its
minimum backend-neutral fields are:

| Field | Meaning |
| --- | --- |
| `request_id` | Stable WMS request identifier. It is not an EARS requirement ID. |
| `intent` | What the requester wants ProtoBot to build or change. |
| `rationale` | Why the request matters. |
| `created_by` | Authenticated originator identity. |
| `owner` | Current refinement owner, when assigned. |
| `affected_interfaces` | Stable interface IDs in the Architecture. |
| `affected_scopes` | Project-defined applicability scopes. |
| `classification` | `undefined`, `changes`, or `contradicts`, once refinement has classified the request. |
| `refinement_state` | MVP values are `unrefined`, `refining`, `ready-for-dimensioning`, or `closed`. |
| `business_priority` | Human-owned priority; it is not assigned by an agent or Job Site. |
| `relationships` | Typed request relationships using the project relationship vocabulary. |
| `change_set_id` | Optional link to a proposed or approved change set. |
| `build_work_item_id` | Optional link to an existing materialized build item. |
| `request_revision` | Monotonically increasing request revision for request mutations. |

`request_revision` is separate from a build work item's
`contract_version`. A request mutation must never use or increment the
work-item contract version.

### Resolution-submission record

`blocked-work.submit-resolution` writes a separate durable submission record
owned by the WMS Adapter. It has its own `resolution_submission_id`,
`resolution_submission_revision`, work-item ID, resolution kind, approval
ID/digest, and submission status. Creating or replaying this record does not
mutate
the work item's lifecycle state or `contract_version`; the Materializer
later consumes it through the authoritative `resolve-block` operation.

### Work-item read projection

The Drafting Table receives a read projection, not ownership of the work
item. The projection includes:

- stable work-item ID and request/change-set links;
- canonical lifecycle state;
- owner and dependency summaries;
- blocking reason class and sanitized next action;
- current `contract_version`; and
- relevant immutable source or merge references when the caller is
  authorized to see them.

The projection excludes credentials, raw Worker content, raw Inspector
findings, and backend-specific labels or status names.

### Canonical lifecycle state

Work-item query results use the Validation Rules vocabulary exactly:
`waiting`, `ready-for-building`, `building`, `inspecting`, `blocked`,
`merging`, `completed`, and `abandoned`. Presentation labels are a Drafting
Table concern and must not replace these values in the adapter contract.

---

## Operation matrix

Every row identifies the caller, authorization, concurrency input, result,
and failure behavior. The operation names are logical API names; an MCP,
HTTP, or local adapter binding may choose a transport-specific spelling.

| Operation | Caller and authorization | Expected version / idempotency | Success result | Failure behavior |
| --- | --- | --- | --- | --- |
| `request.create` | Drafting Table under a project-scoped `drafting-table` context. | New request; required idempotency key. | Complete request at `request_revision: 1`; no change set or work item is created implicitly. | Exact key/fingerprint retry returns `replayed`; a different fingerprint returns `IDEMPOTENCY_CONFLICT`; a semantic duplicate under a new key returns `DUPLICATE_REQUEST`; invalid fields or WMS failure leave no partial record. |
| `request.refine` | Drafting Table submits refinement under a Gate-bound human approval for classification, scope, relationships, owner, and intent. | `expected_request_revision`, `human_approval_id`, `approval_refinement_digest`, and idempotency key. | Updated request, refinement state, classification, links, and incremented request revision. | Reject missing/invalid approval, stale revision, invalid relationship/classification, unauthorized target, duplicate request, or unavailable WMS. |
| `request.update-priority` | `human-maintainer` only. | `expected_request_revision` and idempotency key. | New business priority, request revision, audit event, and an atomic priority snapshot update for linked proposed change sets/build items. | Reject agent/service caller, stale revision, invalid priority, or failed atomic WMS update. |
| `request.link-change-set` | Drafting Table or human maintainer with visibility to both records. | Expected request revision, target change-set revision when mutable, and idempotency key. | Existing request-to-change-set link and updated request revision. | Reject missing/unauthorized target, duplicate link, stale endpoint, or invalid change-set state. This does not create a change set. |
| `request.link-build-work-item` | Drafting Table may link only to an existing authorized build item; Materializer may create the automatic link as part of materialization. | Expected request revision, existing target ID, and idempotency key. | Existing request-to-build-item link and updated request revision. | Reject missing target, duplicate link, stale source, or any attempt to materialize the target as a side effect. |
| `request.get/query` | Drafting Table with project read visibility. | No expected version; return current request revision. | One request or filtered requests by refinement state, owner, priority, interface, scope, or relationship. | Return typed query failure, visibility-safe not-found, or WMS-unavailable result. |
| `work-item.get/query` | Drafting Table with project read visibility. | No expected version; return current contract version. | Work-item projection, status, owner/dependency summary, or filtered items. | Return typed visibility-safe not-found, invalid-query, or WMS-unavailable result; never mutate state. |
| `blocked-work.query` | Drafting Table on session start/resume or explicit user request. | No expected version; every item includes current state and contract version. | Sanitized blocked items with reason class, next action, dependencies, and resolution options. | Mark blocked-work status unavailable if WMS is unavailable; never claim review is complete. |
| `lifecycle.preflight` | Drafting Table, Job Site, or Materializer before an authoritative operation. | Caller snapshot includes expected state/version; no mutation key is required for a read-only preflight. | `authority: preflight` decision and diagnostics from the shared Validation Rules evaluator. | Advisory rejection only; the caller must still submit the authoritative operation. |
| `blocked-work.submit-resolution` | Drafting Table submits `add-requirement`, `out-of-scope`, or `impact-amendment` with Gate-bound human approval. | `expected_state: blocked`, `expected_contract_version`, approval-resolution digest, and idempotency key. | A durable resolution-submission record at `resolution_submission_revision: 1` (or its existing revision on replay); the work item remains `blocked` and its `contract_version` is unchanged until Materializer processing. | Return `UNAUTHORIZED_ACTION`, `STALE_STATE`, `STALE_CONTRACT_VERSION`, `PRECONDITION_FAILED`, or replay the exact prior result. |
| `blocked-work.acknowledge` | Drafting Table submits an informational acknowledgement with Gate-bound human approval. | `expected_state: blocked`, `expected_contract_version`, approval-resolution digest, and idempotency key. | A durable acknowledgement record at `resolution_submission_revision: 1`; the work item remains `blocked`. | Apply the same authorization, stale-state/version, precondition, and replay behavior as `blocked-work.submit-resolution`. |

The operation set is intentionally disjoint from Job Site execution. A
fake adapter must reject a Drafting Table caller attempting `claim`,
`renew-lease`, `tests-pass`, `begin-merge`, `record-merge`, finding-ledger
mutation, or materialization. Scheduling is Job Site policy and is not an
operation in the adapter contract.

Priority is a request-owned business value. Before materialization, an
authorized update refreshes the linked proposed change-set priority
snapshot. After a build work item exists, the same authorized operation
updates its WMS priority snapshot atomically with the request revision; it
does not change lifecycle state or dispatch/scheduling decisions.

---

## Request and result protocol

### Request envelope

Every operation carries a transport-neutral envelope:

```json
{
  "operation": "request.refine",
  "project_id": "fixture-project",
  "request_id": "request-001",
  "expected_request_revision": 1,
  "idempotency_key": "refine-request-001-v2",
  "payload": {}
}
```

The request envelope is untrusted input. The WMS Adapter resolves the
project and Gate-issued actor context from trusted configuration; subject,
role, allowed actions/refs, policy version, expiry, and approval bindings
are not caller-controlled request fields. Unknown operation names and
actions outside the context are rejected with `UNAUTHORIZED_ACTION`.
Lifecycle mutations use the Validation Rules request fields, including
expected state, `expected_contract_version`, and any required approval or
fencing token.

For `request.refine`, the Gate binds `human_approval_id` and
`approval_refinement_digest` to the approved human subject, request ID,
proposed refinement fields, delegated Drafting Table subject, expiry, and
single-use state. The adapter rejects a missing, mismatched, expired, or
already-consumed approval before changing the request.

### Result envelope

Successful and failed operations return one structured result:

```json
{
  "ok": true,
  "operation": "request.refine",
  "outcome": "applied",
  "resource": {},
  "request_revision": 2,
  "diagnostics": [],
  "mutation": "applied",
  "idempotency": "new"
}
```

`outcome` is `read`, `applied`, `replayed`, or `rejected`. `mutation` is
`none`, `applied`, `unknown`, or `not-applicable`. A replay returns the
original resource and revision without a second mutation. A preflight result
uses `outcome: read` and nests the full Validation Rules decision under a
`decision` field; the nested decision retains its own `outcome`,
`authority`, `rule_version`, and `replayed` fields. Errors use the
Validation Rules rejection codes for lifecycle operations and WMS-specific
stable codes for request/query operations.

### Failure behavior

| Failure | Result and retry |
| --- | --- |
| Missing/invalid authorization | `UNAUTHORIZED_ACTION`; obtain a new trusted context. |
| Request or target not visible | Visibility-safe `NOT_FOUND`; do not reveal whether another project owns it. |
| Invalid request/query | `INVALID_REQUEST`; correct the payload without mutation. |
| Stale request revision | `STALE_REQUEST_REVISION`; reread and present the changed request before retrying. |
| Stale work-item state/version | `STALE_STATE` or `STALE_CONTRACT_VERSION`; rerun preflight and require renewed review. |
| Duplicate/idempotency conflict | Exact key/fingerprint retries replay; a key fingerprint conflict returns `IDEMPOTENCY_CONFLICT`; a semantic duplicate under a new key returns `DUPLICATE_REQUEST`. |
| Validation Rules rejection | Preserve the shared rule version, code, details, and retry guidance; never convert it to success. |
| WMS unavailable | Return `WMS_UNAVAILABLE`; independent specification drafting may continue, but no WMS mutation or completed blocked-work review is claimed. |
| Unknown mutation result | Return `UNKNOWN_MUTATION`; reconcile with a read before retrying the same key. |

No failure may partially create a request, link, priority audit event, or
resolution submission. Backend-specific errors are translated into this
stable contract without exposing backend labels or raw credentials.

---

## Blocked-work resolution

The Drafting Table offers the five UX choices defined by its sibling
contract. The choices have distinct WMS behavior:

| User choice | Submission payload | Immediate WMS effect |
| --- | --- | --- |
| Add a requirement | `resolution_kind: add-requirement`, linked change-set ID, resolution digest | Submit reviewed resolution; Materializer later validates and refreshes the blocked item. |
| Approve out of scope | `resolution_kind: out-of-scope`, approved declaration/change-set ID, digest | Submit reviewed declaration; no direct state change. |
| Amend impact | `resolution_kind: impact-amendment`, linked change-set ID, digest | Submit reviewed impact amendment; expected state/version remain required. |
| Defer | No adapter operation; session-local decision only. | No WMS call or lifecycle mutation; item remains `blocked` and the session may continue. |
| Acknowledge informational block | `blocked-work.acknowledge`, control-plane reason, approval ID/digest | Records a resolution-submission audit record with no lifecycle transition; it never implies `ready-for-building`. |

For `blocked-work.submit-resolution` and `blocked-work.acknowledge`, the
Drafting Table submits a reviewed resolution with:

- the exact blocked work-item ID;
- `expected_state: blocked`;
- the exact `expected_contract_version` displayed to the user;
- a trusted `human_approval_id` and matching resolution digest; and
- an idempotency key stable across a lost response retry.

The WMS Adapter validates the resource write against the named preconditions
but does not pass it to the Validation Rules evaluator as `resolve-block`.
A successful submission writes only the resolution-submission record; it is
not an unblock. The Materializer later invokes the authoritative
`resolve-block` lifecycle operation, consumes the approval atomically, and
applies these outcomes:

- `add-requirement`: the original item records a dependency on the linked
  change-set build work and remains `waiting` until that dependency completes;
- `out-of-scope`: the item remains `blocked` until the required independent
  Inspector confirmation is recorded;
- `impact-amendment`: the linked change set must validate and its full refresh
  must pass before the item can become `ready-for-building`; and
- `acknowledge`: the item remains `blocked` until the control plane clears the
  informational condition.

Any failed refresh leaves the item `blocked` and returns the shared
diagnostic. The Materializer, not the Drafting Table, owns the lifecycle
transition and contract-version increment.

---

## Deployment, security, and state

The operation and result contract is the same in all deployment modes:

| Mode | Drafting Table to WMS path | Credential posture |
| --- | --- | --- |
| Single-player | Local WMS process over MCP stdio. | User-owned local credential; the agent receives no reusable downstream secret. |
| Multi-player | Project WMS network service. | OAuth 2.1 through the Bridge/Gate pattern. |
| Web | Shared WMS service with project/session authorization. | OAuth 2.1 through Bridge/Gate plus application authorization. |

Relevant persistent state remains owned by existing components:

| State | Owner | Drafting Table WMS contract |
| --- | --- | --- |
| Request backlog and request revisions | WMS Adapter/backend | Create, refine, link, query, and priority operations in this document. |
| Work-item lifecycle and contract versions | WMS Adapter/claim coordinator | Read-only projections plus validated blocked-resolution submissions. |
| Specification records and change sets | Git through `ears-manager` | Referenced by ID/commit; never parsed or edited through WMS operations. |
| Project/session registry | Deployment operator / registry; Web Drafting Table consumes it | Supplies trusted project visibility; never becomes work-item state. |
| Evidence, findings, and attestations | Job Site stores | Display only when authorized; no Drafting Table mutation. |

The WMS Adapter must preserve credential isolation, deny-by-default
authorization, expected-version checks, idempotency, and the Validation
Rules boundary in every mode. A backend's native assignment, label, or
transition must not be treated as a substitute for the contract.

---

## Fake adapter fixture

The fixture
[`fixtures/drafting-table-wms-golden.jsonl`](fixtures/drafting-table-wms-golden.jsonl)
is a harness-neutral transcript for a fake adapter. It contains no
backend-specific fields and requires no WMS service, Git host, OAuth token,
or network.

The `base-state` record defines fake Gate contexts. Operation records use
their role/project fields as references to those trusted contexts; they do
not model caller-supplied authorization claims.

The fixture asserts:

- request creation and refinement use request revisions;
- only a human maintainer can update business priority;
- linked priority snapshots update without changing lifecycle state;
- request-to-change-set and existing request-to-build-item links are
  possible without implicit materialization;
- status, dependency, and blocked-work queries are read-only;
- preflight is advisory and the authoritative resolution preserves
  `blocked` state/version checks;
- resolution submissions own a separate revision and leave the work item
  blocked until Materializer processing;
- an exact resolution retry replays without a second mutation;
- stale resolution state/version is rejected;
- a Drafting Table caller cannot claim, execute, complete, schedule, or
  mutate findings; and
- the Drafting Table operation set is a proper subset of the adapter API
  and disjoint from the Job Site execution set.

Every fixture result records the actor role, expected revision/version,
outcome, mutation status, and structured diagnostic. The fake adapter
implements only the contract; it does not simulate GitHub, Jira, or another
backend.

---

## Out-of-scope decisions

| Topic | Owner or reason |
| --- | --- |
| Backend-specific API, issue/card fields, labels, and webhook mechanics | WMS adapter implementation behind this contract. |
| Lifecycle state machine, rejection codes, fencing, and approval semantics | Validation Rules contract, #32. |
| Materialization and Job Site execution transitions | Job Site Materializer and Job Site contracts. |
| Finding Ledger schema and mutation routing | Job Site/Finding Router contract. |
| Request backlog UX and conversation presentation | Drafting Table UX, #28. |
| Specification reads/writes and impact computation | `ears-manager` CLI contract, #30. |
| Git branch, commit, PR, and merge operations | Git integration contract, #34. |
| Harness skill discovery and tool permissions | OpenCode adapter contract, #33. |
| Scheduling, WIP, assignment, and business-priority policy | Job Site policy; the Drafting Table only reads status and submits human priority updates. |

---

## Related Documents

- [Vision](../vision.md) — Purpose, users, outcomes, and prototype scope.
- [Architecture](../architecture.md) — External interfaces, persistent
  state, deployment modes, and environmental constraints.
- [Overview](overview.md) — Guiding principles, workflow, and platform.
- [System Components](components.md) — WMS Adapter API, component ownership,
  lifecycle, and Job Site boundaries.
- [Drafting Table UX](drafting-table-ux.md) — User-visible status,
  blocked-work resolution, and mutation ownership.
- [User Interaction Flow](user-interaction-flow.md) — Request refinement,
  change types, work-item lifecycle, and human review.
- [Validation Rules](validation-rules.md) — Shared authorization,
  concurrency, transition, and rejection semantics.
- [`ears-manager` CLI Integration Contract](ears-manager-cli.md) — Governed
  specification reads/writes and impact review.
- [Git and Project-Repository Integration](git-integration.md) — Branch,
  commit, PR, and approval registration behavior.
- [WMS Implementations](../../wms/README.md) — Backend implementation
  placement, without changing this contract.
