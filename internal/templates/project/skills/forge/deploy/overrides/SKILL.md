---
name: deploy/overrides
description: Change one field of a forge-rendered object without forking the template — Bundle.overrides, keyed by Kind/name, applied at render, conflicts fail the render.
---

# Overriding a rendered object

Forge renders opinionated objects. Sometimes one field of one of them is wrong
for your environment: this Deployment needs 10 replicas, that container needs
a CPU limit, this Namespace has to run at PSA `baseline` because a workload in
it needs a capability `restricted` forbids.

The escape hatch for that is `Bundle.overrides`. You name the object and the
field; forge renders its own object and then patches it.

```kcl
output = forge.render(forge.Bundle {
    project = "acme"
    workloads = [wl.api | {runtime = forge.OnCluster {target = _k3d}}]

    overrides = {
        "Deployment/api" = {spec.replicas = 10}
    }
})
```

That is the whole surface. A key that names the object, a map of the fields to
change.

**Reach for this when the field you need has no forge affordance.** If forge
models the thing — replicas, resources, probes, env, ports are all on
`forge.Workload` — declare it there instead. An override is for the field
forge does not model, and for the one-off that should not become a property of
the workload everywhere it runs. It is deliberately not a general templating
layer: it changes fields of objects forge renders, and it cannot add an
object (declare a `forge.Manifests` group for that) or remove one.

## The key

```
Kind/name                      Deployment/api
Kind/namespace/name            Deployment/acme-dev/api
Kind/name@cluster              Deployment/api@k3d-cp
Kind/namespace/name@cluster    Deployment/acme-dev/api@k3d-cp
```

Give only as much as it takes to be unambiguous. Most envs render one
namespace and one cluster, so `Deployment/api` is the whole key you need.
Add the namespace when one kind+name is rendered into two namespaces; add
`@cluster` when a multi-cluster env renders the same object to several
clusters. Forge refuses an ambiguous key and tells you which qualifier to
add — you never have to guess which it picked.

`Kind` is the Kubernetes kind, including a CRD's (`ClusterIssuer/letsencrypt`).
It is the kind of the object as RENDERED, which is the kind `forge env render
<env> --list` prints.

## Writing the patch

The value is a map of the fields to change. KCL's nested selector keeps a deep
field to one line:

```kcl
"Deployment/api" = {spec.replicas = 10}
```

A path segment that is not a bare identifier needs a map literal at that
level. Every label and annotation key is such a segment, because they contain
`/` and `.`:

```kcl
"Namespace/acme-dev" = {metadata.labels = {
    "pod-security.kubernetes.io/enforce" = "baseline"
}}
```

### Three worked examples

**Replicas on a workload's Deployment.** The Deployment does not appear in
your KCL — forge renders a `Workload` record and expands it — but it is the
object that gets applied, so it is the object you override:

```kcl
overrides = {
    "Deployment/api" = {spec.replicas = 10}
}
```

**Container resources, by container name.** A Deployment's pod template has
several containers (yours, plus any sidecar forge injects). Name the one you
mean and the patch merges into it; the others are untouched:

```kcl
overrides = {
    "Deployment/api" = {spec.template.spec.containers = [{
        name = "api"
        resources.limits.cpu = "500m"
        resources.limits.memory = "512Mi"
    }]}
}
```

You list only the container you are patching, and only the fields you are
changing. Its image, env, ports and probes survive, and so does every other
container. See *Patch semantics* for why.

**A Namespace PSA label.** Forge stamps every Namespace `restricted` on
enforce, audit and warn. A workload that needs, say, `NET_RAW` cannot start
under that, so relax the one level you need:

```kcl
overrides = {
    "Namespace/acme-dev" = {metadata.labels = {
        "pod-security.kubernetes.io/enforce" = "baseline"
    }}
}
```

Enforce becomes `baseline`; audit and warn stay `restricted`, so the violation
is still reported even though it no longer blocks. A label-map patch merges by
key — you are not restating the other labels, and you must not try to.

## Patch semantics

Overrides apply in Go, immediately after forge expands its `Workload` records
into Deployments, Services and the rest. That is why a Deployment is
addressable at all, and it means `forge env render`, `forge env deploy`,
`forge env shape` and the bundle all see post-override objects: there is one
hook, and everything downstream reads its output.

How a patch merges depends on the kind:

| Target | Patch | Lists |
|---|---|---|
| a built-in kind (Deployment, Service, Namespace, …) | strategic merge patch | merge by the kind's merge key — `containers` by `name`, `ports` by `port` |
| a CRD or an unknown kind | RFC 7386 JSON merge patch | replaced wholesale |

The strategic path is the reason the container example above is safe. Under a
plain JSON merge patch, a patch naming one container REPLACES the whole
`containers` list — silently deleting every sidecar and every field of your
own container that the patch did not restate. Forge uses the client-go scheme
to get the merge-key metadata for built-in kinds, so the patch lands on the
container you named.

A CRD has no such metadata available, so its lists are replaced. When
overriding a list field on a custom resource, restate the whole list.

**`None` deletes a field** under both:

```kcl
overrides = {
    "Deployment/api" = {spec.template.spec.containers = [{
        name = "api"
        resources.limits = None
    }]}
}
```

**Overrides are deterministic.** They apply in sorted key order, so the same
inputs produce byte-identical output regardless of how the render's map
iterated.

## Seeing what landed

`forge env render <env>` prints a summary of every override that applied, key
next to the object it resolved to:

```
[render]   overrides applied: 2
[render]     Deployment/api                       → Deployment/acme-dev/api
[render]     Namespace/acme-dev                   → Namespace/acme-dev
```

Read it. A partial key like `Deployment/api` is legitimate and unambiguous in
a single-namespace env, but the resolved object is the thing to check against
what you pictured.

## Every failure, and its fix

An override that does not resolve cleanly **fails the render**, non-zero, with
no manifests. That is deliberate: an override forge silently ignored would
deploy an environment that quietly lacks the change, and you would find out
from the symptom rather than from the render.

**The key is not an override key.**

```
Bundle.overrides keys ['api'] are not override keys. The grammar is
`Kind/name`, `Kind/namespace/name`, either with an optional `@<cluster>`
```

You wrote a bare name, or a lower-case kind. Add the kind: `Deployment/api`.
This one is caught by KCL at load, so it points at your declaration.

**The target does not exist.**

```
override "Deployment/ap" matches no rendered object. This env renders these
Deployment objects: Deployment/acme-dev/api, Deployment/acme-dev/worker
```

A typo, or the wrong kind. The message lists the candidates; `forge env render
<env> --list` prints everything the env renders.

**The key matches more than one object.**

```
override "Deployment/api" matches 2 objects (Deployment/acme-dev/api,
Deployment/acme-stage/api): add the qualifier that tells them apart — the
namespace (`Deployment/<namespace>/api`) or the cluster (`Deployment/api@<cluster>`)
```

Qualify it. Forge will not pick one for you: it has no way to know which you
meant, and picking wrong would patch an object you never looked at.

**Two overrides resolve to the same object.**

```
overrides "Deployment/acme-dev/api" and "Deployment/api" both resolve to
Deployment/acme-dev/api: one object takes one patch, so merge the two patches
into a single override
```

Usually a partial key and a qualified key for the same thing. Combine them
into one entry.

**The patch would change the object's identity.**

```
override "Deployment/api" sets metadata.name: an override changes FIELDS of
the rendered object, never its identity
```

`apiVersion`, `kind`, `metadata.name` and `metadata.namespace` are off limits.
Changing one would not override forge's object — it would add a SECOND object
alongside it, leaving the original in the stream. To target a different object,
change the override key.

**The target is a hosted workload.**

```
override "Deployment/api" targets "api", which runs on the HOSTED runtime: the
control plane renders its objects, not this env, so there is nothing here to
patch
```

A workload on `forge.OnHosted` renders no Kubernetes object in your env — the
control plane renders it, from the workload spec you published. There is no
local object to patch, and the platform does not accept object patches from a
bundle. Set the field on the workload declaration itself (where forge models
it), or bind the workload to `forge.OnCluster` if you need the object in your
own cluster, where you can override it.

This is not a restriction forge chose to add on top of hosted; it falls out of
hosted's shape. The control plane owns those objects, which is what makes a
hosted environment something it can safely admit.

## What this does to a release

The shape and the bundle are post-override, because both project from the same
post-override stream. So an override moves the object's hash in `forge env
shape <env>`, and a release sealed from that render carries the overridden
objects. That is the correct behavior — the shape describes what the deploy
will apply — and it means an override is part of the release, not a local
tweak applied on the way out.
