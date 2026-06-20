#!/bin/sh
# Offline install.sh smoke: stand up a local mirror of a GitHub Release from
# `make release` output and drive a parameterised install + first-run. No network.
set -eu
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

[ -d dist ] || { echo "run 'make release' first"; exit 1; }
os="$(go env GOOS)"; arch="$(go env GOARCH)"
asset="crux_${os}_${arch}.tar.gz"
[ -f "dist/${asset}" ] || { echo "missing dist/${asset}"; exit 1; }

mirror="$(mktemp -d)"; trap 'rm -rf "$mirror"' EXIT
cp "dist/${asset}" "dist/checksums.txt" "$mirror/"

# Re-point a copy of install.sh at the local mirror and a tagless direct path.
sed -e "s#https://api.github.com/repos/\${REPO}/releases/latest#file://${mirror}/release.json#" \
    -e "s#https://github.com/\${REPO}/releases/download/\${tag}#file://${mirror}#" \
    install.sh > "$mirror/install.sh"
printf '{"tag_name":"v0.0.0-test"}\n' > "$mirror/release.json"

CRUX_INSTALL_DIR="$mirror/bin" CRUX_NO_FIRST_RUN=1 sh "$mirror/install.sh"
"$mirror/bin/crux" first-run --requests 30 --timeout 90s | grep -q report_hash \
  || { echo "FAIL: installed crux first-run produced no report_hash"; exit 1; }
echo "PASS: install.sh + crux first-run"
