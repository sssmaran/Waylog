#!/bin/sh
# Crux installer — downloads the latest GitHub Release archive, verifies its
# SHA-256 against checksums.txt, installs `crux` + `ingest`, and offers to run
# the first-run demo. Audit this script before piping it to a shell:
#   curl -fsSL https://raw.githubusercontent.com/sssmaran/WaylogCLI/master/install.sh
set -eu

REPO="sssmaran/WaylogCLI"
INSTALL_DIR="${CRUX_INSTALL_DIR:-$HOME/.local/bin}"

err() { echo "crux-install: $*" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

detect_os() {
  case "$(uname -s)" in
    Linux)  echo linux ;;
    Darwin) echo darwin ;;
    *) err "unsupported OS: $(uname -s)" ;;
  esac
}
detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64) echo amd64 ;;
    arm64|aarch64) echo arm64 ;;
    *) err "unsupported architecture: $(uname -m)" ;;
  esac
}

sha256_of() {
  if have sha256sum; then sha256sum "$1" | awk '{print $1}'
  elif have shasum; then shasum -a 256 "$1" | awk '{print $1}'
  else err "need sha256sum or shasum to verify the download"; fi
}

main() {
  have curl || err "curl is required"
  have tar  || err "tar is required"

  os="$(detect_os)"; arch="$(detect_arch)"
  asset="crux_${os}_${arch}.tar.gz"

  # Resolve the latest release tag via the GitHub API (no auth needed for public repos).
  tag="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep -m1 '"tag_name"' | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/')"
  [ -n "$tag" ] || err "could not resolve latest release tag"

  base="https://github.com/${REPO}/releases/download/${tag}"
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT

  echo "Downloading ${asset} (${tag}) ..."
  curl -fsSL "${base}/${asset}"      -o "${tmp}/${asset}"
  curl -fsSL "${base}/checksums.txt" -o "${tmp}/checksums.txt"

  want="$(grep " ${asset}\$" "${tmp}/checksums.txt" | awk '{print $1}')"
  [ -n "$want" ] || err "no checksum for ${asset} in checksums.txt"
  got="$(sha256_of "${tmp}/${asset}")"
  [ "$want" = "$got" ] || err "checksum mismatch for ${asset}: want ${want}, got ${got}"
  echo "Checksum verified."

  mkdir -p "$INSTALL_DIR"
  tar xzf "${tmp}/${asset}" -C "$tmp"
  install -m 0755 "${tmp}/crux"   "${INSTALL_DIR}/crux"
  install -m 0755 "${tmp}/ingest" "${INSTALL_DIR}/ingest"
  echo "Installed crux + ingest to ${INSTALL_DIR}"

  case ":$PATH:" in
    *":$INSTALL_DIR:"*) : ;;
    *) echo "Add ${INSTALL_DIR} to PATH:  export PATH=\"${INSTALL_DIR}:\$PATH\"" ;;
  esac

  if [ -t 0 ] && [ "${CRUX_NO_FIRST_RUN:-}" != "1" ]; then
    printf "Run the 5-minute demo now? [Y/n] "
    read -r ans
    case "$ans" in [nN]*) echo "Later: ${INSTALL_DIR}/crux first-run" ;; *) "${INSTALL_DIR}/crux" first-run ;; esac
  else
    echo "Next: ${INSTALL_DIR}/crux first-run"
  fi
}
main "$@"
