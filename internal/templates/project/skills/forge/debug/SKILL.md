---
name: debug
description: Debug methodology — triage, parallel investigation, and synthesis.
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

**Ask the running system before reading code.** `forge obs status <env>` says what is wrong now (erroring services, new and regressed issues, alerts, stale telemetry, the release in force); `forge obs issue <id> <env>` gives the culprit, the latest event's trace and the suspect commits; `forge obs trace <trace-id> <env>` and `forge obs logs <env> --trace <trace-id>` give the request itself; `forge db query <env> "<select>"` gives the rows (`db/query`). Each prints one JSON document with `--json` whose `suggested[]` names the next command. Locally they read the ClickStack `forge env up` started, so they work in dev too. Depth: `observability/query`.

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

## Observability (ClickStack)

`forge env up dev` runs ClickStack (the `clickstack` compose service: ClickHouse, the HyperDX UI and an OTLP collector) with traces, metrics and logs. It is on by default and off with `_observability = False` in `deploy/kcl/dev/main.k`; see the `observability` skill.

- **HyperDX UI:** the URL `forge env up dev` / `forge env status dev` lists under **Compose services**. No login.
- **Traces:** HyperDX → Search → Traces. Find slow requests, trace cross-service calls.
- **Metrics:** HyperDX → Search → Metrics. Query `rpc.server.call.duration` (the otelconnect RPC edge histogram; attributes `rpc.method`, `rpc.response.status_code`) etc.
- **Logs:** HyperDX → Search → Logs. The collector tails `.forge/logs/<env>/*.log`, so a host process's logs are here and in the file; a log with a `TraceId` links to its trace.
- **Profiles:** not collected locally yet. Use pprof below.

Host processes are pointed at it by `forge env up`, which sets `OTEL_EXPORTER_OTLP_ENDPOINT` to the loopback OTLP/HTTP port the compose service publishes (`_otlp_http_port` in `dev/main.k`) and `OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf`, unless you set them yourself.

**pprof is always on.** It binds its own listener, separate from the app port, defaulting to `127.0.0.1:6060` — live in every environment and routable from none (no k8s Service, no route, no published port). You never have to redeploy to start profiling:

```bash
go tool pprof http://localhost:6060/debug/pprof/heap    # what is this process holding?
go tool pprof http://localhost:6060/debug/pprof/goroutine
curl -o cpu.pb.gz 'http://localhost:6060/debug/pprof/profile?seconds=30'

kubectl port-forward <pod> 6060:6060                    # in a cluster, then the above
```

Reach for the heap profile the moment a process is being OOMKilled: the cgroup's `memory.stat` tells you HOW MUCH it holds and never WHAT, and the answer only exists while the process is still alive.

Change the address with `PPROF_ADDR` / `--pprof-addr`; the scaffolded `docker-compose.yml` sets `0.0.0.0:6060` for the container's own network namespace (nothing publishes it). Pass `--pprof-addr=` to switch pprof off: an empty `PPROF_ADDR` counts as unset, like every empty config env var, so it keeps the default.

<!-- @forge-only:end -->
