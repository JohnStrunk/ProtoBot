# Eliciting Requirements Evaluation

This directory contains the Agent Eval Harness configuration for
`.agents/skills/eliciting-requirements/SKILL.md`.

## Corpus

- `dataset/cases/` contains visible development cases. Each case has an
  `input.yaml`, `annotations.yaml`, and property-based `reference.md`.
- `dataset/regression/` contains held-out cases. Run them with
  `eval-regression.yaml`; do not use them to tune the visible corpus.
- The two semantic rubrics are inline in the configs so nested config paths
  resolve correctly from both the repository root and Agent Eval Harness.
  Structural EARS and readiness checks remain inline as deterministic judges.
- The CLI runner temporarily removes `reference.md` and `annotations.yaml`
  from each case workspace while the skill runs, then restores them for
  collection and scoring. This prevents the evaluated skill from reading its
  answer key without relying on runner-specific permission forwarding.

The references describe semantic properties and acceptable behavior. They do
not require one exact wording, because multiple EARS sentences can preserve
the same intent.

## Harness Workflow

From the repository root, install or load Agent Eval Harness and run:

```text
/eval-setup
/eval-run --config eval/eliciting-requirements/eval.yaml --model google-vertex/claude-sonnet-4-6@default --run-id v2
/eval-review --config eval/eliciting-requirements/eval.yaml --run-id v1
/eval-run --config eval/eliciting-requirements/eval-regression.yaml --model google-vertex/claude-sonnet-4-6@default --run-id v1-regression
```

Use `/eval-dataset` only to add reviewed cases. A confirmed failure becomes a
new case in `dataset/regression/` before a skill change is accepted.

## Baselines

`baselines/v1/` records the corpus, harness revision, model settings, judge
thresholds, and the commands required to create the first live baseline. The
case-level semantic expectations are versioned in the corpus annotations. A
live run must save the harness `summary.yaml`, case outputs, findings, traces,
cost, latency, and human calibration artifacts without overwriting this
baseline. Later baselines are new directories.
