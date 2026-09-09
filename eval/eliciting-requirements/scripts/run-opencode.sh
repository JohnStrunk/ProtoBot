#!/usr/bin/env bash
set -euo pipefail

workspace=${1:?workspace is required}
model=${2:?model is required}
agent=${3:?skill name is required}
args=${4-}
backup=$(mktemp -d)

restore_answer_keys() {
  for name in reference.md annotations.yaml; do
    if [[ -f "$backup/$name" ]]; then
      mv "$backup/$name" "$workspace/$name"
    fi
  done
  rmdir "$backup"
}

trap restore_answer_keys EXIT

for name in reference.md annotations.yaml; do
  if [[ -f "$workspace/$name" ]]; then
    mv "$workspace/$name" "$backup/$name"
  fi
done

opencode run \
  --dir "$workspace" \
  --model "$model" \
  --format json \
  --auto \
  "/$agent $args"
