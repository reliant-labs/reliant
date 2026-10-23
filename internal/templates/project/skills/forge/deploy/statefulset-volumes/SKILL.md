---
name: deploy/statefulset-volumes
description: Growing a StatefulSet's volume — raise the declared size and forge converges it (a pre-rollout resize Job grows the claims and orphan-recreates the StatefulSet), under direct apply and under Flux. Refuses a shrink or any other volumeClaimTemplate change. Opt-out and image knobs.
---

# Growing a StatefulSet's volume

A StatefulSet's `volumeClaimTemplates` are immutable, so raising a declared size
(`clickhouse_disk = "50Gi"` → `"100Gi"` in KCL) would make every apply of the new
render fail with `updates to statefulset spec for fields other than … are
forbidden`. **You do not work around this.** Raise the number and deploy.

## What forge does

For every StatefulSet that has `volumeClaimTemplates`, the render carries a
**pre-rollout Job** (`sts-volume-resize-<unit>-<hash>`, plus its ServiceAccount,
Role, RoleBinding and NetworkPolicy) in the same cluster, namespace and delivery
unit. It runs before the rollout, under `forge env deploy` and in a Flux-applied
bundle alike (the bundle's `pre` tree is what the `rollout` tree waits on), and for
each StatefulSet:

1. reads the live object. **Absent, or the sizes already match → does nothing.**
2. refuses (below) if anything but a template's storage request grew;
3. grows every existing claim `<template>-<sts>-<ordinal>` that is smaller than the
   declared size (including claims of replicas scaled away). A claim already at or
   above it is left alone, so a volume expanded by hand ahead of the declaration is
   the declaration catching up, not drift;
4. deletes the StatefulSet with `--cascade=orphan`. The pods keep running; the
   rollout then creates the StatefulSet again with the new templates and adopts
   them, so nothing restarts unless the pod template changed too.

It is idempotent: the Job's name carries a hash of the declared volumes, so it runs
once per declared change and a re-apply or a Flux reconcile is a no-op. Interrupted
at any step, running it again finishes the job.

## What it refuses (the deploy stops, nothing is written)

| Change | Why |
|---|---|
| a **shrink** (`100Gi` → `50Gi`) | a PersistentVolumeClaim cannot shrink; lowering the template alone would describe volumes that do not exist |
| a different storage class, access mode, volume mode, label or annotation on a template | a different volume, not a bigger one |
| an added, removed or renamed template | existing claims would contradict the template |
| a changed `selector`, `serviceName` or `podManagementPolicy` alongside the growth | the recreated StatefulSet would not adopt the running pods |
| a claim whose StorageClass lacks `allowVolumeExpansion` | the API server refuses the claim patch |

The refusal is reported **twice, before anything is applied**: `forge env deploy`'s
preflight compares each rendered StatefulSet with the live one (the same
`stsresize.Decide`) and blocks with `StatefulSet volumes that cannot be converged`;
and the Job itself exits 2 — its `podFailurePolicy` fails the Job at once rather
than retrying — so a bundle applied by Flux stops at the `pre` tree with the reason
in the pod's log (`kubectl -n <ns> logs job/<name>`).

## Knobs

```python
statefulset_resize = forge.StatefulSetResize {
    image = "mirror.example/k8s:1.34"   # anything with kubectl and jq; default: digest-pinned docker.io/alpine/k8s
    # disabled = True                    # render no Job: a raised size then fails the apply, and you resize by hand
}
```

It is **not rendered** for `lifecycle = "local"` / `"ephemeral"` envs (scratch
clusters, which the direct-apply recovery converges), nor for a StatefulSet with no
`volumeClaimTemplates`.

## Things to know

- **The Job's permissions are namespace-scoped and narrow:** `get`/`delete` on the named StatefulSets
  only, `get`/`list`/`patch` on PVCs. Nothing cluster-scoped, so the Job learns
  whether a claim may grow from a server-side dry-run patch, not by reading
  StorageClasses.
- **A default-deny namespace** is handled: the Job ships a NetworkPolicy selecting
  only its own pod (DNS + the API server).
- **A private mirror:** put the pull Secret on the cluster target's
  `image_pull_secrets`; it rides the Job's ServiceAccount like any forge workload's.
- **The image is the one thing the cluster must be able to pull.** Verify with
  `forge env render <env> --kind Job --name <job>` — the Job's `image` is the digest-pinned
  default unless you set one.
- Scaling a volume *down*, or moving to another storage class, is a data migration:
  restore into a new StatefulSet. `Bundle.overrides` cannot change a template.
