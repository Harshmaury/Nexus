#!/usr/bin/env bash
# @nexus-project: nexus
# @nexus-path: scripts/verify.sh
# ─────────────────────────────────────────────────────────────
# NEXUS VERIFY v1.0
# Run at the start of every AI session.
# Prints a compact state snapshot + a paste block for Claude.
#
# Usage:
#   ./scripts/verify.sh          → full snapshot + paste block
#   ./scripts/verify.sh --short  → build status + key only
# ─────────────────────────────────────────────────────────────
set -euo pipefail

NEXUS_HOME="$HOME/dev/nexus"
MODE="${1:---full}"

# ── COLORS ───────────────────────────────────────────────────
R='\033[0;31m' G='\033[0;32m' Y='\033[1;33m'
C='\033[0;36m' W='\033[1;37m' D='\033[2m' NC='\033[0m'

cd "$NEXUS_HOME"

# ── SESSION KEY ──────────────────────────────────────────────
# Format: NX-<git-short-hash>-<YYYYMMDD>
# Unique per commit per day. Lets Claude cross-reference
# exactly which version of the codebase you are on.
GIT_HASH=$(git rev-parse --short HEAD 2>/dev/null || echo "nogit")
SESSION_KEY="NX-${GIT_HASH}-$(date +%Y%m%d)"

# ── SHORT MODE ───────────────────────────────────────────────
if [ "$MODE" = "--short" ]; then
  echo ""
  echo -e "${W}KEY:${NC} $SESSION_KEY"
  echo -e "${D}WORKFLOW: https://raw.githubusercontent.com/Harshmaury/Nexus/main/WORKFLOW.md${NC}"
  echo ""
  BUILD=$(go build ./... 2>&1)
  [ -z "$BUILD" ] \
    && echo -e "${G}✓ build PASS${NC}" \
    || echo -e "${R}✗ build FAIL${NC}\n$BUILD"
  echo ""
  echo -e "${Y}TODO:${NC}"
  grep "^\s*\[ \]" WORKFLOW.md 2>/dev/null | head -5 | sed 's/^[[:space:]]*/  /'
  echo ""
  exit 0
fi

# ── FULL SNAPSHOT ────────────────────────────────────────────
echo ""
echo -e "${C}${W}╔══════════════════════════════════════════════╗${NC}"
echo -e "${C}${W}║         NEXUS SESSION VERIFY  v1.0          ║${NC}"
echo -e "${C}${W}╚══════════════════════════════════════════════╝${NC}"
echo ""

# SESSION KEY
echo -e "${W}  SESSION KEY │ ${Y}$SESSION_KEY${NC}"
echo -e "${W}  WORKFLOW    │ ${D}https://raw.githubusercontent.com/Harshmaury/Nexus/main/WORKFLOW.md${NC}"
echo ""

# GIT
echo -e "${C}── GIT ────────────────────────────────────────────${NC}"
BRANCH=$(git branch --show-current)
LAST=$(git log --oneline -1)
DIRTY=$(git status --short | wc -l | tr -d ' ')
echo -e "  branch  $BRANCH"
echo -e "  last    $LAST"
if [ "$DIRTY" -gt 0 ]; then
  echo -e "  status  ${R}$DIRTY uncommitted file(s)${NC}"
  git status --short | head -6 | sed 's/^/    /'
else
  echo -e "  status  ${G}clean${NC}"
fi
echo ""

# BUILD
echo -e "${C}── BUILD ──────────────────────────────────────────${NC}"
BUILD_OUT=$(go build ./... 2>&1)
if [ -z "$BUILD_OUT" ]; then
  echo -e "  go build ./...  ${G}PASS ✓${NC}"
else
  echo -e "  go build ./...  ${R}FAIL ✗${NC}"
  echo "$BUILD_OUT" | sed 's/^/    /'
fi
echo ""

# PACKAGES (what files actually exist on disk)
echo -e "${C}── PACKAGES ───────────────────────────────────────${NC}"
find . -name "*.go" -not -path "./.git/*" \
  | sed 's|^\./||' \
  | awk -F'/' 'NF>1{print $1"/"$2}' \
  | sort -u \
  | while read -r pkg; do
      COUNT=$(find "./$pkg" -name "*.go" 2>/dev/null | wc -l | tr -d ' ')
      echo "  $pkg  ($COUNT files)"
    done
echo ""

# BUILD STATUS SUMMARY from WORKFLOW
echo -e "${C}── PROGRESS ───────────────────────────────────────${NC}"
DONE=$(grep -c "^\s*\[x\]" WORKFLOW.md 2>/dev/null || echo 0)
TODO=$(grep -c "^\s*\[ \]" WORKFLOW.md 2>/dev/null || echo 0)
echo -e "  done  ${G}$DONE${NC}   todo  ${Y}$TODO${NC}"
echo ""
echo -e "  ${Y}Next up:${NC}"
grep "^\s*\[ \]" WORKFLOW.md 2>/dev/null | head -4 | sed 's/^[[:space:]]*/    /'
echo ""

# DAEMON PROCESS
echo -e "${C}── DAEMON ─────────────────────────────────────────${NC}"
if pgrep -x "engxd" > /dev/null 2>&1; then
  PID=$(pgrep -x engxd)
  echo -e "  engxd   ${G}RUNNING${NC}  pid=$PID"
  [ -S "/tmp/engx.sock" ] \
    && echo -e "  socket  ${G}/tmp/engx.sock ✓${NC}" \
    || echo -e "  socket  ${R}missing${NC}"
else
  echo -e "  engxd   ${R}stopped${NC}"
fi
echo ""

# ── PASTE BLOCK ──────────────────────────────────────────────
# Copy everything between the markers and paste to Claude.
# Claude reads the session key + WORKFLOW URL and is instantly
# oriented — no long explanation needed.
echo -e "${C}${W}╔══════════════════════════════════════════════╗${NC}"
echo -e "${C}${W}║  PASTE THIS BLOCK TO CLAUDE:                 ║${NC}"
echo -e "${C}${W}╚══════════════════════════════════════════════╝${NC}"
echo ""
echo "---NEXUS-SESSION-START---"
echo "KEY:     $SESSION_KEY"
echo "WORKFLOW: https://raw.githubusercontent.com/Harshmaury/Nexus/main/WORKFLOW.md"
echo "BRANCH:  $BRANCH"
echo "COMMIT:  $LAST"
echo "BUILD:   $([ -z "$BUILD_OUT" ] && echo 'PASS' || echo "FAIL")"
echo "DONE:    $DONE items"
echo "TODO:    $TODO items"
echo "NEXT:    $(grep "^\s*\[ \]" WORKFLOW.md 2>/dev/null | head -2 | tr '\n' ' | ' | sed 's/^[[:space:]]*//')"
echo "---NEXUS-SESSION-END---"
echo ""
