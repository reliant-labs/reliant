# 04 — Gap vs the demo, and a phased build plan

**Status:** product spec draft, 2026-11-26. Compares the demo as built (reliant#3693,
control-plane#3747, both open; preview at
https://teal-jaguar.sites.reliantlabs.dev/forge/ml/machines; screenshots in
`../ml-demo/`) with `00`–`03`. The demo's data contract is control-plane
`proto/services/ml_early_access/v1/ml_early_access.proto` (6 RPCs, flat messages).

The owner's verdict stands: the demo covers roughly a tenth of the useful data. This
file says exactly which tenth, and in what order to build the rest.

## 1. Settled constraints the plan keeps

- Customer clusters only; no GPU provisioning; **no GPU spend** for testing (fixtures,
  fake-gpu-operator, KWOK, CPU vLLM opt-125m, CPU torch/gloo).
- NVIDIA first; neutral schema (01-metrics neutral field names).
- KCL-declarative: findings fix through KCL diffs/PRs; nothing mutates a cluster outside
  a declaration or a recorded launch.
- Discover → adopt → install-only-missing.
- Gated access (per-org entitlement, `FEATURE_NOT_ENTITLED`); demo shell shows all of it.
- Run tracking, model registry and inference comparison held until a design partner
  **for real ingestion** — but the owner's review means their **demo fixtures and views**
  should be built now, so the design partner sees the target.
- forge semver major 1: additive only; never stand in for another tool.

## 2. Gap by view

Legend: ✓ in demo · ◐ partial · ✕ absent.

### Runs (biggest gap)

| Spec item (02-views §3)                                                    | Demo                                        | Gap                                                                                |
| -------------------------------------------------------------------------- | ------------------------------------------- | ---------------------------------------------------------------------------------- |
| Clickable list → run detail                                                | ✕ rows are not links; no detail page exists | the owner's explicit ask                                                           |
| Experiments level, grouping, sweeps                                        | ✕                                           | entity + list + leaderboard + parallel coordinates                                 |
| Phases (pretrain/SFT/RL/eval…)                                             | ✕ a run is one CR                           | phase DAG model + Gantt                                                            |
| Attempts/waves with end reasons                                            | ◐ restart/preemption counts only            | per-attempt bars, end reason, steps lost                                           |
| Progress, step/total, ETA, tokens                                          | ✕ (duration only, shown as "540m 31s")      |                                                                                    |
| Health badges (straggler, stall, NaN, spike, bad device)                   | ✕                                           |                                                                                    |
| Metrics workspace (loss, LR, grad norm, throughput, MFU, memory, MoE, RL…) | ◐ one loss sparkline                        | ~60 training metric families (01c)                                                 |
| System tab per rank/node/device                                            | ✕                                           | rank heatmap, straggler panel, per-device DCGM                                     |
| Logs                                                                       | ✕                                           | rank-tagged merged logs with auto-highlights                                       |
| Traces                                                                     | ✕                                           | step/collective/checkpoint/data spans                                              |
| Profiles                                                                   | ✕                                           | Kineto/Nsight/memory snapshot/flight recorder/eBPF                                 |
| Checkpoints                                                                | ✕                                           | table, save/load cost, at-risk steps, promote/rewind                               |
| Evals in run                                                               | ✕                                           | score vs step                                                                      |
| Config & lineage                                                           | ◐ `params` map, template name               | effective config with provenance, parallelism, env, data, code diff, lineage graph |
| Events                                                                     | ✕                                           | merged k8s/Kueue/device/training timeline                                          |
| Cost & goodput                                                             | ✕                                           | GPU-h, $, badput categories                                                        |
| Compare                                                                    | ✕                                           | summary, config diff, overlays, parallel coords, eval diff                         |

### Evals

| Spec item (§4)                                                 | Demo                                                                         | Gap        |
| -------------------------------------------------------------- | ---------------------------------------------------------------------------- | ---------- |
| Model × benchmark matrix with baseline deltas and significance | ✕ (3 numbers inline on a model row: `eval/accuracy`, `eval/loss`, `rouge_l`) | whole page |
| Eval run detail, slices, harness/judge config, cost            | ✕                                                                            |            |
| Drill to examples, side-by-side, flipped samples               | ✕                                                                            |            |
| Gates                                                          | ✕                                                                            |            |
| Online evals per revision / Changes arm                        | ✕                                                                            |            |

### Models

| Spec item (§5)                                                   | Demo                            | Gap                                                           |
| ---------------------------------------------------------------- | ------------------------------- | ------------------------------------------------------------- |
| Registry list with aliases per env                               | ◐ versions table with tag v1/v2 | aliases, env mapping                                          |
| Model card (arch, params, dtype, context, base, license, shards) | ◐ size only                     |                                                               |
| Lineage graph                                                    | ◐ run id text                   | interactive graph dataset→phases→ckpt→version→release→service |
| Eval scorecard vs production                                     | ◐ 3 raw numbers                 |                                                               |
| Promotion gates & history, alias audit                           | ✕                               |                                                               |
| Serving performance per hardware                                 | ✕                               |                                                               |

### Inference

| Spec item (§6)                                            | Demo                            | Gap                                      |
| --------------------------------------------------------- | ------------------------------- | ---------------------------------------- |
| Services list                                             | ◐ one card per service          | sortable list with SLO, errors, $/1M tok |
| Latency p50/p95/p99 over time: queue, TTFT, ITL, e2e      | ◐ single p95 numbers            | time series + release markers            |
| Time split queue/prefill/decode                           | ✕                               |                                          |
| Traffic, tokens in/out, finish reasons, per-adapter       | ◐ tokens/s number               |                                          |
| Engine: batch, KV%, preemptions, prefix hits, spec-decode | ◐ KV% number                    |                                          |
| Replicas table/heatmap                                    | ◐ replica count                 |                                          |
| Cold start                                                | ✓ stage breakdown (good — keep) | per-replica waterfall                    |
| Revisions + verdicts + compare                            | ◐ one verdict line              | revision table, A/B via Changes          |
| Request traces                                            | ✕                               |                                          |
| Cost                                                      | ✕                               |                                          |

### Fleet (Machines)

| Spec item (§7)                     | Demo                                             | Gap                                                                                                                                                                                                        |
| ---------------------------------- | ------------------------------------------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Allocation vs utilisation per pool | ✓ line chart                                     | heatmap + percentile bands                                                                                                                                                                                 |
| Node table                         | ✓ alloc, util, mem, health text, driver          | clickable; health score                                                                                                                                                                                    |
| Node detail                        | ✕                                                | whole page                                                                                                                                                                                                 |
| GPU detail                         | ✕                                                | whole page (all DCGM groups)                                                                                                                                                                               |
| DCGM coverage                      | ◐ util + memory only (~2 of ~80 relevant fields) | profiling pipes, power/limit, temps incl. HBM, clocks + clock-event reasons, violations, ECC SBE/DBE vol/agg, retired pages, row remap (4 fields), Xid history, PCIe replay/link, NVLink per-link + errors |
| NPD conditions/events              | ◐ one condition string ("Xid 79 on GPU-5")       | all NPD conditions/events + kubelet/containerd health                                                                                                                                                      |
| Host/kubelet/IB                    | ✕                                                | CPU, PSI, disk, image fs, IB counters                                                                                                                                                                      |
| Topology/NVLink                    | ✕                                                |                                                                                                                                                                                                            |
| Bad-machine score + history        | ✕                                                |                                                                                                                                                                                                            |
| Cost/idle                          | ◐ idle count in header                           | $ and GPU-h idle, top idle table                                                                                                                                                                           |

### Queues / scheduling

| Spec item (§8)                                                                               | Demo                                     | Gap              |
| -------------------------------------------------------------------------------------------- | ---------------------------------------- | ---------------- |
| Queues page (Kueue quota, usage, borrowing, pending with reasons, preemptions, wait p50/p95) | ✕                                        | whole page       |
| Pod start stages fleet-wide                                                                  | ◐ only inside a finding / inference card | page + waterfall |

### Recommendations

| Spec item (§9)                                        | Demo                          | Gap                                                 |
| ----------------------------------------------------- | ----------------------------- | --------------------------------------------------- |
| Inbox: sortable table, grouped, prioritised by impact | ✕ flat list of expanded cards | redesign (owner: "particularly ugly")               |
| Impact in $/GPU-h/latency/run-risk                    | ✕                             |                                                     |
| Evidence as charts                                    | ✕ (text key/values + code)    |                                                     |
| Triage states, assign, snooze, verify-after-fix       | ✕                             |                                                     |
| Open KCL diff / Open PR                               | ◐ static code block           | real diff + PR action                               |
| Coverage findings collapsed                           | ✕                             |                                                     |
| Rule count                                            | 6 fixtures                    | ~40 rules in ml-workloads §5 + addendum + this spec |

### Setup (Stack)

| Spec item (§10)           | Demo                                  | Gap                              |
| ------------------------- | ------------------------------------- | -------------------------------- |
| Coverage checklist        | ✕ full KCL blocks for every component | collapse KCL, lead with coverage |
| Adopted/installed/missing | ✓                                     | keep                             |

### Cross-cutting

| Item                                   | Demo                                       | Gap                                                                                                  |
| -------------------------------------- | ------------------------------------------ | ---------------------------------------------------------------------------------------------------- |
| Overview page                          | ✕                                          | new                                                                                                  |
| Synced crosshair/brush, event overlays | ✕ (one chart, unsynced, tooltip shows "—") | chart kit                                                                                            |
| Compare tray                           | ✕                                          |                                                                                                      |
| Click-through everywhere               | ✕ most ids are text                        |                                                                                                      |
| Not-collected states                   | ✕                                          |                                                                                                      |
| Ask about this / Draft the fix         | ✕                                          |                                                                                                      |
| Visual quality                         | owner: "ugly"                              | dense tables, header strips, one palette; a design pass with `frontend/design` brief before building |

### Data contract

`ml_early_access.proto` has 6 list RPCs and flat summary messages. The spec needs
entity RPCs (Get/List per entity), time-series queries (by entity + metric family +
range + step/time axis), events, traces/profiles links, lineage edges and findings with
impact. Recommendation: keep the early-access service as the fixture contract but
reshape it now (it is unreleased — reliant#3693/cp#3747 are open, so no v1 compatibility
burden yet) around the entities in `00-object-model.md`:

- `ListExperiments`, `ListRuns`, `GetRun` (with phases, attempts, checkpoints), `GetRunTimeline`,
  `QueryRunMetrics` (keys, step/time axis, attempts, ranks, downsample),
  `ListRunEvents`, `ListRunArtifacts` (profiles, dumps), `CompareRuns`;
- `ListEvalRuns`, `GetEvalMatrix`, `ListEvalSamples`;
- `ListModels`, `GetModel`, `GetModelVersion`, `GetLineage`;
- `ListInferenceServices`, `GetInferenceService`, `QueryInferenceMetrics`, `ListRevisions`;
- `GetFleet`, `GetPool`, `GetNode`, `GetDevice`, `QueryDeviceMetrics`, `ListHealthEvents`;
- `ListQueues`, `ListPendingWorkloads`, `GetPodStartBreakdown`;
- `ListFindings` (grouped, impact, triage state), `GetFinding`;
- `GetMlStack` (coverage checklist).

Traces and profiles link to the existing Observability views and stores rather than
duplicating them. This is an **owner decision G1** (reshape now vs after design partner).

## 3. Phased build plan

Two tracks run in parallel: **D** = demo/fixtures + views (no real data; can ship now,
gated, to show design partners); **R** = real ingestion (needs code in forge,
control-plane, and a cluster). Each phase lists both. Real-hardware proof = the first
design partner.

### Phase A — Make the demo the target (fixtures only) — D

Goal: the owner sees ~80% of the product in the demo shell before any ingestion work.

1. Design brief + chart kit (synced crosshair, brush, event overlays, percentile bands,
   heatmap, Gantt, small multiples, not-collected placeholder, compare tray) in reliant
   `frontends/web`. Load `frontend/design` and `web-styling`; get the owner's brief first.
2. Reshape `ml_early_access.proto` around the entities (G1).
3. A **fixture story generator** (deterministic, seeded) in `fixtures/shop/handlers/ml*`:
   one experiment with 3 runs; run v3 = pretrain (1 attempt) → SFT (3 attempts: preempted,
   Xid 79, running) → GRPO pending, eval-periodic every 2k steps; rank 37 straggler on a
   throttling GPU; ~60 training series (01c) generated from plausible curves; per-rank
   series for 16 ranks; DCGM full field set per device for 2 pools; NPD events; Kueue
   queues with pending reasons; 2 inference services × 2 revisions with full SLIs and 50
   request traces; eval suite of 8 tasks × 4 subjects with samples; models with lineage;
   ~25 findings across categories with impact.
4. Pages in order of owner pain: Runs list (clickable) → Run detail (timeline, Overview,
   Metrics, System, Checkpoints, Events, Config, Compare) → Recommendations inbox →
   Evals → Models → Inference → Fleet drill (node, GPU) → Queues → Overview → Setup.
   Traces/Profiles tabs render fixture traces and a sample Perfetto link.

Exit: owner review of the demo; iterate.

### Phase B — Fleet and health for real (slice 1 of ml-workloads, widened) — R

- Cluster collector (`k8sobjects`, `k8s_events`), NVIDIA adapter as versioned data with
  the **full** DCGM field map (01a), the Xid table, the throttle/clock-event decoder,
  NPD adoption/installation with GPU rules, kubelet `nodes/metrics`, node-exporter /
  IB where present.
- `obsquery.Machines`/`Accelerators`/`HealthEvents`; health score.
- Findings engine (`pkg/mlrecs`) with impact computation and triage state.
- Proof: k3d + fake-gpu-operator (+ injected DCGM fields, synthetic Xid), KWOK for scale.

### Phase C — Scheduling and pod start — R

- Kueue adopt + metrics + Workload conditions; scheduler/autoscaler/Karpenter events;
  `obsquery.PodStarts` (useful for all forge workloads). Queues page live.
- Proof: k3d with Kueue + JobSet + fake GPUs.

### Phase D — Inference — R

- vLLM/SGLang/TGI/Triton adapters; vLLM OTLP tracing via `ml/instrument`; metric
  findings in release health; revisions from the release ledger; cost per 1M tokens.
- Proof: CPU vLLM (opt-125m) in k3d; two revisions; `forge obs check` judging TTFT.

### Phase E — Runs: observe first, then instrument (design partner) — R

1. **Observed runs** (no customer code): runs/phases/attempts from orchestrator CRs +
   Kueue + pods + labels; end reasons; pod-start per attempt; per-device hardware joined
   via pod attribution; logs with highlights; cost and goodput (time-based badput).
2. **Imported metrics:** MLflow-compatible endpoint and MLflow/W&B readers (00 §10).
3. **Instrumented runs:** `ml.train.*` OTel conventions (01c) via the `ml/instrument`
   skill (HF Trainer/Lightning callbacks, plain-loop snippet); step breakdown, MFU,
   stragglers, checkpoints, sampled step traces; profiling captures as run artifacts
   (decision 19); flight-recorder dumps; eBPF CPU profiles.
4. Launch (`forge ml run`, phases in `forge.Training`).

- Proof: CPU torch/gloo multi-pod run in k3d (straggler injected via sleep), a
  preemption via Kueue, a kill to force a resume; real hardware at the design partner.

### Phase F — Evals and models — R

- Eval ingest (`forge ml eval record`, lm-eval-harness/Inspect/MLflow importers), samples
  storage, matrix + significance; ModelPack push, aliases from releases, gates (decision
  22), lineage edges.
- Online evals + continuous Changes readout (decision 8 b).

### Phase G — Breadth

Private clusters (pull mode), AMD/Neuron/Gaudi/TPU adapters, reports/sharing, alerts from
charts, topology-aware views at scale.

## 4. Risks and how the plan handles them

| Risk                                                 | Mitigation                                                                                                             |
| ---------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------- |
| Demo depth implies features we will not ship soon    | demo labels "Early access"; each fixture-only view names its data source in "How it was built"                         |
| Fixture realism (owner knows the space)              | fixtures generated from published curve shapes and real metric names (01 files); owner reviews the fixture story first |
| Cardinality/cost of full DCGM + per-rank             | adapter allow-lists, per-rank only on 2 series, rollups (decision 17), 6h default per-device windows                   |
| Building a W&B clone                                 | V3 hybrid: strong defaults + overlays + compare, import/link the rest                                                  |
| Profiling conflicts (DCGM profiling vs Nsight/CUPTI) | capture flow pauses DCGM profiling on the node or warns (01c gotchas)                                                  |
| No real hardware                                     | design partner is the proof; everything else simulated (settled)                                                       |

## 5. Owner decisions raised by this file

| #   | Decision                                                                  | Options                                               | Recommendation                                                                                   |
| --- | ------------------------------------------------------------------------- | ----------------------------------------------------- | ------------------------------------------------------------------------------------------------ |
| G1  | Reshape the early-access proto now                                        | (a) now, while unreleased; (b) after design partner   | (a): the current shape cannot carry phases/attempts/time series                                  |
| G2  | Build the full demo (Phase A) before real ingestion for Runs/Evals/Models | (a) yes; (b) only Fleet/Recs/Queues                   | (a): the owner's review asked for depth; a design partner needs to see the target                |
| G3  | Ship order of real ingestion                                              | B→C→D→E→F (this file) vs E earlier                    | B→C→D first: no customer code, value on connect day                                              |
| G4  | Merge the open demo PRs (reliant#3693, cp#3747) as-is or redo first       | (a) merge gate/access parts, redo pages; (b) redo all | (a): the entitlement gate and access page are sound; the pages and proto get replaced in Phase A |
