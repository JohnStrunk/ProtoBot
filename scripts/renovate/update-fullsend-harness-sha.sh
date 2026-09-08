#!/usr/bin/env bash
set -euo pipefail

# Called by Renovate postUpgradeTasks after a fullsend-ai/agents version bump.
# Recomputes SHA256 hashes in .fullsend/harness/*.yaml base: URLs.

for harness_file in .fullsend/harness/*.yaml; do
  [[ -f "$harness_file" ]] || continue

  base_url=$(grep -m1 'base:' "$harness_file" | sed 's/.*base: *"//;s/".*//') || continue
  [[ -z "$base_url" ]] && continue

  # Extract version and path from the URL.
  version=$(echo "$base_url" | sed -n 's|.*/fullsend-ai/agents/v\([0-9.]*\)/.*|\1|p')
  file_path=$(echo "$base_url" | sed -n 's|.*/fullsend-ai/agents/v[0-9.]*/\(.*\)#.*|\1|p')

  [[ -z "$version" || -z "$file_path" ]] && continue
  [[ "$file_path" =~ ^[a-zA-Z0-9/_.-]+$ ]] || { echo "ERROR: Invalid file_path '${file_path}'" >&2; exit 1; }

  # Fetch the versioned harness and compute its integrity hash.
  raw_content=$(gh api "repos/fullsend-ai/agents/contents/${file_path}?ref=v${version}" --jq '.content')
  [[ -n "$raw_content" ]] || { echo "ERROR: Empty content for ${file_path} at v${version}" >&2; exit 1; }
  if base64 --help 2>&1 | grep -q '\-d'; then
    base64_decode=(base64 -d)
  else
    base64_decode=(base64 -D)
  fi
  if command -v sha256sum >/dev/null 2>&1; then
    new_sha=$(printf '%s' "$raw_content" | "${base64_decode[@]}" | sha256sum | cut -d' ' -f1)
  else
    new_sha=$(printf '%s' "$raw_content" | "${base64_decode[@]}" | shasum -a 256 | cut -d' ' -f1)
  fi

  [[ -n "$new_sha" ]] || { echo "ERROR: Failed to compute SHA for ${file_path} at v${version}" >&2; exit 1; }
  [[ "$new_sha" =~ ^[a-f0-9]{64}$ ]] || { echo "ERROR: Invalid SHA256 hash '${new_sha}'" >&2; exit 1; }

  # Replace the old SHA in the base URL (portable sed -i).
  if sed --version >/dev/null 2>&1; then
    sed -i "s|#sha256=[a-f0-9]\{64\}|#sha256=${new_sha}|" "$harness_file"
  else
    sed -i '' "s|#sha256=[a-f0-9]\{64\}|#sha256=${new_sha}|" "$harness_file"
  fi

  echo "Updated ${harness_file}: sha256=${new_sha}"
done
