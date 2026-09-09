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
TMPDIR_BASE="$(mktemp -d)"
PASS=0
FAIL=0

cleanup() { rm -rf "${TMPDIR_BASE}"; }
trap cleanup EXIT

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

    local filepath="${REPO_ROOT}/${filename}"
    mkdir -p "$(dirname "${filepath}")"
    printf '%s' "${content}" > "${filepath}"

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

cd "${REPO_ROOT}"

# ── Parity check ──────────────────────────────────────────────

echo "── Parity ──────────────────────────────────────────────"
if python3 "${LINT}" --check-parity 2>&1 | grep -q "PARITY CHECK PASSED"; then
    echo "PASS  parity: all hooks have registered handlers"
    PASS=$((PASS + 1))
else
    echo "FAIL  parity: some hooks lack registered handlers"
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

# ── Summary ──────────────────────────────────────────────────

echo ""
echo "════════════════════════════════════════════════════════"
echo "Results: ${PASS} passed, ${FAIL} failed"

if [[ "${FAIL}" -gt 0 ]]; then
    exit 1
fi
