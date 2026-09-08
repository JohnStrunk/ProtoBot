#!/usr/bin/env bash
set -euo pipefail

# Called by Renovate postUpgradeTasks after a fullsend-ai/agents version bump.
# Recomputes SHA256 hashes in .fullsend/harness/*.yaml base: URLs.

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

  file_type=$(gh api "repos/fullsend-ai/agents/contents/${file_path}?ref=v${version}" --jq '.type')
  if [[ "$file_type" != "file" ]]; then
    echo "ERROR: Expected a regular file for ${file_path} at v${version}, got ${file_type:-null}" >&2
    exit 1
  fi

  # Fetch the raw versioned harness and compute its integrity hash.
  if command -v sha256sum >/dev/null 2>&1; then
    new_sha=$(gh api "repos/fullsend-ai/agents/contents/${file_path}?ref=v${version}" \
      -H 'Accept: application/vnd.github.raw' | sha256sum | cut -d' ' -f1)
  else
    new_sha=$(gh api "repos/fullsend-ai/agents/contents/${file_path}?ref=v${version}" \
      -H 'Accept: application/vnd.github.raw' | shasum -a 256 | cut -d' ' -f1)
  fi

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
