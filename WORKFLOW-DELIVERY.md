# WORKFLOW-DELIVERY.md
# @version: 1.0.0
# @updated: 2026-03-15
# @scope: ALL code deliveries to ~/workspace/projects/apps/nexus

---

## THIS FILE IS LAW
# Every zip Claude produces for this project MUST follow this protocol.
# No exceptions. No shortcuts.

---

## ZIP NAMING

```
nexus-<phase>-<what>-<YYYYMMDD>-<HHMM>.zip
```

| Segment   | Rule                                              | Example            |
|-----------|---------------------------------------------------|--------------------|
| `nexus`   | always literal                                    | `nexus`            |
| `<phase>` | phaseN or fix or hotfix or patch                  | `phase14`, `fix1`  |
| `<what>`  | 1–3 hyphenated words describing the change        | `agent-mode`       |
| `<date>`  | YYYYMMDD of delivery                              | `20260315`         |
| `<time>`  | HHMM of delivery (24h)                            | `0046`             |

Good: `nexus-phase14-agent-mode-20260315-0035.zip`
Good: `nexus-fix1-sigch-unused-20260315-0046.zip`
Bad:  `nexus.zip`, `fix.zip`, `phase14.zip`

---

## ZIP STRUCTURE

Files inside the zip MUST mirror the repo directory tree exactly.
No wrapper folder. Unzip with `-o -d .` from repo root drops every
file in the correct location automatically.

```
nexus-phase14-agent-mode-20260315-0035.zip
  internal/state/db_agents.go        ← correct
  cmd/engxa/main.go                  ← correct
  WORKFLOW-SESSION.md                ← correct

nexus-phase14-agent-mode-20260315-0035.zip
  nexus-phase14/internal/state/...   ← WRONG — wrapper folder
```

Single-file hotfix: use `-j` flag to strip folder, target the exact dir with `-d`.

---

## STANDARD APPLY COMMAND

Copy this exactly. Substitute the zip name and file list.

```bash
cd ~/workspace/projects/apps/nexus && \
unzip -o /mnt/c/Users/harsh/Downloads/nexus-drop/<ZIP_NAME>.zip -d . && \
go build ./... && \
git add <file1> <file2> ... WORKFLOW-SESSION.md && \
git commit -m "<type>: <description>" && \
git push origin <branch>
```

For a single-file hotfix (flat zip, `-j` was used when creating):

```bash
cd ~/workspace/projects/apps/nexus && \
unzip -oj /mnt/c/Users/harsh/Downloads/nexus-drop/<ZIP_NAME>.zip -d <target-dir>/ && \
go build ./... && \
git add <file> && \
git commit -m "fix: <description>" && \
git push origin <branch>
```

**`go build ./...` MUST pass before `git add` runs.**
If it fails, the chain stops. Fix the error, get a new zip, rerun.

---

## COMMIT MESSAGE FORMAT

```
<type>: <description>

types: feat | fix | refactor | test | docs | chore
```

| Scenario          | Message                                               |
|-------------------|-------------------------------------------------------|
| New phase         | `feat: phase 14 — agent mode, engxa binary`           |
| Bug fix           | `fix: remove unused sigCh in engx watchCmd`           |
| Workflow update   | `docs: update WORKFLOW-SESSION.md to v2.6.0`          |
| Patch to phase    | `fix: phase 14 patch — correct server route method`   |

---

## WORKFLOW-SESSION.md RULE

WORKFLOW-SESSION.md MUST travel in every zip.
It is always listed in `git add`.
It is always the last file added before commit.
Version number bumps on every delivery.

---

## FULL EXAMPLE (phase delivery)

```bash
cd ~/workspace/projects/apps/nexus && \
unzip -o /mnt/c/Users/harsh/Downloads/nexus-drop/nexus-phase14-agent-mode-20260315-0035.zip -d . && \
go build ./... && \
git add internal/state/db_agents.go \
        internal/state/storer.go \
        internal/agent/client.go \
        internal/api/handler/agents.go \
        internal/api/server.go \
        cmd/engxa/main.go \
        cmd/engx/main.go \
        WORKFLOW-SESSION.md && \
git commit -m "feat: phase 14 — multi-machine agent mode, engxa binary" && \
git push origin phase14-agent-mode
```

## FULL EXAMPLE (single-file hotfix)

```bash
cd ~/workspace/projects/apps/nexus && \
unzip -oj /mnt/c/Users/harsh/Downloads/nexus-drop/nexus-fix1-sigch-unused-20260315-0046.zip -d cmd/engx/ && \
go build ./... && \
git add cmd/engx/main.go && \
git commit -m "fix: remove unused sigCh in engx watchCmd" && \
git push origin phase14-agent-mode
```

---

## CHECKLIST (run mentally before every apply)

- [ ] Zip name follows `nexus-<phase>-<what>-<YYYYMMDD>-<HHMM>.zip`
- [ ] Zip is in `C:\Users\harsh\Downloads\nexus-drop\`
- [ ] Running from `~/workspace/projects/apps/nexus`
- [ ] On the correct branch before applying
- [ ] `go build ./...` passes before `git add`
- [ ] `WORKFLOW-SESSION.md` is in `git add`
- [ ] Commit message follows `type: description`
