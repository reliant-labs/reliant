---
name: deploy/pod-priority
description: priorityClassName on a workload — when pods share nodes with ranked pods, an unranked pod is priority 0 and gets preempted, and a rollout's surge pod can stall in FailedScheduling. Apply the PriorityClass to the cluster first.
---

# Pod priority on shared nodes

`priorityClassName` names a cluster-scoped PriorityClass that ranks a
workload's pods against every other pod competing for a node. Declare it
wherever your pods share nodes with pods that already carry one: an unranked
pod is priority 0, so a higher-priority pod preempts it, and a rollout's surge
pod can be evicted repeatedly, stalling in `FailedScheduling`.

```kcl
proxy = fw.Workload {name = "proxy", kind = "service", priorityClassName = "acme-platform"}
```

Apply the PriorityClass to every cluster the workload lands on BEFORE the pods
that name it — Kubernetes refuses a pod naming a class the cluster does not
have. Unset means the cluster's default. `forge.OnCluster` only.
