# OSS Migration — Status

## What's done

### OSS repo (`reliant-labs/reliant`) — 47 files changed, +382 -4,757

**Auth overhaul:**
- [x] Deleted `internal/auth/keys.go` (hardcoded Supabase ES256 public key)
- [x] Removed hardcoded `dash.reliantlabs.io` URL and `sb_publishable_*` key from `internal/auth/oauth.go`
- [x] `RELIANT_AUTH_URL` / `RELIANT_AUTH_KEY` now required for OAuth (no fallback)
- [x] `RELIANT_JWT_PUBLIC_KEY` required in `monolith.go` and `serverapi/run.go` (no embedded key fallback)
- [x] Added API key auth mode (`AUTH_MODE=apikey` + `AUTH_API_KEY`) — `internal/auth/apikey.go` with tests
- [x] Added JWKS URL support (`RELIANT_JWKS_URL`) — wired into monolith, server, and gRPC interceptor
- [x] Exported `TokenValidator` interface for pluggable auth
- [x] Frontend auth mode switching — `useAuthMode` hook, `ApiKeyLogin` component, authStore API key session
- [x] gRPC client API key bearer token support
- [x] `/health` endpoint exposes `auth_mode` for frontend discovery

**Hosted config removal:**
- [x] Cleared hosted Sentry DSN, Supabase URL/key from `.env`
- [x] Cleaned `.env.example` — no hosted defaults, auth vars promoted to primary config
- [x] Fixed stale "embedded Supabase key" in `server.go` flag help and `distributed.mdx` docs
- [x] Fixed CORS defaults — removed hardcoded `reliant-prod.web.app,reliantlabs.io`, now defaults to `*`

**Removed hosted-only features:**
- [x] Deleted `supabase/` directory (edge functions, migrations, email templates)
- [x] Removed feedback system entirely (`feedback.ts`, `FeedbackModal.tsx`, `FeedbackSettings.tsx`, etc.)
- [x] Cleaned `.gitignore` Supabase entries

**Release pipeline:**
- [x] Deleted `.github/workflows/release.yml` (moved to control plane)
- [x] Updated `contributing/RELEASE_SETUP.md`

### Control plane (`reliant-labs/control-plane`)

**Release pipeline:**
- [x] Created `.github/workflows/release-electron.yml` (cross-repo Electron build + publish)
- [x] Added `electron-release` job to `deploy.yml` — triggers after prod deploy succeeds
- [x] Removed premature `trigger-release` from `watch-reliant-image.yml`
- [x] Created `scripts/migrate-secrets.sh` for interactive secret migration
- [x] 27/28 secrets migrated from GCP Secret Manager automatically
- [x] Set GitHub variables: `CUSTOMERIO_CHANGELOG_BROADCAST_ID`, `VITE_SENTRY_REPLAY_CANVAS`

**Release flow (new):**
```
OSS tag v1.3.0 → GHCR image
  → watch-reliant-image detects it → PR to bump versions.yaml
  → PR merge → build-images → deploy staging (auto)
  → tag push on control-plane → deploy prod (manual approval)
  → prod succeeds → dispatch release-electron
  → Electron builds (macOS/Win/Linux) → GitHub Release on OSS repo
  → Homebrew tap dispatch → R2 upload → Cloudflare cache purge
```

---

## What's remaining

### Manual: Set APPLE_ID secret

The only missing secret. It's the Apple Developer email used for macOS notarization (team `23P64LQTZD`):
```bash
gh secret set APPLE_ID --repo reliant-labs/control-plane --body "your-apple-email@reliantlabs.io"
```

### Optional: Cosmetic cleanup

**Analytics client naming** — `internal/analytics/client.go` has `gtmURL`/`gtmAPIKey` fields. Rename to `analyticsURL`/`analyticsAPIKey`.

**Hosted links** — `reliantlabs.io`, `docs.reliantlabs.io`, `downloads.reliantlabs.io` in README/docs are product links, not credentials. Standard for vendor-backed OSS.

---

## Auth modes summary

| Mode | Config | Backend | Frontend |
|------|--------|---------|----------|
| **Dev** | `ENVIRONMENT=development` | Bypass — all requests get dev user | Auto-authenticated |
| **API Key** | `AUTH_MODE=apikey` + `AUTH_API_KEY=secret` | Bearer token validated | API key input screen |
| **Supabase (PEM)** | `RELIANT_JWT_PUBLIC_KEY=...` | JWT validated with key | Supabase auth screen |
| **Supabase (JWKS)** | `RELIANT_JWKS_URL=https://...` | JWT validated via JWKS | Supabase auth screen |
| **Supabase (OAuth)** | `RELIANT_AUTH_URL` + `RELIANT_AUTH_KEY` | OAuth PKCE flow | Supabase auth screen |
