#!/usr/bin/env bash

set -euo pipefail

script_dir="$(cd "$(dirname "$0")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"

cd "$repo_root"

base_ref="${1:-origin/main}"
change_spec="${2:-}"

args=(--base "$base_ref")
if [[ -n "$change_spec" ]]; then
  args+=(--change "$change_spec")
fi

# Run quality gate with diff coverage check or minimal flags
exec go run ./cmd/homeagent-quality-gate "${args[@]}"
