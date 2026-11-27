# ML MVP: managed KServe, Kueue and KubeRay; model releases; training runs

**Status:** proposal. Nothing here is built. It narrows and re-orders the merged
ML proposals into a first product, following the owner's input of 2026-11-26
15:45Z (verbatim in the [spec's owner input log](ml-spec/README.md#owner-input-log)).
Its owner decisions 45–57 are settled on their recommended options
([section 7](#7-owner-decisions-settled-2026-11-26)), under one settled principle
above them all: [piecemeal opt-in](#settled-principle-piecemeal-opt-in).
Multi-cluster scheduling is [on hold](#5-build-order).
**Date:** 2026-11-26
**Narrows:** [reliant-for-ml.md](reliant-for-ml.md) (the north star; its §11 build
order is what this re-orders), [ml-workloads.md](ml-workloads.md) (the stack,
models, training templates, testing without GPUs) and the
[ML product spec](ml-spec/README.md). They are not repeated here. Where this
document changes one of them, it says so in [section 0](#0-what-this-changes).
**In flight beside it:** `forge.MLStack` (forge#2887), `forge ml discover`
(forge#2893), the Phase A console views (reliant#3726, control-plane#3772),
multi-cluster scheduling (forge#2994, held by the owner on 2026-11-26; it also
records the fencing design this document lands first, inside one cluster), and
the owner's "take your recommendations on all" record
(forge#2989), which settles ml-workloads decisions 1–22.

## Summary

The owner's order: **KServe and vLLM for serving first. Kueue when teams or batch
jobs compete for GPUs. KubeRay when a team writes distributed Python or scales the
stages of an inference pipeline independently. Kueue and Ray coexist, because
they make different decisions.** And all three are managed the way Reliant
already manages standard parts of an app: Zitadel, the OTel pipeline, CNPG, Valkey
(in flight) and CronJobs.

So the MVP is three things, in this order:

1. **Managed ML add-ons.** `forge.KServe`, `forge.Kueue`, `forge.KubeflowTrainer`
   and `forge.KubeRay`, each declared on its own in an env's `forge.MLStack` (a
   stack of one is valid). Each one adopts the
   install the cluster already runs, or installs forge's tested, pinned version.
   Each also carries its telemetry, dashboards and alerts by default, and is
   upgraded by a reviewed KCL diff. Reliant hosting has no GPUs, so "hosted" means
   the operator runs in the customer's cluster under forge's lifecycle, and the
   control plane hosts the management layer around it
   ([section 1](#1-managed-ml-add-ons)).
2. **A model release.** It records the weights, tokenizer and preprocessing, the
   serving image and the input/output contract, on the existing release ledger. It
   is rendered as a KServe InferenceService that pulls the weights by digest. It is
   judged before promotion by an eval gate whose verdict is per segment, and after
   promotion by `forge obs check` on inference SLIs. Every request trace names the
   model digest ([section 2](#2-the-model-release)).
3. **A training run.** It is recorded as a run with attempts, runs as a TrainJob
   under Kueue, publishes a checkpoint only when every shard is durable, fences
   stale attempts, and resumes from the last published checkpoint. A dataset is a
   reference, a digest and a region, nothing more ([section 3](#3-the-training-run)).

The acceptance test is a golden-path demo that needs no GPU spend: two k3d
clusters with fake GPUs, CPU vLLM serving `facebook/opt-125m` through KServe, and a
CPU torch TrainJob ([section 4](#4-the-pr-fast-path-and-the-golden-path-demo)).

Datasets as a catalog, sweeps, pipelines, labeling and a feature store are out of
v1 ([section 5](#5-build-order)).

"In this order" is the build order, not an adoption order. A team can take any
one of the three, or any single piece of one, without the others
([piecemeal opt-in](#settled-principle-piecemeal-opt-in)).

## Settled principle: piecemeal opt-in

The owner, 2026-11-26: "We are piecemeal opt-in. Nothing is all or nothing users
can opt into certain features without taking others."

**Every ML capability is independently adoptable.** Managed KServe, managed
Kueue, managed Kubeflow Trainer, managed KubeRay, the add-ons' telemetry, the
cluster collector's Kubernetes inventory, `forge ml discover`, `forge.ModelService`, model releases, eval gates,
training-run records, the Python SDK, and recommendations and remediation: none
requires another, unless there is a hard technical dependency, and each such
dependency is named in the matrix below. Everything else in this document is
read through this rule:

- **`forge.MLStack` is only a grouping.** It gives the per-cluster checks
  (never adopted and installed, never installed twice) one place to live. A
  stack holding one component is a valid stack. Nothing outside the stack
  requires one: `forge.ModelService`, a model release and an eval gate work with
  no `MLStack`, no Kubernetes inventory and no Kueue.
- **There is no ML-specific collector.** The owner, 2026-11-26: "Simpler is
  better, not sure if we need MLcollector when it's really just an Otel
  collector that can be used without that". Cluster inventory and events are a
  generic, opt-in capability of forge's existing node agent,
  `ObservabilityAgent.kubernetes_inventory` (forge#2998), useful to any app on a
  cluster. GPU metrics are a plain `forge.Telemetry` scrape of the DCGM exporter
  in the env's `observability.telemetry`, and `forge.Adopt` records that the
  exporter runs. `Adopt`'s own scrape fields (`selector`, `port`, `allow`,
  `service_name`) shipped in v1.13.0, so they keep working and are deprecated,
  not removed (forge#3030).
- **forge never adds an add-on the file does not name.** "When Kueue is
  installed" ([decision 55](#7-owner-decisions-settled-2026-11-26)) means when
  the stack declares `forge.Kueue`, or a person accepts the finding's KCL diff
  that declares it. A template that names a queue on a cluster with no Kueue,
  declared or adopted, is a render error that names the fix, never an implicit
  install.
- **A missing piece narrows what is reported; it is not an error.** Discovery,
  the Kubernetes inventory, the default telemetry and the recommendations report on what
  is there. A cluster with only KServe gets KServe's findings and nothing about
  queues.
- **Integrations switch on when both sides are present.** Kueue's TrainJob and
  RayJob integrations, the queue label on a TrainJob, Ray admission through
  Kueue: each is configured only where both components are declared or adopted.
- **The golden-path demo uses everything together** because it is the
  acceptance test of the whole path. Each slice's own proof
  ([section 6](#6-rollout)) runs with only that slice's capability.
- **Code that couples two capabilities without a hard dependency named here is
  a bug** against this principle, fixed in the code rather than documented.

### Opt-in matrix

"Needs" lists only hard technical dependencies. Every capability also needs what
any forge project needs (a `forge.yaml`, an env) and, for anything rendered into
a cluster, a `forge.ClusterTarget`.

| Capability                                         | Needs                                                                                                                                                                                                                                                                                      | Works without                                                                                                                                                      |
| -------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Managed KServe (`forge.KServe`)                    | cert-manager, adopted or installed (KServe's webhooks need it)                                                                                                                                                                                                                             | Kueue, Trainer, KubeRay, the collector, `forge.ModelService` (customer InferenceServices in `forge.Manifests` keep working), model releases, the control plane     |
| Managed Kueue (`forge.Kueue`)                      | Nothing else (cert-manager optional)                                                                                                                                                                                                                                                       | KServe, Trainer, KubeRay (their integrations stay off), the collector, training-run records, the control plane                                                     |
| Managed Kubeflow Trainer (`forge.KubeflowTrainer`) | Nothing else (it manages its own certificates)                                                                                                                                                                                                                                             | Kueue (a TrainJob runs unqueued, decision 55), KServe, KubeRay, training-run records, the control plane                                                            |
| Managed KubeRay (`forge.KubeRay`)                  | Nothing else (webhooks off by default)                                                                                                                                                                                                                                                     | Kueue (Ray admission through Kueue is optional), KServe, Trainer, the control plane                                                                                |
| Kubernetes inventory and add-on telemetry          | Off until declared (`ObservabilityAgent.kubernetes_inventory`, a generic observability field, not part of `MLStack`). forge's node agent on the cluster and an observability destination; a telemetry pipeline (forge's, or the customer's OTLP endpoint) for the add-ons' default scrapes | `forge.MLStack`, every managed add-on (it reports what it finds), the SDK, model releases, training-run records                                                    |
| `forge ml discover`                                | Read access to the cluster                                                                                                                                                                                                                                                                 | `forge.MLStack` (it can write the first one), any add-on being present, the collector, the control plane                                                           |
| `forge.ModelService`                               | A KServe install serving the `v1beta1` InferenceService CRD (managed, adopted, or the customer's own, checked by the cluster preflight); a `forge.Model` whose digest it renders                                                                                                           | `forge.MLStack`, managed KServe, Kueue, Trainer, KubeRay, the collector, eval gates, training runs, the control plane                                              |
| Model release                                      | forge's release ledger (`forge env build --release`) and a `forge.Model`; for OCI loading, the modelcar binding of section 2.2                                                                                                                                                             | `forge.ModelService` (`forge.Manifests` with `release_inputs` takes the digest), `forge.MLStack`, any add-on, eval gates, training runs (`source_run` is optional) |
| Eval gate (`forge.EvalSuite`)                      | A candidate and a baseline endpoint, a versioned eval set, a harness workload. As a promotion gate, a `DeliveryPolicy.required_gates` entry; `forge ml eval check --json` alone runs in any CI                                                                                             | KServe, `forge.ModelService` (any endpoint is judged), `forge.MLStack`, Kueue, training runs, the collector                                                        |
| Training-run records                               | The control plane's run ledger (decision 25 of reliant-for-ml), so a Reliant org with the ML entitlement; a `forge.Training` template, or an observed orchestrator CR                                                                                                                      | Kueue, Trainer (a plain `Job` or JobSet runs too), KServe, the SDK (a forge-rendered sidecar reports checkpoint durability), model releases, the collector         |
| Python SDK                                         | An endpoint to report to: the control plane for run records, or any OTLP collector for spans and metrics                                                                                                                                                                                   | A forge project, `forge.Training`, `forge.MLStack`, every add-on, the collector                                                                                    |
| Recommendations and remediation                    | The data each one reads (a recommendation with no source does not fire). Remediation in `act` mode also needs a declared `forge.Resilience` and a run record                                                                                                                               | Every capability it does not read; `recommend` mode needs no executor                                                                                              |

## 0. What this changes

| Merged proposal                                                                                                             | This document                                                                                                                                                                                                                                                                                                                                                                       |
| --------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| reliant-for-ml §11: phases B (fleet) → C (scheduling) → D (inference) → E (runs, E1–E5) → F (evals and models) → G          | Re-ordered: Fleet and Setup (in flight, kept) → **serving, model release and the eval gate** (D + F's model half, moved up) → **training runs** (C + E1, E4 and the in-cluster part of E5) → multi-cluster (forge#2994, on hold) → KubeRay. Observed runs, MLflow import, the SDK's breadth and resilience actions beyond resume stay where they were ([section 5](#5-build-order)) |
| ml-workloads §3.1: `MLStack` slots hold `Adopt \| Install`; the add-on's configuration is chart values                      | Adds typed **managed** components (`forge.KServe`, `forge.Kueue`, …) that lower to the same `Adopt` and `Install`, carrying forge's tested version, its configuration and its default telemetry ([section 1.2](#12-the-kcl-shape))                                                                                                                                                  |
| ml-workloads §3.1, "not proposed: typed wrappers for third-party CRDs"; a KServe InferenceService goes in `forge.Manifests` | Proposes one narrow forge-owned shape, `forge.ModelService`, that renders an InferenceService from a model release. `forge.Manifests` with `release_inputs` stays supported as the escape hatch. This is [decision 48](#7-owner-decisions-settled-2026-11-26)                                                                                                                       |
| ml-workloads §6 and decision 8: model splits through KServe `canaryTrafficPercent`                                          | KServe's canary is Serverless-only, and the MVP installs Standard mode ([decision 49](#7-owner-decisions-settled-2026-11-26)). So online comparison stays in-app Changes for v1                                                                                                                                                                                                     |
| ml-workloads decision 22, reliant-for-ml §6.2: eval results displayed first, gates later                                    | Unchanged as a default. The MVP builds the gate itself (a `required_gates` entry, opt-in per env) and makes the verdict **per segment**, because the golden path needs a failing case ([section 2.5](#25-the-eval-gate-and-the-segment-verdict))                                                                                                                                    |
| spec 00 and reliant-for-ml §2: datasets as versioned lineage entities; §6.4 data validation                                 | v1 records only a dataset reference, a digest or snapshot id, and a region on the run. No catalog, no validators ([section 3.4](#34-datasets-a-reference-not-a-catalog))                                                                                                                                                                                                            |
| forge#2994 §8.4: attempt write fencing for a cross-cluster resume                                                           | The same mechanism lands first inside one cluster, with the training-run slice ([section 3.3](#33-checkpoints-fencing-and-resume)). forge#2994 would extend it across clusters; multi-cluster is on hold                                                                                                                                                                            |

Everything else in those documents stands, including the settled decisions
(1–32, and 1–22 as recorded by forge#2989).

## 1. Managed ML add-ons

### 1.1 How Reliant manages standard parts of an app today

The owner asked for Kueue, Ray and KServe to be managed "similar to how we host
standard parts of a user's app". Each of those parts follows the same shape: a KCL
type declares it, and the env's runtime binding decides who runs it. On the
customer's cluster, forge renders it (or the operator's resources). On Reliant
hosting, the control plane converges it onto platform-run infrastructure.
Adoption is always declared, never guessed.

| Part                                                   | KCL type                                                                                                                                | On the customer's cluster (`forge.OnCluster`)                                                                                                                                             | On Reliant hosting (`forge.OnHosted`)                                                                                                                                                                                                                                            | Operator lifecycle                                                                                                                                          | Adopt if present                                                                                                                                                             | Its own telemetry                                                                                                        | Billing                                                                                                                                                                                                                            |
| ------------------------------------------------------ | --------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Identity (Zitadel)                                     | `forge.ManagedIdentity` (`kcl/schema.k:8735`); `runtime` is `OnHosted` or `IdentityOnHost` (`:8712`, dev)                               | **No arm.** forge never renders Zitadel into a customer cluster                                                                                                                           | One shared, platform-run Zitadel, an instance per env, converged by control-plane `internal/identityreconcile` (`reconciler.go:53`); consumers get `forge-identity-<name>` Secrets                                                                                               | The control plane pins the image (`deploy/kcl/lib/infra.k:1586`, held equal to forge's dev pin in `internal/hostinfra/zitadel.go:101`); upgrades by runbook | Control-plane config `identity.adopt` binds an existing instance (`internal/identityreconcile/adopt.go:16`)                                                                  | Reconciler metrics; `IdentityAdvisoryOpen` for advisories on the running version (`deploy/kcl/lib/selfmon_alerts.k:106`) | No price found                                                                                                                                                                                                                     |
| Telemetry (OTel)                                       | `forge.Observability` (`kcl/schema.k:8101`), `forge.Telemetry` (`kcl/telemetry.k:93`), `forge.ObservabilityAgent` (`kcl/schema.k:7838`) | forge renders its collector DaemonSet as manifests (`pkg/deploy/observability.go:646`), image pinned by digest (`kcl/base.k:117`)                                                         | Nothing from forge; the platform owns the endpoint (control-plane `internal/proxy/otel_ingest.go`)                                                                                                                                                                               | Pin moves with forge releases                                                                                                                               | Declared: `forge.TelemetryCollector` (`kcl/telemetry.k:490`) replaces forge's agent                                                                                          | `Telemetry.isolated` gives the pipeline its own bounded queue                                                            | 10 GiB a month included, then $0.10/GiB (control-plane `internal/plansconfig/pricing.yaml:463-468`)                                                                                                                                |
| Postgres (CloudNativePG)                               | `forge.ManagedDatabase` (`kcl/schema.k:1251`), `runtime = OnCluster \| OnHosted`                                                        | forge renders a CNPG `Cluster` with roles and Barman backups (`pkg/deploy/render.go:484`). **The customer installs the CNPG operator**; forge neither installs nor discovers it           | A dedicated CNPG Cluster per tenant namespace, NetworkPolicy-isolated, with scheduled backups (control-plane `internal/operators/manageddatabase/controller.go:245`)                                                                                                             | The control plane pins the operator (`deploy/kcl/lib/cnpg_vendored.k:82`); upgrades ship with the release train                                             | None                                                                                                                                                                         | Default `managed_database_telemetry` scrape, check and dashboard (`kcl/database_telemetry.k:68`)                         | Storage metered (control-plane `internal/billing/inframeter/sweeper.go:118`); hosted compute refused without a plan (`internal/computegate/gate.go:165`); the CLI exits 7 with a billing link (`internal/cli/promote_plan.go:616`) |
| Key-value store (Valkey)                               | In flight: `ManagedKeyValueStore` wire types (forge#2987); the KCL surface follows                                                      | Planned: a forge-rendered StatefulSet, no operator                                                                                                                                        | Planned: a control-plane operator (data-tier placement shared first, control-plane#3790)                                                                                                                                                                                         | forge owns the render                                                                                                                                       | —                                                                                                                                                                            | Planned                                                                                                                  | One sizing table shared by render, admission and billing (forge#2987)                                                                                                                                                              |
| Scheduled jobs (CronJob)                               | `forge.Workload {kind = "cron", schedule}` (`kcl/workload.k:751`)                                                                       | A Kubernetes CronJob (`pkg/deploy/workload_render.go:713`); host and compose refuse cron                                                                                                  | The workload operator renders through the same forge renderer (control-plane `internal/operators/workload/decorate.go:411`). Whether hosted admission accepts cron end to end is unverified; the documented hosted pattern is a schedule inside a worker                         | Kubernetes' own controller                                                                                                                                  | —                                                                                                                                                                            | Workload summary per kind                                                                                                | As compute                                                                                                                                                                                                                         |
| **Proposed: KServe, Kueue, Kubeflow Trainer, KubeRay** | `forge.KServe`, `forge.Kueue`, `forge.KubeflowTrainer`, `forge.KubeRay` in `forge.MLStack` slots ([section 1.2](#12-the-kcl-shape))     | **forge installs and upgrades the operator** (a pinned chart, labelled as forge's), or adopts the one found, and renders the add-on's own resources (queues, runtimes, InferenceServices) | No operator on hosting in v1: hosting has no GPUs. The control plane hosts the management layer: version advisories, telemetry, dashboards and alerts, the run and release ledgers, launch budgets and the ML entitlement ([section 1.4](#14-what-hosted-means-for-each-add-on)) | forge's tested version table; bumps proposed as a reviewed KCL diff ([section 1.5](#15-lifecycle-and-upgrades))                                             | **Yes, from the file**: `forge ml discover` records what runs (`MLStack.observed`, forge#2887), and `mode = "auto"` adopts an observed kind rather than install a second one | A default scrape, dashboard and alerts per add-on, like `managed_database_telemetry` ([section 1.6](#16-telemetry))      | No separate charge while ML is by request ([section 1.7](#17-billing-and-gating))                                                                                                                                                  |

Two lessons from the table shape the proposal:

- **CNPG on a customer cluster is the gap to close.** There, forge renders the
  `Cluster` but leaves the operator to the customer. For ML add-ons the operator
  _is_ most of what the customer wants managed, so forge installs and upgrades it,
  through the `Install` lowering forge#2887 already has.
- **Zitadel is the precedent for "hosted" meaning "run by the control plane".**
  That cannot apply to a GPU operator while Reliant hosting has no GPUs. What can
  be hosted is everything around the operator.

### 1.2 The KCL shape

`forge.MLStack` (forge#2887) stays the umbrella: one per connected cluster, with
slots, and the checks that a kind is never both adopted and installed, never
installed twice, and never installed when discovery observed it. The managed
add-ons are typed components in its slots, a third member of the component union
beside `Adopt` and `Install`. Each **lowers to `Adopt` or `Install`**, so nothing
new reaches the cluster that #2887 cannot already deliver.

The umbrella is **only a grouping**. Every slot is optional, and a stack that
declares one component is complete: a team that wants managed Kueue and nothing
else writes this, and gets Kueue, its queues and its telemetry, with no KServe,
no Trainer, no cluster inventory and no other add-on rendered:

```python
ml_stacks = [forge.MLStack {
    cluster = _gpu
    queue = forge.Kueue {version = "0.20.8", queues = [forge.Queue {name = "team-a", flavor = "l4", quota = {"nvidia.com/gpu": 8}}]}
}]
```

The fuller example below declares every add-on only to show each slot:

```python
import forge

_gpu = forge.ClusterTarget {cluster = "gke_acme_us-central1_gpu", namespace = "ml", connected_cluster = "gpu-prod"}

ml_stacks = [forge.MLStack {
    cluster = _gpu
    observed = ["dcgm-exporter"]                       # written by forge ml discover
    accelerators = [forge.Accelerators {vendor = "nvidia", metrics = forge.Adopt {kind = "dcgm-exporter"}}]

    # 1. Serving, first.
    serving = [forge.KServe {
        version = "0.21.0"                             # forge's tested pin, written explicitly
        runtimes = ["vllm"]                            # ServingRuntimes forge declares, images by digest
    }]
    cert_manager = forge.Adopt {kind = "cert-manager"} # KServe needs it; Install when absent

    # 2. When GPUs are contended.
    queue = forge.Kueue {
        version = "0.20.8"
        integrations = ["trainjob", "rayjob"]
        queues = [forge.Queue {name = "team-a", flavor = "l4", quota = {"nvidia.com/gpu": 8}}]
    }
    training = [forge.KubeflowTrainer {version = "2.4.0"}]

    # 3. For distributed Python, later.
    distributed = [forge.KubeRay {mode = "adopt"}]     # adopt-only by policy (forge#2887)
}]
```

Every managed component has the same fields, and the same lowering:

| Field       | Meaning                                                                                                                                                                                                                                                                      |
| ----------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `mode`      | `"auto"` (default): adopt if the kind is in `MLStack.observed`, else install. `"adopt"`: never install; render refuses if the kind is not observed and says to run `forge ml discover`. `"install"`: install; refused when observed, as #2887 already refuses                |
| `version`   | The chart and image version. The scaffold and `forge ml discover` write forge's tested pin explicitly, so a forge upgrade never changes it silently ([decision 47](#7-owner-decisions-settled-2026-11-26)). Ignored on adopt, where the observed version is recorded instead |
| typed knobs | The few settings forge must own to make the rest work (KServe's deployment mode and OCI model loading, Kueue's integrations and queues). A short list, not a mirror of the chart                                                                                             |
| `values`    | Chart values passed through for everything else: the 20% escape hatch                                                                                                                                                                                                        |
| `telemetry` | Defaults on (`default_telemetry = True`, as `forge.ManagedDatabase` has); overridable per field                                                                                                                                                                              |

What each one installs and configures:

| Add-on                  | Install lowering (pinned chart, `delivery = "bootstrap"`, it has CRDs)                                                        | What forge sets, and why                                                                                                                                                                                                                                                                          | Resources forge renders on top                                                                                                                             |
| ----------------------- | ----------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `forge.KServe`          | `oci://ghcr.io/kserve/charts`: the CRD chart, then the resources chart                                                        | **Standard** deployment mode (no Knative or Istio; the upstream default is still Serverless) and **OCI model support** on (`enableOciModelSupport`, `ociModelMode = "modelcar"`; the raw ConfigMap defaults both off, the Helm values turn modelcar on). Needs cert-manager, adopted or installed | A `ServingRuntime` per declared runtime (vLLM first), and an InferenceService per `forge.ModelService` ([section 2.3](#23-rendering-the-inferenceservice)) |
| `forge.Kueue`           | `oci://registry.k8s.io/kueue/charts/kueue`                                                                                    | The integrations the stack uses (TrainJob, RayJob, RayCluster, plain Job), `waitForPodsReady` for gang admission, the metrics endpoint. cert-manager optional                                                                                                                                     | `ResourceFlavor`, `ClusterQueue` and `LocalQueue` from `queues`; the priority classes spares need (reliant-for-ml decision 29)                             |
| `forge.KubeflowTrainer` | `oci://ghcr.io/kubeflow/charts/kubeflow-trainer` (manages its own certificates)                                               | Nothing beyond the defaults                                                                                                                                                                                                                                                                       | A `ClusterTrainingRuntime` for the torch runtime the templates use; TrainJobs per run ([section 3.2](#32-what-a-run-renders))                              |
| `forge.KubeRay`         | `https://ray-project.github.io/kuberay-helm/`, `kuberay-operator`; **adopt-only by policy**: Install needs a `reason` (#2887) | Webhooks off by default (they would need cert-manager)                                                                                                                                                                                                                                            | Later: RayJob for distributed Python, RayService for independently scaled inference stages, each admitted by Kueue when both are present                   |

Chart locations, versions and the cert-manager needs above are as read from the
projects' repositories at their latest tags on 2026-11-26 (KServe v0.21.0, Kueue
v0.20.8, Trainer v2.4.0, KubeRay v1.8.0). KubeRay's OCI chart location is
unverified.

**Why the umbrella, and not first-class Bundle kinds** like `Bundle.managed_databases`
([decision 45](#7-owner-decisions-settled-2026-11-26)): the add-ons share one cluster.
The one hard dependency among them is KServe's on cert-manager; Kueue's TrainJob
and RayJob integrations are not dependencies, they switch on only where Trainer or
KubeRay is also present. The checks that prevent a duplicate install, and the
inventory they read, are per cluster and already live on `MLStack`. Bundle-level
kinds would need each to name its cluster again and would lose those checks. The
umbrella is a grouping, not a bundle: declaring one add-on in it takes nothing
else. The cost is that `ManagedDatabase` and these differ in shape; the shared
idea is the runtime split, which the umbrella still has through `MLStack.cluster`.

**Why typed components, and not `Install` with chart values:** a bare `Install`
cannot carry forge's knowledge of which settings make the rest work. Without
`forge.KServe`, the model release in section 2 would depend on the customer having
turned on OCI model support and Standard mode by hand. These types configure the
add-on install. They do not mirror a third-party CRD: an InferenceService is still
KServe's, rendered from a forge shape that is not a copy of it (section 2.3).

### 1.3 Adopt if present

Adoption follows the settled rule (ml-workloads Settled 5): discover, then adopt,
then install only what is missing and agreed. The decision is made from the file,
not from a live read at render. Render stays a pure function of the repository.

- `forge ml discover` (forge#2893) writes `observed` and the versions it found.
- `mode = "auto"` then lowers to `Adopt` for an observed kind. It renders nothing
  for the operator, scrapes it, and records an inventory assertion.
- forge renders the add-on's _own_ resources (queues, runtimes, InferenceServices)
  against an adopted install too, after a preflight check that the CRD versions it
  renders are served. The check reuses the cluster preflight's served-kinds lookup
  (`internal/cluster/preflight.go:1653`). An adopted KServe in Serverless mode with
  OCI support off is reported as a finding with the exact setting to change. forge
  never reconfigures an install it did not make.
- Where the env turns on the cluster collector's Kubernetes inventory
  (`ObservabilityAgent.kubernetes_inventory`, ml-workloads §3.2), it keeps
  `observed` honest after the first look: a kind that appears later raises
  `ml-addon-installed-twice` before the next deploy installs over it. The
  inventory is its own opt-in, not part of `MLStack`; without it, re-running `forge ml discover` and the
  deploy preflight's served-kinds check do the same job at deploy time.

### 1.4 What "hosted" means for each add-on

Reliant hosting has no GPUs, and we never provision them (ml-workloads Settled 1).
So for v1, "managed" means **forge runs the operator's lifecycle in the
customer's cluster, and the control plane hosts what surrounds it**:

| Add-on            | In the customer's cluster                                                | Hosted by the control plane                                                                                                                                     | A hosted runtime later?                                                                                                                                                                                            |
| ----------------- | ------------------------------------------------------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| KServe + vLLM     | The operator, the ServingRuntimes, the InferenceServices, the model pull | The model release ledger; the weights in Reliant's registry (ml-workloads Settled 7); release health and `obs check`; dashboards and alerts; version advisories | Possible for small CPU models: a platform-run KServe, like the platform-run CNPG operator, serving tenant InferenceServices in tenant namespaces. Not in v1 ([decision 46](#7-owner-decisions-settled-2026-11-26)) |
| Kueue (+ Trainer) | The operator, the queues and flavors, admission                          | Queue and admission telemetry (the Queues view), the training-run ledger, launch budgets and approvals (reliant-for-ml decision 28), version advisories         | No: there is nothing to queue on hosting. The multi-cluster manager is a separate question, on hold with the rest of multi-cluster (forge#2994, its decision 34)                                                   |
| KubeRay           | Adopted, as today                                                        | Telemetry and run records for RayJobs                                                                                                                           | No                                                                                                                                                                                                                 |

`MLStack.cluster` is a `ClusterTarget`. A hosted runtime, if it comes, is an
additive arm on that binding, as `OnHosted` is on `ManagedDatabase`. Nothing in v1
has to change for it.

### 1.5 Lifecycle and upgrades

- **forge publishes a tested-version table**, one row per add-on: the chart,
  version, image digests, the Kubernetes range it was tested on, and the CRD
  versions it stores. It is data in the forge library, like
  `OTEL_COLLECTOR_IMAGE` (`kcl/base.k:117`). The k3d e2e of section 4 is what
  "tested" means.
- **The version lives in the customer's `ml.k`.** An upgrade is a recommendation,
  `ml-addon-outdated`, that proposes the version bump as a KCL diff (a PR from the
  console, or `forge ml discover --write`). It is reviewed, merged and delivered
  by Flux like any change (ml-workloads P1). forge never upgrades an operator
  because the customer upgraded forge.
- **CRDs go first.** KServe ships its CRDs as a separate chart; forge orders the
  CRD chart before the operator and refuses, at preflight, an upgrade that drops a
  CRD version objects are still stored at. Whether each chart's CRDs upgrade in
  place under Helm needs checking per chart (verify).
- **Removal.** An install removed from the file is pruned by Flux (#2887). A
  disconnect removes forge-labelled components only (ml-workloads decision 10,
  settled by forge#2989).
- **Advisories.** The control plane alerts on a security advisory for an
  installed add-on version, as `IdentityAdvisoryOpen` does for Zitadel.

### 1.6 Telemetry

Each managed add-on carries a default `forge.Telemetry`, as
`forge.ManagedDatabase` carries `managed_database_telemetry`: a scrape with an
`allow` list, a check, a dashboard and default alerts. These are defaults of a
component forge installs, not presets for systems forge does not know, so the
third-party-telemetry rule of "no presets" is not bent (and the accelerator
adapters are already the settled exception, ml-workloads decision 3).

The default telemetry is per add-on and travels with it: managed Kueue alone
brings Kueue's scrape and alerts, and nothing else. It needs a telemetry
pipeline, not the cluster inventory, and `telemetry` turns it off for a team
that already watches the operator another way. The Kubernetes inventory is a
separate, generic observability opt-in that declaring an add-on never switches on.

| Add-on      | Scraped (exact names verify against the pinned version)                                                                                          | Default alerts                                                                  |
| ----------- | ------------------------------------------------------------------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------- |
| KServe/vLLM | vLLM's `vllm:*` engine metrics (requests running and waiting, TTFT, inter-token latency, KV-cache use), the KServe controller's reconcile errors | InferenceService not ready; KV cache saturated; queue wait above baseline       |
| Kueue       | Kueue's admission, pending-workload and quota metrics                                                                                            | Workloads pending past a threshold; a ClusterQueue at quota with preemption off |
| Trainer     | Controller reconcile errors; TrainJob status from the cluster collector's watch                                                                  | TrainJob failed; stuck in `Created`                                             |
| KubeRay     | The operator's metrics and Ray's own                                                                                                             | RayCluster unhealthy                                                            |

Request traces from vLLM, if it exports OTLP spans on the pinned version (verify),
go through forge's node agent like any workload's.

### 1.7 Billing and gating

- **No separate charge for a managed add-on** while ML is by request. What the
  add-ons send is observability ingest, billed as today, with the free accelerator
  tier settled for the beta (ml-workloads decision 11). [Decision 56](#7-owner-decisions-settled-2026-11-26)
  asks whether that changes at GA.
- **Model storage** uses the separate `models/` allowance per plan (ml-workloads
  decision 13), and pull egress is metered first (decision 6).
- **The ML entitlement** gates the control-plane features (ledgers, findings,
  ML reads), never forge's types, render or `forge ml discover`
  (ml-workloads Settled 9).
- **No deploy exit 7** for anything here in v1: nothing lands on Reliant hosting.

## 2. The model release

### 2.1 What a model release is

A model release is a release on the existing ledger
(`pkg/release/release.go`) whose model artifacts changed. It answers "what is
serving this request?". Staging and prod promote the same release by digest.
Retraining is a new candidate release, even at the same commit, with the code
artifacts carried forward (`Artifact.CarriedFrom`, ml-workloads decision 7).

| Component                   | Recorded as                                                                                                                                 | Notes                                                                                                                                                                                      |
| --------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Weights                     | An `oci` artifact (`KindOCI`, `pkg/release/release.go:43`): a ModelPack manifest, one layer per weight file                                 | ml-workloads §3.5. Safetensors preferred; pickle formats warn, then gate (reliant-for-ml decision 31)                                                                                      |
| Tokenizer and preprocessing | Layers of the same ModelPack (tokenizer files, generation config, chat template). Preprocessing **code** is in the serving image or the app | One digest covers the weights and everything that must change with them                                                                                                                    |
| Serving image               | An `oci` image artifact: the vLLM runtime image, pinned by digest, usually carried forward                                                  | The ServingRuntime references it by digest                                                                                                                                                 |
| Input/output contract       | A `file` artifact: the contract file's digest                                                                                               | A named built-in (`openai-chat`, `openai-completions`) or a JSON Schema / OpenAPI file in the repo ([decision 57](#7-owner-decisions-settled-2026-11-26)). The app's tests call against it |
| Lineage                     | On the model: the source run, attempt and checkpoint, the dataset reference and digest, the template's release and commit                   | `forge.Model.source_run` (ml-workloads §7), widened to the run record of section 3                                                                                                         |

**The Flux bundle stays small.** It carries the rendered InferenceService with an
immutable reference to the weights, never the weights. Checkpoints go to object
storage; a release is cut only when a checkpoint becomes a candidate
(`forge ml model push --from-checkpoint <run>@<step>`, a widening of ml-workloads'
`forge ml model push`, which converts a published checkpoint into a ModelPack).

### 2.2 Where the weights live

| Source                                                   | When                                                                                                      | Verified how                                                                                                                                                                                                                                                          |
| -------------------------------------------------------- | --------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **OCI, by digest** (default)                             | Reliant's registry under `models/` (ml-workloads Settled 7, workstream R1–R3), or the customer's registry | The ModelPack digest is the model identity, not a runnable image reference. Modelcar requires a separately recorded, digest-pinned image whose payload is verified against that ModelPack; release is blocked until the pinned KServe integration proves this binding |
| **Object storage with a verified digest** (escape hatch) | `s3://` / `gs://`, for teams whose weights cannot leave their bucket                                      | The version is the path plus a manifest of file SHA-256s (ml-workloads §3.5). forge renders a verify step that checks the downloaded files against the manifest before the server starts; how it attaches to KServe's storage initializer is unverified               |

The OCI loading mode is a KServe setting: `modelcar` (a sidecar image, works on
any containerd), `native` (a Kubernetes image volume) or `fetch` (an init
container). The MVP uses `modelcar` ([decision 50](#7-owner-decisions-settled-2026-11-26)).

**Modelcar identity is a release prerequisite, not a tag fallback.** A ModelPack
is an OCI artifact; its manifest digest does not by itself name a runnable
modelcar image. The release must separately record the ModelPack digest and the
modelcar image digest, with a verified binding between the image's model files
(weights, tokenizer and config) and the ModelPack's file manifest. The build
verifies that binding before publishing the candidate. Eval and production use
the same recorded image digest; `ml.model.digest` remains the ModelPack digest.

Before enabling model releases, an integration test against the pinned KServe
version must prove that the rendered `storageUri` results in a modelcar container
image reference of `registry/repository@sha256:<modelcar-image-digest>` and that
the served files match the recorded ModelPack. A digest-looking tag is still
mutable and is not accepted. Digest URI support is unverified today: if the
controller cannot preserve that immutable image reference, model release and
promotion fail closed until a verified immutable loading path exists. No
silent fallback to a tag, nor substituting the ModelPack digest as an image
digest, is allowed.

### 2.3 Rendering the InferenceService

`forge.ModelService` (the name is a sketch) is the one forge shape that renders an
InferenceService. It names a model and a runtime, and forge fills in the digest
from the release:

```python
models = [forge.Model {name = "support-assistant", contract = "openai-chat"}]

model_services = [forge.ModelService {
    name = "support-assistant"
    cluster = _gpu
    model = "support-assistant"                 # the release's digest is substituted at render
    runtime = "vllm"                            # a ServingRuntime: one forge.KServe declares, or one the cluster already has
    accelerators = tiers.Accelerators {count = 1, product = "nvidia-l4"}   # omitted on CPU
    replicas = 2
    args = ["--max-model-len=4096"]
    overrides = {}                              # a patch on the rendered InferenceService
}]
```

It renders:

- a `serving.kserve.io/v1beta1` InferenceService in Standard mode (the annotation's
  exact spelling after KServe's rename of RawDeployment is unverified), with
  `storageUri` set to `oci://registry/repository@sha256:<modelcar-image-digest>`
  from the release's verified ModelPack-to-image binding, **only after** the
  integration prerequisite in section 2.2 passes (not the ModelPack digest);
- the predictor's resources, accelerators and `/dev/shm` from ml-workloads §3.1;
- `OTEL_RESOURCE_ATTRIBUTES` carrying `forge.release`, `ml.model.name` and
  `ml.model.digest`, and the release labels forge stamps on every workload.

**Why a forge shape and not `forge.Manifests`** ([decision 48](#7-owner-decisions-settled-2026-11-26)):
the release has to know which serving resource carries which model, to substitute
the digest, tag the traces and judge the right SLIs. `forge.Manifests` with
`release_inputs` can substitute a digest, but the link from model to service is
then a string in a YAML file. `forge.ModelService` covers the common case with a
handful of fields and an `overrides` patch. It does not copy KServe's schema.
`forge.Manifests` stays supported for anything it does not cover, including
KServe's alpha `LLMInferenceService`.

`forge.ModelService` needs a KServe that serves the InferenceService CRD and
nothing else from this document. That KServe may be forge's managed one, one an
`MLStack` adopts, or one the customer runs that no `MLStack` mentions: the
cluster preflight's served-kinds check is what proves it is there. No Kueue, no
cluster inventory and no eval gate is involved, and the training-run lineage on the
model is filled in only when a run produced it.

### 2.4 Request traces name the model

Every serving span carries `ml.model.digest` and `forge.release` from its
resource attributes. The app's call into the model is the parent span through
`traceparent`. So one request links through to everything behind it:

```
app span ── serving span {ml.model.digest} ── model release ── eval verdict ── run, attempt, checkpoint ── dataset ref
```

That is the lineage the owner asked for ("prediction → rollout → eval → run"),
read as joins over data the ledgers already hold.

### 2.5 The eval gate and the segment verdict

An eval suite is the test suite of a model release (reliant-for-ml §6.2). The MVP
builds it as a gate in the existing promotion path, because the golden path needs
to fail one.

```python
evals = [forge.EvalSuite {
    name = "support-regression"
    eval_set = "oci://registry.reliantapi.com/acme/support/evalsets/support@sha256:…"   # versioned, by digest
    harness = forge.Workload {name = "support-eval", kind = "job", image = "…"}       # the customer's evaluator
    metrics = [forge.EvalMetric {name = "accuracy", max_regression = 0.01}]
    segments = [forge.EvalSegment {name = "billing-disputes", metrics = ["recall"], max_regression = 0.03, min_samples = 200}]
}]

delivery_policy = forge.DeliveryPolicy {
    required_gates = forge.RequiredGates {names = ["eval:support-regression"]}
}
```

- **The gate is the existing one.** `RequiredGates` (`kcl/schema.k:7095`) already
  refuses a promotion that does not carry a named gate as passed, and accepts a
  check's JSON verdict document as the gate. Naming the suite is the opt-in, per
  env, which keeps decision 22 (display first) as the default.
- **It judges any endpoint.** The candidate and the baseline are endpoints, so a
  model served by `forge.ModelService`, by the customer's own InferenceService or
  outside Kubernetes is judged the same way. The gate needs no KServe, no
  `MLStack` and no training run, and `forge ml eval check --json` runs in any CI
  without a forge promotion at all.
- **The run.** `forge ml eval run staging --suite support-regression` runs the
  harness as a Job in the env's cluster against the candidate's endpoint, on the
  versioned eval set ([decision 52](#7-owner-decisions-settled-2026-11-26)). The baseline is
  the current prod release's result **on the same eval-set digest**. When none
  is recorded, the harness runs against prod's model too.
- **The verdict.** `forge ml eval check staging --suite support-regression --baseline prod --json`
  compares candidate and baseline per item (paired), for the aggregate and for
  every declared segment. Its exit codes copy `forge obs check`: 0 passed, 1
  regressed, 2 not proven (a segment below `min_samples`). The JSON document is
  what `forge env deploy prod <v> --gate` carries.
- **A regression is statistical.** A metric regresses only when the drop exceeds
  both its confidence interval and the declared `max_regression`
  ([decision 51](#7-owner-decisions-settled-2026-11-26)). One failing segment fails the verdict.

The case the owner's notes name, as the verdict shows it:

| Scope                                | Metric   | prod (v13) | candidate (v14) | Δ      | 95% CI of Δ     | Verdict       |
| ------------------------------------ | -------- | ---------- | --------------- | ------ | --------------- | ------------- |
| All items (n = 5,120)                | accuracy | 0.874      | 0.876           | +0.002 | [−0.006, 0.010] | pass          |
| Segment `billing-disputes` (n = 412) | recall   | **0.91**   | **0.78**        | −0.13  | [−0.18, −0.08]  | **regressed** |

Aggregate accuracy is flat, so an aggregate-only gate would ship it. The segment
verdict holds the promotion, and the failure carries:

- **examples**: the items that flipped from right to wrong, with candidate and
  baseline outputs. They are stored in the customer's bucket, because eval samples
  may hold customer data (reliant-for-ml §8);
- **the baseline**: v13, its model digest and its result on the same eval set;
- **the refs an agent needs**: the run, attempt and checkpoint behind v14, the
  template's commit and diff against v13's, and the dataset reference and digest.
  The `ml.eval.regressed` event carries them, and the typed `forge` trigger
  (reliant-for-ml decision 27) hands them to a workflow.

### 2.6 `forge obs check` on inference SLIs

After promotion, `forge obs check <env> --wait` judges the inference service as it
judges any release: server-span RED from the serving spans, plus metric findings
from the vLLM scrape. These are the metric findings ml-workloads §4 proposes for
`pkg/releasehealth`: TTFT p95, inter-token latency p95, queue wait and KV-cache
saturation, each against the previous release in the same env. `min_requests`
applies unchanged, so a quiet staging env exits 2, "not proven good". That is why
the eval gate is the quality proof before promotion and `obs check` is the runtime
proof after it.

### 2.7 Rolling forward, and rollback floors

Nothing rolls back on its own. A bad model is fixed by promoting a release that
carries the previous model digest, which is a new release with the old weights
(ml-workloads §7). The rollback floors forge already has (`pkg/release/floor.go`,
the `deploy/rollback-floor` skill) say which of those moves are safe, with no new
mechanism:

- **An order floor** when a model must never serve again:
  `forge env floor set prod v14 --reason "v13 leaks account ids"`.
- **A declared floor** when a model release changes something older releases
  cannot live with. An input/output contract change is a
  `deploy/rollback-floors/<id>.yaml` of kind `api`. A new embedding model whose
  index was rebuilt is kind `data`. Promoting a release without that declaration
  is then refused, `below_rollback_floor`.
- **Later:** an embedding model and its index promoted as one compatible pair. It
  is noted, not designed here.

## 3. The training run

### 3.1 What a run records

A training run answers "what produced these weights?". It is a recorded launch
against a declared `forge.Training` template (ml-workloads decision 5), stored in
control-plane Postgres per org (reliant-for-ml decision 25). The MVP records the
two levels the golden path needs, **run** and **attempt**, inside the hierarchy
the owner confirmed (project → experiment → run → phase → attempt → pod/rank →
step). A run with no declared phases has one implicit phase.

| Record     | Fields (sketch)                                                                                                                                                                                                                          |
| ---------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Run        | id (stable across clusters); template and its release and commit; trainer image digest; base model digest; dataset reference, digest and region; params; launcher (person, agent, workflow); state; outputs (checkpoints, model release) |
| Attempt    | `n`, the epoch; cluster and namespace; the orchestrator CR's uid; started and ended; end reason (closed set, spec 00 §2.4); first and last step; the checkpoint it resumed from                                                          |
| Checkpoint | step; attempt `n`; tier; manifest digest; `durable_at`; `published` (only a published checkpoint exists to a resume)                                                                                                                     |

The run's identity is the ledger's, not the CR's. When an attempt moves to another
cluster (forge#2994, on hold), it is the same run with a new attempt.

### 3.2 What a run renders

`forge.Training` (ml-workloads §3.1) with `orchestrator = "trainjob"` renders a
Kubeflow Trainer **TrainJob** against a `ClusterTrainingRuntime`: the one
`forge.KubeflowTrainer` installs, or one an adopted Trainer already has. When the
template names a Kueue LocalQueue, the TrainJob is labelled with it
(`kueue.x-k8s.io/queue-name`), so Kueue admits it as a gang; Kueue has a TrainJob
integration on the pinned version. A template that names no queue runs unqueued.
A plain `Job` stays available for the CPU smoke run and for clusters without
Trainer ([decision 53](#7-owner-decisions-settled-2026-11-26)). A run is recorded
the same way whichever of these runs it.
forge renders into the pod: `FORGE_RUN`, `FORGE_ATTEMPT`, `FORGE_CHECKPOINT_DIR`
(the attempt's own prefix) and `FORGE_RESUME_FROM` (the last published
checkpoint, empty on the first attempt).

Kueue arrives with training, not before: it is installed when the stack declares
it, which is when a team starts naming queues or accepts the
`gpu-jobs-bypass-queue` finding's KCL diff ([decision 55](#7-owner-decisions-settled-2026-11-26)).
forge never installs it as a side effect: a template that names a queue on a
cluster with no Kueue, declared or adopted, is a render error that names the fix.
Kueue decides who may run and when; Ray, when it comes, decides how a running
program spreads its work. That is why they coexist.

### 3.3 Checkpoints, fencing and resume

This is forge#2994 §8.4, landed first inside one cluster. The stale writer in one
cluster is real: a pod on a partitioned node that the kubelet has not killed, or
a worker still flushing after its JobSet or TrainJob restarted.

- **Every attempt has an epoch** `(run, phase, attempt n)` and writes under its
  own prefix, `…/attempt-<n>/step-<k>/`. Two attempts never write the same path.
- **Publish when durable.** Each shard writes its files, then the manifest is
  written last, listing every shard's digest. A checkpoint with no manifest does
  not exist. Where the store supports it, the manifest is a conditional create
  (`If-None-Match: *`), so a manifest is never overwritten (verify per store).
- **Publishing is a ledger write that checks the epoch.** The SDK, or a small
  forge-rendered sidecar for a team without the SDK, reports
  `checkpoint.durable`. The control plane accepts it for attempt _n_ only while
  _n_ is the run's current attempt. A fenced attempt's checkpoints are kept as
  orphans, never resumed from, and collected after a declared time.
- **Resume reads only published checkpoints.** A new attempt starts with
  `FORGE_RESUME_FROM` set to the newest published checkpoint, records it on the
  attempt, and `steps_lost` follows exactly (reliant-for-ml §4.3).
- **What recovers it in v1** is the full-restart tier (reliant-for-ml §4.6, tier
  C): the TrainJob or Job failure policy restarts the workers, or the ledger
  launches a new attempt. Replace-node recovery, spares and remediation stay in
  phase E5.

### 3.4 Datasets: a reference, not a catalog

A run records `forge.DatasetRef {uri, digest | snapshot, region}`. The digest is
whatever identifies the data immutably in the customer's system (an object
manifest's SHA-256, a lakeFS commit, an Iceberg snapshot id). Reliant stores the
reference, never the data. It is enough for lineage (which data made these
weights) and for placement locality if multi-cluster resumes (forge#2994, on
hold, scores data staging time by region). There is no catalog, no versioning service, no validators and no split
management in v1 ([decision 54](#7-owner-decisions-settled-2026-11-26)).

## 4. The PR fast path and the golden-path demo

### 4.1 The PR fast path

A pull request that changes training or serving code gets the normal tests
(coverage and flakes as for any code, [tests-coverage-flakes.md](tests-coverage-flakes.md)),
plus one fast path in its preview env, all on CPU:

1. **Train tiny:** a tiny model on a tiny data slice for a few dozen steps
   (`forge.SmokeRun`, reliant-for-ml §6.1): loss falls, values stay finite.
2. **Save and restore:** kill at step _k_, resume from the published checkpoint,
   assert the steps continue and the loss carries on.
3. **Export:** the checkpoint becomes a ModelPack, pushed to the preview's
   registry, and a candidate release is cut.
4. **Boot the real inference container:** the same ServingRuntime image as prod,
   through KServe, serving the exported model on CPU.
5. **Call it through the app's API:** the app's own scenario test, not a direct
   call to the model endpoint, so the contract is exercised where it is used.
6. **Judge:** golden outputs, the eval suite on its smoke slice, and
   `forge obs check` on the preview.

Results land as test results beside the unit tests. The target is ten minutes of
wall time (a target, not yet measured).

### 4.2 The golden-path demo: the MVP's acceptance test

The MVP is done when this runs end to end, in CI on CPU, and as a recorded demo.
It uses every capability at once because it is the whole path; no team has to
adopt them together, and each slice is also proven alone ([section 6](#6-rollout)).

| Step | What happens                                                                                                              | What proves it                                                                                                           | Slice |
| ---- | ------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------ | ----- |
| 1    | Change a training recipe in a worktree and open a PR                                                                      | —                                                                                                                        | —     |
| 2    | The PR fast path passes (4.1)                                                                                             | Smoke train, resume, export, KServe boot, app call, all green in Tests                                                   | S7    |
| 3    | Merge; dispatch the full run. It goes to the template's cluster (multi-cluster dispatch is on hold)                       | A run with attempt 1 in the ledger; a TrainJob admitted by Kueue on fake GPUs                                            | S5–S6 |
| 4    | Inject an interruption: delete a worker pod mid-run; separately, `SIGSTOP` attempt 1 and resume it after attempt 2 starts | Attempt 2 resumes from the last published checkpoint; the stopped attempt's publish is refused (fencing)                 | S6    |
| 5    | The run publishes a candidate: checkpoint → ModelPack → release with code carried forward                                 | An immutable release whose model digest is new and whose code digests equal v13's                                        | S2    |
| 6    | Staging serves it and evaluates it against prod on the same eval-set digest                                               | `forge ml eval check` exit 0, its verdict document                                                                       | S3–S4 |
| 7a   | **Pass:** `forge env deploy prod v14 --gate <verdict>`; Flux delivers; `forge obs check prod --wait`                      | Exit 0; prod traces carry the new `ml.model.digest`                                                                      | S2–S4 |
| 7b   | **Fail:** the same run with one segment's labels corrupted in its data slice                                              | Exit 1 on the segment; promotion refused `required_gate_failed`; flipped examples, baseline and run/code/data refs shown | S4    |

**The rig, with no GPU spend** (ml-workloads §13):

- **Two k3d clusters**, connected with `forge cluster connect`: `ml-a` (staging,
  and training) and `ml-b` (prod). Each runs run:ai's **fake-gpu-operator**, which
  advertises `nvidia.com/gpu` on CPU nodes; it is documented for Kubernetes
  generally, and running it on k3d specifically is to be confirmed (verify).
  Dispatching step 3 to either cluster waits on multi-cluster, which is on hold
  ([section 5](#5-build-order)).
- **Serving:** KServe in Standard mode with a vLLM CPU ServingRuntime
  (`vllm/vllm-openai-cpu`, per vLLM's docs) serving `facebook/opt-125m`. KServe
  ships no CPU vLLM runtime, so forge declares one (verify that it runs under
  KServe's modelcar as declared). If it does not, KServe's Hugging Face runtime on
  CPU is the fallback for the demo, without vLLM's metrics.
- **Training:** a CPU torch DDP TrainJob (gloo backend) requesting fake GPUs,
  admitted by Kueue.
- **Registry:** the k3d registry for the preview; Reliant's registry in the hosted
  dogfood.
- **Hub:** control-plane's dev stack, for the ledgers, gates and console.

## 5. Build order

This replaces reliant-for-ml §11's order for the MVP. The phases' content is
unchanged unless named.

| #   | Step                | What lands                                                                                                                                                                                                     | Its reliant-for-ml phase                   | State                                                            |
| --- | ------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------ | ---------------------------------------------------------------- |
| 0   | Access and the demo | The entitlement gate, request page and Phase A views on fixtures                                                                                                                                               | 0, A                                       | In flight (control-plane#3747, #3772; reliant#3693, #3726). Keep |
| 1   | Fleet and Setup     | `forge.MLStack` and `forge ml discover`; the cluster collector's opt-in Kubernetes inventory (forge#2998, generic observability); Fleet and Setup views on real data                                           | B                                          | In flight (forge#2887, #2893). Keep                              |
| 2   | **Serving**         | Managed `forge.KServe` and cert-manager; `forge.Model`, ModelPack push, the registry's R1–R3; `forge.ModelService`; traces tagged with the digest; inference release health; the eval gate and segment verdict | D, and F's eval-and-model half, moved up   | This proposal                                                    |
| 3   | **Training runs**   | Managed `forge.Kueue` and `forge.KubeflowTrainer`; `forge.Training` → TrainJob; the run and attempt ledger; checkpoint publish, fencing and resume in one cluster; `forge ml run` and `wait`; the PR fast path | C, E1, E4, and the full-restart part of E5 | This proposal                                                    |
| 4   | Multi-cluster       | MultiKueue and the open placement controller                                                                                                                                                                   | new                                        | **On hold** (owner, 2026-11-26; below)                           |
| 5   | KubeRay             | Managed `forge.KubeRay` (adopt by default); RayJob and RayService templates; Kueue admission of Ray workloads                                                                                                  | new                                        | Later                                                            |

**Multi-cluster is on hold (owner, 2026-11-26).** The owner held forge#2994:
"Either we need to push this to a separate repo or we need to instead create or
allow or hook into primitives that work with whatever the customer is already
using. Or we just hold on multicluster for foreseeable future." The three
options, with no recommendation asked for now:

1. **A separate repository.** The placement controller and its MultiKueue
   dispatcher live outside forge, as their own project.
2. **Hook into the primitives the customer already runs.** forge adopts the
   customer's MultiKueue, Karmada, SkyPilot or Armada, as it adopts their KServe
   or Kueue, rather than shipping a multi-cluster scheduler of its own.
3. **Hold indefinitely.** Nothing multi-cluster is built for the foreseeable
   future.

Until the owner picks one, step 4 does not start, and nothing in steps 0–3 or 5
waits on it. The in-cluster fencing of section 3.3 does not depend on it either.

The SDK track (reliant-for-ml phase B′) runs in parallel from step 2. The MVP
needs only its checkpoint-durability report and run and attempt tags.

**Why serving moves before training.** It is the owner's order, and it is the
shorter path to value: most teams serve before they train their own models, a
served model already exercises the release ledger, gates and `obs check`, and
those are what make a training run's output worth recording. Fleet and Setup stay
first because the add-ons are declared on the stack they introduce. The order is
what gets built first, not what a team must adopt first: a team can take step 2's
eval gate, or step 3's run records, without anything from step 1.

**Explicitly out of v1:**

- a dataset catalog, versioning service or validators (Label Studio, Great
  Expectations, Ray Data prep, lakeFS or Iceberg integrations, split management);
- sweeps (Katib, Ray Tune) and pipelines (Kubeflow Pipelines);
- labeling and a feature store (Feast), and data caching (Fluid);
- in-KServe canaries and router splits (Serverless only; ml-workloads decision 8
  stays (a) for v1);
- an operator on Reliant hosting (decision 46);
- replace-node recovery, spares and the remediation executor (phase E5 after the
  MVP), and phases and experiments beyond the implicit single phase;
- embedding-and-index pair promotion.

## 6. Rollout

Each slice ships alone, behind the ML entitlement where it touches the control
plane, and every forge piece works without the control plane (start anywhere).
Each is also adopted alone: its proof below runs with only that slice's
capability declared ([piecemeal opt-in](#settled-principle-piecemeal-opt-in)).
Each adds its fixtures to the demo in the same PR.

| Slice | Repo                          | What lands                                                                                                                                                                                                            | Proven by (k3d, CPU)                                                          |
| ----- | ----------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------- |
| S1    | forge                         | The managed-component union on `MLStack`; `mode`, the lowering to `Adopt`/`Install`, the tested-version table, default telemetry; `forge.KServe` with cert-manager; preflight CRD-version checks; `ml-addon-outdated` | Install on a bare cluster; adopt a planted install and refuse a second        |
| S2    | forge, control-plane          | `forge.Model` and `forge ml model push` (ml-workloads slice 4); registry R1–R3 (control-plane); `forge.ModelService` → InferenceService; the vLLM ServingRuntime; trace attributes                                    | opt-125m served on CPU from a pushed digest; the digest on every serving span |
| S3    | forge, control-plane          | Inference metric findings in `pkg/releasehealth`; previews that serve a candidate                                                                                                                                     | `obs check` passes a good release and fails a planted latency regression      |
| S4    | forge, control-plane, reliant | `forge.EvalSuite`, `forge ml eval run` and `check`, the segment verdict, results stored and shown in Tests, `ml.eval.*` events and the trigger                                                                        | Golden steps 6, 7a and 7b                                                     |
| S5    | forge                         | `forge.Kueue` and `forge.KubeflowTrainer`; `forge.Training` → TrainJob or Job; queues from KCL                                                                                                                        | A TrainJob on fake GPUs admitted as a gang                                    |
| S6    | control-plane, forge          | The run, attempt and checkpoint ledger; publish, fencing and resume; `forge ml run`, `wait`, `runs get`; `forge ml model push --from-checkpoint`                                                                      | Golden steps 3–5, including the fenced stale attempt                          |
| S7    | forge                         | `forge.SmokeRun` in previews: the PR fast path                                                                                                                                                                        | Golden step 2                                                                 |
| S8    | all                           | The golden path as one e2e, and the recorded demo                                                                                                                                                                     | The whole table in 4.2                                                        |

S1 and the registry work of S2 can start together. S5 can start beside S2; S6
needs S5, and S4 needs S2.

## 7. Owner decisions (settled 2026-11-26)

Numbering continues forge#2994's (33–44). The owner accepted this document on
2026-11-26 ("2997 accept"), with one principle over all of it: "We are piecemeal
opt-in. Nothing is all or nothing users can opt into certain features without
taking others" ([the settled principle](#settled-principle-piecemeal-opt-in)).

### Settled (owner, 2026-11-26)

Every decision below is settled on its recommended option. Item N is decision N,
and the sections that raised each one describe the chosen option.

45. **Typed managed components in `forge.MLStack`'s slots**, lowering to `Adopt`
    and `Install`. `MLStack` is only a grouping: a stack with one component is a
    valid stack, and `forge.ModelService`, a model release and an eval gate work
    without any `MLStack`, cluster inventory or Kueue.
46. **"Hosted" means forge manages the operator in the customer's cluster**, and
    the control plane hosts the ledgers, telemetry, advisories and budgets. A
    hosted runtime stays an additive option for later.
47. **The add-on version is written into `ml.k`**, and forge proposes bumps as a
    reviewed diff (`ml-addon-outdated`). An omitted version follows forge's pin,
    with a lint warning.
48. **A narrow `forge.ModelService` renders the InferenceService**, with an
    `overrides` patch. `forge.Manifests` with `release_inputs` keeps working.
49. **KServe runs in Standard mode only in v1.** Online comparison stays in-app
    Changes.
50. **KServe loads OCI models as `modelcar`** by default; `native` when image
    volumes are on where the customer runs; `fetch` only as a fallback.
51. **The segment verdict is a paired comparison** on the same eval-set digest. A
    metric regresses when the drop exceeds its confidence interval and the
    declared `max_regression`; any failing segment fails; an under-sampled
    segment is "not proven".
52. **The eval harness runs as a Job in the env's cluster**, against the
    candidate's endpoint. LLM judges keep their control-plane worker.
53. **The default training orchestrator is a Kubeflow Trainer TrainJob**, queued
    by Kueue where Kueue is present, with a plain Job for smoke runs. JobSet stays
    an `orchestrator` value.
54. **A dataset is a reference** with a digest or snapshot id and a region on the
    run, nothing else, in v1.
55. **Kueue is installed when the stack declares it**: when a team starts naming
    queues, or accepts the `gpu-jobs-bypass-queue` finding's KCL diff. Never as a
    side effect, and never always with Trainer.
56. **No separate charge for managed add-ons** through beta; GA pricing is decided
    with ml-workloads decision 11's data.
57. **The model's input/output contract** is a named built-in (`openai-chat`,
    `openai-completions`) or a JSON Schema or OpenAPI file in the repo.

### The record

The options weighed, for the record. The chosen option is the recommended one.

| #   | Decision                                 | Options weighed                                                                                                                                                                                                                                                                  | Chosen, and why                                                                                                                                                                                                                                                                                                           | Unblocks    |
| --- | ---------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------- |
| 45  | Shape of the managed add-ons             | (a) Typed managed components in `forge.MLStack`'s slots, lowering to `Adopt`/`Install`. (b) First-class Bundle kinds (`Bundle.kserves`), each with its own runtime binding, like `ManagedDatabase`. (c) No typed kinds: `Install` with chart values                              | (a). The add-ons share a cluster, and the duplicate-install checks already live on `MLStack`. `MLStack` is only a grouping: one component alone is a valid stack, and nothing outside it requires one. (b) repeats the cluster on each and loses those checks; (c) cannot carry the settings the model release depends on | S1          |
| 46  | What "hosted" means for ML add-ons in v1 | (a) forge manages the operator in the customer's cluster; the control plane hosts the ledgers, telemetry, advisories and budgets; a hosted runtime stays an additive option. (b) Also a platform-run KServe for small CPU models on Reliant hosting now. (c) GPU hosting         | (a). Hosting has no GPUs, and CPU-only serving is a narrow product. (c) contradicts Settled 1. Revisit (b) when a customer asks to serve small models without a cluster                                                                                                                                                   | S1, pricing |
| 47  | Add-on upgrade policy                    | (a) The version is written into `ml.k`; forge proposes bumps as a reviewed diff (`ml-addon-outdated`). (b) Omitted versions follow forge's pin, so upgrading forge upgrades the operator                                                                                         | (a). It is declarative and reviewed (ml-workloads P1), and an operator upgrade on a GPU cluster is not a side effect of a CLI upgrade. (b) stays possible for a team that omits `version`, with a lint warning                                                                                                            | S1          |
| 48  | How an InferenceService is declared      | (a) A narrow `forge.ModelService` that renders it, with `overrides`; `forge.Manifests` still works. (b) `forge.Manifests` with `release_inputs` only (ml-workloads as written). (c) KServe's alpha `LLMInferenceService`                                                         | (a). The release must link a model to its service to substitute the digest, tag traces and judge SLIs. It amends ml-workloads' "no typed wrappers" for this one shape, and stays a few fields, not a copy of the CRD. (c) is alpha                                                                                        | S2          |
| 49  | KServe deployment mode                   | (a) Standard only in v1. (b) Serverless with Knative, for canaries and scale-to-zero                                                                                                                                                                                             | (a). It installs without Knative or Istio, which is what a customer's GPU cluster usually lacks. Online comparison stays in-app Changes. Revisit (b) with ml-workloads decision 8's router splits                                                                                                                         | S1, S2      |
| 50  | How KServe loads an OCI model            | (a) `modelcar`. (b) `native`, a Kubernetes image volume. (c) `fetch`, an init container                                                                                                                                                                                          | (a) by default: it works on any containerd. (b) when image volumes are on by default where the customer runs (needs containerd 2.1+, ml-workloads §3.5). (c) only as a fallback                                                                                                                                           | S2          |
| 51  | The segment verdict's rule               | (a) Paired comparison on the same eval-set digest; a metric regresses when the drop exceeds its confidence interval and the declared `max_regression`; any failing segment fails; an under-sampled segment is "not proven". (b) Aggregate only. (c) Thresholds without intervals | (a). It is the case the owner's notes name, and (c) blocks releases on noise (decision 22's concern)                                                                                                                                                                                                                      | S4          |
| 52  | Where the eval harness runs              | (a) As a Job in the env's cluster, against the candidate's endpoint. (b) In a control-plane worker                                                                                                                                                                               | (a). Code evaluators run in customer code, as llm-agent-observability decision 5 settled; samples stay in the customer's bucket. LLM judges keep that decision's control-plane worker                                                                                                                                     | S4          |
| 53  | The default training orchestrator        | (a) Kubeflow Trainer TrainJob under Kueue, plain Job for smoke runs. (b) JobSet. (c) Plain Job only                                                                                                                                                                              | (a). Trainer v2 runs on JobSet, has runtimes to declare once, and Kueue integrates with it where Kueue is present. JobSet stays an `orchestrator` value for teams that use it                                                                                                                                             | S5          |
| 54  | Datasets in v1                           | (a) A reference with a digest or snapshot id and a region on the run, nothing else. (b) A minimal dataset entity with versions                                                                                                                                                   | (a), as the owner suggested. It serves lineage and placement; (b) is the catalog's first step and can follow a design partner                                                                                                                                                                                             | S6          |
| 55  | When Kueue is installed                  | (a) When a template names a queue, or the `gpu-jobs-bypass-queue` finding is accepted. (b) Always with Trainer                                                                                                                                                                   | (a). The owner's order: Kueue when GPUs are contended. A single team's first TrainJob does not need it. "Installed" means the stack declares it; naming a queue with no Kueue is a render error, never an implicit install                                                                                                | S5          |
| 56  | Billing for managed add-ons at GA        | (a) None separate: covered by observability ingest and the ML pricing. (b) A fee per managed cluster. (c) Per managed accelerator-hour                                                                                                                                           | (a) through beta, then decide with ml-workloads decision 11's data                                                                                                                                                                                                                                                        | GA          |
| 57  | The model's input/output contract        | (a) Named built-ins (`openai-chat`, `openai-completions`) plus a JSON Schema or OpenAPI file in the repo. (b) Free-form, unchecked                                                                                                                                               | (a). vLLM serves the OpenAI-compatible API, so most models need no file; a custom model's contract is a file the app's tests call against, and a contract change is a declared rollback floor                                                                                                                             | S2          |

## Appendix: verification list

Upstream claims marked "(verify)" above, to check against the pinned versions
before they become rendered configuration or user-facing copy. Read from the
projects' repositories at their latest tags on 2026-11-26:

- **Verified:** KServe's OCI model support is a config switch (`enableModelcar`,
  newer `enableOciModelSupport`, `ociModelMode`), off in the raw ConfigMap and on
  for modelcar in the Helm values; its default mode is still Serverless; Standard
  mode installs without Knative or Istio but needs cert-manager, and has no canary;
  `kserve-huggingfaceserver` uses vLLM when the model supports it;
  `LLMInferenceService` exists at v1alpha1/v1alpha2. Kueue's MultiKueue is beta
  and on by default; `multiKueue.dispatcherName` selects a built-in or external
  dispatcher, and Workload `status.nominatedClusterNames` exists (since v0.13).
  Kueue integrates TrainJob, RayJob, RayCluster and RayService. Trainer v2 has
  TrainJob, TrainingRuntime and ClusterTrainingRuntime. fake-gpu-operator
  advertises fake `nvidia.com/gpu` on labelled CPU nodes.
- **Unverified, blocking OCI model release:** preservation of the modelcar image's
  `@sha256:` digest through KServe's `oci://` storage URI into the serving pod,
  and verification of its payload against the separate ModelPack digest
  (section 2.2). There is no mutable-tag fallback.
- **Other unverified:** `gs://`
  on the pinned KServe; how a verify step attaches to KServe's storage
  initializer; the Standard-mode annotation's exact spelling; a CPU vLLM image
  under KServe's modelcar; vLLM's OTLP span export on the pinned version; exact
  metric names of vLLM, Kueue, Trainer and KubeRay; whether each chart upgrades
  its CRDs in place; the external dispatcher API's maturity level; KubeRay's OCI
  chart location; fake-gpu-operator on k3d; conditional manifest creates per
  object store.
