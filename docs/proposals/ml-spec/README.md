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
- 2026-11-26 owner: "take your recommendations on all". Every owner decision still open in the merged forge proposals is SETTLED on its recommended option. Each proposal records it in a dated "Settled (owner, 2026-11-26)" list, and its decision table is now a record that keeps the options weighed.
  - `ml-workloads.md` §11 and decisions 17–22 (18 was already settled): 1 public endpoint restricted to hub egress now, pull-mode delivery as the durable path; 2 local `forge ml discover` plus an in-cluster read-only collector; 3 accelerator adapters ship as versioned data; 4 own the run record and lineage, MLflow-compatible ingest, link out to W&B; 5 declared templates with recorded launches, observe-only always on; 6 meter pull egress, then a free regional allowance with cross-cloud billed; 7 model digest in the app release (`CarriedFrom`), per-env pin allowed; 8 in-app Changes variants now, router splits later as an ML exception; 9 nav label "ML"; 10 disconnect removes `forge-ml` components only; 11 ingest plus a free accelerator tier through beta; 12 a design partner's cluster gates wider access, plus a Spot L4/T4 per-release smoke pool (est. $15–40/month, prices to verify); 13 a separate `models/` allowance per plan; 14 tag fallback now with `models/` protected, referrers when distribution ships them; 15 a per-(cluster, env) read-only pull token; 16 entitlement in a DB table with admin RPCs and audit; 17 30s raw for 30 days, 13-month rollup on demand; 19 profiler captures in the customer's checkpoint store; 20 pod-start timeline derived at query time; 21 customer-declared accelerator prices plus OpenCost; 22 eval gates display-only first.
  - `mcp-from-rpcs.md` §9, decisions 1–9; `static-site-edge.md` §8, decisions 1–9; `changes.md` §8, decisions 4, 5, 7 and 8; `changes-from-sources.md` §7, decisions 1–4. `autoscaling.md`, `status-page.md`, `reliant-for-ml.md` and `llm-agent-observability.md` had no open rows.
