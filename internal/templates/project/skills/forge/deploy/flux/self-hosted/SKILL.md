---
name: deploy/flux/self-hosted
description: Opt a self-hosted env (no control plane) into being reconciled by a Flux forge installs in its own cluster — `applier = "flux"`, `forge.FluxDelivery`, the one-time `forge env deploy <env> --target flux-delivery` bootstrap, then `forge env deploy <env> [<version>]` to ship each release (push the bundle, point the Flux, wait), what happens when a release needs a permission the Flux was not granted (exit 9), and `forge env status`. Use when an env without a control plane should be delivered by an in-cluster Flux instead of a direct apply.
---

# Self-hosted delivery: `applier = "flux"`

For an env with **no control plane** that should still be reconciled from a
bundle rather than applied by hand. Opt in on the Bundle:

```kcl
output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    cluster_target = _prod
    applier = "flux"
    # Optional; every field has a working default.
    flux_delivery = forge.FluxDelivery {
        pull_secret_from = "acme-prod/ghcr-pull"   # an existing image-pull Secret
    }
    workloads = [...]
})
```

Then bootstrap the env's Flux, once per cluster (and again whenever you change
`flux_delivery`):

```bash
forge env render prod --target flux-delivery    # review what it installs
forge env deploy prod --target flux-delivery    # install it; records no release
```

That installs the pinned Flux Operator and FluxInstance (`forge.flux_chart()`),
and the env's own **delivery namespace** (`forge-delivery-<project>-<env>`),
holding the env's delivery ResourceSet (`delivery.<env>`), the copy of the pull
Secret, and the identities its Flux applies as. Two envs can share a cluster:
each gets its own namespace. Run `forge cluster up <env>` for a forge-managed
k3d cluster and it installs the same thing.

## Shipping a release

```bash
forge env deploy prod              # build, cut, then ship it (like any env)
forge env deploy prod v1.4.0       # ship a release that is already cut
forge env status prod              # what the env's Flux reports
forge env status prod --wait       # wait on it
```

forge never applies an `applier = "flux"` env itself. A deploy records the
release in forge's file ledger, pushes the release's bundle to the env's
registry (`<registry>/bundle.v1/<env>`, pinned by digest), points the env's
Flux at it, and waits until every Kustomization has applied it. The usual
deploy flags work: `--plan`, `--yes`, `--approve <digest>`, `--no-wait`,
`--timeout`, `--json`. A release deploy cannot be scoped (`--target`,
`--frontends-only`): the Flux applies the whole bundle.

The bundle registry is `flux_delivery.registry` when declared, otherwise the
registry of the env's first cluster workload image (`ghcr.io/acme/api` →
`ghcr.io/acme`). A `localhost:<port>` registry is pointed at as
`host.k3d.internal:<port>`, the name a pod in a forge-managed k3d cluster
reaches it by. The push uses your docker credentials; the Flux pulls with the
copied `pull_secret_from` Secret.

**Several deployers, one order.** The pointer carries a fence, the number of
pointer writes so far, and every write is conditional on the pointer it read
(create-only for the first, resourceVersion-conditional after). So a CI runner
and an administrator, each with their own ledger, share one sequence: the one
that loses a race re-reads the pointer and re-checks against what won.

**A release that drops a tree** (its last post-rollout Job, or a whole delivery
unit) first retires that tree under the live bundle (`retire: orphan`: what it
applied stays in the cluster, unmanaged), waits until the Flux has applied the
retire, then ships. A deploy that times out (or dies) after the retire is
written leaves it on the pointer; the next deploy still waits for the Flux to
apply it before it drops the tree. Delete the orphaned objects yourself if you
no longer want them.

**`forge env status <env> --wait`** waits on whatever the pointer names.
`--release <v>` and `--promotion <id>` are checked against the pointer first
(exit 6 when it names another, 2 when an older pointer does not record its
release); `--interval` and `--fail-fast` apply, and `--stable-for`,
`--watch-json` and `--include-unpinned` are refused (exit 1). A release is live
only when the delivery ResourceSet has rendered this pointer's input (the
OCIRepository carries its fence and digest, and every Kustomization's spec is
the one the input renders, suspension, dependencies and health checks
included), the source has fetched the digest, and every Kustomization reports
it applied, each by a status of its current generation.

Like every `--wait`, the wait is not fail-fast: a Kustomization that fails a
health check on a cold start and then settles converges, and a failure Flux
still reports when the budget runs out is exit 1. `--fail-fast` (on the deploy
or on `forge env status --wait`) ends it at the first failure. A renderer that
stalls on the input ends it at once either way.

The deploy's exit codes are the usual ones (`forge env deploy --help`): 0 live,
1 Flux reported a failure (relayed in its own words), 8 still progressing when
the budget ran out (keep waiting with `forge env status <env> --wait`), and 9
below.

## When a release needs a new permission

The bootstrap grants the Flux's apply identities exactly what the env's render
needs, and records what it granted. A release that adds a kind those grants do
not cover — a new CRD and its resources, a first StatefulSet or CronJob, a
namespace no earlier release used — would fail `Forbidden` in the cluster. So
every deploy checks the release against the record **before anything is
recorded, pushed or pointed**:

- **Your credential can grant it** (an admin kubeconfig on your laptop): the
  deploy plan lists the new grants (`delivery identity grants`), and once you
  approve the plan (`--yes`, `--approve <digest>`, or at the prompt) the deploy
  re-runs the bootstrap with your credential and continues. Nothing extra to
  do.
- **It cannot** (a CI credential that may only point the Flux): the deploy
  stops with **exit 9** and nothing written, naming each missing permission
  and the command to run:

  ```
  release 20261102.101500-ab12cd34ef56 for env prod needs permissions the delivery identities were not granted:
    forge-delivery-pre    apiextensions.k8s.io/customresourcedefinitions  (cluster)
    forge-delivery-apply  apps/statefulsets                               (namespace shop)
  this credential cannot grant them (it may not escalate rbac.authorization.k8s.io/clusterroles/...).
  Ask a cluster admin to run:   forge env deploy prod --target flux-delivery
  then re-run this deploy. Nothing was recorded, pushed or pointed.
  ```

  A cluster admin runs that bootstrap from a checkout of the release; then
  re-run the deploy. `--json` carries the same list as `delivery_grants`.

If the cluster reports the same gap anyway (a pointer written by hand, a kind
forge could not derive), the deploy's wait and `forge env status` print the
same message, naming the Kustomization that reported it, and exit 1. Flux
retries on its own; once the bootstrap re-runs, it converges with no
re-deploy.

A deploy credential needs, in the env's delivery namespace only: create and
update on `resourcesetinputproviders.fluxcd.controlplane.io`, get on the
`forge-delivery-grants` ConfigMap, and get/list on `resourcesets`,
`ocirepositories` and `kustomizations`; plus push access to the bundle
registry. It needs no RBAC write anywhere.

## Tuning the delivery

| `forge.FluxDelivery` field | default | what it changes |
|---|---|---|
| `apply_identities` | `True` | each tree applies as an identity whose grant forge derives from the env's render: the `pre` trees (Namespaces, CRDs, Roles, config) as one, the rollout and post trees as another, and a Kustomization naming neither is Forbidden. `False`: Flux applies as cluster-admin; only for a cluster nobody else shares, where this Flux is the only applier |
| `delivery_scope` | `"namespace"` | `"namespace"`: the rollout identity holds a Role in each namespace the render targets. `"cluster"`: one ClusterRole over the same kinds in every namespace |
| `namespace` | `forge-delivery-<project>-<env>` | the delivery namespace, when the derived one is too long or taken |
| `pull_secret_from` | `""` | `<namespace>/<name>` of an existing image-pull Secret the bundles are pulled with; the bootstrap copies it. Empty: the registry needs no credential. Re-run the bootstrap after rotating it |
| `registry` | the first cluster workload image's registry | where each release's bundle is pushed (`ghcr.io/acme` → `ghcr.io/acme/bundle.v1/<env>`), as the machine running the deploy reaches it |
| `values` / `placement` / `distribution` | `{}` | passed to `forge.flux_chart()` (operator chart values, a node pool, a Flux image mirror) |

**The grants follow the render.** Re-running `forge env deploy <env> --target
flux-delivery` re-derives them from the checkout's render; a grant the render
no longer needs is removed by the same run.

**Signing is at most `audit`.** Nothing signs a self-hosted env's bundles yet.
Left unset, `delivery_policy`'s `signing` and `flux_verify` resolve to `off`
for an env with no control plane; an env that asks for `enforce` gets `audit`,
and `forge env render`, `forge env shape` and the bootstrap say so. An env that
asks for `audit` or `off` keeps what it asked.

What the render refuses: `applier = "flux"` beside a `control_plane` (that env
is delivered by its hub: `hub_converges`), on an env that targets more or fewer
than one cluster, beside a declared `forge.flux_chart()` (the bootstrap is that
install), and `flux_delivery` on an env that does not declare `applier = "flux"`.
