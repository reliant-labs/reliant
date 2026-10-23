---
name: observability/profiles
description: Continuous profiles — what pkg/observe captures and pushes over OTLP, when profiling is on (local ClickStack by default, signals.profiles elsewhere), the otel-profiles collector and its ClickHouse tables, and exporting flamegraphs with forge obs profile.
---

# Continuous profiles

Every process with an OTLP endpoint profiles itself, in-process (`pkg/observe`, started by `observe.Setup`, which `serverkit.Run` calls). About once a minute, jittered, it captures a **10s CPU profile**, and every 15 minutes a **live-heap snapshot** (`inuse_space`/`inuse_objects`); it converts them to OTLP profiles and pushes them. Allocation counters, goroutine, mutex and block profiles are opt-in (see "Choosing what is captured"). The collector never scrapes anything; this is the same push path as traces, so it also works from a locked-down pod with no inbound network.

**On by default locally, governed by `signals.profiles` elsewhere.** The env's `forge.Observability` decides, in KCL, like every other signal:

| Env | `signals.profiles` unset | Rendered for host processes |
|---|---|---|
| local ClickStack (no destination, `profiles_port` set: the dev default) | **on** | `OTEL_EXPORTER_OTLP_PROFILES_ENDPOINT=http://127.0.0.1:<profiles_port>/v1development/profiles` |
| a destination, a cluster, `forge.OnHosted`, or no profiles collector | off | `OTEL_PROFILES_EXPORTER=none` |

An explicit value wins either way: `signals = forge.ObservabilitySignals {profiles = False}` turns local profiling off; `True` with a destination sends profiles to the forwarder or node agent at the base endpoint (both carry the `service.profilesSupport` gate), so the destination must accept OTLP profiles. `True` with the local ClickStack and no `profiles_port` is refused at render: ClickStack's own collector cannot store profiles. An endpoint, protocol or profiles endpoint you set yourself (KCL `env`, or exported in the shell) wins over the derived one, and a base endpoint you chose takes profiles along with it.

- **Cost.** Converting a full heap+goroutine cycle took ~90 ms CPU (about 0.15% of one core averaged); the default set converts less. Plus the runtime's own 100 Hz sampling during the 10s window.
- **Never in the way.** The loop runs on its own goroutine, recovers panics, probes the collector with one TCP dial before each cycle (an unreachable collector costs a dial, not a profile), backs off exponentially to 15 min, and skips the CPU profile if someone else (`/debug/pprof/profile`) is already running one.
- **Off for one process.** `OTEL_PROFILES_EXPORTER=none`, `FORGE_PROFILES=none`, or `observe.Config{ProfilesDisabled: true}`.
- **Trace links.** `TracingInterceptor` (and `observe.ProfileLabelsMiddleware` for plain `net/http` handlers) wrap each request in pprof labels naming its span; the profiler turns them into OTLP links, so the raw rows carry `TraceId`/`SpanId`. The CPU profiler samples at 100 Hz, so a span under ~10 ms usually has no samples: links are for slow spans.

## Choosing what is captured

| Type (`FORGE_PROFILES` name) | Sample types | Default | Cadence |
|---|---|---|---|
| `cpu` | `cpu` | on | 10s window every cycle (~1 min) |
| `heap` | `inuse_space`, `inuse_objects` | on | every heap interval (15 min) |
| `alloc` | `alloc_space`, `alloc_objects` | off | with the heap snapshot |
| `goroutine` | `goroutine` | off | every cycle |
| `mutex` | `contentions`, `delay` | off | every cycle; sets `runtime.SetMutexProfileFraction(10)` while on |
| `block` | `contentions`, `delay` | off | every cycle; sets `runtime.SetBlockProfileRate(10000)` while on |

Why allocation counters are off: Go's `alloc_*` values are cumulative since process start, so every snapshot repeats every allocation site the process ever had and grows with uptime. In one production deployment they were 97.7% of stored profile rows, and one service's alloc profile grew from 11k to 39k rows per snapshot over 8 hours of uptime. When you opt in they are exported cumulative, never as deltas (`pkg/observe/PROFILES.md` says why).

Turn a type on, or the heap cadence up, for one workload in KCL, and turn it back off when you are done:

```kcl
wl.worker | {profiling = fw.Profiling {alloc = True, heap_interval = "1m"}}
```

`fw.Profiling` (`forge.Profiling`) defaults to the runtime's set, so name only what changes; `heap_interval` is whole minutes or hours, `1m` to `24h`. It renders `FORGE_PROFILES` (the selected types, or `none`) and `FORGE_PROFILES_HEAP_INTERVAL`; a value the workload's own `env` sets wins. It never turns profiling on where the env has it off. In Go, `observe.Config{Profiles: observe.ProfilesConfig{Types: ..., HeapInterval: ...}}` sets a binary's own default; the two variables win over it, so the deploy-time override always works. An invalid variable is logged and ignored, never fatal.

## Where profiles are stored

Locally, the `otel-profiles` compose service (stock contrib collector **v0.162.0**, digest-pinned, `service.profilesSupport` gate on) receives them and writes to the ClickHouse inside `clickstack`. ClickStack's own collector is v0.155 and cannot write profiles. The producer's translator and pdata are pinned to the same v0.162.0: **the profiles wire format is pre-release, so bump all three together.** Tables and retention live in `deploy/observability/profiles-schema.sql` (yours; applied idempotently by the `profiles-schema` job on every `forge env up`):

| Table | Holds | TTL |
|---|---|---|
| `default.otel_profiles` | raw: one row per sample, with `TraceId`, `SpanId`, `ProfileId`, `StackHash` | 72h |
| `default.profile_stacks` | distinct stacks per service (`FunctionNames` leaf first) | 30d |
| `default.profile_samples_1m` | per-minute totals per `(service, env, type, stack)` | 30d |

**HyperDX cannot draw flamegraphs**, so export and open them elsewhere:

```bash
forge obs profile --list                                   # which services sent profiles
forge obs profile --service api --since 30m -o api.pb.gz
go tool pprof -http=: api.pb.gz                                # flamegraph, top, peek
forge obs profile --service api --format speedscope -o api.json   # open at speedscope.app
forge obs profile --trace <trace-id>                       # CPU burned inside one trace (raw tier, last 72h)
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
