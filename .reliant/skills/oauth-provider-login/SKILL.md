---
name: oauth-provider-login
description: How Claude / Codex provider OAuth login works in Electron (daemon callback) versus the web browser (`reliant auth serve` on localhost:19284), and the key files on each side. Load before touching provider login or OAuth callback code.
---

# OAuth (Claude / Codex provider login)

Claude and Codex OAuth flows require a localhost callback receiver. The
architecture differs by runtime:

- **Electron**: the daemon handles it automatically via `auth.start_oauth` — no user action needed.
- **Web browser**: there is no local daemon. Users run `reliant auth serve` in their terminal, which starts a lightweight HTTP server on `localhost:19284` with:
  - `GET /health` — the frontend pings this to detect availability
  - `POST /oauth/start` — receives the authorize URL template, opens the browser, waits for the callback, returns the auth code

The `auth serve` server is **stateless** — it only bridges the localhost
callback gap. Token exchange and persistence always go through the
authenticated backend gRPC (`completeClaudeOAuth` / `completeCodexOAuth`).

Key files:

- `internal/auth/oauthcallback/` — shared Go package for OAuth callback handling (used by both the daemon and `auth serve`)
- `cmd/reliant/commands/auth_serve.go` — the `reliant auth serve` CLI command
- `web/src/lib/oauth-local.ts` — frontend helper to call the local server
- `web/src/hooks/useOAuthAvailability.ts` — whether OAuth is available (Electron → always, web → pings health)
- `web/src/lib/claude-oauth.ts` / `codex-oauth.ts` — flow libs that branch between daemon (Electron) and local server (web)
