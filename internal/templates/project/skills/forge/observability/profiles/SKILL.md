---
name: observability/profiles
description: Continuous profiles — what pkg/observe captures and pushes over OTLP, when profiling is on (local ClickStack by default, signals.profiles elsewhere), the otel-profiles collector and its ClickHouse tables, exporting flamegraphs with forge obs profile, and profile-guided builds (forge pgo pull writes cmd/<bin>/default.pgo from production CPU profiles).
---

# Continuous profiles

Every process with an OTLP endpoint profiles itself, in-process (`pkg/observe`, started by `observe.Setup`, which `serverkit.Run` calls). Every 5 minutes, jittered, it captures a **5s CPU profile**, and every two hours a **live-heap snapshot** (`inuse_space`/`inuse_objects`); it converts them to OTLP profiles and pushes them. Allocation counters, goroutine, mutex and block profiles are opt-in (see "Choosing what is captured"). The collector never scrapes anything; this is the same push path as traces, so it also works from a locked-down pod with no inbound network.

**On by default locally, governed by `signals.profiles` elsewhere.** The env's `forge.Observability` decides, in KCL, like every other signal:

| Env | `signals.profiles` unset | Rendered for host processes |
|---|---|---|
| local ClickStack (no destination, `profiles_port` set: the dev default) | **on** | `OTEL_EXPORTER_OTLP_PROFILES_ENDPOINT=http://127.0.0.1:<profiles_port>/v1development/profiles` |
| a destination, a cluster, `forge.OnHosted`, or no profiles collector | off | `OTEL_PROFILES_EXPORTER=none` |

A hosted env that `forge project new` scaffolds sets `profiles = True` on its bundle, so a new project's prod collects the CPU profiles profile-guided builds need (below); an older project's choice is left as it is. Profiles are telemetry like any other signal: their stored bytes count toward the env's telemetry allowance. An explicit value wins either way: `signals = forge.ObservabilitySignals {profiles = False}` turns local profiling off; `True` with a destination sends profiles to the forwarder or node agent at the base endpoint (both carry the `service.profilesSupport` gate), so the destination must accept OTLP profiles. `True` with the local ClickStack and no `profiles_port` is refused at render: ClickStack's own collector cannot store profiles. An endpoint, protocol or profiles endpoint you set yourself (KCL `env`, or exported in the shell) wins over the derived one, and a base endpoint you chose takes profiles along with it.

- **Cost.** Converting a full heap+goroutine cycle took ~90 ms CPU (about 0.15% of one core averaged); the default set converts less. Plus the runtime's own 100 Hz sampling during the 5s window.
- **Never in the way.** The loop runs on its own goroutine, recovers panics, probes the collector with one TCP dial before each cycle (an unreachable collector costs a dial, not a profile), backs off exponentially to 15 min, and skips the CPU profile if someone else (`/debug/pprof/profile`) is already running one.
- **Off for one process.** `OTEL_PROFILES_EXPORTER=none`, `FORGE_PROFILES=none`, or `observe.Config{ProfilesDisabled: true}`.
- **Trace links.** `TracingInterceptor` (and `observe.ProfileLabelsMiddleware` for plain `net/http` handlers) wrap each request in pprof labels naming its span; the profiler turns them into OTLP links, so the raw rows carry `TraceId`/`SpanId`. The CPU profiler samples at 100 Hz, so a span under ~10 ms usually has no samples: links are for slow spans.

## Choosing what is captured

| Type (`FORGE_PROFILES` name) | Sample types | Default | Cadence |
|---|---|---|---|
| `cpu` | `cpu` | on | 5s window every cycle (5 min) |
| `heap` | `inuse_space`, `inuse_objects` | on | every heap interval (2 h) |
| `alloc` | `alloc_space`, `alloc_objects` | off | with the heap snapshot |
| `goroutine` | `goroutine` | off | every cycle |
| `mutex` | `contentions`, `delay` | off | every cycle; sets `runtime.SetMutexProfileFraction(10)` while on |
| `block` | `contentions`, `delay` | off | every cycle; sets `runtime.SetBlockProfileRate(10000)` while on |

Why allocation counters are off: Go's `alloc_*` values are cumulative since process start, so every snapshot repeats every allocation site the process ever had and grows with uptime. In one production deployment they were 97.7% of stored profile rows, and one service's alloc profile grew from 11k to 39k rows per snapshot over 8 hours of uptime. When you opt in they are exported cumulative, never as deltas (`pkg/observe/PROFILES.md` says why).

Turn a type on, or the heap cadence up, for one workload in KCL, and turn it back off when you are done:

```kcl
wl.worker | {profiling = fw.Profiling {alloc = True, heap_interval = "1m"}}
```

`fw.Profiling` (`forge.Profiling`) defaults to the runtime's set, so name only what changes; `heap_interval` is whole minutes or hours, `1m` to `24h`. The CPU cadence is not in `profiling`; set `FORGE_PROFILES_CPU_INTERVAL` / `FORGE_PROFILES_CPU_DURATION` in the workload's `env`. It renders `FORGE_PROFILES` (the selected types, or `none`) and `FORGE_PROFILES_HEAP_INTERVAL`; a value the workload's own `env` sets wins. It never turns profiling on where the env has it off. In Go, `observe.Config{Profiles: observe.ProfilesConfig{Types: ..., HeapInterval: ...}}` sets a binary's own default; the two variables win over it, so the deploy-time override always works. An invalid variable is logged and ignored, never fatal.

## Where profiles are stored

Locally, the `otel-profiles` compose service (stock contrib collector **v0.162.0**, digest-pinned, `service.profilesSupport` gate on) receives them and writes to the ClickHouse inside `clickstack`. ClickStack's own collector is v0.155 and cannot write profiles. The producer's translator and pdata are pinned to the same v0.162.0: **the profiles wire format is pre-release, so bump all three together.** Tables and retention live in `deploy/observability/profiles-schema.sql` (yours; applied idempotently by the `profiles-schema` job on every `forge env up`):

| Table | Holds | TTL |
|---|---|---|
| `default.otel_profiles` | raw: one row per sample, with `TraceId`, `SpanId`, `ProfileId`, `StackHash` | 24h |
| `default.profile_stacks` | distinct stacks per service (`FunctionNames` leaf first) | 30d |
| `default.profile_samples_1m` | per-minute totals per `(service, env, type, stack)` | 30d |
| `default.profile_pgo_edges` | per-hour CPU call-site edge weights (caller, callee, call-site offset), the input to `forge pgo pull` | 30d |

**A heap is a snapshot, not a sum.** CPU time (and alloc, contentions, delay) accumulates, so a flamegraph over a range adds up its samples. The live heap (`inuse_*`) and `goroutine` are gauges: `forge obs profile`, the console's Profiles page and `GetFlamegraph` read each instance's (environment and `service.instance.id`) **newest snapshot in the range**, from the raw tier. Only when the range holds no snapshot at all do they fall back to the newest before it, looking back up to 24h (`profilequery.SnapshotLookback`); an instance with no snapshot in a range that has others is left out rather than shown stale. So the default 1h window still shows the heap when the 2h cadence put no snapshot inside it, and two snapshots of one heap are never counted twice. A heap older than 24h is not shown; shorten `heap_interval` if you need more frequent snapshots.

**HyperDX cannot draw flamegraphs**, so export and open them elsewhere:

```bash
forge obs profile --list                                   # which services sent profiles
forge obs profile --service api --since 30m -o api.pb.gz
go tool pprof -http=: api.pb.gz                                # flamegraph, top, peek
forge obs profile --service api --format speedscope -o api.json   # open at speedscope.app
forge obs profile --trace <trace-id>                       # CPU burned inside one trace (raw tier, last 24h)
forge obs profile --service api --type inuse_space --format folded | flamegraph.pl > heap.svg
forge obs profile --service api --sql                      # the query, to run yourself
```

Types: `cpu` (nanoseconds) and `inuse_space` (bytes) / `inuse_objects` by default; `alloc_space`/`alloc_objects`, `goroutine`, and the mutex/block `contentions`/`delay` only from a workload that opted in. The same aggregated-flamegraph SQL, to run in HyperDX's SQL console or `clickhouse-client`:

```sql
SELECT arrayStringConcat(arrayReverse(s.FunctionNames), ';') AS stack, sum(a.Value) AS value
FROM default.profile_samples_1m a
INNER JOIN (SELECT * FROM default.profile_stacks FINAL WHERE ServiceName = 'api') s USING (ServiceName, StackHash)
WHERE a.ServiceName = 'api' AND a.SampleType = 'cpu' AND a.Minute >= now() - INTERVAL 1 HOUR
GROUP BY stack ORDER BY value DESC
```

Stacks carry function names only (no line numbers): the dedup key is a hash of function and file. `pkg/profilequery` holds the SQL and the pprof/speedscope/folded writers for reuse by other tools.

## Profile-guided builds (PGO)

The CPU profiles above also feed Go's profile-guided optimization: the compiler inlines hot calls and devirtualizes hot interface calls it can see in a profile, typically a few percent of CPU for free. Forge turns the profiles your envs already collect into the file Go reads, with no profiling setup.

```bash
forge pgo pull --env prod        # merge prod's recent CPU profiles into cmd/<bin>/default.pgo, per Go binary
git add cmd/*/default.pgo && git commit -m "pgo: refresh profiles"
forge env build prod --release v1.4.0   # every build now uses it; no flag changes
forge pgo status                 # per binary: file, provenance (env, services, window), age, effective config
```

**You commit the file.** Go's default `-pgo=auto` reads `default.pgo` from the main package's directory, so the Dockerfile build, host builds and air all pick it up, and a build stays reproducible: it depends on the committed file, never on a live ClickStack or control plane. `forge env build` prints one line per Go binary (`pgo: cmd/app/default.pgo (prod, 6d old)`, `pgo: none (run forge pgo pull)` or `pgo: off`).

**One binary, one profile.** Go applies PGO per main package, so each `cmd/<bin>` gets its own file. A modular-monolith binary that runs as server, worker and cron gets one profile merging the CPU of every workload that runs it, weighted by CPU time, which is what the compiler should optimize. The workloads of a binary are read from the rendered env, not from naming; `services = [...]` overrides the list of OTEL service names.

**Flags.** `--env` is whose KCL to read (default prod when it exists); `--binary ./cmd/x` (repeatable) limits the binaries; `--since` and `--from-env` override the window and the source env; `--dry-run` shows what would be written; `--json` prints the result as a document. Profiles from a hosted env are fetched through the control plane (`ExportPGOProfile`, needs sign-in and `obs:read`) and are byte-for-byte what a local pull would write.

**Config, per build in KCL.** `GoBuild.pgo` (and, for an image holding several binaries, `DockerBuild.pgo` keyed by binary path) takes a `forge.PGO`:

```kcl
build = forge.GoBuild {cmd = "./cmd/app", pgo = forge.PGO {from_env = "prod", since = "168h", min_weight = "10s"}}
build = forge.DockerBuild {pgo = {"./cmd/proxy" = forge.PGO {enabled = False}}}
```

| Field | Default | Meaning |
|---|---|---|
| `enabled` | `True` | `False`: this env's builds pass `-pgo=off` for the binary (host build, Dockerfile, dev launches) and `pull` skips it |
| `from_env` | `""` | the env whose profiles `pull` merges; `""` is the env declaring the build |
| `since` | `"168h"` | the window, at most 30 days (the edge table's TTL) |
| `min_weight` | `"10s"` | minimum edge weight in the window (sampled CPU summed over call edges, Go's PGO total; not unique CPU time, since one sample counts once per distinct call edge among its leaf frames); below it `pull` leaves the file alone |
| `services` | `[]` | OTEL service names to merge; `[]` is every workload of `from_env` running this binary |

Two declarations for one binary in one env that disagree are a render error. A `-pgo=` in `GoBuild.flags` always wins over forge's.

**A ShellBuild's binaries.** forge cannot see what a shell command builds, so `pull` reads only the binaries `ShellBuild.pgo` names, keyed by package path relative to the build's `cwd`, each a `forge.ShellPGO`: the fields above plus `out` (where the profile goes, relative to the project root; default `<cwd>/<binary>/default.pgo`, which may be a sibling checkout's) and `namespace` (the `service.namespace` to read; default this project's). A deploy repo whose `cwd = "../reliant"` build runs three workloads gets one `../reliant/cmd/reliant/default.pgo` merging all three; commit it in that checkout. `enabled = False` only skips the binary, since forge does not edit the command. Load `deploy/build`.

**Opting out.** `enabled = False` turns PGO off for that binary. It also renders `FORGE_PROFILES_PGO=off` for its workloads, so `pkg/observe` stops attaching the PGO attributes to CPU samples (about +24% gzipped OTLP on a CPU profile, ~2% of stored row bytes; `pkg/observe/PROFILES.md`, "PGO start lines"). Deleting `default.pgo` alone does not stop the collection.

**Failing gracefully.** `pull` never breaks a build and never damages a file: no data yet, below `min_weight`, a ClickStack that is not running, not signed in, or a control plane older than the RPC each give a per-binary note naming the reason and the fix, and the existing `default.pgo` is left untouched. It exits 2 only when a requested source could not be read at all.

**Staleness.** Go tolerates a stale profile: edges naming functions that no longer exist are ignored, and shifted call sites just stop matching. Re-pull at each release, or weekly, and commit the result. A project whose hosted env builds a Go binary is scaffolded `.github/workflows/pgo-refresh.yml`, which does exactly that every Monday and opens a pull request when a profile changed; it reads the env with a `FORGE_OBS_TOKEN` secret (`deploy:read,obs:read`: the env lookup, then the profiles; no `deploy:write`) and ends green, with a notice, until one is set. An existing hosted project gets the file on its next `forge generate` (`ci.workflows: {pgo-refresh: false}` declines it; a deleted one stays deleted, and a project that already runs `forge pgo pull` in a workflow of its own, or has a Reliant workflow `.reliant/workflows/pgo-refresh.yaml`, is not given a second one), so PGO turns on by itself wherever the env has profiles; where it has none the job finds nothing and opens nothing. Edges come from the `profile_pgo_edges` table, which fills only from CPU samples carrying `forge.profile.start_lines`, so a project that predates that (its local `profiles-schema.sql`) needs the `migrations/v1.8.0/profile-guided-builds` playbook once, and the table has no history from before the view existed.
