---
name: deploy/byo-yaml
description: Getting your own Kubernetes YAML into an env's bundle — forge.Manifests (literal objects), HelmChart delivery = "bundle" (your app's chart), forge.Generated (kustomize/jsonnet/any command that prints YAML).
---

# Bringing your own YAML

A real env deploys ONE bundle per env, applied by its reconciler or by forge.
Anything not in the bundle does not deploy there. Three ways to put your own objects in it, all
placed the same way (own `cluster`/`namespace`, stamped
`forge.dev/workload = <name>`, so `--target <name>` selects them and
`Bundle.overrides` can patch them). `forge env render`, `env shape`, a local
env's direct apply and the bundle all see the same objects.

## 1. Literal objects: `forge.Manifests`

```kcl
manifests = [forge.Manifests {
    name = "issuer"                    # optional: its own --target
    cluster = "k3d-dev"                # default: the primary cluster
    objects = [_cluster_issuer, _config_map]
}]
```

## 2. Your app's Helm chart: `HelmChart.delivery = "bundle"`

```kcl
helm_charts = [forge.HelmChart {
    name = "shop"
    repo = "https://charts.acme.dev"
    chart = "shop"
    version = "1.4.2"                  # pinned, like every chart
    namespace = "shop"
    values = {replicas = 3}
    delivery = "bundle"                # default "bootstrap"
}]
```

`helm template --skip-crds` runs at build time; the output ships in the
bundle. Default `"bootstrap"` is for PLATFORM charts (cert-manager, Envoy
Gateway, CNPG): installed by `forge env deploy <env> --target <name>`, kept OUT
of the bundle because a reconciler's prune would delete its CRDs and orphan
their data. A bundled chart may not set `crds`.

## 3. Anything that prints YAML: `forge.Generated`

```kcl
generated = [forge.Generated {
    name = "edge"                      # its --target
    command = ["kustomize", "build", "deploy/edge"]   # argv, not a shell string
    dir = "."                          # optional, relative to the project root
    cluster = "k3d-dev"                # optional, as Manifests
    namespace = "edge"                 # optional: for namespaced objects naming none
}]
```

stdout is parsed as a multi-document YAML stream. A non-zero exit, unparsable
YAML, or an object without `apiVersion`/`kind` fails the render naming the
generator. **The command must be deterministic**: the bundle digest is its
identity, so a timestamp or random value cuts a new bundle every build. Pin tool
versions and inputs.

## Hosted envs

An env with `forge.OnHosted` workloads refuses a bundled chart or a
`Generated` at render: the control plane admits only Workload, ManagedDatabase
and StaticSite there.
