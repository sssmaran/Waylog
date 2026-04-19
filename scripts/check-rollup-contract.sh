#!/usr/bin/env bash
# check-rollup-contract.sh — enforce the canonical rollup contract:
# default user-facing surfaces (top errors, overview KPIs, compare_windows,
# spike detection, failure_patterns) must consume analysis.RollupWindow,
# not the propagation-counted store.SummarizeWindow / analysis.DiffSummaries
# / analysis.DetectFailurePatternsFromSummary.
#
# The allow-list below pins every legitimate reference. Any NEW mention of
# these propagation-counted APIs outside the allow-list fails CI — that's
# how we prevent the PMT_502=9-not-3 cascade-amplification bug from
# coming back.
#
# If you are adding a NEW detail surface that genuinely needs propagation
# spread (trace stories, blast radius, failure chains), bind the result
# to a variable named `propagationSummary` and extend the allow-list with
# a short justification.
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

# Files allowed to reference propagation-counted APIs.
allowlist=(
  "internal/graph/store/summaries.go"            # defines WindowSummary/SummarizeWindow
  "internal/graph/store/summaries_test.go"       # tests the definition
  "internal/graph/analysis/diff.go"              # defines DiffSummaries alongside DiffRollups
  "internal/graph/analysis/diff_test.go"         # tests the definition
  "internal/graph/analysis/patterns.go"          # defines DetectFailurePatternsFromSummary
  "internal/graph/analysis/rollup.go"            # doc contract referencing both paths
  "internal/tools/store.go"                      # interface surface (preserved)
  "internal/tools/failures_test.go"              # test stub implements interface
  "internal/ingest/handler.go"                   # frozenStore delegator for tools.Store
)

is_allowed() {
  local path="$1"
  for allowed in "${allowlist[@]}"; do
    if [ "$path" = "$allowed" ]; then
      return 0
    fi
  done
  return 1
}

violations=0
pattern='\b(SummarizeWindow|DiffSummaries|DetectFailurePatternsFromSummary)\b'

while IFS= read -r path; do
  # Skip worktree scratch dirs and vendored code.
  case "$path" in
    .claude/*|.git/*|vendor/*) continue ;;
  esac
  if ! is_allowed "$path"; then
    matches=$(grep -nE "$pattern" "$path" || true)
    if [ -n "$matches" ]; then
      echo "VIOLATION: $path references propagation-counted rollup API"
      echo "$matches" | sed 's/^/  /'
      violations=1
    fi
  fi
done < <(find . -type f -name '*.go' \
  -not -path './.claude/*' \
  -not -path './.git/*' \
  -not -path './vendor/*' \
  | sed 's|^\./||' \
  | sort)

if [ "$violations" -ne 0 ]; then
  echo ""
  echo "FAIL: default rollups must consume analysis.RollupWindow."
  echo "See internal/graph/analysis/rollup.go for the contract."
  echo "If this is a legitimate detail surface, extend the allow-list in"
  echo "scripts/check-rollup-contract.sh with a short justification."
  exit 1
fi

echo "OK: rollup contract honored"
