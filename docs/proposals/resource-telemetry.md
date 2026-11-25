# Proposal: process and machine resource telemetry

**Status:** proposal, not implemented. Written for owner review; nothing here
has landed in forge, reliant or control-plane.
**Date:** 2026-11-19
**Scope:** forge (`pkg/observe`, a new sampler package, KCL, `obsdash`,
`releasehealth`), with first-consumer tasks in reliant (the daemon, the gateway,
the web UI) and control-plane (alert rules).

## Summary

Owner request (2026-11-19): forge should report CPU and memory, of itself and
of the machine it runs on, and Reliant should use that telemetry. File
descriptors and similar are welcome, and all of it must be disable-able.

Today forge cannot do this. `observe.Setup` records Go runtime metrics (GC,
heap, goroutines) and only when OTLP metrics export is on. It records no CPU
time, no resident memory, no file descriptors, nothing about the host. The one
resource signal forge dashboards read is the Kubernetes node agent's
`kubeletstats`, so a workload that is not a pod (`forge env up dev`, `OnHost`,
a laptop daemon) has no resource telemetry at all, and a pod has no view of its
own file descriptors or its RSS-versus-heap gap. Reliant meanwhile built its own
linux-only samplers (`cgroupmem`, `machinehealth`, `diskwatch`, `forkwatch`)
because forge offered nothing, and control-plane alerts on a log line those
samplers print.

The recommended design:

1. **One forge-owned sampler package, `pkg/resmetrics`**, started from
   `observe.Setup` beside the Go runtime metrics. It owns small per-OS readers
   (linux `/proc` and cgroup v2 by hand; darwin and windows through gopsutil v4
   for basics only), and every reader fails independently, so one unreadable
   file loses one metric and not the sample.
2. **Two groups.** `process` is the binary itself: CPU time, RSS, virtual
   memory, file descriptors and their limit, threads, context switches, page
   faults, disk IO, uptime. `host` is the machine, or the container's cgroup
   when the binary runs in one: CPU, load, memory, swap, bounded filesystem
   usage, system fd and pid pressure, and cgroup v2 memory, OOM kills, PSI, CPU
   throttling and pids.
3. **OTel semantic-convention names** (`process.*`, `system.*`), with a
   `forge.*` namespace only where semconv has no name. Both groups are
   instruments on the same meter provider, so they reach the always-on
   Prometheus `/metrics` reader and the OTLP reader alike.
4. **A typed `Snapshot` API** (`Latest()` and `Subscribe`) beside the
   instruments, so an app can project the same numbers into its own protocol.
   Reliant's daemon has no OTLP endpoint; it will fill its heartbeat from the
   snapshot.
5. **On by default, disable-able per group.** `observe.Config.ResourceMetrics`
   and `FORGE_OBSERVE_RESOURCE_METRICS=auto|off|process|host|all`, read in
   `otelenv.go`, rendered from a per-workload KCL field. `OTEL_SDK_DISABLED=true`
   and `OTEL_METRICS_EXPORTER=none` keep their existing meaning.
6. **Reliant is the first consumer, in a compatible way.** `DaemonHeartbeat`
   gains one new message field; its existing fields stay populated, from the
   same snapshot. `cgroupmem` and `machinehealth` sample through the primitive,
   so there is one implementation.

Recommendation, numbered so the owner can accept or amend each:

1. Build `pkg/resmetrics` and start it from `observe.Setup` (section 3).
2. Default `auto`: process group on everywhere; host group on everywhere except
   that inside a Kubernetes pod it reports the container's cgroup, never the
   node's totals (section 4).
3. Feed the always-on `/metrics` reader as well as OTLP (section 4).
4. Emit self and host aggregates only, with no identifying attributes and no
   per-CPU, per-NIC or per-mount series (section 5).
5. Add a `forge.ResourceMetrics` KCL field on the workload, an obsdash
   process/host panel, and a saturation block in `forge obs status` (section 6).
6. Move Reliant's daemon onto the primitive and extend `DaemonHeartbeat` with a
   nested `ResourceReport` (section 7).
7. Make the `releasehealth` resource signal a later phase, proposed here and
   not committed (section 6).

---

## 1. What exists

**forge records Go runtime metrics, conditionally.** `observe.Setup` builds one
resource and one meter provider. The provider always has a Prometheus reader
that serves `/metrics` (`pkg/observe/setup.go:107-111`). Only when OTLP metrics
export is on does it add an OTLP `PeriodicReader` and call
`goruntime.Start` (`setup.go:153-165`; contrib runtime v0.72.0). With no
endpoint set, `Setup` returns early with the Prometheus reader alone
(`setup.go:126-129`), so `/metrics` carries no `go.*` series either. The
resource has `service.*` and whatever `OTEL_RESOURCE_ATTRIBUTES` carries; it has
no `process.*`, `host.*` or `os.*` attributes (`setup.go:205`).

**Nothing in forge reads CPU, RSS, fds or the host.** No file in `forge/pkg`
reads `/proc`. `prometheus/procfs` is an indirect dependency
(`go.mod:274`), `purego` a direct one (`go.mod:21`, for the KCL plugin).

**Saturation comes from Kubernetes only.** The node agent's `kubeletstats`
receiver scrapes every 30s (`pkg/deploy/observability.go:369-375`).
`internal/obsdash/workloads.go:12-17` reads `container.cpu.usage`,
`container.memory.working_set` and the `k8s.container.*_utilization` family;
`alerts.go:960-990` reads restarts and last-terminated reason. The Overview
dashboard's "saturation and restarts" tile (`obsbuiltin/overview.go:14`) is
therefore Kubernetes-only. `pkg/releasehealth` judges a release on spans,
ingress and browser signals (`signals.go:21-23`) and has no resource signal.

**Reliant's daemon built its own samplers.** `DaemonHeartbeat`
(`reliant/proto/reliant/v1/tools_daemon.proto:319`) carries cgroup memory
used/limit/pressure, `DiskReport`, `oom_kills` and `MachineHealth`; the
`ProcessReport` message (`:447`) adds a task count against `RLIMIT_NPROC` and
the top five process trees. The samplers are linux and cgroup-v2 shaped:
`internal/cgroupmem` reports zeros on macOS and for every local daemon, and
`machinehealth` reads only `/proc`. `machinehealth.go:3-14` explains why it logs
instead of exporting: "a managed daemon exports no metrics of its own: it has
no OTLP endpoint or token in its pod". The gateway stores the memory fields in
`daemon_attachment` (`tools_daemon_inbound.go:317`) and the UI shows them
(`MachineHealthBanner.tsx`, `OomKillBanner.tsx`).

**control-plane alerts grep a log line.** `MachineLoadHigh`, `MachineDiskLow`
and `MachineToolTimeouts` (`deploy/kcl/lib/selfmon_alerts.k:165-185`) are
queries over the daemon's `machine health` lines, which the node agent parses
into `otel_logs` (`otel_agent.k:35`). The attribute names are a contract between
the daemon and those rules.

**The gap**, in one list: the in-process view (RSS against Go heap, fds,
threads) of every binary; any resource signal for host-process workloads;
anything at all from a local or Electron daemon; and the cloud machine as a
host, which exists only as log lines.

---

## 2. Goals and non-goals

Goals: every forge-built binary reports its own CPU, memory and fd use and, on
request, its host's, with no code in the app; the same numbers are readable
in-process; the whole thing can be turned off per workload and per env; and
Reliant's daemon drops its parallel implementation.

Non-goals: per-process or per-container breakdowns of other processes (the
daemon's `ProcessTree` stays Reliant's, because it carries chat ids and
command lines); network metrics (`system.network.*` is a per-interface series
explosion and is not in scope); a replacement for `kubeletstats`; GPU, battery
or temperature; alert rules over any of this inside forge (dashboards and
`obs status` first).

---

## 3. The primitive

### Shape

`forge/pkg/resmetrics` has three parts. A **reader layer** is one small file per
OS (`proc_linux.go`, `cgroup_linux.go`, `gopsutil_darwin.go`,
`gopsutil_windows.go`, `stub_other.go`), each returning a plain struct and an
error per field group. A **Sampler** owns the readers, derives rates (CPU
utilization is a delta between two reads) and caches the last `Snapshot`. An
**OTel registration** (`Register(meter, sampler, groups)`) declares observable
instruments whose callbacks read the cached snapshot.

```go
type Snapshot struct {
    At       time.Time
    Process  *ProcessStats // nil when the group is off or unreadable
    Host     *HostStats
    Cgroup   *CgroupStats  // nil outside a cgroup-v2 container
    Errors   map[string]error // per reader, for logs and debugging
}

func Start(ctx context.Context, opts Options) (*Sampler, error)
func (s *Sampler) Latest() Snapshot
func (s *Sampler) Subscribe(every time.Duration) (<-chan Snapshot, func())
```

`observe.Setup` calls `resmetrics.Start` after it creates the meter provider, in
both branches (with and without OTLP), and registers the instruments on it. It
also returns the sampler for callers that want the snapshot; the cheapest
surface is a package-level `observe.Resources()` accessor, like
`SetAppVersion`, so `serverkit` and `appkit` need no signature change.

### Sampling

Observable instruments are read at collect time, which is the OTLP interval
(60s default) or a Prometheus scrape. The sampler refreshes lazily: a read older
than `FORGE_OBSERVE_RESOURCE_INTERVAL` (default 15s, floor 1s) re-samples, a
younger one is served from cache. A background ticker runs only while a
`Subscribe` is open, so a binary with no in-process consumer does no work
between collections. A sample is a handful of small reads, except the fd count
(a directory listing of `/proc/self/fd`, linear in open fds), which is why it is
interval-bound and not per-call.

### Metrics

Names follow the OpenTelemetry semantic conventions for `process.*` and
`system.*`. Those conventions are **development status, not stable**
([I], from the spec as of the research): names have been renamed before (the fd
count was `process.open_file_descriptor.count` and is now
`process.unix.file_descriptor.count`), and forge pins semconv v1.26.0
(`setup.go:20`), which predates several of them. The metric names are therefore
string literals in one file with a test that fails on drift
(`internal/obsdash/metric_names_test.go` is the precedent), not constants from
the semconv package. Where the spec has no name, the metric is `forge.*` and the
table says so.

**Group `process` (self).** One series set per binary instance.

| Metric                                                                           | Unit     | Instrument | Platforms              | Source                                                                                                                                           |
| -------------------------------------------------------------------------------- | -------- | ---------- | ---------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------ |
| `process.cpu.time` {`cpu.mode`=user,system}                                      | s        | counter    | linux, darwin, windows | `/proc/self/stat`; gopsutil elsewhere                                                                                                            |
| `process.cpu.utilization`                                                        | 1        | gauge      | all                    | delta of the above over wall time, in cores (can exceed 1)                                                                                       |
| `process.memory.usage` (RSS)                                                     | By       | updown     | all                    | `/proc/self/statm`; gopsutil                                                                                                                     |
| `process.memory.virtual`                                                         | By       | updown     | all                    | same                                                                                                                                             |
| `forge.process.memory.go_gap`                                                    | By       | gauge      | all                    | RSS minus the Go runtime's mapped memory (`runtime/metrics` `/memory/classes/total:bytes` minus released): the cgo, mmap and fragmentation share |
| `process.unix.file_descriptor.count`                                             | {fd}     | updown     | linux, darwin          | `/proc/self/fd` listing; darwin `/dev/fd` ([I], verify; omit if it needs `lsof`)                                                                 |
| `forge.process.file_descriptor.limit`                                            | {fd}     | gauge      | unix                   | `getrlimit(RLIMIT_NOFILE)`, soft                                                                                                                 |
| `forge.process.file_descriptor.utilization`                                      | 1        | gauge      | unix                   | count / limit                                                                                                                                    |
| `process.thread.count`                                                           | {thread} | updown     | all                    | `/proc/self/status` Threads; gopsutil                                                                                                            |
| `process.context_switches` {`process.context_switch_type`=voluntary,involuntary} | {switch} | counter    | linux, darwin          | `/proc/self/status`; gopsutil                                                                                                                    |
| `process.paging.faults` {`system.paging.fault.type`=major,minor}                 | {fault}  | counter    | linux, darwin          | `/proc/self/stat`                                                                                                                                |
| `process.disk.io` {`disk.io.direction`=read,write}                               | By       | counter    | linux, windows         | `/proc/self/io`; gopsutil                                                                                                                        |
| `process.uptime`                                                                 | s        | gauge      | all                    | process start time                                                                                                                               |

Windows has no fds; it reports `forge.process.handle.count` instead ([I]:
gopsutil v4 may not expose it, in which case it is a `GetProcessHandleCount`
call through `x/sys`). Goroutine count stays with the existing `go.goroutine.count`;
the `Snapshot` carries it from `runtime` so the heartbeat needs no OTel read.

**Group `host` (machine, or the container's cgroup).** Aggregates only.

| Metric                                                         | Unit      | Instrument    | Platforms                 | Source                                                         |
| -------------------------------------------------------------- | --------- | ------------- | ------------------------- | -------------------------------------------------------------- |
| `system.cpu.time` {`cpu.mode`=user,system,idle,iowait,steal}   | s         | counter       | all (iowait, steal linux) | `/proc/stat` aggregate line; gopsutil                          |
| `system.cpu.utilization` {`cpu.mode`}                          | 1         | gauge         | all                       | delta of the above                                             |
| `system.cpu.logical.count`                                     | {cpu}     | updown        | all                       | `runtime.NumCPU`; in a cgroup, the quota-effective count ([I]) |
| `system.cpu.load_average.{1m,5m,15m}`                          | 1         | gauge         | linux, darwin             | `/proc/loadavg`; `getloadavg`                                  |
| `system.memory.usage` {`system.memory.state`=used,free,cached} | By        | updown        | all                       | `/proc/meminfo`; gopsutil                                      |
| `system.memory.limit`                                          | By        | updown        | all                       | MemTotal, or cgroup `memory.max` when set                      |
| `system.linux.memory.available`                                | By        | updown        | linux                     | `MemAvailable`                                                 |
| `system.paging.usage` {`system.paging.state`=used,free}        | By        | updown        | all                       | `SwapTotal`/`SwapFree`                                         |
| `system.filesystem.usage` {state=used,free} and `.utilization` | By, 1     | updown, gauge | all                       | `statfs` of at most four volumes (below)                       |
| `forge.system.file_descriptor.count` and `.limit`              | {fd}      | gauge         | linux                     | `/proc/sys/fs/file-nr`                                         |
| `forge.system.process.count` and `.limit`                      | {process} | gauge         | linux                     | `/proc` count; `pid_max` and `threads-max`                     |

**Group `host`, inside a cgroup v2 container** (linux only; `container.*` is
deliberately not used, see section 9):

| Metric                                                                      | Unit        | Instrument | Source                                                            |
| --------------------------------------------------------------------------- | ----------- | ---------- | ----------------------------------------------------------------- |
| `forge.cgroup.memory.working_set`                                           | By          | updown     | `memory.current` minus `inactive_file` (the kubelet's definition) |
| `forge.cgroup.memory.limit`                                                 | By          | updown     | `memory.max`; absent when `max`                                   |
| `forge.cgroup.memory.oom_kills`                                             | {kill}      | counter    | `memory.events` `oom_kill`                                        |
| `forge.cgroup.pressure.stall_time` {resource=cpu,memory,io; kind=some,full} | s           | counter    | `cpu.pressure`, `memory.pressure`, `io.pressure` `total=` (µs)    |
| `forge.cgroup.pressure.avg10` {resource, kind}                              | 1           | gauge      | the same files, kernel-computed `avg10`                           |
| `forge.cgroup.cpu.throttled_periods` and `.throttled_time`                  | {period}, s | counter    | `cpu.stat` `nr_throttled`, `throttled_usec`                       |
| `forge.cgroup.pids.count` and `.limit`                                      | {pid}       | gauge      | `pids.current`, `pids.max`                                        |

**Filesystems are bounded, not enumerated.** The sampler reports the volumes the
binary actually writes to: the working directory, `os.TempDir()`, and any path a
caller adds through `Options.Volumes` (Reliant adds its worktree and cache
roots), de-duplicated by device and capped at four, labelled by a caller-chosen
`role` (`cwd`, `tmp`, `data`), never by the mount path. This is `diskwatch`'s
de-duplication rule, minus its process scan.

### Readers and libraries

linux readers are written here, over `/proc` and `/sys/fs/cgroup`: cgroup v2
memory events, PSI, throttling and pids are covered by neither contrib `host`
nor gopsutil, and the files are stable, tiny and already parsed by
`cgroupmem` and `machinehealth` (the parsers move into `resmetrics`). A missing
file, an unmounted cgroup or a permission error sets that field's `Errors` entry
and omits that one metric. This is the difference from
`contrib/instrumentation/host`, whose single callback returns an error and drops
the whole sample when any one read fails.

darwin and windows use gopsutil v4 (v4.26.10, 2026-10-24) for process CPU, RSS,
threads, host CPU, memory and swap. [I] On darwin v4 needs no cgo (it goes
through sysctl and purego), which forge already links; on windows it uses
`x/sys` plus WMI. This is inferred from the module, not built here; phase 2
starts by building forge's three release targets with it. Its weight is moderate
(`go-sysconf`, `wmi`, `purego`), and it is imported only from the darwin and
windows files, so a linux build does not carry it. If a cross-compile fails, the
fallback is a stub that reports `process.uptime` and Go runtime numbers only.

---

## 4. Disabling it

**Switch.** `observe.Config.ResourceMetrics` (typed, for a binary that always
wants something else) and the environment variable
`FORGE_OBSERVE_RESOURCE_METRICS`, read in `pkg/observe/otelenv.go`, the only
file `pkg/.golangci.yml` lets call `os.Getenv`, and the environment wins, as it
does for `FORGE_PROFILES` (`profiles_config.go`). Values:

| Value           | Process group | Host group                                           |
| --------------- | ------------- | ---------------------------------------------------- |
| unset or `auto` | on            | on; in a Kubernetes pod, cgroup metrics only (below) |
| `all`           | on            | on, node-wide `system.*` included even in a pod      |
| `process`       | on            | off                                                  |
| `host`          | off           | on                                                   |
| `off`           | off           | off; the sampler is not started                      |

`OTEL_SDK_DISABLED=true` and `OTEL_METRICS_EXPORTER=none` keep their existing
meaning for **export**; they do not stop the sampler if an in-process consumer
asked for it, because the `Snapshot` is not an export. Only
`FORGE_OBSERVE_RESOURCE_METRICS=off` stops the sampler. (An app with no
consumer and `OTEL_SDK_DISABLED=true` starts nothing in practice: the
instruments have nowhere to be read.) `FORGE_OBSERVE_RESOURCE_INTERVAL` sets the
sampling interval (Go duration, default 15s, floor 1s). Both are projected from
KCL: a `resource_metrics` field on the workload (`"off" | "process" | "host" |
"all"`) and an env-level default, rendered the way `profiling` is
(`kcl/workload.k:645`, `kcl/render.k:874`), with a workload's own `env` winning.

**Why host is on by default, narrowed in a pod.** Inside a pod, `/proc/meminfo`
and `/proc/stat` show the node, so every pod on a node would export the same
node-wide series under its own `service.name`: N copies of one fact, and
a pod-scoped alert that fires on a neighbour's load. So in a pod (detected by a
cgroup path that is not the root and `KUBERNETES_SERVICE_HOST`) `auto` reports
the container's cgroup instead and suppresses node-wide `system.*`; the node
stays `kubeletstats`'s. Outside a pod (a host process, a VM, a laptop, a cloud
machine such as a Reliant workspace) the host is the machine and node-wide
numbers are right. `system.memory.limit` and `system.cpu.logical.count` follow
the same rule, and a `forge.resource.scope` info gauge (`scope=host|cgroup`)
records which view a binary reported.

**Prometheus.** Recommended: register on the meter provider in both `Setup`
branches, so `/metrics` serves it. Reasons: the Prometheus reader is the one
always-on surface, so an app with no OTLP endpoint (a local daemon, a dev
process) can still be scraped and `forge env up dev` can show it; the numbers
are the same either way; and `goruntime` being OTLP-only is a quirk, not a
design. The cost is `/metrics` growing by about 20 to 80 series (section 5).
`goruntime.Start` is left as it is in this proposal; moving it into the
always-on path is a separate, smaller change.

---

## 5. Privacy and cardinality

**Rules, enforced by a test over every instrument's attribute keys:**

- Self process and host totals only. No `process.command`, `process.command_line`,
  `process.executable.*`, `process.owner`, `process.pid`, `user.*`, `host.name`
  or `host.id` on any datapoint. (`service.instance.id` is already on the
  resource and is the only per-instance key.)
- No per-CPU, per-network-interface or per-mount series. CPU is the aggregate
  line; filesystems are capped at four and labelled by role.
- Children are not enumerated. A caller that wants a child count can read it
  from the `Snapshot` (`Process.Children int`) and project it; it is not an
  instrument.
- The sampler reads only the binary's own `/proc/self`, `/proc/stat`,
  `/proc/meminfo`, `/proc/loadavg`, `/proc/sys/fs/file-nr`, the cgroup files and
  `statfs` of its own volumes. It does not walk `/proc/<pid>`, except the
  process count, which lists the directory and opens nothing.

**Series.** About 20 for `process` alone, linux; about 40 more for `host`
outside a container (five CPU modes twice, three load averages, memory,
swap, up to four volumes, system fd and pid pairs); about 20 for a container's
cgroup. A linux container with everything on is roughly 40, a bare linux host
with four volumes roughly 80. This is larger than the 15 to 25 per instance an
earlier estimate gave, because that counted the process group only. At the 60s
export interval, 80 series is about 115,000 points a day per instance, which is
negligible next to the span and log volume the same instance already produces.

**Where it goes.** A forge workload exports resource metrics wherever its other
metrics go (the node agent, the keyed ingest, the local ClickStack). A **laptop
or Electron daemon never exports OTLP to Reliant**: it has no endpoint, and this
proposal does not give it one. The numbers reach Reliant only through the
daemon's existing heartbeat to its own gateway, which is already the case for
memory and disk. A local `/metrics` is served only if the app already serves one.

---

## 6. forge consumers

**obsdash.** `BuildWorkloads` (`internal/obsdash/workloads.go:50`) gets a
"process and host" row built on the new names: CPU cores, RSS against limit,
fd utilization, threads, load, PSI. For a Kubernetes workload it stays on
`kubeletstats` for the pod and adds the in-process series (RSS-versus-heap gap,
fds) next to it; for a host-process workload, which has no `kubeletstats`, it is
the first resource panel it has. The metric-name test
(`metric_names_test.go`) pins the new names.

**`forge obs status`.** Gains a saturation block per workload: CPU utilization
against the cgroup quota, memory against its limit, fd utilization, and any
nonzero OOM kill or throttling count in the window. It reads the same series;
a workload that exports none is shown as "not reporting" with the reason
(`off`, or no OTLP endpoint).

**`pkg/releasehealth` (proposed, not committed).** A later phase adds a
`Signal` comparing per-instance `process.memory.usage` and
`process.unix.file_descriptor.count` p95 after a release against its baseline,
normalized per `service.instance.id`, so a leak or an fd regression fails a
release the way an error-rate rise does, and covers hosted and host-process
workloads that `kubeletstats` cannot see. It is left out of the committed phases
because the baseline needs real data from phases 1 to 3, and a noisy resource
signal in the release gate is worse than none (`reason_code` exit 2 exists for
"cannot prove"). It starts as an advisory finding, never a failing one.

---

## 7. Reliant as the first consumer

**Daemon.** `reliant server` roles (api, worker, gateway) already call
`forgeobserve.Setup` (`internal/observability/observability.go:128`), so they
get the process and host groups with a forge pin bump and no code, and their
fleet and pod dashboards gain fds and RSS. The daemon is the substantive work:

1. It starts the sampler itself with `resmetrics.Start`, adding its worktree and
   cache roots to `Options.Volumes`, and subscribes at its heartbeat cadence.
2. `DaemonHeartbeat` gains a nested `ResourceReport resources = 11;` (next free
   tag, to be checked at implementation) holding the process and host numbers
   the UI and alerts want: `process_cpu_cores`, `process_rss_bytes`,
   `process_fd_count`, `process_fd_limit`, `process_threads`, `host_cpu_utilization`,
   `host_load1`, `host_memory_used_bytes`, `host_memory_total_bytes`, swap, and
   the cgroup `cpu_throttled_ratio`, PSI `avg10` triple, `pids_count` and
   `pids_limit`. A new message is a new optional field, so old gateways ignore
   it and old daemons simply do not send it; there is no min-version check
   anywhere to trip.
3. The **existing fields stay populated**, from the same snapshot, so an old
   gateway or UI keeps working: `memory_used_bytes` and `memory_limit_bytes`
   keep their documented basis (cgroup unreclaimable memory), which the daemon
   derives from the `Snapshot`'s raw cgroup components (`memory.current`,
   `inactive_file`, `active_file`, `slab_reclaimable`) rather than from the OTel
   working-set metric; `memory_pressure` keeps its 85% and 75% hysteresis,
   evaluated over the snapshot; `oom_kills` comes from the snapshot's
   `memory.events` counter.
4. `internal/cgroupmem` and `internal/machinehealth`'s `/proc` and cgroup parsing
   are deleted in favour of the package; `machinehealth` keeps its slot-pool and
   concurrent-build fields (Reliant-specific) and logs the same keys. `diskwatch`
   and `forkwatch` stay: their volume discovery (docker root, apt dirs) and fork
   probe are Reliant-specific. Local daemons stop reporting zero for memory on
   macOS and Windows because the primitive reads them.

**Gateway and UI.** The gateway stores the new report beside the memory fields
in `daemon_attachment` (`tools_daemon_inbound.go:317`, repo ext at
`daemon_attachment_repo_ext.go:133`) and copies it into `DaemonInfo`
(`daemon_registry.go:549`). The machine settings page
(`Settings/cloud/machines.tsx`) shows CPU, memory, fd and load for the daemon
process and for the machine, with the degradation reasons it already derives.

**control-plane alerts.** `MachineLoadHigh`, `MachineDiskLow` and
`MachineToolTimeouts` move off the log line in two steps. First, the gateway
(which does export OTLP) records **fleet histograms** with no per-daemon label
(load per CPU, memory utilization, fd utilization, disk free ratio), so a rule
can alert on the tail of the distribution without a series per machine. Second,
a per-machine finding that needs a name is read from `daemon_attachment`, which
already exists for the UI. The log-grep rules stay until the daemon floor
(the upgrade track's minimum-version floor) passes the release that sends the
heartbeat report, because old daemons still emit only the log line; they are
deleted in the PR that raises the floor, not before.

---

## 8. Phased plan

| Phase | Repo          | PR                                                                                                                                                                                                            | Depends on                   |
| ----- | ------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------- |
| 1     | forge         | `pkg/resmetrics`: Snapshot, linux readers (process, host, cgroup v2), instruments, registered from `observe.Setup` in both branches, `FORGE_OBSERVE_RESOURCE_*`, attribute-key privacy test, metric-name test | none                         |
| 2     | forge         | darwin and windows readers (gopsutil v4), `Options.Volumes`, cross-compile of the three release targets, `observe.Resources()` accessor                                                                       | 1                            |
| 3     | forge         | KCL `resource_metrics` field and env default, render and tests; obsdash panel; `forge obs status` saturation; `observability` skill and `observe/doc.go`                                                      | 1                            |
| 4     | reliant       | pin bump; daemon starts the sampler; `ResourceReport` on `DaemonHeartbeat`; `cgroupmem` and `machinehealth` parsing onto the primitive; existing fields kept                                                  | 1, 2                         |
| 5     | reliant       | gateway persists the report and exposes it on `DaemonInfo`; machine settings page; fleet histograms                                                                                                           | 4                            |
| 6     | control-plane | `Machine*` alerts over the histograms and `daemon_attachment`; log rules removed when the floor passes                                                                                                        | 5, the upgrade track's floor |
| 7     | forge         | `releasehealth` advisory resource signal                                                                                                                                                                      | 1 to 3, real baseline data   |

Phases 1 to 3 ship as a forge release with a default that changes no existing
alert; phase 4 is the first reliant PR and needs the matching pin, per the
forge-in-lockstep rule.

---

## 9. Alternatives and the case against

**A. `contrib/instrumentation/host`.** The upstream answer: process CPU time and
system CPU, memory and network, on gopsutil. Rejected as the base: it has no
RSS, fd, thread, load, disk or cgroup metric (so the metrics we most want are
missing), one failed read errors the whole callback and drops the sample, it
exposes no in-process snapshot, and its own documentation calls its names
work-in-progress. It remains a reasonable thing to match names with.

**B. gopsutil only.** One dependency, every platform. Rejected as the whole
answer: it does not read cgroup v2 events, PSI or throttling, which are the
signals that explain an OOM-killed or throttled container, and the linux fast
path is cheaper written by hand than through its process object. It is used
where it is best: darwin and windows basics.

**C. `client_golang`'s `ProcessCollector`.** Already a dependency (`go.mod:41`),
gives `process_cpu_seconds_total`, RSS, virtual memory and fd count and limit on
linux and windows. Rejected: Prometheus-style names that do not match the
`process.*` names `kubeletstats` and every OTel backend use, no host group, no
OTLP path, and no snapshot.

**D. Rely on Kubernetes and node-exporter.** `kubeletstats` already gives pod
CPU and memory, and control-plane runs no node-exporter. This is adequate for a
hosted pod and is kept. It does not cover host-process workloads, local daemons,
a cloud machine as a host, or any in-process view; those are the cases the
owner asked about.

**Devil's advocate against this proposal.**

_It duplicates `kubeletstats` for the most common deployment._ True for pod CPU
and memory, which is why `auto` suppresses node-wide series in a pod, and
`container.*` is not emitted. What it adds in a pod is what `kubeletstats`
cannot see: fds, threads, RSS against Go heap, PSI and throttling.

_Reimplementing `/proc` parsers is maintenance forge does not want._ The
parsers already exist twice in Reliant and are being moved, not written; the
files are kernel ABI and change slowly. The alternative, gopsutil for linux
too, buys little and loses the cgroup signals.

_Always-on sampling is a cost on every binary._ It is lazy: nothing runs between
collections unless a `Subscribe` is open, and a collection is a few small reads
plus one directory listing. The extra `/metrics` series are real, and `off` is
one environment variable.

_A development-status naming convention is a moving target._ Yes. The names are
literals behind one test, a rename is a one-file change plus a dashboard edit,
and the `forge.*` names are ours to hold steady.

_Sampler plus a typed `Snapshot` plus instruments is two APIs for one set of
numbers._ The instruments are a thin registration over the snapshot, not a
second reader. The snapshot is the part Reliant needs, because the daemon has no
OTLP endpoint and must not grow one, and the part another forge app that
projects into its own protocol will need.

_The heartbeat is a poor transport for fleet alerting._ It is the transport
that already exists and is already authenticated; building a second pipe from
laptops to us is worse. Fleet histograms and the database keep per-machine
cardinality out of the metrics store.

---

## 10. Open decisions

The owner said to proceed on the recommendations, so each is stated as the
default taken.

1. **Package location.** Recommended default: `pkg/resmetrics`, a sibling of
   `pkg/observe`, so it has no import cycle with `observe` and Reliant can use
   it without OTel (proceeding on this). The alternative, `pkg/observe/resource`,
   couples the readers to the SDK.
2. **Default.** Recommended default: process on everywhere, host on everywhere,
   narrowed to the container's cgroup in a Kubernetes pod (proceeding on this).
3. **Prometheus `/metrics`.** Recommended default: yes, register in both
   `Setup` branches (proceeding on this). `goruntime` is left alone.
4. **Interval.** Recommended default: 15s sampling, collected at the existing
   export interval (proceeding on this).
5. **Naming.** Recommended default: `process.*` and `system.*` where semconv has
   them, `forge.*` otherwise, and no `container.*`, to avoid merging with
   `kubeletstats`'s series that `obsdash` reads by name (proceeding on this).
6. **Filesystems.** Recommended default: cwd, tmp and caller-added roots, at
   most four, labelled by role (proceeding on this).
7. **gopsutil on darwin and windows.** Recommended default: yes, v4, imported
   only from those two OS files, subject to the phase 2 cross-compile check
   (proceeding on this).
8. **Reliant heartbeat shape.** Recommended default: one nested `ResourceReport`
   message on a new tag, existing fields kept and filled from the same snapshot
   (proceeding on this).
9. **Release-health resource signal.** Recommended default: later, advisory
   only (proceeding on this).
10. **Disable switch.** Recommended default: `FORGE_OBSERVE_RESOURCE_METRICS`
    with `auto|off|process|host|all`, a KCL `resource_metrics` field, and
    unchanged `OTEL_SDK_DISABLED` and `OTEL_METRICS_EXPORTER` semantics
    (proceeding on this).
