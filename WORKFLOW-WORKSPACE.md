# WORKFLOW-WORKSPACE.md
# @version: 4.0.0
# @updated: 2026-03-16

---

## WORKSPACE LAYOUT

```
~/workspace/
  developer-platform/   ADRs, capability docs, governance (no code)
  architecture/
    decisions/           ADR-001 ... ADR-NNN
  projects/
    apps/
      nexus/             Control domain   :8080
      atlas/             Knowledge domain :8081
      forge/             Execution domain :8082
    archive/             Inactive projects
```

---

## PROJECTS

| Repo  | Path                            | Port | Tag                    | Status             |
|-------|---------------------------------|------|------------------------|--------------------|
| Nexus | ~/workspace/projects/apps/nexus | 8080 | v1.0.0-fixes-complete  | Feature-complete   |
| Atlas | ~/workspace/projects/apps/atlas | 8081 | v0.3.0-fixes-complete  | Phase 1+2 complete |
| Forge | ~/workspace/projects/apps/forge | 8082 | v0.4.0-fixes-complete  | Phase 1+2+3 complete |

---

## DROP FOLDER (all three projects share one folder)

Windows:  C:\Users\harsh\Downloads\engx-drop\
WSL2:     /mnt/c/Users/harsh/Downloads/engx-drop/

---

## PLATFORM STARTUP

```bash
engxd &
sleep 2
~/bin/atlas &
~/bin/forge &
curl -s http://127.0.0.1:8080/health
curl -s http://127.0.0.1:8081/health
curl -s http://127.0.0.1:8082/health
```

Atlas and Forge reconnect to Nexus automatically within 3s.

---

## REGISTER A PROJECT WITH NEXUS

```bash
# Minimum .nexus.yaml
name: my-project
type: web-api
language: go

# Register and verify
engx register /path/to/project
engx project status my-project
curl -s http://127.0.0.1:8081/workspace/project/my-project
```

---

## GIT WORKFLOW

Branch: main (always stable) | feature branches: feat-description
Commit: feat | fix | refactor | test | docs | chore
Rule:   go build ./... must pass before git add

---

## UMS (separate concern — not part of developer platform)

path:  /mnt/c/Users/harsh/source/repos/AspireApp1
lang:  .NET 10 + C#  |  branch: master
ci:    GitHub Actions, runner at ~/actions-runner
ports: identity:5002 academic:5004 student:5003 attendance:5005
       examination:5006 fee:5007 faculty:5008 hostel:5009 notification:5010
obs:   Grafana:3000 Prometheus:9090 Jaeger:16686 Seq:8081
