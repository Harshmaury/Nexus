#!/usr/bin/env bash
# ═══════════════════════════════════════════════════════════════
# NEXUS CONTEXT DUMP v1.0
# Generates a clean, token-efficient ZIP for AI context sharing
# Usage: ./context-dump.sh [project-name] [task-description]
# ═══════════════════════════════════════════════════════════════

set -euo pipefail

# ── CONFIG ────────────────────────────────────────────────────
NEXUS_HOME="$HOME/dev/nexus"
PROJECTS_REGISTRY="$NEXUS_HOME/configs/projects.yaml"
OUTPUT_DIR="$HOME/bin/nexus-drops"
TIMESTAMP=$(date +"%Y%m%d_%H%M")
PROJECT="${1:-all}"
TASK="${2:-Not specified}"

# ── COLORS ───────────────────────────────────────────────────
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
BLUE='\033[0;34m'; CYAN='\033[0;36m'; NC='\033[0m'; BOLD='\033[1m'

echo -e "${CYAN}${BOLD}"
echo "╔═══════════════════════════════════════╗"
echo "║       NEXUS CONTEXT DUMP v1.0         ║"
echo "║   Smart AI Context Generator          ║"
echo "╚═══════════════════════════════════════╝"
echo -e "${NC}"

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
  # Logs and locks
  "*.log" "*.lock" "package-lock.json" "yarn.lock"
  "go.sum"
  # Binaries and media
  "*.exe" "*.dll" "*.so" "*.dylib"
  "*.jpg" "*.jpeg" "*.png" "*.gif" "*.mp4" "*.zip"
  "*.tar.gz" "*.bin"
  # Secrets
  ".env" ".env.*" "*.pem" "*.key" "*.cert"
  "secrets.yaml" "secrets.json"
  # IDE
  ".vscode" ".idea" "*.suo" "*.user"
  # Large folders (>10MB handled separately)
  "migrations/data" "seeds/large"
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
  echo "## SYSTEM SNAPSHOT"
  echo "Generated: $(date -u '+%Y-%m-%d %H:%M:%S UTC')"
  echo "Machine: $(hostname) | WSL2 Ubuntu $(lsb_release -rs 2>/dev/null || echo 'unknown')"
  echo ""
  echo "### Runtimes"
  echo "- Go: $(go version 2>/dev/null | awk '{print $3}' || echo 'not installed')"
  echo "- Python: $(python3 --version 2>/dev/null | awk '{print $2}' || echo 'not installed')"
  echo "- Node: $(node --version 2>/dev/null || echo 'not installed')"
  echo "- .NET: $(dotnet --version 2>/dev/null || echo 'not installed')"
  echo "- Docker: $(docker --version 2>/dev/null | awk '{print $3}' | tr -d ',' || echo 'not installed')"
  echo "- kubectl: $(kubectl version --client -o json 2>/dev/null | python3 -c 'import sys,json; print(json.load(sys.stdin)["clientVersion"]["gitVersion"])' 2>/dev/null || echo 'not installed')"
  echo "- Minikube: $(minikube version 2>/dev/null | head -1 | awk '{print $3}' || echo 'not installed')"
  echo ""
  echo "### Infrastructure Status"
  echo "- Minikube: $(minikube status 2>/dev/null | grep 'host:' | awk '{print $2}' || echo 'unknown')"
  echo "- Docker containers: $(docker ps --format '{{.Names}}' 2>/dev/null | tr '\n' ', ' || echo 'none')"
  echo ""
  echo "### Disk"
  df -h "$HOME" 2>/dev/null | tail -1 | awk '{print "- Home: "$3" used / "$2" total ("$5" full)"}'
}

# ── PROJECT TREE (clean, max depth 4) ─────────────────────────
generate_project_tree() {
  local project_path="$1"
  local project_name="$2"

  echo "## PROJECT STRUCTURE: $project_name"
  echo "Path: $project_path"
  echo ""
  echo '```'

  if command -v tree &>/dev/null; then
    tree "$project_path" -L 4 \
      --noreport \
      -I "node_modules|vendor|venv|.venv|__pycache__|.git|bin|obj|dist|build|*.log" \
      2>/dev/null || find "$project_path" -maxdepth 4 -not -path '*/.git/*' -not -path '*/node_modules/*' -not -path '*/bin/*' -not -path '*/obj/*' | sort | sed "s|$project_path||" | head -100
  else
    find "$project_path" -maxdepth 4 \
      -not -path '*/.git/*' \
      -not -path '*/node_modules/*' \
      -not -path '*/bin/*' \
      -not -path '*/obj/*' \
      -not -path '*/__pycache__/*' \
      -not -path '*/venv/*' \
      | sort | sed "s|$project_path/||" | head -120
  fi

  echo '```'
}

# ── GIT CONTEXT ───────────────────────────────────────────────
generate_git_context() {
  local project_path="$1"

  if [ -d "$project_path/.git" ]; then
    echo "## GIT STATE"
    echo "- Branch: $(git -C "$project_path" branch --show-current 2>/dev/null)"
    echo "- Remote: $(git -C "$project_path" remote get-url origin 2>/dev/null || echo 'none')"
    echo ""
    echo "### Recent commits (last 7)"
    git -C "$project_path" log --oneline -7 2>/dev/null | while read -r line; do
      echo "- $line"
    done
    echo ""
    echo "### Uncommitted changes"
    git -C "$project_path" status --short 2>/dev/null | head -20 | while read -r line; do
      echo "- $line"
    done
  fi
}

# ── DEPENDENCIES ─────────────────────────────────────────────
generate_dependencies() {
  local project_path="$1"

  echo "## DEPENDENCIES"

  # Go
  if [ -f "$project_path/go.mod" ]; then
    echo "### Go (go.mod)"
    grep "^require\|^\t" "$project_path/go.mod" 2>/dev/null | head -30
  fi

  # .NET
  find "$project_path" -name "*.csproj" 2>/dev/null | head -5 | while read -r f; do
    echo "### .NET: $(basename "$f")"
    grep "PackageReference" "$f" 2>/dev/null | sed 's/.*Include="\([^"]*\)".*/- \1/' | head -20
  done

  # Python
  if [ -f "$project_path/requirements.txt" ]; then
    echo "### Python (requirements.txt)"
    head -20 "$project_path/requirements.txt" | while read -r line; do
      echo "- $line"
    done
  fi

  # Node
  if [ -f "$project_path/package.json" ]; then
    echo "### Node (package.json)"
    python3 -c "
import json, sys
with open('$project_path/package.json') as f:
    p = json.load(f)
deps = {**p.get('dependencies',{}), **p.get('devDependencies',{})}
for k,v in list(deps.items())[:20]:
    print(f'- {k}: {v}')
" 2>/dev/null
  fi
}

# ── ENV VARIABLES (KEYS HIDDEN) ───────────────────────────────
generate_env_snapshot() {
  local project_path="$1"

  # Find .env.example or .env (hide values)
  for env_file in "$project_path/.env.example" "$project_path/.env.dev" "$project_path/.env.template"; do
    if [ -f "$env_file" ]; then
      echo "## ENVIRONMENT VARIABLES (from $(basename "$env_file"))"
      echo "Keys only — values hidden for security:"
      grep -v '^#' "$env_file" 2>/dev/null | grep '=' | sed 's/=.*/=***HIDDEN***/' | while read -r line; do
        echo "- $line"
      done
      break
    fi
  done
}

# ── ACTIVE PORTS ─────────────────────────────────────────────
generate_ports() {
  echo "## ACTIVE SERVICES & PORTS"
  if command -v ss &>/dev/null; then
    ss -tlnp 2>/dev/null | grep LISTEN | awk '{print $4}' | grep -oP ':\K\d+' | sort -n | while read -r port; do
      echo "- Port $port: open"
    done | head -20
  fi

  # Kubernetes services
  if minikube status &>/dev/null 2>&1; then
    echo ""
    echo "### Kubernetes Services"
    kubectl get services --all-namespaces 2>/dev/null | grep -v "^NAMESPACE" | awk '{print "- "$2": "$5}' | head -15
  fi
}

# ── TOKEN COUNTER ────────────────────────────────────────────
count_tokens_approx() {
  local file="$1"
  local words=$(wc -w < "$file" 2>/dev/null || echo 0)
  echo $((words * 4 / 3))  # rough token approximation
}

# ── MAIN BUILD ───────────────────────────────────────────────
main() {
  mkdir -p "$OUTPUT_DIR"
  local WORK_DIR=$(mktemp -d)
  local CONTEXT_FILE="$WORK_DIR/NEXUS_CONTEXT.md"
  local OUTPUT_ZIP="$OUTPUT_DIR/nexus-context__${PROJECT}__${TIMESTAMP}.zip"

  echo -e "${YELLOW}→ Collecting system snapshot...${NC}"

  # ── BUILD CONTEXT FILE ──────────────────────────────────────
  cat > "$CONTEXT_FILE" << HEADER
# NEXUS AI CONTEXT
> Auto-generated by Nexus Context Dump v1.0
> $(date -u '+%Y-%m-%d %H:%M:%S UTC')
> DO NOT EDIT MANUALLY

---

## CURRENT TASK
$TASK

## HOW TO USE THIS FILE
You are an expert software engineer helping Harsh Maury.
Read this entire file before responding.
Follow the coding standards in the AI_SKILLSET section.
Ask clarifying questions BEFORE writing any code.
Generate files using the naming convention: [project]__[feature]__[YYYYMMDD_HHMM].[ext]
First line of every file must be: // @nexus-project: [project-name]
Second line: // @nexus-path: [relative/path/to/file]

---

HEADER

  # System snapshot
  generate_system_snapshot >> "$CONTEXT_FILE"
  echo "" >> "$CONTEXT_FILE"
  echo "---" >> "$CONTEXT_FILE"
  echo "" >> "$CONTEXT_FILE"

  # Active ports
  generate_ports >> "$CONTEXT_FILE"
  echo "" >> "$CONTEXT_FILE"
  echo "---" >> "$CONTEXT_FILE"
  echo "" >> "$CONTEXT_FILE"

  # Projects
  echo -e "${YELLOW}→ Scanning projects...${NC}"

  # Define projects to scan
  declare -A PROJECTS
  PROJECTS["nexus"]="$HOME/dev/nexus"
  PROJECTS["ums"]="/mnt/c/Users/harsh/source/repos/AspireApp1"
  PROJECTS["ai"]="$HOME/dev/experiments/ai"

  if [ "$PROJECT" != "all" ] && [ -n "${PROJECTS[$PROJECT]:-}" ]; then
    # Single project
    local path="${PROJECTS[$PROJECT]}"
    if [ -d "$path" ]; then
      generate_project_tree "$path" "$PROJECT" >> "$CONTEXT_FILE"
      echo "" >> "$CONTEXT_FILE"
      generate_git_context "$path" >> "$CONTEXT_FILE"
      echo "" >> "$CONTEXT_FILE"
      generate_dependencies "$path" >> "$CONTEXT_FILE"
      echo "" >> "$CONTEXT_FILE"
      generate_env_snapshot "$path" >> "$CONTEXT_FILE"
      echo "" >> "$CONTEXT_FILE"
    fi
  else
    # All projects
    for proj_name in "${!PROJECTS[@]}"; do
      local proj_path="${PROJECTS[$proj_name]}"
      if [ -d "$proj_path" ]; then
        echo -e "${BLUE}  Scanning: $proj_name${NC}"
        generate_project_tree "$proj_path" "$proj_name" >> "$CONTEXT_FILE"
        echo "" >> "$CONTEXT_FILE"
        generate_git_context "$proj_path" >> "$CONTEXT_FILE"
        echo "" >> "$CONTEXT_FILE"
        generate_dependencies "$proj_path" >> "$CONTEXT_FILE"
        echo "" >> "$CONTEXT_FILE"
      fi
    done
  fi

  echo "---" >> "$CONTEXT_FILE"
  echo "" >> "$CONTEXT_FILE"

  # AI Skillset
  cat >> "$CONTEXT_FILE" << 'SKILLSET'
## AI SKILLSET — MANDATORY CODING STANDARDS

### Code Quality
- Follow SOLID principles strictly
- Single responsibility per function/class/file
- Maximum function length: 40 lines
- All errors must be handled explicitly — never swallow errors
- No magic numbers — use named constants
- Write self-documenting code — variable names explain intent

### Architecture Rules
- Clean Architecture: separate domain, application, infrastructure layers
- Dependency injection everywhere — no static globals
- Interfaces over concrete types
- Repository pattern for all data access

### File & Naming Conventions
- Every generated file: [project]__[feature]__[YYYYMMDD_HHMM].[ext]
- First line: // @nexus-project: [name]
- Second line: // @nexus-path: [relative/path]
- No abbreviations in names (usr → user, cfg → config, svc → service)

### Before Writing Any Code
1. State what you understood from the request
2. Ask ANY missing information (max 3 questions)
3. Confirm the approach before coding
4. List every file you will create/modify

### Security
- Never log sensitive data
- Validate all inputs
- Use parameterized queries always
- Secrets from environment only — never hardcoded

### Testing
- Every public function needs a test
- Table-driven tests (Go), parametrize (Python), Theory (C#)
- Test file naming: [original_name]_test.[ext]

### IEEE & Industry Standards
- Follow IEEE 830 for requirements documentation
- Follow IEEE 1016 for software design
- REST APIs: follow RFC 7231, OpenAPI 3.0
- Git commits: Conventional Commits specification
- Semantic versioning: SemVer 2.0

SKILLSET

  # ── BUILD ZIP ──────────────────────────────────────────────
  echo -e "${YELLOW}→ Building ZIP...${NC}"

  local EXCLUDE_ARGS=$(build_exclude_args)

  # Add context file
  cp "$CONTEXT_FILE" "$WORK_DIR/"

  # Add AI skillset separately
  cp "$CONTEXT_FILE" "$WORK_DIR/AI_SKILLSET.md"

  # Create ZIP with exclusions
  cd "$WORK_DIR"

  # Add source files if single project (clean cut, no bloat)
  if [ "$PROJECT" != "all" ] && [ -n "${PROJECTS[$PROJECT]:-}" ]; then
    local src_path="${PROJECTS[$PROJECT]}"
    if [ -d "$src_path" ]; then
      # Only include source files, strictly filtered
      find "$src_path" -type f \
        -not -path "*/.git/*" \
        -not -path "*/node_modules/*" \
        -not -path "*/bin/*" -not -path "*/obj/*" \
        -not -path "*/__pycache__/*" \
        -not -path "*/venv/*" \
        -not -path "*/.venv/*" \
        -not -name "*.log" -not -name "*.lock" \
        -not -name "*.exe" -not -name "*.dll" \
        -not -name ".env" \
        -not -name "go.sum" \
        -size -500k \
        2>/dev/null | head -150 | while read -r f; do
          local rel_path="${f#$src_path/}"
          local dest_dir="$WORK_DIR/src/$(dirname "$rel_path")"
          mkdir -p "$dest_dir"
          cp "$f" "$dest_dir/" 2>/dev/null || true
        done
    fi
  fi

  zip -r "$OUTPUT_ZIP" . -x "*.DS_Store" -x "__MACOSX/*" > /dev/null 2>&1

  # ── STATS ──────────────────────────────────────────────────
  local zip_size=$(du -sh "$OUTPUT_ZIP" 2>/dev/null | cut -f1)
  local token_estimate=$(count_tokens_approx "$CONTEXT_FILE")
  local line_count=$(wc -l < "$CONTEXT_FILE")

  echo ""
  echo -e "${GREEN}${BOLD}╔══════════════════════════════════════════╗"
  echo -e "║           CONTEXT DUMP COMPLETE          ║"
  echo -e "╚══════════════════════════════════════════╝${NC}"
  echo ""
  echo -e "${CYAN}Output:${NC}     $OUTPUT_ZIP"
  echo -e "${CYAN}ZIP Size:${NC}   $zip_size"
  echo -e "${CYAN}Context:${NC}    $line_count lines"
  echo -e "${CYAN}~Tokens:${NC}    $token_estimate tokens"
  echo -e "${CYAN}Task:${NC}       $TASK"
  echo ""
  echo -e "${YELLOW}Next step:${NC}"
  echo "  1. Upload $OUTPUT_ZIP to Claude/ChatGPT"
  echo "  2. Paste the AI_SKILLSET from NEXUS_CONTEXT.md"
  echo "  3. Describe your task — AI already knows everything"
  echo ""

  # Cleanup
  rm -rf "$WORK_DIR"
}

main "$@"
