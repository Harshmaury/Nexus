#!/usr/bin/env bash
# scripts/install.sh
# Nexus Developer Platform — zero-to-running installer (ADR-031)
#
# Usage:
#   curl -fsSL https://get.engx.dev/install.sh | bash
#   curl -fsSL https://get.engx.dev/install.sh | bash -s -- --channel beta
#
# What this script does:
#   1. Detect OS and architecture
#   2. Resolve latest release from GitHub Releases API
#   3. Download tarball to a temp directory
#   4. Verify SHA256 checksum against the checksums manifest
#   5. Extract binaries to ~/bin/
#   6. Add ~/bin/ to PATH in shell rc file if not already present
#   7. Run: engx platform install  (registers engxd as launchd/systemd service)
#
# Supported platforms:
#   linux/amd64, linux/arm64, darwin/amd64, darwin/arm64
#   Windows: not supported — download from GitHub Releases directly.
#
# Requirements: bash, curl, tar, sha256sum (Linux) or shasum (macOS)

set -euo pipefail

# ── Constants ─────────────────────────────────────────────────────────────────

REPO="Harshmaury/Nexus"
RELEASES_API="https://api.github.com/repos/${REPO}/releases"
DOWNLOAD_BASE="https://github.com/${REPO}/releases/download"
BIN_DIR="${HOME}/bin"
CHANNEL="${ENGX_CHANNEL:-stable}"   # override with: ENGX_CHANNEL=beta | --channel beta

# ── Colours ───────────────────────────────────────────────────────────────────

if [ -t 1 ]; then
  BOLD="\033[1m"; GREEN="\033[32m"; YELLOW="\033[33m"; RED="\033[31m"; RESET="\033[0m"
else
  BOLD=""; GREEN=""; YELLOW=""; RED=""; RESET=""
fi

step()  { printf "${BOLD}  →${RESET} %s\n" "$*"; }
ok()    { printf "  ${GREEN}✓${RESET} %s\n" "$*"; }
warn()  { printf "  ${YELLOW}!${RESET} %s\n" "$*"; }
die()   { printf "  ${RED}✗${RESET} %s\n" "$*" >&2; exit 1; }

# ── Argument parsing ──────────────────────────────────────────────────────────

parse_args() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --channel) CHANNEL="${2:?--channel requires an argument}"; shift 2 ;;
      --channel=*) CHANNEL="${1#*=}"; shift ;;
      *) die "unknown argument: $1" ;;
    esac
  done
  case "$CHANNEL" in
    stable|beta) ;;
    *) die "unknown channel '${CHANNEL}' — use stable or beta" ;;
  esac
}

# ── Platform detection ────────────────────────────────────────────────────────

detect_platform() {
  local os arch
  os="$(uname -s)"
  arch="$(uname -m)"

  case "$os" in
    Linux)  OS="linux" ;;
    Darwin) OS="darwin" ;;
    *)      die "unsupported OS: ${os} — download from https://github.com/${REPO}/releases" ;;
  esac

  case "$arch" in
    x86_64)          ARCH="amd64" ;;
    aarch64 | arm64) ARCH="arm64" ;;
    *)               die "unsupported architecture: ${arch}" ;;
  esac
}

# ── Release resolution ────────────────────────────────────────────────────────

resolve_release() {
  step "resolving latest ${CHANNEL} release..."

  local releases
  releases="$(curl -fsSL -H "Accept: application/vnd.github+json" \
    "${RELEASES_API}?per_page=20")" \
    || die "failed to reach GitHub API — check your internet connection"

  # Pick first non-draft release; for stable, skip pre-releases.
  TAG="$(echo "$releases" | python3 -c "
import sys, json
releases = json.load(sys.stdin)
channel = '${CHANNEL}'
for r in releases:
    if r.get('draft'): continue
    if channel == 'stable' and r.get('prerelease'): continue
    print(r['tag_name'])
    break
" 2>/dev/null)"
  [ -n "$TAG" ] || die "no ${CHANNEL} release found in the last 20 releases"
  VERSION="${TAG#v}"
  ok "found ${TAG}"
}

# ── Download ──────────────────────────────────────────────────────────────────

download_tarball() {
  TARBALL="engx-${VERSION}-${OS}-${ARCH}.tar.gz"
  CHECKSUMS="engx-${VERSION}-checksums.txt"
  TARBALL_URL="${DOWNLOAD_BASE}/${TAG}/${TARBALL}"
  CHECKSUMS_URL="${DOWNLOAD_BASE}/${TAG}/${CHECKSUMS}"

  TMPDIR="$(mktemp -d)"
  # shellcheck disable=SC2064
  trap "rm -rf '${TMPDIR}'" EXIT

  step "downloading ${TARBALL}..."
  curl -fsSL --output "${TMPDIR}/${TARBALL}" "${TARBALL_URL}" \
    || die "download failed: ${TARBALL_URL}"
  ok "downloaded"
}

# ── Checksum verification ─────────────────────────────────────────────────────

verify_checksum() {
  step "verifying SHA256 checksum..."
  curl -fsSL --output "${TMPDIR}/${CHECKSUMS}" "${CHECKSUMS_URL}" \
    || die "failed to download checksums: ${CHECKSUMS_URL}"

  local expected actual
  expected="$(grep "${TARBALL}" "${TMPDIR}/${CHECKSUMS}" | awk '{print $1}')"
  [ -n "$expected" ] || die "no checksum found for ${TARBALL} in manifest"

  if command -v sha256sum &>/dev/null; then
    actual="$(sha256sum "${TMPDIR}/${TARBALL}" | awk '{print $1}')"
  elif command -v shasum &>/dev/null; then
    actual="$(shasum -a 256 "${TMPDIR}/${TARBALL}" | awk '{print $1}')"
  else
    die "no sha256sum or shasum found — cannot verify checksum"
  fi

  [ "$expected" = "$actual" ] || die "checksum mismatch
    expected: ${expected}
    actual:   ${actual}"
  ok "checksum verified"
}

# ── Extract + install ─────────────────────────────────────────────────────────

install_binaries() {
  step "extracting to ${BIN_DIR}/..."
  mkdir -p "${BIN_DIR}"
  tar -xzf "${TMPDIR}/${TARBALL}" -C "${TMPDIR}"

  for binary in engxd engx engxa; do
    local src="${TMPDIR}/bin/${binary}"
    if [ -f "$src" ]; then
      install -m 755 "$src" "${BIN_DIR}/${binary}"
      ok "installed ${binary}"
    else
      warn "${binary} not in tarball for ${OS}/${ARCH} — skipping"
    fi
  done
}

# ── PATH setup ────────────────────────────────────────────────────────────────

setup_path() {
  case ":${PATH}:" in
    *":${BIN_DIR}:"*) return ;;  # already in PATH
  esac

  local rc_file=""
  if [ -n "${ZSH_VERSION:-}" ] || [ "$(basename "${SHELL:-}")" = "zsh" ]; then
    rc_file="${HOME}/.zshrc"
  else
    rc_file="${HOME}/.bashrc"
  fi

  step "adding ${BIN_DIR} to PATH in ${rc_file}..."
  # shellcheck disable=SC2016  # ${PATH} is intentionally literal in the rc file snippet
  printf '\n# engx — added by install.sh\nexport PATH="%s:${PATH}"\n' "${BIN_DIR}" >> "$rc_file"
  ok "PATH updated — restart your shell or run: export PATH=\"${BIN_DIR}:\${PATH}\""

  # Make available in the current session too
  export PATH="${BIN_DIR}:${PATH}"
}

# ── Platform service registration ─────────────────────────────────────────────

register_service() {
  if ! command -v engx &>/dev/null; then
    warn "engx not in PATH yet — skipping platform install"
    warn "run manually after restarting shell: engx platform install"
    return
  fi
  step "registering engxd as a system service..."
  if engx platform install; then
    ok "engxd registered — will auto-start on login"
  else
    warn "engx platform install failed — run it manually after restarting"
  fi
}

# ── Summary ───────────────────────────────────────────────────────────────────

print_summary() {
  printf "\n"
  printf "  %bengx %s installed successfully.%b\n\n" "${BOLD}${GREEN}" "${VERSION}" "${RESET}"
  printf "  Binaries: %s/engx  engxd  engxa\n" "${BIN_DIR}"
  printf "\n"
  printf "  Next steps:\n"
  printf "    1. Restart your shell (or: export PATH=\"%s:\${PATH}\")\n" "${BIN_DIR}"
  printf "    2. engx doctor          — verify platform health\n"
  printf "    3. engx status          — view all services\n"
  printf "\n"
  printf "  To upgrade later:  engx upgrade\n"
  printf "  To uninstall:      engx platform uninstall && rm %s/eng{x,xd,xa}\n" "${BIN_DIR}"
  printf "\n"
}

# ── Main ──────────────────────────────────────────────────────────────────────

main() {
  parse_args "$@"
  printf "\n  ${BOLD}engx installer${RESET}  channel=%s\n\n" "${CHANNEL}"
  detect_platform
  resolve_release
  download_tarball
  verify_checksum
  install_binaries
  setup_path
  register_service
  print_summary
}

main "$@"
