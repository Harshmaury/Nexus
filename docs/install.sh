#!/usr/bin/env bash
# engx installer — zero to running (ADR-031)
# Usage:
#   curl -fsSL https://get.engx.dev/install.sh | bash
#   curl -fsSL https://get.engx.dev/install.sh | ENGX_CHANNEL=beta bash

set -euo pipefail

REPO="Harshmaury/Nexus"
INSTALL_DIR="${ENGX_INSTALL_DIR:-$HOME/bin}"
CHANNEL="${ENGX_CHANNEL:-stable}"

# ── PLATFORM DETECTION ────────────────────────────────────────────────────────
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"

# Detect Windows native environment (not WSL) and exit clearly.
if [[ "$OS" == "windows"* ]] || [[ "$OS" == "mingw"* ]] || [[ "$OS" == "msys"* ]]; then
  echo ""
  echo "  engx does not support Windows natively."
  echo ""
  echo "  Supported path: Windows Subsystem for Linux (WSL2)"
  echo ""
  echo "  Setup:"
  echo "    1. Install WSL2:  https://learn.microsoft.com/windows/wsl/install"
  echo "    2. Open a WSL terminal (Ubuntu recommended)"
  echo "    3. Run this installer again inside WSL:"
  echo "       curl -fsSL https://get.engx.dev/install.sh | bash"
  echo ""
  exit 1
fi

# Detect WSL2 (uname returns Linux, but /proc/version contains Microsoft).
IS_WSL=false
if [[ -f /proc/version ]] && grep -qi "microsoft" /proc/version 2>/dev/null; then
  IS_WSL=true
fi

case "$ARCH" in
  x86_64)        ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *)
    echo "error: unsupported architecture: $ARCH"
    exit 1
    ;;
esac

if [[ "$OS" != "linux" && "$OS" != "darwin" ]]; then
  echo "error: unsupported OS: $OS"
  exit 1
fi

# engxd (CGO) is not available for darwin/arm64 in the standard release.
# Apple Silicon users install the amd64 build which runs via Rosetta 2.
if [[ "$OS" == "darwin" && "$ARCH" == "arm64" ]]; then
  ARCH="amd64"
fi

# ── HELPERS ───────────────────────────────────────────────────────────────────
info()    { echo "  → $*"; }
success() { echo "  ✓ $*"; }
warn()    { echo "  ! $*"; }
die()     { echo "error: $*" >&2; exit 1; }

require() {
  for cmd in "$@"; do
    command -v "$cmd" &>/dev/null || die "required command not found: $cmd (install it and retry)"
  done
}

checksum_verify() {
  local file="$1" expected="$2"
  local actual
  if command -v sha256sum &>/dev/null; then
    actual="$(sha256sum "$file" | awk '{print $1}')"
  elif command -v shasum &>/dev/null; then
    actual="$(shasum -a 256 "$file" | awk '{print $1}')"
  else
    die "no sha256sum or shasum found — cannot verify checksum"
  fi
  [[ "$actual" == "$expected" ]] || \
    die "checksum mismatch for $(basename "$file")"$'\n'"  expected: $expected"$'\n'"  actual:   $actual"
}

# ── TEMP DIR ──────────────────────────────────────────────────────────────────
TMPDIR="$(mktemp -d)"
trap 'rm -rf "$TMPDIR"' EXIT

# ── STEP 1: resolve version ───────────────────────────────────────────────────
echo
if [[ "$IS_WSL" == "true" ]]; then
  echo "  engx installer — WSL2 detected — platform: ${OS}/${ARCH}  channel: ${CHANNEL}"
else
  echo "  engx installer — platform: ${OS}/${ARCH}  channel: ${CHANNEL}"
fi
echo "  ────────────────────────────────────────────────"

require curl

info "resolving latest ${CHANNEL} release..."

if [[ "$CHANNEL" == "beta" ]]; then
  VERSION="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases" \
    | grep '"tag_name"' | head -1 | sed 's/.*"v\([^"]*\)".*/\1/')"
else
  VERSION="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep '"tag_name"' | sed 's/.*"v\([^"]*\)".*/\1/')"
fi

[[ -n "$VERSION" ]] || die "could not resolve latest release version — check your network"
success "found v${VERSION}"

# ── STEP 2: download ──────────────────────────────────────────────────────────
TARBALL="engx-${VERSION}-${OS}-${ARCH}.tar.gz"
TARBALL_URL="https://github.com/${REPO}/releases/download/v${VERSION}/${TARBALL}"
CHECKSUMS_URL="https://github.com/${REPO}/releases/download/v${VERSION}/engx-${VERSION}-checksums.txt"

info "downloading ${TARBALL}..."
curl -fsSL --progress-bar -o "${TMPDIR}/${TARBALL}" "$TARBALL_URL"
success "downloaded"

# ── STEP 3: verify checksum ───────────────────────────────────────────────────
info "verifying SHA256..."
curl -fsSL -o "${TMPDIR}/checksums.txt" "$CHECKSUMS_URL"
EXPECTED="$(grep "${TARBALL}" "${TMPDIR}/checksums.txt" | awk '{print $1}')"
[[ -n "$EXPECTED" ]] || die "tarball not found in checksums: ${TARBALL}"
checksum_verify "${TMPDIR}/${TARBALL}" "$EXPECTED"
success "checksum verified"

# ── STEP 4: extract ───────────────────────────────────────────────────────────
info "extracting..."
mkdir -p "${TMPDIR}/extract"
tar -xzf "${TMPDIR}/${TARBALL}" -C "${TMPDIR}/extract"

for bin in engxd engx engxa; do
  [[ -f "${TMPDIR}/extract/bin/${bin}" ]] || die "binary missing from release: bin/${bin}"
done
success "extracted engxd, engx, engxa"

# ── STEP 5: install ───────────────────────────────────────────────────────────
info "installing to ${INSTALL_DIR}..."
mkdir -p "$INSTALL_DIR"
for bin in engxd engx engxa; do
  install -m 755 "${TMPDIR}/extract/bin/${bin}" "${INSTALL_DIR}/${bin}"
done
success "installed"

# ── STEP 6: PATH setup ────────────────────────────────────────────────────────
export PATH="$INSTALL_DIR:$PATH"
add_to_path() {
  local rc="$1"
  local line='export PATH="$HOME/bin:$PATH"'
  [[ -f "$rc" ]] && grep -q 'HOME/bin' "$rc" && return
  { echo ""; echo "# engx — added by installer"; echo "$line"; } >> "$rc"
  success "added ~/bin to PATH in $(basename "$rc")"
}
[[ ":$PATH:\" != *\":$INSTALL_DIR:\"* ]] || true
[[ -f "$HOME/.zshrc" ]]  && add_to_path "$HOME/.zshrc"
[[ -f "$HOME/.bashrc" ]] && add_to_path "$HOME/.bashrc"

# ── STEP 7: register engxd as system service ──────────────────────────────────
info "registering engxd as system service..."
if "${INSTALL_DIR}/engx" platform install &>/dev/null 2>&1; then
  success "engxd registered as system service (starts at login)"
else
  warn "platform install skipped — run manually: engx platform install"
fi

# ── STEP 8: summary ───────────────────────────────────────────────────────────
echo
  echo "  ✓ engx v${VERSION} installed"
  echo
  echo "  Get started:"
  echo "    source ~/.bashrc         # reload PATH (or open a new terminal)"
  echo "    cd <your-project>"
  echo "    engx platform install    # install platform services"
  echo "    engx init                # detect project + write nexus.yaml"
  echo "    engx run <your-project>  # start it"
  echo "    engx ps                  # see what is running"
  echo
  echo "  Tip: next time use Homebrew for easier upgrades:"
  echo "    brew install harshmaury/engx/engx"
  echo
  if [[ "$IS_WSL" == "true" ]]; then
    echo "  WSL2 note: use your Linux terminal for all engx commands."
    echo
  fi
  echo "  Docs: https://engx.dev"
  echo
