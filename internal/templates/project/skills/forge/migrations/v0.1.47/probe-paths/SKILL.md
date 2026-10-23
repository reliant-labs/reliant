---
name: migration-v0.1.47-probe-paths
description: Upgrade a v0.1.46 server to configurable probe paths — hand serverkit.Config.Probes to serverkit.Probes.Paths, and declare liveness_paths / readiness_paths when a platform probes a path other than /healthz and /readyz.
relevance: migration
---

# v0.1.47 — configurable probe paths

Use when `cmd/<bin>/cmd/serve.go` builds `&serverkit.Probes{` with no
`Paths:`, or when a platform probes the process on a path other than
`/healthz` / `/readyz` (a GKE `HealthCheckPolicy`, a docker-compose
healthcheck, an ingress health route).

## What changed

`serverkit.Config.Probes` (`serverkit.ProbePaths{Live, Ready []string}`) names
the paths the process answers its probes on. Empty is `/healthz` and
`/readyz`, so nothing changes for a project that sets none. Each list's
first path is the canonical one; the rest are aliases served alike. The boot
probes, `serverkit.Run`'s listener and a `serverkit.Probes` surface
(`Probes.Paths`) all honour it, and `Config.Probes.Validate` rejects a path no
listener can serve.

The scaffold projects it from two typed config fields, `liveness_paths`
(`LIVENESS_PATHS`, default `/healthz`) and `readiness_paths`
(`READINESS_PATHS`, default `/readyz`), both comma-separated. The KCL lowering
reads the same env values, together with `PORT`, for a forge-built
workload's default probe: the first path of each, on the process's port. So
the probe a pod is given is the one its process serves. A probe the workload
declares is kept as declared.

`serverkit.Probes.Handle(path, h)` adds a route to the probe surface, such as a
`/flow-health` that must answer on the probe port. Add it before the
listener that serves the probes is built. A node that only calls
`g.Probes.Handle` or `g.Probes.AddReadyCheck` no longer counts as serving the
probes, so its root keeps its probe listener.

## What to do

1. In `serve.go` (yours), hand the paths to the probe surface, or the
   listener serves the defaults while the boot probes serve the configured
   ones:

   ```go
   Probes: &serverkit.Probes{Drainer: drainer, Metrics: metrics,
       Timeout: skCfg.ReadinessTimeout, Logger: logger, Paths: skCfg.Probes},
   ```

2. To configure the paths, add the two fields to `proto/config/v1/config.proto`
   (copy them from a fresh `forge project new`), project them in
   `serverkitConfig`:

   ```go
   skCfg.Probes.Live = serverkit.ProbePathList(cfg.LivenessPaths)
   skCfg.Probes.Ready = serverkit.ProbePathList(cfg.ReadinessPaths)
   ```

   then set the values per env in KCL config, not as a hand-written probe.
   Keep `/healthz` and `/readyz` as aliases (`/health,/healthz`) while
   anything else still probes them, such as `forge doctor`.

3. Run `forge generate`, then `go build ./...`.
