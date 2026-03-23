# DNS Setup — get.engx.dev

## Required DNS records

Add these to your DNS provider (Cloudflare, Route53, etc.) for the `engx.dev` zone:

```
Type    Name    Value                   TTL
CNAME   get     Harshmaury.github.io    300
```

## GitHub Pages settings

In the Nexus repo on GitHub:
1. Settings → Pages
2. Source: GitHub Actions
3. Custom domain: get.engx.dev
4. ✅ Enforce HTTPS (once DNS propagates)

## Verification

Once DNS propagates (5–60 minutes):
```bash
# Should return the install.sh content
curl -fsSL https://get.engx.dev/install.sh | head -5

# Should return the landing page
curl -fsSL https://get.engx.dev/ | grep "engx"
```

## What gets served

The `docs/` directory is served at `get.engx.dev`:
- `https://get.engx.dev/`            → docs/index.html (landing page)
- `https://get.engx.dev/install.sh`  → docs/install.sh (installer)

## Fallback install (while DNS propagates)

```bash
curl -fsSL https://raw.githubusercontent.com/Harshmaury/Nexus/main/docs/install.sh | bash
```
