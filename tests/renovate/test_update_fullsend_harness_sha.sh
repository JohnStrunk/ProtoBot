#!/usr/bin/env bash
# tests/renovate/test_update_fullsend_harness_sha.sh
#
# Verifies scripts/renovate/update-fullsend-harness-sha.sh fetches harness
# files with curl (not gh) and updates #sha256= pins.
#
# Usage:
#     bash tests/renovate/test_update_fullsend_harness_sha.sh
#
# Exit 0  = all assertions passed
# Exit 1  = at least one assertion failed

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
SCRIPT="${REPO_ROOT}/scripts/renovate/update-fullsend-harness-sha.sh"
PASS=0
FAIL=0
TEST_ROOT=""
LAST_CASE=""
LAST_OUTPUT=""

cleanup() {
    if [[ -n "${TEST_ROOT:-}" ]]; then
        rm -rf "${TEST_ROOT}"
    fi
}
trap cleanup EXIT INT TERM HUP

TEST_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/harness-sha-test.XXXXXX")"

STALE_SHA="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
BODY=$'fixture harness body\n'
EXPECTED_SHA="$(printf '%s' "${BODY}" | sha256sum | cut -d' ' -f1)"
EXPECTED_URL="https://raw.githubusercontent.com/fullsend-ai/agents/v0.45.0/harness/fix.yaml"

write_harness() {
    local dest="$1"
    mkdir -p "$(dirname "${dest}")"
    cat > "${dest}" <<EOF
---
base: "https://raw.githubusercontent.com/fullsend-ai/agents/v0.45.0/harness/fix.yaml#sha256=${STALE_SHA}"
EOF
}

write_mock_curl() {
    local dest="$1"
    cat > "${dest}" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
url=""
auth_header=""
while [[ $# -gt 0 ]]; do
    case "$1" in
        -H)
            if [[ "${2:-}" == @* ]]; then
                auth_header_file="${2#@}"
                if [[ -f "${auth_header_file}" ]]; then
                    auth_header="$(cat "${auth_header_file}")"
                fi
            elif [[ "${2:-}" == Authorization:* ]]; then
                auth_header="$2"
            fi
            shift 2
            ;;
        --)
            url="${2:-}"
            shift 2
            break
            ;;
        -*)
            shift
            ;;
        *)
            url="$1"
            shift
            ;;
    esac
done

{
    printf 'url=%s\n' "${url}"
    printf 'auth=%s\n' "${auth_header}"
} >> "${MOCK_CURL_LOG}"

if [[ "${MOCK_CURL_FAIL:-}" == "1" ]]; then
    echo "curl: (22) The requested URL returned error: 404" >&2
    exit 22
fi

if [[ "${url}" != "${MOCK_CURL_EXPECTED_URL}" ]]; then
    echo "curl: (22) The requested URL returned error: 404" >&2
    exit 22
fi

cat "${MOCK_CURL_BODY}"
EOF
    chmod +x "${dest}"
}

write_mock_gh() {
    local dest="$1"
    cat > "${dest}" <<'EOF'
#!/usr/bin/env bash
echo "ERROR: gh should not be invoked" >&2
exit 127
EOF
    chmod +x "${dest}"
}

harness_sha() {
    sed -n 's/.*#sha256=\([a-f0-9]\{64\}\).*/\1/p' "$1"
}

indent() {
    while IFS= read -r line; do
        printf '    %s\n' "${line}"
    done
}

run_case() {
    local label="$1"
    local expect_rc="$2"
    shift 2

    local case_dir
    case_dir="$(mktemp -d "${TEST_ROOT}/case.XXXXXX")"
    mkdir -p "${case_dir}/bin" "${case_dir}/workspace/.fullsend/harness"

    write_harness "${case_dir}/workspace/.fullsend/harness/fix.yaml"
    write_mock_curl "${case_dir}/bin/curl"
    write_mock_gh "${case_dir}/bin/gh"
    printf '%s' "${BODY}" > "${case_dir}/body"
    : > "${case_dir}/curl.log"

    local rc=0
    local output=""
    output="$(
        cd "${case_dir}/workspace"
        env -u GH_TOKEN -u GITHUB_TOKEN \
            PATH="${case_dir}/bin:/usr/bin:/bin" \
            MOCK_CURL_LOG="${case_dir}/curl.log" \
            MOCK_CURL_BODY="${case_dir}/body" \
            MOCK_CURL_EXPECTED_URL="${EXPECTED_URL}" \
            "$@" \
            bash "${SCRIPT}" 2>&1
    )" || rc=$?

    LAST_CASE="${case_dir}"
    LAST_OUTPUT="${output}"

    if [[ "${rc}" -ne "${expect_rc}" ]]; then
        echo "FAIL  ${label}: expected exit ${expect_rc}, got ${rc}"
        echo "${output}" | tail -8 | indent
        return 1
    fi
    return 0
}

record() {
    local label="$1"
    local ok="$2"
    if [[ "${ok}" == "true" ]]; then
        echo "PASS  ${label}"
        PASS=$((PASS + 1))
    else
        FAIL=$((FAIL + 1))
    fi
}

# ── 1. Updates the pin without gh or a GitHub token ───────────

label="updates-sha-without-gh-or-token"
if run_case "${label}" 0; then
    ok=true
    got_sha="$(harness_sha "${LAST_CASE}/workspace/.fullsend/harness/fix.yaml")"
    if [[ "${got_sha}" != "${EXPECTED_SHA}" ]]; then
        echo "FAIL  ${label}: expected sha256=${EXPECTED_SHA}, got ${got_sha}"
        ok=false
    fi
    if ! grep -q "Updated .fullsend/harness/fix.yaml: sha256=${EXPECTED_SHA}" <<<"${LAST_OUTPUT}"; then
        echo "FAIL  ${label}: missing update log line"
        echo "${LAST_OUTPUT}" | indent
        ok=false
    fi
    if grep -q '^auth=Authorization:' "${LAST_CASE}/curl.log"; then
        echo "FAIL  ${label}: Authorization header sent without a token"
        ok=false
    fi
    if grep -q 'gh should not be invoked' <<<"${LAST_OUTPUT}"; then
        echo "FAIL  ${label}: script invoked gh"
        ok=false
    fi
    record "${label}" "${ok}"
else
    record "${label}" false
fi

# ── 2. Does not send Authorization when GH_TOKEN is set ───────

label="does-not-send-authorization-with-gh-token"
if run_case "${label}" 0 GH_TOKEN=test-token; then
    ok=true
    got_sha="$(harness_sha "${LAST_CASE}/workspace/.fullsend/harness/fix.yaml")"
    if [[ "${got_sha}" != "${EXPECTED_SHA}" ]]; then
        echo "FAIL  ${label}: expected sha256=${EXPECTED_SHA}, got ${got_sha}"
        ok=false
    fi
    if ! grep -q "Updated .fullsend/harness/fix.yaml: sha256=${EXPECTED_SHA}" <<<"${LAST_OUTPUT}"; then
        echo "FAIL  ${label}: missing update log line"
        echo "${LAST_OUTPUT}" | indent
        ok=false
    fi
    if grep -q '^auth=Authorization:' "${LAST_CASE}/curl.log"; then
        echo "FAIL  ${label}: Authorization header was sent when GH_TOKEN was set"
        ok=false
    fi
    record "${label}" "${ok}"
else
    record "${label}" false
fi

# ── 3. Does not send Authorization when GITHUB_TOKEN is set ───

label="does-not-send-authorization-with-github-token"
if run_case "${label}" 0 GITHUB_TOKEN=test-token; then
    ok=true
    got_sha="$(harness_sha "${LAST_CASE}/workspace/.fullsend/harness/fix.yaml")"
    if [[ "${got_sha}" != "${EXPECTED_SHA}" ]]; then
        echo "FAIL  ${label}: expected sha256=${EXPECTED_SHA}, got ${got_sha}"
        ok=false
    fi
    if ! grep -q "Updated .fullsend/harness/fix.yaml: sha256=${EXPECTED_SHA}" <<<"${LAST_OUTPUT}"; then
        echo "FAIL  ${label}: missing update log line"
        echo "${LAST_OUTPUT}" | indent
        ok=false
    fi
    if grep -q '^auth=Authorization:' "${LAST_CASE}/curl.log"; then
        echo "FAIL  ${label}: Authorization header was sent when GITHUB_TOKEN was set"
        ok=false
    fi
    record "${label}" "${ok}"
else
    record "${label}" false
fi

# ── 4. Fails closed when the remote fetch fails ───────────────

label="fails-when-curl-returns-http-error"
if run_case "${label}" 22 MOCK_CURL_FAIL=1; then
    ok=true
    got_sha="$(harness_sha "${LAST_CASE}/workspace/.fullsend/harness/fix.yaml")"
    if [[ "${got_sha}" != "${STALE_SHA}" ]]; then
        echo "FAIL  ${label}: harness file was modified on fetch failure"
        ok=false
    fi
    record "${label}" "${ok}"
else
    record "${label}" false
fi

# ── 5. Fails clearly when curl is missing ─────────────────────

label="fails-when-curl-missing"
case_dir="$(mktemp -d "${TEST_ROOT}/case.XXXXXX")"
mkdir -p "${case_dir}/bin" "${case_dir}/tools" "${case_dir}/workspace/.fullsend/harness"
write_harness "${case_dir}/workspace/.fullsend/harness/fix.yaml"
write_mock_gh "${case_dir}/bin/gh"
for cmd in bash grep sed sha256sum cut printf cat; do
    src="$(command -v "${cmd}")"
    ln -s "${src}" "${case_dir}/tools/${cmd}"
done
rc=0
output="$(
    cd "${case_dir}/workspace"
    env -u GH_TOKEN -u GITHUB_TOKEN \
        PATH="${case_dir}/bin:${case_dir}/tools" \
        bash "${SCRIPT}" 2>&1
)" || rc=$?
ok=true
if [[ "${rc}" -eq 0 ]]; then
    echo "FAIL  ${label}: expected non-zero exit when curl is missing, got 0"
    echo "${output}" | indent
    ok=false
elif ! grep -q 'curl is required' <<<"${output}"; then
    echo "FAIL  ${label}: expected a curl-is-required error"
    echo "${output}" | indent
    ok=false
fi
record "${label}" "${ok}"

# ── Summary ──────────────────────────────────────────────────

echo ""
echo "════════════════════════════════════════════════════════"
echo "Results: ${PASS} passed, ${FAIL} failed"

if [[ "${FAIL}" -gt 0 ]]; then
    exit 1
fi
