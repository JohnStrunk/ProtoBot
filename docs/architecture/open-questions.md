# ProtoBot: Open Design Questions

> Design document — draft, July 2026

Questions that are not yet resolved. Updated as decisions are made.

---

### Interactive phase

1. **Spec gap surfacing UX** — How does the agent present unspecified
   behaviors during Dimensioning? Inline suggestions? Separate "gap
   report"? What's the interaction pattern for the user to say "out of
   scope" vs. "add a requirement for that"?

2. **Async requirement suggestion delivery** — When the autonomous
   phase discovers unspecified behavior, it blocks the work item and
   escalates to the user (decided). The remaining questions: what
   notification channel delivers the escalation (email, Slack,
   webhook)? Is the TUI's pull-on-start model fast enough for all
   escalation types, or do some need the web UI's push model? See
   [Drafting Table](components.md#drafting-table).

3. **Multi-interface orchestration** — When a project has many
   interfaces (API + CLI + web GUI), does the user dimension them
   sequentially or can they jump between interfaces? Is there a
   dependency graph?

4. **IdeaBot handoff format** — The handoff mechanism is decided
   (manual: IdeaBot artifacts seed the initial Sketching session;
   automated integration later). Remaining question: how much of the
   Vision/Architecture can be pre-populated from IdeaBot's output?

### Specification storage

7. **Requirements storage format** — JSONL is tentatively chosen for
   git-friendliness, but only works if requirements are independent
   records. Need to evaluate whether cross-references or hierarchy
   make a different format necessary. `ears-manager` abstracts the
   format, so this can be deferred. See the detailed note in
   [Phase 2](user-interaction-flow.md#phase-2-dimensioning).

### Building phase

8. **Building merge infrastructure** — Worker B must produce a
   runnable artifact (container, listening API, executable CLI) before
   Worker A's tests can run. The merge step needs build/start
   orchestration, not just file concatenation. See
   [Phase 3](user-interaction-flow.md#phase-3-building-autonomous).

9. **Isolated vs. implementation-aware tests** — A three-category
   taxonomy is established: (1) isolated interface tests (default,
   dual-model isolation holds), (2) implementation-aware tests (DI /
   mock time, isolation breaks), (3) mutation testing (hidden audit).
   Remaining questions: what fraction of requirements fall into
   category 2? How do we decide which category a requirement falls
   into? How do we mitigate oracle gaming for category 2? See
   [Phase 3](user-interaction-flow.md#phase-3-building-autonomous).

10. **Mutation testing triage oracle** — A surviving mutant tells us
    "a test didn't catch this change" but not *why* (equivalent
    mutant, weak tests, or unspecified behavior). Distinguishing the
    three cases requires knowing whether the mutated program still
    conforms to the spec — the very oracle we lack. Mutation testing
    is still valuable as a coverage signal in aggregate, but how do
    we triage individual mutants at scale? Inspector agent heuristics?
    Human review? Accept that some mutants are noise? See
    [Phase 3](user-interaction-flow.md#phase-3-building-autonomous).

15. **Phase 3 triage mechanism** — When tests fail after merging
    Worker A's tests with Worker B's code, who or what decides
    whether the fault is in the tests, the code, or both? Options
    identified: another agent, a heuristic, or a hybrid. Forge's
    pattern (self-review 2 passes → CI fix loop 5 retries → AI
    review → human gate) is a concrete reference. The triage
    mechanism has access to both Workers' outputs, which is a tension
    with the isolation model. See
    [Phase 3](user-interaction-flow.md#phase-3-building-autonomous)
    and [Forge](related-work.md#forge-red-hat-israel--openstackshift-on-stack).

19. **Worker discovery of applicable existing requirements** —
    Workers must consider not just the requirements on the current
    work item but also existing requirements whose scope covers the
    work being done (ongoing obligations — e.g., "All CLI
    subcommands shall provide `--help`"). `ears-manager list` supports
    filtering by EARS pattern type (`--pattern ubiquitous`) and by
    interface (`--interface cli`), which gives Workers a concrete
    mechanism: pull all ubiquitous and state-driven requirements for
    the interfaces the work item touches as likely candidates for
    follow-up. Remaining question: is this filter-based approach
    sufficient, or does the Worker also need to evaluate
    event-driven and optional-feature requirements for applicability?
    How much of this discovery should be mechanical (in the
    Specification Toolkit or `ears-manager`) vs. left to the Worker
    agent's judgment? See
    [ongoing obligations](user-interaction-flow.md#ongoing-obligations).

### Inspecting phase

11. **Test Completeness Inspector placement** — Should test
    completeness checking happen in Phase 3 (Building, so gaps are
    caught and filled earlier in the loop) or Phase 4 (Inspecting,
    as an independent review)? Placing it in Phase 3 means faster
    feedback but the checker is no longer independent of the Workers.
    See [Phase 4](user-interaction-flow.md#phase-4-inspecting-autonomous).

12. **Inspector roster per project type** — Not every Inspector is
    relevant for every prototype. A CLI tool doesn't need an
    Accessibility Inspector; a library doesn't need a Security
    Inspector scanning for OWASP web vulnerabilities. Two approaches
    identified: derived from the interface types in the Architecture,
    or config-driven user selection (Swarm Forge's model). See
    [Phase 4](user-interaction-flow.md#phase-4-inspecting-autonomous)
    and [Swarm Forge](related-work.md#swarm-forge-robert-c-martin).

14. **Inspection report format** — The report is currently described
    as free-form. Should it have a structured schema (defect type,
    severity, location, remediation suggestion) to make it
    machine-actionable for the Phase 3 Workers? Or is free-form
    sufficient since the Workers are LLM agents that can parse
    natural language? See
    [Phase 4](user-interaction-flow.md#phase-4-inspecting-autonomous).

### Compliance

13. **HU-02 compliance** — The multi-player workflow places the
    human checkpoint at PR merge time (when specs land on main).
    Everything after is autonomous execution of approved intent.
    This may satisfy HU-02, since the human explicitly approved the
    specifications that drive all subsequent autonomous action.
    However, this needs confirmation — ProtoBot's autonomous code
    generation likely rates "High risk" on the AI Agent Risk
    Evaluator, which may make an additional checkpoint unavoidable.

### Interface specifications

18. **CLI interface spec evaluation** — `usage` (jdx.dev), docopt,
    and `wasi:cli` are listed as candidates for CLI interface
    specification. Need to evaluate which (if any) is suitable for
    ProtoBot's needs. See the interface-type taxonomy in
    [Specification Hierarchy](user-interaction-flow.md#specification-hierarchy).

---

## Related Documents

- [Overview](overview.md) — What ProtoBot is, guiding principles,
  and workflow summary
- [User Interaction Flow](user-interaction-flow.md) — Phase details
  and sequence diagrams
- [System Components](components.md) — Component architecture,
  interfaces, and cross-cutting concerns
- [Related Work](related-work.md) — Red Hat internal projects,
  external factory projects, and lessons learned
