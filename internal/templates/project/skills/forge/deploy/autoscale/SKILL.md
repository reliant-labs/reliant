---
name: deploy/autoscale
description: forge.Autoscale — a replica range (minReplicas, maxReplicas up to 10, cpuTargetPercent 10-95, default 70) on a service or worker, rendered as an autoscaling/v2 HorizontalPodAutoscaler with stated scale-up and scale-down behaviour. What it replaces, what render refuses, the scaffold's `_cluster_replicas` collision, the metrics-server prerequisite, and why a hosted deploy refuses it until the control plane advertises autoscales-workloads.
---

# Autoscale a service or worker

A workload declares a replica **range** instead of a count; forge renders a
HorizontalPodAutoscaler beside its Deployment and stops stating
`spec.replicas`, so nothing forge applies fights the autoscaler.

```python
_api = fw.Workload {
    name = "api"
    # ...
    autoscale = forge.Autoscale {minReplicas = 2, maxReplicas = 6}   # cpuTargetPercent defaults to 70
}
```

`forge.Autoscale` and `fw.Autoscale` are the same schema. Check the result with
`forge env render <env> --kind HorizontalPodAutoscaler`.

## Fields

| field | default | range |
|---|---|---|
| `minReplicas` | required | 1-10. The fewest pods and the starting count. No scale to zero. |
| `maxReplicas` | required | 1-10, not below `minReplicas`. |
| `cpuTargetPercent` | 70 | 10-95. Average pod CPU as a percent of the pod's CPU **request**, summed over its containers. |

The target stops at 95 because a CPU limit equal to the request (the default
shape: 250m / 1 GiB) caps utilization near 100%. The request is the real dial:
set `resources` so one pod's request matches what it can do, or it never scales.

## Behaviour (stated in the HPA, never left to cluster defaults)

Scale up: no stabilization window; per 60s it may double the pods or add 4,
whichever is more. Scale down: a 300s window (it goes only to the highest count
any recommendation in the window asked for), then it may remove every excess pod
at once. The PDB and the hostname spread are keyed on `maxReplicas`, so they stay
put while the count moves; a maximum of 1 renders neither.

## What render refuses

- `kind` other than `service` or `worker` (an operator elects one leader; a
  job, cron or tool has no Deployment).
- `replicas` set beside `autoscale`. **The scaffold's `_on_cluster` binder
  applies `_cluster_replicas = 3` as a floor, which collides: set
  `_cluster_replicas = 1`** in `deploy/kcl/<env>/main.k`.
- `storageGiB` (a ReadWriteOnce volume is mounted by one pod) and
  `strategy = "Recreate"` (a correctness bound, not a capacity choice).
- `Bundle.overrides` on the Deployment's `spec.replicas` or on the HPA
  (`deploy/overrides`). Change `autoscale`.
- A host, compose or build-only runtime (nothing there scales).

## Prerequisites

The cluster needs metrics-server (k3d, GKE, EKS and AKS ship it); without it the
HPA reports unknown metrics and never scales.

## On Reliant hosting: not applied yet

`forge.OnHosted` passes the hosted profile, but `forge env deploy` refuses an
autoscaled hosted workload until the control plane advertises the
`autoscales-workloads` capability (`release.CapabilityAutoscalesWorkloads`),
because an older one would run it at one replica. The error names each
workload: `declares autoscale, but this control plane does not apply
HorizontalPodAutoscalers yet`. Until then set `replicas` for hosted envs, or bind
the workload to a cluster you operate (`forge.OnCluster`). Do not promise a user
hosted autoscaling: the design (`docs/proposals/autoscaling.md`) puts it in later
slices, and managed Postgres storage autogrow is not built.

Related: `deploy/model` (workload fields), `deploy/pdb`, `deploy/hosting`.
