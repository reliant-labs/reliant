# Briefing: prod Electron config + daemon login UX

Shared context for the parallel work streams. **Do not delete this file** — the
parent agent owns cleanup. Every fact in "Verified" below was checked directly
against DNS, live HTTP, GCP Secret Manager, or the shipped production logs.
Do NOT re-derive them.

---

## The user-visible bug

Opening the packaged (prod) Electron app spawns browser tabs pointing at
`localhost`. Up to 5 per launch, then the app sits with no working backend.

## Root cause chain (VERIFIED — do not re-derive)

1. Packaged build bakes `RELIANT_SERVER_URL=https://api.reliant.so/api`.
2. **`api.reliant.so` does not exist.** The whole `reliant.so` zone is
   undelegated — no NS, no SOA from `8.8.8.8`. It is not a missing subdomain;
   the domain itself is dead.
3. `electron/src/daemon-creds.js` `ensureDaemonPATForOrigin` tries to pre-mint a
   daemon PAT so the daemon finds credentials and skips its own registration.
   The host is unreachable, so it fails and — **by design** — swallows the error
   (`backend-manager.js:1120`, "never throws").
4. The daemon spawns, finds no creds for that origin in `~/.reliant/daemon.json`,
   and runs `ensureDaemonCredentials` → `registerDaemon`
   (`cmd/reliant/commands/daemon.go:83`) → `auth.Login` → `openBrowser`
   (`internal/auth/oauth.go:271`). That call is **unconditional** — there is no
   TTY check, no non-interactive flag anywhere in `cmd/` or `internal/auth/`.
5. `auth.Login` starts a temp HTTP server on a random loopback port and opens
   `http://127.0.0.1:<port>` — the localhost tab the user sees.
6. Daemon never becomes ready → `waitForReady` 30s timeout → crash-restart loop,
   5 attempts, a fresh tab each time.

Production log proving it (`~/Library/Logs/reliant/main.log`, 14 occurrences):

```
[daemon-creds] ensureDaemonPATForOrigin: minting daemon PAT for origin https://api.reliant.so
[daemon-creds] mint failed, falling back to daemon flow: fetch failed
[Daemon]: No daemon credentials found. Registering...
[Daemon]: Not logged in. Starting authentication...
[Daemon]: Opening browser to log in...
...
[BackendManager] Daemon failed to become ready within 30000ms
[BackendManager] Max restart attempts (5) reached, giving up
```

---

## Verified production facts

### Hostnames — the REAL prod domain is `reliantapi.com`

`reliant.so` was never real. `control-plane/deploy/kcl/prod/main.k` uses
`reliantapi.com` throughout and contains **zero** references to `reliant.so`.

| Host | DNS | HTTP | Notes |
|---|---|---|---|
| `api.reliantapi.com` | 34.63.203.181 | **401** at `/` | THE api server. Live. |
| `gateway.reliantapi.com` | 34.31.112.130 | 404 at `/` | THE daemon gateway. Live. |
| `admin.reliantapi.com` | 34.122.251.238 | — | admin-server |
| `dash.reliantlabs.io` | → supabase.co | — | Supabase / `SUPABASE_URL` |
| `app.reliantlabs.io` | → reliant-prod.web.app | — | the web SPA |
| `api.reliant.so` | **NXDOMAIN** | dead | current baked value — WRONG |
| `gateway-api.reliantapi.com` | **NXDOMAIN** | dead | see gateway bug below |

### CRITICAL: base URL takes NO `/api` suffix

The current value ends in `/api`; that path 404s. Probes:

```
GET  https://api.reliantapi.com/            → 401 {"error":"missing bearer token"}
GET  https://api.reliantapi.com/health      → 200
POST https://api.reliantapi.com/reliant.v1.SettingsService/ListSettings      → 401
POST https://api.reliantapi.com/api/reliant.v1.SettingsService/ListSettings  → 404
```

401 with `missing bearer token` on the un-suffixed Connect RPC path is the
server routing correctly and rejecting an unauthenticated call — that is the
**correct** base. 404 under `/api` is the router not knowing the path.

**So the correct value is `https://api.reliantapi.com` — no trailing `/api`.**

### SECOND BUG: gateway derivation produces a dead host

`deriveGatewayURL` (`cmd/reliant/commands/connection.go:219`) prefixes the host:

- 2+ dots → `gateway-<first>.<rest>`
- 1 dot  → `gateway.<host>`

`api.reliantapi.com` has 2 dots, so it derives **`gateway-api.reliantapi.com`**
— confirmed **NXDOMAIN**. The real gateway is `gateway.reliantapi.com`.

So even after fixing the API URL, the daemon still cannot reach a gateway unless
`RELIANT_GATEWAY_URL` is set explicitly. Both must be fixed or prod stays broken.
This is exactly what the shipped log shows:
`gateway="https://gateway-api.reliant.so/api (derived from server ...)"`.

### The LIVE web app already uses the correct host (independent confirmation)

The deployed SPA at `app.reliantlabs.io` was built from a bundle that has the
RIGHT hostnames baked in. Extracted from the live JS bundle
(`/assets/index-BPhpfHH3.js`):

```
https://api.reliantapi.com      ← the correct API base, NO /api suffix
https://admin.reliantapi.com
https://dash.reliantlabs.io
https://app.reliantlabs.io
https://docs.reliantlabs.io
```

Zero occurrences of `reliant.so`. So production web is healthy and already
proves the correct value; only the DESKTOP build carries the dead host. This is
independent corroboration that `https://api.reliantapi.com` (no `/api`) is right.

Note the same `secrets.VITE_API_URL` also feeds the web `.env` at
`release.yml:167` — so whatever built the live bundle did NOT use that secret's
current value, or used it before it drifted. Worth confirming the web release
path is not silently broken for the NEXT web release.

### Gateway derivation: prod is the odd one out (NOT a broken function)

`deriveGatewayURL`'s convention is correct for every env EXCEPT prod:

```
preprod.reliantapi.com → gateway-preprod.reliantapi.com → 8.232.106.145  LIVE  ✅
api.reliantapi.com     → gateway-api.reliantapi.com     → NXDOMAIN       DEAD  ❌
real prod gateway is     gateway.reliantapi.com         → 34.31.112.130  LIVE
```

Confirmed by control-plane's own KCL:
- `deploy/kcl/preprod/main.k:133` → `GATEWAY_URL=https://gateway-preprod.reliantapi.com`
- `deploy/kcl/prod/main.k:201`    → `GATEWAY_URL=https://gateway.reliantapi.com`

prod's API host is `api.` rather than a bare env name, so prefixing yields
`gateway-api.` instead of the actual `gateway.`. Therefore **`RELIANT_GATEWAY_URL`
must be set explicitly for prod** — it is required, not belt-and-braces.

The apex `reliantapi.com` resolves (34.117.36.251) but does NOT serve HTTPS
(curl exit 35, TLS failure). It is not a usable fallback.

### GCP Secret Manager

Project `reliant-labs-475814`, Secret Manager API enabled, `gcloud` authenticated
as `sean@reliantlabs.io` and working.

- `VITE_API_URL` currently = `https://api.reliant.so/api` ← **the dead host**
- `VITE_SUPABASE_URL` = `https://dash.reliantlabs.io` (correct)
- Also present: `SUPABASE_ANON_KEY`, `VITE_SUPABASE_ANON_KEY`, `STATSIG_CLIENT_KEY`,
  `VITE_SENTRY_DSN`, Apple signing (`CSC_*`, `APPLE_*`), `AWS_*`, `CLOUDFLARE_*`,
  `AZURE_*`, `HOMEBREW_TAP_TOKEN`, `BUF_API_KEY`.
- Adding a new secret VERSION is non-destructive (old versions are retained and
  can be re-enabled). Creating a new secret is additive. Neither is risky.
- **Never disable/destroy an existing version or secret.**

### Config duplication in the release pipeline

`.github/workflows/release.yml` (1313 lines) generates `src/build-config.js` from
**three separate copy-pasted blocks** at lines **510, 709, 894** (macOS, Linux,
Windows jobs). Same keys, same validation, three copies — they can and did drift.

Config also reaches the binary via ldflags into `internal/builddefaults`
(`release.yml:355-357`) whose vars MUST stay package-level strings (`-ldflags -X`
cannot write anything else — see that package's doc comment).

Precedence at runtime (`builddefaults.Value`): env var > compiled-in `-X` default
> neutral `http://localhost:8080` fallback.

### Existing forge dev-electron service (REUSE THIS)

Already built, in `control-plane`:

- `deploy/kcl/lib/services.k:415` — `reliant_electron_base` (`forge.Service`,
  name `reliant-electron`)
- `deploy/kcl/dev/main.k` — `_electron_svc`, opt-in via `forge env up dev -D electron=1`,
  uses `host = HostOverrides { command_override = ["npm","run","dev:electron"],
  working_dir = "../reliant/electron" }`
- `Taskfile.yml` — `task electron` is superseded by the forge command

Forge primitives that already exist (do NOT rebuild):

- `forge/internal/deploytarget/external.go` — generic `sh -c` deploy provider with
  `deploy_cmd`/`rollback_cmd`/`health_cmd`, `${IMAGE}/${TAG}/${CODE_VERSION}/${ENV}/${LAST_TAG}`
  substitution, deploy-state tracking for rollback.
- `forge/internal/secrets/secrets.go` — `Provider` interface (`Kind`/`Resolve`/`All`)
  with `file` / `external` / `none` kinds. **No cloud-manager provider exists yet.**
- `forge/kcl/core.k:545` — `Service`, explicitly target-agnostic.

---

## Ground rules for every stream

- **NEVER run git commands.** No commit, stash, checkout, reset, add. Multiple
  agents share this worktree; the user reviews and commits. This is a hard rule
  from project memory.
- **Never kill processes** — no `pkill`, no `forge env down`. This agent session
  runs inside forge; a pattern-kill ends the whole session and every sibling agent.
- Write a test that **FAILS before your fix and PASSES after**, and report the
  actual before/after output. A fix without a reproduction is a guess.
- Report real command output. A confident false "it works" is much worse than a
  clear "this blocked here."
- Touch ONLY the files in your own ownership list. Another agent owns the rest.
- This project has **not launched** — no backwards compatibility is required.
  Remove old code paths rather than adding compatibility shims.

## Repo layout

Root `/Users/seanteeling/src/reliant-labs` with nested repos: `reliant` (the
product — Electron, daemon, web), `control-plane` (forge app, deploy config),
`forge` (the framework). Paths below are relative to whichever repo is named.

## Test commands

- Electron JS: `cd reliant/electron && npm test` (node --test; add new files to
  the `test` script in `electron/package.json`)
- Go: `cd reliant && go test ./internal/auth/... ./cmd/...`
- Go build gate: `cd reliant && go build ./...`
