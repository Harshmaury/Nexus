# WORKFLOW-DELIVERY.md
# @version: 2.0.0
# @updated: 2026-03-16

---

## DROP FOLDER

Windows:  C:\Users\harsh\Downloads\engx-drop\
WSL2:     /mnt/c/Users/harsh/Downloads/engx-drop/

---

## ZIP NAMING

```
nexus-<what>-<YYYYMMDD>-<HHMM>.zip
```

Examples: `nexus-fix-watcher-race-20260316-1200.zip`
          `nexus-phase15-atlas-events-20260316-0900.zip`

---

## ZIP STRUCTURE

Mirror the repo tree exactly. No wrapper folder.

```
nexus-fix-something-20260316-1200.zip
  internal/state/db.go          ← correct
  WORKFLOW-SESSION.md            ← correct

  nexus/internal/state/db.go     ← WRONG
```

---

## APPLY COMMAND

```bash
cd ~/workspace/projects/apps/nexus && \
unzip -o /mnt/c/Users/harsh/Downloads/engx-drop/<ZIP>.zip -d . && \
go build ./... && \
git add <files> WORKFLOW-SESSION.md && \
git commit -m "<type>: <description>" && \
git push origin <branch>
```

`go build ./...` must pass before `git add`. Always.

---

## COMMIT FORMAT

```
<type>: <description>
types: feat | fix | refactor | test | docs | chore
```

---

## RULES

- WORKFLOW-SESSION.md travels in every zip
- Version bumps on every delivery
- One logical unit per zip — no batching unrelated changes
- Grep all import usages before removing any import
