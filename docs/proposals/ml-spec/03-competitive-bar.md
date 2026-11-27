# 03 — Competitive bar

**Status:** product spec draft, 2026-11-26. A synthesis of two research parts, which
hold the per-product, per-view detail:

- `parts/03a-competitive-trackers.md`: W&B (Models + Weave), MLflow 3, Neptune (sunset),
  Comet/Opik, ClearML, Determined, TensorBoard, Arize, LangSmith, Braintrust; MLflow REST
  field names for import (§18).
- `parts/03b-competitive-platforms.md`: Run:ai, Anyscale/Ray, Modal, SageMaker
  (HyperPod + classic), Vertex/GKE, Azure ML, Datadog, Grafana, CoreWeave, Lambda/
  Together/Nebius, NVIDIA BCM/DGX Cloud/Mission Control, SkyPilot/dstack, Kubeflow,
  Phoenix/LangSmith/Helicone/Langfuse.

**Confidence.** Most vendor documentation sites render with JavaScript and returned empty
pages to the researchers. Verified (V) this session: the MLflow 3.18 REST reference and
SageMaker HyperPod's observability dashboards, health agent and default alert rules.
Everything else is (K), from knowledge of the products as of 2025. **Re-check any panel
or field name against the live product before it appears in user-facing copy.**

Market facts already established in ml-workloads.md: Neptune was bought by OpenAI and
its SaaS closed on 2026-03-05; W&B is owned by CoreWeave and Run:ai by NVIDIA, both of
which sell GPUs. That ownership is our neutrality opening.

---

## 1. Who sets the bar, per view

| Our view (02-views)         | Bar-setter(s)                                             | What the bar is, concretely                                                                                                                                                                                                                                                                                                                                             |
| --------------------------- | --------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Runs list                   | W&B, MLflow, ClearML, Determined                          | Filter on any config/summary/tag with expressions; group by a config key with aggregate rows; sortable, pinnable columns; saved views; bulk tag/archive; distinct states for crashed, failed, killed and preempted; Determined's progress % and best searcher metric per experiment                                                                                     |
| Experiment page             | W&B, Determined                                           | Leaderboard; parallel coordinates with brushing; param-importance; sweep status with early-stopping state                                                                                                                                                                                                                                                               |
| Run overview                | W&B, ClearML                                              | git commit + uncommitted diff, command/args, image, packages, host and GPU type/count, searchable config tree, summary (last/min/max), inputs/outputs (datasets, checkpoints, model), notes, parent/child                                                                                                                                                               |
| Run metrics (charts)        | W&B, Neptune                                              | x-axis step / wall / relative / any metric; EMA, Gaussian and running-average smoothing with the original shown; log axes; outlier-ignoring scale; group-by with mean/median and min–max/stddev shading; regex metric pickers; prefix sections; synced zoom; **Neptune: full-fidelity min/max envelopes at 1M+ points and forked/resumed runs stitched into one curve** |
| Run compare                 | W&B, Comet, MLflow                                        | Diff-only config table; overlays synced by step; parallel coordinates; scatter; param importance                                                                                                                                                                                                                                                                        |
| Run system/hardware         | W&B, HyperPod, CoreWeave                                  | Per-GPU util, memory %, memory bytes, temperature, power W and %, clocks, ECC, plus CPU/RAM/disk/net per process and node at ≤15 s (W&B); per-rank views for training jobs in Grafana (CoreWeave)                                                                                                                                                                       |
| Run logs                    | Determined, Ray                                           | Filter by rank/pod/container and level, search, follow, download                                                                                                                                                                                                                                                                                                        |
| Run checkpoints / artifacts | W&B Artifacts, Determined                                 | Versioned, digest, aliases, size, metric-at-checkpoint, GC policy, "used by", lineage DAG                                                                                                                                                                                                                                                                               |
| Run profiles                | SageMaker Debugger/Profiler, Determined profiler tab, Ray | Step-time breakdown, kernel table, trace viewer, memory timeline; Ray's actor stack/flamegraph                                                                                                                                                                                                                                                                          |
| Workload / job detail       | Run:ai, HyperPod                                          | Per-pod/rank table, restarts (automatic vs manual), events, logs, metrics; HyperPod auto-resume from checkpoint                                                                                                                                                                                                                                                         |
| Evals                       | Braintrust, LangSmith, W&B Weave, MLflow genai            | Dataset × model × scorer matrix; per-row drill into the trace; diff vs a base with regression counts; human annotation queues; online scorers on sampled traffic                                                                                                                                                                                                        |
| Models / registry           | W&B Registry, MLflow 3                                    | Unique, protected aliases; model card; lineage back to run and dataset; automations and webhooks on alias/version events; RBAC; MLflow 3 `LoggedModel` linking checkpoints ↔ runs ↔ eval metrics ↔ traces                                                                                                                                                            |
| Inference service           | SageMaker endpoints, Modal, Datadog, GKE dashboards       | p50/p95/p99, TTFT, TPOT, tokens/s, KV-cache %, queue depth, replicas, cold start (SageMaker `ModelSetupTime`, Modal container start), traffic split                                                                                                                                                                                                                     |
| Request traces              | Langfuse, LangSmith, Phoenix, Helicone                    | Span tree, tokens, cost, latency, TTFT, sessions/threads, add-to-dataset, OTLP ingest                                                                                                                                                                                                                                                                                   |
| Fleet                       | Run:ai, Datadog GPU Monitoring, HyperPod                  | Allocated vs used vs idle GPUs; healthy-node %; filter by cluster/pool/team; idle cost                                                                                                                                                                                                                                                                                  |
| Node / GPU detail           | HyperPod, Grafana DCGM dashboard (12239), Datadog         | Per-GPU DCGM core and profiling (SM active, tensor active, NVLink/PCIe), MIG instance metrics, conditions, workloads on the node, fault history (HyperPod keeps the last 20)                                                                                                                                                                                            |
| Health / bad node           | HyperPod, CoreWeave Mission Control                       | Passive and deep checks (DCGM diag, NCCL tests); labels, taints, auto-reboot/replace; burn-in; job auto-resume                                                                                                                                                                                                                                                          |
| Queues / quotas             | Run:ai, HyperPod task governance (Kueue)                  | Project/department hierarchy, deserved vs over-quota, pending reasons, fair share                                                                                                                                                                                                                                                                                       |
| Cost                        | Run:ai, Datadog, Anyscale, Modal, LLM tools               | GPU-hours per team; $ per job; idle $; $ per token                                                                                                                                                                                                                                                                                                                      |
| Alerts                      | HyperPod default rules                                    | The floor: temperature, ECC DBE, power, NVLink, thermal, Xid, MIG, disk                                                                                                                                                                                                                                                                                                 |
| Recommendations             | SageMaker Debugger rules, Run:ai idle reclaim             | LowGPUUtilization, IO/CPU bottleneck, StepOutlier; automatic idle reclaim                                                                                                                                                                                                                                                                                               |

## 2. Where they are weak (consistently)

1. **Training and hardware live in different products.** Trackers poll NVML only (no
   DCGM profiling, Xid, NVLink/PCIe, clock-event reasons, NPD conditions) and have no node
   identity across runs ("is this node bad for everyone?"). Fleet tools have no loss,
   step or phase.
2. **Kubernetes is invisible in a run.** No scheduling, image pull, OOMKilled, eviction,
   Kueue admission/wait or JobSet restarts on the run's timeline.
3. **Restarts are a gap, not an object.** W&B shows a resumed run as a gap; only
   Determined counts trial restarts. Nobody attributes lost steps, cause and GPU-hours
   per attempt.
4. **Cost of failure is absent.** Idle cost appears (Datadog, Run:ai); badput (restarts,
   startup, checkpoint stalls) rarely does.
5. **Lineage ends at the alias.** Registries stop at "alias = production": no image
   digest, deploy, canary health, production traffic or roll-forward history.
6. **Traces stop at the LLM span.** No join to the serving engine (queue, prefill,
   decode, KV cache), the pod and GPU behind it, or service code and continuous profiles.
7. **Advice is text.** Debugger rules and idle reports say what is wrong; none turns the
   advice into a reviewed change.
8. **Vendor lock.** Run:ai requires its scheduler and is NVIDIA's; CoreWeave, HyperPod
   and Modal own the hardware; W&B is CoreWeave's.
9. **Scale.** MLflow's UI struggles at scale; Neptune's exit leaves 100k-step,
   thousands-of-series users without a home.

## 3. Where Reliant must match (non-negotiable for the design partner)

- W&B-grade chart interactions on run metrics (smoothing, axes, grouping, shading,
  synced zoom, stitched attempts), at Neptune-grade fidelity for long runs
  (server-side min/max downsampling, never point-dropping).
- Diff-only config comparison and parallel coordinates.
- Per-rank logs with filters (Determined).
- A registry with protected aliases, lineage and automations (we get automations from
  releases and Changes).
- Eval matrices with per-sample drill-down and diff vs a base (Braintrust/LangSmith).
- Inference p50/p95/p99, TTFT, TPOT, KV cache and cold start per endpoint.
- Allocated vs used vs idle, healthy-node %, idle $ (Run:ai, Datadog, HyperPod).
- HyperPod's default alert set as the alert floor.
- MLflow and W&B import, so teams keep their SDK (decision 4 b).

## 4. Where Reliant's integrated angle wins

| Angle                   | What only we can show                                                                                                                                              | Built on                                                       |
| ----------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------ | -------------------------------------------------------------- |
| One run page            | Phases and attempts with cause (Xid 79 on node X, Kueue preemption, OOMKilled), metrics, k8s events, per-rank hardware, logs, traces and profiles on one time axis | 00 object model; cluster collector; DCGM adoption              |
| Release ledger          | checkpoint → model digest → release → env → `forge obs check` verdict on TTFT/ITL/queue/tokens per GPU; roll forward by promoting the previous digest              | `pkg/release`, `pkg/releasehealth`                             |
| Changes A/B             | Two model versions on live traffic, with online eval, latency and cost per arm                                                                                     | Changes + `experimentstats` (continuous metrics: decision 8 b) |
| Code-joined traces      | app span → gateway → vLLM queue/prefill/decode → replica → GPU; training step → data loader → continuous profile; regressions tied to the commit                   | `pkg/observe/genai`, runtime-context                           |
| Cost from cluster truth | GPU-seconds by run/phase/attempt/team, including waste categories (failed attempts, idle allocation, startup, checkpoint)                                          | 01b cost model; customer price sheet                           |
| Fixes, not advice       | A finding becomes a KCL diff and a PR drafted by an agent, verified when the rule clears                                                                           | `pkg/mlrecs`, `RecommendationDecision`, agents                 |
| Neutral and adopt-first | Works on the customer's existing GPU Operator, Kueue, KServe and vLLM on any cloud; installs only what is missing                                                  | discover → adopt → install                                     |

## 5. Implications for the spec

- 02-views adopts the bar rows above as acceptance criteria per view.
- The attempt is our sharpest differentiator (only Determined has a weak version): it
  must be visible in the runs list (mini-Gantt), the run timeline, metrics (stitched with
  redone steps dashed), cost (restart-lost GPU-hours) and findings.
- We should not build W&B Reports, sweep search logic, or an annotation tool in v1:
  link or import instead (decision V3 in 02-views).
