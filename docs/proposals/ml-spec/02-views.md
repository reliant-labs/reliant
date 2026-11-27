# 02 — Views

**Status:** product spec draft, 2026-11-26. Every page, tab and drill-down of the ML area,
with its purpose, an ASCII wireframe, the questions it answers, its metrics, its
interactions and its empty/partial states. Entities are defined in `00-object-model.md`;
every metric name used here is in `01-metrics-inventory.md` (and its `parts/` files).
The competitive bar each view must clear is in `03-competitive-bar.md`.

---

## 0. Principles for every view

These answer the owner's "looks ugly" as much as the "10%". They are rules, not taste.

1. **One question per screen, answered above the fold.** Each page opens with a header
   strip of 4–6 numbers that answer its main question (with deltas vs the comparison
   window), then the evidence below. No page opens with a wall of cards or code.
2. **Everything is clickable to the next level.** A row, a bar, a point on a chart, a
   node in a graph, an event marker: each opens the entity it names. No dead text. Ids
   in monospace are links. Breadcrumbs carry the full path
   (`ML › Runs › llama-8b-support-v3 › sft › attempt 3 › rank 37`).
3. **Shared time, synced cursors.** All charts on a page share one time range and one
   hover cursor (crosshair moves across every chart; the tooltip lists every series at
   that instant). Brush-select on any chart zooms all of them. Training charts offer the
   x-axis choice **step / tokens / wall time / relative time** and keep it synced.
4. **Events are overlays, not a separate list only.** Restarts, preemptions, checkpoint
   saves, Xids, ECC, NPD conditions, deploys/releases, alias moves, Changes ramps, loss
   spikes and finding openings are drawn as markers/bands on every chart they affect,
   with a legend to toggle each kind. Clicking a marker opens its evidence.
5. **Distributions over fleets, lines over a few.** Fleet panels are heatmaps,
   percentile bands (p5/p50/p95), and top-N tables (the 50-series cap in `obsquery`).
   One line per device appears only at node level (≤ 8–16 devices).
6. **Absent ≠ zero.** A metric we do not receive shows a **"not collected"** state that
   names the missing source and links the finding that fixes it (e.g. "SM occupancy
   needs DCGM profiling counters → `dcgm-profiling-off`"). Never an empty chart, never 0.
7. **Compare is universal.** Any list (runs, attempts, ranks, nodes, devices, model
   versions, revisions, eval runs) supports multi-select → **Compare**. A persistent
   compare tray at the bottom holds the selection across pages.
8. **Cost on every subject.** Each entity header shows GPU-hours and, with a declared
   price sheet, money — split into productive vs waste.
9. **Agent-ready.** Every page has "Ask about this" which opens a Reliant chat seeded with
   the page's entity and time range; findings have "Draft the fix" (opens a PR with the
   KCL diff). The agent reads the same queries the page draws.
10. **Density with hierarchy.** Tables are compact (32px rows), numbers right-aligned
    and tabular, status as colored dot + word, sparklines inline. Severity color only for
    severity; series colors from one categorical palette; one accent. Dark and light.

---

## 1. Navigation

The settled nav (ml-workloads item 8) is one **ML** item with six sub-pages. The owner's
review asks for Evals and for scheduling as first-class views; this spec proposes nine
entries. **Owner decision V1** below.

```
ML
  Overview          org home: what is running, what is wasting, what is broken
  ── Work
  Runs              experiments → runs → phases → attempts
  Evals             suites × models matrix, regressions, samples
  Models            registry, versions, lineage, gates
  Inference         services → revisions → replicas → requests
  ── Infrastructure
  Fleet             clusters → pools → nodes → devices   (was "Machines")
  Queues            Kueue queues, pending work, why it waits, pod-start
  Recommendations   triaged findings with impact and one-click fixes
  Setup             the ML stack per cluster: adopted / installed / missing (was "Stack")
```

Global controls on every page: **scope** (org / project / cluster), **time range**
(15m … 90d, plus "this run's lifetime"), **compare tray**, **Ask about this**. URLs carry
all state (deep-linkable, shareable).

---

## 2. Overview (new)

**Purpose:** the first screen of the morning. "Is anything burning, where is the money
going, what finished?"

```
┌ ML › Overview ───────────────────────────────── scope: acme ▾   last 24h ▾ ┐
│ GPUs  512 total · 431 allocated · 58% busy · 74 idle-allocated · 3 faulty    │
│ GPU-h 9,840 (24h) · productive 71% · $42.1k · waste $9.3k  ▲12% vs prev 24h  │
├──────────────────────────────────┬──────────────────────────────────────────┤
│ Needs attention (5)              │ Fleet utilisation (heatmap: pools × time)│
│ ● r4vb GPU-5 Xid 79 — 1 run hit  │ h100-train ▓▓▓▓▓▓▒▒░░▓▓▓▓▓▓▓▓            │
│ ● llama-8b-v3 straggler rank 37  │ l4-serve   ▒▒▒▒▒▓▓▓▓▓▓▒▒▒▒▒▒             │
│ ● 6 jobs pending > 1h (quota)    │ a100-dev   ░░░░░░░░▒▒░░░░░░             │
│ ● support-asst TTFT p95 +31%     │                                          │
│ ● 74 GPUs idle-allocated ($310/h)│                                          │
├──────────────────────────────────┼──────────────────────────────────────────┤
│ Active runs (7)                  │ Inference (4 services)                   │
│ name        phase  step   ETA  ⚠ │ svc   rev  rps  TTFT95 ITL95 err  KV%    │
│ llama-8b-v3 sft  41.2k/60k 6h ● │ ...                                      │
├──────────────────────────────────┼──────────────────────────────────────────┤
│ Recently finished: runs, eval runs, model promotions, releases (timeline)  │
└─────────────────────────────────────────────────────────────────────────────┘
```

- **Answers:** what is broken now; idle and wasted GPU-hours; which runs are at risk;
  which services breach SLO; what changed (promotions, releases, stack changes).
- **Primary metrics:** allocated vs busy devices, idle-allocated count and $/h,
  GPU-hours by category (cost), open findings by severity × impact, active runs with
  progress/ETA/health, serving SLIs.
- **Interactions:** every row opens its entity; heatmap cell → pool at that time;
  "Needs attention" is the top 5 of Recommendations by impact.
- **Empty states:** no cluster connected → the connect flow (`forge cluster connect`,
  `forge ml discover`) with the demo link; cluster connected, no exporter → Fleet in
  allocation-only mode plus the `accel-telemetry-missing` finding; no runs → "Runs appear
  when you launch from a template, or when we observe a training job" with the three
  ways in (label, MLflow URI, W&B id).

---

## 3. Runs

### 3.1 Runs list (clickable — the owner's first ask)

```
┌ ML › Runs ─────────────────────────────────── project: shop ▾  24h ▾ ┐
│ [Experiments ▾ support-assistant-8b] [State ▾] [Phase ▾] [Cluster ▾] [Owner ▾] [Tag ▾] [Search params: lr>1e-4] │
│ Group by: experiment ▾   Columns ▾   Saved views ▾                     │
├──┬────────────────────┬────────┬─────────────────────────┬───────┬─────┬──────┬────────┬──────┬──────┤
│☐ │ Run                │ State  │ Phases (mini-Gantt)     │ Step  │ ETA │ GPUs │ Goodput│ GPU-h│ loss │
├──┼────────────────────┼────────┼─────────────────────────┼───────┼─────┼──────┼────────┼──────┼──────┤
│☐ │ llama-8b-support-v3│●running│ pt▇▇▇│sft▇▇▇▇░│rl ·     │41.2k  │ 6h  │16×H100│ 83%  │ 1,204│ ╲__  │
│  │ maya · 9h · sweep 4│ ⚠ strag│  a1 a2 a3 (3 attempts)  │/60k   │     │      │        │ $5.1k│ 1.92 │
│☐ │ llama-8b-support-v2│✓ ok    │ pt▇▇▇│sft▇▇▇▇│eval▇     │60k    │  —  │16×H100│ 91%  │ 1,800│ ╲__  │
│☐ │ llama-8b-support-v1│✕ failed│ pt▇▇│sft▇✕ (Xid 79, a4)   │22k    │  —  │16×H100│ 54%  │   560│ ╲_/  │
└──┴────────────────────┴────────┴─────────────────────────┴───────┴─────┴──────┴────────┴──────┴──────┘
│ Compare (2) ▸                                                                  │
```

- **Answers:** what is running, how far along, which is unhealthy, what each cost, which
  is best on the objective.
- **Columns (configurable):** name + owner + age + sweep/trial; state with a health badge
  (straggler, stalled, loss-spike, NaN, restart-storm, bad-device); mini-Gantt of phases
  with attempt ticks; current step/total and tokens; ETA; accelerators; goodput %;
  GPU-hours and $; restarts/preemptions; any **summary metric** or **param** as a column
  (min val loss, best eval, lr, batch size); sparkline of the objective.
- **Interactions:** row → Run detail; mini-Gantt segment → that phase/attempt; filter by
  any param/metric expression; group by experiment, sweep, template, cluster, any param;
  sort by any column; multi-select → Compare; bulk tag/archive; saved views per user.
- **Experiments level:** the Experiments filter has a list view of its own (name, runs,
  best run by objective, last activity, GPU-h total, owner) and an **Experiment page**
  = this list scoped, plus a **leaderboard** and a **parallel-coordinates** panel over its
  runs (params → objective) and a **param-importance** table.
- **Empty/partial:** observed jobs with no tracking show state, GPUs, attempts and cost
  but "—" for step/loss, with an inline "Track this run" link (`runs-untracked`).

### 3.2 Run detail — the frame

```
┌ ML › Runs › llama-8b-support-v3 ─────────── ●running ⚠ straggler · [Compare] [Ask] [⋯ Cancel] ┐
│ exp support-assistant-8b · template support-assistant-sft@rel v1.8.3 (abc123) · maya · 9h ago │
│ Step 41.2k/60k · 2.7B tokens · ETA 6h10m · 16×H100 · goodput 83% · 1,204 GPU-h · $5.1k        │
├ Timeline (Gantt) ──────────────────────────────────────────────────── time ▾ | step ▾ ────────┤
│ pretrain  ▇▇▇▇▇▇▇▇▇▇▇▇▇▇ a1 ✓                                                                │
│ sft             │▇▇▇▇ a1 ✕pre│░q│▇▇▇▇▇ a2 ✕xid79│░q│▇▇▇▇▇▇▇ a3 ●                             │
│ eval-periodic   │   ◆   ◆   ◆      ◆       ◆        (each ◆ = an eval of a checkpoint)        │
│ rl (GRPO)                                                      · pending on sft              │
│ events: ▲ckpt ▲ckpt  ✕preempt  ▲ckpt ✕Xid79 ...   ░ = queued   ▒ = starting                  │
├ Tabs: Overview │ Metrics │ System │ Logs │ Traces │ Profiles │ Checkpoints │ Evals │ Config & Lineage │ Events │ Cost ┤
```

- **The timeline is the hero.** One row per phase; each attempt is a bar segmented into
  **queued / starting (pod-start stages) / running / checkpointing / stalled**, ending
  in an icon for its end reason (00-object-model §2.4). Checkpoints are ticks; evals are
  diamonds; data Segments are bands. Hover → attempt summary (duration, steps done,
  steps lost, end reason, nodes). Click → scopes the tabs to that phase/attempt.
- **Scope selector:** tabs operate on `run | phase | attempt` (default: the selected
  phase, all attempts overlaid on the step axis).
- **Header actions:** Compare (adds to tray), Ask about this, Cancel / Suspend / Resume
  (launch-scoped permissions `ml:launch`), Rewind (relaunch phase from a checkpoint with
  a data skip range — a new attempt with end reason `rewind`), Promote checkpoint →
  ModelVersion.

### 3.3 Run tab: Overview

```
┌ Health ───────────────────────────────┐┌ Progress ───────────────────────────────┐
│ ⚠ Straggler: rank 37 (h100-5 GPU 3)   ││ loss (train/val) vs step, phase bands   │
│   step +18% since 02:10, HW slowdown  ││ ✕ markers for attempts, ▲ checkpoints   │
│ ✓ No NaN/Inf · ✓ grad norm stable     ││                                          │
│ ✓ Data loader wait 3%                 │├ Throughput ──────────────────────────────┤
├ Where the time went (this phase) ─────┤│ tokens/s, MFU, step time p50/p95        │
│ █████████████░░▒▒▓  goodput 83%       │└─────────────────────────────────────────┘
│ productive 83 · queued 6 · startup 3  │┌ Step time breakdown (stacked by phase) ─┐
│ restart-lost 5 · ckpt 2 · stall 1     ││ data_wait forward backward opt comm ckpt│
└───────────────────────────────────────┘└─────────────────────────────────────────┘
┌ Key numbers: best val loss 1.88 @38k · last eval MMLU-Pro 41.2 (+0.8) · MFU 41% · HFU 47% ┐
```

- **Answers:** is it healthy, how far, how fast, where did time and money go, what is the
  last eval saying.
- **Metrics:** loss (train/val), eval headline, tokens/s, MFU/HFU, step-time breakdown
  by `ml.step.phase`, goodput decomposition, active findings for the run.

### 3.4 Run tab: Metrics

A W&B-class chart workspace (the bar in 03-competitive-bar) over step-indexed scalars.

```
┌ Panels: [auto by prefix ▾]  x: step ▾  smoothing ──●──── 0.6  ☐log y  ☐ignore outliers  [+ panel] ┐
│ ▸ loss/      train/loss  val/loss  loss per domain (web, code, math, books)                       │
│ ▸ optim/     lr (schedule phase band) · grad_norm (global, clipped %) · update/param ratio        │
│ ▸ stability  loss_scale · skipped steps · NaN/Inf count · loss-spike detector (z-score)          │
│ ▸ throughput tokens/s (global, per-GPU) · samples/s · step time p50/p95 · MFU · HFU               │
│ ▸ memory     allocated/reserved/peak per rank (band) · fragmentation · alloc retries             │
│ ▸ data       tokens per source · mixture weights · loader wait · epoch                            │
│ ▸ moe        expert load (heatmap experts × step) · aux loss · tokens dropped · router z-loss    │
│ ▸ rl         reward mean/std · KL to ref · entropy · clip fraction · response length · pass rate │
│ ▸ eval       per-task score vs tokens (from eval-periodic)                                         │
│ ▸ custom     everything else the run logs, grouped by "/" prefix                                   │
```

- **Interactions:** synced crosshair across panels; brush to zoom; smoothing (EMA,
  Gaussian, running avg) with raw shown faint; log scale; outlier clipping; min/max
  shading across ranks or across runs in a group; per-panel series picker with regex;
  attempts overlay on the step axis with a resume marker where attempt n+1 starts (the
  steps between the last checkpoint and the failure are drawn dashed: work redone);
  "add panel" from any metric key; panel layout saved per experiment.
- **Phase-specific default panels:** pretrain (loss per domain, tokens seen vs plan,
  scaling-law fit), SFT (per-task loss, packing efficiency), DPO (rewards chosen/rejected,
  margins, accuracies), PPO/GRPO (reward, KL, entropy, clip fraction, value loss,
  advantage stats, response length, rollout vs train time split), distillation
  (teacher/student KL), eval phase (scores vs checkpoint step).
- **Partial:** a run imported from MLflow shows its logged keys only; missing families
  show "not logged" with the `ml/instrument` skill link.

### 3.5 Run tab: System (hardware per rank and node)

```
┌ View: [Ranks ▾ | Nodes | Devices]  metric: [SM active ▾]  attempt: a3 ▾ ┐
│ Rank heatmap (rank × time), color = SM active / step compute time / comm wait     │
│ r0  ▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓                                                            │
│ ...                                                                              │
│ r37 ▓▓▓▓▓▒▒▒▒▒▒▒▒▒▒▒   ← straggler (compute ↑, comm_exposed ↓)                   │
├ Straggler panel ──────────────────────────────────────────────────────────────────┤
│ rank 37 · pod sft-w-4 · node h100-5 · GPU 3 (UUID …a91) · since 02:08             │
│ clock-event: HW slowdown 64% of time · temp 87°C (mem 95°C) · power 412/700 W     │
│ [Open device] [Cordon (KCL/command)] [Rewind without node]                        │
├ Per-node small multiples (≤16): util, SM, tensor, DRAM, power vs limit, temp, NVLink tx/rx, PCIe, IB ┤
├ Errors on this run's devices: Xid, ECC SBE/DBE, row remap, NVLink CRC/replay, PCIe replay (markers) ┤
```

- **Answers:** is the job compute-, memory-, input- or communication-bound; which rank
  is the straggler and why; did a hardware fault cause the restart.
- **Metrics:** per-rank step compute and `comm_exposed`; per-device DCGM profiling
  (GR_ENGINE_ACTIVE, SM_ACTIVE, SM_OCCUPANCY, PIPE_TENSOR_ACTIVE, DRAM_ACTIVE), memory
  used, power vs limit, GPU/memory temp, clocks + clock-event reasons, NVLink/PCIe
  bytes, IB port counters, host CPU/mem/PSI, `/dev/shm`, network.
- **Interactions:** click a heatmap cell → that rank at that time across every panel;
  device → Fleet GPU detail; pick a time window → "Capture profile for steps N–M".
- **Partial:** no pod attribution → falls back to node + allocation (banner:
  `accel-pod-attribution-off`); no profiling counters → GPU_UTIL only (banner:
  `dcgm-profiling-off`); time-sliced GPUs → per-pod numbers marked "shared device".

### 3.6 Run tab: Logs

- All pods of the selected attempt(s), merged and **rank-tagged**; filter by rank,
  pod, node, container, level, regex; "errors only"; pin the first error per attempt.
- **Auto-highlights:** NCCL WARN/timeout, watchdog, CUDA OOM, `NVRM: Xid`, Python
  tracebacks, `Bus error` (shm), loss NaN; each highlight links to its event marker.
- Live tail while running; jump-to-time from any chart; download.

### 3.7 Run tab: Traces

- **Step traces** (sampled: rank 0 every Nth step, every slow step, all ranks in a
  profiling window): a waterfall `train.step` → data fetch / forward / backward /
  optimizer / collectives (`all_reduce`, `reduce_scatter`, `all_gather`, by bucket) /
  checkpoint; side-by-side **rank lanes** for the same step to see who arrived late at
  the collective.
- **Checkpoint traces:** serialize → write shards → upload → commit, with bytes and
  throughput per span.
- **Data pipeline traces:** fetch → decode → tokenize → collate, per worker.
- Slow-step list (step, rank, duration vs median, dominant span) → opens the trace.
- **Empty:** `training-uninstrumented` with the skill; the "Profiles" tab may still
  have a capture.

### 3.8 Run tab: Profiles

- **Captures list:** kind (PyTorch profiler/Kineto, Nsight Systems, Nsight Compute,
  memory snapshot, flight-recorder dump, py-spy), trigger (declared window / auto on
  regression / manual / watchdog), steps, ranks, size, link to open in **Perfetto**
  (Kineto), Nsight (download), our memory-snapshot viewer, and an **HTA summary** we
  compute: temporal breakdown (compute / comm / memcpy / idle), top kernels by time,
  comm-comp overlap %, kernel launch gaps, CPU-side launch-bound indicator.
- **Continuous CPU flamegraph** (eBPF, Python+native frames) for the selected attempt
  and time range, diffable between two windows or two runs.
- **Hang dump view:** per-rank last collective (seq id, op, size, state) from the
  flight recorder, with the mismatched rank highlighted.
- **Action:** "Capture profile" → declares a profiling window on the live run
  (steps N–M, ranks) — a recorded action, not a cluster mutation outside the run.

### 3.9 Run tab: Checkpoints

```
│ step   │ phase │ attempt │ kind      │ size   │ save s │ upload s │ async │ val loss │ evals     │ status   │
│ 41,000 │ sft   │ a3      │ sharded   │ 212 GB │ 38     │ 140      │ yes   │ 1.88     │ ◆ 41.2    │ durable  │
│ 40,000 │ sft   │ a3      │ sharded   │ 212 GB │ 41     │ 151      │ yes   │ 1.90     │ ◆ 40.9    │ durable  │
│ 32,000 │ sft   │ a2      │ sharded   │ 212 GB │ 40     │ —        │ yes   │ 1.95     │           │ partial ✕│
```

- **Answers:** where can I resume from, what does checkpointing cost, which checkpoint
  is best, which became a model.
- **Metrics:** save/load duration, bytes, throughput, async vs sync blocking time,
  interval (steps and minutes), last durable checkpoint vs current step (**at-risk
  steps**), storage used.
- **Actions:** Evaluate this checkpoint (launch EvalRun), Promote → ModelVersion, Rewind
  from here, Compare with another checkpoint (eval diff).
- **Recommendation hooks:** `checkpoint-interval-too-long` (expected loss per failure vs
  observed MTBF), `checkpoint-blocking` (sync save > x% of step time),
  `no-checkpointing`.

### 3.10 Run tab: Evals

- Score-vs-step (or tokens) curves per task from `eval-periodic`; a table of the latest
  checkpoint vs the run's baseline with deltas and CIs; click a cell → the Evals sample
  drill (§4.3). Release-gate suites show pass/fail per gate.

### 3.11 Run tab: Config & Lineage

- **Config:** effective params (template defaults → launch overrides → per-phase
  overrides, each value annotated with where it came from), parallelism layout
  (DP/TP/PP/CP/EP, ZeRO stage, micro/global batch, grad accumulation), environment
  (image digest, CUDA, driver, NCCL version + `NCCL_*`/`TORCH_NCCL_*` env, PyTorch
  version), hardware (product, count, topology, node pool), data (dataset versions +
  digests + mixture), code (release, commit, diff vs the previous run's commit via
  runtime-context).
- **Diff vs:** previous run / baseline run / any run — keys that differ only.
- **Lineage graph:** datasets → phases (with attempts collapsed) → checkpoints →
  ModelVersions → releases → InferenceServices; click any node to open it.

### 3.12 Run tab: Events

- One merged timeline: Kueue (created, quota reserved, admitted, evicted, requeued),
  scheduler (FailedScheduling with classified reason), pods (pulling/pulled with
  size+duration, started, OOMKilled, exit codes, DisruptionTarget), nodes (NotReady,
  NPD conditions), devices (Xid, ECC, throttle episodes), orchestrator (JobSet restarts,
  TrainJob conditions), training (checkpoints, loss spikes, NaN, rewinds), people/agents
  (launch, cancel, rewind, profile capture). Filter by kind; each event links to its
  evidence.

### 3.13 Run tab: Cost

- GPU-hours and $ by phase × attempt × category (productive, queued-holding, startup,
  checkpoint, restart-lost, stalled, idle-allocated); $ per 1B tokens trained; projected
  cost to finish; comparison with the experiment's median run.

### 3.14 Run comparison

```
┌ Compare 3 runs ─── baseline: v2 ▾ ───────────────────────────────────────────┐
│ Summary table: rows = runs; cols = objective, best val loss, final evals,    │
│   tokens, GPU-h, $, goodput, MFU, restarts — deltas vs baseline colored      │
├ Config diff (only differing keys): lr 3e-4 | 2e-4 | 3e-4 · data mix … · image…┤
├ Overlaid curves (step axis): loss, eval, grad norm, tokens/s — per run color  │
├ Parallel coordinates (params → objective) when ≥ 4 runs                      │
├ System diff: MFU, step breakdown stacked per run, comm share                  │
├ Eval diff: task × run matrix with significance                               │
└───────────────────────────────────────────────────────────────────────────────┘
```

- Also: **attempt comparison** within a run (why was a3 slower than a1? different nodes,
  driver, image) and **rank comparison** within an attempt.

---

## 4. Evals

### 4.1 Evals matrix

```
┌ ML › Evals ── suite: release-gate-v7 ▾  baseline: production ▾  models: support-assistant ▾ ┐
│                  │ MMLU-Pro │ GSM8K │ IFEval │ support-v3 │ safety │ judge(win%) │ cost/run │
│ prod  sha…56ad v2 │  40.4    │ 71.2  │ 78.0   │ 0.871      │ 0.991  │ —           │ $41      │
│ cand  ckpt@41k   │  41.2 ▲  │ 70.1 ▼*│ 79.3 ▲ │ 0.884 ▲*   │ 0.987 ▼│ 56% ▲*      │ $43      │
│ cand  v3-rl@2k   │  41.0    │ 74.8 ▲*│ 77.1 ▼ │ 0.879 ▲    │ 0.979 ▼* GATE ✕│ 61%  │ $45      │
│ (* = significant at 95%; red = gate fails)                                                  │
```

- **Answers:** which candidate is best, what regressed, is it significant, does it pass
  the gate, what did the eval cost.
- **Cells:** score, delta vs baseline, CI/stderr, n; color by significance not by raw
  delta. Rows: model versions, checkpoints, live revisions, external models (an
  API baseline). Columns: tasks or suites; expandable to metrics and slices.
- **Interactions:** click a cell → task detail (§4.3); click a row → model version /
  checkpoint; pin baseline; sort; filter tasks by category (knowledge, reasoning, math,
  code, long-context, safety, instruction-following, agentic, internal).

### 4.2 Eval run detail

- Header: subject, harness + version, suite version, dataset digests, seed, sample
  limit, judge model + rubric version, duration, tokens, $, who ran it.
- Per-task scores with stderr; per-slice table; latency and token distributions of the
  subject during the eval; logs; config.

### 4.3 Task drill-down to examples

```
┌ GSM8K · cand ckpt@41k vs prod v2 · 1,319 samples · 70.1 vs 71.2 (−1.1, p=0.21) ┐
│ Filter: [regressed only ▾] [slice ▾] [search text]                              │
│ id   │ prod │ cand │ input (truncated)                 │ cand output     │ judge │
│ 0412 │ ✓    │ ✕    │ "Janet has 3 apples…"             │ "…= 14" (ref 12)│  —    │
│ ── side-by-side diff of outputs, rationale, tokens, latency ──                    │
```

- Win/loss/tie counts between two subjects (McNemar / paired bootstrap), samples that
  flipped, judge agreement with humans, annotate/label a sample (sends to an annotation
  queue in the customer's tool or ours).

### 4.4 Online evals

- Per InferenceService revision and per Changes arm: user feedback rate, task success,
  guardrail trigger rate, LLM-judge score on sampled traffic, with the inference SLIs of
  the arm beside it.

---

## 5. Models

### 5.1 Registry

```
│ Model              │ Latest │ production (prod) │ staging    │ candidate │ Versions │ Serving        │ Last eval │
│ support-assistant  │ v3     │ v2 sha…56ad       │ v3 sha…a1f │ ckpt@41k  │ 7        │ 2 svcs, 1.8k rps│ gate ✓ v3 │
```

### 5.2 Model detail

```
┌ ML › Models › support-assistant ─────────────────────────────────────────────┐
│ Aliases: production → v2 (prod) · staging → v3 (staging) · candidate → ckpt@41k │
├ Versions ───────────────────────────────────────────────────────────────────┤
│ v │ digest   │ from                 │ base        │ params │ dtype │ size │ eval gate │ serving    │
│ v3│ sha…a1f  │ llama-8b-v3 / rl a3  │ llama-3.1-8b│ 8.0B   │ bf16  │ 16GB │ ✓ 5/5     │ staging    │
├ Lineage graph (interactive) ────────────────────────────────────────────────┤
│ dataset:support-v4 ─▶ run v3: pt → sft(a1,a2,a3) → rl(a1..a3) ─▶ ckpt@2k ─▶ v3 ─▶ rel v1.9.0 ─▶ staging/support-assistant │
├ Scorecard (v3 vs production): tasks × delta × CI; online metrics once serving   │
├ Promotion: gates (evals, approvals, obs check) — status per env, [Promote → prod: opens release PR] │
```

- **Version page:** model card (architecture, params total/active, context length,
  dtype/quantization, tokenizer, license, base model, adapters, layers/shards, ModelPack
  layers, signatures/SBOM when referrers land), files, eval scorecard, where it serves
  (env, service, revision, traffic share), serving performance per hardware (TTFT,
  tokens/s/GPU, $/1M tokens from Inference), alias history (who moved it, when, why),
  pull performance (size, pull time per cluster).
- **Promotion gates:** declared (KCL) eval gates + `DeliveryPolicy` approvals + the
  inference `obs check` after deploy. Promote opens the release/PR; it never edits a
  cluster directly.

---

## 6. Inference

### 6.1 Services list

```
│ Service           │ env  │ model / rev       │ runtime   │ rps │ TTFT p95 │ ITL p95 │ e2e p95 │ err% │ SLO  │ KV% │ GPUs  │ util │ $/1M tok │
│ support-assistant │ prod │ v2 · rel v1.8.3   │ vLLM 0.8  │ 42  │ 411ms    │ 38ms    │ 4.1s    │ 0.2  │ 99.1%│ 50  │ 6×L4  │ 37%  │ $0.41    │
```

### 6.2 Service detail

```
┌ ML › Inference › support-assistant (prod) ── rev v1.8.3 · model v2 · obs check ✓ Pass ┐
│ SLO attainment 99.1% · rps 42 · TTFT p95 411ms (▲4%) · ITL p95 38ms · tokens/s 1,834   │
├ Latency (p50/p95/p99, synced): queue · TTFT · ITL · e2e — release/alias/Changes markers│
├ Where time goes (stacked): queue wait │ prefill │ decode │ (network/gateway)          │
├ Traffic: rps by status/finish reason · tokens in/out per s · per-adapter (LoRA) split │
├ Engine: running/waiting reqs · batch size · KV cache % · preemptions · prefix-cache hit│
│         spec-decode acceptance · max-num-seqs saturation                              │
├ Replicas: table (pod, node, GPU, util, KV%, running, TTFT p95, restarts, age) + heatmap│
├ Scaling & cold start: replicas vs demand, scale-up latency, pod-start stages per replica│
├ Cost: GPU-h, $/h, $/1M tokens in/out, idle replicas                                  │
├ Revisions: rev · model · runtime · started · traffic % · verdict · SLI deltas          │
```

- **Answers:** are we meeting SLO; is latency queueing, prefill or decode; is the KV
  cache the limit; did the new model/release regress; what does a token cost; are
  replicas balanced; how fast do we scale.
- **Interactions:** click a latency spike → exemplar traces of that bucket; click a
  replica → its pod/GPU; click a revision → its verdict detail (release health
  findings); compare two revisions (Changes arms or router split) side by side with
  significance on continuous metrics (decision 8 b).
- **Partial:** no tracing → latency from histograms only, banner
  `inference-tracing-off`; KServe without runtime metrics → status + knative metrics
  only.

### 6.3 Request trace

```
app: POST /chat ─────────────────────────────────────────────── 4.3s
 └ chat claude… gen_ai client span (pkg/observe/genai) ───────── 4.2s
   └ gateway / endpoint picker (pool, chosen pod, reason: KV-aware) 3ms
     └ vllm request ─────────────────────────────────────────── 4.1s
        ├ queued ── 27ms
        ├ prefill ─ 310ms  (prompt 2,104 tok, prefix-cache hit 1,536)
        ├ decode ── 3.7s   (512 tok, ITL p50 7ms, preempted 0)
        └ detokenize/stream
 attrs: model, adapter, revision, replica, GPU, finish_reason, tokens, $ cost
```

- Slow-request list by bucket; filter by model/adapter/finish reason/tenant.

---

## 7. Fleet (was Machines)

### 7.1 Fleet overview

```
┌ ML › Fleet ─────────────────────────────── clusters: all ▾  24h ▾ ┐
│ 512 GPUs · 431 alloc (84%) · busy 58% · idle-alloc 74 · unalloc 81 · faulty 3 · throttled 9 │
├ Pools ───────────────────────────────────────────────────────────────────────────────────────┤
│ pool        │ cluster │ product  │ nodes │ GPUs │ alloc │ SM act p50 │ idle-alloc │ health  │ $/day │
│ h100-train  │ gke-uc1 │ H100 80G │ 32    │ 256  │ 95%   │ 61%        │ 6          │ ● 1 bad │ 18.4k │
├ Utilisation heatmap (devices binned by util × time) · Allocated vs busy area chart ─────────┤
├ Health: open device/node issues ranked by health score; Xid/ECC/throttle events over time ───┤
├ Top idle-allocated (namespace, pod, GPUs, hours, $) ─────────────────────────────────────────┤
```

### 7.2 Pool detail

- Node grid (one tile per node, color = health score; inside, one cell per device
  colored by utilisation); percentile bands for SM/tensor/DRAM, memory, power vs limit,
  temps; throttle-reason share; error counts; driver/CUDA/image skew table;
  allocation by namespace/queue over time; cost.

### 7.3 Node detail

```
┌ ML › Fleet › h100-train › gke-…-r4vb ── ● faulty (score 92) · Xid 79 GPU-5 · not cordoned ┐
│ 8×H100 · driver 550.127.05 · CUDA 12.4 · kernel 6.1 · image cos-… · up 14d · incarnation 3 │
├ Devices (8 rows, sparkline per metric): util │ SM │ tensor │ mem │ power/limit │ temp/mem temp │ clk events │ ECC │ Xid │
├ Workloads on this node over time (Gantt by pod, colored by run/service) ────────────────────┤
├ Interconnect: NVLink topology (GPU×GPU matrix with link state + bandwidth + CRC/replay), PCIe tree, IB HCAs (port state, xmit/rcv, errors, link downed) │
├ Node health: conditions (Ready, Mem/Disk/PID pressure, NPD: KernelDeadlock, ReadonlyFilesystem, GPU*), NPD events, kubelet/containerd restarts │
├ Host: CPU, memory, PSI, disk (incl. image fs), network, /dev/shm users                    │
├ History: health events across incarnations; DCGM diag results; RMA notes                  │
│ [Cordon (command / KCL)] [Run DCGM diag (if allowed)] [Open in Recommendations]            │
```

### 7.4 GPU (device) detail

- Identity (UUID, serial, PCI bus id, VBIOS, product, MIG mode/profiles), every DCGM
  field over time in grouped panels (utilisation & profiling pipes, memory & BAR1,
  power & energy & violations, thermals, clocks & clock-event reasons decoded, ECC
  volatile/aggregate, retired pages, row remap (correctable/uncorrectable/pending/failed),
  Xid history with meanings, PCIe link gen/width + replay, NVLink per-link), workloads
  that held it, health score with its inputs, and the device's history if it moved nodes.

### 7.5 Topology view

- Cluster → rack/zone → node → NVSwitch/NVLink domain → GPU, with IB fabric where
  visible; colored by health or utilisation. Used to see whether a job's ranks were
  placed topology-aware (Kueue TAS) and where a bad link sits.

---

## 8. Queues (scheduling)

```
┌ ML › Queues ─────────────────────────────────────────────────── 24h ▾ ┐
│ Pending 14 workloads · 112 GPUs demanded · free 23 · admission wait p95 1h12m · preemptions 9 │
├ ClusterQueues ─────────────────────────────────────────────────────────────────────┤
│ queue     │ cohort │ flavor  │ nominal │ used │ borrowed │ lendable │ pending │ wait p50/p95 │ fair share │
│ research  │ ml     │ h100    │ 64      │ 72   │ +8       │ 0        │ 6       │ 18m / 1h12m  │ 1.12       │
├ Demand vs capacity over time (stacked: admitted, pending by reason; capacity line) ─┤
├ Pending workloads: name │ queue │ priority │ GPUs │ waiting │ reason (classified) │ position │ run │
├ Preemptions & evictions: who preempted whom, reason, GPU-hours lost              │
├ Pod start (all accelerator pods): stage breakdown p50/p95 → §8.1                 │
```

- **Answers:** why hasn't my job started; who holds the quota; is borrowing/preemption
  fair; how much accelerator time is lost to waiting and preemption.
- **Interactions:** pending row → its run/phase and the full wait timeline; queue →
  queue detail (usage over time per flavor, admitted workloads, history); reason chip →
  all workloads waiting for that reason.

### 8.1 Pod start drill-down

```
│ stage            │ p50   │ p95    │ share │ top cause                         │
│ queued (Kueue)   │ 4m    │ 41m    │ 48%   │ insufficient quota in flavor h100 │
│ scheduling       │ 4s    │ 41s    │ 1%    │ untolerated taint (2 pods)        │
│ node provisioning│ 3m10s │ 9m     │ 22%   │ capacity error us-central1-a      │
│ sandbox/init     │ 12s   │ 1m30s  │ 4%    │ storage-initializer (weights)     │
│ image pull       │ 1m36s │ 3m30s  │ 18%   │ 9.4 GB image, cross-region        │
│ model load/ready │ 58s   │ 1m20s  │ 7%    │ 16 GB from registry               │
```

- Waterfall per pod attempt (Kueue → scheduled → node ready → sandbox → init → pulled
  → started → ready), with kubelet spans where kubelet tracing is on. Group by workload,
  pool, image; each "top cause" links to its recommendation.

---

## 9. Recommendations (redesigned)

The demo rendered every finding as an expanded card with a code block — long, flat,
unprioritised. The redesign is an **inbox**.

```
┌ ML › Recommendations ── 23 open · impact $4.1k/day · 410 GPU-h/day ── [Group: category ▾] [Sort: impact ▾] ┐
│ Filters: severity ▾  category ▾  cluster ▾  owner ▾  status: open ▾   [Bulk: dismiss · assign · draft PRs] │
├────┬──────────────────────────────────────────┬────────────┬───────────┬──────────┬─────────┬─────────────┤
│ sev│ finding                                  │ impact/day │ subjects  │ effort   │ age     │ action      │
├────┼──────────────────────────────────────────┼────────────┼───────────┼──────────┼─────────┼─────────────┤
│ ●  │ Faulty GPU taking work: r4vb GPU-5 Xid 79│ 1 run at risk│ 1 node   │ command  │ 2h      │ [Fix ▸]     │
│ ●  │ 74 GPUs allocated and idle               │ $1.9k · 178h│ 11 pods  │ KCL/PR   │ 3d      │ [Fix ▸]     │
│ ◐  │ Replica cold start 5m40s (image pull 62%)│ p95 +5m    │ 1 svc     │ KCL/PR   │ 1d      │ [Fix ▸]     │
│ ○  │ DCGM profiling counters off              │ blind spot │ 2 clusters│ command  │ 7d      │ [Fix ▸]     │
├ Categories (counts, impact): Health · Waste · Scheduling · Startup · Training efficiency · Inference · Coverage (telemetry gaps) · Reliability ┤
```

Selecting a row opens a **side panel** (not an inline wall):

```
┌ 74 GPUs allocated and idle ───────────────────────────────── ● open · seen 3d ┐
│ Why it matters: $1.9k/day, 178 GPU-h/day; 6 pending jobs wait for H100s.      │
│ Evidence: [chart: allocated vs busy for these pods, 3d] [table: pod, ns, GPUs, │
│   idle hours, owner, last activity]                                           │
│ Rule: util < 5% for ≥ 1h on allocated devices (DCGM_FI_PROF_GR_ENGINE_ACTIVE)  │
│ Fix (adopt first): Kueue idle timeout / notebook culling — [View KCL diff]     │
│   [Open PR]  [Copy command]  [Assign]  [Dismiss with reason & expiry]          │
│ After fix: verified when the rule clears for 24h.                              │
└────────────────────────────────────────────────────────────────────────────────┘
```

- **Impact** is the sort key, in one of four comparable units: $/day, GPU-h/day, run
  time at risk, user-facing latency; severity breaks ties. Each finding states the unit.
- **Triage states:** open → assigned → fix proposed (PR link) → fixed (verified by the
  rule clearing) | dismissed (`RecommendationDecision`, with reason + expiry) |
  snoozed.
- **Grouping:** one finding per rule per scope, with N subjects inside it (not 74
  cards). Categories: Health, Waste, Scheduling, Startup, Training efficiency,
  Inference, Coverage (missing telemetry), Reliability (checkpointing, spot handling).
- **Fix types:** KCL diff → PR; a command (for something outside our declarations,
  e.g. a cloud node-pool flag) with copy; "explain" (opens agent chat).
- **Coverage findings** (dcgm-profiling-off, inference-tracing-off, …) are collapsed into
  one "Telemetry coverage" score with a checklist, so they do not drown incidents.

---

## 10. Setup (was Stack)

- Per cluster: a **coverage checklist** rather than a code dump: Accelerator metrics ✓
  (DCGM 3.3.9 adopted) · Profiling counters ✕ · Pod attribution ✓ · Node health ✓ (NPD
  installed by forge) · Events ✓ · Queueing ✓ (Kueue adopted) · Serving metrics ◐ ·
  Training instrumentation ✕ · Tracing ✕ · Profiling ✕ · Kubelet metrics ✓ — each row
  with state (adopted / installed by forge / missing / not applicable), version, and
  **View KCL** (collapsed) / **Fix** (opens the Recommendation).
- Inventory: nodes, products, drivers, CRDs and versions found; last discovery time;
  "Re-run discovery" (CLI command shown).

---

## 11. Cross-view interactions (catalogue)

| Interaction                                 | Where                  | Behaviour                                                                                        |
| ------------------------------------------- | ---------------------- | ------------------------------------------------------------------------------------------------ |
| Synced crosshair + brush zoom               | every page with charts | one time cursor and range per page                                                               |
| Event overlays toggle                       | every chart            | restarts, preemptions, checkpoints, Xid/ECC, NPD, releases, alias moves, Changes ramps, findings |
| x-axis step/tokens/time/relative            | Run metrics, compare   | synced across panels; attempts overlay on step                                                   |
| Smoothing, log, outlier clip, min/max bands | Run metrics, compare   | per workspace, saved                                                                             |
| Compare tray                                | all lists              | runs, attempts, ranks, nodes, devices, versions, revisions, eval runs                            |
| Click-through ids                           | everywhere             | every id is a link; breadcrumb carries the path                                                  |
| Exemplars                                   | latency charts         | bucket → traces                                                                                  |
| Capture profile                             | Run system/profiles    | declares a window on the live run                                                                |
| Draft the fix / Ask about this              | findings, every page   | opens agent with entity + range                                                                  |
| Deep links                                  | everywhere             | URL encodes scope, range, tabs, filters, compare set                                             |
| Saved views and reports                     | lists, run workspaces  | per user; shareable read-only report snapshot                                                    |
| Alerts from any chart                       | charts                 | "alert on this" → forge alert spec (declared)                                                    |

## 12. Empty, partial and gated states (catalogue)

| State                                                                 | Shown as                                                                             |
| --------------------------------------------------------------------- | ------------------------------------------------------------------------------------ |
| Org not entitled                                                      | the request-access page (settled)                                                    |
| No cluster connected                                                  | connect flow + demo link                                                             |
| Cluster connected, stale (> 2 intervals)                              | banner "data from 14m ago", entities `unknown`                                       |
| Source missing (exporter, counters, events, tracing, instrumentation) | "not collected" placeholder naming the source + finding link                         |
| MIG / time-slicing                                                    | per-instance panels / "shared device: per-pod not meaningful"                        |
| Imported run (MLflow/W&B)                                             | logged keys only; system tab from cluster when the pod can be matched; "open in W&B" |
| Run older than retention (30d metrics, 24h raw profiles)              | summaries kept on the entity; charts show the retention edge                         |
| Demo shell                                                            | everything filled from fixtures; writes refused with a tooltip                       |

## 13. Owner decisions raised by this file

| #   | Decision                                                                                       | Options                                                                                                            | Recommendation                                                                                          |
| --- | ---------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------- |
| V1  | ML sub-pages (settled at six)                                                                  | (a) keep six: Evals inside Models, Queues a tab of Machines, Overview absent; (b) nine as in §1                    | (b): evals and scheduling are where the owner said we are weakest; an Overview is the daily entry point |
| V2  | Rename Machines → Fleet, Stack → Setup                                                         | (a) keep; (b) rename                                                                                               | (b); "Stack" reads as a code page, which the demo became                                                |
| V3  | Do we build a chart workspace at W&B depth for run metrics, or import-and-link (decision 4 b)? | (a) full workspace; (b) solid defaults + link out; (c) hybrid: defaults, smoothing, overlays, compare — no Reports | (c): the owner's bar implies depth; Reports can wait                                                    |
| V4  | Do we host eval samples (inputs/outputs) — sensitive data?                                     | (a) host, per-org storage; (b) link to customer storage only                                                       | (a) with per-org bucket override                                                                        |
