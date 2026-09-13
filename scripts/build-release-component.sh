#!/usr/bin/env bash

set -euo pipefail

component="${1:-}"
version="${2:-}"
dist_dir="${3:-dist}"
metrics_dir="${4:-workflow-metrics/build-${component}}"
if [[ "$component" != "server" && "$component" != "agent" ]]; then
  echo "component must be server or agent" >&2
  exit 2
fi
if [[ -z "$version" ]]; then
  echo "version must be non-empty" >&2
  exit 2
fi

mkdir -p "$dist_dir" "$metrics_dir"
targets_file="$(mktemp "${TMPDIR:-/tmp}/homeagent-release-targets.XXXXXX")"
trap 'rm -f "$targets_file"' EXIT
go run ./cmd/homeagent-workflow-metrics targets \
  -manifest configs/release-targets.json -component "$component" > "$targets_file"

status_file="$metrics_dir/target-status.jsonl"
: > "$status_file"
target_count=0
while IFS=$'\t' read -r target_component goos goarch output; do
  target_count=$((target_count + 1))
  target_id="${target_component}-${goos}-${goarch}"
  started_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  start_seconds=$SECONDS
  ldflags="-s -w -X homeagent/internal/version.ServerVersion=${version}"
  if [[ "$component" == "agent" ]]; then
    ldflags="-s -w -X homeagent/internal/version.AgentVersion=${version}"
  fi
  if CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build -trimpath \
    -ldflags "$ldflags" -o "$dist_dir/$output" "./cmd/homeagent-${component}"; then
    (cd "$dist_dir" && sha256sum "$output" > "${output}.sha256")
    status="succeeded"
    exit_code=0
  else
    status="failed"
    exit_code=1
  fi
  duration_seconds=$((SECONDS - start_seconds))
  printf '{"target_id":"%s","component":"%s","goos":"%s","goarch":"%s","output":"%s","started_at":"%s","duration_seconds":%d,"status":"%s"}\n' \
    "$target_id" "$component" "$goos" "$goarch" "$output" "$started_at" "$duration_seconds" "$status" >> "$status_file"
  if [[ $exit_code -ne 0 ]]; then
    exit "$exit_code"
  fi
done < "$targets_file"

expected_count=7
expected_files=14
if [[ "$component" == "agent" ]]; then
  expected_count=9
  expected_files=18
fi
test "$target_count" -eq "$expected_count"
test "$(find "$dist_dir" -maxdepth 1 -type f | wc -l | tr -d ' ')" -eq "$expected_files"
(cd "$dist_dir" && sha256sum -c ./*.sha256)
