---
name: observability/resources
description: Process and host resource metrics — the CPU, memory, file descriptors and threads every forge Go binary reports about itself, and its machine's CPU, load, memory, swap and volumes (or its container's cgroup), on linux, darwin and windows; turning them down per env or workload (resource_metrics, FORGE_OBSERVE_RESOURCE_METRICS); reading them in-process (observe.Resources); the dashboard panels.
---

# Process and host resource metrics

Every Go binary built on `forge/pkg/observe` reports its own resource use and its machine's, with no code: `observe.Setup` (which `serverkit.Run` calls) starts a `pkg/resmetrics` sampler and registers it on the meter provider. The metrics are on `/metrics` always, and in OTLP wherever metrics export is on, filed under the process's `service.name`.

| Group | What | Names |
|---|---|---|
| process | CPU time and cores, resident and virtual memory, RSS outside the Go runtime, open fds against the soft limit, threads, context switches, page faults, disk IO, uptime | `process.*`, `forge.process.*` |
| host | CPU by mode, load average, memory by state, swap, up to four volumes by role (`cwd`, `tmp`, `data`), system-wide fd and process counts | `system.*`, `forge.system.*` |
| host, inside a Kubernetes pod | the container's cgroup v2 instead of the node: memory working set and limit, OOM kills, CPU throttling, pressure (PSI), pids | `forge.cgroup.*` |

Names follow the OpenTelemetry semantic conventions; `forge.*` is used where they have none. Each is declared, with its unit and its closed attribute values, in `forge/pkg/observe/metrics`. No datapoint carries a pid, a command line, a user, a host name or a path.

**Platforms.** linux reads `/proc` and the cgroup; darwin and windows ask the kernel directly (no cgo, so cross-compiled releases have them). A platform leaves out what it cannot measure rather than reporting zero: darwin has no process disk IO or iowait/steal CPU time; windows has no fds (it reports open handles as `forge.process.handle.count`), page faults, context switches, process disk IO, load average, swap or iowait/steal. Any other OS reports only uptime and the CPU count.

## Turning them down

One switch, most specific first: the workload's own `env`, then `resource_metrics` on the workload, then the env default on `forge.ObservabilitySignals`, then the runtime's `auto`.

```kcl
observability = forge.Observability {
    signals = forge.ObservabilitySignals {resource_metrics = "process"}   # every workload in this env
}

fw.Workload {name = "api", resource_metrics = "off", ...}                  # this one only
```

| Value | process group | host group |
|---|---|---|
| `auto` (default) | on | on; inside a pod, the container's cgroup instead of the node |
| `all` | on | on, the whole machine even inside a pod |
| `process` | on | off |
| `host` | off | on (narrowed in a pod, as under `auto`) |
| `off` | off | off; no sampler starts |

It renders `FORGE_OBSERVE_RESOURCE_METRICS` on every workload that runs a process. `FORGE_OBSERVE_RESOURCE_INTERVAL` (a Go duration, default `15s`, at least `1s`) is how old a sample may get before a collection takes a new one; the sampler runs nothing between collections. The typed equivalents are `observe.Config.ResourceMetrics` and `ResourceMetricsInterval`, which the environment wins over. `OTEL_SDK_DISABLED` and `OTEL_METRICS_EXPORTER=none` stop export only: `/metrics` keeps the series.

## Reading them in-process

An app that projects the same numbers into its own protocol (a heartbeat, a status page) reads the sampler `Setup` started instead of starting a second one:

```go
if s := observe.Resources(); s != nil {   // nil when the switch is off
    snap := s.Latest()                     // cached; resampled when older than the interval
    if p := snap.Process; p != nil && p.Memory != nil {
        log.Info("rss", "bytes", p.Memory.RSS)
    }
}
```

Every pointer in a `Snapshot` is nil when its reader failed or the platform has no such number; `snap.Errors` says which reader failed and why. `Subscribe(every)` delivers a fresh sample on a ticker. A binary that does not call `observe.Setup` starts its own: `resmetrics.Start(ctx, resmetrics.Options{...})`. To report its own data volumes, append them: `Volumes: append(resmetrics.DefaultVolumes(), resmetrics.Volume{Role: resmetrics.VolumeData, Path: dir})`.

## On the dashboards

Each Go process's dashboard has a **Process resources** section (CPU cores, resident memory and its share outside the Go runtime, open files against the limit, threads and handles). A process that runs straight on a machine (`forge.OnHost`, compose), which has no node agent, also gets a **Host** section (CPU by mode, memory by state, load against the CPU count, how full its volumes are). Cluster and hosted processes keep the node agent's workload saturation panels for the container's view. In the metric catalog these names are built-ins (`process.`, `system.`, `forge.`), listed apart from the app's own metrics.
