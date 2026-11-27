---
name: deploy/ml-stack
description: Early access, by request — declare the ML stack of a GPU cluster you run with forge.MLStack (forge.Adopt a component that already runs, forge.Install a pinned chart for what is missing, forge.Accelerators, forge.RecommendationDecision). Policy (discover, adopt, install only what is missing), what render refuses, and what is NOT built (the cluster discovery command, the console ML views, model releases).
---

# ML stack on a cluster you run (early access)

ML is **early access, by request**: it is switched on per organization and is not
generally available. Reliant provisions no GPUs and runs no training or
inference; everything is on a Kubernetes cluster the user operates, NVIDIA first.
Nothing is all-or-nothing: declare any subset of components, one is enough.

## Declare a stack

`forge.MLStack` goes in `Bundle.ml_stacks`, one per cluster. Only `cluster` is
required; every slot is optional and a slot left out renders nothing.

```python
ml_stacks = [forge.MLStack {
    cluster = forge.ClusterTarget {
        cluster = "gke_acme_us-central1_gpu", namespace = "forge-ml"
        platform = "amd64", connected_cluster = "acme-gpu"
    }
    observed = ["gpu-operator", "dcgm-exporter"]   # found running, not installed by forge
    observed_at = "2026-11-26"
    accelerators = [forge.Accelerators {
        vendor = "nvidia"
        metrics = forge.Adopt {
            kind = "dcgm-exporter", namespace = "gpu-operator", version = "3.3.9"
            selector = {"app": "nvidia-dcgm-exporter"}, port = 9400
            allow = ["DCGM_FI_DEV_GPU_UTIL", "DCGM_FI_DEV_FB_USED"]
        }
    }]
    queue = forge.Install {
        kind = "kueue"
        chart = forge.HelmChart {name = "kueue", oci = "oci://registry.k8s.io/kueue/charts/kueue",
                                 version = "0.14.4", namespace = "kueue-system"}
    }
    decisions = [forge.RecommendationDecision {id = "kubelet-tracing-off",
        reason = "GKE does not expose kubelet tracing config", expires = "2027-06-01"}]
}]
```

Slots: `accelerators` (a `forge.Accelerators` per vendor: nvidia, amd, tpu, neuron,
gaudi, generic; `resource` defaults per vendor, a generic one names its own),
`node_health`, `queue`, `training`, `serving`, `monitoring`, `pull_cache`. A
cluster that is a connected cluster needs `connected_cluster` (render says so).
Check with `forge env render <env>`.

## Policy: discover, then adopt, then install only what is missing

- `forge.Adopt {kind, namespace?, version?, note?, selector?, port?, allow?,
  service_name?}`: already runs; forge reads it, never upgrades or owns it. With
  `selector` and `port` it is also scraped (below).
- `forge.Install {kind, chart, reason?}`: forge installs a pinned chart on the
  stack's cluster. `delivery = "bundle"` (no CRDs: rendered into the env's bundle
  and pruned with it) or `"bootstrap"` (default; an operator with CRDs, applied
  with `forge env deploy <env> --target <chart name>`, kept out of the bundle).
  Every object is labelled `forge.dev/component-owner` / `forge.dev/component`.
- Render refuses: a kind both adopted and installed or installed twice; an
  Install of a kind in `observed` (two DCGM exporters break each other's
  profiling counters); two Accelerators of one vendor; a decision id twice; an
  Install chart bound to another cluster; KubeRay installed without a `reason`
  (adopt-only by policy).
- forge ships no per-vendor presets: metric allow lists and ports are the
  author's data.

## Scraping a component (released in v1.13.0)

`selector` (pod labels, all must match) plus `port` on an Adopt or Install lower,
when the env's observability is on, to a `forge.Telemetry`: service_name
`ml-<kind>` (suffixed with the cluster when an env has several stacks, or set
`service_name`), scope `platform`, the stack's cluster, `namespaces` = the
component's namespace (an Install's chart namespace), one metrics scrape on
`port` keeping only `allow` (a list of Prometheus names; unset keeps all). An
env-declared `Telemetry` with the same service_name replaces the generated one.
Render refuses `port` without `selector`, `allow`/`service_name` without `port`,
and a bad metric name. An Adopt with no port scrapes nothing.

**Preferred path is changing:** forge#3030 (open, being reworked) deprecates
these fields, keeping them working with a warning, in favour of
`Observability.telemetry`. Once it lands, write scrapes as `forge.Telemetry` in
the env's `observability` and treat Adopt as an inventory statement. Until then
the fields above are the released behaviour; do not tell a user they do not exist.

## Not built — do not promise

- A discovery command that inventories a cluster and proposes the ml.k
  (forge#2893, open). Until it merges there is no such command: write the stack
  by hand.
- The console ML views, access request UI, cluster inventory on the collector,
  model releases, previews, evals: design only (`docs/proposals/ml-workloads.md`,
  `reliant-for-ml.md`).

Public page: docs.reliantlabs.io/forge/ml/overview (early access).
