# Continuous profiles: what is captured, and what it costs

## Defaults

| Profile          | Sample types                   | Default | Cadence                                 |
| ---------------- | ------------------------------ | ------- | --------------------------------------- |
| CPU              | `cpu`                          | on      | 10s window every cycle (~60s, jittered) |
| Heap in-use      | `inuse_space`, `inuse_objects` | on      | every 15 min (`DefaultHeapInterval`)    |
| Heap allocations | `alloc_space`, `alloc_objects` | off     | with the heap snapshot, cumulative      |
| Goroutine        | `goroutine`                    | off     | every cycle                             |
| Mutex            | `contentions`, `delay`         | off     | every cycle; mutex fraction 10 while on |
| Block            | `contentions`, `delay`         | off     | every cycle; block rate 10µs while on   |

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
