#!/usr/bin/env bash
# tests/lint/test_violations.sh
#
# Reproducible fixture that verifies scripts/lint.py catches
# representative violations for each supported check category.
#
# Usage:
#     bash tests/lint/test_violations.sh
#
# Exit 0  = all assertions passed
# Exit 1  = at least one assertion failed

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
LINT="${REPO_ROOT}/scripts/lint.py"
PASS=0
FAIL=0
SKIPPED=0
# Track test fixture files created in the repo so they can be
# cleaned up even if the script is interrupted.
CREATED_FILES=()

cleanup() {
    for f in "${CREATED_FILES[@]}"; do
        rm -f "${f}"
    done
}
trap cleanup EXIT INT TERM HUP

# ── Helpers ────────────────────────────────────────────────────

# Create a temp file inside the repo (tools need repo-relative
# paths), run lint.py --files on it, and check for the expected
# exit code and output substring.
assert_lint() {
    local label="$1"
    local filename="$2"   # relative to repo root
    local content="$3"    # file content
    local expect_rc="$4"  # 0 or nonzero
    local expect_str="${5:-}"  # substring to find in output
    local expect_diag="${6:-}" # diagnostic substring to find in output

    local filepath="${REPO_ROOT}/${filename}"
    mkdir -p "$(dirname "${filepath}")"
    printf '%s' "${content}" > "${filepath}"
    CREATED_FILES+=("${filepath}")

    local rc=0
    local output
    output="$(python3 "${LINT}" --files "${filename}" 2>&1)" || rc=$?

    rm -f "${filepath}"

    local ok=true

    if [[ "${expect_rc}" == "0" ]] && [[ "${rc}" -ne 0 ]]; then
        echo "FAIL  ${label}: expected exit 0, got ${rc}"
        ok=false
    elif [[ "${expect_rc}" != "0" ]] && [[ "${rc}" -eq 0 ]]; then
        echo "FAIL  ${label}: expected non-zero exit, got 0"
        ok=false
    fi

    # Strip ANSI colour codes for reliable substring matching.
    local clean_output
    # shellcheck disable=SC2001  # regex substitution requires sed
    clean_output="$(echo "${output}" | sed 's/\x1b\[[0-9;]*m//g')"

    if [[ -n "${expect_str}" ]] && ! echo "${clean_output}" | grep -qE "(✓|✗|○|\?|!) ${expect_str}( |$)"; then
        echo "FAIL  ${label}: expected output to contain '${expect_str}'"
        echo "  actual output (last 5 lines):"
        echo "${output}" | tail -5 | sed 's/^/    /'
        ok=false
    fi

    if [[ -n "${expect_diag}" ]] && ! echo "${clean_output}" | grep -qF "${expect_diag}"; then
        echo "FAIL  ${label}: expected output to contain diagnostic '${expect_diag}'"
        echo "  actual output (last 5 lines):"
        echo "${output}" | tail -5 | sed 's/^/    /'
        ok=false
    fi

    # When expecting a failure, verify the expected hook was the one
    # that actually failed (not just that the name appears somewhere).
    # If the hook is unavailable (tool not installed), treat the test
    # as skipped — violation detection can only be verified when the
    # tool is present.
    if [[ "${expect_rc}" != "0" ]] && [[ -n "${expect_str}" ]]; then
        if echo "${clean_output}" | grep -qE "! ${expect_str}( |$)"; then
            echo "SKIP  ${label}: ${expect_str} tool not available"
            SKIPPED=$((SKIPPED + 1))
            return
        fi
        if ! echo "${clean_output}" | grep -qE "✗ ${expect_str}( |$)"; then
            echo "FAIL  ${label}: expected '${expect_str}' to be the failing hook"
            echo "  actual output (last 5 lines):"
            echo "${output}" | tail -5 | sed 's/^/    /'
            ok=false
        fi
    fi

    if ${ok}; then
        echo "PASS  ${label}"
        PASS=$((PASS + 1))
    else
        FAIL=$((FAIL + 1))
    fi
}

cd "${REPO_ROOT}"

# ── Parity check ──────────────────────────────────────────────

echo "── Parity ──────────────────────────────────────────────"
parity_rc=0
parity_output="$(python3 "${LINT}" --check-parity 2>&1)" || parity_rc=$?
if [[ "${parity_rc}" -eq 0 ]] && echo "${parity_output}" | grep -q "PARITY CHECK PASSED"; then
    echo "PASS  parity: all hooks have registered handlers"
    PASS=$((PASS + 1))
else
    echo "FAIL  parity: some hooks lack registered handlers (exit code: ${parity_rc})"
    echo "${parity_output}" | tail -5 | sed 's/^/    /'
    FAIL=$((FAIL + 1))
fi

# ── YAML violations ──────────────────────────────────────────

echo ""
echo "── YAML ────────────────────────────────────────────────"

assert_lint \
    "yaml-valid" \
    "_test_lint_valid.yaml" \
    $'---\nkey: value\n' \
    0

assert_lint \
    "yaml-bad-indent" \
    "_test_lint_bad.yaml" \
    $'---\nbad:\n yaml:\n   wrong: true\n' \
    nonzero \
    "yamllint"

# ── JSON violations ──────────────────────────────────────────

echo ""
echo "── JSON ────────────────────────────────────────────────"

assert_lint \
    "json-valid" \
    "_test_lint_valid.json" \
    $'{"key": "value"}\n' \
    0

assert_lint \
    "json-invalid" \
    "_test_lint_bad.json" \
    '{ invalid json' \
    nonzero \
    "check-json"

# ── Markdown violations ──────────────────────────────────────

echo ""
echo "── Markdown ──────────────────────────────────────────────"

assert_lint \
    "md-valid" \
    "_test_lint_valid.md" \
    $'# Valid heading\n\nSome text.\n' \
    0

assert_lint \
    "md-trailing-ws" \
    "_test_lint_ws.md" \
    $'# Heading\n\nTrailing spaces   \n' \
    nonzero \
    "trailing-whitespace"

# ── Shell violations ─────────────────────────────────────────

echo ""
echo "── Shell ─────────────────────────────────────────────────"

assert_lint \
    "sh-valid" \
    "_test_lint_valid.sh" \
    $'#!/usr/bin/env bash\nset -euo pipefail\necho "hello"\n' \
    0

assert_lint \
    "sh-unquoted-var" \
    "_test_lint_bad.sh" \
    $'#!/bin/bash\necho $unquoted_var\n' \
    nonzero \
    "shellcheck"

# ── JSON5 violations ─────────────────────────────────────────

echo ""
echo "── JSON5 ─────────────────────────────────────────────────"

assert_lint \
    "json5-valid" \
    "_test_lint_valid.json5" \
    $'{key: "value", /* comment */ trailing: 1,}\n' \
    0

assert_lint \
    "json5-invalid" \
    "_test_lint_bad.json5" \
    $'{key: value: invalid}\n' \
    nonzero \
    "check-json5"

# ── End-of-file fixer ────────────────────────────────────────

echo ""
echo "── End-of-file ────────────────────────────────────────────"

assert_lint \
    "eof-correct" \
    "_test_lint_eof_ok.txt" \
    $'has newline\n' \
    0

assert_lint \
    "eof-missing-newline" \
    "_test_lint_eof_bad.txt" \
    "no final newline" \
    nonzero \
    "end-of-file-fixer"

# ── Secrets violations ───────────────────────────────────

echo ""
echo "── Secrets ─────────────────────────────────────────────"

# detect-secrets should flag a private key header regardless of
# baseline contents — PrivateKeyDetector matches the PEM marker.
# Content stored in a variable so the pragma can sit on the same line.
_secret_fixture=$'# Test file\n-----BEGIN RSA PRIVATE KEY-----\nMIIBog\n-----END RSA PRIVATE KEY-----\n'  # pragma: allowlist secret
assert_lint \
    "secrets-private-key" \
    "_test_lint_secret.py" \
    "${_secret_fixture}" \
    nonzero \
    "detect-secrets"

# ── Workflow violations ──────────────────────────────────

echo ""
echo "── Workflow (zizmor) ─────────────────────────────────────"

# zizmor should flag template injection: an untrusted input from
# pull_request_target used directly in a run: step.
assert_lint \
    "workflow-template-injection" \
    ".github/workflows/_test_lint_bad.yml" \
    $'name: Test\non:\n  pull_request_target:\n    types: [opened]\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo "${{ github.event.pull_request.title }}"\n' \
    nonzero \
    "zizmor"

# ── Spec hierarchy sync ─────────────────────────────────────

echo ""
echo "── Spec hierarchy sync ─────────────────────────────────"

# Compare two fixture files via the checker CLI (not lint.py
# --files): the hook always reads the canonical repo paths, so
# drift cases have to be exercised against temp copies.
assert_hierarchy() {
    local label="$1"
    local agents_content="$2"
    local review_content="$3"
    local expect_rc="$4"  # 0 or nonzero
    local expect_str="${5:-}"

    local tmp
    tmp="$(mktemp -d)"
    printf '%s\n' "${agents_content}" > "${tmp}/AGENTS.md"
    printf '%s\n' "${review_content}" > "${tmp}/review.yaml"

    local rc=0
    local output
    output="$(python3 "${REPO_ROOT}/scripts/check_spec_hierarchy.py" \
        --agents "${tmp}/AGENTS.md" \
        --review "${tmp}/review.yaml" 2>&1)" || rc=$?
    rm -rf "${tmp}"

    local ok=true
    if [[ "${expect_rc}" == "0" ]] && [[ "${rc}" -ne 0 ]]; then
        echo "FAIL  ${label}: expected exit 0, got ${rc}"
        echo "  actual output (last 5 lines):"
        echo "${output}" | tail -5 | sed 's/^/    /'
        ok=false
    elif [[ "${expect_rc}" != "0" ]] && [[ "${rc}" -eq 0 ]]; then
        echo "FAIL  ${label}: expected non-zero exit, got 0"
        ok=false
    fi
    if [[ -n "${expect_str}" ]] && ! echo "${output}" | grep -qF "${expect_str}"; then
        echo "FAIL  ${label}: expected output to contain '${expect_str}'"
        echo "  actual output (last 5 lines):"
        echo "${output}" | tail -5 | sed 's/^/    /'
        ok=false
    fi
    if ${ok}; then
        echo "PASS  ${label}"
        PASS=$((PASS + 1))
    else
        FAIL=$((FAIL + 1))
    fi
}

# Fixtures use ANSI-C quoting so backticks stay literal (markdown code).
# shellcheck disable=SC2016
_HIERARCHY_AGENTS_OK=$'# Title

## Specification document hierarchy

Intro mentioning `docs/` membership.

- `docs/vision.md` — project Vision (purpose, users,
  outcomes).
- [`docs/architecture/git-integration.md`][git-integration-doc] — git.
- `docs/decisions/` — ADRs.

[git-integration-doc]: docs/architecture/git-integration.md

### Other section

- `docs/not-in-hierarchy.md` — must be ignored.
'

_HIERARCHY_REVIEW_OK='env:
    sandbox:
        REVIEW_SPEC_HIERARCHY: "docs/vision.md,docs/architecture/git-integration.md,docs/decisions/"
'

assert_hierarchy \
    "hierarchy-match" \
    "${_HIERARCHY_AGENTS_OK}" \
    "${_HIERARCHY_REVIEW_OK}" \
    0

assert_hierarchy \
    "hierarchy-order-independent" \
    "${_HIERARCHY_AGENTS_OK}" \
    'env:
    sandbox:
        REVIEW_SPEC_HIERARCHY: "docs/decisions/,docs/vision.md,docs/architecture/git-integration.md"
' \
    0

# Future hierarchy addition left out of review.yaml.
# shellcheck disable=SC2016
_HIERARCHY_AGENTS_NEW=$'# Title

## Specification document hierarchy

Intro mentioning `docs/` membership.

- `docs/vision.md` — project Vision (purpose, users,
  outcomes).
- [`docs/architecture/git-integration.md`][git-integration-doc] — git.
- `docs/decisions/` — ADRs.
- `docs/architecture/new.md` — new.

[git-integration-doc]: docs/architecture/git-integration.md

### Other section

- `docs/not-in-hierarchy.md` — must be ignored.
'

assert_hierarchy \
    "hierarchy-missing-from-review" \
    "${_HIERARCHY_AGENTS_NEW}" \
    "${_HIERARCHY_REVIEW_OK}" \
    nonzero \
    "docs/architecture/new.md"

assert_hierarchy \
    "hierarchy-extra-in-review" \
    "${_HIERARCHY_AGENTS_OK}" \
    'env:
    sandbox:
        REVIEW_SPEC_HIERARCHY: "docs/vision.md,docs/architecture/git-integration.md,docs/decisions/,docs/extra.md"
' \
    nonzero \
    "docs/extra.md"

assert_hierarchy \
    "hierarchy-missing-heading" \
    $'# Title

- `docs/vision.md` — vision.
' \
    "${_HIERARCHY_REVIEW_OK}" \
    nonzero \
    "Specification document hierarchy"

assert_hierarchy \
    "hierarchy-missing-key" \
    "${_HIERARCHY_AGENTS_OK}" \
    'env:
    sandbox:
        TIMEOUT_SECONDS: "2700"
' \
    nonzero \
    "REVIEW_SPEC_HIERARCHY"

# Canonical repo files must already be in sync.
hierarchy_rc=0
hierarchy_output="$(python3 "${REPO_ROOT}/scripts/check_spec_hierarchy.py" 2>&1)" || hierarchy_rc=$?
if [[ "${hierarchy_rc}" -eq 0 ]]; then
    echo "PASS  hierarchy-real-files: AGENTS.md matches review.yaml"
    PASS=$((PASS + 1))
else
    echo "FAIL  hierarchy-real-files: expected exit 0, got ${hierarchy_rc}"
    echo "${hierarchy_output}" | tail -5 | sed 's/^/    /'
    FAIL=$((FAIL + 1))
fi

# lint.py must wire the local hook when AGENTS.md is in the file set.
lint_hier_rc=0
lint_hier_output="$(python3 "${LINT}" --files AGENTS.md 2>&1)" || lint_hier_rc=$?
# shellcheck disable=SC2001  # regex substitution requires sed
lint_hier_clean="$(echo "${lint_hier_output}" | sed 's/\x1b\[[0-9;]*m//g')"
if [[ "${lint_hier_rc}" -eq 0 ]] && echo "${lint_hier_clean}" | grep -qE "✓ spec-hierarchy-sync( |$)"; then
    echo "PASS  hierarchy-lint-wiring: lint.py runs spec-hierarchy-sync"
    PASS=$((PASS + 1))
else
    echo "FAIL  hierarchy-lint-wiring: spec-hierarchy-sync did not pass (exit ${lint_hier_rc})"
    echo "${lint_hier_output}" | tail -8 | sed 's/^/    /'
    FAIL=$((FAIL + 1))
fi

# ── Go formatting and vet ───────────────────────────────────

echo ""
echo "── Go ──────────────────────────────────────────────────"

assert_lint \
    "go-fmt-valid" \
    "tests/lint/fixtures/linttest_valid.go" \
    $'package linttest\n\nfunc Hello() {\n}\n' \
    0 \
    "gofmt"

assert_lint \
    "go-fmt-unformatted" \
    "tests/lint/fixtures/linttest_unformatted.go" \
    $'package linttest\n\nfunc Hello() {\n\tvar x int=1\n\t_ = x\n}\n' \
    nonzero \
    "gofmt"

assert_lint \
    "go-vet-printf" \
    "tests/lint/fixtures/linttest_vet.go" \
    $'package linttest\n\nimport "fmt"\n\nfunc Hello() {\n\tfmt.Printf("%d", "not an int")\n}\n' \
    nonzero \
    "go-vet" \
    "wrong type"

# lint.py must wire the local Go hooks when a real Go file is in
# the file set (not only the temp fixtures above).
lint_go_rc=0
lint_go_output="$(python3 "${LINT}" --files wms/memory/lifecycle.go 2>&1)" || lint_go_rc=$?
# shellcheck disable=SC2001  # regex substitution requires sed
lint_go_clean="$(echo "${lint_go_output}" | sed 's/\x1b\[[0-9;]*m//g')"
if [[ "${lint_go_rc}" -eq 0 ]] \
    && echo "${lint_go_clean}" | grep -qE "✓ gofmt( |$)" \
    && echo "${lint_go_clean}" | grep -qE "✓ go-vet( |$)"; then
    echo "PASS  go-hooks-wiring: lint.py runs gofmt and go-vet"
    PASS=$((PASS + 1))
else
    echo "FAIL  go-hooks-wiring: gofmt/go-vet did not pass (exit ${lint_go_rc})"
    echo "${lint_go_output}" | tail -8 | sed 's/^/    /'
    FAIL=$((FAIL + 1))
fi

# ears-manager check (module with third-party dependencies):
# verify go-vet either succeeds (with populated cache/network) or
# fails closed with the dependency resolution diagnostic banner.
lint_ears_rc=0
lint_ears_output="$(python3 "${LINT}" --files "ears-manager/cmd/ears-manager/main.go" 2>&1)" || lint_ears_rc=$?

# shellcheck disable=SC2001  # regex substitution requires sed
lint_ears_clean="$(echo "${lint_ears_output}" | sed 's/\x1b\[[0-9;]*m//g')"
if [[ "${lint_ears_rc}" -eq 0 ]] && echo "${lint_ears_clean}" | grep -qE "✓ go-vet( |$)"; then
    echo "PASS  go-vet-ears-manager: go-vet passed with populated cache/network"
    PASS=$((PASS + 1))
elif [[ "${lint_ears_rc}" -ne 0 ]] && echo "${lint_ears_clean}" | grep -qF "dependency resolution failed in ears-manager"; then
    echo "PASS  go-vet-ears-manager: go-vet failed closed with dependency resolution diagnostic"
    PASS=$((PASS + 1))
else
    echo "FAIL  go-vet-ears-manager: expected go-vet success or dependency-resolution diagnostic (exit ${lint_ears_rc})"
    echo "${lint_ears_output}" | tail -8 | sed 's/^/    /'
    FAIL=$((FAIL + 1))
fi

# ── Summary ──────────────────────────────────────────────────

echo ""
echo "════════════════════════════════════════════════════════"
echo "Results: ${PASS} passed, ${FAIL} failed, ${SKIPPED} skipped"

if [[ "${FAIL}" -gt 0 ]]; then
    exit 1
fi

# Guard against vacuous success: if no tests passed and some were
# skipped (all tools unavailable), the suite provides no regression
# value — fail so the environment issue is surfaced.
if [[ "${PASS}" -eq 0 ]] && [[ "${SKIPPED}" -gt 0 ]]; then
    echo "ERROR: no tests passed (${SKIPPED} skipped) — tools may be missing"
    exit 1
fi
