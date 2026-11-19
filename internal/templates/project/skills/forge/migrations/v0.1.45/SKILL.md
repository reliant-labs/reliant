---
name: v0.1.45
description: Upgrade a project to forge v0.1.45. One section per change — (1) the typed `forge.Observability` declaration replaces the bare `_observability` toggle; (2) continuous profiles, on by default beside the local ClickStack (`profiles_port`, the `otel-profiles` collector); (3) `gen/go.mod` requiring the retired `github.com/reliant-labs/forge/pkg` module, which `forge generate` now rewrites (the root `go.mod` is yours to fix); (4) the gitignored `.npmrc` (`legacy-peer-deps`) that stops `npm install` crashing in a `--link-forge` project; (5) `serve.go` opens the otelconnect span OUTSIDE auth (`chainDeps.Trace`), so rejected calls have spans and request logs carry `trace_id`; (6) lean CI over a local gate — `task ci` / `task ci:checks`, a consolidated `.github/workflows/ci.yml`; (7) `forge.ControlPlaneSecret`, a Secret the control plane delivers and renews, replacing a `secret_optional` env var or a hand-minted Secret for a control-plane OTLP token; (8) `Bundle.applier = "forge"` for a self-applied platform env, and the deleted Flux pointer path; (10) `forge.AlertRule` alert rules replacing the pending alerts in the dashboard spec. Use when deploy/kcl/<env>/main.k has `_observability` but no `observability =` on its forge.Bundle or no `profiles_port`, when `grep forge/pkg gen/go.mod go.mod` matches or `forge env up` reports "go module graph is stale" on every start, when a bridged frontend's `npm install` dies with "Cannot read properties of null (reading 'edgesOut')", or when `grep -n 'Extras = append(\[\]connect.Interceptor{otelInt}' cmd/*/cmd/serve.go` matches, or when Taskfile.yml has no `ci:checks` task or `.github/workflows/ci.yml` runs one job per check instead of calling `task ci:checks`, or when `deploy/kcl/<env>/main.k` mounts an OTLP token Secret a control plane writes after the deploy (`secret_optional = True` on a `SecretRef`, or a `required_secrets` entry for a Secret the control plane mints), or when an env declares `forge.ControlPlane` over bound clusters it applies itself and sets no `applier`, or when `cmd/<bin>/cmd/metrics.go` is missing, or when `deploy/observability/dashboard-spec_gen.json` still has an `alerts` array.
version: v0.1.45
detection: grep -qs 'Extras = append(\[\]connect.Interceptor{otelInt}' cmd/*/cmd/serve.go || (grep -qs '^_observability = ' deploy/kcl/*/main.k && ! grep -qs '^    observability = ' deploy/kcl/*/main.k) || (grep -qs '^_observability = ' deploy/kcl/dev/main.k && ! grep -qs 'profiles_port' deploy/kcl/dev/main.k) || grep -qsE '^[[:space:]]*(require[[:space:]]+)?github\.com/reliant-labs/forge/pkg[[:space:]]+v' go.mod gen/go.mod || (test -f Taskfile.yml && ! grep -qs '^  ci:checks:' Taskfile.yml) || (test -f .github/workflows/ci.yml && ! grep -qs 'task ci:checks' .github/workflows/ci.yml) || (ls cmd/*/cmd/root.go >/dev/null 2>&1 && ! ls cmd/*/cmd/metrics.go >/dev/null 2>&1)
---

# Upgrading to v0.1.45

Each section below is independent: apply the ones whose "Use when" matches.

## 1. Typed observability destination

Use when `deploy/kcl/<env>/main.k` has `_observability` but no `observability =` on its `forge.Bundle`.

Where an environment's telemetry goes is now **declared** on the Bundle as `forge.Observability` (see `observability/destination`). `forge env up` and the cluster render read it; the ad-hoc endpoint injection that used to live in the up layer is gone.

### What changed

- `_observability = True/False` still works as the local switch, but it now feeds a typed declaration instead of being read by `forge env up` directly. A scaffolded `deploy/kcl/dev/main.k` ends its Bundle with `observability = _observability_decl`.
- `forge env up` exports `OTEL_EXPORTER_OTLP_ENDPOINT`/`_PROTOCOL` to host processes **from `output.observability`**, so `forge env render dev` shows the pair on each workload.
- `forge.OTEL_ENV`'s endpoint was `http://otel-collector.observability:4317`, which nothing deployed. It now points at the node agent Service (`otel-agent.forge-observability`), and no longer sets a sampler (sampling is `Observability.sampling`).
- New: a destination other than local ClickStack, with a node-agent DaemonSet on cluster runtimes.

### Steps

1. In `deploy/kcl/dev/main.k`, replace the `_observability` block with the one forge scaffolds (copy it from a fresh `forge project new`), or add, below your port declarations:

   ```python
   _observability_decl = forge.Observability {
       local_port = _otlp_http_port
   } if _observability else None
   ```

   and add `observability = _observability_decl` to the `forge.Bundle`.

2. Staging/prod: leave `observability` unset (telemetry off, as before) or declare a destination. A workload that set `OTEL_EXPORTER_OTLP_*` by hand keeps working: an author's value wins.
3. Delete any `env` entry that pointed a workload at `otel-collector.observability:4317`.
4. `forge env render dev` — each host workload now carries the endpoint/protocol pair.

### Verify

`forge env render dev | grep OTEL_EXPORTER_OTLP` shows `http://127.0.0.1:<port>` and `http/protobuf` on every host workload. `forge env up dev` and `forge doctor` still show traces, metrics and logs arriving.

## 2. Continuous profiles beside the local ClickStack

Use when `deploy/kcl/dev/main.k` declares `_observability` but no `profiles_port`.

### What changed

- `pkg/observe` profiles every process that has an OTLP endpoint (10s CPU + heap + goroutine, about once a minute) and pushes OTLP profiles; `OTEL_PROFILES_EXPORTER=none` turns one process off.
- They go to a second collector, `otel-profiles` (contrib 0.162.0), which writes into ClickStack's ClickHouse. `profiles-schema` applies `deploy/observability/profiles-schema.sql` on every up.
- `forge.Observability` gained `profiles_port`, and `signals.profiles` is now unset by default: **on** with the local ClickStack (no destination and a `profiles_port`), off elsewhere, where the render sets `OTEL_PROFILES_EXPORTER=none`. An explicit `True`/`False` wins. The declaration, not `forge env up`, derives `OTEL_EXPORTER_OTLP_PROFILES_ENDPOINT` for host processes.

### Steps

1. `forge generate` writes `deploy/observability/otel-profiles.yaml` and `deploy/observability/profiles-schema.sql` (both yours).
2. `docker-compose.yml`: copy the `profiles-schema` and `otel-profiles` services from a fresh `forge project new` (they replace the `SEAM: profiles` comment), and add `--feature-gates=+service.profilesSupport` to `otel-forwarder`'s `command` if you have one.
3. `deploy/kcl/dev/main.k`, below `_hyperdx_port`:

   ```python
   _profiles_otlp_port = plugin.resolve_port("<project>-dev-otlp-profiles", 4328) if _observability and not _destination else 0
   ```

   add `profiles_port = _profiles_otlp_port` to `_observability_decl`, and append to the ClickStack branch of `_telemetry_workloads`:

   ```python
   fw.Workload {
       name = "otel-profiles"
       runtime = forge.OnCompose {
           service = "otel-profiles"
           env = {"CLICKSTACK_PROFILES_OTLP_PORT" = str(_profiles_otlp_port)}
       }
   }
   ```

### Verify

`forge env render dev | grep PROFILES` shows `OTEL_EXPORTER_OTLP_PROFILES_ENDPOINT=http://127.0.0.1:<port>/v1development/profiles` on each host workload. After `forge env up dev` and a minute of traffic, `forge obs profile --list` names your services.

## 3. Retired `forge/pkg` module in `gen/go.mod`

Use when `grep forge/pkg gen/go.mod go.mod` matches, or `forge env up` reports "go module graph is stale" on every start.

### What happened

forge v0.1.44 collapsed `github.com/reliant-labs/forge/pkg` into the one module
`github.com/reliant-labs/forge`. Import paths did not change
(`github.com/reliant-labs/forge/pkg/orm` is still that path); only the module
that provides them did.

A project scaffolded before this release could end up with this in `gen/go.mod`:

```
require github.com/reliant-labs/forge/pkg v0.1.15
```

The scaffold wrote the correct `require github.com/reliant-labs/forge vX.Y.Z`, but its own `go mod tidy` ran while `gen/` had no Go files and deleted it as unused. The first real tidy then found `forge/pkg/forgepb` imported with no forge requirement and resolved it to the retired `forge/pkg` module. Symptoms: `forge env up` says the module graph is stale on every start, and a root `go.mod` that also requires `forge` makes every `forge/pkg/*` import "ambiguous".

### What forge does now

- `forge project new` no longer tidies a `gen/` that holds no Go files, so the
  template's forge require survives.
- `forge generate` re-asserts `gen/go.mod`'s forge requirement before codegen:
  it drops `github.com/reliant-labs/forge/pkg` and adds
  `github.com/reliant-labs/forge` at the root `go.mod`'s pin. Nothing for you to
  do in `gen/`: run `forge generate`, and it prints
  `gen/go.mod now requires github.com/reliant-labs/forge`.

### Steps

1. Run `forge generate`. `gen/go.mod` is migrated.
2. Check the ROOT `go.mod`. `forge generate` refuses (it will not edit your
   `go.mod`) while it still requires the retired module:

   ```bash
   grep -n 'reliant-labs/forge' go.mod
   # if it lists forge/pkg:
   go mod edit -droprequire=github.com/reliant-labs/forge/pkg
   go get github.com/reliant-labs/forge@$(grep -m1 '^forge_version' forge.yaml | awk '{print $2}')
   go mod tidy
   ```

3. Commit `gen/go.mod` and `gen/go.sum` if your project tracks them.

### Verify

```bash
grep -c 'forge/pkg ' go.mod gen/go.mod      # 0 and 0
(cd gen && GOWORK=off go mod tidy -diff)    # exits 0
```

## 4. `npm install` crash in a `--link-forge` project

Use when a bridged project's frontend `npm install` dies with "Cannot read properties of null (reading 'edgesOut')". Nothing to run: `forge generate` handles it.

A project bridged to a forge checkout (`forge project new --link-forge`, or a
`go.work` that `use`s one) also gets a gitignored npm workspace root that links
`web-runtime` into the install. That brings `web-runtime`'s `vitest ^4.1`
devDependency into the tree, and npm 10.x (what Node 22 ships) and 11.0.0 crash
resolving any tree holding vitest >= 4.1:

```
npm error Cannot read properties of null (reading 'edgesOut')
```

Unbridged projects are unaffected (scaffolded frontends pin vitest `^3`).

`forge generate` now writes a gitignored `/.npmrc` with `legacy-peer-deps=true` next to the bridge's root `package.json`. Nothing to do. If the project has its own `.npmrc`, forge leaves it alone and warns: add `legacy-peer-deps=true` to it, or use a newer npm.

## 5. Tracing outside auth: `serve.go` sets `chainDeps.Trace`

Use when `grep -n 'Extras = append(\[\]connect.Interceptor{otelInt}' cmd/*/cmd/serve.go` matches. `serve.go` is scaffold-once, so this edit is yours; forge will not make it.

### What was wrong

The scaffolded `serve.go` PREPENDED the `otelconnect` interceptor to `chainDeps.Extras`, and its comment said that made the span "cover every interceptor after it, including auth rejections". It did not: `observe.Chain` appends `Extras` **after** auth, so the span sat inside auth.

- A call auth rejected (a 401: missing or bad token) produced **no span**.
- The `rpc failed` line and the audit event for it were written outside any span, so they carried **no `trace_id`**, and the failure could not be found from a trace.
- Profile samples taken during the request had no span to link to.

### What changed

`observe.Deps` has a new `Trace connect.Interceptor` field. `observe.Chain` puts it OUTERMOST (trace, then pprof labels naming the span, then recovery, request-id, logging, tracing, metrics, auth, audit, rate-limit, extras). Inside it, every log line written for the request has the span in its context.

### Steps

In `cmd/<bin>/cmd/serve.go`, find:

```go
if otelInt, oerr := otelconnect.NewInterceptor(otelconnect.WithTrustRemote()); oerr == nil {
    chainDeps.Extras = append([]connect.Interceptor{otelInt}, chainDeps.Extras...)
} else {
```

and replace the one assignment:

```go
    chainDeps.Trace = otelInt
```

Keep `otelconnect.WithTrustRemote()`: it makes the browser's `traceparent` the PARENT of the server span (without it the two are linked, not joined). Update the comment above the `observe.Deps` literal if you copied the old "recovery, request-id, logging, tracing, metrics, then application interceptors" text. If you also build your own chain with `connect.WithInterceptors(...)` instead of `observe.Chain`, put the otelconnect interceptor FIRST.

If you set `Deps.Tracer` (forge's own `TracingInterceptor`) as well as `Deps.Trace`, you get two spans per RPC; use one.

### Frontends

Nothing to edit; bump `@reliantlabs/forge-web-runtime` when the next release is published (it waits up to 2s for the HyperDX SDK, so early RPCs carry a real `traceparent`).

### Verify

```bash
curl -si -X POST localhost:<port>/<pkg>.<Service>/<Method> -H 'content-type: application/json' -d '{}' \
  -H 'traceparent: 00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01' | head -1   # 401
```

Then, in ClickHouse or the HyperDX search: a span for that call with `TraceId = 0af7651916cd43dd8448eb211c80319c`, status error or unset, and an `otel_logs` row `rpc failed` with the same `TraceId`.

Also covers `forge env up`: a scaffold with no commits tags images `uncommitted`, and a missing `air` is installed at forge's pinned version (or the API runs under `go-run`). Nothing to change.

## 6. Lean CI over a local gate

Use when `Taskfile.yml` has no `ci:checks` task, or `.github/workflows/ci.yml` has one job per check (`lint`, `build`, `verify-generated`, `vuln-scan`, `deployability`, `license-check`) instead of one `checks` job that runs `task ci:checks`.

### What changed

`ci.yml` is scaffold-once (yours), so `forge generate` never rewrites it and your project keeps the old nine-job shape until you adopt the new one. Worth adopting: GitHub bills each job rounded up to the whole minute, and every job repeats checkout, toolchain setup, cache restore and the forge install; for the one project generated from the old layout that was roughly half its Actions minutes.

- **A local gate: `forge ci gate`**, behind two new Taskfile tasks. `task ci` runs everything CI runs (`ci:checks`, then `test`, then `test:integration`) in one summary table; `task ci:checks` is the static and build lane. `task ci` before you push gives CI's verdict.
- **The workflow became a thin wrapper.** Jobs are `changes` (skips heavy jobs for docs-only changes), `checks` (one setup, then `task ci:checks`), `test` (`task test`), `integration-test` (`task test:integration`, PRs only, only when the repo has `//go:build integration` files), plus `docker-build` and `e2e` where you already had them. `lint`, `build`, `verify-generated`, `deployability`, `vuln-scan` and `license-check` are no longer jobs: they are rows in the gate. Every job has `timeout-minutes`.
- **Gate semantics.** Every check runs even if an earlier one failed, then one table; non-zero exit if any failed. A check that could not run (missing tool, dirty tree) is `SKIPPED`, which fails the gate unless you name it: `CI_SKIP=vuln-go task ci`. Under Actions the table goes in the job summary.
- **Tools run at the versions CI pins**, built into forge's tool cache on first use; a copy on PATH at another version is not used.
- **Your own checks** go under `ci.gate.extra_checks` in forge.yaml (name, command, optional `dir` and `requires`) instead of a new job or a script copied into the repo. They run in `checks`, so `task ci` runs them too.

### Steps

1. **Taskfile.yml.** `forge generate` does not touch it, so add the two tasks (copy them from a fresh `forge project new`, or paste these):

   ```yaml
   ci:
     desc: "The whole PR gate locally (what CI runs): ci:checks + test + test:integration, one summary"
     cmds:
       - forge ci gate all
   ci:checks:
     desc: "Static + build lane of the CI gate (what the CI `checks` job runs)"
     cmds:
       - forge ci gate checks
   ```

   `test` and `test:integration` already exist; if `test:integration` does not, add it (`go test -race -tags=integration -count=1 -timeout=10m ./...`).

2. **ci.yml.** Set your own jobs and steps aside, then adopt forge's file: delete `.github/workflows/ci.yml` and run `forge project rescaffold .github/workflows/ci.yml` (or `forge project upgrade --check .github/workflows/ci.yml` to read the diff, then `--force`). Re-apply what was yours:

   - a step you added to `lint` / `build` / `vuln-scan` becomes an `extra_checks` entry (step 4), or a step in `checks` before `task ci:checks` if it needs the runner's environment;
   - a job with its own runner requirement (service container, another OS) stays a separate job;

3. **Branch protection.** The required checks were the old job names. Require `Checks`, `Test` and (if you have an integration lane) `Integration Tests` instead; `changes` and the skipped jobs report as satisfied.

4. **Project checks.** For each thing the old workflow did that forge's gate does not:

   ```yaml
   ci:
     gate:
       extra_checks:
         - name: gen-llm-gateway
           command: [go, run, ./cmd/gen-llm-gateway, -check]
           requires: [go]
   ```

   Names must be unique and may not reuse one of forge's (`forge ci gate --list`).

5. **Run it once.** `task ci` on a committed tree. `verify-generated` needs the committed tree (it attributes every change to `forge generate`), so a dirty tree reports it `SKIPPED` rather than a misleading pass.

### Verify

`forge ci gate --list` names every check and its lane. `task ci:checks` prints a summary table whose last line is `PASS` or `FAIL`. `.github/workflows/ci.yml` has jobs `changes`, `checks`, `test`, `integration-test` and each one has `timeout-minutes`; each calls exactly one `task`. `actionlint .github/workflows/ci.yml` is clean.

## 7. Secrets the control plane delivers: `forge.ControlPlaneSecret`

Use when a control-plane env still hand-provisions telemetry secrets or marks
them optional to bypass first-deploy checks. Replace those workarounds with
declared delivery: `forge skill load migrations/v0.1.45/control-plane-secrets`
for the required binding, migration steps and verification.

## 8. `Bundle.applier = "forge"`: a declared forge-applied env (transitional)

Use when an env declares `forge.ControlPlane`, binds its clusters to connected clusters, AND is still applied by `forge env deploy` rather than converged by the hub (control-plane's `prod`). A stage, not a destination: `Bundle.hub_converges` moves it one delivery unit at a time (`deploy/shape`).

### What changed

- New `Bundle.applier?: "forge"`: a **real** env forge applies for now. Exempt from the planned F-REFUSE-DIRECT, **not** recorded hub-converged (`hubConverged = false`), recorded as `shape.applier = "forge"`. Refused beside `lifecycle`. Detail: `deploy/shape`.
- **Deleted**: the dead in-cluster Flux pointer path (Flux install in `forge cluster up`, the OCIRepository/Kustomization `env deploy` wrote, Flux convergence in `env status`). `forge.flux_chart()` and declared bootstrap charts stay. See `deploy/flux`.

### Steps

Add `applier = "forge"` to the env's `forge.Bundle`, and remove `lifecycle` if both are set. Without it a fully bound env with nothing hosted is recorded `hubConverged = true` and the hub converges it as a second applier.

### Verify

`forge env shape <env> --json | jq .shape.applier` prints `"forge"`; `forge env deploy <env> --dry-run` prints no "declares neither" notice.

## 9. Declared metrics registry

Use when `<bin> metrics schema --json` is an unknown command, or `internal/metrics/metrics.go` does not exist: `forge generate` births both; detail in `migrations/v0.1.45/metrics-schema`.

## 10. Alert rules: `forge.AlertRule`, and the pending alerts are retired

Use when an env should alert, or when something reads the `alerts` array of `deploy/observability/dashboard-spec_gen.json` (the v0.1.44 "pending" definitions).

### What changed

- New `forge.Observability.alerts = forge.Alerts {rules, defaults, routes}`: the Prometheus rule shape with the condition inside a ClickHouse SQL `expr`. Depth: `observability/alerts`.
- `forge generate` writes `deploy/observability/alert-defaults_gen.json`; `forge env shape` resolves four default rules per env from it into `shape.alerts` (`source: default`), routed to in-app + Slack, never a pager.
- **Retired:** the `alerts` array of `dashboard-spec_gen.json` and `obsdash.Alert`. `error-rate` became default `error-rate` (now server errors over rpc metrics, with a request floor), `p99-latency` became `p95-latency`, `no-data` became `service-silent` (only for services with steady traffic).
- New optional `output.observability.alerts`; the shape gains optional `alerts` and `alert_routes`.

### Steps

Run `forge generate`. Opt a default out with `defaults = {"service-silent": forge.AlertDefault {enabled = False}}`. Read `shape.alerts` instead of the old array.

### Verify

`forge env shape <env> --json | jq '.shape.alerts[].alert'` lists the rules.

## 11. HelmChart prunes

See `migrations/v0.1.45/helm-chart-prune`.
