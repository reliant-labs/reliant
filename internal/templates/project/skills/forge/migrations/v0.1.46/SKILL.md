---
name: v0.1.46
description: Upgrade a project to forge v0.1.46. One section per change — (1) host-wide heavy slots: `forge slot run` / `forge slot status` (pkg/hostslot) queue heavy work machine-wide, `forge generate` and `forge lint` take a slot on their own, and the scaffolded Taskfile runs `test` and `test:integration` under a `SLOT` prefix; (2) `forge ci gate` runs checks concurrently (after / mutates_tree / heavy), caches PASS verdicts by tree hash, and unit-tests only the affected packages on a branch — with a wider ci.gate.extra_checks schema (lane, group, after, heavy, mutates_tree, cache, probe, skip_exit_code, env, replaces) for moving a hand-written gate script onto it. Use when Taskfile.yml has a `test` task but no `SLOT:` var, or when several agents or worktrees share one machine and builds and test runs slow each other down. (3) `forge.flux_chart()` is hardened and sized by default (behavior change: per-namespace controller RBAC replaces the chart's cluster-wide Secret read and impersonate-any-ServiceAccount grants, NetworkPolicies narrowed, concurrency and resources raised, a PriorityClass, Pod Security restricted on flux-system) and gains `rbac`, `placement`, `scrape_namespaces`. Use also when deploy/kcl declares `forge.flux_chart(`, or a chart override re-declares Flux's ClusterRoles, NetworkPolicies or controller args. (4) A bundle now seals the env's `observability_policy` (behavior change: the bundle digest of an env that opts out of, or into, any observability category changes on its next build). (5) The same-origin dev API proxy: `<basePath>/_api` on the dev server, so a Next.js or Vite frontend works when its dev server is opened through a Reliant workspace preview URL, a tunnel or a LAN IP. Use when deploy/kcl declares `forge.Observability` with `enabled = False`, a disabled signal or an opt-in, and a bundle of that env is already recorded, or when a frontend's `next.config.ts` / `vite.config.ts` has no `FORGE_DEV_API_PROXY`. (6) A web frontend's test toolchain moves together to vitest 5, vite 8 and `@vitejs/plugin-react` 6, with `engines.node >=22.12`. Use when a Next.js or Vite SPA frontend's package.json pins vitest ^3 or ^4 or `@vitejs/plugin-react` ^4 or ^5, or `tsc --noEmit` fails vitest.config.ts / vite.config.ts with TS2769 "Property 'rolldownVersion' is missing", or `npm audit` reports `tinypool`. (7) API keys: the owned APIKeyValidator seam is installed after OpenInfra on the shared DB pool; middleware reads X-API-Key. (8) Streaming RPCs end inside the shutdown budget: the owned serve.go adds `serverkit.StreamDrainInterceptor()` to the interceptor chain. Use when cmd/<bin>/cmd/serve.go builds `observe.Chain` without `StreamDrainInterceptor`. (9) lifecyclekit no longer re-exports operatorkit's `Run` / `Controller` / `Options`, so importing it never links controller-runtime. Use when internal/app/lifecycle.go calls `lifecyclekit.Run` or names `lifecyclekit.Controller` / `lifecyclekit.Options`.
version: v0.1.46
detection: ( test -f Taskfile.yml && grep -qs '^  test:' Taskfile.yml && ! grep -qs 'forge slot run' Taskfile.yml ) || ( grep -qsE 'forge\.flux_chart\(|flux_chart\(' deploy/kcl/*/*.k deploy/kcl/lib/*.k ) || grep -qsE 'forge\.Observability' deploy/kcl/*/*.k || ( for f in frontends/*/next.config.ts frontends/*/vite.config.ts; do test -f "$f" && ! grep -qs FORGE_DEV_API_PROXY "$f" && exit 0; done; exit 1 ) || grep -qsE '"vitest": "[~^]?[34]\.|"@vitejs/plugin-react": "[~^]?[45]\.' frontends/*/package.json || grep -qsF 'authDeps.Validate = authValidate' cmd/*/cmd/serve.go || grep -qsF 'func SetupAuth(cfg *config.Config) (func(token string)' internal/app/auth.go || grep -qsF idp-provision deploy/kcl/workloads.k deploy/kcl/dev/main.k || test -f deploy/kcl/dev/idp_identity_gen.k || ( grep -qsF 'observe.Chain(' cmd/*/cmd/serve.go && ! grep -qsF 'StreamDrainInterceptor' cmd/*/cmd/serve.go ) || grep -qsE 'lifecyclekit\.(Run|Controller|Options)[^A-Za-z0-9_]' internal/app/lifecycle.go
---

# Upgrading to v0.1.46

Each section below is independent: apply the ones whose "Use when" matches.

## 1. Host-wide heavy slots

Use when `Taskfile.yml` has a `test` task but no `SLOT:` var.

### What changed

On a shared machine the cost of a heavy job is set by how many others run
beside it. Measured on a 16 vCPU / 32 GiB workspace VM: `go build ./...` took
36–50 s beside 0–3 other heavy jobs and 101–155 s beside 8 or more, and a full
local gate went from ~40 min to ~2.3 h. The loss was memory-cgroup reclaim, so
past a handful of concurrent heavy jobs every one slows down. forge now queues
them:

- **`pkg/hostslot`**, a host-wide counting semaphore. A slot is an OS file lock
  on one of N files under the user cache dir; a crashed holder releases it.
  N defaults to min(CPUs / 4, memory GiB / 8), read from the cgroup v2 limit
  when there is one, at least 1. `FORGE_HEAVY_SLOTS=<n>` overrides it,
  `FORGE_HEAVY_SLOTS=off` switches it off, `FORGE_SLOTS_DIR` moves the files.
- **`forge slot run --class heavy -- <cmd>`** runs a command holding a slot,
  printing who holds the slots while it waits. **`forge slot status`** lists
  the count, why, the holders and the queue.
- **`forge generate` and `forge lint` take a slot themselves.** A `⏳ forge:
  waiting for a heavy slot` line is a queue, not a hang.
- **Re-entrant.** Anything already inside a slot (a gate check, a child of
  `forge slot run`) runs at once: the slot travels to children in
  `FORGE_SLOT_HELD`, and an ancestor holding one counts too.

### Steps

1. **Taskfile.yml** is yours, so `forge generate` does not touch it. Add the
   `SLOT` var beside `GOTESTRACE` (or copy it from a fresh `forge project new`):

   ```yaml
   vars:
     SLOT:
       sh: forge slot status --class heavy >/dev/null 2>&1 && echo 'forge slot run --class heavy --' || true
   ```

   and prefix the `go test` lines of `test` and `test:integration`, quoting the
   line because it now starts with `{{`:

   ```yaml
   - '{{.SLOT}} go test {{.GOTESTRACE}} -count=1 -timeout 120s {{if .CLI_ARGS}}{{.CLI_ARGS}}{{else}}./...{{end}}'
   ```

   In `test:frontend`, run each frontend's suite as
   `(cd "$dir" && {{.SLOT}} npm test -- --passWithNoTests)`.

   Keep `--class heavy` in the `SLOT` value: a forge older than v0.1.46 rejects
   the flag loudly, where a bare `forge slot run -- ...` would print its help
   and exit 0 without running the tests. The probe leaves `SLOT` empty when
   forge is absent or too old, so the tasks still run.

2. **Do not** put `ci` / `ci:checks` under `SLOT`: the gate takes slots itself,
   one per heavy check, and one slot around the whole run would let every
   check inside it skip the queue.

3. **Size it** where the machine is shared by more (or fewer) heavy jobs than
   its CPUs and memory suggest: set `FORGE_HEAVY_SLOTS` in the environment the
   agents and terminals inherit. A CI runner that runs one job per machine can
   leave the default (no other job contends) or set `off`.

### Verify

`forge slot status` prints the count and its source (e.g.
`heavy slots: 4 (min(16 CPUs / 4, 32 GiB / 8))`). `task test` while
`forge slot status` runs in another terminal lists `go test ...` as a holder.
`task --list` still works on a machine without forge.

## 2. A concurrent, cached, affected-aware `forge ci gate`

Use when a project runs `task ci` / `forge ci gate`, or keeps a gate script of
its own (`scripts/ci-gate.sh`) that `forge ci gate` could now replace.

### What changed

Nothing to migrate for the gate to work: the change is in forge. What a project
sees:

- **Checks run concurrently.** `verify-generated` (it rewrites the tree) runs
  alone before the checks that read the tree; frontend rows wait for
  `deps-<fe>`; heavy checks hold host slots (section 1); the rest run under
  `--jobs`. The table keeps plan order and adds the wall time and a `head:` line.
- **Per-check logs** in `tmp/ci/<lane>/<check>.log` (`.forge/ci/<lane>/` when
  git does not ignore `tmp/`; `CI_LOG_DIR` moves them). The console shows start
  and finish lines and a failure's tail instead of every check's full output.
- **Verdict cache.** On a committed tree a check that already passed on that
  tree reads `PASS (cached <sha>)`, in any worktree of the repository.
  `CI_NO_CACHE=1` runs everything; a dirty tree turns it off.
- **Affected unit tests.** On a branch, `unit-tests` runs `task test -- <pkgs>`
  for the packages the change reaches; anything but Go source changed, the
  default branch, or `CI_FULL=1` runs the whole suite. The line before the
  table says which.
- **`CI_ONLY`** (checks or groups, plus their `after` dependencies) and
  **`CI_REPORT`** (write the Markdown table to a file).
- `ci.gate` gained `env`, `path` and `affected_ignore`; each extra check gained
  `lane`, `group`, `after`, `heavy`, `mutates_tree`, `cache`, `probe`,
  `skip_exit_code`, `env` and `replaces` (`ci/gate` has the schema).

### Steps

1. **`.gitignore`**: keep `tmp/` ignored (the scaffold does), so the logs never
   dirty the tree the gate judges.
2. **`task test`** must accept Go package patterns after `--` (the scaffolded
   task does: `{{if .CLI_ARGS}}{{.CLI_ARGS}}{{else}}./...{{end}}`). A task that
   ignores them still passes, but runs the whole suite every time.
3. **Mark your extras.** For each `ci.gate.extra_checks` entry: `heavy: true`
   for a whole-module build, test run or render; `mutates_tree: true` if it
   rewrites tracked files (a formatter, pre-commit); `cache: false` if it reads
   the network or a sibling checkout; `after:` if it needs another check's
   output; `group:` if CI should select it with `CI_ONLY`.
4. **Retiring a gate script.** Each step becomes an extra: put the step's
   body in a script (`scripts/ci-step.sh <step>` is a fine shape), have it exit
   77 with a one-line reason when it cannot run (`skip_exit_code: 77`), move
   `PATH` / `GOPRIVATE` exports to `ci.gate.path` / `ci.gate.env`, a step that
   overrides one of forge's to `replaces: <the forge-owned check name>`, and image or cluster
   lanes to `lane: docker` / `lane: k3d`. Then point `task ci` and
   `task ci:checks` at `forge ci gate all` / `forge ci gate checks`.
5. **`ci.gate.affected_ignore`**: list files no Go test reads (`*.md`,
   `docs/**`) so a docs edit does not force the whole suite.

### Verify

`forge ci gate --list` shows each check's lane, group and scheduling. Run
`task ci:checks` twice on a committed tree: the second run's rows read
`PASS (cached <sha>)` (except `vuln-*` and `cache: false` extras). On a branch
that changes one package, `forge ci gate test` prints `unit tests: affected
mode — N of M test packages …`.

## 3. Hardened `forge.flux_chart()`

Use when an env declares `forge.flux_chart(...)`, or carries chart overrides that re-declare Flux's RBAC, NetworkPolicies or controller args by hand.

### What changed (BEHAVIOR CHANGE)

`forge.flux_chart()` with no arguments used to install the community chart with only `--no-remote-bases`, `--no-cross-namespace-refs` and four controllers off. It now installs a Flux hardened and sized for one controller set shared by several authors. Every default below is overridable (see Steps).

| Area | Before | Now (default) |
|---|---|---|
| Controller RBAC | `crd-controller` read every Secret/ConfigMap cluster-wide; `cluster-reconciler-impersonator` could impersonate ANY ServiceAccount; leader election was a cluster-wide ConfigMap write | `rbac = "namespaced"`: Flux CRs cluster-wide; leader election is a Role in `flux-system`; impersonation (pinned by `resourceNames` to `reliant-deploy-tenant`) and Secret reads exist only where a RoleBinding names `flux-tenant-reconciler` / `flux-tenant-source` |
| Multitenancy | off | on, `privileged = False` (no cluster-admin binding), `--default-service-account=reliant-deploy-tenant` |
| Config watcher | on | off (`DisableConfigWatchers`); a rotated kubeconfig is picked up at the next interval |
| Head-of-line | `--concurrent=4`, `--concurrent-ssa=4`, `--requeue-dependency=30s`, a stuck health check blocks a fix-forward | kustomize-controller `--concurrent=16 --concurrent-ssa=8 --requeue-dependency=10s`, `CancelHealthCheckOnNewRevision`; source-controller `--concurrent=8` |
| Resources | 100m/64Mi, no limits | kustomize 500m/512Mi (limits 2/2Gi); source 250m/256Mi (limits 1/1Gi) |
| Priority | none | PriorityClass `forge-delivery` (1000000), declared with the chart |
| NetworkPolicies | `allow-egress`, `allow-scraping` (:8080 from every namespace), `allow-webhooks` | `allow-egress` only (ingress from `flux-system` pods only); `allow-scraping` only for `scrape_namespaces`; no `allow-webhooks` |
| Namespace | no labels | Pod Security `restricted` (enforce/warn/audit) on `flux-system` |

### Steps

1. **Per-namespace RBAC needs a binding in every namespace that holds Flux objects** (OCIRepository, Kustomization, or the Secret they reference). Without it the controller can neither impersonate there nor read the Secret, and the source/apply fails `Forbidden` (fail-closed). Declare it with the namespace:

   ```python
   forge.flux_namespace_bindings("flux-acme")   # two RoleBindings, for forge.Manifests
   ```

   A Go writer binds `flux.ReconcilerRole` (kustomize-controller) and `flux.SourceRole` (source-controller) by name. The destination namespace needs nothing: the apply, health check and prune run as the impersonated identity.
2. **Cannot write bindings yet?** Step in: `forge.flux_chart(rbac = "pinned")` keeps the chart's cluster-wide reads but pins impersonation to one ServiceAccount. `rbac = "chart"` restores the chart's own RBAC and policies exactly (and `network_policies = "chart"`, `pod_security = ""`, `priority_class_name = ""`, `priority_class_create = False` undo the rest).
3. **Impersonating a different ServiceAccount:** `impersonate_service_account = "..."`. It changes the default, the pin and the role together.
4. **Remove your hand-written copies.** If the env re-declared the chart's `crd-controller`, `cluster-reconciler-impersonator`, the NetworkPolicies, `rbac.create = False`, `policies.create = False`, the controller `additionalArgs`, or a flux-system PSA label, delete them: they now duplicate (and, for `extraObjects`, collide with) the defaults. `values.extraObjects` is APPENDED to forge's, not substituted. `values` still merges over the posture, last wins, so a value you want different (`kustomizeController.resources...`) stays a one-line override. A list value (`additionalArgs`) replaces the default list; use the typed arguments (`concurrency`, `feature_gates`) instead.
5. **Metrics:** nothing outside `flux-system` reaches :8080 by default. Name the scraper: `scrape_namespaces = ["observability"]`.
6. **Chart objects the old render left behind** (the previous `allow-webhooks`, `cluster-reconciler-impersonator`) are removed by the chart prune (`migrations/v0.1.45/helm-chart-prune`) on the first apply, if forge stamped them; objects dropped before that stamp must be deleted by hand once.
7. **Placement** is opt-in (a default must run on every cluster): `placement = forge.flux_placement_delivery_v1()` for a dedicated tainted node pool, and `runtime_class_name = forge.FLUX_RUNTIME_CLASS_GVISOR` only on a target that has the RuntimeClass (a pod naming one the cluster lacks never schedules). Provision the pool yourself; forge has no node-pool primitive.

### Verify

`forge env render <env> --target flux` shows no `cluster-reconciler-impersonator` ClusterRole, a `flux-tenant-reconciler` ClusterRole whose impersonate rule carries `resourceNames`, one NetworkPolicy (`allow-egress`), and `kustomize-controller` args including `--feature-gates=CancelHealthCheckOnNewRevision=true,DisableConfigWatchers=true`. After deploy: `kubectl auth can-i impersonate serviceaccounts --as=system:serviceaccount:flux-system:kustomize-controller -n default` is `no`, and `kubectl auth can-i get secrets -A --as=system:serviceaccount:flux-system:kustomize-controller` is `no`.

## 4. A bundle seals the env's `observability_policy`

Use when an env declares `forge.Observability` with `enabled = False`, a signal switched off, or an opt-in (session replay), and a bundle of that env has been built before.

### What changed (BEHAVIOR CHANGE)

The release shape that `forge env build` records on the ledger has always carried `observability_policy`. The shape sealed inside the bundle (its config blob, and so its digest) did not: `forge env build` never handed the policy to `bundle.Build`, while `delivery_policy` was handed over. The bundle now seals what the shape declares.

The consequence is a one-time digest change. An env whose policy opts out of anything (or opts in to replay) builds a bundle with a different digest than the same render built by an older forge. Envs that declare no `forge.Observability`, or declare one that opts out of nothing, seal no policy in either version and are unchanged.

### Steps

1. Nothing to edit. Rebuild the env (`forge env build <env>`): the new bundle records under a new digest.
2. A release already bound to the old digest keeps resolving to it. Replacing the bundle of record for that same release takes `forge env deploy <env> --rerecord-bundle`; otherwise cut a new release.
3. Anything that stored an affected bundle digest outside the ledger (a pinned OCIRepository digest, a promotion record you compare by hand) needs the new value.

### Verify

`forge env build <env>` then read the config blob of the bundle: `observability_policy` is present when the env opts out of or in to anything, and equals the `observability_policy` of `forge env shape <env> --json`.

## 5. Same-origin dev API proxy

Use when a Next.js or Vite frontend's `next.config.ts` / `vite.config.ts` has no `FORGE_DEV_API_PROXY`, or the dev app works on localhost but every RPC and sign-in fails through a Reliant workspace preview URL, a tunnel or a LAN IP.

Under the dev server the browser now reaches the API at `<basePath>/_api` on its own origin, and the dev server forwards it to the API; builds still read `API_URL` from `config.js`. `next.config.ts` also sets `allowedDevOrigins` so a non-localhost page can hydrate. The files involved are scaffold-once, so adopting it is a hand merge and touches no generated file: see `migrations/v0.1.46/dev-api-proxy`.

## 6. One vite: vitest 5, vite 8, `@vitejs/plugin-react` 6

Use when a Next.js or Vite SPA frontend's `package.json` pins `vitest` ^3 or ^4 or `@vitejs/plugin-react` ^4 or ^5, when `tsc --noEmit` (or `next build`) fails `vitest.config.ts` / `vite.config.ts` with TS2769 "Property 'rolldownVersion' is missing", or when `npm audit` reports `tinypool`.

### What changed

The config file takes `defineConfig` from vitest's copy of vite and `react()` from the plugin's, so the two must be one copy. The Next.js scaffold left vite to vitest, and once something else in the tree resolved vite 8 (a dev build of forge links web-runtime, whose vitest ^4.1 does), the plugin bound vite 8 while vitest 3 kept vite 7. Both web kinds now scaffold the same three, by one major each, with vite declared directly:

| package | was | now |
|---|---|---|
| `vite` | (Next.js: not declared) / Vite SPA: `^6.0.0 \|\| ^7.0.0` | `^8.0.0` |
| `vitest` | `^3.1.0` | `^5.0.0` |
| `@vitejs/plugin-react` | `^4.4.0` | `^6.0.0` |
| `engines.node` | `>=22` | `>=22.12` |

vitest 4.1 is skipped on purpose: the npm that ships with Node 22 (10.x) crashes installing it ("Cannot read properties of null (reading 'edgesOut')"). vitest 5 also drops `tinypool`, which `npm audit` flagged critical. No test or config file changes: the scaffolded `vitest.config.ts`, `vite.config.ts` and tests run unchanged on vitest 5.

### Steps

1. In each Next.js or Vite SPA frontend's `package.json` (yours, so `forge generate` does not touch it), set the three `devDependencies` to the ranges above, adding `"vite": "^8.0.0"` to a Next.js frontend, and set `engines.node` to `>=22.12`. React Native frontends use jest and need nothing.
2. Run `npm install` where you install (the frontend, or the workspace root).
3. If your own config or tests use an API vitest 5 removed, the run in Verify names it; fix it there.

### Verify

`npm ls vite` lists one version. `npm run typecheck`, `npm test` and `npm run build` pass in each frontend, and `npm audit` no longer lists `tinypool` or `vitest`.

## 7. API keys, and a validator that sees the request

Use when `serve.go` assigns `authDeps.Validate = authValidate`, or `SetupAuth`
returns a token-only validator. Owned auth/middleware/serve files need edits;
upgrading untouched middleware alone can break that old assignment. Validators
returning `(nil, nil)` now fail closed. Load
`forge skill load migrations/v0.1.46/api-keys` for every edit and verification
step before upgrading this wiring.

## 8. Streaming RPCs end inside the shutdown budget

Use when `cmd/<bin>/cmd/serve.go` builds `observe.Chain` and never mentions `StreamDrainInterceptor`.

### What changed

`http.Server.Shutdown` waits for every active request, and a server-streaming
or bidi RPC never finishes on its own. So every rollout of a server with an
open stream waited out the whole `SHUTDOWN_TIMEOUT`, logged `server shutdown
error: context deadline exceeded` at ERROR from every replica, and exited
cutting the stream without a status. serverkit's listener now ends its
long-lived streams inside the budget (`forge skill load services/shutdown`).
Plain-HTTP routes opt in with `serverkit.LongLived(h)`; Connect streams are
bound by an interceptor, which has to be in the chain your serve.go owns.

### Steps

1. In `cmd/<bin>/cmd/serve.go`, before the protovalidate interceptor is
   appended to `chainDeps.Extras`, add:

   ```go
   chainDeps.Extras = append(chainDeps.Extras, serverkit.StreamDrainInterceptor())
   ```

   A drained stream then ends OK with the `x-forge-drained: true` trailer, and
   the client reopens it against a live replica.
2. A peer that should redial on Unavailable instead (a daemon on a bidi
   stream): list its procedure or service in `skCfg.RedialOnDrain` in
   `serverkitConfig`, e.g. `skCfg.RedialOnDrain = []string{daemonv1connect.DaemonServiceName}`.
3. A plain-HTTP stream (SSE, websocket, long poll) mounted on the mux: wrap its
   handler in `serverkit.LongLived(...)`.

### Verify

Open a stream against a running server and send it SIGTERM: the stream ends
(OK, trailer `x-forge-drained: true`) right after `PRE_STOP_DELAY`, and the log
has no `server shutdown error`.

## 9. The operator bridge moved out of lifecyclekit

Use when `internal/app/lifecycle.go` calls `lifecyclekit.Run` or names
`lifecyclekit.Controller` / `lifecyclekit.Options`: those three names now live
only in `forge/pkg/appkit/operatorkit`. `forge project upgrade` makes the
rename itself; on a project bumped some other way, run `forge fix operatorkit`
(`--dry-run` lists the files first). Details:
`forge skill load migrations/v0.1.46/operatorkit-bridge`.
