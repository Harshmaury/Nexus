#!/usr/bin/env bash
# @nexus-project: nexus
# @nexus-path: scripts/context-dump.sh
# ═══════════════════════════════════════════════════════════════
# NEXUS CONTEXT DUMP v2.0.0
# @updated: 2026-03-15
#
# Generates clean, token-efficient ZIPs for AI context sharing.
# Covers all three platform projects + governance repo.
#
# Usage:
#   ./scripts/context-dump.sh              → all platform projects
#   ./scripts/context-dump.sh nexus        → Nexus only
#   ./scripts/context-dump.sh atlas        → Atlas only
#   ./scripts/context-dump.sh forge        → Forge only
#   ./scripts/context-dump.sh workdox      → AI_CONTEXT.md/ sections only
#   ./scripts/context-dump.sh architecture → ADRs + capability docs only
#   ./scripts/context-dump.sh platform     → developer-platform/ governance only
#
# Output: /mnt/c/Users/harsh/Downloads/workdox-dump/
# ═══════════════════════════════════════════════════════════════

set -euo pipefail

# ── CONFIG ────────────────────────────────────────────────────
WORKSPACE="$HOME/workspace"
OUTPUT_DIR="/mnt/c/Users/harsh/Downloads/workdox-dump"
TIMESTAMP=$(date +"%Y%m%d-%H%M")
TARGET="${1:-all}"

# ── COLORS ───────────────────────────────────────────────────
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
BLUE='\033[0;34m'; CYAN='\033[0;36m'; NC='\033[0m'; BOLD='\033[1m'

echo -e "${CYAN}${BOLD}"
echo "╔═══════════════════════════════════════╗"
echo "║     NEXUS CONTEXT DUMP v2.0.0         ║"
echo "║   Developer Platform Context Export   ║"
echo "╚═══════════════════════════════════════╝"
echo -e "${NC}"

# ── PROJECT REGISTRY ─────────────────────────────────────────
declare -A PROJECT_PATHS
PROJECT_PATHS["nexus"]="$WORKSPACE/projects/apps/nexus"
PROJECT_PATHS["atlas"]="$WORKSPACE/projects/apps/atlas"
PROJECT_PATHS["forge"]="$WORKSPACE/projects/apps/forge"
PROJECT_PATHS["developer-platform"]="$WORKSPACE/developer-platform"

declare -A PROJECT_TYPES
PROJECT_TYPES["nexus"]="platform-daemon"
PROJECT_TYPES["atlas"]="platform-service"
PROJECT_TYPES["forge"]="platform-service"
PROJECT_TYPES["developer-platform"]="governance"

# ── EXCLUSION PATTERNS ────────────────────────────────────────
EXCLUDES=(
  # Dependencies
  "node_modules" "vendor" "venv" ".venv" "env"
  "__pycache__" ".pytest_cache" ".mypy_cache"
  # Build outputs
  "bin" "obj" "out" "dist" "build" "target"
  ".next" ".nuxt" "coverage"
  # Version control
  ".git" ".svn" ".hg"
  # Locks and generated
  "*.log" "*.lock" "package-lock.json" "yarn.lock" "go.sum"
  # Binaries and media
  "*.exe" "*.dll" "*.so" "*.dylib"
  "*.jpg" "*.jpeg" "*.png" "*.gif" "*.mp4"
  "*.tar.gz" "*.bin" "*.db"
  # Secrets
  ".env" ".env.*" "*.pem" "*.key" "*.cert"
  # IDE
  ".vscode" ".idea" "*.suo" "*.user"
)

build_exclude_args() {
  local args=""
  for pattern in "${EXCLUDES[@]}"; do
    args="$args --exclude=$pattern"
  done
  echo "$args"
}

# ── SYSTEM SNAPSHOT ──────────────────────────────────────────
generate_system_snapshot() {
  echo "## System Snapshot"
  echo "Generated: $(date -u '+%Y-%m-%d %H:%M:%S UTC')"
  echo "Machine:   $(hostname) | WSL2 Ubuntu $(lsb_release -rs 2>/dev/null || echo 'unknown')"
  echo ""
  echo "### Runtimes"
  echo "- Go:        $(go version 2>/dev/null | awk '{print $3}' || echo 'not installed')"
  echo "- Python:    $(python3 --version 2>/dev/null | awk '{print $2}' || echo 'not installed')"
  echo "- Node:      $(node --version 2>/dev/null || echo 'not installed')"
  echo "- Docker:    $(docker --version 2>/dev/null | awk '{print $3}' | tr -d ',' || echo 'not installed')"
  echo "- kubectl:   $(kubectl version --client --short 2>/dev/null | head -1 || echo 'not installed')"
  echo "- Minikube:  $(minikube version 2>/dev/null | head -1 | awk '{print $3}' || echo 'not installed')"
  echo ""
  echo "### Platform Status"
  echo "- Nexus  :8080  $(curl -s http://127.0.0.1:8080/health 2>/dev/null | python3 -c 'import sys,json; d=json.load(sys.stdin); print("✓ " + d.get("status","?"))' 2>/dev/null || echo '✗ not reachable')"
  echo "- Atlas  :8081  $(curl -s http://127.0.0.1:8081/health 2>/dev/null | python3 -c 'import sys,json; d=json.load(sys.stdin); print("✓ " + d.get("status","?"))' 2>/dev/null || echo '✗ not reachable')"
  echo "- Forge  :8082  $(curl -s http://127.0.0.1:8082/health 2>/dev/null | python3 -c 'import sys,json; d=json.load(sys.stdin); print("✓ " + d.get("status","?"))' 2>/dev/null || echo '✗ not started')"
  echo ""
  echo "### Disk"
  df -h "$HOME" 2>/dev/null | tail -1 | \
    awk '{print "- Home: " $3 " used / " $2 " total (" $5 " full)"}'
}

# ── GIT CONTEXT ───────────────────────────────────────────────
generate_git_context() {
  local project_path="$1"
  local project_name="$2"

  if [ -d "$project_path/.git" ]; then
    echo "## Git State — $project_name"
    echo "- Branch: $(git -C "$project_path" branch --show-current 2>/dev/null)"
    echo "- Remote: $(git -C "$project_path" remote get-url origin 2>/dev/null || echo 'none')"
    echo "- Last:   $(git -C "$project_path" log --oneline -1 2>/dev/null)"
    echo ""
    echo "### Recent commits"
    git -C "$project_path" log --oneline -7 2>/dev/null | \
      while read -r line; do echo "  - $line"; done
    echo ""
    local dirty
    dirty=$(git -C "$project_path" status --short 2>/dev/null | wc -l | tr -d ' ')
    if [ "$dirty" -gt 0 ]; then
      echo "### Uncommitted changes ($dirty files)"
      git -C "$project_path" status --short 2>/dev/null | head -10 | \
        while read -r line; do echo "  $line"; done
    else
      echo "### Working tree: clean"
    fi
  fi
}

# ── PROJECT TREE ─────────────────────────────────────────────
generate_project_tree() {
  local project_path="$1"
  local project_name="$2"

  echo "## Project Structure — $project_name"
  echo "Path: $project_path"
  echo ""
  echo '```'
  if command -v tree &>/dev/null; then
    tree "$project_path" -L 4 --noreport \
      -I "node_modules|vendor|venv|.venv|__pycache__|.git|bin|obj|dist|build|*.log|*.db" \
      2>/dev/null
  else
    find "$project_path" -maxdepth 4 \
      -not -path '*/.git/*' \
      -not -path '*/node_modules/*' \
      -not -path '*/bin/*' \
      -not -path '*/obj/*' \
      -not -path '*/__pycache__/*' \
      | sort | sed "s|$project_path/||" | head -100
  fi
  echo '```'
}

# ── BUILD SINGLE PROJECT ZIP ─────────────────────────────────
dump_project() {
  local name="$1"
  local path="${PROJECT_PATHS[$name]}"

  if [ ! -d "$path" ]; then
    echo -e "${RED}  ✗ $name not found at $path — skipping${NC}"
    return
  fi

  local zip_name="${name}-${TIMESTAMP}.zip"
  local zip_path="$OUTPUT_DIR/$zip_name"
  local work_dir
  work_dir=$(mktemp -d)

  echo -e "${BLUE}  → $name${NC}"

  # Context file
  local ctx="$work_dir/CONTEXT.md"
  {
    echo "# ${name^} Context"
    echo "Timestamp: $TIMESTAMP"
    echo "Path: $path"
    echo ""
    generate_system_snapshot
    echo ""
    echo "---"
    echo ""
    generate_project_tree "$path" "$name"
    echo ""
    echo "---"
    echo ""
    generate_git_context "$path" "$name"
  } > "$ctx"

  # Source files — filtered, size-capped
  local EXCLUDE_ARGS
  EXCLUDE_ARGS=$(build_exclude_args)

  find "$path" -type f \
    -not -path "*/.git/*" \
    -not -path "*/node_modules/*" \
    -not -path "*/bin/*" \
    -not -path "*/obj/*" \
    -not -path "*/__pycache__/*" \
    -not -path "*/.venv/*" \
    -not -name "*.log" \
    -not -name "*.lock" \
    -not -name "go.sum" \
    -not -name "*.exe" \
    -not -name "*.dll" \
    -not -name ".env" \
    -not -name "*.db" \
    -size -500k \
    2>/dev/null | head -200 | while read -r f; do
      local rel="${f#$path/}"
      local dest="$work_dir/$(dirname "$rel")"
      mkdir -p "$dest"
      cp "$f" "$dest/" 2>/dev/null || true
    done

  cp "$ctx" "$work_dir/CONTEXT.md"

  cd "$work_dir"
  zip -r "$zip_path" . \
    -x "*.DS_Store" \
    -x "__MACOSX/*" \
    > /dev/null 2>&1

  local size
  size=$(du -sh "$zip_path" 2>/dev/null | cut -f1)
  echo -e "    ${GREEN}✓ $zip_name ($size)${NC}"

  rm -rf "$work_dir"
}

# ── WORKDOX DUMP ─────────────────────────────────────────────
dump_workdox() {
  local zip_name="workdox-${TIMESTAMP}.zip"
  local zip_path="$OUTPUT_DIR/$zip_name"
  local src="$WORKSPACE/AI_CONTEXT.md"

  echo -e "${BLUE}  → workdox (AI_CONTEXT.md/)${NC}"

  if [ ! -d "$src" ]; then
    echo -e "${RED}  ✗ $src not found — skipping${NC}"
    return
  fi

  local work_dir
  work_dir=$(mktemp -d)
  cp -r "$src/." "$work_dir/"

  # Add a CONTEXT.md manifest
  {
    echo "# Workspace Documentation Context"
    echo ""
    echo "Timestamp: $TIMESTAMP"
    echo "Path: $src"
    echo ""
    echo "## Documents Included"
    ls "$src" | while read -r f; do echo "$f"; done
    echo ""
    echo "## Structure"
    find "$work_dir" -type f | sort | sed "s|$work_dir/||"
  } > "$work_dir/CONTEXT.md"

  cd "$work_dir"
  zip -r "$zip_path" . \
    -x "*.DS_Store" \
    -x "__MACOSX/*" \
    > /dev/null 2>&1

  local size
  size=$(du -sh "$zip_path" 2>/dev/null | cut -f1)
  echo -e "    ${GREEN}✓ $zip_name ($size)${NC}"

  rm -rf "$work_dir"
}

# ── ARCHITECTURE DUMP ─────────────────────────────────────────
dump_architecture() {
  local zip_name="architecture-${TIMESTAMP}.zip"
  local zip_path="$OUTPUT_DIR/$zip_name"
  local src="$WORKSPACE/architecture"

  echo -e "${BLUE}  → architecture (ADRs + capability docs)${NC}"

  if [ ! -d "$src" ]; then
    echo -e "${RED}  ✗ $src not found — skipping${NC}"
    return
  fi

  local work_dir
  work_dir=$(mktemp -d)
  cp -r "$src/." "$work_dir/"

  {
    echo "# Architecture Context"
    echo ""
    echo "Timestamp: $TIMESTAMP"
    echo ""
    echo "Files:"
    find "$work_dir" -type f | sort | sed "s|$work_dir/||"
  } > "$work_dir/CONTEXT.md"

  cd "$work_dir"
  zip -r "$zip_path" . \
    -x "*.DS_Store" \
    -x "__MACOSX/*" \
    > /dev/null 2>&1

  local size
  size=$(du -sh "$zip_path" 2>/dev/null | cut -f1)
  echo -e "    ${GREEN}✓ $zip_name ($size)${NC}"

  rm -rf "$work_dir"
}

# ── DEVELOPER PLATFORM DUMP ──────────────────────────────────
dump_platform() {
  local zip_name="developer-platform-${TIMESTAMP}.zip"
  local zip_path="$OUTPUT_DIR/$zip_name"
  local src="$WORKSPACE/developer-platform"

  echo -e "${BLUE}  → developer-platform (governance repo)${NC}"

  if [ ! -d "$src" ]; then
    echo -e "${RED}  ✗ $src not found — skipping${NC}"
    return
  fi

  local work_dir
  work_dir=$(mktemp -d)

  find "$src" -type f \
    -not -path "*/.git/*" \
    -not -name "*.log" \
    -size -500k \
    2>/dev/null | while read -r f; do
      local rel="${f#$src/}"
      local dest="$work_dir/$(dirname "$rel")"
      mkdir -p "$dest"
      cp "$f" "$dest/" 2>/dev/null || true
    done

  {
    echo "# Developer Platform Context"
    echo ""
    echo "Timestamp: $TIMESTAMP"
    echo "Path: $src"
    echo ""
    echo "Files:"
    find "$work_dir" -type f | sort | sed "s|$work_dir/||"
  } > "$work_dir/CONTEXT.md"

  cd "$work_dir"
  zip -r "$zip_path" . \
    -x "*.DS_Store" \
    -x "__MACOSX/*" \
    > /dev/null 2>&1

  local size
  size=$(du -sh "$zip_path" 2>/dev/null | cut -f1)
  echo -e "    ${GREEN}✓ $zip_name ($size)${NC}"

  rm -rf "$work_dir"
}

# ── MAIN ─────────────────────────────────────────────────────
main() {
  mkdir -p "$OUTPUT_DIR"

  echo -e "${YELLOW}Output: $OUTPUT_DIR${NC}"
  echo -e "${YELLOW}Target: $TARGET${NC}"
  echo ""
  echo -e "${YELLOW}→ Building dumps...${NC}"
  echo ""

  case "$TARGET" in
    nexus)            dump_project "nexus" ;;
    atlas)            dump_project "atlas" ;;
    forge)            dump_project "forge" ;;
    developer-platform|platform) dump_platform ;;
    workdox)          dump_workdox ;;
    architecture)     dump_architecture ;;
    all)
      dump_workdox
      dump_architecture
      dump_platform
      dump_project "nexus"
      dump_project "atlas"
      dump_project "forge"
      ;;
    *)
      echo -e "${RED}Unknown target: $TARGET${NC}"
      echo "Valid targets: all | nexus | atlas | forge | workdox | architecture | platform"
      exit 1
      ;;
  esac

  echo ""
  echo -e "${GREEN}${BOLD}╔══════════════════════════════════════════╗"
  echo -e "║         CONTEXT DUMP COMPLETE           ║"
  echo -e "╚══════════════════════════════════════════╝${NC}"
  echo ""
  echo -e "${CYAN}Output folder:${NC} $OUTPUT_DIR"
  echo ""
  echo -e "${YELLOW}Next step:${NC}"
  echo "  Upload the ZIP(s) to Claude and describe your task."
  echo "  Claude reads context in this order:"
  echo "    1. workdox-*            (platform foundation)"
  echo "    2. architecture-*       (ADRs + boundaries)"
  echo "    3. developer-platform-* (governance)"
  echo "    4. nexus-* / atlas-* / forge-* (project code)"
  echo ""
}

main "$@"
