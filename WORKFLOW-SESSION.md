# WORKFLOW-SESSION.md
# Session: nexus-landing-page-github-pages
# Date: 2026-03-21

## What changed — Landing page + GitHub Pages

Adds docs/ folder with landing page (index.html) and install script (install.sh)
served via GitHub Pages. After enabling Pages from main/docs in repo settings,
the platform has a public home page and a working install URL.

## New files

- `docs/index.html`   — landing page
- `docs/install.sh`   — install script served via GitHub Pages

## Apply

```bash
cd ~/workspace/projects/engx/services/nexus && \
unzip -o /mnt/c/Users/harsh/Downloads/engx-drop/nexus-landing-page-20260321-1200.zip -d . && \
go build ./...
```

## Verify

```bash
ls docs/
# docs/index.html  docs/install.sh
```

## Commit

```bash
git add docs/ WORKFLOW-SESSION.md && \
git commit -m "feat: landing page + GitHub Pages (docs/)" && \
git push origin main
```

## After push

Enable GitHub Pages at:
  https://github.com/Harshmaury/Nexus/settings/pages
  Source: Deploy from branch → main → /docs → Save

Live at:    https://harshmaury.github.io/Nexus/
Install:    curl -fsSL https://harshmaury.github.io/Nexus/install.sh | bash
