---
name: adopt/libraries
description: forge's runtime libraries in an existing app, with no forge project — Go's github.com/reliant-labs/forge/pkg/* (observe, svcerr, middleware, pgtest, testkit, …) and npm's @reliantlabs/forge-web-runtime (/node initNodeTelemetry, browser telemetry, Connect interceptors). What each is good for standalone, how to add it, and how to verify.
emit: both
applies: any
---

# Adopt: forge's runtime libraries

Ordinary libraries, Apache-2.0, usable with no forge.yaml and no CLI. Part of
`adopt`; `forge-libraries` has the adopt-or-port judgment once a repo is a
forge project.

**Estimate.** ~15 min for the first service, +10 per further service; ~2 min
of the user's time to review the diff.

## Go: `github.com/reliant-labs/forge/pkg/*`

One module. Add it and import the packages you need:

```bash
go get github.com/reliant-labs/forge@latest   # raises the module's go directive to forge's (1.27 today)
forge project libraries                        # every package and what it is for
forge project libraries svcerr                 # one package's full signatures (go doc shows no methods)
```

Say the `go` directive bump to the user before you add it: it is the one
change outside their own code.

| Package | Standalone use |
|---|---|
| `observe` | `Setup` (OTel + propagator), `NewTraceHandler` / `NewErrorClassHandler` for slog, `DefaultMiddlewares` (Connect), `NewHTTPClient` (traced outbound) — `adopt/monitoring` has the code |
| `middleware` | `RequestIDMiddleware`, `HTTPStack` (recovery, request log, audit) for `net/http`; `CORSMiddleware`, `SecurityHeadersMiddleware`, `VerifyHMACSHA256` for webhooks |
| `svcerr` | in a Connect service, `svcerr.Interceptor()` plus `return nil, svcerr.Wrap(err)` / `svcerr.NotFound("…")`: a classified code to the caller, and an unclassified error reaches it as `internal server error`, its SQL and DSN kept in the log |
| `pgtest`, `testkit` | `db, cleanup, err := pgtest.New()` in a test: a real embedded Postgres shared machine-wide, no Docker; `testkit.Slow(t, "<why>")` marks a slow test (`forge lint --slow-tests` checks) |
| `tdd` | table-driven test helpers |
| `serverkit` | the probe contract and `DBReadyCheck`; it runs a whole forge server, so take the pieces, not `serverkit.Run` |

Note that `svcerr.Wrap` classifies what it knows: wrap a `sql.ErrNoRows`
yourself as `svcerr.NotFound(...)`, or the caller sees `internal`.

**Verify.** `go build ./...` passes; a test using `pgtest.New()` runs with no
Docker; for `svcerr`, a handler returning `svcerr.Wrap(driverErr)` answers
`internal` with no driver text in the response, and the log has it.

## Node, Electron and the browser: `@reliantlabs/forge-web-runtime`

```bash
npm i @reliantlabs/forge-web-runtime @opentelemetry/api
```

- **`/node`**: `initNodeTelemetry({ serviceName, serviceVersion, environment,
  serviceNamespace, endpoint, headers })` returns `{ logger, tracer,
  reportError, setUser, flush, shutdown, dropped }`, or `null` when
  `endpoint` is unset, empty or `off` (it then installs nothing). Uncaught
  exceptions and unhandled rejections become ERROR records with the same
  `exception.*` keys forge's Go writes; credential shapes are redacted; batches
  flush on `beforeExit`; `flushOnQuit(app)` for Electron. It posts OTLP/HTTP
  JSON to `${endpoint}/v1/logs` and `/v1/traces`. Logs and traces, no metrics.
  Node ≥ 22. `adopt/monitoring` has an Express middleware around it.
- **`/telemetry`**: browser telemetry (errors, console, network, traces) that
  ships to the page's own origin at `/_otel`, so it needs a relay there; forge
  frontends get one from `forge env up` and hosting. For a site hosted
  elsewhere, skip it rather than invent a relay.
- **`/interceptors`**: the Connect interceptor stack (auth, `traceparent`,
  typed errors). Only for an app that already speaks Connect.

**Verify (Node).** Point `endpoint` at a collector, or at a throwaway local
HTTP server that records request paths; call `telemetry.logger.info(...)` and
`telemetry.reportError(new Error("boom"))`, `await telemetry.flush()`, and see
a POST to `/v1/logs` (and `/v1/traces` after a span).

## Before porting a utility

Run `forge project libraries` first: retries, money, API keys, SSRF-safe
dialing, encryption with key rotation and Postgres-lock singletons are already
there, tested.
