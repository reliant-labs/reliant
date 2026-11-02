---
name: deploy/flux/self-hosted
description: Opt a self-hosted env (no control plane) into being reconciled by a Flux forge installs in its own cluster — `applier = "flux"`, `forge.FluxDelivery`, and the `forge env deploy <env> --target flux-delivery` bootstrap (Flux Operator, a per-env delivery namespace and ResourceSet, apply identities derived from the render, the pull Secret copy). Use when an env without a control plane should be delivered by an in-cluster Flux instead of a direct apply.
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

**Shipping releases to it is not in forge yet.** Until it is, `forge env deploy`
of an `applier = "flux"` env (with or without a version) and `forge env up` are
refused before anything is recorded or applied; only the bootstrap runs. Remove
`applier = "flux"` to deploy such an env directly in the meantime.

| `forge.FluxDelivery` field | default | what it changes |
|---|---|---|
| `apply_identities` | `True` | each tree applies as an identity whose grant forge derives from the env's render: the `pre` trees (Namespaces, CRDs, Roles, config) as one, the rollout and post trees as another, and a Kustomization naming neither is Forbidden. `False`: Flux applies as cluster-admin; only for a cluster nobody else shares, where this Flux is the only applier |
| `delivery_scope` | `"namespace"` | `"namespace"`: the rollout identity holds a Role in each namespace the render targets. `"cluster"`: one ClusterRole over the same kinds in every namespace |
| `namespace` | `forge-delivery-<project>-<env>` | the delivery namespace, when the derived one is too long or taken |
| `pull_secret_from` | `""` | `<namespace>/<name>` of an existing image-pull Secret the bundles are pulled with; the bootstrap copies it. Empty: the registry needs no credential. Re-run the bootstrap after rotating it |
| `values` / `placement` / `distribution` | `{}` | passed to `forge.flux_chart()` (operator chart values, a node pool, a Flux image mirror) |

**The grants follow the render.** A release that adds a kind the identities do
not hold (a new CRD, a new namespace) is Forbidden until you re-run
`forge env deploy <env> --target flux-delivery`, which re-derives them; a grant
the render no longer needs is removed by the same run.

**Signing is at most `audit`.** Nothing signs a self-hosted env's bundles yet,
so `delivery_policy`'s `signing` and `flux_verify`, which default to `enforce`,
take effect as `audit` here; `forge env render`, `forge env shape` and the
bootstrap say so. An env that asks for `audit` or `off` keeps what it asked.

What the render refuses: `applier = "flux"` beside a `control_plane` (that env
is delivered by its hub: `hub_converges`), on an env that targets more or fewer
than one cluster, beside a declared `forge.flux_chart()` (the bootstrap is that
install), and `flux_delivery` on an env that does not declare `applier = "flux"`.
