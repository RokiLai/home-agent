#!/usr/bin/env bash

set -euo pipefail

output_dir="${1:-workflow-metrics/test}"
mkdir -p "$output_dir"
events="$output_dir/go-test-events.jsonl"
report="$output_dir/go-test-report.json"
started_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
start_seconds=$SECONDS

set +e
go test -json -count=1 -race ./... | tee "$events"
test_status=${PIPESTATUS[0]}
set -e

go run ./cmd/homeagent-workflow-metrics test-report < "$events" > "$report"
duration_seconds=$((SECONDS - start_seconds))
printf '{"started_at":"%s","duration_seconds":%d,"exit_code":%d}\n' \
  "$started_at" "$duration_seconds" "$test_status" > "$output_dir/test-command.json"
exit "$test_status"
