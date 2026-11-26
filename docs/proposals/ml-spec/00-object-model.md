# 00 — Object model

**Status:** product spec draft, 2026-11-26. It feeds the ML redesign after the owner's
review of the demo ("about 10% of the way there"). It refines forge
`docs/proposals/ml-workloads.md` section 2 ("The model") and the observability
addendum (forge#2884). **The run hierarchy is a working hypothesis the owner asked to
confirm** (open question O1 below). Owner input overrides anything here.

Read with: `01-metrics-inventory.md` (what we measure), `02-views.md` (where it shows),
`03-competitive-bar.md`, `04-gap-vs-demo.md`.

---

## 1. The five domains

```
                ┌──────────── FLEET ────────────┐        ┌────────── WORK ───────────────────────────────┐
 Org ─┬─ Cluster ── NodePool ── Node ── Device ─┼─ MIG   │ Project ── Experiment ── Run ── Phase ── Attempt│── Pod/Rank ── Step
      │      └── Queue (ClusterQueue/LocalQueue)│        │                           │       │             │
      │                                         │        │                           │       └── Checkpoint┘
      │                                         │        │                           └── EvalRun ── EvalResult ── EvalSample
      │                                         │        └───────────────────────────────────────────────────┘
      │                                                                      │ produces
      │  ┌──────────── ARTIFACTS ─────────────┐                              ▼
      ├─ Dataset ── DatasetVersion            │       Model ── ModelVersion (OCI digest) ── Alias
      │                                       │                    │
      │  ┌──────────── SERVING ───────────────┤                    │ shipped by
      └─ InferenceService ── Revision(Release)── Replica ── Request (trace)
                    └── Adapter (LoRA)   └── Split (Changes arm / router weight)

 cross-cutting: Finding (recommendation) · HealthEvent · PriceSheet/Cost · Release · Change
```

Every page in `02-views.md` is a view over one of these entities, and every chart is
keyed by an entity id plus time (or step). Nothing is page-shaped in storage.

---

## 2. Work: the run hierarchy (hypothesis — owner to confirm)

### 2.1 Why the demo's flat `Run` fails

The demo models a run as one orchestrator CR with one loss curve
(`MlRun` in control-plane `ml_early_access.proto`). Real LLM work is a **chain** of
jobs: pre-training for weeks, a mid-training/annealing stage, SFT, a preference or RL
stage, distillation or quantization, and evals that run beside all of them. Each stage
is its own Kubernetes job, restarts many times (preemption, Xid, NCCL hang, loss-spike
rewind), and hands a checkpoint to the next. A product that cannot show "this model
came from pre-train attempt 14 → SFT attempt 2 → GRPO attempt 3, and lost 212 GPU-hours
to restarts" is not a training product.

### 2.2 The levels

| Level                            | What it is                                                                                                                                                                                                                                     | Identity                                                                                 | Typical count                   | Created by                                                                                                         |
| -------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------- | ------------------------------- | ------------------------------------------------------------------------------------------------------------------ |
| **Project**                      | A forge project (app) or an imported MLflow/W&B project. Owns templates, models, services                                                                                                                                                      | `project` slug (forge.yaml `name`)                                                       | 1–20 per org                    | forge / import                                                                                                     |
| **Experiment**                   | A named line of work sharing a goal and comparable runs ("support-assistant-8b", "tokenizer-v4 ablations"). The unit of comparison and of sweeps                                                                                               | `exp_<ulid>`, unique `name` per project                                                  | 10s per project                 | first launch with a new name, import, or explicit create                                                           |
| **Run**                          | One logical attempt to produce a model: a DAG of phases with one config lineage. A sweep trial is a run                                                                                                                                        | `run_<ulid>`; human `name` (`llama-8b-support-v3`); optional `sweep_id` + `trial`        | 100s–1000s per experiment       | `forge ml run`, `LaunchRun`, observed job, MLflow/W&B import                                                       |
| **Phase**                        | A stage of the run with its own job, config, metrics and step counter: `pretrain`, `midtrain`, `sft`, `dpo`, `rl`, `distill`, `quantize`, `eval`, `custom:<name>`. A DAG: an `eval` phase may run beside `pretrain`, consuming its checkpoints | `(run_id, phase_key)`; `phase_key` unique in a run (`sft`, `rl`, `eval-periodic`)        | 1–8 per run                     | template (`forge.Training.phases`), or the launch, or an observed job labelled with the run                        |
| **Attempt** (the owner's "wave") | One continuous lifetime of a phase's job: from admission to its end. A restart, resume from checkpoint, preemption/re-admission, elastic resize or a manual rewind starts a new attempt                                                        | `(run_id, phase_key, n)`, `n` = 1,2,3…                                                   | 1–100s per phase on a large run | derived from orchestrator status, Kueue re-admission, pod generations                                              |
| **Pod / Rank**                   | One worker process group: a pod (node, devices) carrying one or more ranks                                                                                                                                                                     | `(attempt, pod_uid)`, `rank` 0..world-1, `local_rank`, `node`, `device_uuid[]`           | world size / GPUs per pod       | Kubernetes; rank↔pod from orchestrator labels and env (`JOB_COMPLETION_INDEX`, JobSet job-index, `PET_NODE_RANK`) |
| **Step**                         | One optimizer step of a phase. Steps are **continuous across attempts** (attempt 3 resumes at step 41,200, not 0)                                                                                                                              | `(run_id, phase_key, step)`; also `tokens_seen` and `samples_seen` as alternative x-axes | 10^3–10^6 per phase             | the training process (instrumentation, MLflow/W&B import)                                                          |

**Why phase sits under run, not run under experiment as separate runs.** W&B and MLflow
model stages as separate runs joined by a `group` / `parentRunId`. That works for
charting but loses: one config lineage, one cost total, one goodput number, and the
checkpoint hand-off as a first-class edge. We keep compatibility by importing an MLflow
parent run with nested child runs as Run → Phases, and a W&B `group` with `job_type` as
Run → Phases (section 6).

**"Wave" ambiguity.** The owner wrote "phases/waves". This spec treats a _wave_ as an
**attempt**. Two other meanings exist in the field and are modelled separately, so
either reading is covered:

- a **data stage** inside one phase (curriculum, data-mixture change, LR annealing
  "cooldown"): modelled as **Segments** — named step ranges on a phase
  (`{name: "anneal", from_step, to_step, mixture, lr_schedule}`), drawn as bands on every
  chart;
- a **sweep wave** (a batch of trials launched together, e.g. successive halving rungs):
  modelled as `sweep_id` + `rung` on runs.

### 2.3 Checkpoints and lineage

| Entity           | What it is                                                                       | Identity                                                                                                               | Links                                                                                                                                                                                                                                                                                                                                                                                              |
| ---------------- | -------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Checkpoint**   | A saved state of a phase at a step: weights, optimizer, RNG, dataloader position | `(run_id, phase_key, step)` + `uri` + content `digest` when known; `kind` ∈ {full, weights-only, sharded-DCP, adapter} | produced by an attempt; consumed by the next attempt (resume), by a downstream phase (`input_checkpoint`), by an EvalRun, or promoted to a ModelVersion                                                                                                                                                                                                                                            |
| **Lineage edge** | A typed, directed edge                                                           | `(from, to, kind)`                                                                                                     | kinds: `resumed_from` (attempt←checkpoint), `initialized_from` (phase←checkpoint or ModelVersion, incl. a base model from Hugging Face), `evaluated` (EvalRun←checkpoint/version), `promoted_to` (checkpoint→ModelVersion), `trained_on` (phase←DatasetVersion), `built_from` (phase←Release/commit/image digest), `serves` (Revision←ModelVersion), `distilled_from` (phase←teacher ModelVersion) |

The lineage graph is the product's spine: every view can walk from a serving request back
to the dataset, commit and attempt that made the weights, and forward from a bad dataset
version to every model and service it touched.

### 2.4 Lifecycle states

**Run state** is derived from its phases; it is never set by hand except `cancel`.

| Run state   | Meaning                                                                                                                           | Derived when                                                 |
| ----------- | --------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------ |
| `queued`    | Nothing running yet; at least one phase waits for admission/capacity                                                              | first runnable phase is `queued`/`admitted`/`starting`       |
| `running`   | At least one phase running                                                                                                        | any phase `running`/`restarting`                             |
| `degraded`  | Running, with an active problem: straggler, stall (no step in k× median), loss spike, NaN, bad device under a rank, restart storm | running + an open `run`-scoped finding of severity ≥ warning |
| `paused`    | Suspended on purpose (Kueue `suspend`, JobSet suspended)                                                                          | all live phases suspended                                    |
| `succeeded` | Every required phase succeeded                                                                                                    | —                                                            |
| `failed`    | A required phase failed terminally                                                                                                | —                                                            |
| `cancelled` | A person or agent cancelled it                                                                                                    | —                                                            |
| `unknown`   | Telemetry and status lost (cluster disconnected, collector down) for > 2 scrape intervals                                         | —                                                            |

**Phase state:** `pending` (waiting on an upstream phase or checkpoint) → `queued`
(Kueue Workload created, not `QuotaReserved`) → `admitted` → `starting` (pods
scheduling/pulling/initialising; the pod-start timeline applies) → `running` →
(`restarting` ↔ `running`)\* → `succeeded` | `failed` | `cancelled` | `skipped`.

**Attempt end reason** (closed set, the most valuable field in the model; drives
goodput and recommendations):

| Reason                   | Source of truth                                                                                                     |
| ------------------------ | ------------------------------------------------------------------------------------------------------------------- |
| `completed`              | orchestrator success condition                                                                                      |
| `preempted_by_queue`     | Kueue `Evicted` reason `Preempted` / `InCohortReclamation` etc.                                                     |
| `preempted_by_scheduler` | pod `DisruptionTarget` reason `PreemptionByScheduler`                                                               |
| `spot_reclaimed`         | node termination notice / Karpenter interruption / node deleted with spot labels                                    |
| `node_failure`           | node `NotReady`, `DeletionByTaintManager`, NPD condition, or Xid/ECC on a device the attempt held                   |
| `gpu_error`              | Xid / DBE / row-remap failure on an attempt device, with the code                                                   |
| `nccl_timeout` / `hang`  | NCCL watchdog / flight-recorder dump / no step progress past the stall threshold                                    |
| `oom_gpu` / `oom_host`   | CUDA OOM in logs / container `OOMKilled` (exit 137)                                                                 |
| `user_code_error`        | non-zero exit not matched above (exit code + last log lines)                                                        |
| `pods_ready_timeout`     | Kueue `PodsReadyTimeout`                                                                                            |
| `rewind`                 | a person/agent restarted from an earlier checkpoint (loss spike) — records `rewound_to_step` and skipped data range |
| `elastic_resize`         | world size changed (torchelastic / Trainer elastic)                                                                 |
| `cancelled`              | user                                                                                                                |
| `unknown`                | none of the above                                                                                                   |

Each attempt also records: `start_step`, `end_step`, `resumed_from_checkpoint`,
`steps_lost` (= end_step − last durable checkpoint step), `world_size`, node set,
device set, image digest, and its pod-start timeline (02-views §Pod start).

### 2.5 Templates, launches, sweeps

| Entity                                  | What                                                                                                                                                                                                                               | Declared or recorded                                                                                                                                                       |
| --------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **TrainingTemplate** (`forge.Training`) | Image/build, accelerators, workers, queue, checkpoint store, default params, and now **`phases: [PhaseTemplate]`** with `depends_on`, `input_checkpoint` policy (`latest` / `best:<metric>` / `step:<n>`), and per-phase overrides | Declared in KCL; versioned by the Release that carries it                                                                                                                  |
| **Launch**                              | Who/what launched a run, with which template release, params, overrides, git commit                                                                                                                                                | Recorded (control plane), like a deploy                                                                                                                                    |
| **Sweep**                               | A search over params launching many runs: method (grid/random/Bayesian/ASHA), objective metric, budget, rungs                                                                                                                      | Recorded; the search logic is the customer's tool (Katib, Optuna, Ray Tune, W&B Sweeps) — we observe and group, we do not run a searcher (never stand in for another tool) |

---

## 3. Evals

| Entity                     | What                                                                                                                                                                                                                             | Identity                                                                       |
| -------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------ |
| **EvalSuite**              | A named, versioned set of tasks with a scoring config ("release-gate-v7": MMLU-Pro 5-shot, GSM8K, IFEval, internal support-tickets-v3, safety-refusals)                                                                          | `(project, name, version)`                                                     |
| **Task / Benchmark**       | One benchmark or internal dataset + prompt template + metric(s) + filter                                                                                                                                                         | `task_key` (`mmlu_pro`, `gsm8k:flexible-extract`); `dataset_digest`            |
| **EvalRun**                | One execution of a suite (or tasks) against one **subject** — a checkpoint, a ModelVersion, or a live InferenceService revision — with a harness (lm-eval-harness, Inspect, lighteval, custom), judge config, seed, sample limit | `eval_<ulid>`; `subject`; may be a Phase of a run (`eval` phase) or standalone |
| **EvalResult**             | One metric value: `(eval_run, task, metric, slice?) → value, stderr/CI, n`                                                                                                                                                       | composite                                                                      |
| **Slice**                  | A subset definition (language, difficulty, customer tier, topic, length bucket)                                                                                                                                                  | `slice_key`                                                                    |
| **EvalSample**             | Per-example record: input, reference, output, score(s), judge rationale, tokens, latency, cost                                                                                                                                   | `(eval_run, task, sample_id)`; stored as an artifact, indexed for drill-down   |
| **Baseline**               | The comparison anchor for regressions: the currently-serving version, a pinned version, or the previous checkpoint                                                                                                               | reference                                                                      |
| **Gate**                   | A declared promotion rule over EvalResults ("no task regresses > 1pt at 95% CI; safety ≥ 0.98")                                                                                                                                  | KCL (`forge.Model.gates`, decision 22)                                         |
| **HumanEval / Annotation** | Rater judgments: rater, rubric, label, agreement                                                                                                                                                                                 | imported (LangSmith/Argilla/Label Studio)                                      |
| **OnlineEval**             | Production-sampled scoring: guardrail hits, LLM-judge on sampled traffic, user feedback, task success; per Revision and per Changes arm                                                                                          | streamed metrics + samples                                                     |

---

## 4. Models and artifacts

| Entity                       | What                                                                                                                                                                                           | Identity                                           | forge object                                                                          |
| ---------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------- | ------------------------------------------------------------------------------------- |
| **Model**                    | A named lineage of versions ("support-assistant")                                                                                                                                              | `(project, name)`; repository                      | `forge.Model` in KCL                                                                  |
| **ModelVersion**             | Immutable weights (+ tokenizer, config, adapters)                                                                                                                                              | **OCI digest** (`sha256:…`), CNCF ModelPack layout | release `oci` Artifact                                                                |
| **Alias**                    | A movable pointer: `candidate`, `staging`, `production`, `@env`                                                                                                                                | `(model, alias)` → digest; moves are audited       | derived from releases per env (the alias _is_ "what env X serves"), plus free aliases |
| **ModelCard**                | Facts: architecture, params (total/active), context length, dtype/quantization, tokenizer, base model, license, adapters, size, shard count, training data summary, intended use, safety evals | on the version                                     | —                                                                                     |
| **Dataset / DatasetVersion** | Training/eval data by URI + digest (or manifest digest), size, token count, mixture weights                                                                                                    | `(project, name, version)`; digest                 | — (recorded; optionally an OCI artifact)                                              |

---

## 5. Serving

| Entity               | What                                                                                                                                                       | Identity                                                                        | k8s / forge                                                                                                                                          |
| -------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------- |
| **InferenceService** | A served model endpoint                                                                                                                                    | `(project, env, name)`                                                          | `forge.Workload` kind `service` with `accelerators` + `model`; or an adopted KServe `InferenceService`, vLLM Deployment, LeaderWorkerSet, RayService |
| **Revision**         | What the service runs at a time: release + model digest + runtime version + serving config (TP size, max batch, quantization, KV cache dtype, spec-decode) | forge **Release** version (+ KServe revision when adopted)                      | Release, `forge obs check` verdict                                                                                                                   |
| **Replica**          | One serving pod (or LWS group for multi-node)                                                                                                              | pod uid                                                                         | Pod; its devices join Fleet                                                                                                                          |
| **Adapter**          | A LoRA served on a base                                                                                                                                    | `(service, adapter_name)`                                                       | vLLM `--lora-modules` / KServe                                                                                                                       |
| **Split**            | Traffic division between revisions/models                                                                                                                  | Changes arm, or router weight (KServe canary, HTTPRoute weights, InferencePool) | **Changes** (decision 8)                                                                                                                             |
| **Request**          | One inference call                                                                                                                                         | trace id; spans per 01-metrics §Traces                                          | OTel trace                                                                                                                                           |

---

## 6. Fleet

| Entity            | What                                                                                    | Identity                                                                                     | k8s object                              |
| ----------------- | --------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------- | --------------------------------------- |
| **Cluster**       | A connected cluster                                                                     | `cluster_id` (control-plane `clusterregistry`)                                               | —                                       |
| **NodePool**      | Nodes sharing product + config                                                          | GKE nodepool / EKS nodegroup / Karpenter NodePool / label set                                | labels                                  |
| **Node**          | A machine                                                                               | node name + `providerID` + boot id (a re-imaged node is a new **incarnation**, history kept) | `Node`                                  |
| **Device**        | One accelerator                                                                         | **GPU UUID** (survives node rename; follows an RMA'd board)                                  | DCGM labels / GFD / DRA `ResourceSlice` |
| **MIG instance**  | A partition                                                                             | `(device_uuid, GPU_I_ID, GPU_I_PROFILE)`                                                     | DCGM labels                             |
| **Link**          | NVLink / NVSwitch port / IB HCA port                                                    | `(node, link_id)` / `(node, hca, port)`                                                      | DCGM / node-exporter infiniband         |
| **HealthEvent**   | Xid, ECC, row-remap, NPD condition/event, throttle episode, link flap, DCGM diag result | `(subject, kind, code, time)`                                                                | events, logs, metrics                   |
| **HealthScore**   | Rolling bad-machine score per device and node with its inputs (01-metrics §Derived)     | `(subject, window)`                                                                          | derived                                 |
| **Queue**         | Kueue ClusterQueue / LocalQueue / Cohort, with quotas per ResourceFlavor                | names                                                                                        | Kueue CRs                               |
| **QueueWorkload** | Kueue `Workload` for one phase attempt (or inference job)                               | Workload name/uid                                                                            | `kueue.x-k8s.io/Workload`               |

---

## 7. Cross-cutting

| Entity         | What                                                                                                                                                                                                                                                                                                                                                               |
| -------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ----------------- |
| **Finding**    | A recommendation instance: rule `id`, subject (cluster/pool/node/device/run/phase/service/model), severity, **impact** (GPU-hours/day, $/day, p95 ms, run-days at risk), evidence (metric refs + events), fix (KCL diff / command / PR), first/last seen, status (`open`, `dismissed` via `forge.RecommendationDecision`, `fixed` — verified by the rule clearing) |
| **PriceSheet** | Customer-declared $/device-hour per product/pool/capacity type (on-demand/spot/reserved) and optional $/GB storage, egress                                                                                                                                                                                                                                         | KCL (decision 21) |
| **CostRecord** | GPU-hours and $ attributed to subject × category (productive, idle-allocated, queued-holding, startup, restart-lost, checkpoint, unallocated)                                                                                                                                                                                                                      |
| **Release**    | forge release ledger entry (build once, promote by digest)                                                                                                                                                                                                                                                                                                         |
| **Change**     | forge Changes flag/experiment                                                                                                                                                                                                                                                                                                                                      |

---

## 8. Mapping to Kubernetes objects

| Our entity | Batch `Job` (Indexed)                                                                      | JobSet                                                                   | Kubeflow Trainer v2 `TrainJob`                 | Training Operator `PyTorchJob`/`MPIJob`                | KubeRay `RayJob`                              | Kueue                                                                                                                                         |
| ---------- | ------------------------------------------------------------------------------------------ | ------------------------------------------------------------------------ | ---------------------------------------------- | ------------------------------------------------------ | --------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------- |
| Phase      | the Job (or a series of Jobs with label `ml.reliant.dev/phase`)                            | the JobSet                                                               | the TrainJob (it creates a JobSet)             | the PyTorchJob                                         | the RayJob                                    | —                                                                                                                                             |
| Attempt    | a new Job object for the same phase, or the same Job after `suspend` toggle / re-admission | `status.restarts` increment (every child Job recreated)                  | underlying JobSet restart; TrainJob conditions | `Restarting` condition / replica pod recreation        | new RayCluster / `jobDeploymentStatus` retry  | a new `QuotaReserved`→`Admitted` after `Evicted`/`Requeued`                                                                                   |
| Pod/rank   | pod with `batch.kubernetes.io/job-completion-index`                                        | `jobset.sigs.k8s.io/replicatedjob-name` + `job-index` + completion index | via JobSet labels + `trainer.kubeflow.org/*`   | `training.kubeflow.org/replica-type` + `replica-index` | Ray worker pods (rank from Ray Train context) | `kueue.x-k8s.io/podset`                                                                                                                       |
| Queueing   | —                                                                                          | —                                                                        | —                                              | —                                                      | —                                             | `Workload` (per Job/JobSet/TrainJob/RayJob), `LocalQueue`, `ClusterQueue`, `Cohort`, `ResourceFlavor`, `AdmissionCheck`/`ProvisioningRequest` |

**Joining keys we inject** (when we launch) or **infer** (when we observe), in priority
order: labels `ml.reliant.dev/run`, `ml.reliant.dev/phase`, `ml.reliant.dev/attempt`
(stamped by `forge ml run` on the CR and propagated to pods) → env/resource attributes
in OTLP (`ml.run.id`, `ml.phase.key`, `ml.phase.kind`, `ml.attempt`, `ml.rank`,
`ml.world_size`; the step breakdown uses `ml.step.phase`, see 01 rule 9) →
MLflow `MLFLOW_RUN_ID` / W&B `WANDB_RUN_ID` + `WANDB_RUN_GROUP` env on the pod spec →
heuristics (CR name prefix, owner references, Kueue workload name). An observed job with
no key becomes a single-phase run named after its CR, marked `observed`, and the
`runs-untracked` finding offers the labels.

Inference: `InferenceService` ↔ Deployment / KServe `InferenceService` /
`LeaderWorkerSet` / `RayService`; Revision ↔ ReplicaSet hash / KServe revision + forge
`service.version`; Replica ↔ pod; Split ↔ KServe `canaryTrafficPercent` / `HTTPRoute`
backendRefs weights / `InferencePool` + `InferenceObjective`.

Fleet: Node ↔ `Node` (+ `nodeInfo`, labels, conditions, `status.images`); Device ↔
extended resource + GFD labels + DCGM series + DRA `ResourceSlice` device; HealthEvent ↔
`Event`s (NPD, kubelet), NPD node conditions, DCGM fields, kernel log lines.

## 9. Mapping to forge objects

| Our entity                     | forge object                                                                                | Note                                                                  |
| ------------------------------ | ------------------------------------------------------------------------------------------- | --------------------------------------------------------------------- |
| TrainingTemplate (+ phases)    | `forge.Training` (KCL, ml-workloads §3.1), extended with `phases`                           | template changes ship in a Release; a run records the release version |
| Launch                         | recorded event, like a deploy; `forge ml run`, `MLService.LaunchRun`                        | production launches use the Changes approval pattern                  |
| Run pinned for reproducibility | `Training.runs: [TrainingRun]` in KCL                                                       | ml-workloads decision 5 (b)                                           |
| ModelVersion                   | release `Artifact{Kind: oci}` (ModelPack media types)                                       | `CarriedFrom` for model-only releases (decision 7)                    |
| Alias per env                  | the release deployed to that env                                                            | no second ledger                                                      |
| InferenceService               | `forge.Workload` kind `service` + `accelerators` + `model`, or `forge.Manifests` for KServe | adopt-only for existing KServe/vLLM                                   |
| Revision verdict               | `forge obs check` + metric findings in `pkg/releasehealth`                                  | TTFT p95, ITL p95, queue p95, error share, tokens/s/accelerator       |
| Split / online comparison      | **Changes** + `experimentstats` (binary today; continuous = decision 8 b)                   | —                                                                     |
| Finding                        | `pkg/mlrecs` rule; dismissal = `forge.RecommendationDecision` (KCL)                         | fix = KCL diff / PR                                                   |
| ML stack component             | `forge.MLStack` `Adopt` / `Install`                                                         | discover → adopt → install-only-missing                               |
| Telemetry sources              | `forge.Telemetry` `MetricsScrape` (allow-listed), node agent, cluster collector             | vendor adapters as versioned data (decision 3)                        |
| Code joins                     | runtime-context (`observe.ComponentChain`), release `Commits`                               | "step time +18% since commit abc"                                     |

## 10. Mapping to MLflow and W&B (import, never rebuild)

| Our entity           | MLflow (REST 2.0 / MLflow 3)                                                                | W&B                                                                                                                           |
| -------------------- | ------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------- |
| Project              | Experiment (or tag `project`)                                                               | entity/project                                                                                                                |
| Experiment           | Experiment, or tag `reliant.experiment`                                                     | project, or `group` prefix / tag                                                                                              |
| Run                  | parent Run (`RunInfo.run_id`)                                                               | `group` (runs sharing a group) — or a single run when ungrouped                                                               |
| Phase                | nested child Run (`tags["mlflow.parentRunId"]`), phase from tag `reliant.phase` or run name | run with `job_type` = phase                                                                                                   |
| Attempt              | a resumed run's restart boundaries (gaps in `system/` metrics, `start_time` of resumed run) | `resume="allow"` boundaries; **fork / rewind** (`fork_from`, `resume_from` `run?_step=n`) → `rewind` attempts                 |
| Step metrics         | `RunData.metrics` history (`key, value, timestamp, step`)                                   | `history` rows (`_step`, `_timestamp`)                                                                                        |
| Params               | `RunData.params`                                                                            | `config`                                                                                                                      |
| Tags                 | `RunData.tags`                                                                              | `tags`, `notes`                                                                                                               |
| Dataset              | `RunInputs.dataset_inputs` (`Dataset{name, digest, source_type, source, schema, profile}`)  | artifact of type `dataset`                                                                                                    |
| Checkpoint           | `LoggedModel` (MLflow 3; `model_id`, `step`, `source_run_id`, metrics) or artifacts         | artifact of type `model`/`checkpoint` with aliases                                                                            |
| ModelVersion / Alias | `RegisteredModel` / `ModelVersion` (`version`, `source`, `run_id`), `aliases`               | Registry collection + version + aliases                                                                                       |
| EvalRun / Result     | `mlflow.evaluate` / `mlflow.genai.evaluate` results, assessment tables                      | Weave Evaluation, scorers                                                                                                     |
| System metrics       | `system/gpu_0_utilization_percentage`, `system/gpu_0_memory_usage_megabytes`, …             | `system/gpu.0.gpu`, `system/gpu.0.memory`, `system/gpu.0.powerWatts`, … (shown, but the cluster's DCGM wins where both exist) |
| Traces               | MLflow Tracing (OTel-compatible spans)                                                      | Weave traces (link out)                                                                                                       |

Import modes: (a) an MLflow-compatible tracking endpoint we host (clients set
`MLFLOW_TRACKING_URI`), (b) read the customer's MLflow server, (c) W&B link by run id
with optional API read of summary + history. Decision 4 (b) in ml-workloads.

---

## 11. Storage shape (where each entity lives)

| Data                                                                                           | Store                                                                                             | Why                                                        |
| ---------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------- | ---------------------------------------------------------- |
| Entities and edges (run, phase, attempt, checkpoint, model version, eval run, finding, launch) | control-plane Postgres, per org                                                                   | relational, small, audited                                 |
| Time series (DCGM, NPD, kube, Kueue, serving SLIs, `ml.train.*`)                               | tenant ClickHouse `t_<org>` (existing metrics tables)                                             | volume; existing obsquery                                  |
| Step-indexed training scalars (loss, LR, grad norm, evals by step)                             | ClickHouse, a step-keyed metric table **(new)** — `(run, phase, attempt, key, step, time, value)` | charts by step, not time; resumed attempts overlay on step |
| Events, conditions, inventory snapshots                                                        | ClickHouse logs (`k8s_events`, `k8sobjects`)                                                      | existing collector path                                    |
| Traces (requests, sampled train steps, checkpoints)                                            | ClickHouse traces                                                                                 | existing                                                   |
| CPU profiles                                                                                   | ClickHouse profiles (24h raw)                                                                     | existing                                                   |
| GPU profiler captures (Kineto/Nsight), flight-recorder dumps, memory snapshots, eval samples   | object storage as run artifacts (decision 19)                                                     | 100 MB–GBs; opened in Perfetto / Nsight / our viewer       |

---

## 12. Open questions for the owner

| #   | Question                                                                                          | Default in this spec                                                                                                     |
| --- | ------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------ |
| O1  | Is the hierarchy project → experiment → run → phase → attempt → pod/rank → step right?            | Yes, with Segments for data stages and `sweep_id`/`rung` for sweep waves                                                 |
| O2  | Does "wave" mean attempt (restart/resume/preemption), a data/curriculum stage, or something else? | Attempt; Segments cover data stages                                                                                      |
| O3  | Are phases of one run allowed to span clusters (pre-train on cluster A, SFT on B)?                | Yes; a phase has its own cluster                                                                                         |
| O4  | Is an eval a phase of a run, a standalone EvalRun, or both?                                       | Both: periodic/in-training evals are a phase; release evals are standalone EvalRuns against a ModelVersion               |
| O5  | Steps continuous across attempts, or per attempt?                                                 | Continuous (the step the trainer reports); attempts overlay on the same axis                                             |
| O6  | Which identifiers must be human-typed (CLI, URLs)?                                                | run `name` and `phase_key` in URLs: `/ml/runs/<run-name>/phases/<phase>/attempts/<n>` with `run_<ulid>` as the stable id |
