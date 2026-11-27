# ML training and inference in your own clusters

**Status:** proposal. Nothing here is built. The owner settled the direction on
2026-11-25 (the "Settled" list below; items 7 and 8 came from the owner's
review of forge#2836, items 9 and 10 from the owner's direction after it
merged). On 2026-11-26 the owner accepted every remaining recommendation
("take your recommendations on all"): decisions 1 to 22 are settled (the
[Settled (owner, 2026-11-26)](#settled-owner-2026-11-26) list), and
[section 11](#11-owner-decisions-decided-2026-11-26) and
[decisions 17 to 22](#decisions-17-to-22-decided-2026-11-26) keep the options
that were weighed. Details not named there remain proposed until their slice's
PR.
**Date:** 2026-11-25
**North star:** [reliant-for-ml.md](reliant-for-ml.md) (the practices Reliant
gives apps, for ML: resilience at scale, feedback-loop hooks, testing and
previews, model security), which this proposal feeds.
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
| A per-org feature entitlement                                                           | **None.** `grants.yaml` grants plans (and needs a release), `LimitsOverride` is per subscription, the claimable switches are global, and `CHANGES_UI` is per deployment. No RPC tells the console what an org may use ([section 4.1](#41-access-by-request)).                                                                                                                  |
| The demo shell, `demo.reliantlabs.io`                                                   | Released (reliant v1.29.0). One fixture transport answers every RPC from the `fixtures/shop` pack, read-only (reliant `frontends/web/src/fixtures/transport.ts`, `components/Forge/Demo/demoTransport.ts`).                                                                                                                                                                    |
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

**Access is by request.** ML is off for every org until Reliant grants it.
The forge types and CLI still parse, render and run without it. Everything the
control plane does for ML refuses without the grant, and says how to ask for
it. The real console's ML item opens a request-access page. The demo at
`demo.reliantlabs.io` shows the whole experience on fixture data. It is built
and tested without GPUs ([section 13](#13-testing-without-gpus)).

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

   The label is **"ML"**
   ([decision 9](#11-owner-decisions-decided-2026-11-26), settled 2026-11-26).

   **Amended (owner, 2026-11-26):** the nav is **hybrid**
   ([reliant-for-ml.md](reliant-for-ml.md#21-what-this-means-for-navigation),
   decision 23). An ML area holds what has no app equivalent: **Training**
   (not "Runs", which the nav already uses for workflow runs), Evals, Models,
   Fleet and the stack setup. Model releases live in Deployments, smoke runs in
   Previews, GPU telemetry in Observability, alerts in Inbox and eval gates in
   Tests. Where this document says "the six pages" or "the one ML item", read
   the hybrid nav.

9. **Access is gated, per org, by Reliant** (owner, 2026-11-25). The owner
   will not offer an experience nobody has tested on real GPUs, and will not
   pay for real GPUs to test it. So ML is off for every org unless Reliant
   grants it ([section 4.1](#41-access-by-request)):
   - **Server-side, per org.** The control plane checks an entitlement in
     every ML handler that ingests, recommends or shows. Without it, the
     handler refuses with an error that names how to request access.
   - **forge stays usable piece by piece.** The KCL types, `forge env render`,
     `forge lint` and the local `forge ml discover` work without the
     entitlement, because none of them calls the control plane's ML features.
   - **The real console** shows the ML nav item to everyone. Without the
     entitlement it opens a "Reach out to Reliant for access" page with a
     request-access action, so it is never a dead end.
   - **The demo shell** (`demo.reliantlabs.io`) shows the full ML experience,
     unblocked, on fixture data.
   - **Public docs and the landing page call it "by request".** They make no
     claim of general availability.
10. **Built and tested without GPUs.** The test strategy runs on CPU-only k3d
    ([section 13](#13-testing-without-gpus)):

    - run-ai's fake-gpu-operator;
    - KWOK;
    - CPU inference and training;
    - synthetic exporter fixtures.

    There is no GPU spend. What only hardware can prove is checked on the
    first early-access design partner's cluster before access widens
    ([decision 12](#11-owner-decisions-decided-2026-11-26), settled
    2026-11-26).

11. Standing rules that apply unchanged:

- Monitoring requires sign-in.
- forge never stands in for another tool.
- forge is semver at major 1, so every surface here is additive.
- Every piece is usable alone ("start anywhere").
- Recovery rolls forward.

## Settled (owner, 2026-11-26)

The owner accepted the recommendation of every open decision in
[section 11](#11-owner-decisions-decided-2026-11-26) and
[decisions 17 to 22](#decisions-17-to-22-decided-2026-11-26) ("take your
recommendations on all"). Decision 18 was settled earlier the same day.
The owner then overrode decision 12 (2026-11-26 16:42Z): no GPU spend.

1. **Private GPU clusters:** a public endpoint restricted to published hub
   egress addresses now, after verifying that the connected shard's egress is
   a stable, publishable set. **Pull-mode delivery** (Flux pulls the release
   bundle outbound; telemetry pushes outbound) is the durable path.
2. **Discovery:** the local `forge ml discover` for the first look, plus an
   in-cluster collector with a declared read-only ClusterRole, derived into the
   connect grant, for continuous discovery. No standing hub-side discovery
   ServiceAccount.
3. **Vendor metric adapters:** forge ships accelerator adapters as versioned
   data, a narrow exception to third-party-telemetry's no-presets rule, kept
   outside the collector.
4. **Run tracking:** Reliant owns the run record, accelerator metrics and
   lineage, accepts an MLflow-compatible tracking API and links out to W&B. Our
   own charting UI is revisited only on demand.
5. **Runs:** templates are declared, runs are recorded launches against them,
   and reproducible runs may be pinned in KCL. Observing runs others create is
   always on.
6. **Cross-cloud pull egress:** meter first (R5), then a free regional
   allowance, with cross-cloud pulls billed.
7. **Model releases:** the model digest is part of the app release, and a
   model-only release carries code artifacts forward (`CarriedFrom`). A digest
   pinned per env in `main.k` is allowed as an explicit pin.
8. **Online comparison of model versions:** in-app Changes variants now;
   router-declared splits and shadows (KServe, Gateway API, Inference
   Extension) later, as a deliberate ML exception.
9. **The ML nav label is "ML".**
10. **Disconnect** also removes the components labelled `forge-ml` (our
    installs, the collector). Customer workloads stay.
11. **Pricing:** observability ingest plus a free tier of N accelerators
    through beta. A per-accelerator-hour fee is decided later, with dogfood and
    early-customer data.
12. **No GPU spend** (owner, 2026-11-26 16:42Z, overriding the
    recommendation). There is no standing Spot GPU pool, or any GPU we pay
    for. Testing uses fake GPUs (fake-gpu-operator), KWOK and CPU workloads
    (settled item 10). The first early-access design partner's cluster, with
    their consent, is the real-hardware proof before access widens beyond
    design partners.
13. **Model storage:** a separate `models/` allowance per plan, sized for
    weights and billed beyond it through the storage pool. `models/` is refused
    for unclaimed orgs.
14. **Referrers:** keep distribution with the `sha256-<hex>` tag fallback and
    add `models/` to sigguard's protected set now; move to a distribution
    release that serves referrers when it ships.
15. **Connected-cluster pull credential:** a new token per (cluster, env)
    binding, read-only and resource-bound to the project's `models/` (and
    image) paths.
16. **ML entitlement storage:** a DB table with admin RPCs and an audit trail,
    like the claimable switches.
17. **Hardware metrics:** 30s raw for the tenant's 30 days now; a 5-minute
    per-device rollup kept for 13 months when a measured workload hits the read
    quota or a design partner asks for history.
18. **Training instrumentation:** a published Python SDK with PyTorch support,
    taught by the `ml/instrument` skill (settled earlier, in
    [reliant-for-ml.md](reliant-for-ml.md)).
19. **GPU profiler captures** live in the customer's checkpoint store, linked
    from the run.
20. **The pod-start timeline** is derived at query time from events and
    conditions, never materialised as synthetic spans.
21. **Cost attribution:** a price per accelerator product declared by the
    customer in `MLStack`, plus OpenCost's prices where it is installed (adopt
    first).
22. **Eval gates:** display only first; a `DeliveryPolicy` threshold on a
    recorded eval metric after a design partner confirms which metrics they
    trust.

These are not reopened below.

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
can be pinned in KCL. This is [decision 5](#11-owner-decisions-decided-2026-11-26).

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
| `Accelerators`                                               | `vendor: "nvidia" \| "amd" \| "tpu" \| "neuron" \| "gaudi" \| "generic"`; `resource?` (default from vendor, e.g. `nvidia.com/gpu`); `metrics?: Adopt \| Install`; `node_selector?`                                                                                                                | `Adopt`: a `forge.Telemetry` scrape with the vendor adapter's `allow` list ([decision 3](#11-owner-decisions-decided-2026-11-26)). `Install`: a HelmChart, refused if inventory shows any exporter on those nodes                  |
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
| R4  | The OCI referrers API                  | None in distribution v3.1.2; signatures use the `sha256-<hex>` tag fallback (`sigguard/guard.go:33-36`).                                                                                              | Referrers for model signatures, SBOMs and eval attestations. Either a distribution release that serves it, or a registry that does (zot, Harbor). That replacement is a control-plane architecture choice ([decision 14](#11-owner-decisions-decided-2026-11-26)). Until then, the tag fallback, with `models/` joining sigguard's protected set so only the platform signer can write signature tags.                                                                                                                                                                                                                                                                               |
| R5  | Region placement and egress            | One region (us-central1). A pull into an EKS cluster in eu-west-1 is cross-cloud internet egress, unmetered and unpriced.                                                                             | A registry-pull egress meter first. Then placement: either regional mirrors (GCS dual- or multi-region buckets plus redirect, so the signed URL is near the puller), or an **in-cluster pull-through cache** the customer installs (`MLStack.pull_cache`: Spegel, Dragonfly, or a distribution or Harbor proxy cache). With a cache, the bytes cross clouds once per digest, not once per node. Who pays is [decision 6](#11-owner-decisions-decided-2026-11-26).                                                                                                                                                                                                                    |
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

### 4.1 Access by request

Nothing in the control plane today is a per-org feature switch that Reliant
grants and the console can read. These are the nearest mechanisms, all
released, and why each misses:

| Mechanism                                                                                                                                                 | Why it is not the ML gate                                                                                                                                                                                                     |
| --------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Declared plan grants, `internal/plansconfig/grants{,.e2e,.prod}.yaml`                                                                                     | They can target an `org_id`, but they grant a **plan**; every change is a release; and there is no RPC ("BillingAdminService.AssignPlan was deleted", `grants.yaml:8-14`). The file is picked per env (`plansconfig.go:247`). |
| `LimitsOverride` (`internal/planlimits/override.go:20`)                                                                                                   | Per subscription, with a closed key list.                                                                                                                                                                                     |
| Claimable switches (`proto/services/deploy_admin/v1/deploy_admin.proto:184-222`; table from `db/migrations/20261027151013_claimable_admission.up.sql:66`) | **Global** kill switches, not per org. The _shape_ is the one to copy: a DB row; an admin RPC that requires a reason and records who and when; effect within seconds with no deploy; generated internal-console hooks.        |
| `platform_org_ids` (`proto/control_plane/config/v1/config.proto:2717`)                                                                                    | A per-org allowlist for one purpose; each change is a deploy; the console cannot read it.                                                                                                                                     |
| Org policy (`proto/services/org_policy/v1/org_policy.proto:31-121`)                                                                                       | Set by the customer's own admins: the wrong owner for a grant Reliant makes.                                                                                                                                                  |
| `CHANGES_UI` (reliant `proto/reliant/config/v1/web_config.proto:234`)                                                                                     | One flag per deployment, read by `changesUIEnabled()` to show Experiments in the nav (reliant `navConfig/items.tsx:434-445`).                                                                                                 |

`BillingService.GetCurrentUserComputeEligibility`
(`proto/services/billing/v1/billing.proto:830-843`) states the right read
pattern. The authoritative gate is server-side, and the RPC reports the same
facts so the UI can predict that gate without owning it.

**Proposed** (all control-plane, all additive):

1. **Storage.**
   - A table `controlplane.org_entitlements`: `org_id`, `feature`, `reason`
     (required), `granted_by`, `granted_at`, `revoked_at`, `revoked_by`,
     `revoke_reason`, and `requested_at` / `requested_by`.
   - The primary key is `(org_id, feature)`, and the migration is
     `forge:backward-compatible`.
   - The feature set is closed by a CHECK, today `IN ('ml')`, the way
     `ck_cp_claimable_switch_name` closes the switch set.
   - Revoking is soft, so the audit trail survives.
2. **The check.** A package, `internal/entitlement`:

   - `Has(ctx, org, feature)`, cached for seconds like the switches;
   - `Require(ctx, org, feature)`, which every ML handler calls first.

   Handlers check inline today; no interceptor does it. Refusal is
   `svcerr.PermissionDenied`, with `svcerr.WithReason(…, "FEATURE_NOT_ENTITLED")`
   and metadata `feature=ml` (forge `pkg/svcerr/svcerr.go:269,1023,1070`). The
   message names the next step, following the convention of
   `internal/computegate/gate.go:156-161`:

   > ML is available by request and is not enabled for this organization.
   > Request access in the console (ML → Request access) or email
   > support@reliantlabs.io with your organization id.

   It is PermissionDenied, not FailedPrecondition, because nothing the caller
   changes makes it pass. The reason code is what the CLI and the web branch
   on. The CLI prints the message and exits non-zero; this is a new code in
   its `--json` documents, so it is additive.

   What is gated: `MLService`, the ML inventory and findings ingest, the
   recommendations engine, and the ML read RPCs. **What is not gated:**

   - forge's KCL types and render;
   - `forge lint`;
   - the local `forge ml discover`;
   - forge's generic observability. A `forge.Telemetry` scrape of an exporter
     keeps working under the org's normal observability, as it does today.
     Only the ML-specific queries, inventory and rules are gated.

3. **Grant path (Reliant admins).** `GrantOrgEntitlement`,
   `RevokeOrgEntitlement` and `ListOrgEntitlements`, beside `SetClaimableSwitch`
   in `deploy_admin.proto`. A reason is required and the actor is recorded.
   They are used through
   `control-plane ops call --env prod DeployAdminService/GrantOrgEntitlement '{"org_id": …, "feature": "ml", "reason": …}'`
   and an internal-console page over the generated hooks.
4. **The console's read.** `OrganizationService.GetMyOrgEntitlements`
   (`auth_required`, `NO_SIDE_EFFECTS`), returning
   `features[{feature, enabled, requested_at}]`. The console puts it on `NavCtx`,
   which already carries per-app console state such as `ctx.console?.navCounts`
   (reliant `navConfig/types.ts:32-49`). The console reads this to decide what
   to draw; the server still decides.
5. **Request access.** Nothing like it exists today; there are only support@
   mailtos (reliant `components/Settings/cloud/usage/UsageBillingCard.tsx:54-55`)
   and error text. `OrganizationService.RequestOrgEntitlement(feature, note)`:

   - records `requested_at` and `requested_by`, idempotently per org and
     feature;
   - posts to Reliant's Slack through an existing sender (`internal/alerting`);
   - emits a product event, `ml_access_requested`.

   A mailto is the fallback when the RPC fails.

Where the entitlement is stored was a sub-decision, settled on 2026-11-26 as a
DB table with admin RPCs and an audit trail. A declared `features:` list in
`grants.yaml` would also be reviewed in git, but every grant would need a
release ([decision 16](#11-owner-decisions-decided-2026-11-26)).

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
logger they already use. So the decision is
[decision 4](#11-owner-decisions-decided-2026-11-26) (b): Reliant owns the parts nobody joins
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
- **Later, as a deliberate ML exception:** for model versions, declare the
  split through the router the cluster already runs: KServe
  `canaryTrafficPercent`, Gateway API `HTTPRoute` weights, or the Inference
  Extension's InferencePool. forge declares, the router routes, and the readout
  joins the two variants' metrics. In-app Changes variants come first. This is
  [decision 8](#11-owner-decisions-decided-2026-11-26) (settled 2026-11-26: (a)
  now, (b) later).

## 7. Releasing models

A model rides the existing release. `forge env build prod --release v13` records
the model's digest beside the image digests, and `forge env deploy prod v13`
promotes it. Promotion runs `forge obs check prod --wait`, which then judges
inference findings too.

**Model-only releases.** The cadence that differs from code is a new model with
unchanged code. The decided answer
([decision 7](#11-owner-decisions-decided-2026-11-26)) is a new release whose code artifacts
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

## ML observability: where time and money go

**Status:** proposal, added 2026-11-25 at the owner's request: "we need
heavily: tracing/profiling, metrics. What will help people more? drill downs
into scheduling, inferencing, where is time spent? drill downs into model runs
and training, dashboards on the hardware (temp, DCGM stats), eval metrics,
more?" Nothing here is built. It refines sections 3.3, 5, 6 and 8 rather than
replacing them. Its decisions are numbered 17 to 22, after section 11's, and
are settled (owner, 2026-11-26).

### What helps most, in order

The order is by how many teams it helps on the day they connect, and by how
little they must change to get it.

1. **Hardware dashboards.** Every GPU team needs them, and they need no
   customer code: the DCGM exporter is already on most clusters (the GPU
   Operator ships it), and we adopt it.
2. **Scheduling and pod start: why it waited.** The first question on any
   shared GPU cluster is "why hasn't my job started?". The answer is already in
   Kubernetes events and Kueue's conditions. No customer code.
3. **Inference: where a request's time goes.** Queue wait, prefill against
   decode, time to first token, tokens per second, KV-cache pressure, per model
   release, judged by `forge obs check`. The serving runtimes already export
   it.
4. **Training: where a step's time goes.** Data loading, forward, backward,
   communication, checkpointing, GPU stalls and MFU. The most valuable for a
   large run and the only one that needs code in the training loop, so it ships
   with the first design partner.
5. **Runs.** Progress, ETA, restarts and preemptions, and a diff of config,
   data, commit and metrics. We import from MLflow and link to W&B rather than
   rebuild them (decision 4).
6. **Evals.** Offline results recorded on model versions; online comparison
   through Changes and `experimentstats`.

Each needs one more thing the others share: a **cost axis**. Accelerator-hours
attributed to a namespace, a run or an inference service, idle hours, and,
where the customer declares a price, money per run and per million tokens
(decision 21).

### Shared foundations

Every drill-down reuses what forge already has. None adds a second pipeline.

| Already in forge                                                                                                                                                                                                                             | What the drill-downs use it for                                                                                                                  |
| -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------ |
| `forge.Telemetry` `MetricsScrape` with `allow`, `drop_labels`, `sample_limit` (default 5000) and `label_limit` (`kcl/telemetry.k:229-262`)                                                                                                   | Scraping the adopted DCGM exporter, Kueue's controller and the serving runtimes, with cardinality bounded by construction                        |
| Telemetry dashboards as code: a generated system dashboard per Telemetry (`internal/obsdash/telemetry.go:364`), plus declared `dashboards: [dashspec.Dashboard]` (`kcl/telemetry.k:191-199`), replaced by a hand file                        | The generated hardware, queue and inference dashboards. They ship in the release and draw in the console and in local HyperDX                    |
| `pkg/dashspec` panel kinds, including `heatmap` (`pkg/dashspec/spec.go:44-48`) and the console-drawn `release_health` (`:100`)                                                                                                               | Fleet utilisation as a distribution over time; release health per model release                                                                  |
| `pkg/obsquery`'s metric evaluator (`metric_eval.go:278`), exemplars (`exemplars.go:25`) and its 50-series cap per panel (`metrics.go:22`)                                                                                                    | Every chart. The cap is why fleet panels are distributions and top-N tables, never one line per GPU                                              |
| Kubernetes events in `obsquery`: `Workloads.Events()` and `Jobs()` read `k8s.event.reason` and `k8s.object.kind` that a cluster collector delivers (`pkg/obsquery/workloads.go:26-31, 288, 637`)                                             | The scheduling timeline. Section 3.2's cluster collector brings the same attributes to connected clusters, so these queries work there unchanged |
| Continuous profiling: forge's Go runtime pushes OTLP profiles (`pkg/observe/profiler.go`), `PprofScrape` pulls pprof from anything (`kcl/telemetry.k:437`), and a flamegraph can be narrowed to one trace (`pkg/obsquery/profiles.go:18-29`) | CPU flamegraphs of serving and training processes, linked from a run or a slow request                                                           |
| The GenAI span shape, `pkg/observe/genai` (v1.7.0), and the console's AI hub built over it (`pkg/obsbuiltin`, v1.5.0)                                                                                                                        | Joining an app's `chat <model>` span to the model server's own request span, in one trace                                                        |
| Release health (`pkg/releasehealth`): before/after windows gated by `min_requests` (`baseline.go`), changed ops, attribution                                                                                                                 | Judging a model release on inference SLIs (section 4's metric finding type)                                                                      |
| Runtime context ([runtime-context.md](runtime-context.md), accepted, in implementation): telemetry joined to code, diff and agent                                                                                                            | A training regression traced to the commit that changed the template                                                                             |

### Drill-down 1: hardware dashboards

**The questions it answers.** Is this GPU busy, or only allocated? Is it doing
tensor math or waiting on memory? Is it throttling, and why: power, heat or a
hardware slowdown? Which device is about to fail? Is the interconnect the
bottleneck?

**Data.** The adopted vendor exporter, through the neutral field set of
[section 3.4](#34-one-accelerator-model). For NVIDIA, the DCGM exporter. Its
default counter file omits several of these fields, so each row names its field
and what happens when it is absent:

| Neutral field               | DCGM field (verify names against the discovered exporter version)                                                                                                                 | When absent                                                                              |
| --------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------- |
| Utilisation                 | `DCGM_FI_PROF_GR_ENGINE_ACTIVE`, else `DCGM_FI_DEV_GPU_UTIL` (not supported on MIG instances)                                                                                     | `accel-telemetry-missing`: allocation only                                               |
| SM activity and occupancy   | `DCGM_FI_PROF_SM_ACTIVE`, `DCGM_FI_PROF_SM_OCCUPANCY`                                                                                                                             | `dcgm-profiling-off`                                                                     |
| Tensor activity             | `DCGM_FI_PROF_PIPE_TENSOR_ACTIVE` (plus the FP16/FP32/FP64 pipes)                                                                                                                 | `dcgm-profiling-off`                                                                     |
| Memory used and bandwidth   | `DCGM_FI_DEV_FB_USED` / `FB_FREE`; `DCGM_FI_PROF_DRAM_ACTIVE`                                                                                                                     | memory always; bandwidth needs profiling                                                 |
| Power and energy            | `DCGM_FI_DEV_POWER_USAGE`, `DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION`, and the power limit (`DCGM_FI_DEV_POWER_MGMT_LIMIT`, not in the default file)                                  | no "power against limit" line                                                            |
| Temperature                 | `DCGM_FI_DEV_GPU_TEMP`, `DCGM_FI_DEV_MEMORY_TEMP` (HBM)                                                                                                                           | —                                                                                        |
| Clocks and throttle reasons | `DCGM_FI_DEV_SM_CLOCK`, `DCGM_FI_DEV_MEM_CLOCK`; the reasons bitmask `DCGM_FI_DEV_CLOCK_THROTTLE_REASONS`, renamed `DCGM_FI_DEV_CLOCKS_EVENT_REASONS` in DCGM 3.3                 | `accel-throttle-reasons-off` (new, below)                                                |
| Errors                      | `DCGM_FI_DEV_XID_ERRORS` (the value is the **last** Xid code, not a count, so a change of value is the event), ECC SBE/DBE totals, row-remap failure, PCIe replay counter         | `accel-error-counters-off`                                                               |
| Interconnect                | `DCGM_FI_DEV_NVLINK_BANDWIDTH_TOTAL` and the NVLink CRC, replay and recovery error totals; `DCGM_FI_PROF_PCIE_TX_BYTES` / `RX_BYTES`, `DCGM_FI_PROF_NVLINK_TX_BYTES` / `RX_BYTES` | `accel-interconnect-off` (new, below)                                                    |
| Attribution to a pod        | the exporter's `pod`, `namespace` and `container` labels (`DCGM_EXPORTER_KUBERNETES=true`)                                                                                        | `accel-pod-attribution-off` (new): per-run and per-service views fall back to allocation |

The throttle-reasons bitmask is decoded into named reasons (power cap, thermal
slowdown in software and in hardware, hardware slowdown, sync boost, idle) by
the adapter's versioned data ([decision 3](#11-owner-decisions-decided-2026-11-26)), so the
dashboard shows "share of time throttled, by reason" instead of an integer.

**Dashboards as code.** forge generates them, the way `obsdash` already draws a
system dashboard for every `forge.Telemetry`: a new builder reads the env's
`MLStack`, the adapter's field map and the exporter version from the last
inventory, and writes `deploy/observability/dashboards/<project>-ml-hardware-<cluster>_gen.json`.
It ships in the release like every generated dashboard, and a hand file
`<project>-ml-hardware-<cluster>.json` replaces it. Its sections:

- **Fleet:** accelerators allocated against used, per pool; a utilisation
  heatmap (the distribution of per-device utilisation over time); the
  allocated-but-idle devices, as a top-N table.
- **Pool:** SM and tensor activity p50 and p95; memory used; power against
  limit; hottest device; share of time throttled, by reason; error counts.
- **Node:** one line per device (eight fit well under the 50-series cap);
  NVLink and PCIe throughput; Xid and ECC events on the same time axis as the
  pods that held each device.

**Per-vendor degradation.** Every panel names a neutral field. When the
adopted exporter does not provide it (a vendor without the field, an exporter
version without it, or a counter left out of the CSV), the panel says what is
missing and links the recommendation that fixes it, instead of drawing an empty
chart. The generic fallback (any extended resource, no exporter) draws
allocation only. Under MIG, utilisation is per GPU instance and
`DCGM_FI_DEV_GPU_UTIL` is unavailable, so the panels switch to the profiling
fields per instance. Under time-slicing, per-pod attribution is marked as not
meaningful (section 3.3).

**Console.** ML → Machines, drilled: cluster → pool → node → device. The node
view puts the hardware panels beside what ran there (pods with accelerator
allocations over time, from the collector's watch) and its health events (Xid,
node-problem-detector conditions). Each device row links to the generated
dashboard for the long view.

**Slice 1.** No customer code.

### Drill-down 2: scheduling and pod start, and why it waited

**The questions it answers.** Why hasn't my job started? Is it quota,
capacity, preemption, a taint, or a slow image? How long does a new inference
replica take to serve, and which stage dominates? How much accelerator time
does the cluster lose to waiting?

**The timeline.** One row per pod attempt (and per Kueue workload above it),
built from what the cluster already records. It extends the six cold-start
stages of [section 3.6](#36-cold-start) with the queue in front of them:

| Stage                | From → to                                                    | Source (no new privilege unless noted)                                                                                                                            | Why it waited: the reasons we classify                                                                                                                                          |
| -------------------- | ------------------------------------------------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Queued               | Kueue Workload created → `QuotaReserved`                     | Workload conditions from the collector's `k8sobjects` watch; Kueue's `kueue_admission_wait_time_seconds` and `kueue_pending_workloads`                            | The condition message: insufficient unused quota in a flavor, the cohort's borrowing limit, waiting behind higher priority. Requeues after `Evicted` or `Preempted` are counted |
| Admission checks     | `QuotaReserved` → `Admitted`                                 | Workload conditions                                                                                                                                               | A pending admission check, e.g. a ProvisioningRequest                                                                                                                           |
| Scheduling           | pod created → `PodScheduled`                                 | the pod condition; `FailedScheduling` events                                                                                                                      | The event message, classed: insufficient `nvidia.com/gpu` (or memory, CPU), untolerated taint, node selector or affinity, topology spread, volume binding, pending preemption   |
| Node provisioning    | scale-up triggered → node `Ready`                            | cluster-autoscaler events (`TriggeredScaleUp`, `NotTriggerScaleUp`); Karpenter NodeClaim conditions and events; node creation time and `Ready`                    | A pool at its maximum size, no matching node group, a cloud capacity or quota error                                                                                             |
| Sandbox and init     | `PodScheduled` → `PodReadyToStartContainers` → `Initialized` | pod conditions; `FailedMount` events                                                                                                                              | Network setup, a volume mount, an init container (KServe's storage-initializer downloading weights is the common slow case)                                                     |
| Image pull           | `Pulling` → `Pulled`                                         | the `Pulled` message ("in Xs (Ys including waiting). Image size: N bytes"); kubelet `kubelet_image_pull_duration_seconds` through `nodes/metrics`                 | Image size, serialised pulls, a registry in another region (Appendix A)                                                                                                         |
| Model load and ready | container started → `Ready`                                  | container state and the readiness condition; inside the container, an `ml.model.load` span from the instrumentation skill, or the serving runtime's own log lines | Weights fetched at start against weights loaded to the device (only an in-process signal can split these)                                                                       |

Where kubelet tracing is on (self-managed nodes only; section 5,
`kubelet-tracing-off`), the kubelet's own spans for sandbox creation, image pull
and container start are drawn inside the same row, joined by pod UID (verify
which attribute the kubelet's spans carry).

**How it is built.** At query time, as a new `obsquery.PodStarts` family over
the event logs and condition timestamps the collector already delivers. No new
storage, and nothing synthesised into the trace store ([decision 20](#decisions-17-to-22-decided-2026-11-26)).
Events expire from the API server after an hour by default, so only the
collector's watch makes the timeline durable. That is why
`ml-events-not-collected` (below) exists.

**Not ML-only.** Pod-start drill-down helps every forge workload in a connected
cluster. `obsquery.PodStarts` serves the existing Workloads view as well, and
the ML area adds Kueue and accelerator context on top.

**Console.**

- **Runs:** every attempt's timeline, with the stage that dominated it and the
  reason it waited.
- **Inference:** cold start per replica, and scale-up latency (from queue depth
  rising to the new replica being ready).
- **Machines → Queues tab:** per ClusterQueue and flavor, quota against usage,
  pending workloads with their reasons, admission wait p50 and p95, and
  preemptions. It is a tab of Machines, not a seventh page: the six pages are
  settled (item 8).
- **Org summary:** pending accelerator demand against free capacity, by
  reason. This is the "why is everything waiting" view.

**Slice 2.** No customer code. The kubelet and Kueue metrics come from adopted
scrapes, and the events from the cluster collector of slice 1.

### Drill-down 3: inference, and where a request's time goes

**The questions it answers.** Is latency from queueing, prefill or decode? Is
the KV cache full, so requests are preempted? Did the new model release make
time to first token worse? What does a million tokens cost on this hardware?

**Data.** The serving runtime's metrics through an adopted scrape, mapped to
neutral SLIs. Where the OpenTelemetry GenAI conventions name a server metric
(`gen_ai.server.time_to_first_token`, `gen_ai.server.time_per_output_token`,
`gen_ai.server.request.duration`; still in development, verify), the neutral
name is theirs, as `pkg/observe/genai` already does for spans.

| Neutral SLI              | vLLM (`:8000/metrics`; verify against the pinned version, the V1 engine renamed several) | Triton (`:8002/metrics`)                                |
| ------------------------ | ---------------------------------------------------------------------------------------- | ------------------------------------------------------- |
| Queue wait               | `vllm:request_queue_time_seconds`                                                        | `nv_inference_queue_duration_us` ÷ `nv_inference_count` |
| Time to first token      | `vllm:time_to_first_token_seconds`                                                       | — (backend-specific)                                    |
| Inter-token latency      | `vllm:inter_token_latency_seconds` (was `time_per_output_token_seconds`)                 | —                                                       |
| Prefill and decode       | `vllm:request_prefill_time_seconds`, `vllm:request_decode_time_seconds`                  | `nv_inference_compute_input/infer/output_duration_us`   |
| End-to-end               | `vllm:e2e_request_latency_seconds`                                                       | `nv_inference_request_duration_us`                      |
| Tokens per second        | rates of `vllm:prompt_tokens_total`, `vllm:generation_tokens_total`                      | —                                                       |
| Running and waiting      | `vllm:num_requests_running`, `vllm:num_requests_waiting`                                 | `nv_inference_pending_request_count`                    |
| Batch size               | `vllm:num_requests_running` (a proxy)                                                    | `nv_inference_count` ÷ `nv_inference_exec_count`        |
| KV-cache usage           | `vllm:gpu_cache_usage_perc`, `vllm:kv_cache_usage_perc` in V1                            | —                                                       |
| Preemptions, prefix hits | `vllm:num_preemptions_total`; `vllm:prefix_cache_hits` / `queries`                       | —                                                       |

KServe wraps one of these runtimes, so its metrics are the runtime's, plus its
own status for revisions and canaries. TGI and SGLang are adapters added the
same way, as versioned data (decision 3). A field a runtime does not have is
drawn as absent, never as zero.

**Traces.** vLLM can export a span per request over OTLP (verify the flag on the
pinned version). When it does, three things work with no new code:

- the existing span-based release health (server RED and changed ops) judges
  the model server like any service;
- the app's `chat <model>` span, from `pkg/observe/genai`, and the server's span
  share one trace when the caller propagates `traceparent`: the app's call, the
  server's queue, prefill and decode, and the device it ran on;
- the AI hub draws the caller's side unchanged.

**Per model release.** Each series carries the pod's release
(`service.version`, the attribute issues already key on,
`pkg/obsquery/server_issues.go:192`) and the model digest from
`Workload.model`. `forge obs check` gains section 4's metric findings: time to
first token p95, inter-token p95, queue wait p95, error share, and tokens per
second per accelerator, which catches an efficiency regression that latency
alone hides. Each finding has a relative and an absolute threshold, and is
judged only past `min_requests`, as now.

**Console.** ML → Inference, per service: the SLIs above per release, with the
promotion marked; time split into queue, prefill and decode; KV-cache and batch
size beside accelerator utilisation from drill-down 1; cold start from
drill-down 2; and, with a declared price, cost per million tokens. A slow
bucket opens its exemplar trace where the runtime emits exemplars
(`pkg/obsquery/exemplars.go`).

**Slice 3,** with inference releases. No customer code: the metrics are a
scrape, and tracing is a server flag the `ml/instrument` skill sets.

### Drill-down 4: training, and where a step's time goes

**The questions it answers.** Why is step time up 18% since 02:10? Is the job
input-bound, compute-bound or communication-bound? Which rank is the straggler,
and what is wrong with its GPU? What MFU are we getting?

**The step breakdown needs a signal from inside the loop.** No exporter knows
where a step begins. So this drill-down needs code in the training process, and
it ships with the first design partner. It defines a small contract, the
`ml.train.*` conventions, kept in forge until OpenTelemetry names its own:

| Instrument                                      | Kind                           | Attributes                                                                                                          |
| ----------------------------------------------- | ------------------------------ | ------------------------------------------------------------------------------------------------------------------- |
| `ml.train.step.duration`                        | histogram, seconds             | `ml.phase` ∈ {`data_wait`, `forward`, `backward`, `optimizer`, `comm_exposed`, `checkpoint`, `other`}: a closed set |
| `ml.train.rank.step.duration`                   | histogram, seconds             | `ml.rank` (bounded by world size), `ml.phase` ∈ {`total`, `comm_exposed`}                                           |
| `ml.train.step`, `ml.train.steps.total`         | gauges                         | —                                                                                                                   |
| `ml.train.tokens`, `ml.train.samples`           | counters                       | —                                                                                                                   |
| `ml.checkpoint.duration`, `ml.checkpoint.bytes` | histogram, counter             | `ml.checkpoint.step`                                                                                                |
| `ml.model.load`                                 | span                           | weights fetch against load to device                                                                                |
| `train.step`                                    | span, with one child per phase | sampled (below)                                                                                                     |

Resource attributes carry `ml.run.id`, `ml.training.name`, `ml.attempt` and
`ml.world_size`, so every series joins to its run, and through the run to its
release and commit. `ml.phase` is a closed set, which is exactly what lets the
generated custom-metrics dashboard group by it safely
(`internal/obsdash/custom_metrics.go:24-31`).

How it is measured matters more than what is named:

- **GPU work is timed with CUDA events, not wall clock.** Kernels run
  asynchronously, so CPU timestamps around `forward()` measure the launch, not
  the work. The events are read a step late, so the loop never synchronises for
  telemetry. Target overhead: under 1% of step time, measured in CI on CPU
  (gloo) and confirmed on the design partner's hardware.
- **Exposed communication** is the time a step waits on collectives that did
  not overlap compute. That is the number that costs money; total NCCL time
  mostly overlaps. The precise split comes from a profiler capture (below).
- **Spans are sampled in the process:** rank 0 every Nth step, every step slower
  than k times the trailing median on any rank, and every rank inside a
  declared profiling window. Never one span per step per rank.

**Stragglers.** In synchronous data parallelism the slowest rank's compute
shows up as every other rank's communication wait. So the rank with the
highest compute time and the lowest `comm_exposed` is the straggler. The view
names it, its node and its device, and puts drill-down 1's panels for that
device beside it. The finding reads: "rank 37 on node h100-5 is the
straggler; its GPU 3 has been in hardware slowdown at 87 °C since 02:08".
NCCL failures and hangs are section 5's `nccl-failures`, joined to the same
node.

**GPU stall.** The run's devices' SM activity (drill-down 1), on the same time
axis as the phase breakdown. A high `data_wait` share with low SM activity is
an input-bound job: `input-bound-training` (below) points at loader workers,
pinned memory and `/dev/shm` (`shm-too-small`).

**MFU** = (tokens per second × FLOPs per token) ÷ (devices × peak FLOPs per
device). The instrumentation reports FLOPs per token (6 × parameters for a
dense transformer in training, as a default the team can override). Peak FLOPs
per product and dtype are versioned adapter data keyed by the GPU Feature
Discovery product label. Tensor-pipe activity from DCGM is shown beside it as a
hardware proxy and labelled as one: the two are not the same number.

**The vehicle** is [decision 18](#decisions-17-to-22-decided-2026-11-26), settled by the owner
on 2026-11-26 for a **published Python SDK** with PyTorch support, taught by the
`ml/instrument` skill ([reliant-for-ml.md](reliant-for-ml.md), Settled item 2;
packaging is its decision 26: `sdk/python/` in forge, Apache-2.0, 0.x until a
design partner confirms the API). The SDK is a thin layer over upstream
OpenTelemetry Python, with callbacks for the Hugging Face Trainer, Lightning,
torchtitan, Megatron-LM and DeepSpeed, exporting OTLP to forge's collector.
forge never ships its own tracing library where OpenTelemetry is the tool.

**Profiles.**

- **CPU, continuous:** forge's own profiler is Go-only
  (`pkg/observe/profiler.go`). For Python, the OpenTelemetry eBPF profiler
  (Python and native frames, OTLP profiles; verify maturity) can be an `Adopt`
  or `Install` slot of `MLStack`. Its samples land in the existing profiles
  store and draw with `forge obs profile`. Raw profiles are kept 24 hours
  (control-plane `internal/obstenant/render.go:51-52`).
- **GPU, on demand:** PyTorch profiler (Kineto) traces and Nsight Systems
  reports are captured for a step window, either declared at launch
  (`forge ml run <training> --profile-steps 200-210`) or triggered by the
  instrumentation when step time regresses. They are stored as run artifacts and
  linked from the run. They open in Perfetto or Nsight. forge never converts or
  re-renders them. Where they are stored is
  [decision 19](#decisions-17-to-22-decided-2026-11-26).
- **Hangs:** PyTorch's flight recorder dump, written when the NCCL watchdog
  fires (verify the variables on the pinned version), is attached to the
  attempt in the same way.

**Console.** ML → Runs → a run: step time split by phase over time, the rank
heatmap, the straggler and its hardware, MFU, checkpoint cost, and the profiler
captures. A run with no instrumentation shows what drill-downs 1 and 2 provide
(utilisation, attempts, waiting) and the `training-uninstrumented`
recommendation.

**Slice 5,** held until the first design partner, as the owner's build order sets.

### Drill-down 5: runs

**The questions it answers.** How far along is it, and when will it finish? How
many times did it restart, and how much work did that lose? What differs
between this run and the last good one?

- **Progress and ETA:** `ml.train.step` against `ml.train.steps.total`, or the
  step a team logs to MLflow. ETA is the remaining steps times the trailing
  median step time, widened by the run's recent restart rate.
- **Attempts, restarts and preemptions:** from the orchestrator's status
  (JobSet restarts, TrainJob and PyTorchJob conditions), Kueue `Evicted` and
  `Preempted`, pod `DisruptionTarget` reasons (`PreemptionByScheduler`,
  `DeletionByTaintManager`, `TerminationByKubelet`), spot reclaims, and Xid
  events on the run's nodes. Each attempt carries drill-down 2's timeline.
- **Goodput:** productive accelerator-time over allocated. The losses are named:
  queueing, startup, work redone since the last checkpoint after each restart
  (from `ml.checkpoint.step`), and stalls. This is the number that turns
  "restarts happen" into GPU-hours and, with a price, money.
- **Diff:** only the keys that differ. Parameters (template defaults plus launch
  overrides), data (dataset URIs and digests the run records), code (the
  template's release, its commit and image digests from the release ledger),
  environment (GPU product, driver and CUDA from the inventory, node pool), and
  metrics (summaries, plus overlaid step time and loss).
- **Import, don't rebuild** (decision 4 (b)): params, metrics and tags come in
  through the MLflow-compatible endpoint, or by reading the customer's own
  MLflow server; W&B runs are linked by ID.

**Missing data:** `runs-untracked` (below) fires when accelerator training jobs
have neither the instrumentation nor an MLflow or W&B link, and progress and
ETA cannot be shown.

**Slice 5,** with drill-down 4.

### Drill-down 6: evals

**Offline, on model versions.** An eval result is recorded against a model
digest: suite, suite version, dataset digest, metric, value, sample count, and
the run that produced it. It is recorded by
`forge ml eval record --model <name>@<digest> results.json`, by the same call as
an RPC, or through the MLflow endpoint. Importers read lm-evaluation-harness and
MLflow evaluation output (verify their formats); reading another tool's output
file is not doing its job. The result is stored on the model version in the
control plane, and attached to the digest as an attestation once the registry
serves referrers (R4, [decision 14](#11-owner-decisions-decided-2026-11-26)). The Models page
shows each version's evals, the difference from the version now serving, and
regressions.

**Online, through Changes.** `experimentstats` already reads out a binary
outcome per arm (`pkg/experimentstats/doc.go`). That fits the online quality
signals products have: thumbs up, task success, conversion. Continuous metrics
(latency, cost per request, a quality score) need a continuous estimator, which
is [decision 8](#11-owner-decisions-decided-2026-11-26) (b). Each arm's inference SLIs from
drill-down 3 are drawn beside the readout, keyed by the arm's service.

**Gating promotion on evals** is [decision 22](#decisions-17-to-22-decided-2026-11-26).

**Missing data:** `model-promoted-without-eval` (below).

**Offline in slice 4 (Models); online in slice 6.** Both held until a design
partner.

### Where each drill-down lives in the console

All under the one ML item (settled, item 8), with no new page:

| Page            | Gains                                                                                                       |
| --------------- | ----------------------------------------------------------------------------------------------------------- |
| Machines        | The cluster → pool → node → device drill (1); the Queues tab and the org waiting summary (2); idle and cost |
| Recommendations | The new rules below, each linking the panel that shows its evidence                                         |
| Inference       | SLIs per release, time split, cold start per replica, cost per million tokens (1, 2, 3)                     |
| Runs            | Attempts and their timelines, step breakdown, stragglers, MFU, goodput, profiles, diff (1, 2, 4, 5)         |
| Models          | Evals per version and against the serving version (6)                                                       |

Each view links out to the existing Observability views (traces, metrics,
profiles; reliant `frontends/web/src/components/Forge/Console/paths.ts:120`)
for the raw signal, rather than copying them. The agent tools of section 8 read
the same queries, so "why is my run slow?" is answered from the same evidence a
person sees.

### Recommendations this section adds

They join [section 5](#5-recommendations-catalogue)'s catalogue, with the same
rules: pure, stable `id`, adopt-first fix.

| id                            | Finding                                   | Detection rule                                                                         | Fix (adopt first)                                                                         | Slice |
| ----------------------------- | ----------------------------------------- | -------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------- | ----- |
| `accel-pod-attribution-off`   | Utilisation cannot be joined to pods      | Exporter series carry no `pod` / `namespace` labels                                    | Turn on Kubernetes attribution in their exporter (and the pod-resources mount it needs)   | 1     |
| `accel-throttle-reasons-off`  | Throttling is invisible                   | No clock-event or throttle-reason series                                               | Add the field to their exporter's counter file                                            | 1     |
| `accel-interconnect-off`      | NVLink health and bandwidth are invisible | Multi-GPU nodes and no NVLink series                                                   | Add the NVLink fields to the counter file                                                 | 1     |
| `accel-throttled`             | A device runs slow from heat or power     | Thermal or hardware-slowdown reasons active for more than a threshold share of an hour | Name node and device; check cooling, power capping, or reset/RMA                          | 1     |
| `ml-events-not-collected`     | No pod-start or queue timelines           | Accelerator pods in a cluster and no Kubernetes events from it                         | Turn `MLCollector.events` on                                                              | 2     |
| `kueue-metrics-unscraped`     | Queue wait and quota usage unmeasured     | Kueue found and its controller metrics not adopted                                     | `Adopt` Kueue's metrics endpoint                                                          | 2     |
| `kubelet-metrics-unscraped`   | No image-pull or pod-start histograms     | No kubelet pod-start or image-pull series from accelerator nodes                       | Grant `nodes/metrics` through the collector's declared role                               | 2     |
| `scale-up-capacity-errors`    | A pool cannot grow                        | Repeated capacity or quota failures in autoscaler or Karpenter events for one pool     | A reservation, another zone, or the cloud's queued provisioning; the customer's to change | 2     |
| `inference-metrics-unscraped` | Serving SLIs unmeasured                   | A serving pod found (Appendix B) and its metrics endpoint not adopted                  | `Adopt` it                                                                                | 3     |
| `inference-tracing-off`       | No request spans, no join to callers      | vLLM without OTLP tracing                                                              | Set the server's tracing flag; the `ml/instrument` skill does it                          | 3     |
| `model-promoted-without-eval` | A model shipped unevaluated               | A release changed a model digest that has no recorded eval                             | Record one; or declare the gate (decision 22)                                             | 4     |
| `training-uninstrumented`     | No step breakdown, ETA or MFU             | Accelerator training pods with no `ml.train.*` series                                  | The `ml/instrument` skill                                                                 | 5     |
| `runs-untracked`              | Runs have no progress or lineage          | Accelerator training jobs with neither `ml.train.*` series nor an MLflow or W&B link   | The skill, or the MLflow endpoint                                                         | 5     |
| `input-bound-training`        | GPUs wait on data                         | `data_wait` above 20% of step time while SM activity is low                            | More loader workers, pinned memory, `/dev/shm`, local data cache                          | 5     |
| `straggler-rank`              | One rank slows the whole job              | A rank's compute time above k times the median for N steps                             | Name node and device, with drill-down 1's evidence; cordon if it is a bad device          | 5     |

### Cardinality and cost

Everything lands in the tenant's ClickHouse with its existing retention: 30
days for metrics and traces, 24 hours for raw profiles
(control-plane `internal/obstenant/render.go:51-52`). The estimates below are
row counts from stated assumptions. Bytes per row are **unmeasured**; measure
them on the synthetic exporter before slice 1 ships.

| Signal               | Shape                                                                      | Rows per day (estimate)                    | What bounds it                                                                                                                                                    |
| -------------------- | -------------------------------------------------------------------------- | ------------------------------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Hardware             | ~30 fields × 1 series per device (and per MIG instance), scraped every 30s | 86,400 per device: **~86M for 1,000 GPUs** | The adapter's `allow` list; `drop_labels` for what the inventory already holds (driver, model name, hostname); NVLink totals only, never per link; `sample_limit` |
| Inference            | ~15 families per replica, histograms one row per point                     | ~4M for 100 replicas                       | The adapter's `allow` list; `model_name` and release are the only extra labels                                                                                    |
| Training metrics     | 7 phases without rank, plus 2 per-rank series, exported every 60s          | ~3M for a 1,024-rank run                   | Rank only on the two per-rank series; never a point per step                                                                                                      |
| Training spans       | Rank 0 every Nth step, slow steps, declared windows                        | Tens of thousands per run                  | In-process sampling                                                                                                                                               |
| Events and inventory | Kubernetes events; objects every 5 minutes                                 | Small                                      | `MLCollector.pod_scope = "accelerator"`                                                                                                                           |
| GPU profiler traces  | Hundreds of MB per capture                                                 | None in ClickHouse                         | Run artifacts (decision 19)                                                                                                                                       |

**Reads cost more than writes.** A 30-day per-device read over 1,000 GPUs
scans about 2.6 billion rows, and tenant reads are capped per lane by hourly
ClickHouse quotas (control-plane `internal/obstenant/limits.go:24-35`). So:

- per-device views open on 6 hours, fleet views on pool-level aggregates;
- fleet panels are distributions and top-N tables, never one line per device
  (the 50-series cap enforces this anyway);
- a 5-minute per-device rollup is added to the tenant schema only when a
  measured workload hits the interactive quota ([decision 17](#decisions-17-to-22-decided-2026-11-26)).

The metric tables order by service, metric name, hour and attribute hash
(control-plane `internal/obstenant/testdata/010_tenant_create_v33.sql:81`), so
the adopted exporter's scrape keeps one `service.name` per cluster, and a
per-node read stays inside one key prefix. Ingest volume is what decision 11
prices.

### Rollout and testing

| Drill-down | Slice                   | Customer code             | Held for a design partner   | Tested without GPUs by                                                                                                                                |
| ---------- | ----------------------- | ------------------------- | --------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------- |
| Hardware   | 1                       | None                      | No                          | A synthetic exporter serving dcgm-exporter's exact label set, with Xid, ECC and throttle series (fake-gpu-operator emits only utilisation and memory) |
| Scheduling | 2                       | None                      | No                          | Kueue and JobSet on k3d against fake `nvidia.com/gpu`, and KWOK for scale: the events and conditions are real                                         |
| Inference  | 3                       | None (a server flag)      | No; with inference releases | CPU vLLM (opt-125m), which exports the same metric names (verify)                                                                                     |
| Training   | 5                       | The instrumentation       | Yes                         | CPU torch with gloo DDP; the CUDA-event path only on real hardware                                                                                    |
| Runs       | 5                       | Instrumentation or MLflow | Yes                         | The same CPU runs, plus a local MLflow server                                                                                                         |
| Evals      | 4 (offline), 6 (online) | Recording results         | Yes                         | Fixture results files                                                                                                                                 |

So slices 1 and 2 are the next build, and neither asks a customer to change
code. Every view also gets demo fixtures in the same PR, as the gated-access
plan on forge#2859 proposes.

### Decisions 17 to 22 (decided 2026-11-26)

**All settled (owner, 2026-11-26).** Decision 18 was settled first, in
[reliant-for-ml.md](reliant-for-ml.md); the owner then accepted the
recommendation of every other row ("take your recommendations on all"),
recorded in the [Settled (owner, 2026-11-26)](#settled-owner-2026-11-26) list.
They join [section 11](#11-owner-decisions-decided-2026-11-26)'s table. The
table keeps the options that were weighed, as the record of why.

| #   | Decision                               | Options                                                                                                                                                                         | Decided (owner, 2026-11-26)                                                                                                                                 | Blocked                   |
| --- | -------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------- |
| 17  | Hardware metric resolution and history | (a) 30s raw for the tenant's 30 days. (b) 60s raw. (c) (a) plus a 5-minute per-device rollup kept for 13 months, for capacity planning.                                         | (a) now; (c) when a measured workload hits the read quota or a design partner asks for history. Halving resolution hides short throttling episodes.         | Slice 1                   |
| 18  | Training instrumentation vehicle       | (a) The `ml/instrument` skill writes upstream OpenTelemetry Python into the team's loop or callbacks. (b) A published Python package. (c) Only what MLflow autologging records. | **Settled (owner, 2026-11-26): (b)**, a published Python SDK with PyTorch support, taught by the `ml/instrument` skill (reliant-for-ml.md, Settled item 2). | Slice 5                   |
| 19  | Where GPU profiler captures live       | (a) The customer's checkpoint store, linked from the run. (b) Reliant-hosted object storage. (c) Converted into forge's profiles store.                                         | (a). Captures are large and may contain model internals; (c) re-implements Perfetto and Nsight.                                                             | Slice 5                   |
| 20  | How the pod-start timeline is built    | (a) Derived at query time from events and conditions. (b) Materialised as synthetic spans, one trace per pod start.                                                             | (a). No new storage or writer; (b) would let the trace view draw it, but invents spans no system emitted.                                                   | Slice 2                   |
| 21  | Cost attribution                       | (a) Accelerator-hours only. (b) Plus a price per accelerator product, declared by the customer in `MLStack`. (c) Plus adopting OpenCost's prices where it is installed.         | (b) and (c), adopt first. We sell no GPUs, so the price is always theirs; (a) alone cannot say "money".                                                     | Slices 1, 3, 5 cost views |
| 22  | Gate model promotion on evals          | (a) Display only. (b) A `DeliveryPolicy` threshold on a recorded eval metric, refusing promotion below it.                                                                      | (a) first; (b) after a design partner confirms which metrics they trust. A gate on a noisy eval blocks releases for nothing.                                | Slice 4                   |

## 8. UI and agent tools

**One top-level nav item** (settled, item 8; amended on 2026-11-26 to the hybrid
nav, so this item is the ML area and the pages with an app equivalent live in
the existing areas). Its label is
[decision 9](#11-owner-decisions-decided-2026-11-26): "ML" (settled 2026-11-26). It is one `NavArea`,
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

**Gated access in the real console** (settled, item 9):

- The ML item's `available` is true for every signed-in org, so the feature can
  be discovered.
- Its pages read `GetMyOrgEntitlements`. Without `ml`, every ML route renders
  one **"Reach out to Reliant for access"** page with:
  - what ML does, with screenshots from the demo;
  - a "View the demo" link to `demo.reliantlabs.io`;
  - a **Request access** button, which calls `RequestOrgEntitlement` and then
    shows "Requested on <date>; we'll reach out". Its mailto fallback mirrors
    `HOSTING_SUPPORT_MAILTO`.
- Any ML RPC that returns `FEATURE_NOT_ENTITLED` (for example, after a
  revocation) shows the same page, rather than an error toast.

**The demo shell** (`demo.reliantlabs.io`) shows all six pages unblocked. It
needs no `isDemoShell()` branch in the ML pages, because the demo answers every
RPC from its fixture transport:

- `getShell()` returns `demo` for the separate `demo.html` build (reliant
  `frontends/web/src/lib/shell.ts`, `isDemoShell()` at `:183`;
  `vite.config.ts:230,520`).
- `components/Forge/Demo/demoTransport.ts` installs the `fixtures/shop` pack
  for the whole shell, over forge web-runtime's `createFixtureTransport`
  (`fixtures/transport.ts`). The pack refuses writes.

So the demo:

1. answers `GetMyOrgEntitlements` with `ml` enabled;
2. adds `fixtures/shop/handlers/ml.ts`, which answers the `MLService` reads
   from the shop story (`shopStory.ts`'s cast and clock). The story includes
   two clusters, an H100 pool with one Xid-79 node, an L4 inference pool,
   findings, three runs and two model versions;
3. registers it in `fixtures/shop/index.ts:30-54`;
4. marks the ML read RPCs `NO_SIDE_EFFECTS`;
5. adds the ML item to `components/Forge/Demo/demoNav.ts`, out of
   `DEMO_HIDDEN_PAGES` (`components/Forge/Demo/demoPath.ts:37`).

"Propose fix" and "Request access" are refused there, as every demo write is.
The same fixtures drive the console's component tests, so the demo is also the
ML pages' regression baseline.

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
3 of changes.md](changes.md#8-owner-decisions-decided-2026-11-26)). An agent never writes to
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
  on GPU nodes, [decision 10](#11-owner-decisions-decided-2026-11-26) settles that
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

While access is by request (settled, item 9), no price is published and no
public page claims general availability. The landing page and public docs say
"ML: by request".

We sell no GPUs, so nothing here competes with a customer's cloud bill. That
neutrality is the position against W&B (CoreWeave) and Run:ai (NVIDIA). What
could be charged for is [decision 11](#11-owner-decisions-decided-2026-11-26):

- observability ingest, as today;
- a per-monitored-accelerator-hour fee for the ML area;
- a free tier of N accelerators.

The cost of what we add is collector CPU and ingest volume. Scrapes are bounded
by `allow` lists and `sample_limit`. Inventory is pulled every five minutes.
Events are low-volume.

## 11. Owner decisions (decided 2026-11-26)

**All settled (owner, 2026-11-26): each row is decided on its recommended
option** ("take your recommendations on all"), recorded in the
[Settled (owner, 2026-11-26)](#settled-owner-2026-11-26) list. The table keeps
the options that were weighed, as the record of why. Reopening a row is the
owner's call.

| #   | Decision                                                                                                  | Options                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      | Decided (owner, 2026-11-26)                                                                                                                                                                                                                                                             | Blocked                                                               |
| --- | --------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------- |
| 1   | Private GPU clusters (the hub must reach the API server today)                                            | (a) Public endpoint required, restricted to published hub egress addresses (GKE authorized networks, EKS public access CIDRs). (b) **Pull-mode delivery**: Flux in the customer's cluster pulls the env's release bundle (already an OCI artifact) outbound from a registry, and telemetry pushes outbound, so nothing dials in. Needs a pull credential into the cluster and a status channel back (the collector reports Kustomization status as inventory). (c) A reverse-tunnel agent we build. (d) Per-customer peering or PrivateLink. | (a) now. Verify the connected shard's egress is a stable, publishable set. (b) as the durable path: it is Flux's own model, adds no tunnel, and reuses the bundle. (c) builds a privileged component another tool already does. (d) does not scale.                                     | Private-cluster customers; telemetry token renewal for them; slice 7. |
| 2   | Where discovery runs, and its RBAC                                                                        | (a) In-cluster collector with a declared read-only ClusterRole, derived into the connect grant (needs `--apply-scope cluster`). (b) A separate hub-side discovery ServiceAccount added by `connect --discover`. (c) Only the local `forge ml discover`.                                                                                                                                                                                                                                                                                      | (c) for the first look plus (a) for continuous. Both keep the hub's identity a function of the render; (b) adds a standing hub credential that reads the whole cluster.                                                                                                                 | Slice 1.                                                              |
| 3   | Vendor metric adapters against "no presets"                                                               | (a) forge ships accelerator adapters as versioned data (metric family → neutral field, the `allow` list, the version aliases, the bad-device rules), a narrow exception to third-party-telemetry's no-presets rule. (b) Each project or its agent writes the mapping.                                                                                                                                                                                                                                                                        | (a). A neutral Machines view and bad-machine rules cannot exist without the vendor facts, and every customer would rewrite the same table. Keep it data, outside the collector, so a new DCGM version is a table row.                                                                   | The accelerator model; slice 1.                                       |
| 4   | Run tracking: build or integrate                                                                          | (a) Our own tracking SDK and charting UI. (b) Own the run record, accelerator metrics and lineage; accept an MLflow-compatible tracking API; link out to W&B. (c) Link-out only.                                                                                                                                                                                                                                                                                                                                                             | (b). Users keep their logger; we own what nobody joins (run → model → release → live health). Revisit (a) only on demand.                                                                                                                                                               | Slice 5.                                                              |
| 5   | How a run is declared                                                                                     | (a) Every run is a KCL commit. (b) Templates are declared; runs are recorded launches against them; reproducible runs may be pinned in KCL. (c) Observe-only: runs others create are shown, never launched by us.                                                                                                                                                                                                                                                                                                                            | (b), with (c) always on (start anywhere). (a) turns a sweep into fifty deploys.                                                                                                                                                                                                         | `forge.Training`, `LaunchRun`; slice 5.                               |
| 6   | Who pays cross-cloud pull egress from Reliant's registry                                                  | (a) We absorb it within plan limits, metered per org. (b) Pass it through at cost, per GiB, like backend egress (`pricing.yaml:403-408`). (c) A free regional allowance; cross-cloud billed.                                                                                                                                                                                                                                                                                                                                                 | Meter first (R5), then (c): same-cloud pulls are cheap for us and should feel free; a cross-cloud scale-out is the customer's choice, and a pull-through cache or the override is the way out of it.                                                                                    | Billing for models; R5; GA.                                           |
| 7   | Model release cadence                                                                                     | (a) The model digest is part of the app release; a model-only release carries code artifacts forward (`CarriedFrom`). (b) A separate model release ledger. (c) A digest pinned per env in `main.k`, changed by PR.                                                                                                                                                                                                                                                                                                                           | (a), with (c) allowed as an explicit pin. One ledger, one promotion, one health check.                                                                                                                                                                                                  | Slice 4.                                                              |
| 8   | Online comparison of model versions                                                                       | (a) In-app Changes variants only (consistent with Changes Settled 4). (b) Also declare a split or shadow through the router the cluster runs (KServe, Gateway API, Inference Extension), plus continuous metrics in the readout.                                                                                                                                                                                                                                                                                                             | (a) now; (b) as a deliberate ML exception later. forge declares, the router routes, so it does not stand in for a tool.                                                                                                                                                                 | Slice 6.                                                              |
| 9   | Label of the one ML nav item (the item and its six sub-pages are settled)                                 | (a) "ML". (b) "ML Workloads". (c) "Machine Learning". (d) "AI/ML".                                                                                                                                                                                                                                                                                                                                                                                                                                                                           | (a) "ML": it is short and fits the nav's one-word rows (Data, Storage, Auth). "Workloads" collides with forge's `Workload` type, which would read as a filter of it. "AI/ML" promises agent and LLM features that live elsewhere.                                                       | Nav registration; slice 1 UI.                                         |
| 10  | Disconnect and what we installed                                                                          | (a) Today's behaviour: delivered objects are orphaned. (b) Disconnect also removes components labelled `forge-ml` (our installs, the collector); customer workloads stay. (c) Remove everything delivered.                                                                                                                                                                                                                                                                                                                                   | (b). Our DaemonSets on their GPU nodes should not outlive the relationship; their apps should.                                                                                                                                                                                          | Slice 1 install path.                                                 |
| 11  | Pricing                                                                                                   | (a) Observability ingest only. (b) Per monitored accelerator-hour. (c) A free tier of N accelerators.                                                                                                                                                                                                                                                                                                                                                                                                                                        | (a) + (c) through beta; decide (b) with dogfood and early-customer data.                                                                                                                                                                                                                | GA.                                                                   |
| 12  | Spend on real GPUs for what simulation cannot prove ([section 13.2](#132-what-only-real-hardware-proves)) | (a) None: ship on simulation only, and say so in the gated program's terms. (b) A scale-to-zero GKE Spot L4 (or T4) node pool in our own project, used about 4 hours per release (est. $15–40/month, unverified, section 13.3). (c) A design partner's cluster under the gated-access program, with their consent, before each wider grant.                                                                                                                                                                                                  | **No GPU spend** (owner, 2026-11-26 16:42Z, overriding the recommendation of (c) plus (b)): (a)'s fake GPUs, KWOK and CPU workloads for every slice, with (c), the first early-access design partner's cluster, as the real-hardware proof before access widens. No standing Spot pool. | Granting access beyond design partners; the release checklist.        |
| 13  | Size limits and plan for model storage                                                                    | (a) Model bytes count against today's per-plan registry caps (10/25/50 GiB). (b) A separate `models/` allowance per plan, sized for weights (100s of GiB), billed beyond it through the storage pool. (c) Unlimited with storage billed.                                                                                                                                                                                                                                                                                                     | (b). Today's caps would refuse a single 70B model; a separate allowance keeps image quota behaviour unchanged. Refuse `models/` for unclaimed orgs (no content scan past 1 GiB).                                                                                                        | R2, R6; slice 4.                                                      |
| 14  | Referrers API for signatures and SBOMs on models                                                          | (a) Keep distribution and the `sha256-<hex>` tag fallback, with `models/` added to sigguard's protected set. (b) Move to a distribution release that serves referrers. (c) Replace the registry with zot or Harbor.                                                                                                                                                                                                                                                                                                                          | (a) now, so signing is not blocked; (b) when it ships. (c) is a registry migration whose cost is outside ML and should be decided on its own.                                                                                                                                           | R4; model signing.                                                    |
| 15  | Scope of the connected-cluster pull credential                                                            | (a) Reuse registrypull's per-org, whole-subtree token. (b) A new token per (cluster, env) binding, read-only and resource-bound to the project's `models/` (and image) paths.                                                                                                                                                                                                                                                                                                                                                                | (b). A credential sitting in a customer's cluster should read only what that env deploys; (a) would expose every project of the org.                                                                                                                                                    | R3; slice 4.                                                          |
| 16  | Where the ML entitlement is stored                                                                        | (a) A DB table with admin RPCs and an audit trail, like the claimable switches. (b) A declared `features:` list on `grants.yaml` entries, reviewed in git. (c) A plan feature in `pricing.yaml`.                                                                                                                                                                                                                                                                                                                                             | (a). A grant takes effect without a release, the console can read it, and every grant and revoke records who and why. (b) needs a release per grant and the file is plan-shaped; (c) ties ML to a purchase, while the owner said Reliant grants it.                                     | Slice 0.                                                              |

## 12. Rollout plan

Each slice ships alone and is useful without the next. The first ships the most
value fastest: a team connects a cluster and sees its machines and what to fix,
before we model a single run.

0. **Access gate, request page and demo.** This ships first, so every later
   slice lands gated:

   - control-plane: `org_entitlements`, `internal/entitlement`, the admin
     Grant/Revoke/List RPCs, `GetMyOrgEntitlements` and `RequestOrgEntitlement`
     with its Slack notice ([section 4.1](#41-access-by-request));
   - reliant: the ML nav item, the request-access page, and the `ml` fixture
     handler in the demo fixtures;
   - the landing page and docs: "ML: by request".

   Each later slice adds its pages to the demo fixtures in the same PR.

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

- Every slice is proven on CPU-only k3d in e2e, with the vehicles in
  [section 13](#13-testing-without-gpus). That covers:
  - discover → commit `ml.k` → deploy;
  - Machines filled in from fake GPUs;
  - a planted duplicate exporter that must be detected and never installed over;
  - a synthetic Xid that raises `bad-accelerator`.
- Before access widens beyond design partners, a real cluster checks what only
  hardware can prove ([decision 12](#11-owner-decisions-decided-2026-11-26)).
- The first users are design partners under the gated program, each with an
  existing GPU Operator and Kueue. That is the adopt path, which is the common
  case.

**Explicitly out:**

- provisioning or reselling GPUs;
- our own scheduler, serving router, health checker or image streamer;
- typed wrappers of third-party CRDs;
- automatic cordon or drain by default (we recommend it; the customer's NPD,
  NVSentinel or cloud auto-repair acts). **Amended (owner, 2026-11-26):** it
  becomes possible when a team declares it, `Remediation.mode = "act"`, and the
  default when the block is declared is `recommend`
  ([reliant-for-ml.md](reliant-for-ml.md#48-the-declaration), decisions 24 and
  32);
- automatic rollback.

## 13. Testing without GPUs

The owner will not pay for GPUs to test this (settled, item 10; decision 12,
settled 2026-11-26: no GPU spend). Every slice is
proven in k3d on CPU-only machines, in layers. "Verified" marks what the
research read in source; the rest is marked.

| Layer              | Vehicle                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                    | Runs                  |
| ------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------- |
| Unit and contract  | **Synthetic exporter fixtures**: static `/metrics` exposition with dcgm-exporter's exact label set (`gpu`, `UUID`, `Hostname`, `modelName`, `pod`): Xid codes, ECC DBE, row-remap failure, profiling counters present or absent, two exporters reporting the same `UUID`; plus recorded Kubernetes object lists for `pkg/mldiscover` (a GPU Operator install, GKE managed DCGM, a standalone exporter, a stale CRD)                                                                                                                                                                                                                                                                                                        | every PR              |
| k3d e2e            | **run-ai/fake-gpu-operator** (verified: README and code): advertises `nvidia.com/gpu` on CPU nodes, GFD-style labels (`nvidia.com/gpu.product`, `.count`, `.memory`), DRA, MIG for scheduling, a fake `nvidia-smi`, and per-pod utilisation set by the `run.ai/simulated-gpu-utilization` annotation, exported on port 9400 as `DCGM_FI_DEV_GPU_UTIL`, `DCGM_FI_DEV_FB_USED` and `DCGM_FI_DEV_FB_FREE`.                                                                                                                                                                                                                                                                                                                    | merge tier or nightly |
| k3d e2e: workloads | **CPU inference:** `vllm/vllm-openai-cpu:latest-x86_64` (verified in vLLM's docs) serving `facebook/opt-125m`. It exposes the engine's `vllm:*` metrics, so inference SLIs, a release, `forge obs check` and a Changes comparison of two model digests run for real; that the metric names match the GPU build is unverified. **CPU training:** PyTorch DDP with the `gloo` backend under JobSet and Kueue. Pods request fake `nvidia.com/gpu`, so quota, admission, preemption, restarts and the step-reset checkpoint rule behave as on real GPUs while the container trains on CPU. **Models:** a ModelPack push to the k3d registry and an image-volume mount, if the k3s build's containerd supports it (unverified). | nightly               |
| k3d scale          | **KWOK** (verified: README): hundreds to thousands of fake nodes carrying `nvidia.com/gpu` and GFD labels (fake-gpu-operator ships KWOK plugins); `Stage` objects that delay pods through scheduling, pulling and running and emit their events, which feeds the pod-start breakdown and cold-start rules; node conditions for NPD-style findings. KWOK fakes CPU and memory metrics only, not GPU.                                                                                                                                                                                                                                                                                                                        | nightly               |
| Optional spike     | fake-gpu-operator's `mock` backend (NVIDIA's nvml-mock), under which the real gpu-operator device plugin, DRA driver and `nvidia-smi` run. It is privileged, and the operator's status stays NotReady. **Whether the real DCGM exporter runs under it is undocumented**; assume not until a spike shows it. It is worth having for discovery against a real-looking ClusterPolicy install.                                                                                                                                                                                                                                                                                                                                 | once, before slice 1  |

Not usable here: DCGM's field-injection API needs a real DCGM host engine, so it
cannot raise a Xid in k3d. The synthetic fixtures stand in for it.

### 13.1 What this covers

| Feature                                             | Proven by                                                                     |
| --------------------------------------------------- | ----------------------------------------------------------------------------- |
| Machines inventory, labels, product and memory      | fake-gpu-operator; KWOK at scale                                              |
| Allocation (device plugin, DRA, MIG scheduling)     | fake-gpu-operator                                                             |
| Utilisation (GPU_UTIL, frame buffer)                | fake-gpu-operator (three fields, annotation-driven)                           |
| `bad-accelerator`, ECC, row remap                   | synthetic fixtures; `allocatable < capacity` by patching node status          |
| Duplicate-exporter detection                        | fake-gpu-operator's exporter plus a second synthetic one with the same `UUID` |
| Discovery and adopt                                 | recorded object lists; the mock-backend spike for a real ClusterPolicy        |
| Node health findings                                | KWOK Stage-driven or patched node conditions                                  |
| Kueue, JobSet, runs, restarts, checkpoint rule      | CPU training on fake GPUs                                                     |
| Inference SLIs, release, `forge obs check`, Changes | CPU vLLM                                                                      |
| Model push, digest promotion, image volume          | the k3d registry; CPU vLLM loading the mounted model                          |
| Pod-start breakdown, cold-start rules               | KWOK delays and real small pulls (shape and rules, not real timings)          |
| Scale (100s–1000s of GPU nodes)                     | KWOK                                                                          |
| Gated access, request page, demo                    | control-plane and web tests; the demo fixtures                                |

### 13.2 What only real hardware proves

- `DCGM_FI_PROF_*` profiling counters, and real power, temperature and clocks;
  the `dcgm-profiling-off` rule's negative case.
- Real Xid and ECC events and row remapping. Even on hardware they cannot be
  forced, so the rules stay fixture-tested; hardware proves the exporter
  reports them with the labels we expect.
- Driver, toolkit, device plugin and GFD on a real node; real MIG
  reconfiguration and its metrics.
- Duplicate detection against a real GPU Operator exporter beside GKE managed
  DCGM.
- NCCL over NVLink, InfiniBand or GPUDirect; the `nccl-failures` correlation.
- GPU node provisioning, scale-from-zero time, Spot preemption and stockouts.
- Cloud image streaming (GKE Image Streaming, SOCI, ACR Artifact Streaming) and
  real pull times for 10–20 GB images and large weights.
- Model load into GPU memory, CUDA OOM, and real TTFT and throughput.

### 13.3 The cheapest real check

Two options were weighed under
[decision 12](#11-owner-decisions-decided-2026-11-26). The owner chose the
design partner's cluster and **no GPU spend**; the Spot pool is kept below as
the option not chosen.

**Not chosen: a scale-to-zero GKE Spot pool in our own project.** One zonal cluster (its
management fee is covered by GKE's free tier for one cluster per billing
account), and a GPU pool at zero nodes except during a release check. All
prices are **unverified**: Google's pricing pages could not be read in this
pass.

| Shape                          | Spot price (est.) | Use                                                                 | Per month (est.) |
| ------------------------------ | ----------------- | ------------------------------------------------------------------- | ---------------- |
| g2-standard-4, 1 × L4          | ~$0.25–0.30/h     | ~4 h per release × 2 releases a week (~35 h)                        | ~$9–11           |
| n1-standard-4 + 1 × T4         | ~$0.15–0.18/h     | the same                                                            | ~$5–7            |
| CPU system node, disks, egress | —                 | always on (a Standard cluster cannot scale its system pool to zero) | ~$10–30          |

That is **about $15–40 a month in total**. A new project starts with a GPU quota
of zero, so it needs a quota request for both `GPUS_ALL_REGIONS` and the
regional Spot quota. Spot L4 capacity in us-central1 can run out, so the pool
should allow several zones.

**Chosen: a design partner's cluster under the gated program.** No spend, and real
production shapes (H100s, an existing GPU Operator, Kueue). It needs the
partner's consent and an agreed window, and it cannot run per release.

Decided (owner, 2026-11-26 16:42Z, decision 12): no GPU spend. The first
early-access design partner's cluster is the real-hardware proof before access
widens; per-release testing stays on fake GPUs, KWOK and CPU workloads.

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
  need (section 3.7);
- `entitlement-and-demo.md`: per-org gates today, the demo shell's fixture
  transport, and the entitlement design (section 4.1);
- `testing-without-gpus.md`: fake-gpu-operator, KWOK, CPU inference and
  training, and the coverage matrix (section 13).

The code citations above were checked against forge `main` at 175acbe5f,
control-plane, and reliant on 2026-11-25.
