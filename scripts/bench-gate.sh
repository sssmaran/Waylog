#!/usr/bin/env bash
# bench-gate.sh — Phase 1a regression gate for the Go v2 SDK.
#
# This gate is a *baseline regression* check, not proof that the §4.4.1
# performance budgets are met. Time thresholds match the spec budgets +
# ~10% slack and pass comfortably today. Allocation thresholds are
# current-baseline + 10% — middleware and the 20-step / 50-log assemble
# path do not yet meet the §4.4.1 alloc targets, and that gap is tracked
# as Phase 1a perf debt in docs/v2-plan.md. The gate's job here is to
# prevent accidental further regressions while the team prioritizes TS
# parity and the runtime bridge.
#
# When the SDK is optimized to meet §4.4.1 alloc targets, tighten the
# budget rows below and remove the perf-debt note in docs/v2-plan.md.
#
# Usage:
#   ./scripts/bench-gate.sh            # one go test -bench run
#   BENCH_TIME=2s ./scripts/bench-gate.sh
#
# Output:
#   - prints raw bench output to stderr
#   - prints `<bench>: NS=<n> ALLOCS=<a>` per benchmark to stdout
#   - exits 0 with `BENCH GATE PASSED` on success
#   - exits 1 with the first failing budget on regression

set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

BENCH_TIME="${BENCH_TIME:-1s}"
PKG="./pkg/waylog/v2/bench/..."

# Budget table: NAME NS_OP_MAX ALLOCS_OP_MAX
#
# Time budgets follow §4.4.1 (spec) + 10% slack.
# Alloc budgets for MiddlewareNoOp and Assemble20Steps50Logs follow
# observed-baseline + 10% (regression protection only); the §4.4.1 alloc
# targets are 16 / steady-state for middleware and a tighter assemble
# number, both of which remain open perf debt.
budgets=(
  "BenchmarkMiddlewareNoOp 33000 45"
  "BenchmarkStepEmptyBody 5500 4"
  "BenchmarkLoggerInfo 3300 3"
  "BenchmarkAssemble20Steps50Logs 110000 360"
)

bench_out="$(mktemp)"
trap 'rm -f "$bench_out"' EXIT

echo "running: go test -bench=. -benchmem -run='^$' -benchtime=${BENCH_TIME} ${PKG}" >&2
if ! go test -bench=. -benchmem -run='^$' -benchtime="$BENCH_TIME" "$PKG" >"$bench_out" 2>&1; then
  cat "$bench_out" >&2
  echo "BENCH GATE FAILED: go test -bench failed" >&2
  exit 1
fi

cat "$bench_out" >&2

failed=0

check() {
  local name="$1" ns_max="$2" allocs_max="$3"
  # Match a Benchmark line; capture ns/op and allocs/op fields.
  # Example line:
  #   BenchmarkStepEmptyBody-8   12740458   91.63 ns/op   0 B/op   0 allocs/op
  local line
  line="$(awk -v n="$name" '$1 ~ "^"n"(-[0-9]+)?$" {print; exit}' "$bench_out")"
  if [[ -z "$line" ]]; then
    echo "BENCH GATE FAILED: ${name} not found in bench output" >&2
    failed=1
    return
  fi

  # ns/op: token preceding "ns/op"
  local ns_op
  ns_op="$(awk -v n="$name" '
    $1 ~ "^"n"(-[0-9]+)?$" {
      for (i = 1; i <= NF; i++) {
        if ($i == "ns/op") { print $(i-1); exit }
      }
    }
  ' "$bench_out")"

  local allocs_op
  allocs_op="$(awk -v n="$name" '
    $1 ~ "^"n"(-[0-9]+)?$" {
      for (i = 1; i <= NF; i++) {
        if ($i == "allocs/op") { print $(i-1); exit }
      }
    }
  ' "$bench_out")"

  if [[ -z "$ns_op" || -z "$allocs_op" ]]; then
    echo "BENCH GATE FAILED: ${name} bench line missing fields: ${line}" >&2
    failed=1
    return
  fi

  echo "${name}: NS=${ns_op} ALLOCS=${allocs_op}"

  # Use awk for floating-point comparison on ns_op.
  if awk -v v="$ns_op" -v m="$ns_max" 'BEGIN { exit !(v+0 > m+0) }'; then
    echo "BENCH GATE FAILED: ${name} ${ns_op} ns/op > budget ${ns_max} ns/op" >&2
    failed=1
  fi
  if (( allocs_op > allocs_max )); then
    echo "BENCH GATE FAILED: ${name} ${allocs_op} allocs/op > budget ${allocs_max} allocs/op" >&2
    failed=1
  fi
}

for entry in "${budgets[@]}"; do
  # shellcheck disable=SC2086
  check $entry
done

if (( failed )); then
  exit 1
fi

echo "BENCH GATE PASSED"
