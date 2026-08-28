#!/usr/bin/env bash

set -euo pipefail

base_ref="${1:-HEAD}"
minimum="${2:-60}"
external_profile="${3:-${COVERAGE_PROFILE:-}}"

if ! [[ "$minimum" =~ ^([0-9]+([.][0-9]+)?|[.][0-9]+)$ ]] || ! awk -v value="$minimum" 'BEGIN { exit !(value >= 0 && value <= 100) }'; then
  echo "usage: $0 [base-ref] [minimum-percent between 0 and 100] [coverage-profile]" >&2
  exit 2
fi

git rev-parse --verify "${base_ref}^{commit}" >/dev/null

tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/homeagent-diff-coverage.XXXXXX")"
trap 'rm -rf "$tmp_dir"' EXIT

if [[ -n "$external_profile" && -f "$external_profile" ]]; then
  coverage_profile="$external_profile"
else
  coverage_profile="$tmp_dir/coverage.out"
  go test -count=1 -race -coverprofile="$coverage_profile" ./...
fi

exec go run ./cmd/homeagent-quality-gate internal-diff-coverage \
  --base "$base_ref" \
  --minimum "$minimum" \
  --profile "$coverage_profile"
