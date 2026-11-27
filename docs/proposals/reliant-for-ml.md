# Reliant for ML: the practices Reliant gives apps, for training and inference

**Status:** proposal, the north star for ML. Nothing here is built. It records
the owner's direction of 2026-11-26 (the [Settled](#3-settled-owner-2026-11-25-and-2026-11-26)
list) and, the same day, the owner's acceptance of every design decision this
proposal raised ("agreed with your recs":
[section 3.1](#31-settled-owner-2026-11-26-decisions-23-to-32); the record of
options is [section 10](#10-owner-decisions-decided-2026-11-26)). Phases land
as their own pull requests ([section 11](#11-build-order)).
**Date:** 2026-11-26
**Feeds from:** [ml-workloads.md](ml-workloads.md) (merged: the cluster, stack,
recommendations, models, training templates, gated access, testing without GPUs,
and its "ML observability" section from forge#2884). This document does not
repeat them; it says what they add up to, and adds what they lack: resilience
at scale, the feedback-loop hooks, testing and previews for ML, and the
security of model artifacts.
**The spec:** the ML product spec of 2026-11-26 (object model, metrics
inventory, views, competitive bar, gap against the demo and phases A–G) is the
working document behind the console redesign, in [ml-spec/](ml-spec/README.md)
(forge#2921). Where this proposal says "spec 00" or "spec 04", it means that
spec's [object model](ml-spec/00-object-model.md) and its
[gap-and-phases file](ml-spec/04-gap-vs-demo.md). What this proposal relies on from it is
summarised where it is used: the run hierarchy (§2), attempt end reasons (§4.4)
and the phases (§11).
**Related:** [changes.md](changes.md), [tests-coverage-flakes.md](tests-coverage-flakes.md),
[sbom-vulnerabilities.md](sbom-vulnerabilities.md),
[signed-image-admission.md](signed-image-admission.md),
[start-anywhere.md](start-anywhere.md), [runtime-context.md](runtime-context.md),
[product-events.md](product-events.md).

## Summary

Reliant gives an app a delivery system it did not have to build: every change
tested, every release built once and promoted by digest, every deploy judged by
`forge obs check`, telemetry with no instrumentation code, alerts that land in
an Inbox, previews per pull request, secrets and signed artifacts, and agents
that can act on all of it. The team writes business logic, and the system around
it gives them fast feedback and keeps it reliable.

This proposal does the same for ML, on the customer's own clusters:

- a **model version is a release artifact**, promoted like an image and judged
  like a deploy;
- a **training run is a recorded launch** with phases and attempts, observed
  down to the rank and the GPU, and joined to the hardware, the scheduler and
  the code that made it;
- **resilience is the headline feature for large runs**: failures detected in
  seconds, checkpoints that cost seconds, failed nodes replaced from a spare, and
  every minute of lost time named and priced (goodput);
- **eval suites are the tests of a model**, and a small smoke training run is
  the test of a training change;
- every state change is a typed event with a query API, a CLI verb and a
  workflow trigger, so feedback loops (an agent proposing the next experiment, an
  RL environment feeding rewards back) can be built on top.

Almost all of it reuses what forge and the control plane already have: the OCI
registry, Flux delivery, the release ledger, release health, Changes, the OTel and
ClickHouse pipeline, alerting, the Inbox, previews and Reliant workflows. What is
genuinely new is small: a **training-run ledger**, a **Python instrumentation
SDK**, and the resilience policy that forge renders onto upstream orchestrators
(Kueue, JobSet, Kubeflow Trainer) that we adopt rather than build.

Everything is opt-in, as everything in Reliant is. A team can take the hardware
dashboards and nothing else, or model digests in a release and nothing else.

## 1. The thesis

| What Reliant gives an app                                                  | The same practice for ML                                                                                                                                                                                       |
| -------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Tests on every change, results and flakes on the Tests page                | A smoke training run per pull request (starts, loss falls, no NaN, resumes from a checkpoint, throughput within tolerance); eval suites as the tests of a model release ([§6](#6-testing-and-previews-for-ml)) |
| Build once, promote by digest, one release ledger                          | Weights, checkpoints, datasets and eval sets as OCI digests in the same ledger; a model-only release carries code forward (ml-workloads §7)                                                                    |
| `forge obs check` judges each deploy; a regressed release halts promotion  | The same check judges an inference release on TTFT, inter-token latency, queue wait and errors; an eval gate judges a model before it ships                                                                    |
| Telemetry with no code: traces, RED per endpoint, profiles, dashboards     | GPU, node, scheduler and serving telemetry with no code (adopted exporters); training telemetry through one SDK call                                                                                           |
| Alerts with defaults, routed to the Inbox and Slack                        | Default ML alerts (bad GPU, stalled run, goodput drop, eval regression) in the same Inbox                                                                                                                      |
| Previews per pull request                                                  | A preview env that runs the smoke training and serves the candidate model                                                                                                                                      |
| Secrets, SBOMs, signed artifacts, scoped tokens                            | Signed model artifacts with SBOMs and provenance, unsafe-pickle scanning, scopes for data and weights ([§8](#8-security))                                                                                      |
| Agents act through the same APIs a person uses, under review and approvals | The feedback-loop hooks: events, queries, triggers, and launch or fork of a run from a template ([§5](#5-feedback-loop-hooks))                                                                                 |
| Recovery rolls forward; nothing rolls back on its own                      | Still true for releases. Training adds a different, safer case: resuming the **same** declared run from its last good checkpoint, opt-in and declared ([§4](#4-resilience-at-scale))                           |

Three rules carry over unchanged and shape every section:

1. **Opt-in, piece by piece.** Each capability works alone
   ([start-anywhere.md](start-anywhere.md)). A team that only wants goodput for
   jobs it already runs with its own tooling gets it from observed runs, with no
   template and no SDK.
2. **Declared, not clicked.** Policy (stack, templates, checkpoints, spares,
   remediation, gates, budgets) lives in `deploy/kcl/<env>/`. A console or agent
   action writes or proposes a declaration, or records a launch against one.
3. **Never stand in for another tool.** We adopt Kueue, JobSet, Kubeflow
   Trainer, TorchElastic, DCGM, node-problem-detector, vLLM and the rest, render
   their configuration, read their signals, and run none of their logic.

## 2. New mechanisms, or reuse OCI, Flux and the rest?

The owner asked: "will these be new flavors or mechanisms under the hood, or
reuse existing primitives like OCI, Flux?" The answer is **reuse**, need by need:

| ML need                                                        | Reused primitive                                                                | Where it is today                                                                                                                                                                                 | What ML adds                                                                                                                                 |
| -------------------------------------------------------------- | ------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------- |
| Weights, checkpoints, datasets, eval sets addressed immutably  | OCI artifacts and digests; Reliant's registry or the customer's                 | forge `pkg/release/release.go` (`KindOCI`, `CarriedFrom`); control-plane `internal/ociregistry` (per-org layout; `referrers.go` reads the `sha256-<hex>` referrer tags)                           | ModelPack media types, a `models/` path kind and blob redirect for multi-GB layers (ml-workloads §3.5, §3.7, R1–R7)                          |
| Getting declarations and workloads into the customer's cluster | Flux delivery from the hub, through the connected shard                         | forge `kcl/lib/flux.k` (`kustomize-controller-connected`); control-plane `internal/fluxhub`                                                                                                       | Nothing new. Training templates, the ML stack and resilience policy render into the same bundle                                              |
| Shipping a model, promoting it, judging it                     | The release ledger, promote by digest, `forge obs check`, `DeliveryPolicy`      | forge `pkg/release`, `pkg/releasehealth`, `internal/cli/env_releases*.go`; `DeliveryPolicy` with `health_gate` and `required_gates` (`kcl/schema.k:6969`)                                         | Inference SLIs as release-health findings; an eval suite as a named required gate                                                            |
| Comparing two model versions on live traffic                   | Changes and the experiment readout                                              | forge `pkg/changes`, `pkg/experimentstats`; [changes.md](changes.md)                                                                                                                              | Continuous metrics and router splits (ml-workloads decision 8)                                                                               |
| Hardware, scheduler, serving and training telemetry            | The OTel pipeline into the tenant's ClickHouse; `forge.Telemetry` scrapes       | forge `pkg/observe`, `pkg/obsquery`, the node agent in `pkg/deploy/observability.go`, `kcl/telemetry.k`; control-plane `internal/obstenant`                                                       | Vendor adapters as versioned data (decision 3), a cluster collector, the `ml.*` conventions, a step-keyed scalar table                       |
| Dashboards                                                     | Dashboards as code                                                              | forge `pkg/dashspec` (incl. `heatmap`), `internal/obsdash/telemetry.go`                                                                                                                           | Generated GPU, queue, inference and run dashboards                                                                                           |
| Profiles                                                       | Continuous profiling and the profiles store                                     | forge `pkg/observe/profiler.go`, `pkg/obsquery/profiles.go`; `PprofScrape` in `kcl/telemetry.k`                                                                                                   | The OTel eBPF profiler for Python as an adopt-or-install slot; GPU captures (Kineto, Nsight) and NCCL flight-recorder dumps as run artifacts |
| Alerts and triage                                              | SQL alert rules, Alertmanager routing, Slack receivers, the Inbox               | forge `pkg/alerting` (rule evaluator), `forge.AlertRule` (`kcl/schema.k:7606`); control-plane `internal/alerting`, `internal/handlers/alerting` (Slack receiver); reliant Inbox                   | Default ML rules; findings with impact; remediation outcomes                                                                                 |
| Who did what, and a stream to react to                         | The typed activity stream and product events                                    | forge `pkg/activity` (`Event`, `Record` for non-RPC actors); control-plane `internal/activitystream`; forge `pkg/observe/track.go`                                                                | ML event kinds ([§5.1](#51-a-typed-event-stream))                                                                                            |
| Per-PR environments                                            | Previews                                                                        | forge `internal/cli/env_preview.go`, `pkg/release/preview.go`                                                                                                                                     | A smoke-training job and a candidate model served in the preview                                                                             |
| Test results, flakes                                           | The Tests pipeline                                                              | [tests-coverage-flakes.md](tests-coverage-flakes.md); control-plane `internal/testingest`; reliant Tests page                                                                                     | Smoke-training and eval results reported as test results                                                                                     |
| Automation and agents                                          | Reliant workflows with triggers                                                 | reliant `internal/triggers/triggerspec` (kinds: integration, webhook, schedule, workflow_event, manual; `shape.go:137`); integration manifests in `internal/integrations/catalog/*/manifest.yaml` | ML trigger types ([§5.3](#53-triggers-for-reliant-workflows))                                                                                |
| Gated access                                                   | Per-org entitlements                                                            | Proposed in ml-workloads §4.1; implementation open as control-plane#3747                                                                                                                          | Nothing beyond it                                                                                                                            |
| Signing, SBOMs, vulnerability findings                         | The release's SBOM per artifact, the delivery signer, admission                 | [sbom-vulnerabilities.md](sbom-vulnerabilities.md); control-plane `internal/deliverysign`, `internal/vulnscan`; [signed-image-admission.md](signed-image-admission.md)                            | Model SBOMs and provenance; a pickle scan ([§8](#8-security))                                                                                |
| Queueing, quota, gang scheduling, multi-node jobs              | **Upstream, adopted:** Kueue, JobSet, Kubeflow Trainer v2, KubeRay (adopt-only) | Not ours, and never will be. ml-workloads §3.1 (`forge.Training` lowers to them)                                                                                                                  | Rendering their config from `forge.Training`; reading their status                                                                           |

**What is genuinely new.** Three things, and they are kept small:

1. **A training-run ledger.** Run, phase, attempt, checkpoint and lineage
   records in the control plane, the way releases and deploys are recorded
   there. The model is the spec's object model (project → experiment → run →
   phase → attempt → pod/rank → step; confirmed by the owner on 2026-11-26),
   with attempt end reasons as a closed set. Step-indexed scalars go in a new
   step-keyed ClickHouse table beside the existing metric tables. This is the
   one new store.
2. **The Python instrumentation SDK** ([§3](#3-settled-owner-2026-11-25-and-2026-11-26),
   item 2). A thin layer over upstream OpenTelemetry Python: conventions,
   framework callbacks, CUDA-event timers. Not a tracing library of our own.
3. **Resilience policy and its renderer** ([§4](#4-resilience-at-scale)): the
   KCL that declares checkpoints, spares and remediation, and what forge renders
   from it onto JobSet, Kubernetes Job failure policies, TorchElastic and Kueue.
   Whatever acts across nodes in seconds (cordon, swap in a spare, rewind) is
   upstream first, plus a small forge-rendered executor in the customer's
   cluster for what ties a node action to a run (decision 24).

Kueue, JobSet and Kubeflow Trainer are adopted upstream, not built. So is Ray,
which stays adopt-only.

### 2.1 What this means for navigation

Because ML reuses the app primitives, most ML surfaces are already pages in the
console: a model release is a release, a smoke run is a preview, a GPU trace is
a trace, an alert is an Inbox item. What follows is a **hybrid** nav (decision
23, settled):

- **An ML area** for what has no app equivalent: **Training** (experiments,
  runs, phases, attempts, goodput), **Evals**, **Models**, **Fleet** (clusters,
  pools, nodes, GPUs, queues), and the ML stack setup.
- **Existing areas gain ML content:**
  - **Deployments:** model and inference releases, their verdicts, promotion.
  - **Previews:** smoke training runs and the candidate model per pull request.
  - **Observability:** GPU dashboards, traces into training steps and inference
    requests, profiles.
  - **Inbox:** ML alerts, recommendations and remediation outcomes.
  - **Tests:** eval gates and smoke-training results.
- **"Training", not "Runs".** The nav already has **Runs** under Workflows,
  meaning workflow runs (reliant `navConfig/items.tsx:478`). A second "Runs" for
  training would collide in search, in agent tool names and in speech.

This amends ml-workloads Settled item 8 (one nav item with six sub-pages); the
owner accepted it on 2026-11-26, and ml-workloads.md records the amendment.

## 3. Settled (owner, 2026-11-25 and 2026-11-26)

ml-workloads' Settled list stands (no GPU provisioning, build on
`forge cluster connect`, NVIDIA first with a neutral schema, declarative in
KCL, discover → adopt → install-only-missing, weights in Reliant's registry by
default with a customer override, gated access, built and tested without GPUs).
The owner added on 2026-11-26:

1. **Mirror Reliant, for ML.** Testing, releasing, security, observability and
   feedback loops, so teams focus on the model and the data. Everything is
   opt-in.
2. **A Python instrumentation SDK first, with PyTorch support.** This settles
   ml-workloads decision 18 in favour of a published package, with the
   `ml/instrument` skill teaching it. `forge scaffold training` comes later. The
   SDK covers:
   - **callbacks** for Hugging Face Trainer, Lightning, torchtitan, Megatron-LM
     and DeepSpeed, plus a plain-loop API for everything else;
   - **tags** on every signal: run, phase, attempt, rank, local rank, world
     size, and the mesh (`dp`, `tp`, `pp`, `ep`, `cp` sizes and this rank's
     coordinates);
   - **step, phase and token stats**: step time split by `ml.step.phase`
     (data wait, forward, backward, optimizer, exposed communication,
     checkpoint, eval, other), tokens and samples per second, padding
     efficiency;
   - **training scalars**: loss and `lm_loss`, grad norm, learning rate,
     loss scale, MFU and achieved TFLOP/s, named timers;
   - **sampling and queries**: generation and rollout timing for RL phases, and
     sampled eval queries;
   - **memory and compute**: GPU memory allocated against reserved and peak, CPU
     and host memory, GPU allocation against utilisation (joined from DCGM, not
     re-measured);
   - **goodput**: the SDK reports progress and checkpoint durability; goodput
     itself is derived server-side ([§4.3](#43-goodput-accounting));
   - **model vitals and insights**: per-layer activation and gradient norms,
     update-to-weight ratios, attention entropy and MoE expert load, sampled at a
     declared cadence;
   - **traces deep in steps and requests**: a sampled `train.step` span with a
     child per phase and per collective, checkpoint and data-loader spans, joined
     by trace and resource attributes to DCGM device series, kubelet stats, pod
     scheduling and Kueue admission spans.
3. **Opt-in auto-remediation, declared in KCL.** It amends ml-workloads'
   "Explicitly out: automatic cordon or drain": that stays out by default and
   becomes possible when a team declares it ([§4.8](#48-the-declaration)).
4. **Feedback-loop hooks first.** In the owner's words: "build the system that
   has the feedback loops first; users plug in the forge CLI how they see fit;
   Reliant workflows can be that harness; it's more about the hooks." So we
   build events, query APIs, CLI verbs, triggers and launch/fork APIs, and no
   autoresearch or RL product on top yet ([§5](#5-feedback-loop-hooks)).
5. **Access requests notify through an alert-rule Slack notification**, the
   existing alerting sender, not a bespoke Slack integration.
6. **NVIDIA first, no GPU spend, gated access**, unchanged.
7. **The run hierarchy is confirmed:** project → experiment → run → phase →
   attempt ("wave") → pod/rank → step, with checkpoints linking phases and
   producing model versions.

### 3.1 Settled (owner, 2026-11-26): decisions 23 to 32

The owner accepted every recommendation of
[section 10](#10-owner-decisions-decided-2026-11-26) ("agreed with your recs",
2026-11-26 06:01Z). Item N is decision N, and the sections that raised each
one describe the chosen option.

23. **Hybrid navigation, with "Training", not "Runs".** An ML area holds
    Training, Evals, Models, Fleet and the stack setup; model releases live in
    Deployments, smoke runs in Previews, GPU telemetry in Observability, alerts
    in Inbox and eval gates in Tests ([§2.1](#21-what-this-means-for-navigation)).
    It amends ml-workloads Settled item 8.
24. **Cross-node remediation is upstream first.** JobSet, TorchElastic,
    NVSentinel and cloud auto-repair do what they can (in-job restarts, failure
    policies, node repair), and forge recommends. A small forge-rendered
    executor in the customer's cluster, configured from the declaration and
    holding declared Kubernetes RBAC, does only what ties a node action to a run:
    cordon, swap in a spare, resume and record. The control plane does not
    execute remediation.
25. **The training-run ledger lives in control-plane Postgres, per org**, with
    step-indexed scalars in a new step-keyed ClickHouse table. Not inside the
    release ledger, and not ClickHouse only.
26. **The Python SDK is `sdk/python/` in the forge repository**, Apache-2.0 and
    published to PyPI, at 0.x until a design partner confirms the API, then 1.0
    under semver. The PyPI name still needs checking.
27. **ML triggers reach Reliant workflows through a typed `forge` integration
    manifest**, with trigger types and payload schemas
    ([§5.3](#53-triggers-for-reliant-workflows)), not a generic event trigger
    with a CEL filter.
28. **Launches by agents and workflows are recorded and bounded**: within the
    template's declared parameter bounds and budgets, audited in the activity
    stream, and launches into production queues need an approval, as production
    Changes do ([§5.4](#54-launch-and-fork)).
29. **Hot spares are low-priority Kueue placeholder workloads** the job
    preempts, where Kueue is adopted, and tainted standby nodes without it
    ([§4.6](#46-replace-without-a-full-restart)). Cloud reservations alone are
    not a spare.
30. **Smoke training runs on CPU by default** (gloo, fake GPUs) in the preview
    cluster; a cheap GPU pool is the team's declared choice
    ([§6.1](#61-smoke-training-per-pull-request)).
31. **Pickle-format weights warn first**, then gate through a lint rule on the
    lint rollout table, plus an opt-in `DeliveryPolicy` control that refuses
    promotion ([§8](#8-security)). They are never refused outright on the day a
    team pins the release.
32. **The default remediation mode is `recommend`** when the block is declared;
    `act` is stated in words (`mode = "act"`) ([§4.8](#48-the-declaration)).

## 4. Resilience at scale

The owner's emphasis: "in a large network of GPUs all working collaboratively,
quickly checkpoint, replace, resume. Companies spend millions on GPUs, and
downtime is hugely detrimental." This is the section a large training team
will judge the product by.

### 4.1 Why it matters, in numbers

A synchronous training job is only as available as its least available rank.
With thousands of GPUs, failures are routine, not exceptional:

- Meta reported 419 unexpected interruptions in 54 days of Llama 3 405B
  pre-training on 16,384 H100s, about one every three hours, and attributed
  roughly 78% of them to hardware (GPUs, HBM, NVLink, hosts) (verify against
  the Llama 3 paper).
- At an illustrative customer-declared price of $2.50 per GPU-hour, 4,096 GPUs
  cost $10,240 an hour. Every minute from failure to the next productive step
  costs about $170, and so does every minute of work redone since the last
  checkpoint.

So the product optimises two numbers: **time to resume** (failure to first
productive step) and **goodput** (productive GPU-time over allocated GPU-time).
Every mechanism below is judged by how much it moves them, and both are shown
first on a run, not buried.

### 4.2 The restart-time breakdown

A recovery is a sequence of stages. Each is measured, attributed to the
attempt that ended and the one that started, and shown as a waterfall; the
fleet view shows their percentiles across runs.

| Stage                    | From → to                                                      | Source of the timestamps                                                                               | Typical unoptimised (est.)                   | What shortens it                                                                                              |
| ------------------------ | -------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------ | -------------------------------------------- | ------------------------------------------------------------------------------------------------------------- |
| 1. Detect                | fault → a component declares the attempt failed                | Xid/ECC time (kernel log, DCGM), NCCL watchdog fire, heartbeat miss, last step time; attempt end event | 5–30 min (a hang waits for the NCCL timeout) | Heartbeats, a stall detector, a short collective timeout, DCGM/NPD signals ([§4.4](#44-detection-in-seconds)) |
| 2. Decide                | detected → recovery action chosen                              | remediation event                                                                                      | minutes when a person decides                | A declared policy ([§4.8](#48-the-declaration))                                                               |
| 3. Drain                 | action → the failed attempt's pods gone, the bad node cordoned | pod deletion, node `spec.unschedulable`                                                                | 1–5 min (graceful termination, stuck NCCL)   | Short grace periods for training pods; forced kill once a checkpoint is durable                               |
| 4. Reschedule            | pods gone → new pods bound                                     | Kueue `QuotaReserved`/`Admitted`, pod `PodScheduled`                                                   | seconds to hours (no capacity)               | A hot spare ([§4.6](#46-replace-without-a-full-restart)); Kueue reserved capacity                             |
| 5. Provision (if needed) | no node → a node joins                                         | autoscaler/Karpenter events, node `Ready`                                                              | 5–15 min                                     | Spares, so this stage is skipped                                                                              |
| 6. Pull                  | bound → images and weights local                               | kubelet `Pulling`/`Pulled`, image-pull histograms, init-container spans                                | 2–15 min for a 15 GB image                   | Pre-pulled images on spares, image streaming, a P2P cache ([§4.7](#47-cutting-restart-time))                  |
| 7. Init                  | containers started → process group ready                       | SDK spans: rendezvous, `init_process_group`, NCCL communicator init                                    | 1–10 min at large scale (verify)             | NCCL init tuning, fewer communicators, rendezvous backend choice                                              |
| 8. Load checkpoint       | process group ready → state restored                           | SDK span `ml.checkpoint.load` with bytes and tier                                                      | 2–20 min from object storage                 | In-memory or local NVMe tiers ([§4.5](#45-fast-checkpointing))                                                |
| 9. Warm up               | restored → first step completed                                | first `train.step` of the attempt; compile spans                                                       | 1–10 min with `torch.compile`                | Compile caches on a shared volume                                                                             |
| 10. Recompute            | first step → the step the failed attempt reached               | `steps_lost` = failed attempt's last step − resumed checkpoint step, times step time                   | half the checkpoint interval on average      | A shorter, cheaper checkpoint interval                                                                        |

Time to resume is the sum of stages 1–9; stage 10 is lost work counted in
goodput. The "typical" column is an estimate for orientation, unmeasured; the
product measures it per customer, which is the point.

Stages 1–6 need no customer code: they come from Kubernetes events, Kueue
conditions, DCGM and kernel logs through the cluster collector (ml-workloads
§3.2, drill-down 2). Stages 7–9 need the SDK. Without it, 7–9 show as one
"container start to first step" bar, estimated from logs, with the
`training-uninstrumented` finding.

### 4.3 Goodput accounting

Goodput is productive accelerator-time over allocated accelerator-time, for a
run, a phase, a team or the fleet. Badput is everything else, **always split by
cause**, because each cause has a different fix:

| Badput cause            | Measured as                                            | The fix it points at                     |
| ----------------------- | ------------------------------------------------------ | ---------------------------------------- |
| Queued holding capacity | allocated but not started (gang partly admitted)       | Kueue gang admission, `WaitForPodsReady` |
| Startup                 | stages 3–9 of every attempt                            | §4.7                                     |
| Detection latency       | stage 1                                                | §4.4                                     |
| Lost progress           | stage 10                                               | §4.5, the checkpoint interval            |
| Checkpoint blocking     | time the step loop waited on a save                    | Async save, a faster tier                |
| Hang                    | no step progress while allocated, until detected       | Heartbeats, collective timeouts          |
| Degraded (straggler)    | step time above the healthy baseline, times the excess | Replace the straggler's node             |
| Data stall              | `data_wait` share above its baseline                   | Loader workers, local cache, `/dev/shm`  |
| Eval and other pauses   | in-loop evaluation, declared pauses                    | Move evals to a side phase               |

Rules that keep the number honest:

- **Productive** means steps that survive into the final model's lineage. Steps
  redone after a restart, or skipped by a rewind, are counted once.
- Goodput is computed server-side from the ledger, the step series and the
  attempt timelines. The SDK never reports a goodput number of its own, so an
  uninstrumented run still gets time-based badput (queued, startup, dead time
  between attempts).
- With a declared price (ml-workloads decision 21), every badput row is also
  money, which is what makes the case for a spare node or a faster checkpoint
  store.
- Google publishes an "ML Goodput" measurement library with a similar split
  (verify its categories); the definitions here are chosen to map onto it.

### 4.4 Detection in seconds

The target is to detect a hard failure within **30 seconds** and a hang within
**two minutes** (proposed targets, to be measured on a design partner). Each
signal below is upstream; what is ours is collecting it, joining it to the
attempt and the rank, and acting on it within a declared policy.

| Signal                            | Detects                                         | Upstream source                                                                                      | Latency                | Ours                                                                                                                  |
| --------------------------------- | ----------------------------------------------- | ---------------------------------------------------------------------------------------------------- | ---------------------- | --------------------------------------------------------------------------------------------------------------------- |
| Xid, ECC DBE, row-remap failure   | A GPU fault                                     | Kernel log `NVRM: Xid` (node agent `filelog`), DCGM fields, node-problem-detector conditions         | seconds                | Join device UUID → pod → rank → attempt; classify with the versioned Xid table                                        |
| NPD node conditions and events    | Kernel, runtime, filesystem, GPU log rules      | node-problem-detector                                                                                | seconds to a minute    | Adopt or install NPD (ml-workloads slice 1)                                                                           |
| NCCL watchdog and flight recorder | A collective timed out or hung                  | PyTorch `TORCH_NCCL_*` (heartbeat, timeout, dump on timeout; verify names on the pinned version)     | the configured timeout | Render short timeouts; collect the dump from a collected volume                                                       |
| NCCL RAS                          | Peer and network faults inside NCCL             | NCCL's RAS subsystem (NCCL 2.24+, verify)                                                            | seconds                | Collect and join to the node                                                                                          |
| Rank heartbeats                   | A rank stopped making progress                  | TorchElastic agent, NVIDIA Resiliency Extension fault tolerance (verify), or the SDK's own heartbeat | the heartbeat interval | The SDK emits one per rank; the detector lives beside the job ([decision 24](#10-owner-decisions-decided-2026-11-26)) |
| Step stall                        | No step in k × the trailing median              | The SDK's step series, or an imported tracker's                                                      | k × step time          | Rule `run-stalled`                                                                                                    |
| Straggler                         | One rank slows all                              | SDK per-rank step time (`ml.train.rank.step.duration`)                                               | N steps                | Rule `straggler-rank`, with the rank's GPU throttle and error evidence                                                |
| Network                           | IB port down, link flaps, error counters rising | node-exporter InfiniBand collector, DCGM NVLink fields                                               | a scrape interval      | Join to the ranks on the node                                                                                         |
| Pod and node lifecycle            | Preemption, spot reclaim, node NotReady, OOM    | Kubernetes events and conditions, Kueue `Evicted`, termination notices                               | seconds                | Attempt end reason (closed set, spec 00 §2.4)                                                                         |

A failure is attributed once, to the attempt's end reason, with the evidence
attached. That is what turns "the job died" into "rank 412's GPU 3 raised Xid 79
on node h100-a-17 at 02:41:07; the attempt ended 11 seconds later".

### 4.5 Fast checkpointing

A checkpoint's cost has two parts: how long the step loop blocks, and how long
until the state is durable. The practices, all upstream:

- **Asynchronous and distributed saves.** Each rank writes its own shard in
  parallel, and the loop blocks only for the copy to host memory: PyTorch
  Distributed Checkpoint `async_save`, Megatron and NeMo distributed
  checkpointing, DeepSpeed's sharded checkpoints (verify each on the pinned
  versions).
- **Tiers.** Memory first, then local NVMe, then object storage, each on its
  own cadence:
  - **in memory, peer-replicated:** a copy of each shard held in a peer's
    host memory, so losing one node loses no state and resume needs no storage
    read (the approach of Gemini, ByteCheckpoint, NVIDIA Resiliency Extension's
    local checkpointing and torchft; verify each);
  - **local NVMe:** fast to write and read, lost with the node;
  - **object storage:** durable, slow, the only tier that survives losing the
    job.
- **An interval tuned from the measured failure rate.** Young's approximation
  gives the interval that minimises save cost plus expected lost work:
  τ ≈ √(2 · δ · M), with δ the blocking save time and M the mean time between
  failures; Daly refines it for long saves (verify the refinement before
  quoting it in copy). At δ = 30 s (async) and M = 3 h, τ ≈ 13 minutes and a
  failure loses about 7 minutes of work on average. At δ = 5 minutes
  (synchronous), τ ≈ 42 minutes, and saving alone costs about 12% of the run.

What forge does here, without writing a checkpointer:

- **Measures** δ per tier (SDK `ml.checkpoint.duration` split by `blocking` and
  `durable`) and M per pool and per run from the ledger's attempt end reasons.
- **Recommends** the interval: `checkpoint-interval-suboptimal` fires when the
  declared interval is far from √(2δM), with the GPU-hours or money the
  difference costs.
- **Renders** the tiers from the declaration: the volumes (memory-medium
  `emptyDir`, local NVMe paths, the object-store mount or credentials by
  reference), and the environment the SDK and the framework read
  (`FORGE_CHECKPOINT_DIR` from ml-workloads §3.1, plus a tier list).
- **Records** each checkpoint in the ledger with step, tier, digest when known
  and durability, so a resume always names a durable checkpoint and
  `steps_lost` is exact.
- **Proves resume works** before it matters: the smoke run kills and resumes
  ([§6.1](#61-smoke-training-per-pull-request)), and `no-checkpointing` and
  `checkpoint-never-restored` fire on runs that have never resumed successfully.

### 4.6 Replace without a full restart

Three recovery tiers, from fastest to most general. A team declares which it
allows; the stack decides which it can do.

| Tier                                  | What happens                                                                                                                                                       | Needs                                                                                                                                                                              | Typical time to resume (est.) |
| ------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------- |
| **A. In-job, no reschedule**          | The failed rank's process restarts in place, or a fault-tolerant group continues without it and it rejoins; healthy pods never restart                             | torchft-style per-step fault tolerance over replica groups, NVIDIA Resiliency Extension in-process restart, or TorchElastic restarting workers inside surviving pods (verify each) | seconds to a minute           |
| **B. Replace the node, keep the job** | The bad node is cordoned, a spare takes its place, the job restarts its workers (not its whole gang allocation) and resumes from the in-memory or local checkpoint | A hot spare already admitted and pulled; JobSet or Job failure policy that restarts in place; a peer-replicated checkpoint                                                         | 1–5 minutes                   |
| **C. Full restart**                   | The whole job is torn down, rescheduled and resumed from the last durable checkpoint                                                                               | Nothing beyond a durable checkpoint                                                                                                                                                | 10–60 minutes                 |

**Hot spares.** A spare is capacity that is admitted, scheduled, pulled and
idle, so stages 4–6 take seconds. Options (decision 29 chose the first, with the
second as the fallback):

- low-priority placeholder workloads in the job's Kueue ClusterQueue or cohort,
  which the job's replacement pods preempt (Kueue preemption and priority
  classes; verify the exact mechanics against the pinned Kueue);
- standby nodes held tainted, untainted by the remediation;
- the cloud's reservation or queued provisioning (GKE DWS, AWS capacity
  blocks), which reserves capacity but not warm nodes.

Spares are idle on purpose, so their cost is shown as **insurance**, apart from
idle findings (`accel-idle` must not fire on a declared spare), next to the
badput they saved.

**Failure policies forge renders.** Upstream already expresses most of tier B
and C:

- Kubernetes Job `podFailurePolicy` (ignore `DisruptionTarget`, fail fast on a
  user-code exit code) and `backoffLimitPerIndex` for Indexed Jobs (verify the
  versions where each went GA);
- JobSet `failurePolicy` with rules by child-job failure reason
  (restart the JobSet, restart without counting against `maxRestarts`, or fail)
  (verify field names on the pinned JobSet);
- Kubeflow Trainer v2 TrainJob, which creates a JobSet and inherits its
  policies (verify);
- TorchElastic `--max-restarts` and its rendezvous for in-job restarts.

forge renders these from one declaration so a team does not learn four
syntaxes, and refuses at render a combination the adopted orchestrator cannot
honour (for example tier A on a plain Job with no fault-tolerant runtime).

### 4.7 Cutting restart time

| Stage      | Practice                                                                      | Upstream                                              | forge's part                                                                                           |
| ---------- | ----------------------------------------------------------------------------- | ----------------------------------------------------- | ------------------------------------------------------------------------------------------------------ |
| Pull       | Images pre-pulled on spares and pool nodes; image streaming; a P2P pull cache | GKE Image Streaming, SOCI, Spegel, Dragonfly          | `MLStack.pull_cache` (ml-workloads slice 2); a pre-pull of the job's image digest onto declared spares |
| Pull       | Weights by digest from a near registry, cached per node                       | Image volumes, KServe LocalModelCache                 | `Workload.model` by digest (ml-workloads §3.5); `model-downloaded-per-pod`                             |
| Reschedule | Warm pools of admitted, idle capacity                                         | Kueue, priority classes                               | Spares (§4.6)                                                                                          |
| Init       | Shorter NCCL init and rendezvous at scale                                     | NCCL and PyTorch settings (verify which apply)        | SDK spans that measure it; a recommendation when init exceeds a threshold                              |
| Load       | Resume from memory or local NVMe before object storage                        | §4.5 tiers                                            | Tier rendering; the resume picks the fastest durable tier                                              |
| Warm up    | Persistent compile caches                                                     | `torch.compile` / Inductor cache directories (verify) | A shared cache volume rendered from the declaration                                                    |

### 4.8 The declaration

Resilience is a block on `forge.Training`, opt-in and additive (a template
without it renders byte-identically). The names are a sketch, not settled
syntax.

```python
import forge

trainings = [forge.Training {
    name = "pretrain-8b"
    orchestrator = "jobset"
    queue = "h100-research"
    workers = 512
    resilience = forge.Resilience {
        detect = forge.FailureDetection {
            heartbeat_timeout = "60s"         # per-rank heartbeat from the SDK
            stall_factor = 5                  # no step for 5x the trailing median = hang
            collective_timeout = "300s"       # rendered as the PyTorch NCCL timeout
            flight_recorder = True            # dumps land on a collected volume
        }
        checkpoint = forge.CheckpointPolicy {
            save = "async"
            interval = "auto"                 # tuned from measured save cost and MTBF
            tiers = [
                forge.CheckpointTier {kind = "memory", replicas = 1}
                forge.CheckpointTier {kind = "local", path = "/mnt/nvme/ckpt"}
                forge.CheckpointTier {kind = "object", uri = "gs://acme-ckpt/pretrain-8b", every = "1h"}
            ]
            keep = 5
        }
        spares = forge.Spares {nodes = 2, prepull = True}
        recovery = ["in_job", "replace_node", "full_restart"]   # tiers allowed, fastest first
        remediation = forge.Remediation {
            mode = "act"                      # off | recommend | act; default recommend
            rules = [
                forge.RemediationRule {on = "gpu_error", do = ["cordon_node", "replace", "resume"]}
                forge.RemediationRule {on = "hang", do = ["dump", "restart_workers", "resume"]}
                forge.RemediationRule {on = "loss_spike", do = ["rewind"], skip_data = True}
                forge.RemediationRule {on = "straggler", do = ["replace"], after = "15m"}
            ]
            max_actions_per_hour = 6          # past this, stop and page
            notify = ["inbox", "slack:#training-oncall"]
        }
    }
}]
```

What forge renders from it, all into the existing bundle:

- the orchestrator's failure policy (JobSet `failurePolicy`, Job
  `podFailurePolicy`, TorchElastic flags);
- the PyTorch and NCCL environment for timeouts, heartbeats and flight-recorder
  dumps, with the dump directory on a collected volume;
- checkpoint volumes and the tier list the SDK reads;
- spare placeholders and their priority class, and a pre-pull of the job's
  image digest onto them;
- alert rules (`forge.AlertRule`) for stalls, hangs, goodput drops and
  remediation storms, routed by the declared `notify`;
- the remediation executor's configuration and its Kubernetes RBAC, visible in
  `forge cluster connect --dry-run` before it is granted (decision 24).

**Remediation is not rollback.** Reliant does not roll releases back on its
own (recovery rolls forward), and this does not change that. Resuming the same
declared run from its own last durable checkpoint is a different act: the code,
template and data are unchanged, and nothing is promoted. A `rewind` after a
loss spike is recorded as an attempt with `rewound_to_step` and the skipped data
range, so the lineage stays exact. A remediation never changes a template, a
model release or an inference service.

**Guardrails.** `mode` defaults to `recommend` when the block is declared and
the team has not chosen `act`. `act` needs an explicit grant on the cluster.
`max_actions_per_hour` stops a remediation loop and pages. Every action is an
activity event with its evidence, and appears in the Inbox.

### 4.9 Upstream or ours

| Capability                                                      | Upstream                                                                  | Ours                                                                                                       |
| --------------------------------------------------------------- | ------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------- |
| GPU fault signals                                               | DCGM, NVRM kernel log, node-problem-detector                              | Collection, the Xid table as data, the device → rank → attempt join                                        |
| Collective hang detection and dumps                             | PyTorch NCCL watchdog, flight recorder, NCCL RAS                          | Rendering the settings, collecting the dumps, attaching them to the attempt                                |
| Restart and failure policy                                      | Job, JobSet, Trainer, TorchElastic                                        | One declaration rendered onto them                                                                         |
| Fault-tolerant training without a restart                       | torchft, NVIDIA Resiliency Extension, framework features                  | Detecting and declaring which the job uses; measuring its effect                                           |
| Checkpoint save and load                                        | PyTorch DCP, Megatron, DeepSpeed, Orbax                                   | Tiers rendered, durability recorded, the interval recommended                                              |
| Queueing, quota, preemption, spare capacity                     | Kueue                                                                     | Spare placeholders rendered; spare cost shown as insurance                                                 |
| Cordon, replace, rewind across nodes                            | NVSentinel and cloud auto-repair act on nodes (verify NVSentinel's scope) | The executor that ties a node action to a run and resumes it (decision 24); adopt NVSentinel where present |
| Restart-time breakdown, goodput and badput by cause, the ledger | None joins these across the stack                                         | **Ours, and the differentiator**                                                                           |

### 4.10 Testing resilience without GPUs

Everything here is proven on CPU-only k3d (ml-workloads §13), with chaos
injected on purpose:

- a CPU PyTorch DDP job (gloo) under JobSet and Kueue, killed mid-run to force
  each recovery tier; the restart waterfall and `steps_lost` are asserted;
- a synthetic Xid on a fake-GPU node (synthetic exporter fixtures plus a planted
  kernel-log line) to drive `gpu_error` → cordon → replace → resume;
- a `SIGSTOP` on one rank to drive the hang path (heartbeat miss, dump,
  restart);
- a sleep in one rank's step for the straggler rule;
- KWOK for spare admission and scheduling at scale.

Real detection latency, NCCL dump behaviour on real fabrics and in-memory
checkpoint restore at scale are proven only on hardware, at the first design
partner.

## 5. Feedback-loop hooks

The owner's direction: build the hooks, not the loops. Users plug in the
forge CLI however they like, and Reliant workflows are one harness on top. The
hooks are four: a typed event stream, query APIs with CLI verbs, triggers, and
write APIs that launch or fork a run.

### 5.1 A typed event stream

Every state change in the ledger is an event, reusing forge's activity stream
(`pkg/activity`: actor, verb, target, env, via, detail, trace id; `Record` for
non-RPC actors like a controller) rather than a new bus. Each event is also an
OTel log record (`event.name = ml.<kind>`, the same primitive as product
events, `pkg/observe/track.go`) so it is queryable in ClickHouse and joinable to
the telemetry around it.

| Event                                                                        | Detail (sketch)                                                                      |
| ---------------------------------------------------------------------------- | ------------------------------------------------------------------------------------ |
| `ml.run.launched`, `ml.run.state_changed`, `ml.run.finished`                 | run, experiment, template release, launcher (person, agent, workflow), state, reason |
| `ml.phase.state_changed`                                                     | phase key and kind, state                                                            |
| `ml.attempt.started`, `ml.attempt.ended`                                     | attempt n, end reason (closed set), `steps_lost`, restart breakdown                  |
| `ml.checkpoint.saved`, `ml.checkpoint.durable`, `ml.checkpoint.failed`       | step, tier, bytes, blocking and durable seconds, digest                              |
| `ml.eval.finished`, `ml.eval.regressed`                                      | suite, subject (checkpoint or model digest), scores, delta against baseline, CI      |
| `ml.anomaly.detected`                                                        | kind (loss spike, NaN, stall, straggler, goodput drop), evidence refs                |
| `ml.remediation.proposed`, `ml.remediation.applied`, `ml.remediation.failed` | rule, action, subject, outcome, time to resume                                       |
| `ml.model.recorded`, `ml.model.promoted`                                     | model, digest, source run and checkpoint, release, env                               |
| `ml.finding.opened`, `ml.finding.resolved`                                   | rule id, subject, impact                                                             |

The event kinds and their detail keys are a public surface under semver: added
to, never changed.

### 5.2 Query APIs and CLI verbs

The control plane's ML service gains the entity reads the spec lists
(spec 04 §2, "Data contract"): runs, phases, attempts, checkpoints, evals,
models, lineage, fleet, findings, time series by entity. Agents read the same
calls. The CLI mirrors them, with stable `--json` shapes and exit codes a script
can branch on:

```bash
forge ml runs --experiment support-8b --state running --json
forge ml runs get llama-8b-support-v3 --json      # phases, attempts, checkpoints, goodput
forge ml compare llama-8b-support-v3 llama-8b-support-v2 --json   # config diff, metric summaries, eval deltas
forge ml evals --model support-assistant --json
forge ml events --follow --run llama-8b-support-v3 --json       # the event stream, one JSON object per line
forge ml wait llama-8b-support-v3 --until finished              # 0 succeeded, 1 failed, 2 cancelled, 8 timed out
```

`forge ml wait` copies `forge obs check`'s exit-code style so loops written for
releases read the same way. `forge ml events --follow` is the hook for any
harness: a shell loop, a Python script, CI, or an agent.

### 5.3 Triggers for Reliant workflows

Reliant workflows already trigger on integrations, webhooks, schedules and
workflow events (reliant `internal/triggers/triggerspec`). ML adds trigger types
over the event stream, declared the way integration triggers are
(`internal/integrations/catalog/*/manifest.yaml`), with CEL filters on the
event's detail:

- `ml.run.finished` (filter on state, experiment, template);
- `ml.eval.regressed` (filter on suite, size of the delta);
- `ml.anomaly.detected` (filter on kind; goodput drop is one);
- `ml.attempt.ended` (filter on end reason);
- `ml.remediation.failed`.

They are delivered as typed trigger types of an integration manifest named
`forge`, with payload schemas (decision 27), not a generic event trigger.

### 5.4 Launch and fork

Write APIs, each a recorded launch against a declared template, never a
cluster mutation around one:

```bash
forge ml run pretrain-8b --set lr=2e-4 --set warmup_steps=500       # launch (ml-workloads §8)
forge ml fork llama-8b-support-v3@41200 --set lr=1e-4                # new run from a checkpoint, with overrides
forge ml cancel llama-8b-support-v3
```

- **Overrides are bounded by the template.** `forge.Training.params` declares
  which keys a launch may set and their types or ranges; anything else needs a
  template change, which is a reviewed pull request.
- **Budgets are declared.** GPU-hours per day and concurrent runs, per template
  and per actor kind (person, agent, workflow). A launch past budget is refused
  and says which budget.
- **Approvals follow Changes.** Launches into queues the team marks as
  production need an approval, as production Changes do (decision 28).
- **Fork is lineage.** A fork records `initialized_from` the checkpoint, so
  comparison and the lineage graph see the family.

### 5.5 What can be built on the hooks, later

Not proposed for building now; listed to show the hooks are sufficient:

- **Autoresearch.** A Reliant workflow, triggered by `ml.run.finished`, reads
  `forge ml compare`, has an agent propose the next experiment as a pull request
  (a template change) or a bounded launch within budget, waits with
  `forge ml wait`, and records what it learned. People review the pull
  requests; budgets bound the spend.
- **RL environments.** Rollouts run as workflows in Reliant sandboxes, the
  verifier's rewards are written back as eval results or step scalars through
  the SDK or `forge ml eval record`, and the training phase consumes them. The
  missing piece for a real RL product (a rollout request and response contract
  between trainer and environment) is deliberately left until a design partner
  shows the shape.

## 6. Testing and previews for ML

### 6.1 Smoke training per pull request

A change to training code, data loading or a template gets a smoke run: a tiny
model on a data slice, for a few dozen steps, in the pull request's preview env
(`forge env preview`). It is declared on the template:

```python
smoke = forge.SmokeRun {
    params = {"model_size": "tiny", "max_steps": "60", "data_slice": "0.1%"}
    substrate = "cpu"            # cpu (gloo, fake GPUs) | a declared cheap pool
    assert_loss_decrease = 0.1   # loss at the last step at least 10% below the first
    assert_finite = True         # no NaN or Inf in loss or grad norm
    resume_at_step = 30          # kill and resume: loss continues, steps are continuous
    throughput_tolerance = 0.25  # within 25% of main's last smoke on the same substrate
}
```

Results are reported as test results into the Tests pipeline
([tests-coverage-flakes.md](tests-coverage-flakes.md)), so a pull request shows
them beside its unit tests, and the flaky-test rule applies: a smoke assertion
that fails intermittently is fixed or removed, never retried to green. The
resume assertion is the cheapest proof that recovery (§4) works for this code;
a template with resilience declared and no passing resume is a finding.

Throughput on CPU is noisy and only catches gross regressions, so the default
tolerance is wide. Real performance regressions are caught on the customer's
own pool, by a declared cheap pool for the smoke run, or in the running job's
own step time after the change ships (with the commit from runtime context).

### 6.2 Eval gates: the tests of a model

An eval suite is to a model release what a test suite is to a code release.

- Results are recorded against a checkpoint or model digest (ml-workloads
  drill-down 6, `forge ml eval record`), with sample counts and confidence
  intervals.
- A suite can be a named gate in `DeliveryPolicy.required_gates`, so a model
  release whose suite has not passed is not promoted. Gating is opt-in and comes
  after display (ml-workloads decision 22).
- A regression is judged statistically: a task "regressed" only when the
  difference from the baseline exceeds its confidence interval and a declared
  minimum; a gate on a noisy eval blocks releases for nothing.
- Results show on the Tests page beside the release, and an `ml.eval.regressed`
  event fires either way.

### 6.3 Determinism and numerics

- **Determinism on CPU:** with a fixed seed and deterministic algorithms, the
  first N losses of the smoke run match main's bit for bit. A mismatch with no
  intended change is a finding, not a failure, because some kernels are
  nondeterministic by design.
- **Numerics:** the first k steps in bf16 stay within a declared tolerance of an
  fp32 reference; grad norms are finite; loss-scale overflows are counted.
- **Inference:** greedy outputs on a golden prompt set, and the log-prob drift
  against the serving model digest, run in the preview against the candidate
  model.

### 6.4 Data validation

The dataset version's digest is recorded on each phase (`trained_on` lineage).
Validators are the customer's (schema checks, token counts, deduplication,
contamination against eval sets); forge runs a declared validator as a job in
the preview or before a launch and records its outcome as a test result. It
never re-implements a validator.

### 6.5 Inference in previews

The preview serves the candidate model (CPU vLLM with a small model by default,
ml-workloads §13), runs the golden-output check, and `forge obs check` judges
the preview's SLIs, as it judges any preview.

## 7. Observability, alerts and releases: where they already stand

These are designed in ml-workloads and not repeated here:

- **Hardware, scheduling, inference, training, runs and evals drill-downs**,
  the recommendations catalogue, cardinality and cost: ml-workloads "ML
  observability" (forge#2884). The spec's metrics inventory widens the field set
  (full DCGM groups, NPD, Kueue, pod-start stages, ~60 training metric
  families, every vLLM metric).
- **Releasing models**, model-only releases, rolling forward: ml-workloads §7.
- **Comparing runs and versions**, offline and through Changes: ml-workloads §6.

What this proposal adds on top:

- **Default ML alert rules**, as forge ships default app alerts: bad GPU under a
  running job, run stalled, hang detected, goodput below a declared floor,
  checkpoint not durable for longer than twice its interval, eval regressed,
  remediation storm. They are `forge.AlertRule`s like any other, overridable,
  and land in the Inbox.
- **Traces that cross layers:** a sampled training step or inference request
  span carries the run, attempt, rank and device attributes, so the trace view
  links to the DCGM series of that device, the kubelet stats of that pod, and
  the Kueue admission and scheduling spans of that attempt (pod-start timelines
  per ml-workloads decision 20).

## 8. Security

- **Model artifacts ride the release path.** A ModelPack artifact gets an SBOM
  (the release's SBOM-per-artifact, [sbom-vulnerabilities.md](sbom-vulnerabilities.md)),
  a signature from the delivery signer (control-plane `internal/deliverysign`),
  and admission verification where declared
  ([signed-image-admission.md](signed-image-admission.md)). Until Reliant's
  registry serves the referrers API, referrers use the `sha256-<hex>` tag
  fallback the registry already recognises (`internal/ociregistry/referrers.go`;
  ml-workloads decision 14).
- **Provenance.** Each model version carries an attestation of its lineage:
  the run, phases and checkpoint it came from, the dataset digests, the
  template's release and commit, the image digests. The attestation format
  (in-toto/SLSA provenance) is to be verified against what the delivery signer
  writes.
- **Unsafe pickles.** Weights in pickle-based formats (`.bin`, `.pt`, `.pkl`,
  `.ckpt`) can execute code on load. `forge ml model push` scans them for unsafe
  opcodes (adopting an existing scanner; verify which) and prefers safetensors.
  A lint rule, `model-unsafe-format`, warns first and gates from the release
  after it, per the lint rollout table; refusing a pickle at promotion is a
  declared `DeliveryPolicy` control the team opts into (decision 31). The SDK
  and skill load checkpoints with `torch.load(weights_only=True)` (the default
  since PyTorch 2.6; verify).
- **Scopes for data and weights.**
  - Datasets stay in the customer's storage. Reliant records URIs and digests,
    never the data. Eval samples, which may contain customer data, are stored in
    the customer's bucket, like profiler captures (ml-workloads decision 19).
  - Model pulls into a connected cluster use a per-(cluster, env) read-only
    credential scoped to that project's `models/` path (ml-workloads decision 15).
  - Token scopes for the new surfaces: read, launch, remediate and model push
    are separate scopes, so an agent that compares runs cannot launch them, and
    one that launches cannot remediate.
- **Secrets.** Hugging Face tokens, tracker keys and bucket credentials are
  forge secret references bound per env, never values in KCL. Workload
  identity is preferred where the cloud offers it.
- **The remediation executor's Kubernetes RBAC** is declared and minimal (cordon nodes,
  delete and create pods of the job's namespace, update its own records), part
  of the render, shown by `connect --dry-run`, and granted only when `act` is
  declared.
- **Spend is a security property.** A launched run spends the customer's GPU
  money, so launch and fork are scoped, budgeted and audited in the activity
  stream (§5.4).
- **Monitoring requires sign-in**, and ML is gated per org, unchanged.

## 9. How this relates to ml-workloads.md

| ml-workloads                              | This proposal                                                                                          |
| ----------------------------------------- | ------------------------------------------------------------------------------------------------------ |
| Settled 8: one nav item, six sub-pages    | Amended by the owner, 2026-11-26: hybrid nav (decision 23)                                             |
| Decision 18: instrumentation vehicle      | Settled by the owner: the published Python SDK, taught by the `ml/instrument` skill                    |
| Explicitly out: automatic cordon or drain | Amended by the owner: out by default; possible when declared (`Remediation.mode = "act"`, decision 32) |
| §2 Run: one launch of a template          | Refined into the ledger of runs, phases, attempts and checkpoints (the spec's object model)            |
| §8 Agent tools                            | Generalised into the hooks of §5                                                                       |
| Slices 0–8, decisions 1–22                | Unchanged, except as above; this proposal's decisions continue the numbering from 23                   |

## 10. Owner decisions (decided 2026-11-26)

Numbering continues ml-workloads' (1–22). The owner accepted every
recommendation this section proposed ("agreed with your recs", 2026-11-26
06:01Z). They are recorded as [section 3.1](#31-settled-owner-2026-11-26-decisions-23-to-32),
and the sections above describe the chosen option. The options that were
weighed and not chosen, for the record:

| #   | Decision                                            | Chosen                                                                                                                                                                       | Not chosen                                                                                                                     | Unblocks                       |
| --- | --------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------ | ------------------------------ |
| 23  | Navigation                                          | Hybrid, with **Training**, not Runs: an ML area for Training, Evals, Models, Fleet and setup; the rest in Deployments, Previews, Observability, Inbox and Tests              | One ML item with every ML page (duplicates pages that exist); fully distributed with no ML area (leaves training with no home) | Phase A pages                  |
| 24  | Who executes cross-node remediation                 | Upstream (JobSet, TorchElastic, NVSentinel, cloud auto-repair) for what it can do, plus a small forge-rendered in-cluster executor only for what ties a node action to a run | The control plane acting through the hub's connection (too slow, and fails when the hub cannot reach the cluster)              | Resilience actions (phase E5)  |
| 25  | Where the training-run ledger lives                 | Control-plane Postgres per org; step scalars in a new step-keyed ClickHouse table                                                                                            | Inside the release ledger (per env, promotion-shaped; a sweep would flood it); ClickHouse only                                 | Phase E                        |
| 26  | Python SDK packaging                                | `sdk/python/` in the forge repository, Apache-2.0, published to PyPI; 0.x until a design partner confirms the API, then 1.0                                                  | A separate repository                                                                                                          | The SDK track                  |
| 27  | How ML triggers reach Reliant workflows             | A `forge` integration manifest with typed trigger types and payload schemas                                                                                                  | A generic event trigger with a CEL filter on `event.name` (works, but gives agents no schema)                                  | Hooks H2                       |
| 28  | Launch authority for agents and workflows           | Recorded launches within declared budgets and parameter bounds; production queues need an approval                                                                           | Template changes only, through reviewed pull requests (every sweep a PR); free launches within a budget (production unguarded) | Hooks H3                       |
| 29  | Hot spare mechanism                                 | Low-priority Kueue placeholder workloads where Kueue is adopted; tainted standby nodes as the fallback                                                                       | Cloud reservations only (capacity, but not warm, pulled nodes)                                                                 | Resilience actions             |
| 30  | Smoke-run substrate                                 | Both, CPU by default (gloo, fake GPUs); a cheap GPU pool when the team declares one                                                                                          | CPU only; a GPU pool only                                                                                                      | §6.1                           |
| 31  | Pickle-format weights                               | Warn first, then a lint gate per the rollout table, plus an opt-in `DeliveryPolicy` control that refuses promotion                                                           | Warn only (never moves anyone); refuse pickles outright (breaks teams on older formats the day they pin)                       | `forge ml model push`, slice 4 |
| 32  | Default remediation mode when the block is declared | `recommend`; `act` is stated explicitly                                                                                                                                      | `act` by default                                                                                                               | Resilience actions             |

Decision 22 (eval gates) and 18 (instrumentation, settled on 2026-11-26 for the
published SDK) are ml-workloads'; they are not repeated.

## 11. Build order

The spec's phases (spec 04 §3) and ml-workloads' slices stay the spine. This
proposal places resilience and the hooks in them explicitly. Two tracks run in
parallel: **D**, demo fixtures and views; **R**, real ingestion.

| Phase (spec)                              | ml-workloads slice | What lands                                                                                                                                              | Resilience                                                                                                             | Hooks                                                                                              |
| ----------------------------------------- | ------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------- |
| 0. Access                                 | 0                  | Entitlement gate, request page with the alert-rule Slack notice, demo shell (control-plane#3747, reliant#3693)                                          | —                                                                                                                      | —                                                                                                  |
| A. The demo is the target (D)             | —                  | Reshaped early-access contract around the object model; fixture story; pages in the hybrid nav (decision 23)                                            | Fixture attempts with end reasons, a restart waterfall, goodput and badput by cause, a remediation in the Inbox        | Fixture event stream; the CLI verbs' `--json` shapes drafted against fixtures                      |
| B. Fleet and health (R)                   | 1                  | Cluster collector, NVIDIA adapter, NPD, `obsquery.Machines`, `pkg/mlrecs`                                                                               | **R1 detection, hardware half:** Xid, ECC, NPD, IB signals joined to pods                                              | **H1:** `ml.finding.*` events in the activity stream; `forge ml` read verbs for fleet and findings |
| B′. SDK track (R, parallel from B)        | —                  | The Python SDK on CPU: conventions, callbacks for HF Trainer and Lightning first, then torchtitan, Megatron-LM and DeepSpeed; the `ml/instrument` skill | SDK heartbeats, checkpoint durability, init and load spans                                                             | —                                                                                                  |
| C. Scheduling and pod start (R)           | 2                  | Kueue adoption, events, pod-start stages                                                                                                                | Restart stages 3–6 measured                                                                                            | —                                                                                                  |
| D. Inference (R)                          | 3                  | Serving adapters, inference release health, previews serving a candidate model                                                                          | —                                                                                                                      | `ml.model.*` events                                                                                |
| E1. Observed runs (R)                     | 5                  | The **training-run ledger** from orchestrator status, Kueue and pods; attempts with end reasons                                                         | **R0 measurement:** the restart-time breakdown and time-based goodput for every observed run, no customer code         | **H2:** run, phase, attempt events; query APIs; workflow triggers (decision 27)                    |
| E2–E3. Imported and instrumented runs (R) | 5                  | MLflow-compatible endpoint, W&B links; the SDK proven at the design partner                                                                             | **R2 checkpointing:** tier rendering, durability in the ledger, interval recommendation; full goodput with stages 7–10 | `ml.checkpoint.*`, `ml.anomaly.*`                                                                  |
| E4. Launch (R)                            | 5                  | `forge.Training` lowering, `forge ml run`, smoke runs in previews (§6.1)                                                                                | Failure policies rendered; the smoke run's kill-and-resume                                                             | **H3:** launch, fork, cancel, budgets, approvals (decision 28)                                     |
| E5. Resilience actions (R)                | new                | `forge.Resilience`: spares, remediation in `recommend`, then `act`                                                                                      | **R3:** hot spares (decision 29), replace-node recovery, the executor (decision 24), chaos tests on k3d                | `ml.remediation.*`                                                                                 |
| F. Evals and models (R)                   | 4, 6               | Eval ingest, gates as `required_gates`, ModelPack push, signing, provenance, the pickle scan                                                            | —                                                                                                                      | `ml.eval.*` and the `ml.eval.regressed` trigger                                                    |
| G. Breadth                                | 7, 8               | Private clusters, more vendors                                                                                                                          | In-job fault tolerance (tier A) as frameworks mature                                                                   | —                                                                                                  |

Multi-cluster scheduling (forge#2994; step 4 of [ml-mvp.md §5](ml-mvp.md#5-build-order)) is **on hold (owner, 2026-11-26)**: a separate repository, hooking into what the customer already runs, or holding indefinitely, none chosen yet.

Why this order:

- **Measurement before action.** R0 (restart breakdown and goodput from
  observed runs) needs no customer code and no grant, and it is what makes the
  case for everything after it: a team that sees "212 GPU-hours lost to restarts
  this week, 40% of it image pulls" asks for spares and pre-pulls.
- **The hooks land with the data they describe**, so each event kind ships with
  the ledger state behind it, never ahead of it.
- **Resilience actions come last among the training work** because they act on
  a customer's cluster and must be proven on chaos tests and then at a design
  partner. They are an explicit phase, E5, not an afterthought of E.
- **The SDK runs as its own track from phase B**, on CPU, so it is ready when
  the first design partner arrives at E3, as the owner's "SDK first" asks.

## Appendix: verification list

Upstream claims marked "(verify)" above, to be checked against the pinned
versions before they become rules, rendered configuration or user-facing copy:

- Llama 3 interruption counts and their attribution;
- PyTorch NCCL environment variables for heartbeat, timeout and dump-on-timeout,
  and the flight recorder's output;
- NCCL RAS availability and version;
- JobSet `failurePolicy` rule fields and actions; Job `podFailurePolicy` and
  `backoffLimitPerIndex` GA versions; Kubeflow Trainer v2's use of JobSet;
- Kueue preemption and priority mechanics for spare placeholders;
- torchft, NVIDIA Resiliency Extension (fault tolerance, in-process restart,
  local checkpointing, straggler detection) and NVSentinel scope;
- PyTorch DCP `async_save`, Megatron and DeepSpeed distributed checkpointing;
- Gemini and ByteCheckpoint in-memory checkpointing claims; Daly's refinement of
  Young's interval;
- Google's ML Goodput library categories;
- NCCL init and `torch.compile` cache settings that shorten restarts;
- the pickle scanner to adopt; `torch.load` `weights_only` default version;
- the attestation format the delivery signer writes.
