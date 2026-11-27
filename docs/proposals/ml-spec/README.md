# ML product spec (2026-11-26)

Durable spec for the ML redesign, written after the owner's review of the demo
("you're about 10% of the way there"). Owner input overrides anything here.

| File                      | What                                                                                                                                                                                                                    |
| ------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `00-object-model.md`      | Entities, hierarchy (project → experiment → run → phase → attempt → pod/rank → step; checkpoints, lineage), lifecycle states, attempt end reasons, mapping to k8s / forge / MLflow / W&B, storage, open questions O1–O6 |
| `01-metrics-inventory.md` | Index + neutral field set + cross-cutting rules; detail in `parts/01a`–`01d` (~700 rows)                                                                                                                                |
| `02-views.md`             | Every page and tab with wireframes, questions, metrics, interactions, empty states; decisions V1–V4                                                                                                                     |
| `03-competitive-bar.md`   | Synthesis; detail in `parts/03a` (trackers/registries/evals) and `parts/03b` (platforms/fleet/inference)                                                                                                                |
| `04-gap-vs-demo.md`       | Gap per view vs reliant#3693 / control-plane#3747, phased plan (A fixtures-first … G breadth), decisions G1–G4                                                                                                          |

Sources: forge `docs/proposals/ml-workloads.md` (main) and the observability addendum
(forge#2884, open). Confidence markers (V)/(K) are per fact in the part files.

Related proposals:

- [`docs/proposals/ml-workloads.md`](../ml-workloads.md): ML training and inference in
  your own clusters.
- [ML observability: where time and money go](../ml-workloads.md#ml-observability-where-time-and-money-go),
  the observability section of `ml-workloads.md` (forge#2884; open when this spec was
  written, merged since).
- [`docs/proposals/reliant-for-ml.md`](../reliant-for-ml.md): Reliant for ML, releases,
  testing, previews, observability and feedback loops for training and inference
  (forge#2888, open at the time of writing).
- [`docs/proposals/ml-mvp.md`](../ml-mvp.md): the ML MVP, managed KServe, Kueue and
  KubeRay, model releases and training runs, which re-orders the build.

## Owner input log

- 2026-11-26 01:44Z: hierarchy CONFIRMED ("that's about right"): project → experiment → run → phase → attempt (wave) → pod/rank → step, checkpoints linking phases and producing model versions.
- 2026-11-26: owner will brain-dump in parallel; their input overrides research. Append it here verbatim as it arrives.
- Default decisions taken until the owner overrides (owner said "go for it all" / "both in parallel"): V1 nine sub-pages (Overview, Fleet, Setup, Queues, Recommendations, Runs, Evals, Models, Inference); V-renames Machines→Fleet, Stack→Setup; chart depth = strong defaults + compare, link out for custom reports; G1 reshape early-access proto now; G2 full demo on fixtures first; G3 B→C→D→E→F; G4 keep gate/access from reliant#3693 + cp#3747, redo pages and proto.
- 2026-11-26 01:51Z owner brain-dump (VERBATIM, overrides research):
  "the idea for the ML work is to mirror reliant, but for ML. ... we provide all the best practices around testing, releasing, security, observability, and more, so that you can just focus on business logic. and this type of peripherals make it easier to build the business logic by creating feedback loops and improving reliability. I'd like to do the same with ML. ie: we use flux for releases, should we do something like Kueue, or Ray for ML workloads? We tie releases into alerts to auto rollback, can we detect bad training, and improve things? We have scaffolding built into code, should we do that for pytorch, or other languages/frameworks? Everything in reliant is opt-in, so we'd need the same for ML workloads. We have ephemeral environments, security, testing.
  Some things we want for ML: 1. all the hardware stuff. more on DCGM, node exporter, node problem detector. 2. training runs with training id's to tie metrics to specific runs, training ranks, world size, mesh, dp, tp, pp, ep, cp, Kueue attributes and workloads; training and inference traces built deep into the workloads, and tied to dcgm stats, kubelet stats, scheduling and kueue (likely through trace spans); training step stats, phase stats, token stats, training loss, mfu, grad norm, learning rate; Memory/cpu/gpu alloc/util; Tflops, lm_loss, grad_norm, timers, sampling, queries, **goodput**, model vitals, model insights. Can we create the platform that allows and enables (because we can combine with reliant agentic workflows itself), self improving reinforcement learnings. Scheduling, histograms, continuous profiling, more, auto alerts, etc. Maybe some of this uses existing nav tabs, maybe not. thoughts?"
  Note: Reliant does NOT auto-rollback today (north-star N1 = roll forward; failed rollouts keep the previous release serving; a regressed release halts promotion). Training auto-remediation (rewind to last good checkpoint, cordon bad node + new attempt) is a different, safer case.
  Existing console nav (reliant frontends/web/src/components/Layout/sidebar/navConfig): Overview, Deployments, Previews, Observability (Dashboards, Analytics, Speed insights), Tests, Security, Inbox, Agents, Workflows, Runs (= workflow runs — name collision with ML runs), Sandboxes, Connectors, Library, Auth, Configure, Usage & billing.
- 2026-11-26 02:20Z owner decisions:
  - Nav: owner asked "will these be new flavors/mechanisms under the hood, or reuse existing primitives like OCI, Flux?" Answer given: reuse (OCI registry + digests for weights/checkpoints/eval sets, Flux delivery, release ledger + obs check, OTel/ClickHouse obs pipeline, Changes, alerting/Inbox, workflows, previews). Genuinely new: a training-run ledger (run/phase/attempt records, like releases), the Python SDK, Kueue/JobSet as adopted upstream (not ours). So HYBRID is the working default unless the owner objects.
  - Python: instrumentation SDK first, PyTorch supported (HF Trainer, Lightning, torchtitan, Megatron, DeepSpeed callbacks) + `ml/instrument` skill; `forge scaffold training` later. SETTLED.
  - Bad training: opt-in auto-remediation declared in KCL. SETTLED. Plus, owner emphasis: at large scale (thousands of GPUs, all collaborating), fast checkpoint / replace / resume is the priority — downtime costs millions. Design for minimal time-to-resume: fast failure detection, hot spares, fast checkpoint, node replacement without full restart, restart-time breakdown, goodput.
  - Self-improving RL: build the FEEDBACK-LOOP HOOKS first (events, APIs, CLI, triggers). Users plug in the forge CLI however they like; Reliant workflows are one harness on top. SETTLED.
- 2026-11-26 06:01Z owner: "agreed with your recs". Every open owner decision in the two proposals below is SETTLED on its recommended option. The hybrid nav is no longer just the working default: it is decided.
  - `reliant-for-ml.md` (forge#2888), decisions 23–32:
    - 23: hybrid nav. An ML area holds **Training** (not "Runs"), Evals, Models, Fleet and setup. Releases go in Deployments, smoke runs in Previews, GPU telemetry in Observability, alerts in Inbox and eval gates in Tests.
    - 24: cross-node remediation uses upstream first: JobSet, TorchElastic, NVSentinel and cloud auto-repair. A small forge-rendered executor in the customer's cluster does only what ties a node action to a run (cordon, swap a spare, resume, record). The control plane does not do it.
    - 25: the training-run ledger is in control-plane Postgres per org. Step scalars go in a new step-keyed ClickHouse table.
    - 26: the Python SDK is `sdk/python/` in forge, Apache-2.0, on PyPI. It stays 0.x until a design partner confirms the API, then becomes 1.0.
    - 27: typed `forge` integration triggers with payload schemas.
    - 28: launches are audited, bounded by template params and budgets, and need an approval on production queues.
    - 29: hot spares are Kueue placeholder workloads, or tainted standby nodes without Kueue.
    - 30: smoke training runs on CPU by default. A GPU pool is used only when the team declares one.
    - 31: pickle weights warn first, then a lint gate, plus an opt-in `DeliveryPolicy` promotion block.
    - 32: the remediation default is `recommend`. `act` must be set explicitly.
    - Also settled, in `ml-workloads.md`: decision 18 is the published SDK; Settled item 8 is amended to the hybrid nav; and automatic cordon/drain stays out by default but is possible when declared.
  - `llm-agent-observability.md` (forge#2931) §15, decisions 1–14:
    - 1: normalise at ingest, additively, from one forge table compiled to OTTL.
    - 2: Langfuse OTLP path alias with `langfuse.*` attributes, a migration guide and dual-run. No REST emulation.
    - 3: content capture is off by default, per env and gateway-enforced. It has its own table and retention, tool I/O is a second switch, and the org has a kill switch.
    - 4: a new `obs:content:read` scope. Every read is audited, and agents read only with a person's grant.
    - 5: LLM judges run in a control-plane worker and code evaluators in customer code. A Reliant workflow comes later.
    - 6: evaluators are declared in the repo. Their live state is in the control plane.
    - 7: prompt files ship in the release and a Change picks among them. Console edits open a PR.
    - 8: LiteLLM's price map is vendored, read through a query-time dictionary, with overrides shared with the ML price sheet.
    - 9: quality gates warn first. Offline eval gates are displayed first and block per env on opt-in.
    - 10: one evals model with ML: the spec's model, with its subject widened to a release and a Change variant.
    - 11: one Python package shared with the ML SDK (a `genai` module), plus a TS subpath export.
    - 12: open instrumentation and data, a hosted loop, BYOC later.
    - 13: AI data is billed on the flat GiB meter. Judge calls are billed at gateway passthrough.
    - 14: autopilot stopping a prompt arm is opt-in per change.
- 2026-11-26 15:45Z owner (VERBATIM, overrides research): "I'd start with KServe + vLLM for serving, Add Kueue when teams or batch jobs compete for GPUs. Add KubeRay when you want to write distributed Python applications or independently scale stages of an inference pipeline. Kueue and Ray can coexist because they make different decisions. Then these things should be similar to how we host standard parts of a user's app (hosted zitadel, hosted otel, hosted CNPG, soon to be doing hosted ValKey, and CronJobs) -- similar we can do hosted Kueue, Ray, KServe. Other notes from chatgpt that you can take/choose as you see fit (in the pasted-text's). as always let's start simpler, but with a great product that we can expand upon. ie: maybe we skip datasets if that's overly complicated?"
  - Answered by [`docs/proposals/ml-mvp.md`](../ml-mvp.md): managed KServe, Kueue and KubeRay; model releases; training runs; the golden-path demo as the acceptance test. Its decisions 45–57 were open when written; they are settled by the owner input below.
  - TAKEN from the shared notes:
    - Two first-class objects: a **training run** ("what produced these weights?": trainer image, base model, dataset ref, config, attempts, checkpoints, artifacts; identity survives a move to another cluster) and a **model release** ("what is serving this request?": weights, tokenizer/preprocessing, serving image, input/output contract; staging and prod promote the same release; retraining is a new candidate even at the same commit).
    - The model release rides the existing release ledger: an immutable release, approval bound to a digest, `DeliveryPolicy.required_gates`, the promotion ledger, Flux to the cluster. The Flux bundle stays small: serving resources carry immutable references to weights (OCI modelcar, or object storage with a verified digest). Checkpoints go to object storage; a release is cut when a checkpoint becomes a candidate.
    - The release verdict for models is the flagship: candidate against current prod on the same versioned eval set, including important segments (aggregate accuracy flat but recall on a key segment 91% → 78% holds the promotion, shows examples and the baseline, and hands an agent the run, code and data refs).
    - Serving with KServe InferenceService and the vLLM runtime first; then Kueue (with Kubeflow Trainer TrainJob) when GPUs are contended; then KubeRay for distributed Python and independently scaled inference stages. Kueue and Ray coexist.
    - These are managed add-ons, as forge already manages Zitadel, the OTel pipeline, CNPG, (soon) Valkey and CronJobs: lifecycle, upgrades, telemetry and adopt-if-present.
    - The PR fast path: a tiny model and tiny data, a few steps, save and restore a checkpoint, export, boot the real inference container, call it through the app's API; normal coverage for training and serving code.
    - Attempt identity and write fencing: a stale (partitioned) attempt cannot publish over its successor; a checkpoint is published only when all shards are durable.
    - Request traces carry the model release digest (prediction → rollout → eval → run).
    - Multi-cluster: TrainJob and Kueue per worker cluster, MultiKueue in the management cluster, our open-source placement controller as MultiKueue's external dispatcher; scoring on expected completion time plus total cost; explained decisions; predicted against actual; a serving capacity floor; one synchronous run stays in one cluster and fabric. Taken into forge#2994.
    - The golden-path demo as the MVP milestone: change a training recipe in a worktree → a small e2e → dispatch the full run to one of two clusters → recover from an injected interruption → publish an immutable candidate → staging evaluates against prod → a pass promotes through Flux, a fail leaves examples, traces and evidence.
  - SKIPPED for v1 (the owner: "maybe we skip datasets"):
    - A dataset catalog, labeling (Label Studio), validation (Great Expectations), Ray Data prep, Feast, Fluid caching, lakeFS and Iceberg integrations, split management. v1 records only a dataset reference, a digest or snapshot id, and a region on the run, for lineage and placement locality.
    - Sweeps (Katib, Ray Tune) and Kubeflow Pipelines, for later.
    - Embedding model and index promoted as a compatible pair through rollback floors: noted for later.
- 2026-11-26 owner: "take your recommendations on all". Every owner decision still open in the merged forge proposals is SETTLED on its recommended option. Each proposal records it in a dated "Settled (owner, 2026-11-26)" list, and its decision table is now a record that keeps the options weighed.
  - `ml-workloads.md` §11 and decisions 17–22 (18 was already settled): 1 public endpoint restricted to hub egress now, pull-mode delivery as the durable path; 2 local `forge ml discover` plus an in-cluster read-only collector; 3 accelerator adapters ship as versioned data; 4 own the run record and lineage, MLflow-compatible ingest, link out to W&B; 5 declared templates with recorded launches, observe-only always on; 6 meter pull egress, then a free regional allowance with cross-cloud billed; 7 model digest in the app release (`CarriedFrom`), per-env pin allowed; 8 in-app Changes variants now, router splits later as an ML exception; 9 nav label "ML"; 10 disconnect removes `forge-ml` components only; 11 ingest plus a free accelerator tier through beta; 12 a design partner's cluster gates wider access (the Spot smoke pool first recorded here was overridden at 16:42Z, below); 13 a separate `models/` allowance per plan; 14 tag fallback now with `models/` protected, referrers when distribution ships them; 15 a per-(cluster, env) read-only pull token; 16 entitlement in a DB table with admin RPCs and audit; 17 30s raw for 30 days, 13-month rollup on demand; 19 profiler captures in the customer's checkpoint store; 20 pod-start timeline derived at query time; 21 customer-declared accelerator prices plus OpenCost; 22 eval gates display-only first.
  - `mcp-from-rpcs.md` §9, decisions 1–9; `static-site-edge.md` §8, decisions 1–9; `changes.md` §8, decisions 4, 5, 7 and 8; `changes-from-sources.md` §7, decisions 1–4. `autoscaling.md`, `status-page.md`, `reliant-for-ml.md` and `llm-agent-observability.md` had no open rows.
- 2026-11-26 16:42Z owner: `ml-workloads.md` decision 12 is **no GPU spend**, overriding the recommendation taken above. No standing Spot GPU pool, and no GPU we pay for. Testing uses fake GPUs (fake-gpu-operator), KWOK and CPU workloads; the first early-access design partner's cluster is the real-hardware proof before access widens.
- 2026-11-26 owner (VERBATIM): "2997 accept. 2994 hold. Either we need to push this to a separate repo or we need to instead create or allow or hook into primitives that work with whatever the customer is already using. Or we just hold on multicluster for foreseeable future. On 2997, like with everything else. We are piecemeal opt-in. Nothing is all or nothing users can opt into certain features without taking others."
  - `ml-mvp.md` (forge#2997): decisions 45–57 are SETTLED on their recommended options, recorded in its §7 "Settled (owner, 2026-11-26)" list; the decision table is now a record.
  - **Piecemeal opt-in** is a settled principle over every ML proposal: each capability (managed KServe, Kueue, Kubeflow Trainer and KubeRay, the ML collector and telemetry, `forge ml discover`, `forge.ModelService`, model releases, eval gates, training-run records, the Python SDK, recommendations and remediation) is independently adoptable, and only a named hard technical dependency couples two. `forge.MLStack` is only a grouping: one component alone is a valid stack. `ml-mvp.md` carries the principle and its opt-in matrix.
  - **Multi-cluster is ON HOLD** (forge#2994 keeps its `hold` label and is not to be merged). The owner's three options, none chosen yet: (1) a separate repository; (2) hook into primitives the customer already runs (adopt their MultiKueue, Karmada, SkyPilot or Armada) rather than ship our own; (3) hold indefinitely. `ml-mvp.md` §5 and `reliant-for-ml.md` §11 mark it.
- 2026-11-26 owner (VERBATIM): "Improve things as you see fit. Simpler is better, not sure if we need MLcollector when it's really just an Otel collector that can be used without that…"
  - **No ML-specific collector.** `forge.MLCollector` and `MLStack.collector` are dropped (forge#2998, before merge). Cluster inventory and events become a generic, opt-in field of forge's existing node agent, `ObservabilityAgent.kubernetes_inventory`, useful to any app on a cluster: a `cluster-collector` Deployment with the `k8sobjects` receiver and a read-only ClusterRole, stamping `k8s.cluster.name`; with it on, node-agent scrapes are stamped `k8s.cluster.name` and `k8s.node.name`.
  - **A scrape is a plain `forge.Telemetry`** (forge#3030): a scraped component, the DCGM exporter included, is declared in the env's `observability.telemetry`, and `forge.Adopt` records only that it runs. `forge ml discover` proposes it that way (forge#2893). `Adopt`'s and `Install`'s scrape fields (`selector`, `port`, `allow`, `service_name`) shipped in forge v1.13.0, so under semver at major 1 they keep working unchanged, are deprecated with a render warning, and are removed only in a future major.
  - `pkg/mldiscover` and `pkg/mlrecs` move to `internal/` (forge#2893): their only user is `forge ml discover`.
