---
name: deploy/delivery-units
description: How an env's delivery is ordered — trees per cluster × delivery unit × phase for a bundle a reconciler (Flux) applies, unit-by-unit workload rollout for `forge env deploy`, declaring units with `forge.DeliveryUnit` and `delivery_unit`, what `after` means, and when moving an object between units is a stop finding.
---

# Delivery units

A bundle a reconciler (Flux) applies states the order as TREES: every real cluster is split into
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
  finding when a reconciler applies either unit** (acknowledge it with
  `--acknowledge-destructive tree_moved`): the tree it leaves prunes it and the
  tree it joins creates it, with nothing ordering the two. Adopt it into the new
  tree first, or accept the delete and re-create. Between two units forge applies
  directly (`applier = "forge"`, or not in `hub_converges`) it is an info
  finding: nothing prunes it, only its place in the rollout order changes.

## Direct apply (`forge env deploy`)

Direct apply honours the same `after` for the WORKLOADS (Deployments,
StatefulSets, DaemonSets, CronJobs):

1. Everything a release sends ahead of its workloads goes first, for every unit
   at once, exactly as without units: CRDs and Namespaces, ConfigMaps and
   Secrets, the secret-store stages, Services, ServiceAccounts, Roles and
   RoleBindings and the other support objects, and the pre-rollout Jobs (a
   migration still completes before any new code).
2. Then the units' workloads, wave by wave: a unit's workloads are applied on
   every cluster and awaited Ready on every cluster before any unit `after` it is
   applied.
3. Then the post-rollout Jobs, the apply-time prunes, and the ordinary rollout
   wait.

A unit that does not roll out stops the deploy in every rollout mode
(`--rollout=warn` and `--no-wait` included, like the pre-rollout gate): the units
after it are not applied and nothing is pruned; the error names the unit, the
workloads and the units it held. An env that declares no units, or whose
workloads all sit in one unit, is applied exactly as before. Units the hub
converges are neither applied nor awaited from here.

Typical use: a broker whose config grants what its consumers need. Put the
broker (its StatefulSet and config) in a unit with `after = []` and declare
`forge.DeliveryUnit {name = "base", after = ["broker"]}`, so the broker has
rolled out the new grants before any consumer pod starts.

## What the bundle records per tree

`path`, `depends_on`, a 90 s per-attempt `timeout_seconds` and 15 s
`retry_interval_seconds`, and `health`: the Jobs of a `pre`/`post` tree and the
Deployments and StatefulSets of a `rollout` tree. A DaemonSet is not a gate, which
is also what direct apply waits on.
