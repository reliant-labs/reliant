# ML training and inference in your own clusters

**Status:** proposal. Nothing here is built. The owner settled the direction on
2026-11-25 (the "Settled" list below; items 7 and 8 came from the owner's
review of forge#2836); everything else is **proposed**, and the
open owner decisions are in [section 11](#11-open-owner-decisions), each saying
what it blocks.
**Date:** 2026-11-25
**Related:** `deploy/cluster-connect` skill (the connect this builds on),
[third-party-telemetry.md](third-party-telemetry.md) (`forge.Telemetry`, the
scrape primitive this reuses), [changes.md](changes.md) (online comparison),
[start-anywhere.md](start-anywhere.md) (every piece usable alone),
[external-runtime.md](external-runtime.md), [resource-telemetry.md](resource-telemetry.md),
[sbom-vulnerabilities.md](sbom-vulnerabilities.md) and
[signed-image-admission.md](signed-image-admission.md) (artifact signing), and
the autoscaling proposal on forge#2809 (open).

## Where it stands (2026-11-25)

No ML-specific code exists in forge, control-plane or reliant: nothing in any
repository mentions GPUs, DCGM or accelerators. The design builds on the pieces
below. "Released" means the code is in a final tag (rc tags ignored).

| Building block                                                                          | State                                                                                                                                                                                                                                                                                                                                                                          |
| --------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `forge cluster connect`: a per-(cluster, env) ServiceAccount and a render-derived grant | Released (forge v0.1.44; scopes in v0.1.46). `internal/cli/cluster_connect.go`, `cluster_connect_scope.go:360-462`.                                                                                                                                                                                                                                                            |
| Hub delivery into connected clusters                                                    | Released. A dedicated Flux shard, `kustomize-controller-connected`, dials the cluster with `spec.kubeConfig.secretRef` (`kcl/lib/flux.k:337-372`; control-plane `internal/fluxhub/kubeconfig.go:19-25`).                                                                                                                                                                       |
| Private-address refusal                                                                 | Released (control-plane v1.7.76). `internal/clusteraddr/clusteraddr.go:92-117`: only platform orgs, at exact /32 endpoints, may connect a private address.                                                                                                                                                                                                                     |
| forge's node agent in a connected cluster (OTel collector DaemonSet)                    | Released (v0.1.45). `pkg/deploy/observability.go:338-504`: receivers `otlp`, `kubeletstats`, `filelog`, `k8sattributes`. There is no `k8s_events`, `k8sobjects` or cluster receiver.                                                                                                                                                                                           |
| OTLP token delivered into the customer cluster and renewed                              | Released (control-plane v1.7.54). `internal/telemetrydelivery`; it also dials in (`clusterclient/clusterclient.go:80`).                                                                                                                                                                                                                                                        |
| `forge.Telemetry`: scrape any third-party system, with no presets                       | Released (v1.10.0). `kcl/telemetry.k:93`. `Observability.telemetry` scrapes pods forge never declared (a HelmChart, an operator).                                                                                                                                                                                                                                              |
| `HelmChart`, `Manifests`, `ExternalDatabase`, `OnExternal`                              | Released (v1.0.0, v1.0.0, v1.0.12, v1.5.0).                                                                                                                                                                                                                                                                                                                                    |
| Adopting an existing install: ESO CRDs with no owner label                              | Released. `internal/cluster/component_owner.go:8-22`.                                                                                                                                                                                                                                                                                                                          |
| Release ledger: build once, promote by digest                                           | Released. `pkg/release/release.go:362`. Artifact kinds are a closed set, `oci`, `npm`, `gomod`, `file` and `git` (`:30-118`), and an artifact may be `CarriedFrom` an earlier release.                                                                                                                                                                                         |
| Release health (`forge obs check`)                                                      | Released (v1.0.0). `pkg/releasehealth`. It judges server-span RED (`pkg/obsquery/traces.go:776-840`), with changed ops keyed Connect-style (`changed_ops.go:110`).                                                                                                                                                                                                             |
| Changes and the experiment readout                                                      | Released (v1.10.0), labelled beta. `pkg/experimentstats` reads out a binary metric. Load-balancer routing is out of v1 (changes.md, Settled 4).                                                                                                                                                                                                                                |
| Hosted OCI registry, `registry.reliantapi.com`                                          | Released (control-plane v1.12.0). CNCF distribution v3.1.2 on GCS. Blobs are proxied through the registry pods, there is no referrers API, and there is no pull path into a customer's cluster. An org can already push any OCI artifact under its image paths. The precise gap is in [section 3.7](#37-building-out-the-reliant-registry).                                    |
| Image registry override                                                                 | Released. A host-bearing image reference is pushed as written, and a bare one composes under `ControlPlane.registry_host` (`kcl/schema.k:906-913, 2191-2225`; `internal/cli/build_push_registry.go:16-25`). Credentials come from the docker credential store, never KCL. The release records each artifact's registry (`URI`) and digests (`pkg/release/release.go:162-199`). |
| A GPU request on a `forge.Workload`                                                     | **Not possible.** `tiers.Resources` is CPU and memory only (`kcl/tiers/tiers_gen.k:612-617`). The only way in today is a `Bundle.overrides` patch.                                                                                                                                                                                                                             |
| A Clusters or ML page in the console                                                    | None. The generated client `cluster_pb.ts` exists in reliant `frontends/web`, but no page uses it.                                                                                                                                                                                                                                                                             |

## Summary

A team with its own GPU cluster connects it once with the existing
`forge cluster connect`. Then one command, `forge ml discover`, reads what the
cluster already runs: accelerators, the GPU Operator, DCGM exporter,
node-problem-detector, Prometheus, Kueue, KServe and image streaming. It writes
that inventory into the env's KCL as a declared **ML stack**. Each component in
the stack is **adopted** (we read theirs) or **installed** (only if it is absent
and they agreed), and never both. Delivery is the hub's Flux path, so nothing
new dials the cluster. Within minutes the console's ML area shows **Machines**:
every node, its accelerators, allocation against utilisation, and bad-machine
flags. It also shows **Recommendations**, each a detection rule over that data
with an adopt-first fix, written as a proposed KCL diff.

The rest builds on the same declarations, in later slices:

- A **training template**, `forge.Training`, lowers to the orchestrator the
  cluster already runs: JobSet, a Kubeflow TrainJob, a RayJob or a plain Job.
  A **run** is a recorded launch against that template.
- A **model version** is an OCI digest, recorded and promoted like an image.
  It lives in Reliant's registry by default, which this proposal builds out for
  multi-GB weights. A declared override can point at the customer's own OCI
  repository.
- An **inference service** is a `forge.Workload` that requests accelerators and
  mounts a model by digest. It ships through the existing release, promotion
  and `forge obs check` path, which gains inference SLIs.
- Model versions are compared offline by run and eval metrics, and online with
  Changes.

Reliant never provisions or sells GPUs. The product is the management layer, and
it is neutral about which hardware and which cloud.

## Settled (owner, 2026-11-25)

1. **We never provision GPUs.** We deploy into the customer's environments,
   mostly Kubernetes clusters they own.
2. **Cluster access builds on `forge cluster connect`.** The design adds only
   what ML needs beyond it: telemetry from inside the cluster, and a path for
   private clusters. No second connect mechanism.
3. **NVIDIA ships first; the schema is vendor-neutral from day one.** AMD, Google
   TPU, AWS Trainium/Inferentia and Intel Gaudi fit the same model and follow
   later.
4. **Declarative first, in KCL.** The cluster's ML stack, training templates,
   inference services, model versions and dismissed recommendations live in
   `deploy/kcl/<env>/main.k` as forge types. forge renders them, the existing
   Flux path delivers them, and `forge env render` and `forge lint` validate them.
   Imperative surfaces (console buttons, `forge cluster connect`,
   `forge ml discover`) write or propose declarations. They never mutate a
   cluster around them.
5. **Discover, then adopt, then install only if the component is absent and the
   user agrees.** Never install a duplicate. Two DCGM exporters on one node break
   each other's profiling counters. Never upgrade or reconfigure something we did
   not install. Anything we install is declared, labelled as ours, and removable.
6. **Model artifacts and pull speed are first-class.** Weights are OCI artifacts,
   versioned and promoted by digest, and cold start gets its own per-cloud
   checklist ([Appendix A](#appendix-a-cold-start-checklist-per-cloud)).
7. **Weights live in Reliant's registry by default, and we build it out for them;
   a customer may override with their own OCI repository** (owner, 2026-11-25,
   on forge#2836). The build-out is a first-class workstream
   ([section 3.7](#37-building-out-the-reliant-registry)):

   - serve blobs straight from object storage;
   - the referrers API;
   - pull credentials for connected clusters;
   - region placement;
   - pull-speed rules that apply to it.

   The override is declared per env or per model in KCL: the customer's
   same-region ECR, Artifact Registry, ACR or Harbor. forge pushes to it and
   records the digests. Its credentials stay the customer's and never appear
   in KCL.

8. **One top-level nav item for everything ML** (owner, 2026-11-25). It holds
   six sub-pages:

   - org-wide: Machines, Stack and Recommendations;
   - filterable by app or project: Runs, Models and Inference.

   Only the label is open
   ([decision 9](#11-open-owner-decisions)).

9. Standing rules that apply unchanged:
   - Monitoring requires sign-in.
   - forge never stands in for another tool.
   - forge is semver at major 1, so every surface here is additive.
   - Every piece is usable alone ("start anywhere").
   - Recovery rolls forward.

## Design principles

These follow from the settled list. Every later section is checked against them.

- **P1. The cluster's ML configuration is a file.** Discovery's output is a KCL
  diff, and accepting a recommendation is a reviewed change to that file. A
  console button opens the PR (or, for a user without a repository, writes the
  declaration through the same API `forge` uses). No path mutates the cluster
  without one.
- **P2. Adopt before install.** Every component slot in the ML stack is one of
  three: `forge.Adopt` (a selector over what is already there, which lowers to a
  `forge.Telemetry` scrape and an inventory assertion), `forge.Install` (which
  lowers to an existing `forge.HelmChart` with the `forge.dev/component-owner`
  label), or absent. Install refuses at render when the cluster's last inventory
  shows a conflicting component.
- **P3. Never stand in for another tool.** forge writes no scheduler, serving
  router, GPU health checker, image streamer or P2P cache. It declares Kueue,
  KServe, JobSet, KubeRay, DCGM exporter, node-problem-detector and Spegel, reads
  them, and recommends them. It runs none of their logic.
- **P4. One accelerator model, per-vendor sources.** The schema and the queries
  speak of devices, utilisation, memory, power and temperature, errors, and
  interconnect, never of DCGM. Each vendor's exporter is one source, discovered
  and adopted the same way ([section 3.4](#34-one-accelerator-model)).
- **P5. Start anywhere.** Each piece works alone:

  - `forge ml discover` against a kube context, with no control plane;
  - telemetry on an existing KServe the team deploys with Argo;
  - model digests in a release for a team whose training lives in W&B.

  A project needs no forge-built workload to get the Machines view.

## 1. Problem and experience

ML teams that run their own clusters usually have:

- GPUs allocated but idle;
- one bad GPU or NVLink stalling a 256-GPU job;
- pods that take ten minutes to start because a 15 GB CUDA image and 40 GB of
  weights are pulled per pod;
- no lineage from the model serving traffic back to the run and data that made it;
- checkpoints resumed by hand.

The tools that address these are split across W&B or MLflow (runs), Run:ai or
Datadog (utilisation), CoreWeave or SageMaker HyperPod (bad nodes, on their own
hardware) and Argo or KServe (serving). None of them joins a model version to its
live release health. Neptune is gone (OpenAI bought it, and the SaaS closed on
2026-03-05). W&B is owned by CoreWeave and Run:ai by NVIDIA, both of which sell
GPUs. A hardware-neutral layer has room ([research: competitive](#appendix-d-research)).

**The first five minutes.**

```bash
forge cluster connect gpu-prod --context gke_acme_us-central1_gpu --env prod   # exists today
forge ml discover --env prod                                                    # new
```

`forge ml discover` reads the cluster read-only through **your own kube
context**, so it works on a private cluster your laptop can reach, and it needs
no control plane. It prints an inventory:

```
accelerators   24 × nvidia-h100-80gb on 3 nodes (GPU Operator v24.9, driver 550.127)
               8 × nvidia-l4 on 2 nodes (GKE device plugin)
gpu metrics    FOUND dcgm-exporter 3.3.9 via GPU Operator (gpu-operator/nvidia-dcgm-exporter :9400) — adopt
               FOUND GKE managed DCGM (gke-managed-system) on the L4 pool — adopt; never install a second
node health    MISSING node-problem-detector (GKE auto-repair is a node-pool setting: check it, the API cannot see it)
queueing       FOUND kueue v0.10.1 (2 ClusterQueues)
serving        FOUND kserve v0.15 (3 InferenceServices), vllm pods (2)
image pulls    GKE Image Streaming OFF on the H100 pool; registry us-docker.pkg.dev ≠ node region us-central1
```

It then proposes `deploy/kcl/prod/ml.k` (the full shape is in
[section 3.1](#31-the-declarative-surface)): the adopted components with what was
found, the one install it suggests behind a comment, and nothing for what is
already handled. You review it, commit, and `forge env deploy prod` (or the
hub's Flux reconcile) delivers the scrape config and the cluster collector. The
console's **ML → Machines** page fills in within one scrape interval.

**The first hour.** **ML → Recommendations** lists findings, worst first:

- "3 of 24 H100s idle for 6h (allocated to `ns/research`)";
- "node gke-…-h100-2: Xid 79, GPU fell off the bus; cordon it";
- "pod start p50 7m40s, of which image pull is 5m10s: turn on Image Streaming
  for the H100 pool";
- "PyTorch pods mount no shared memory: `/dev/shm` is 64 MiB".

Each finding has a "Propose fix" action that opens a PR with the KCL change, or
for a fix outside our declarations (a GKE node-pool flag) the exact command. Each
also has a "Dismiss" action that writes a reasoned, expiring
`forge.RecommendationDecision`.

**Later slices.** Runs, Models and Inference fill in as teams declare training
templates and models ([section 2](#2-the-model)), or as discovery finds them
already running (an existing PyTorchJob or InferenceService is shown, not
re-declared).

## 2. The model

Each ML concept maps onto an existing forge object wherever one fits, rather than
a parallel world.

| ML concept        | What it is here                                                                                                         | Built on                                                                          | Declared or observed                                                                |
| ----------------- | ----------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------- |
| Machine           | A node with its accelerators, health, allocation and utilisation                                                        | Nothing: today a node is only an attribute in `pkg/obsquery/workloads.go:516-530` | Observed; never declared (we do not provision)                                      |
| ML stack          | What the cluster runs for ML, each component adopted or installed                                                       | `HelmChart`, `forge.Telemetry`, component-owner adoption                          | Declared (`forge.MLStack`)                                                          |
| Training template | Image or build, accelerators, workers, queue, checkpoint location, default params                                       | `forge.Workload` (the pod template) lowered to the adopted orchestrator's CR      | Declared (`forge.Training`)                                                         |
| Run               | One launch of a template with parameters: status, restarts, metrics, logs, the checkpoints and model it produced        | A record in the control plane, like a deploy or a Changes run                     | Recorded (decision 5); pinned runs may be declared; runs others create are observed |
| Model version     | An OCI artifact digest (CNCF ModelPack layout, one layer per weight file)                                               | A release `oci` artifact                                                          | Recorded in the release; referenced from KCL (`forge.Model`)                        |
| Inference service | A `forge.Workload` (kind `service`) that requests accelerators and mounts a model, or an adopted KServe/vLLM deployment | Workload, Release, `forge obs check`                                              | Declared, or observed when adopted                                                  |
| Model release     | A release whose model digest changed. Code artifacts are carried forward, not rebuilt.                                  | `Artifact.CarriedFrom`, promote by digest                                         | The same release ledger                                                             |
| Comparison        | Offline: runs and eval metrics side by side. Online: two model versions on live traffic.                                | Offline: new; online: Changes and `experimentstats`                               | —                                                                                   |
| Recommendation    | A detection rule over inventory and telemetry, with an adopt-first fix                                                  | A pure rule package shared by CLI and control plane, like `pkg/releasehealth`     | Fix: declared; dismissal: declared (`forge.RecommendationDecision`)                 |

**Why a run is not declared by default.** A sweep launches fifty runs in an
afternoon. If each run were a commit and a deploy, the declarative surface would
become a launch log. A deploy is already the precedent: the release is declared
and built once, and promoting it is a recorded event against it. Changes is the
other precedent: the change is declared, and the run of an experiment is live,
audited state. A training run fits the same split. The template is declared and
reviewed: what may run, on which accelerators, in which queue, with which
checkpoint store. A run is a recorded launch against that template. A run that
must be reproducible and promotable (the one that produced the model you ship)
can be pinned in KCL. This is [decision 5](#11-open-owner-decisions).

## 3. Architecture

```
 repo: deploy/kcl/<env>/main.k (+ ml.k)                     customer cluster (connected)
   forge.MLStack, forge.Training, forge.Model,                ┌──────────────────────────────────────┐
   Workload.accelerators / model, RecommendationDecision      │ adopted: GPU Operator, DCGM exporter, │
        │  forge env render / lint / validate-kcl             │   NPD, Kueue, KServe, Prometheus …    │
        ▼                                                     │ installed (ours, labelled): NPD chart │
   release bundle (OCI) ── hub Flux (connected shard) ──────▶ │ forge node agent (DaemonSet)          │
                           dials the API server as the        │ + cluster collector (1 replica):      │
                           connect ServiceAccount             │   k8s_events, k8sobjects (inventory), │
                                                              │   scrapes of adopted exporters        │
                                                              └──────────────┬───────────────────────┘
                                                                             │ OTLP, outbound, per-org token
                                                                             ▼
   forge ml discover ──(your kube context, read-only)──▶ inventory     control plane: otel-gateway → ClickHouse t_<org>
        │                                                    │          inventory + accelerator metrics + events
        └─ proposes ml.k                                     ▼                     │
                                              pkg/mlrecs (pure rules) ◀────────────┘  (same rules, both sides)
                                                             │
                                             reliant console: ML → Machines, Recommendations, Runs, Models, Inference
```

### 3.1 The declarative surface

Every type below is new, optional and additive. An existing env that does not
mention one renders byte-identically, so the change is a semver minor
(`feat(deploy):`). Types that reach the Workload CR are Go fields in
`pkg/deploy/v1alpha1` regenerated into `kcl/tiers/tiers_gen.k`; authoring-only
types go in `kcl/schema.k` beside their precedents. The names are a sketch, not
settled syntax.

**`Bundle.ml_stacks: [forge.MLStack]`**: one per connected cluster.

```python
import forge

_gpu = forge.ClusterTarget {cluster = "gke_acme_us-central1_gpu", namespace = "ml", connected_cluster = "gpu-prod"}

ml_stacks = [forge.MLStack {
    cluster = _gpu
    accelerators = [forge.Accelerators {
        vendor = "nvidia"
        # found by forge ml discover, 2026-11-25: dcgm-exporter 3.3.9 via GPU Operator v24.9
        metrics = forge.Adopt {namespace = "gpu-operator", selector = {"app": "nvidia-dcgm-exporter"}, port = 9400}
    }]
    node_health = forge.Install {chart = forge.HelmChart {
        name = "node-problem-detector", repo = "https://charts.deliveryhero.io/", version = "2.3.14"
        delivery = "bundle", namespace = "forge-ml"
    }}
    queue = forge.Adopt {kind = "kueue"}
    serving = [forge.Adopt {kind = "kserve"}]
    decisions = [forge.RecommendationDecision {
        id = "kubelet-tracing-off", decision = "dismiss"
        reason = "GKE does not expose kubelet tracing config", expires = "2027-06-01"
    }]
}]
```

| New type or field                                            | Shape (sketch)                                                                                                                                                                                                                                                                                    | Lowers to (existing primitive)                                                                                                                                                                                                     |
| ------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `Bundle.ml_stacks: [MLStack]`                                | `cluster: ClusterTarget`; `accelerators: [Accelerators]`; `node_health?`, `queue?`, `serving: [Component]`, `pull_cache?`: each `Component = Adopt \| Install`; `collector: MLCollector = {}`; `decisions: [RecommendationDecision]`                                                              | A cluster collector (below), scrapes, HelmCharts, and nothing for components that are absent                                                                                                                                       |
| `Accelerators`                                               | `vendor: "nvidia" \| "amd" \| "tpu" \| "neuron" \| "gaudi" \| "generic"`; `resource?` (default from vendor, e.g. `nvidia.com/gpu`); `metrics?: Adopt \| Install`; `node_selector?`                                                                                                                | `Adopt`: a `forge.Telemetry` scrape with the vendor adapter's `allow` list ([decision 3](#11-open-owner-decisions)). `Install`: a HelmChart, refused if inventory shows any exporter on those nodes                                |
| `Adopt`                                                      | `kind?` (a known component id), `namespace?`, `selector?`, `port?`, `address?` (a Prometheus `/federate`), `note?`                                                                                                                                                                                | `Observability.telemetry` entries (`selector` + `cluster`, or `address` with `MetricsScrape.params` for `match[]`), plus an inventory assertion: "adopted X is gone" becomes a finding, not a render error                         |
| `Install`                                                    | `chart: HelmChart` (pinned `version`, `delivery = "bundle"`)                                                                                                                                                                                                                                      | The HelmChart, labelled `forge.dev/component-owner=forge-ml`, pruned by Flux when removed from the file                                                                                                                            |
| `MLCollector`                                                | `events: bool = True`; `inventory: bool = True`; `inventory_interval = "5m"`; `pod_scope: "accelerator" \| "all" = "accelerator"`                                                                                                                                                                 | A one-replica OTel collector Deployment beside forge's node agent, with `k8s_events` and `k8sobjects` receivers and a declared read-only ClusterRole ([section 3.2](#32-discovery))                                                |
| `RecommendationDecision`                                     | `id`, `decision: "dismiss"`, `reason`, `expires` (required, YYYY-MM-DD)                                                                                                                                                                                                                           | Nothing in the cluster. Read by `pkg/mlrecs`. Modelled on `VulnExemption` (`kcl/schema.k:6760`). Accepting a recommendation needs no record: the accepted fix _is_ the declaration.                                                |
| `tiers.Resources.extended: {str:int}`                        | e.g. `{"nvidia.com/gpu": 8}`                                                                                                                                                                                                                                                                      | `resources.limits` (and requests). Full profile only; the hosted profile refuses it.                                                                                                                                               |
| `Workload.accelerators?: tiers.Accelerators`                 | `count`, `vendor?`, `product?` (e.g. `nvidia-h100-80gb`), `mig_profile?`                                                                                                                                                                                                                          | `extended` + a nodeSelector from the vendor's node-label convention (GFD `nvidia.com/gpu.product`, `cloud.google.com/gke-accelerator`, …) + the toleration the stack declares. Sugar over the primitive.                           |
| `Workload.affinity?`, `topologySpread?`, `runtimeClassName?` | Pass-through, as `nodeSelector` and `tolerations` already are (`kcl/workload.k:729-730`)                                                                                                                                                                                                          | Pod spec fields                                                                                                                                                                                                                    |
| `VolumeSource.image`                                         | `{reference, pull_policy?}`                                                                                                                                                                                                                                                                       | A Kubernetes image volume (an OCI artifact mounted read-only). The model mount.                                                                                                                                                    |
| `EmptyDirVolumeSource.medium?: "Memory"`                     | —                                                                                                                                                                                                                                                                                                 | A memory-backed `emptyDir` for `/dev/shm`. PyTorch DataLoader and NCCL need it, and today it cannot be declared.                                                                                                                   |
| `Bundle.models: [Model]`                                     | `name`, `repository` (a bare name: Reliant's registry, the default; a host-bearing reference: the customer's registry, pushed verbatim; `s3://`/`gs://` as the escape hatch), `format = "modelpack"`, `digest?` (pin; else the release's), `source_run?`                                          | A release artifact (section 7). Consumed through `ModelRef`.                                                                                                                                                                       |
| `Workload.model?: ModelRef`                                  | `{model, mount_path = "/models/<name>", via: "image_volume" \| "init" = "image_volume"}`                                                                                                                                                                                                          | An image volume of `repository@digest`. Fallback: an init container that pulls the artifact into an `emptyDir`. The image is the customer's choice (for example `ghcr.io/oras-project/oras`); forge runs no pull logic of its own. |
| `Bundle.trainings: [Training]`                               | `name`, `workload: Workload` (pod template; kind `job`), `orchestrator: "job" \| "jobset" \| "trainjob" \| "pytorchjob" \| "rayjob"`, `workers`, `queue?` (Kueue LocalQueue), `checkpoint?: {uri, env = "FORGE_CHECKPOINT_DIR"}`, `outputs?: {model}`, `params: {str:str}`, `runs: [TrainingRun]` | Nothing until a run exists. Each run renders the orchestrator's own CR (an Indexed Job, a JobSet, a TrainJob, a RayJob) with the `kueue.x-k8s.io/queue-name` label. The CR is the orchestrator's; forge writes it.                 |

What is deliberately **not** proposed:

- **Typed wrappers for third-party CRDs** (a `forge.InferenceService` mirroring
  KServe). A wrapper drifts from the CRD it copies. A KServe InferenceService
  goes in `forge.Manifests`, with the model digest substituted through
  `release_inputs`, which already pins external references at render.
- **A seventh workload kind.** A kind needs a row in the capability matrix
  (`kcl/lib/capabilities.k:155-270`) and a hosted-profile story, and training
  wants launch-many semantics that Workload lacks. `forge.Training` holds a
  Workload as its pod template instead.

**Validation.** Every type gets KCL `check:` blocks and `kcl/tests/negative_*.k`
cases. `forge ci validate-kcl` (`internal/cli/ci.go:209`) renders every env. Two
new lint rules go through `lint_rollout_table.go` with a grace minor:

- a workload requesting `extended` accelerators in an env with no `MLStack` for
  that cluster;
- an `Install` whose component the last recorded inventory shows as present.

The second reads `.forge/state/ml-inventory-<cluster>.json`, written by
`forge ml discover`; with no snapshot it is skipped and says so.

### 3.2 Discovery

Discovery has two halves, and neither needs a new grant on the hub's connect
ServiceAccount.

1. **First look, `forge ml discover`:** local, through the user's kube context.
   - It makes read-only calls: list nodes (capacity, allocatable, labels,
     conditions, `status.images`, `nodeInfo`), CRDs, DaemonSets and Deployments
     (by image and label), Services and ServiceMonitors/PodMonitors, ML CRs
     (Kueue, JobSet, Training Operator, Trainer, KubeRay, KServe, Gateway API
     Inference Extension), and recent Events. It matches them against the
     signature table in [Appendix B](#appendix-b-discovery-signatures).
   - It needs no control plane, so it works for any cluster the laptop reaches,
     private or not, and for a team that never connects.
   - It writes `ml.k` with `--write`, or prints the diff. It also saves the
     snapshot in `.forge/state/` for the lint above.
   - The matching code is a forge package, `pkg/mldiscover`. It is pure over a
     list of objects, so the control plane runs the same code on the
     collector's inventory.
2. **Continuous: the cluster collector.**
   - The declared `MLCollector` runs the OTel collector-contrib `k8sobjects`
     receiver (pull nodes, DaemonSets, Deployments, CRDs and ML CRs every five
     minutes; watch pods with accelerator requests) and the `k8s_events`
     receiver. Both export outbound as logs through the existing per-org token.
   - It is one replica, so events are not duplicated per node. control-plane's
     own platform agent already runs this shape (`deploy/kcl/lib/otel_agent.k:347,383`).
   - Its read-only ClusterRole is **in the render**. So `forge cluster connect
--dry-run` shows it, and the connect grant is derived from it the way every
     other rule is (`escalate` and `bind` pinned to its declared name).
   - It needs `--apply-scope cluster`, because a ClusterRole is cluster-scoped.
     connect already refuses namespace scope for such a render and says why.

Discovery's read set is listed in Appendix B. It **excludes**:

- `secrets`, and every write verb;
- `nodes/proxy`, which is effectively exec on the node. It would be the only
  way to read kubelet `configz` and so detect kubelet tracing directly, and it is
  never requested.
- `pods/log`. NCCL and Xid strings are already in the container logs forge's
  node agent collects through `filelog` (`pkg/deploy/observability.go:438`), so
  no grant is needed for them.

`nodes/metrics` (kubelet `/metrics`: pod start and image pull histograms) is
low-risk and included.

**Version compatibility.** Exporters rename metrics across versions. DCGM 3.3
renamed clock-throttle reasons to clock-event reasons, NVLink counters changed
between DCGM 2, 3 and 4, and vLLM's V1 engine renamed `gpu_cache_usage_perc`.
So the adapter maps metric _families_ through a versioned alias table keyed by
the version discovery found. It never assumes one name.

### 3.3 Telemetry

What already flows from a connected cluster is a starting point:

- pod CPU and memory from `kubeletstats`;
- container logs;
- OTLP from workloads;

all into per-org ClickHouse through the otel-gateway. ML adds four things, each
reusing a primitive:

| Signal                                                         | Source                                                                                                                                                                                                  | Mechanism                                                                                                                                                      |
| -------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Accelerator metrics                                            | The adopted (or installed) vendor exporter, or a federate scrape of the customer's Prometheus                                                                                                           | `forge.Telemetry` `MetricsScrape` with the adapter's `allow` list, so cardinality is bounded by construction                                                   |
| Node entity: capacity, allocatable, conditions, labels, images | `k8sobjects` pull                                                                                                                                                                                       | Inventory logs; a new `obsquery.Machines` query family                                                                                                         |
| Pod start timeline                                             | `k8s_events` (`Scheduled`, `Pulling`, `Pulled` with its "in Xs (Ys including waiting)" text, `Started`) plus pod condition timestamps from watched accelerator pods, plus kubelet `/metrics` histograms | Events and conditions need no extra privilege and work on every cloud. Kubelet tracing (GA in Kubernetes 1.34) is a bonus where a self-managed node allows it. |
| Inference SLIs                                                 | vLLM (`vllm:time_to_first_token_seconds`, `vllm:num_requests_waiting`, KV-cache usage), Triton (`nv_inference_*`), KServe status                                                                        | `forge.Telemetry` scrape. Release health learns metric-based findings (section 7).                                                                             |

Accelerator metrics are labelled with `pod`, `namespace` and `container` when
the exporter attributes them (DCGM does with `DCGM_EXPORTER_KUBERNETES=true`).
That attribution joins utilisation to runs and inference services. Where it is
absent, the join falls back to node and device plus the pod's allocation. Under
time-slicing, per-pod utilisation is not meaningful, and the UI says so.

**The node agent's token** is delivered by control-plane `telemetrydelivery`,
which dials the cluster just as Flux does. So a private cluster gets no
telemetry today either; the private-cluster decision covers both.

### 3.4 One accelerator model

| Field             | NVIDIA (first)                                               | AMD                                             | Google TPU (GKE)                                       | AWS Trainium / Inferentia          | Intel Gaudi              | Generic fallback                                                   |
| ----------------- | ------------------------------------------------------------ | ----------------------------------------------- | ------------------------------------------------------ | ---------------------------------- | ------------------------ | ------------------------------------------------------------------ |
| Detect vendor     | `nvidia.com/gpu` capacity; GFD labels                        | `amd.com/gpu`; `amd.com/gpu.*` labels           | `google.com/tpu`; `cloud.google.com/gke-tpu-*`         | `aws.amazon.com/neuron*`           | `habana.ai/gaudi`        | any extended resource in node capacity                             |
| Source to adopt   | DCGM exporter (GPU Operator, standalone, GKE managed)        | ROCm device-metrics-exporter (AMD GPU Operator) | Cloud Monitoring / Managed Prometheus (not in-cluster) | neuron-monitor exporter            | Gaudi metric exporter    | kube-state-metrics, or node and pod specs                          |
| Utilisation       | `DCGM_FI_PROF_GR_ENGINE_ACTIVE`, else `DCGM_FI_DEV_GPU_UTIL` | `gpu_gfx_activity`                              | duty cycle                                             | `neuroncore_utilization_ratio`     | `habanalabs_utilization` | unknown: allocation only                                           |
| Memory            | `DCGM_FI_DEV_FB_USED` / `FB_FREE`                            | `gpu_used_vram` / `gpu_total_vram`              | memory used / total                                    | `neuron_runtime_memory_used_bytes` | memory used / total      | —                                                                  |
| Health and errors | Xid, ECC DBE, row-remap failure                              | uncorrectable ECC / RAS per block               | GKE repair events                                      | `neuron_hardware_ecc_events_total` | `habanalabs_ecc_*`       | `allocatable < capacity` (device plugin marked a device unhealthy) |
| Interconnect      | NVLink counters                                              | XGMI                                            | ICI (verify)                                           | NeuronLink (verify)                | NIC port status          | —                                                                  |

Which views degrade by vendor:

- **Bad-machine rules are vendor-specific.** NVIDIA uses the Xid code table,
  AMD uses RAS and uncorrectable ECC, and TPU relies on the cloud's repair
  events.
- **TPU** has no in-cluster exporter: we read Cloud Monitoring or Managed
  Prometheus, which needs a GCP read credential. That needs its own
  decision when TPU ships.
- **The generic fallback** shows allocation without utilisation, and offers
  the vendor exporter as a recommendation.

The DRA `ResourceSlice` API (GA in Kubernetes 1.34) is the future vendor-neutral
device inventory. Discovery prefers it where a DRA driver publishes slices, and
falls back to extended resources.

### 3.5 Model artifacts

A model version is an **OCI artifact digest**. The proposed layout is the CNCF
ModelPack spec with one layer per weight file, so unchanged shards deduplicate
across versions and pulls run per layer in parallel. forge already pushes
non-image OCI artifacts with oras-go (static sites, the release bundle), so
pushing one needs no new tool. `forge ml model push <dir> --model <name>`
records the digest in the release ledger as an `oci` artifact. A recorded
artifact is digest-addressed and agnostic to media type, so no new artifact
kind and no strict-decode change are needed.

Consuming the model:

- **Preferred:** a Kubernetes image volume, or KServe's `oci://` modelcar.
  Either way the kubelet pulls the weights, so every image-pull accelerator in
  Appendix A applies to the weights too.
- **Where image volumes are not available** (the gate's default-on version must
  be verified, and it needs containerd 2.1+): an init container pulls the
  artifact into an `emptyDir`.

**Where the bytes live** (settled, item 7) follows the rule forge already
applies to images. There is deliberately no env-level registry field
(`kcl/schema.k:906-913`). The reference _is_ the destination
(`internal/cli/build_push_registry.go:16-25`):

| `forge.Model.repository`                                                                     | Pushed to                                                                                      | Pulled by the cluster with                                                                                                                                                                         |
| -------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| A bare name, `"llama-ft"` (the default)                                                      | `<ControlPlane.registry_host>/<org>/<project>/models/llama-ft`, i.e. `registry.reliantapi.com` | A scoped, rotated pull credential the control plane delivers into the connected cluster, declared as a `forge.ControlPlaneSecret` ([section 3.7](#37-building-out-the-reliant-registry))           |
| A host-bearing reference, `"us-central1-docker.pkg.dev/acme/models/llama-ft"` (the override) | That repository, verbatim                                                                      | The customer's own means: node IAM (GKE Workload Identity, EKS node role, AKS kubelet identity), or a Secret they name in the existing `ClusterTarget.image_pull_secrets` (`kcl/schema.k:956-960`) |

The override can be set per model (one `forge.Model`) or per env: that env's
`main.k` declares the same model with a different `repository`. It needs no
new concept. Push credentials for an override come from the docker credential
store and its helpers (`docker-credential-gcr`, `-ecr-login`, `-acr-env`),
through the same `internal/dockercred` path the static-site push uses
(`internal/deploytarget/staticsite_oci.go:40-50`). Nothing goes in KCL. The
release records the registry and digest on the artifact (`release.Artifact.URI`
and `Digests`, `pkg/release/release.go:162-199`). So promotion is by digest
from whichever registry holds the model, and a release can carry models from
both registries.

Why a team chooses the override:

- weights must not leave their cloud (residency);
- their nodes pull with cloud IAM, so no secret is delivered;
- the cloud's own streaming (GKE Image Streaming, SOCI, ACR Artifact Streaming)
  only works from that cloud's registry.

Why the default is ours:

- nothing to set up;
- one pull credential the platform rotates;
- a single place where retention, signing and lineage are enforced;
- it works the same on every cloud and on bare metal.

The [cold-start checklist](#appendix-a-cold-start-checklist-per-cloud) says
which choice is faster for a given cluster.

A bucket (`s3://`, `gs://`) remains the escape hatch. Its version is the object
path plus a manifest of file SHA-256s, because a bucket has no digest.

### 3.6 Cold start

Pod start is broken into six stages:

1. scheduling;
2. node provisioning;
3. image pull;
4. model download;
5. load to accelerator;
6. ready.

All six are measured from events, conditions and kubelet histograms
(section 3.3) and shown per inference service and per run. The dominant stage
picks which entries of the per-cloud checklist in
[Appendix A](#appendix-a-cold-start-checklist-per-cloud) the Recommendations
view shows. Every option in it is a tool the customer or their cloud runs. forge
detects it, recommends it, or declares it (an image reference, a node selector,
an image volume, a Spegel HelmChart under `MLStack.pull_cache`). It implements
none of them. `node.status.images` is the generic, privilege-free signal for
whether an image is already warm on the GPU nodes.

### 3.7 Building out the Reliant registry

The owner made Reliant's registry the default home for weights. This is what it
has today, and what it needs to carry multi-GB artifacts into customer
clusters. Paths are control-plane unless marked forge. Everything listed as
existing is in a final tag: control-plane v1.7.54, sigguard in v1.7.88, and
the registry declaration in v1.12.0.

**What exists.**

- **The server.** `registry.reliantapi.com` is CNCF distribution v3.1.2 on the
  GCS bucket `reliant-prod-registry`, running 2 replicas in the prod hub
  (us-central1). It has no CDN and no second region
  (`deploy/kcl/lib/hosted_deploy.k:96,104`; `deploy/kcl/prod/main.k:1267-1305`).
- **Pushing.** A customer pushes with their own `rlat_` token:
  `forge registry login`, or an implicit login in `env build --push`. The
  argv is in forge `internal/cli/registry_cmd.go:58-63`; see also forge
  `deploy/hosted-registry` skill.
  - The token realm mints a 15-minute org-scoped JWT
    (`internal/registryauth/mint.go:28`).
  - `allowedActions` grants push only inside the caller's org, and only with
    `deploy:write` and the push-quota verdict. It drops `delete` and every
    other action (`internal/registryauth/claims.go:160-230`).
- **Only the repository name is checked.** The path grammar has four kinds,
  `image`, `static_site`, `bundle` and `config`
  (`internal/ociregistry/name.go:57-62`; `doc.go:20-25`).
  - Nothing checks `mediaType` or `artifactType`. So **an org can already push
    any OCI artifact, a ModelPack included, under its `<org>/<project>/…`
    image paths.**
  - `sigguard` protects only `bundle.v1` repositories
    (`tools/registry-reliant/sigguard/guard.go:1-62`).
- **Quota and metering.**
  - A per-plan push cap of 10, 25 or 50 GiB refuses pushes, never pulls, and
    fails open (`internal/app/registry_quota_wire.go:46-100`;
    `internal/plansconfig/pricing.yaml:576,597,618`).
  - Storage is metered in GiB-hours but shadow-only, so nothing is billed
    (`pricing.yaml:410-417`; `internal/orgcosts/history.go:547`).
  - **Pull egress is not metered at all.**
  - Unclaimed orgs' content scan gives up above 256 MiB per layer or 1 GiB
    per image (`internal/contentscan/service.go:25-29`).
- **Pulling.** One credential per org (`internal/registrypull/contract.go:1-40`):
  - read-only (`deploy:read`), but covering the org's **whole subtree** with no
    expiry, rotated every 30 days with a 1-hour overlap (`tokens.go:33-68`,
    `contract.go:126,141`);
  - used by exactly three platform consumers: the tenant-namespace kubelet,
    hub Flux and the static-site operator. None of them is a customer cluster.
- **The precedent for writing into a customer cluster.** `internal/telemetrydelivery`
  already writes and renews a Secret in connected clusters. It is
  level-triggered and server-side-applies with its own field manager. On
  withdrawal it revokes first, then deletes the Secret. It is declared from
  forge as `forge.ControlPlaneSecret`, which today accepts only
  `kind = "telemetry_token"` (`internal/cpsecrets/shape.go:131`).

**The gap, as workstream items.** Each is a separate control-plane change, and
none blocks the customer-registry override.

| #   | Item                                   | Today                                                                                                                                                                                                 | Needed                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                               |
| --- | -------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| R1  | Serve blobs from object storage        | `REGISTRY_REDIRECT = False` (`deploy/kcl/lib/hosted_deploy.k:205-224`). Every byte goes through 2 pods. The GCS driver's signed-URL redirect is "expected to work" but was never verified in-cluster. | Verify one redirected pull (the runbook's one-line flip) and turn it on. Manifests stay on the registry, which keeps auth and admission; blob GETs redirect to short-lived signed GCS URLs. Without this, one 70 GB model pulled by 16 nodes is about 1.1 TB through two pods.                                                                                                                                                                                                                                                                                                                                                                                                       |
| R2  | A model namespace                      | Models would sit under image paths, indistinguishable from images.                                                                                                                                    | A reserved `<org>/<project>/models/<name>` (or `model.v1`) kind in `ParseTenantName` and the forge artifact type, so quota, retention, scanning, pricing and signing can treat weights as weights.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                   |
| R3  | Pull credentials in connected clusters | None.                                                                                                                                                                                                 | A `forge.ControlPlaneSecret {kind = "registry_pull"}`, delivered by the `telemetrydelivery` reconciler, generalised (new `cpsecrets` kind; forge KCL kind), rendered as `kubernetes.io/dockerconfigjson`, and wired into the workload's ServiceAccount the way `image_pull_secrets` is. Scope: `deploy:read`, **resource-bound to the project's `models/` subtree** (not registrypull's whole-org grant), one per (cluster, env) binding, rotated on a declared period with an overlap, revoked first on withdraw or disconnect. A docker pull secret cannot refresh a 15-minute JWT, so the Secret holds the rotated long-lived pull token and each pull exchanges it at the realm. |
| R4  | The OCI referrers API                  | None in distribution v3.1.2; signatures use the `sha256-<hex>` tag fallback (`sigguard/guard.go:33-36`).                                                                                              | Referrers for model signatures, SBOMs and eval attestations. Either a distribution release that serves it, or a registry that does (zot, Harbor). That replacement is a control-plane architecture choice ([decision 14](#11-open-owner-decisions)). Until then, the tag fallback, with `models/` joining sigguard's protected set so only the platform signer can write signature tags.                                                                                                                                                                                                                                                                                             |
| R5  | Region placement and egress            | One region (us-central1). A pull into an EKS cluster in eu-west-1 is cross-cloud internet egress, unmetered and unpriced.                                                                             | A registry-pull egress meter first. Then placement: either regional mirrors (GCS dual- or multi-region buckets plus redirect, so the signed URL is near the puller), or an **in-cluster pull-through cache** the customer installs (`MLStack.pull_cache`: Spegel, Dragonfly, or a distribution or Harbor proxy cache). With a cache, the bytes cross clouds once per digest, not once per node. Who pays is [decision 6](#11-open-owner-decisions).                                                                                                                                                                                                                                  |
| R6  | Sizes, plans and billing               | Push caps of 10, 25 and 50 GiB per plan; storage shadow-billed; no per-blob cap; claimable scan limits.                                                                                               | Plan limits sized for weights (decision 13). Storage billed through the org storage-pool cutover. Unclaimed (claimable) orgs refused for the `models/` kind, because weights cannot be content-scanned.                                                                                                                                                                                                                                                                                                                                                                                                                                                                              |
| R7  | Retention                              | No tenant delete; retention is platform-run.                                                                                                                                                          | Keep every digest a live release references, and expire unreferenced model digests after a declared period. Today's platform retention must learn the `models/` kind so it never collects a digest an env still runs.                                                                                                                                                                                                                                                                                                                                                                                                                                                                |

**How the pull-speed checklist applies to our registry.** Several Appendix A
rows behave differently when the source is `registry.reliantapi.com`:

- **Cloud streaming does not apply.** GKE Image Streaming, SOCI and ACR
  Artifact Streaming only serve from their own cloud's registry.
- **What does apply:**
  - an in-cluster pull-through cache or P2P (Spegel, Dragonfly), which is also
    the egress answer;
  - pre-warming on GPU nodes (`node.status.images`);
  - node-local NVMe;
  - per-file layers for parallel pulls;
  - R1, which every other row assumes.
- **A new rule, `registry-cross-cloud`:** the model's registry is ours, the GPU
  nodes are on another cloud or region, and no cache is present. Its fix is to
  install a pull-through cache, or to declare the override for that env.

So the default stays fast for a scale-out, and the override stays a one-line
change for a team whose cloud streaming matters more.

## 4. Control plane

| Piece                     | What it does                                                                                                                                                                                                                      | Built on                                                                                         |
| ------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------ |
| Inventory store           | Latest inventory per connected cluster (components found, version, adopted or installed or missing), derived from `k8sobjects` logs by `pkg/mldiscover`                                                                           | ClickHouse `t_<org>`; a small Postgres row per cluster for the latest summary                    |
| Machines query            | Nodes, accelerators, allocation (pod specs) against utilisation (adapter), health flags                                                                                                                                           | `pkg/obsquery` new `Machines` and `Accelerators` families, used by both CLI and console          |
| Recommendations engine    | Evaluates `pkg/mlrecs` rules on a schedule per cluster and stores findings, with first seen, last seen and evidence. A declared `RecommendationDecision` (read from the deployed release shape) hides a finding until it expires. | The `pkg/releasehealth` precedent: a pure rule package the CLI and control plane both run        |
| Runs                      | Records launches and their status, restarts, preemptions and artifacts, read from the orchestrator CR through the collector's watch                                                                                               | New `MLService` (`ListRuns`, `GetRun`, `LaunchRun`, `ListModels`, `GetMachines`, `ListFindings`) |
| Release health, inference | Adds metric-based findings (TTFT p95, queue wait, KV-cache saturation) to the span-based ones, gated by `min_requests` as now                                                                                                     | `pkg/releasehealth` gains a metric finding type                                                  |

**Launching a run** (`LaunchRun`, or `forge ml run <training> --set lr=3e-4`):

- The template comes from the deployed release shape. Parameters are checked
  against the template's `params`.
- The orchestrator's CR is created **in the template's namespace, as the
  connect ServiceAccount**. The grant to create that CR kind would be derived
  from the render, as every rule is. This is a proposed extension: a template
  adds its orchestrator kind to the kinds connect derives rules for.
- The run is recorded with who launched it, the template's release, and the
  parameters.
- `forge ml run --context <ctx>` applies the same CR with the user's own
  credentials, for a team without a control plane.

Either way, the CR is the orchestrator's (Kueue admits it, JobSet runs it); forge
runs no scheduling logic.

**Authorization.** The new actions need new scopes in forge's closed scope set:
`ml:read` and `ml:launch`, grantable per environment. Reading machines and
findings needs sign-in, as all monitoring does.

## 5. Recommendations catalogue

Each rule is pure over inventory and telemetry, has a stable `id` (what a
`RecommendationDecision` names), and proposes the adopt-first fix as a KCL diff
or, for a setting outside our declarations, the exact command. "Slice" refers to
[section 12](#12-rollout-plan).

| id                             | Finding                                      | Detection rule                                                                                                                                                                                                                  | Fix (adopt first)                                                                                                      | Vendor       | Slice |
| ------------------------------ | -------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------- | ------------ | ----- |
| `accel-telemetry-missing`      | Accelerators with no utilisation source      | Accelerator capacity > 0 on a node and no adopted or installed metrics source covering it                                                                                                                                       | GPU Operator with `dcgmExporter.enabled=false`: tell them (their install); otherwise propose `Install` of the exporter | all          | 1     |
| `accel-exporter-duplicate`     | Two exporters on one node                    | ≥ 2 exporter pods (any of: `*dcgm-exporter*`, GKE managed DCGM, standalone `nv-hostengine`) on one node                                                                                                                         | Say which to remove. Touch neither.                                                                                    | NVIDIA       | 1     |
| `dcgm-profiling-off`           | Only coarse utilisation                      | Exporter present and no `DCGM_FI_PROF_*` series                                                                                                                                                                                 | Enable profiling counters in their exporter's counter CSV                                                              | NVIDIA       | 1     |
| `accel-error-counters-off`     | Xid and ECC not collected                    | No `DCGM_FI_DEV_XID_ERRORS` / ECC DBE series                                                                                                                                                                                    | Add the counters to the CSV                                                                                            | NVIDIA       | 1     |
| `bad-accelerator`              | A device is faulty                           | NVIDIA: Xid ∈ {48, 64, 74, 79, 94, 95, 119, 120, 140} or DBE > 0 or row-remap failure. AMD: uncorrectable ECC. Any vendor: `allocatable < capacity`                                                                             | Show node and device; propose cordon and reset/RMA                                                                     | per vendor   | 1     |
| `driver-skew`                  | Driver or CUDA versions differ within a pool | GFD `nvidia.com/cuda.driver.major` (or node-image) differs across nodes of one pool                                                                                                                                             | Align the node image                                                                                                   | NVIDIA       | 1     |
| `mig-config-failed`            | MIG partitioning failed                      | Label `nvidia.com/mig.config.state=failed`                                                                                                                                                                                      | Show the MIG manager's events                                                                                          | NVIDIA       | 1     |
| `no-node-health`               | Bad nodes keep getting work                  | No node-problem-detector, no managed auto-repair condition (e.g. EKS `AcceleratedHardwareReady`), no operator health                                                                                                            | Turn on the cloud's auto-repair; else `Install` NPD with a GPU log rule                                                | all          | 1     |
| `accel-idle`                   | Allocated but idle                           | Utilisation < 5% for ≥ 1h on an allocated device                                                                                                                                                                                | Name the pod and namespace; suggest Kueue quotas or an idle timeout                                                    | all          | 1     |
| `accel-unallocated`            | Capacity nobody uses                         | Allocatable devices unrequested for ≥ 24h                                                                                                                                                                                       | Scale the pool down (their autoscaler)                                                                                 | all          | 1     |
| `shm-too-small`                | `/dev/shm` is the 64 MiB default             | Pod requests accelerators and mounts no memory-medium `emptyDir` at `/dev/shm`                                                                                                                                                  | Declare `emptyDir {medium = "Memory"}`                                                                                 | all          | 1     |
| `pod-start-slow`               | Startup dominated by one stage               | p50 of a stage (section 3.6) > its threshold for accelerator pods                                                                                                                                                               | Routes to the Appendix A rows for that stage                                                                           | all          | 2     |
| `kubelet-tracing-off`          | Startup phases are opaque inside the kubelet | Self-managed nodes only (not GKE/EKS/AKS managed) and no kubelet spans arriving. We never read `configz`.                                                                                                                       | Add `tracing` to KubeletConfiguration and point it at the collector                                                    | all          | 2     |
| `gpu-jobs-bypass-queue`        | No fair share or preemption                  | Accelerator Jobs exist and there is no Kueue or Volcano                                                                                                                                                                         | Propose `Install` of Kueue                                                                                             | all          | 1     |
| `job-not-queued`               | A job skips quota                            | Kueue present and an accelerator Job lacks `kueue.x-k8s.io/queue-name`                                                                                                                                                          | Set `Training.queue`                                                                                                   | all          | 1     |
| `spot-no-termination-handling` | Spot nodes with no graceful notice           | Spot labels (`cloud.google.com/gke-spot`, `karpenter.sh/capacity-type=spot`, `eks.amazonaws.com/capacityType=SPOT`, `kubernetes.azure.com/scalesetpriority=spot`) and, on EKS, no termination handler or Karpenter interruption | Install the cloud's handler                                                                                            | all          | 1     |
| `no-checkpointing`             | Restarts lose work                           | Strong: the logged step or loss resets after a restart (JobSet `restarts`++ or a `Restarting` condition). Weak: on spot, with no PVC or object-store mount and no checkpoint arg                                                | Declare `Training.checkpoint`; show GPU-hours lost                                                                     | all          | 5     |
| `nccl-failures`                | Collective timeouts or network faults        | NCCL and PyTorch watchdog strings in collected logs, joined to Xid 74/79 and NVLink errors on the same node at the same time                                                                                                    | Name the node and rank; set `TORCH_NCCL_*` timeouts                                                                    | NVIDIA first | 5     |
| `llm-scales-on-cpu`            | Inference scales on the wrong signal         | vLLM or Triton with an HPA on CPU or GPU utilisation, or no autoscaler                                                                                                                                                          | KEDA on `vllm:num_requests_waiting` or KV-cache usage (an owner call alongside forge#2809)                             | all          | 3     |
| `model-downloaded-per-pod`     | Weights fetched on every start               | A storage-initializer or init container fetches from `s3://`/`gs://`/`hf://` on each start; no LocalModelCache                                                                                                                  | `Workload.model` by digest, or KServe LocalModelCache                                                                  | all          | 4     |
| `weights-in-image`             | One huge image for code and weights          | Serving image > 10 GB (from manifest sizes)                                                                                                                                                                                     | Split into a ModelPack artifact and a slim runtime image                                                               | all          | 4     |
| plus the Appendix A rows       | Cold start, per cloud                        | See Appendix A                                                                                                                                                                                                                  | See Appendix A                                                                                                         | per cloud    | 2     |

## 6. Comparing runs and model versions

**Offline: runs and evals.** The bar is W&B. It shows a config diff of only the
keys that differ, overlaid curves, and parallel coordinates. A competitive
charting UI and logging SDK is years of work, and teams will not switch the
logger they already use. So the recommendation is
[decision 4](#11-open-owner-decisions) (b): Reliant owns the parts nobody joins
well, and imports or links to everything else.

What Reliant owns:

- the run record, with its accelerator metrics on the same time axis;
- restarts, preemptions and bad-node events;
- the lineage spine: run → checkpoints → model digest → release → env → live
  health.

What it accepts or links to:

- an **MLflow-compatible tracking endpoint**: an OSS REST API, so no new SDK.
  The `params` and summary metrics a run logs land on its record.
- each run's W&B page, linked by run ID.

Comparing runs is then a table and overlaid curves over what was logged, plus a
config diff.

**Online: model versions on live traffic.**

- **Available today**, without new mechanisms: Changes evaluates in the app (or
  in a small router the team owns) and calls inference service A or B, with
  exposures recorded and the readout from `experimentstats`.
- **Missing for ML:**
  - continuous metrics: latency, a quality score, cost per token. Today's readout
    judges only a binary event within a window.
  - router-level splits and shadow traffic. Changes Settled 4 rules
    load-balancer routing out of v1.
- **Proposed:** for model versions, declare the split through the router the
  cluster already runs: KServe `canaryTrafficPercent`, Gateway API
  `HTTPRoute` weights, or the Inference Extension's InferencePool. forge
  declares, the router routes, and the readout joins the two variants' metrics.
  This is [decision 8](#11-open-owner-decisions).

## 7. Releasing models

A model rides the existing release. `forge env build prod --release v13` records
the model's digest beside the image digests, and `forge env deploy prod v13`
promotes it. Promotion runs `forge obs check prod --wait`, which then judges
inference findings too.

**Model-only releases.** The cadence that differs from code is a new model with
unchanged code. The recommended answer
([decision 7](#11-open-owner-decisions)) is a new release whose code artifacts
are `CarriedFrom` the previous one: no rebuild, same digests. It promotes, is
judged, and rolls forward like any other release. There is no second ledger.

**Fixing a bad model rolls forward.** A regression is fixed by promoting a
release with the previous model digest, not by an automatic rollback.

**Gates.** Model promotions use the existing `DeliveryPolicy` controls:

- required approvals;
- `health_gate`;
- when signing lands, a cosign or notation signature over the model digest, as
  a referrer. That waits on signed-image-admission and on a registry with
  referrers, which the customer's cloud registries have and ours lacks.

**Lineage.** `forge.Model.source_run` and the run record's outputs link the
release to the run that produced it, and through the run to its template's
release and git commit.

## 8. UI and agent tools

**One top-level nav item** (settled, item 8). Its label is
[decision 9](#11-open-owner-decisions); "ML" is recommended. It is one `NavArea`,
a drill-in whose rows replace the nav on its routes, the same kind as
Observability.

The precedent is the Observability area. In reliant `frontends/web/src`, it is
registered like this:

- The nav is a plain list in `components/Layout/sidebar/navConfig/hybrid.ts:44-61`,
  and its item kinds (row, group, area, heading, slot) are in `navConfig/types.ts:1-22`.
  `NavArea` is at `:147-168`.
- `observabilityArea` (`navConfig/items.tsx:367-377`) is a `NavArea` with:
  - `available: (ctx) => hasApp(ctx) || ctx.mode !== "one"`, so it shows with
    or without an app chosen;
  - a `match` (`isObservabilityRoute`, `:359-364`) that claims both per-app
    console pages and the org pages.
- Org pages are an `OrgPage` kind (`components/Forge/Console/paths.ts:141`,
  `orgPath` at `:148`) under the org layout's routes
  (`forgeConsoleRoutes.tsx:10-11`: `/forge/observability`, where `?project=`
  scopes to one app; route at `:211`). Per-app pages are a `ConsolePage` kind
  (`paths.ts:166`, `consolePath` at `:328`).

The ML item follows the same steps:

1. An `mlArea: NavArea` in `items.tsx`, inserted into `hybridNav.items` (the
   proposal places it after `observabilityArea`).
2. `OrgPage` kinds for Machines, Stack and Recommendations, since clusters are
   org resources.
3. `ConsolePage` kinds, or org pages with a `?project=` filter, for Runs, Models
   and Inference, so they are filterable by app or project.
4. One `match` that claims all six pages.

| Page            | Shows                                                                                                                                                                             | Slice |
| --------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----- |
| Machines        | Per cluster and pool: nodes, accelerators (product, count, MIG/sharing), allocation against utilisation over time, health flags with evidence (Xid, ECC, conditions), warm images | 1     |
| Stack           | The inventory: "found DCGM exporter 3.3.9 via GPU Operator — adopted", "node-problem-detector — installed by forge", "Kueue — not found", with the KCL that declares each         | 1     |
| Recommendations | Findings worst first, with evidence, "Propose fix" (a PR with the KCL diff, or the command), and "Dismiss" (writes a `RecommendationDecision`)                                    | 1     |
| Inference       | Each service: release, model digest, TTFT, queue wait, throughput, accelerator use, cold-start breakdown, release health per promotion                                            | 3     |
| Models          | Versions by digest, the run that produced each, eval metrics, where each is serving                                                                                               | 4     |
| Runs            | Status, parameters, logged metrics, accelerator metrics, restarts and preemptions, logs, checkpoints, model output; comparison of selected runs                                   | 5     |

**Agent tools:** the same `MLService` calls. An agent can:

- run discovery;
- explain a finding;
- draft the KCL fix as a PR;
- compare runs;
- launch a run against a declared template.

Production launches follow the Changes approval pattern ([decision
3 of changes.md](changes.md#8-open-owner-decisions)). An agent never writes to
the cluster except through a declaration or a recorded launch.

## 9. Security

- **No new inbound credential.** The hub reaches the cluster only as the
  existing connect ServiceAccount. Its grant is still derived from the render,
  and the collector's read-only ClusterRole is part of that render, visible in
  `connect --dry-run` before it is granted.
- **Least privilege for discovery.**
  - Read only.
  - Never `secrets`, `nodes/proxy` or `pods/log`; the logs we need are already
    collected.
  - `nodes/metrics` only.
  - The local `forge ml discover` uses the user's own credentials and sends
    nothing anywhere.
- **Installs are reviewed declarations.** An `Install` is a pinned chart in a PR.
  It is labelled `forge.dev/component-owner=forge-ml`, and forge refuses to
  install over a component labelled for another owner (the
  `component_owner.go` rule). We never upgrade or reconfigure an adopted
  component. A recommendation that would change it tells the owner what to
  change.
- **Disconnect.** Today the hub suspends and deletes its Kustomizations without
  pruning, so delivered objects stay running (control-plane
  `internal/clusterregistry/cascade.go:100-105`). For components _we_ installed
  on GPU nodes, [decision 10](#11-open-owner-decisions) proposes that
  `forge cluster disconnect --context` also removes them by label, as it already
  removes the grant objects.
- **Telemetry stays per org.** It goes outbound with the per-org token into
  `t_<org>`. Reading it requires sign-in.
- **Model weights.**
  - In Reliant's registry, they are pulled with a per-binding,
    resource-scoped, read-only credential that the control plane delivers
    and rotates, and revokes first on disconnect (R3).
  - Under the override, they never touch our infrastructure. Push and pull
    credentials stay the customer's, in their credential helpers or their own
    pull Secret, never in KCL.

## 10. Pricing

We sell no GPUs, so nothing here competes with a customer's cloud bill. That
neutrality is the position against W&B (CoreWeave) and Run:ai (NVIDIA). What
could be charged for is [decision 11](#11-open-owner-decisions):

- observability ingest, as today;
- a per-monitored-accelerator-hour fee for the ML area;
- a free tier of N accelerators.

The cost of what we add is collector CPU and ingest volume. Scrapes are bounded
by `allow` lists and `sample_limit`. Inventory is pulled every five minutes.
Events are low-volume.

## 11. Open owner decisions

None of these is decided. Each gives the realistic options, a recommendation, and
what it blocks.

| #   | Decision                                                                  | Options                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      | Recommendation                                                                                                                                                                                                                                      | Blocks                                                                |
| --- | ------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------- |
| 1   | Private GPU clusters (the hub must reach the API server today)            | (a) Public endpoint required, restricted to published hub egress addresses (GKE authorized networks, EKS public access CIDRs). (b) **Pull-mode delivery**: Flux in the customer's cluster pulls the env's release bundle (already an OCI artifact) outbound from a registry, and telemetry pushes outbound, so nothing dials in. Needs a pull credential into the cluster and a status channel back (the collector reports Kustomization status as inventory). (c) A reverse-tunnel agent we build. (d) Per-customer peering or PrivateLink. | (a) now. Verify the connected shard's egress is a stable, publishable set. (b) as the durable path: it is Flux's own model, adds no tunnel, and reuses the bundle. (c) builds a privileged component another tool already does. (d) does not scale. | Private-cluster customers; telemetry token renewal for them; slice 7. |
| 2   | Where discovery runs, and its RBAC                                        | (a) In-cluster collector with a declared read-only ClusterRole, derived into the connect grant (needs `--apply-scope cluster`). (b) A separate hub-side discovery ServiceAccount added by `connect --discover`. (c) Only the local `forge ml discover`.                                                                                                                                                                                                                                                                                      | (c) for the first look plus (a) for continuous. Both keep the hub's identity a function of the render; (b) adds a standing hub credential that reads the whole cluster.                                                                             | Slice 1.                                                              |
| 3   | Vendor metric adapters against "no presets"                               | (a) forge ships accelerator adapters as versioned data (metric family → neutral field, the `allow` list, the version aliases, the bad-device rules), a narrow exception to third-party-telemetry's no-presets rule. (b) Each project or its agent writes the mapping.                                                                                                                                                                                                                                                                        | (a). A neutral Machines view and bad-machine rules cannot exist without the vendor facts, and every customer would rewrite the same table. Keep it data, outside the collector, so a new DCGM version is a table row.                               | The accelerator model; slice 1.                                       |
| 4   | Run tracking: build or integrate                                          | (a) Our own tracking SDK and charting UI. (b) Own the run record, accelerator metrics and lineage; accept an MLflow-compatible tracking API; link out to W&B. (c) Link-out only.                                                                                                                                                                                                                                                                                                                                                             | (b). Users keep their logger; we own what nobody joins (run → model → release → live health). Revisit (a) only on demand.                                                                                                                           | Slice 5.                                                              |
| 5   | How a run is declared                                                     | (a) Every run is a KCL commit. (b) Templates are declared; runs are recorded launches against them; reproducible runs may be pinned in KCL. (c) Observe-only: runs others create are shown, never launched by us.                                                                                                                                                                                                                                                                                                                            | (b), with (c) always on (start anywhere). (a) turns a sweep into fifty deploys.                                                                                                                                                                     | `forge.Training`, `LaunchRun`; slice 5.                               |
| 6   | Who pays cross-cloud pull egress from Reliant's registry                  | (a) We absorb it within plan limits, metered per org. (b) Pass it through at cost, per GiB, like backend egress (`pricing.yaml:403-408`). (c) A free regional allowance; cross-cloud billed.                                                                                                                                                                                                                                                                                                                                                 | Meter first (R5), then (c): same-cloud pulls are cheap for us and should feel free; a cross-cloud scale-out is the customer's choice, and a pull-through cache or the override is the way out of it.                                                | Billing for models; R5; GA.                                           |
| 7   | Model release cadence                                                     | (a) The model digest is part of the app release; a model-only release carries code artifacts forward (`CarriedFrom`). (b) A separate model release ledger. (c) A digest pinned per env in `main.k`, changed by PR.                                                                                                                                                                                                                                                                                                                           | (a), with (c) allowed as an explicit pin. One ledger, one promotion, one health check.                                                                                                                                                              | Slice 4.                                                              |
| 8   | Online comparison of model versions                                       | (a) In-app Changes variants only (consistent with Changes Settled 4). (b) Also declare a split or shadow through the router the cluster runs (KServe, Gateway API, Inference Extension), plus continuous metrics in the readout.                                                                                                                                                                                                                                                                                                             | (a) now; (b) as a deliberate ML exception later. forge declares, the router routes, so it does not stand in for a tool.                                                                                                                             | Slice 6.                                                              |
| 9   | Label of the one ML nav item (the item and its six sub-pages are settled) | (a) "ML". (b) "ML Workloads". (c) "Machine Learning". (d) "AI/ML".                                                                                                                                                                                                                                                                                                                                                                                                                                                                           | (a) "ML": it is short and fits the nav's one-word rows (Data, Storage, Auth). "Workloads" collides with forge's `Workload` type, which would read as a filter of it. "AI/ML" promises agent and LLM features that live elsewhere.                   | Nav registration; slice 1 UI.                                         |
| 10  | Disconnect and what we installed                                          | (a) Today's behaviour: delivered objects are orphaned. (b) Disconnect also removes components labelled `forge-ml` (our installs, the collector); customer workloads stay. (c) Remove everything delivered.                                                                                                                                                                                                                                                                                                                                   | (b). Our DaemonSets on their GPU nodes should not outlive the relationship; their apps should.                                                                                                                                                      | Slice 1 install path.                                                 |
| 11  | Pricing                                                                   | (a) Observability ingest only. (b) Per monitored accelerator-hour. (c) A free tier of N accelerators.                                                                                                                                                                                                                                                                                                                                                                                                                                        | (a) + (c) through beta; decide (b) with dogfood and early-customer data.                                                                                                                                                                            | GA.                                                                   |
| 12  | Dogfood hardware                                                          | (a) Simulated GPUs only (a fake GPU operator on k3d, emitting DCGM-shaped metrics). (b) Plus a small real GPU pool (e.g. 2 × L4 on GKE) for two weeks per slice.                                                                                                                                                                                                                                                                                                                                                                             | (b). Xid, driver and streaming behaviour cannot be simulated; the cost is a few hundred dollars per slice (estimate).                                                                                                                               | Slice 1 sign-off.                                                     |
| 13  | Size limits and plan for model storage                                    | (a) Model bytes count against today's per-plan registry caps (10/25/50 GiB). (b) A separate `models/` allowance per plan, sized for weights (100s of GiB), billed beyond it through the storage pool. (c) Unlimited with storage billed.                                                                                                                                                                                                                                                                                                     | (b). Today's caps would refuse a single 70B model; a separate allowance keeps image quota behaviour unchanged. Refuse `models/` for unclaimed orgs (no content scan past 1 GiB).                                                                    | R2, R6; slice 4.                                                      |
| 14  | Referrers API for signatures and SBOMs on models                          | (a) Keep distribution and the `sha256-<hex>` tag fallback, with `models/` added to sigguard's protected set. (b) Move to a distribution release that serves referrers. (c) Replace the registry with zot or Harbor.                                                                                                                                                                                                                                                                                                                          | (a) now, so signing is not blocked; (b) when it ships. (c) is a registry migration whose cost is outside ML and should be decided on its own.                                                                                                       | R4; model signing.                                                    |
| 15  | Scope of the connected-cluster pull credential                            | (a) Reuse registrypull's per-org, whole-subtree token. (b) A new token per (cluster, env) binding, read-only and resource-bound to the project's `models/` (and image) paths.                                                                                                                                                                                                                                                                                                                                                                | (b). A credential sitting in a customer's cluster should read only what that env deploys; (a) would expose every project of the org.                                                                                                                | R3; slice 4.                                                          |

## 12. Rollout plan

Each slice ships alone and is useful without the next. The first ships the most
value fastest: a team connects a cluster and sees its machines and what to fix,
before we model a single run.

1. **Machines, Stack and Recommendations (NVIDIA).**
   - `pkg/mldiscover` (signatures, pure) and `forge ml discover` (local; proposes
     `ml.k`).
   - `forge.MLStack`, `Adopt`, `Install`, `Accelerators`, `MLCollector` and
     `RecommendationDecision`.
   - The cluster collector (`k8sobjects`, `k8s_events`) in
     `pkg/deploy/observability.go`.
   - The NVIDIA adapter (decision 3).
   - `obsquery.Machines` and `obsquery.Accelerators`.
   - `pkg/mlrecs` with the slice-1 rules.
   - Control-plane inventory and findings, and `MLService` read RPCs.
   - Reliant: Machines, Stack, Recommendations.
   - The `ml/connect` skill.
2. **Startup breakdown and cold-start checklist.**
   - Pod-start stages from events, conditions and kubelet metrics.
   - Appendix A rules for GKE, EKS and AKS.
   - `MLStack.pull_cache` (Spegel or Dragonfly as `Install`).
3. **Accelerator workloads and inference.**
   - `tiers.Resources.extended`, `Workload.accelerators`, `affinity`,
     `topologySpread`, `runtimeClassName` and `emptyDir.medium`.
   - Inference SLIs through `forge.Telemetry`; metric findings in
     `pkg/releasehealth`.
   - The Inference page.
   - The `ml/instrument` skill: OTLP traces from vLLM and Triton, metric
     `allow` lists, and the `/dev/shm` and probe settings serving needs.
4. **Models.**

   - `forge.Model`, `Workload.model`, `VolumeSource.image`, and
     `forge ml model push` (ModelPack via oras-go). It pushes to Reliant's
     registry, or verbatim to a declared override.
   - The registry workstream, as control-plane changes that land before or
     alongside it:

     - R1, redirect, comes first, because every model pull depends on it;
     - then R2, the `models/` kind;
     - R3, the `registry_pull` ControlPlaneSecret;
     - R6 and R7, limits and retention;
     - the egress meter from R5.

     R4, referrers, and regional placement follow. The override needs none of
     them, so a team that brings its own registry is not blocked.

   - Model-only releases with `CarriedFrom` (decision 7).
   - The Models page.

5. **Training.**
   - `forge.Training` lowering to Job, JobSet, TrainJob, PyTorchJob or RayJob
     with a Kueue queue.
   - `LaunchRun` and `forge ml run`.
   - Run records from CR status.
   - MLflow-compatible ingest and W&B links (decision 4).
   - Checkpoint and NCCL rules.
   - The Runs page and comparison.
6. **Online comparison.** Changes over two inference services first, then
   decision 8 (b).
7. **Private clusters.** Decision 1 (b), pull-mode delivery.
8. **More vendors.** AMD, then Neuron and Gaudi, then TPU (Cloud Monitoring
   source). Each is an adapter, its signatures, and its bad-device rules; the
   schema does not change.

**Dogfood.**

- Every slice is proven first on a k3d cluster with a simulated GPU operator in
  CI.
- Slice 1 is then proven on a real small GPU pool in our own GCP project
  (decision 12). That covers:
  - discover → commit `ml.k` → deploy;
  - Machines filled in;
  - a planted duplicate exporter that must be detected and never installed over;
  - an Image Streaming finding that goes away after the fix.
- The first external user is a design partner with an existing GPU Operator and
  Kueue. That is the adopt path, which is the common case.

**Explicitly out:**

- provisioning or reselling GPUs;
- our own scheduler, serving router, health checker or image streamer;
- typed wrappers of third-party CRDs;
- automatic cordon or drain (we recommend it; the customer's NPD, NVSentinel or
  cloud auto-repair acts);
- automatic rollback.

## Appendix A: cold-start checklist per cloud

forge detects these, recommends them, or declares them. It implements none of
them. Rows marked † contain label names or figures not verified in this pass
([Appendix C](#appendix-c-what-to-verify-before-building)).

| Finding                                                                                          | Cloud | Detection (k8s API or registry)                                                                                                                                   | Typical impact                             | Fix (the customer's tool)                                                                              |
| ------------------------------------------------------------------------------------------------ | ----- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------ | ------------------------------------------------------------------------------------------------------ |
| Model on Reliant's registry, nodes on another cloud or region, no cache (`registry-cross-cloud`) | all   | `forge.Model.repository` is ours, the GPU nodes' `topology.kubernetes.io/region` is outside our region, and no Spegel, Dragonfly or pull-through cache is present | High at scale-out; egress per node         | Install a pull-through cache (`MLStack.pull_cache`), or declare the override to a same-region registry |
| Registry not in the nodes' region                                                                | all   | Registry host region ≠ `topology.kubernetes.io/region` of the GPU nodes                                                                                           | High                                       | Replicate or mirror into the region                                                                    |
| Weights baked into the serving image                                                             | all   | Image > 10 GB from manifest sizes                                                                                                                                 | High: weights re-pulled on code changes    | ModelPack artifact + slim runtime image (section 3.5)                                                  |
| Image Streaming off †                                                                            | GKE   | Image on Artifact Registry and the GPU pool lacks the streaming label                                                                                             | High for large images                      | Enable Image Streaming on the pool                                                                     |
| No secondary boot disk †                                                                         | GKE   | GPU pool lacks the secondary-boot-disk label                                                                                                                      | Medium–high                                | A secondary boot disk preloaded with the CUDA and runtime images                                       |
| SOCI not in use †                                                                                | EKS   | No SOCI index referrer for the image, or the node image lacks SOCI / parallel pull                                                                                | Medium–high                                | Enable SOCI (parallel-pull mode on AL2023 / Bottlerocket) and build the index                          |
| Artifact Streaming off †                                                                         | AKS   | ACR not Premium, or streaming not enabled on the node pool                                                                                                        | Medium                                     | Enable ACR Artifact Streaming                                                                          |
| No P2P or in-cluster cache at fan-out                                                            | all   | ≥ N accelerator replicas of one image and no Spegel, Dragonfly or pull-through cache                                                                              | High when scaling out                      | Spegel (one small DaemonSet) or Dragonfly, as `MLStack.pull_cache`                                     |
| Image not warm on GPU nodes                                                                      | all   | Target image absent from `node.status.images` on the pool                                                                                                         | Medium                                     | Pre-pull DaemonSet or kube-fledged, or a baked node image                                              |
| Local NVMe unused                                                                                | all   | GPU nodes with local SSD where ephemeral storage is not on it                                                                                                     | Medium                                     | Put ephemeral storage on local SSD (node-pool setting)                                                 |
| Model downloaded per pod                                                                         | all   | Init or storage-initializer fetch from `s3://`/`gs://`/`hf://` on each start; no LocalModelCache                                                                  | High                                       | `Workload.model` by digest; KServe LocalModelCache                                                     |
| Slow loader                                                                                      | all   | vLLM args without `runai_streamer` / `tensorizer`; non-safetensors weights                                                                                        | Medium                                     | safetensors + Run:ai Model Streamer                                                                    |
| Hugging Face download unaccelerated                                                              | all   | Pods fetch from `hf://` without `HF_HUB_ENABLE_HF_TRANSFER` / Xet                                                                                                 | Medium                                     | Enable it, or stop downloading per pod                                                                 |
| GPU node provisioning dominates                                                                  | all   | `TriggeredScaleUp` event; scale-up to Ready time                                                                                                                  | High                                       | Warm pool / minimum nodes, or capacity reservations (their choice)                                     |
| Image volumes unavailable †                                                                      | all   | Server version below the default-on release, or containerd < 2.1 (`nodeInfo.containerRuntimeVersion`)                                                             | Enables mounting weights without a sidecar | Upgrade; meanwhile the modelcar sidecar or init-container fallback                                     |

## Appendix B: discovery signatures

A component is "present" when its CRD **and** a running controller match, or when
one of the listed workload signatures matches. Discovery records which signature
matched.

| Component                               | Detection (any of)                                                                                                                       |
| --------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------- |
| NVIDIA GPU Operator                     | CRD `clusterpolicies.nvidia.com` and a ClusterPolicy; Deployment image `*gpu-operator*`                                                  |
| NVIDIA device plugin                    | Node capacity `nvidia.com/gpu` or `nvidia.com/mig-*`; DaemonSet image `*k8s-device-plugin*`; GKE `nvidia-gpu-device-plugin*`             |
| DCGM exporter                           | Pod image `*dcgm-exporter*` in any namespace, including GKE's `gke-managed-system`; ClusterPolicy `spec.dcgmExporter.enabled`; port 9400 |
| DCGM host engine                        | Pod image `*/dcgm:*` or container `nvidia-dcgm`; ClusterPolicy `spec.dcgm.enabled`                                                       |
| MIG / sharing                           | Labels `nvidia.com/mig.config.state`, `nvidia.com/gpu.replicas > 1`, `nvidia.com/gpu.sharing-strategy`                                   |
| AMD                                     | Capacity `amd.com/gpu`; CRD `deviceconfigs.amd.com`; image `*device-metrics-exporter*`                                                   |
| TPU                                     | Capacity `google.com/tpu`; label `cloud.google.com/gke-tpu-accelerator`                                                                  |
| Neuron                                  | Capacity `aws.amazon.com/neuron*`; images `*neuron-device-plugin*`, `*neuron-monitor*`                                                   |
| Gaudi                                   | Capacity `habana.ai/gaudi`; image `*habana*`                                                                                             |
| DRA                                     | API group `resource.k8s.io` served and ResourceSlices present                                                                            |
| Prometheus Operator / GMP               | CRDs `servicemonitors.monitoring.coreos.com`, `podmonitorings.monitoring.googleapis.com`                                                 |
| kube-state-metrics                      | Image `*kube-state-metrics*`                                                                                                             |
| OTel collector (theirs)                 | Image `*opentelemetry-collector*` without forge's labels; CRD `opentelemetrycollectors.opentelemetry.io`                                 |
| node-problem-detector                   | Image `*node-problem-detector*`; node condition types beyond the five core ones                                                          |
| EKS node monitoring agent               | Image `*eks-node-monitoring-agent*`; condition `AcceleratedHardwareReady`                                                                |
| Kueue / Volcano                         | CRD group `kueue.x-k8s.io` and image `*kueue*` / group `batch.volcano.sh`                                                                |
| JobSet, Trainer, Training Operator, LWS | `jobsets.jobset.x-k8s.io`, `trainjobs.trainer.kubeflow.org`, `pytorchjobs.kubeflow.org`, `leaderworkersets.leaderworkerset.x-k8s.io`     |
| KubeRay                                 | CRD group `ray.io` and image `*kuberay-operator*`                                                                                        |
| KServe                                  | CRD group `serving.kserve.io`                                                                                                            |
| Gateway API Inference Extension         | CRD `inferencepools.inference.networking.k8s.io` (or `.x-k8s.io`)                                                                        |
| vLLM / Triton                           | Pod image `*vllm*` or args `vllm serve`; image `*tritonserver*`                                                                          |
| KEDA                                    | CRD `scaledobjects.keda.sh`                                                                                                              |
| Spegel / Dragonfly                      | DaemonSet image `*spegel*` / `*dragonfly*dfdaemon*`                                                                                      |
| Spot capacity and handlers              | Spot labels (section 5); image `*aws-node-termination-handler*`; CRD `nodepools.karpenter.sh`                                            |

**The read set:**

- core: `nodes`, `pods`, `services`, `endpoints`, `namespaces`, `events`;
- `discovery.k8s.io` `endpointslices`;
- `apps` `daemonsets`, `deployments`, `statefulsets`, `replicasets`;
- `batch` `jobs`, `cronjobs`;
- `apiextensions.k8s.io` `customresourcedefinitions`;
- the monitoring CRD groups above;
- the ML CRD groups above;
- `resource.k8s.io`;
- `autoscaling` `horizontalpodautoscalers`.

Every one of these is `get`, `list` and `watch`, plus `get` on `nodes/metrics`.
Nothing else.

## Appendix C: what to verify before building

The research for this proposal could not check every external fact against a
live source. Before any of these is implemented or quoted to a customer, confirm:

- the Kubernetes release in which the `ImageVolume` gate is on by default, and
  the KServe version that mounts modelcars as image volumes;
- the GKE, AKS and EKS node labels for Image Streaming, secondary boot disks
  and Artifact Streaming, and the vendor speed-up figures;
- the AMD, Neuron and Gaudi exporter ports and metric names, and the TPU
  metrics in Cloud Monitoring;
- the DCGM metric renames by version, and the vLLM V1 metric names;
- the CNCF ModelPack media types;
- registry layer caps for GAR, ACR and Docker Hub (ECR allows 200 GB per layer,
  checked);
- whether the hub's connected shard has a stable, publishable egress address
  set (decision 1 (a));
- that `REGISTRY_REDIRECT` works in-cluster (the GCS signer has never been
  exercised), and the region of the `reliant-prod-registry` bucket, which the
  research did not find.

Checked in this pass:

- `KubeletTracing` and API server tracing went GA in Kubernetes 1.34
  (CHANGELOG-1.34).
- DRA's `resource.k8s.io` APIs went GA in 1.34.
- Neptune's SaaS closed on 2026-03-05.
- NVIDIA open-sourced the Run:ai scheduler as KAI Scheduler (Apache-2.0).

## Appendix D: research

The research notes behind this proposal, with full citations, are kept outside
the repository with the chat that produced it:

- `what-we-have-platform.md`: connect, delivery, telemetry, console nav;
- `what-we-have-kcl.md`: the KCL schema and attachment points;
- `primitives.md`: accelerators, health, orchestration, inference, startup;
- `artifacts-and-pull-speed.md`: registries, model-as-OCI, the cold-start
  checklist;
- `competitive.md`: W&B, MLflow, Run:ai, HyperPod, SkyPilot and others.
- `registry-gap.md`: what Reliant's registry has today and what model weights
  need (section 3.7).

The code citations above were checked against forge `main` at 175acbe5f,
control-plane, and reliant on 2026-11-25.
