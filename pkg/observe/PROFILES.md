# Continuous profiles: what is captured, and what it costs

## Defaults

| Profile          | Sample types                   | Default | Cadence                                 |
| ---------------- | ------------------------------ | ------- | --------------------------------------- |
| CPU              | `cpu`                          | on      | 5s window every cycle (5 min, jittered) |
| Heap in-use      | `inuse_space`, `inuse_objects` | on      | every 2 h (`DefaultHeapInterval`)       |
| Heap allocations | `alloc_space`, `alloc_objects` | off     | with the heap snapshot, cumulative      |
| Goroutine        | `goroutine`                    | off     | every cycle                             |
| Mutex            | `contentions`, `delay`         | off     | every cycle; mutex fraction 10 while on |
| Block            | `contentions`, `delay`         | off     | every cycle; block rate 10µs while on   |

Before this release the defaults were a 10s CPU window every minute and a heap
snapshot every 15 minutes. A consumer gets the new defaults by pinning the
release that ships them; `FORGE_PROFILES_CPU_INTERVAL=1m`,
`FORGE_PROFILES_CPU_DURATION=10s` and `FORGE_PROFILES_HEAP_INTERVAL=15m` bring
the old cadence back for one workload.

## Why these numbers (measured, 2026-10-26)

Every stored profile **sample** is one ClickHouse row that repeats its whole
stack: function names, file names, addresses, line numbers and the resource
attributes. In tenant zero's `otel_profiles` a row is about 4.5 KB uncompressed
whichever sample type it belongs to, and the bill counts uncompressed bytes of
new parts. So storage is `profiles x stacks per profile`, and a profile's stack
count depends on how many distinct stacks the window caught, not on the clock.

Production, fixed UTC window 2026-10-25 20:00 → 2026-10-26 20:00,
12 services and 45 instance IDs including rollout replacements (40 IDs excluding
trader's older-pin staging service; `otel_profiles`). Full reproducible breakdown:
control-plane `research/profile-volume-2026-10-26.md` and its TSVs.

| Sample type              | Rows/profile | Share of bytes (excluding one allocation-heavy staging service) |
| ------------------------ | -----------: | --------------------------------------------------------------: |
| `cpu`                    |          ~22 |                                                             55% |
| `inuse_space`/`_objects` |          ~73 |                                                             45% |

CPU was 14.7k profiles a day (one per pod per ~70s) and heap 1.8k. Heap
snapshots carry about 3.4x the rows of a CPU profile, so a heap every 15 min
costs about as much as a CPU profile every 70s. Both have to come down.

The earlier fix (alloc/goroutine off) was already in the 2.5 GiB/day measured
here; the 150 GiB billed in October is the era before it: allocation counters
were 90-97% of aggregate rows from 15 to 21 October (`profile_samples_1m`), and
the `reliant-temporal-worker` service was 57% of Oct 15–21 aggregate rows (all
types; aggregates discard replica identity).

CPU window: the sampler runs at 100 Hz of **on-CPU time**, not wall time.
A 10s wall window does not imply 1,000 samples: the worker averaged 86.65 CPU
ticks/profile, the API server 30.69, and most other services 2–11. A Bernoulli
thinning model keeps stack `c` with probability `1-(1-f)^c` for window fraction
`f`; at `f=0.5` it predicts **50–52%** of stored rows survive. This is a model,
not a measured 5s capture, and bursty workloads can differ. Fewer samples lose
rare stacks and trace links; they do not preserve every hot path by guarantee.

| Cadence                          | Fleet bytes/day (model) |   vs now |
| -------------------------------- | ----------------------: | -------: |
| Now: cpu 10s/70s, heap 15m       |                2.52 GiB |       1x |
| cpu 10s/5m, heap 15m             |                1.45 GiB |     1.7x |
| cpu 10s/5m, heap 10m             |                2.02 GiB |    1.25x |
| cpu 5s/5m, heap 1h               |                0.47 GiB |     5.3x |
| **cpu 5s/5m, heap 2h (default)** |            **0.33 GiB** | **7.6x** |
| cpu 5s/10m, heap 2h              |                0.25 GiB |    10.1x |

Per-series model: each instance's expanded logical row bytes are scaled by the
ratio of expected profile counts (`1 + span/period`, including the first capture)
and CPU by a conservative 0.52 thinning factor. These are **not billed bytes or
compressed disk bytes**: LowCardinality storage, part merges and compression
change those totals. Repeating with `byteSize(*)` over the fixed window gives
2.56 → 0.336 GiB (7.6x), roughly half CPU and half heap. Snapshot cost and restart
frequency matter; physical savings must be checked after rollout. Heap exports
two sample-type profiles per capture (one per inuse type), not twice as many
captures. The old cumulative-allocation era is excluded from this forecast.

Why the chosen point (5s every 5 min, heap every 2 h, raw retention 24h):

- A 5 min cycle is 12 CPU profiles an hour per pod: any flamegraph over an hour
  or more still has ~60 s of samples, and the per-minute aggregate behind range
  flamegraphs is read over days. This is sparse coverage, not a continuous
  recording: a brief incident or a specific trace may have no samples at all.
  Raw trace-linked samples that were captured remain available for 24h.
- Heap changes slowly. A snapshot every two hours is 12 points a day per pod,
  enough to see sustained growth and leaks, but it can miss short-lived spikes.
  A new process includes heap on its first jittered cycle (roughly 1–5 minutes
  after startup), rather than waiting two hours.
- Raw retention sets the bytes held, not the bytes billed. Raw only serves
  `--trace` lookups and short-range reads; ranges read the 30-day aggregates.

How the comparable tools default: Datadog's Go profiler uploads every **60s**
with a CPU window of **60s** (`DefaultPeriod`, `DefaultDuration`) and heap on
by default; mutex and block are off ("we do not recommend enabling" block).
[Pyroscope Go](https://github.com/grafana/pyroscope-go/blob/main/session.go)
defaults to 15s uploads, CPU at 100 Hz and delta heap/block/mutex collectors.
[Grafana Alloy's `pyroscope.scrape`](https://grafana.com/docs/alloy/latest/reference/components/pyroscope/pyroscope.scrape/)
scrapes every **15s** with a 14s delta window; Alloy is Grafana's collector for
Pyroscope, not a third independent profiler. The OSS Pyroscope server's
`retention_period` defaults to 31d. These differ in representation and storage;
we have not measured a cross-vendor bytes/profile multiplier. Datadog's defaults
are in [dd-trace-go options.go](https://github.com/DataDog/dd-trace-go/blob/main/profiler/options.go).
For a temporary incident investigation, override the cadence or capture an
on-demand pprof instead of expecting sparse defaults to catch a short spike.

`ProfilesConfig` sets a binary's own selection; `FORGE_PROFILES` and
`FORGE_PROFILES_HEAP_INTERVAL` win over it. A forge workload's `profiling`
block (KCL, `forge.Profiling`) renders those two variables, so one service can
turn a type on, or the heap cadence up, for a while without a code change.
Invalid values are logged and ignored; they never stop the service. Mutex and
block sampling rates are set only while those profiles are selected, and put
back when the profiler stops.

The first cycle always takes a heap snapshot, so a new process is visible at
once. A failed heap delivery is retried on the next cycle rather than waiting
another interval.

## Why allocation counters are off

Go heap pprof has four useful types:

- `alloc_objects/count`, `alloc_space/bytes`: cumulative lifetime allocation
  counters. Old allocation sites remain present after their objects are freed.
- `inuse_objects/count`, `inuse_space/bytes`: current live-heap gauges (as of the
  runtime's sampled heap accounting), not allocation deltas.

Because the allocation counters are cumulative, every snapshot repeats every
allocation site the process has ever had, so a snapshot's size grows with
uptime. Measured in a production ClickHouse (2026-10-20, 48h window,
254.6 M raw rows, the previous default of every heap type every minute):

| Sample type                     | Share of rows | Share of uncompressed bytes (6h) |
| ------------------------------- | ------------: | -------------------------------: |
| `alloc_space` + `alloc_objects` |         97.7% |                            99.2% |
| `inuse_space` + `inuse_objects` |          1.5% |                             0.6% |
| `goroutine`                     |          0.6% |                             0.1% |
| `cpu`                           |          0.2% |                             0.1% |

One service's allocation profile grew from about 11,400 to 39,200 rows per
snapshot over its first 8 hours of uptime; its live-heap, goroutine and CPU
profiles stayed flat (75–93, 56–59 and under 110 rows). That one service's
allocation counters alone were 68.6% of all rows.

Applied to that window, the new defaults (CPU unchanged, the live heap at a
fifteenth of the rate, no allocation or goroutine profiles) keep about 0.69 M of
the 254.6 M rows: **−99.7% rows, −99.9% uncompressed bytes**.

When allocation counters are opted in they stay cumulative, at the heap
cadence. They are not converted to deltas:

- **Allocation deltas** would avoid repeating unchanged counters, but cannot
  silently reuse cumulative sample-type names or queries. The pinned OTLP
  translator/pdata has no temporality field; existing readers sum profile
  samples. A correct delta design needs explicit interval/representation, reset
  handling, delivery/loss semantics and matching query/storage changes. Applying
  subtraction to in-use gauges would instead produce signed heap changes, no
  longer a live-heap snapshot.
- **Top-N, or dropping unchanged snapshots** loses valid information or makes
  time-window queries depend on samples outside their window. Value-based
  deduplication across distinct capture times is also incorrect.

## How a snapshot is converted

The converter preserves every exported type's totals, units, period, timestamp
and trace links. It removes a sample only when **all values of that individual
OTLP sample type are zero**, after attaching links in the original pprof order.
Signed nonzero values remain valid. A dead live-heap stack can still have large
allocation counters; it must not be dropped from alloc profiles. CPU's redundant
`samples/count` type is dropped.

When the heap's unselected types are dropped (the default drops `alloc_*`), a
stack that is zero in every kept type is removed before conversion, and with it
the locations, functions and strings only it referenced. Without that, a
live-heap snapshot would still carry the dictionary of every allocation site
the process ever had.

Retrying the same captured payload leaves profile IDs/values unchanged. The
sender adds no retry loop or server-side deduplication guarantee.

## Measured fixture, not a production forecast

`TestHeapVolumeEvidence` simulates 60 one-minute heap snapshots with 1,000→1,590
historical allocation stacks, 24 frames each, ~1% live and an entirely zero
stack. Per-type totals and identity/link behavior are checked separately in
`TestHeapSparseSnapshotsPreserveSemantics`,
`TestHeapLiveOnlyKeepsTotalsAndShrinksTheDictionary` and
`TestProfileRetryKeepsIdentityAndRows`.

| One simulated hour of heap snapshots                             |    Rows | Uncompressed OTLP protobuf bytes |
| ---------------------------------------------------------------- | ------: | -------------------------------: |
| Legacy converter, every heap type every minute                   | 310,800 |                        9,067,104 |
| Previous default: sparse converter, every heap type every minute | 156,888 |                        7,847,028 |
| `alloc` opted in: every heap type every 15 min                   |   9,892 |                          493,954 |
| Default: live heap every 15 min                                  |     100 |                            5,895 |

The default keeps 0.06% of the previous default's heap rows and 0.08% of its
wire bytes. The fixture's live share (1%) is close to production's (the
`inuse_*` rows were 1.5% of all rows), so its row ratio and the measured
production ratio agree. These are not ClickHouse logical row widths, compressed
disk occupancy or NewPart accounting bytes; repeated stack/resource expansion
can make database savings differ.

Production's ~7% exact duplicate excess remains unexplained. An ambiguous
successful write followed by a retry can still create duplicates downstream.
The transport replay test proves identical payload/IDs, not exactly-once storage.
