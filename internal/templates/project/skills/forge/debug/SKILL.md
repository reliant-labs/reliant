---
name: debug
description: Debug methodology — triage running-system evidence, guarded data reads, parallel investigation, and synthesis.
emit: both
---

# Debug Methodology

## Triage First

Classify the bug before diving in:

- **Crash / panic** → check logs, attach a debugger.
- **Wrong behavior** → trace the code path, form hypotheses, then verify with a targeted test.
- **Only in multi-service / multi-process flows** → reproduce with an end-to-end test against the full stack.
- **Flaky test** → run in a loop with stress / race detection enabled.
- **Works locally, fails in CI** → diff the environment (versions, env vars, filesystem, time, network) before assuming a code bug.

## Parallel Investigation

If your environment supports parallel agents, split the work into three independent tracks:

- **Researcher** — trace code paths, form hypotheses, check git log for recent changes that touch the affected area.
- **Tester** — write a failing test that isolates the bug. Top-down bisection: start at the outermost reproducing layer (e2e), then narrow to the unit.
- **Reproducer** — add diagnostic logging, exercise the path in a running system, collect runtime evidence (request IDs, error chains, timing).

Working solo? Run the three passes sequentially: research → reproduce → isolate.

## Synthesis

Combine findings from all tracks before proposing a fix:

- **Root cause** with confidence level (High / Medium / Low).
- **Evidence** from each investigation track — which path, which test, which log line.
- **Recommended fix** approach — hand off to an implementer; don't fix in debug mode.

## Discipline

- **Root cause over symptom.** Trace the failing call through the *real* code path and read the actual server / pod / service logs — don't pattern-match the error message and guess. The first plausible explanation is often a symptom one layer down from the cause.
- **Reproduce before you guess.** A bug you can't trigger on demand is one you can't verify a fix for.
- **One hypothesis per test.** A failing test that "exercises the bug area" doesn't prove the hypothesis; one that fails for exactly one reason does.
- **Check connectivity and credentials first.** For anything that crosses a process or cluster boundary, the top failure mode is stale connection state, not a logic bug: can the caller actually reach the target API/endpoint? Is the kubeconfig context current and pointed at the right cluster? Are the credentials live? Confirm the wire before you debug the payload.
- **Fail fast — isolate and name the one blocker.** When stuck, don't loop re-running the same failing command with small tweaks. Identify the single specific thing that's blocking (a 403, an unreachable host, an undefined symbol) and state it plainly. A precise blocker is actionable; "it's broken" is not.
- **Don't widen the diff.** Touch only what the evidence indicts; refactoring "while you're in there" turns a 1-line fix into a 200-line PR that needs its own review.

<!-- @forge-only:start -->
## Forge-Specific Triage

On top of the generic triage above, common forge-shaped bug classes:

- **Stale generated code / config drift** → if symbols are "undefined" or a build breaks *right after* a proto / forge.yaml / schema change, suspect stale gen before you chase the code. Run `forge generate`, then `forge project audit` to surface drift, then retest. Forge generates from proto + forge.yaml; stale gen masquerades as a bug in hand-written code.
- **Can't reach a service / cross-cluster flow fails** → verify connectivity and credentials before assuming a logic bug. Each workload deploys to the kubectl context its `forge.OnCluster {target}` names (multi-cluster is native); a "service unreachable" or auth failure is usually a stale or wrong kubectl context, an expired credential, or a service that isn't up — not a code path. Confirm the context is current and the target responds first.
- **Broken DI wiring** → check the explicit composition (`internal/app/compose.go` `NewComponents`, off the owned `internal/app/providers.go` `OpenInfra`). Deps are interface-typed fields resolved by type off `Infra`, so a missing required provider is a compile error (`NewComponents` won't build) or a `forge generate` "no provider" report, not a silent nil — but a wrong fill (an unintended optional dep left nil) can still surface as a nil-pointer panic deep in handler code. There is no generated `wire_gen.go`/`bootstrap.go` to inspect; the wiring is `NewComponents` filling typed `Deps` off `Infra`.
- **Mock vs real divergence** → tests pass with generated mocks but the real adapter fails. Re-run the integration suite (`task test:integration`) before chasing a unit-test ghost.
- **Proto-DB drift** → entity types and DB schema evolve independently; `forge project audit` flags the mismatch. If the symptom is "column X not found" in a handler that names the right struct field, audit first.

## Forge Debug Tools

```
forge project introspect handlers      # every RPC path the binary registers — localize the fault
forge api curl <svc.method>    # build a copy-pasteable Connect curl from the shell
forge cluster logs --service X # kubectl-backed log tail for one service (single cluster)
forge debug start <svc>        # build + attach Delve debugger
forge debug start --attach PID # attach Delve to a live process (don't `stop` it — see below)
forge debug break/continue/eval# breakpoints, resume, evaluate in the debug session
forge debug stop               # ends the session AND kills the debugged process
task test -- -v ./internal/handlers/<name>/... # verbose isolated test runs
task test:e2e                 # full-stack reproduction
task test              # run tests with race detector
forge generate                 # regenerate code (use when stale gen is suspected)
```

### Ask the running system before reading code

Start with `forge obs status <env> --json`, then follow the failing request,
not a broad production scan. Load `forge skill load observability/query` for
the full verb/JSON reference and `forge skill load db/query` for application
rows. Each step should narrow the hypothesis:

| Evidence needed | Read-only command |
|---|---|
| Scope, telemetry freshness, failing services and release | `forge obs status prod --since 15m --json` |
| Error lines in that window | `forge obs logs prod 'body:timeout' --service api --since 15m --limit 5 --json` |
| Failed requests, then the call tree | `forge obs traces prod --errors --service api --since 15m --limit 5 --json`, then `forge obs trace <trace-id> prod` |
| Grouped cause, call site and latest event | `forge obs issues prod --since 15m --limit 5 --json`, then `forge obs issue <id> prod` |
| Logs for that request | `forge obs logs prod --trace <trace-id> --since 15m --limit 5 --json` |
| Available metrics, then values | `forge obs metrics list prod --contains rpc --since 15m`, then `forge obs metrics query prod rpc.server.call.duration --agg quantile --quantile 0.95 --since 15m` |
| A question beyond the structured verbs | `forge obs endpoints dev --explain --json`, then adapt its SQL with `forge obs query dev --sql-file investigation.sql` |
| A dashboard panel's data | `forge obs query dev --panel panel.json --since 15m` |
| A dashboard that looks empty | `forge obs dashboards audit prod --dashboard <id> --json` (recipe below) |
| Continuous CPU/heap evidence | `forge obs profile --env dev --service api --since 15m -o /tmp/api.pb.gz` (local ClickStack; see below) |

`api` is an example **telemetry service.name**, not necessarily a workload or
binary name. Use the name status actually returned. Prefer complete `suggested`
commands from the result, but verify their env/backend before running them.
Use `forge env releases at <env> <time-or-trace-id>` to relate evidence to the
release live then. `forge obs check <env>` judges a deploy, not an arbitrary
application-flow invariant. Reading issues is not triage: do not resolve,
ignore, assign or comment during a read-only investigation.

#### Bind the project, environment and authority first

Run the project's pinned Forge; check `forge version` and heed version-skew
warnings. An embedded `reliant forge` can differ from the project's standalone
binary. Run in the project root (or select it with `-C <project-root>`), and
name the env explicitly: most obs verbs default to dev, **db query never does**.
Servers need not share your filesystem; `--sql-file` and `--panel` read files
on the CLI machine, not a remote API server.

The env declaration should resolve the telemetry location: local ClickStack
or the control plane. Normal use is env-first, without a backend flag. Check
the returned `scope` (env, backend, cloud env_id, services and time window).
On an older binary that routes the env incorrectly, `--backend cloud` or
`--backend local` is a troubleshooting override, not required configuration;
report the routing/version mismatch instead of normalizing it. Start with a
short window, then fix `--from`/`--to` for comparable evidence.
`--explain` prints SQL/parameters instead of executing the telemetry query;
it is not a connectivity or authorization test.

Cloud reads require the credential's role-bound `obs:read`; release reads may
also need `deploy:read`, and hosted database reads need `db:query`. Under
Reliant use the credential helper, not an interactive `forge login`. Elsewhere
use the project's documented login. HTTP 403/missing scope on a valid token
is an authorization blocker, not evidence of an app outage. Report it to the
owner/admin; do not mint broader tokens, read secret values or bypass policy.
A backend that is itself broken (for example ClickHouse code 33) may also break
its own observability: use an already-authorized, read-only out-of-band source
such as the owning cluster's pod logs. Coordinate with the incident
investigator rather than duplicating scans or escalating privileges.

#### Unknown is not healthy

With `--json`, inspect `ok`, `exit_code`, `error`, `scope`, and, where present,
`unread`, `warnings`, `not_evaluated`, `truncated`, `omitted`, `next_page_token`.
**Status exits 0 when any section was read**, even if every telemetry section
is unread and only the release ledger was available. An empty array means
read-and-empty; an absent/unread section means unknown. A capped result is
incomplete: narrow or page it, never infer absence from it.

For obs reads, exit 1 means a bad/refused request; exit 2 means Forge could not
look (auth, quota, unreachable/off telemetry or an unsupported backend call).
Database reads similarly distinguish refused SQL/policy (1) from unavailable,
rate-limited or timed-out reads (2). Preserve the exact error/reason: missing
scope, quota and a SQL error need different actions. Do not retry blindly or
turn a failed query into “no matching errors”.

#### Query telemetry and application data separately

`forge obs query` is **ClickHouse telemetry SQL**, not the app's Postgres.
Start with `--explain` from a structured verb. For example, count logs in the
bound window without interpolating values into the statement:

```bash
forge obs query dev --since 15m --service api --sql \
  'SELECT count() AS n FROM otel_logs
   WHERE Timestamp >= {from:DateTime64(9)} AND Timestamp < {to:DateTime64(9)}
   AND (empty({services:Array(String)}) OR has({services:Array(String)}, ServiceName))'
```

The parameters bind time/services; keep the predicates when adapting SQL.
The cloud backend enforces the credential's environment scope. The guard
allows one read-only SELECT, not DDL, writes, system tables or table functions.
`--panel` takes **one panels[] element**, not a whole dashboard document;
load `observability/dashboards` for authoring and `observability/query` for
`--param`, `--step` and the JSON contract.

`forge db query` reads the workload's **application database** instead:

```bash
forge db query dev 'select 1 as reachable' --max-rows 1 --timeout 5s --json
# For an existing, authorized route to a non-hosted cluster DB:
forge db query prod 'select 1 as reachable' --addr localhost:15432 \
  --confirm-env prod --reason 'incident connectivity check' --max-rows 1 --json
```

Use `--workload` (default `api`) and, for several managed DBs, `--database` to
select the database that owns the records. A database forge does not run
(Cloud SQL behind a pooler) is reachable when the env declares it as a
`forge.ExternalDatabase`: `--database <name>` then tunnels to its service
with your kube context, on a real env too (load `db/query`). `--addr host:port` supplies only
**your existing VPN/tunnel/bastion route**; it creates no route and bypasses
neither policy nor guards. Hosted envs refuse `--addr`: the control plane
executes/audits the query. Cluster routes use `forge_ro`, its kube Secret and
CA without printing credentials; host/compose keeps the workload's URL user
with host/port replaced, not necessarily `forge_ro`. Keep default cluster TLS
verification; do not disable it to get past a failed connection.

Every query is one statement in a timed READ ONLY transaction under row/byte
caps. Real envs normally require `--confirm-env` and `--reason`; `deny` stays
denied. Hosted audited access is person/role-bound, not a machine-token escape
hatch. The managed read-only role excludes secret columns; select explicit
safe columns or aggregates, not `SELECT *`. Never reset, seed, migrate or write
production data to investigate it. `select 1` proves connectivity only, not
that app records or schema are correct. Full transport/policy rules: `db/query`.

#### An empty dashboard

Run `forge obs dashboards audit <env>` to replay the console's panels. Empty
panels are probed to distinguish absent data from data outside the window,
environment or filters; failed probes are not evidence of absence. Load
`forge skill load debug/dashboards` for verdicts, failure classes and remedies.

#### Tool API versus CLI

Forge's stdio MCP server is `forge mcp` (embedded: `reliant forge mcp`).
Register the entrypoint your installed binary actually exposes; load `debug`
with `skill_load` using `name: "debug"` and `dir: "<project-root>"` to select
the project. Its current catalog exposes project/skill/lint/generate tools,
**not obs or db query tools**. Use the harness's shell for those CLI reads;
do not invent MCP tool names. See `docs/mcp.md` in the Forge repository for
registration and the tool list. Older embedded binaries may not expose `mcp`;
check `--help` before registering.

**Pick the tool for the job — and know where each stops short:**

- **`forge project introspect handlers` to localize.** Prints every RPC path the assembled
  binary serves. If the failing RPC isn't there, the fault is a downstream/remote
  hop, not this binary — that one check collapses the search space.
- **`forge api curl <service.method>` to exercise an endpoint.** Builds and runs a
  Connect request from the shell, but it **stops at the auth interceptor** — it
  does not mint a token. Use it for shape/connectivity; supply your own credential
  for an authed call.
- **`forge cluster logs --service <name>` for the symptom.** kubectl-backed, no
  file-grepping — but it tails only the **owner cluster** (the one cluster name in
  config). For a multi-cluster app, set the context and run
  `kubectl --context <other> -n <ns> logs <pod>` for the other half.
- **`forge debug start <svc>` (Delve) — two sharp edges.** In a multi-binary repo
  `start <service>` can mis-build (it falls back to `./cmd/...`); pass an explicit
  path/service so it builds the binary you mean. And `forge debug stop` after
  `--attach` **kills the live process** — detach instead of `stop` when you've
  attached to something you need to keep running.

**Smoke/status are NOT app-flow proofs.** A green `forge env smoke` / `forge env status <env>`
checks listeners, local compose, and telemetry — never app-flow invariants. They
(and `forge cluster status`, green whenever pods are `Running`) can all be green
while the actual flow is broken. The **only** things that prove an app-flow fix:

1. a declarative, exit-coded app-health assertion (model: a project `doctor:<flow>`
   task that fails non-zero when the invariant is violated), and
2. a full `task test:e2e`.

Generic forge tools localize and surface evidence; they do not certify the fix.

## Frontend logs are ON DISK in dev — read them before reaching for a browser

**The browser console is not the only sink.** A scaffolded frontend installs
`installDevLogging()` (from `@reliantlabs/forge-web-runtime`) in dev, which
mirrors every `console.log/info/warn/error/debug` — plus every **uncaught
error and unhandled promise rejection** — to the dev server, which prints them
to its stdout. `forge env up` already tees that to a file:

```
.forge/logs/<env>/frontend_<name>.log     # ← browser lines land here, prefixed [browser:<level>]
.forge/logs/<env>/<service>.log           # ← backend services
```

So for a frontend bug, **grep the log first** — it costs one command and needs
no browser:

```bash
grep '\[browser:' .forge/logs/dev/frontend_web.log | tail -40
grep '\[browser:error\]' .forge/logs/dev/*.log          # errors across every frontend
tail -f .forge/logs/dev/frontend_web.log                 # follow while reproducing
```

The uncaught-error capture is the part worth internalizing: a `TypeError` that
crashes a render, or a rejected promise, produces **no `console` call at all**
and is invisible to a code search. It still lands in this file, with its stack.

Three caveats that will otherwise mislead you:

- **Dev only.** The receiving endpoint is a `apply: "serve"` Vite plugin (or a
  Next.js route that 404s in production), and `installDevLogging` no-ops
  outside dev. An empty log in a deployed environment is expected, not a bug.
- **A project may have opted out.** It is ordinary scaffolded code the project
  owns — one call in `src/main.tsx` / `src/app/providers.tsx` and one plugin
  entry. If the file has no `[browser:` lines at all, check those two places
  before concluding the code never ran.
- **Lines arrive in batches, up to ~250ms late.** Forwarding coalesces console
  calls into one POST per quarter-second (errors flush immediately, and the
  buffer is beaconed on page unload), so `tail -f` lags slightly behind the
  browser. That is the fix for one-request-per-line saturating the browser's
  six-connections-per-origin pool and slowing the app's own RPCs.

### If the log shows empty `[browser:...]` lines, the endpoint is out of date

The endpoint is a **scaffold-once file the project owns**, so `forge generate`
will not update it — and a `@reliantlabs/forge-web-runtime` upgraded through npm
can outrun it. A pre-batch endpoint parses `{"entries":[…]}` as a single line,
finds no `msg`, and prints one empty line for the whole batch.

The client detects this (a 2xx with no `X-Forge-Devlog` response header), falls
back to one request per line so nothing is lost, and says so once:

```
[browser:warn] [forge-devlog] this dev server's /__forge/log endpoint predates batched posts; update it
```

To fix it, make the endpoint understand both shapes — a `{"entries":[…]}` batch
and a bare `{"level","msg"}` — print one `[browser:<level>] <msg>` line per
entry in a single `console.log`, and answer `X-Forge-Devlog: 2`. The current
version is what `forge scaffold frontend` writes into a new project:
`src/lib/devlog-plugin.ts` (Vite) or `src/app/%5F_forge/log/route.ts`
(Next.js). Copy it from a freshly scaffolded frontend.

Related failure modes that produce the same silence: a malformed or oversized
post now prints `[browser:warn] [forge-devlog] dropped …` rather than being
swallowed, and a client that could not deliver a batch prepends
`[forge-devlog] dropped N lines (<reason>)` to the next one. If you see none of
those and no `[browser:` lines at all, the forwarder is not installed.

Use chrome-devtools MCP tools (snapshots, console, network) when you need to
DRIVE the UI or inspect live DOM/network state — the log file tells you what
already happened; devtools tells you what is happening now.

## Sub-Skills (forge)

The three investigation tracks above each have a dedicated forge sub-skill with concrete patterns:

- `reproduce` — runtime evidence collection and e2e reproduction.
- `isolate` — top-down bisection from e2e to unit test.
- `investigate` — hypothesis formation and code tracing.
- `multi-cluster-e2e` — a workload stuck `Pending` / not-ready, or a flow that fails only in a k3d multi-cluster e2e env: read the pod status before the network, hold the pod up on failure, and the nested-cluster image-pull / host-gateway-DNS / path-MTU gotchas.

For local ClickStack endpoints and live pprof, load `forge skill load debug/telemetry`.
Continuous profiles: `forge skill load observability/profiles`.

<!-- @forge-only:end -->
