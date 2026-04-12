#!/usr/bin/env bash
# check-doc-links.sh — verify every relative markdown link in README.md and
# docs/**/*.md resolves to an existing file on disk. Exits non-zero on any miss.
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

broken=0

check_links() {
  local source="$1"
  local dir
  dir="$(dirname "$source")"
  local found_broken=0

  while IFS= read -r link; do
    # Skip external URLs and anchor-only links
    if [[ "$link" == http://* ]] || [[ "$link" == https://* ]] || [[ "$link" == \#* ]] || [[ "$link" == mailto:* ]]; then
      continue
    fi
    # Strip anchor fragment
    local path="${link%%\#*}"
    if [ -z "$path" ]; then
      continue
    fi
    local target="$dir/$path"
    if [ ! -e "$target" ]; then
      echo "BROKEN: $source -> $link (resolved: $target)"
      found_broken=1
    fi
  done < <(grep -oP '\]\(\K[^)]+' "$source" 2>/dev/null || true)

  return $found_broken
}

files=("README.md")
while IFS= read -r f; do
  files+=("$f")
done < <(find docs -name '*.md' -type f 2>/dev/null || true)

for f in "${files[@]}"; do
  if ! check_links "$f"; then
    broken=1
  fi
done

if [ "$broken" -ne 0 ]; then
  echo ""
  echo "FAIL: broken doc links detected"
  exit 1
fi

echo "OK: all doc links resolve"
