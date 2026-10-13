#!/usr/bin/env bash
set -euo pipefail

# Called by Renovate postUpgradeTasks after a fullsend-ai/agents version bump.
# Recomputes SHA256 hashes in .fullsend/harness/*.yaml base: URLs.
# Uses curl (not gh) so the script runs inside renovatebot/github-action.

if ! command -v curl >/dev/null 2>&1; then
  echo "ERROR: curl is required to fetch harness files" >&2
  exit 1
fi

fetch_harness() {
  local url="$1"
  curl -fsSL -- "${url}"
}

hash_sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum | cut -d' ' -f1
  else
    shasum -a 256 | cut -d' ' -f1
  fi
}

for harness_file in .fullsend/harness/*.yaml; do
  [[ -f "$harness_file" ]] || continue

  base_line=$(grep -m1 -E '^[[:space:]]*base:' "$harness_file" || true)
  if [[ -z "$base_line" ]]; then
    echo "ERROR: Missing base URL in ${harness_file}" >&2
    exit 1
  fi
  base_url=$(printf '%s\n' "$base_line" | sed 's/^[[:space:]]*base:[[:space:]]*"//;s/"[[:space:]]*$//')
  if [[ ! "$base_url" =~ ^https://raw\.githubusercontent\.com/fullsend-ai/agents/v([0-9]+\.[0-9]+\.[0-9]+)/([a-zA-Z0-9/_.-]+)#sha256=([a-f0-9]{64})$ ]]; then
    echo "ERROR: Invalid Fullsend base URL in ${harness_file}" >&2
    exit 1
  fi
  version="${BASH_REMATCH[1]}"
  file_path="${BASH_REMATCH[2]}"
  raw_url="https://raw.githubusercontent.com/fullsend-ai/agents/v${version}/${file_path}"

  new_sha=$(fetch_harness "${raw_url}" | hash_sha256)

  [[ -n "$new_sha" ]] || { echo "ERROR: Failed to compute SHA for ${file_path} at v${version}" >&2; exit 1; }
  [[ "$new_sha" =~ ^[a-f0-9]{64}$ ]] || { echo "ERROR: Invalid SHA256 hash '${new_sha}'" >&2; exit 1; }

  # Replace the old SHA in the base URL (portable sed -i).
  if sed --version >/dev/null 2>&1; then
    sed -i "s|#sha256=[a-f0-9]\{64\}|#sha256=${new_sha}|" "$harness_file"
  else
    sed -i '' "s|#sha256=[a-f0-9]\{64\}|#sha256=${new_sha}|" "$harness_file"
  fi
  updated_sha=$(sed -n 's/.*#sha256=\([a-f0-9]\{64\}\).*/\1/p' "$harness_file")
  if [[ "$updated_sha" != "$new_sha" ]]; then
    echo "ERROR: Failed to update SHA256 hash in ${harness_file}" >&2
    exit 1
  fi

  echo "Updated ${harness_file}: sha256=${new_sha}"
done
