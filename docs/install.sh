#!/usr/bin/env bash
# scripts/install.sh — engx zero-to-running installer (ADR-031)
# Usage: curl -fsSL https://get.engx.dev/install.sh | bash
#        curl -fsSL https://get.engx.dev/install.sh | ENGX_CHANNEL=beta bash

set -euo pipefail

REPO="Harshmaury/Nexus"
INSTALL_DIR="${ENGX_INSTALL_DIR:-$HOME/bin}"
CHANNEL="${ENGX_CHANNEL:-stable}"

# ── PLATFORM CHECK ────────────────────────────────────────────────────────────
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"

if [[ "$OS" == "windows"* ]] || [[ "$OS" == "mingw"* ]]; then
  echo "error: Windows is not supported by this installer."
  echo "  Download the .zip from: https://github.com/${REPO}/releases"
  exit 1
fi

case "$ARCH" in
  x86_64)  ARCH="amd64" ;;
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

# ── HELPERS ───────────────────────────────────────────────────────────────────
info()    { echo "  → $*"; }
success() { echo "  ✓ $*"; }
warn()    { echo "  ! $*"; }
die()     { echo "error: $*" >&2; exit 1; }

require() {
  for cmd in "$@"; do
    command -v "$cmd" &>/dev/null || die "required command not found: $cmd"
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

# ── TEMP DIR (cleaned up on exit) ─────────────────────────────────────────────
TMPDIR="$(mktemp -d)"
trap 'rm -rf "$TMPDIR"' EXIT

# ── STEP 1: resolve version ───────────────────────────────────────────────────
echo
echo "engx installer — platform: ${OS}/${ARCH}  channel: ${CHANNEL}"
echo "────────────────────────────────────────────────"

require curl

info "resolving latest ${CHANNEL} release..."

if [[ "$CHANNEL" == "beta" ]]; then
  VERSION="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases" \
    | grep '"tag_name"' | head -1 | sed 's/.*"v\([^"]*\)".*/\1/')"
else
  VERSION="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep '"tag_name"' | sed 's/.*"v\([^"]*\)".*/\1/')"
fi

[[ -n "$VERSION" ]] || die "could not resolve latest release version"
success "found v${VERSION}"

# ── STEP 2: download tarball ──────────────────────────────────────────────────
TARBALL="engx-${VERSION}-${OS}-${ARCH}.tar.gz"
TARBALL_URL="https://github.com/${REPO}/releases/download/v${VERSION}/${TARBALL}"
CHECKSUMS_URL="https://github.com/${REPO}/releases/download/v${VERSION}/engx-${VERSION}-checksums.txt"

info "downloading ${TARBALL}..."
curl -fsSL --progress-bar -o "${TMPDIR}/${TARBALL}" "$TARBALL_URL"
success "downloaded ${TARBALL}"

# ── STEP 3: verify checksum ───────────────────────────────────────────────────
info "verifying SHA256 checksum..."
curl -fsSL -o "${TMPDIR}/checksums.txt" "$CHECKSUMS_URL"
EXPECTED="$(grep "${TARBALL}" "${TMPDIR}/checksums.txt" | awk '{print $1}')"
[[ -n "$EXPECTED" ]] || die "tarball not found in checksums file: ${TARBALL}"
checksum_verify "${TMPDIR}/${TARBALL}" "$EXPECTED"
success "checksum verified"

# ── STEP 4: extract ───────────────────────────────────────────────────────────
info "extracting binaries..."
mkdir -p "${TMPDIR}/extract"
tar -xzf "${TMPDIR}/${TARBALL}" -C "${TMPDIR}/extract"

for bin in engxd engx engxa; do
  [[ -f "${TMPDIR}/extract/bin/${bin}" ]] || die "binary not found in tarball: bin/${bin}"
done
success "extracted engxd, engx, engxa"

# ── STEP 5: install to ~/bin ─────────────────────────────────────────────────
info "installing to ${INSTALL_DIR}..."
mkdir -p "$INSTALL_DIR"

for bin in engxd engx engxa; do
  install -m 755 "${TMPDIR}/extract/bin/${bin}" "${INSTALL_DIR}/${bin}"
done
success "installed to ${INSTALL_DIR}"

# ── STEP 6: PATH setup ────────────────────────────────────────────────────────
add_to_path() {
  local shell_rc="$1"
  local export_line='export PATH="$HOME/bin:$PATH"'
  if [[ -f "$shell_rc" ]] && grep -q 'HOME/bin' "$shell_rc"; then
    return 0
  fi
  echo "" >> "$shell_rc"
  echo "# engx — added by installer" >> "$shell_rc"
  echo "$export_line" >> "$shell_rc"
  success "added ~/bin to PATH in $(basename "$shell_rc")"
}

export PATH="$INSTALL_DIR:$PATH"

if [[ ":$PATH:" != *":$INSTALL_DIR:"* ]]; then
  [[ -f "$HOME/.zshrc" ]]  && add_to_path "$HOME/.zshrc"
  [[ -f "$HOME/.bashrc" ]] && add_to_path "$HOME/.bashrc"
fi

# ── STEP 7: register as system service (best-effort) ──────────────────────────
info "registering engxd as system service..."
if "${INSTALL_DIR}/engx" platform install &>/dev/null; then
  success "engxd registered as system service"
else
  warn "platform install skipped — run manually: engx platform install"
fi

# ── STEP 8: summary ───────────────────────────────────────────────────────────
echo
echo "  engx v${VERSION} installed successfully"
echo
echo "  Next steps:"
echo "    source ~/.bashrc   (or open a new terminal)"
echo "    engxd &            (start the daemon if not auto-started)"
echo "    engx platform start"
echo "    engx doctor"
echo
