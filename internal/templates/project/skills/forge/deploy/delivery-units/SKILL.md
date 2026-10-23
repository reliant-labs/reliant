---
name: deploy/delivery-units
description: How a bundle a reconciler (Flux) applies is ordered — trees per cluster × delivery unit × phase, declaring units with `forge.DeliveryUnit` and `delivery_unit`, what `after` means, and why moving an object between units is a stop finding.
---

# Delivery units

`forge env deploy` applies an env in its own order. A bundle a reconciler (Flux)
applies cannot, so it states the order as TREES: every real cluster is split into
`manifests/<cluster>/<unit>/<phase>/` (phase `pre`, `rollout`, `post`), and each
tree depends on the one before it. The phases are the rules `deploy` describes
for migrations: a Job is `pre` unless it declares `deployPhase = "post-rollout"`,
workloads are `rollout`, and everything else is the `pre` support a Job rests on.
`forge env deploy` and the bundle use the same classifier, so they cannot disagree.

A **unit** is a named slice you deliver in a chosen order. Declare the order once
and place workloads and raw objects in units:

```kcl
delivery_units = [
    forge.DeliveryUnit {name = "daemon-substrate", after = []}            # waits for nothing
    forge.DeliveryUnit {name = "hub-base", after = ["daemon-substrate"]}
    forge.DeliveryUnit {name = "control-plane", after = ["hub-base"]}
]
wl.migrate | {delivery_unit = "control-plane", runtime = ...}
forge.Manifests {name = "crds", delivery_unit = "hub-base", objects = [...]}
```

- `after` names the units whose LAST tree must be Ready, on every cluster it has
  trees, before this unit's first tree applies. Unset means "after `base`" (the
  unit nothing else declares); `after = []` waits for nothing. "The daemon
  cluster before the hub" is a dependency between units, never a cluster name.
- An env that declares no units puts everything in `base`: it still gets trees.
- A cycle, a dependency on a unit nothing declares, or a unit name that is not a
  path segment is refused at render.
- **Moving an object to another unit between releases is a `tree_moved` stop
  finding** (acknowledge it with `--acknowledge-destructive tree_moved`): the tree
  it leaves prunes it and the tree it joins creates it, with nothing ordering the
  two. Adopt it into the new tree first, or accept the delete and re-create.

## What the bundle records per tree

`path`, `depends_on`, a 90 s per-attempt `timeout_seconds` and 15 s
`retry_interval_seconds`, and `health`: the Jobs of a `pre`/`post` tree and the
Deployments and StatefulSets of a `rollout` tree. A DaemonSet is not a gate, which
is also what direct apply waits on.
