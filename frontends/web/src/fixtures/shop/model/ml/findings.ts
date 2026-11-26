// Copyright (c) 2026 Reliant Labs

/**
 * The ML fixture world's RECOMMENDATIONS: ListMlFindings, one finding per
 * world.ts FINDINGS id (every one, exactly once), each with its impact in a
 * comparable unit, the subjects inside it, the evidence (a chart, a table,
 * facts, events), the rule as a person reads it, and the adopt-first fix
 * (a KCL diff opened as a PR, or a command for what is outside the
 * declarations). The numbers come from the fleet model (fleet.ts) and the
 * shared curves, so the inbox agrees with the Fleet pages.
 *
 * Triage states: most are open; the straggler is assigned to maya; the
 * replica cold start has a PR (#412); the reranker's overprovisioning is
 * snoozed; the v1 /dev/shm fix is verified fixed and the pretrain checkpoint
 * interval dismissed (both only with include_closed). listMlEvents for a
 * FINDING subject answers the finding's evidence events.
 */

import type { MessageInitShape } from "@bufbuild/protobuf";

import {
  MlAxis,
  MlCollection,
  MlEventKind,
  MlFindingCategory,
  MlFindingState,
  MlFixKind,
  MlImpactUnit,
  MlSeverity,
  MlSubjectKind,
  type MlEventSchema,
  type MlFindingSchema,
  type MlSeriesSchema,
} from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";

import { v3StepTimeMs } from "./curves";
import type { MlDomain } from "./domain";
import { DCGM_PROFILING_MISSING, deviceByUuid, deviceSeries, fleetEvents, HW_ALERT_IDS, idleAllocations, nodeByName, sampleAt, subj } from "./fleet";
import { coverageRatio, STACK_ITEMS } from "./setup";
import {
  at,
  attemptId,
  BAD_GPU_INDEX,
  BAD_NODE,
  CLUSTERS,
  DAY,
  DPO_PENDING,
  EVAL_RUNS,
  FINDINGS,
  gpuUuid,
  hash,
  HOT_GPU_INDEX,
  HOT_NODE,
  HOUR,
  IDLE_NODE,
  MINUTE,
  ML_PEOPLE,
  MODELS,
  noise,
  NOTEBOOK_POD,
  NVLINK_NODE,
  phaseId,
  POOLS,
  PREEMPTOR_WORKLOAD,
  QUEUES,
  rankId,
  REMAP_GPU_INDEX,
  REMAP_NODE,
  REVISIONS,
  rng,
  RUNS,
  secAt,
  SERVICES,
  SPARE_NODE,
  STRAGGLER_RANK,
  STRAGGLER_SINCE,
  V3_ATTEMPTS,
  VERSIONS,
  workloadName,
} from "./world";

type FindingInit = MessageInitShape<typeof MlFindingSchema>;
type SeriesInit = MessageInitShape<typeof MlSeriesSchema>;
type EventInit = MessageInitShape<typeof MlEventSchema>;
const K = MlSubjectKind;
const C = MlFindingCategory;

// ── Subjects ──────────────────────────────────────────────────────────────────

const node = (name: string) => subj(K.NODE, nodeByName(name)!.id, name);
const gpu = (name: string, i: number) => subj(K.DEVICE, gpuUuid(name, i), `${name} GPU ${i}`);
const run = (r: { id: string; name: string }) => subj(K.RUN, r.id, r.name);
const attempt = (phase: string, n: number) => subj(K.ATTEMPT, attemptId(RUNS.v3.id, phase, n), `${phase} attempt ${n}`);
const rank = (r: number) => subj(K.RANK, rankId(RUNS.v3.id, "sft", 3, r), `rank ${r}`);
const pool = (p: { id: string; name: string }) => subj(K.POOL, p.id, p.name);
const service = (s: { id: string }) => subj(K.INFERENCE_SERVICE, s.id, s.id);
const cluster = (c: { id: string; name: string }) => subj(K.CLUSTER, c.id, c.name);
const replicaS = (svc: string, pod: string, label: string) => subj(K.REPLICA, `${svc}/${pod}`, label);

// ── Evidence helpers ──────────────────────────────────────────────────────────

/** A time series over the last `hours`, sampled every `resMin` minutes. */
function series(now: () => number, key: string, label: string, unit: string, hours: number, resMin: number, fn: (offMs: number, r: () => number) => number, group = "", seed = key): SeriesInit {
  const r = rng(hash(`${seed}|${group}`));
  const x: number[] = [];
  const y: number[] = [];
  for (let off = -hours * HOUR; off <= 0; off += resMin * MINUTE) {
    x.push(secAt(off, now));
    y.push(fn(off, r));
  }
  return { key, label, unit, axis: MlAxis.TIME, x, y, group, resolution: resMin * 60, collection: MlCollection.COLLECTED };
}

function withGroup(s: SeriesInit | undefined, group: string): SeriesInit[] {
  return s ? [{ ...s, group }] : [];
}

const eventsById = (now: () => number, ids: string[]) => fleetEvents(now).filter((e) => ids.some((id) => e.id === id || (id.endsWith("*") && e.id!.startsWith(id.slice(0, -1)))));

const diff = (path: string, hunk: string) => `--- a/${path}\n+++ b/${path}\n${hunk.trim()}\n`;

// ── The findings ──────────────────────────────────────────────────────────────

const TRAIN = CLUSTERS.train.id;
const SERVE = CLUSTERS.serve.id;
const TRAIN_KCL = "deploy/kcl/ml/gke-us-central1-train.k";
const SERVE_KCL = "deploy/kcl/ml/eks-us-east-1-serve.k";
const PROD_KCL = "deploy/kcl/prod/main.k";

const XID_AT = V3_ATTEMPTS[3].endedAt!;
const h100Day = POOLS.h100.usdPerGpuHour * 24;

function build(now: () => number): FindingInit[] {
  const idle = idleAllocations(now);
  const notebook = idle.find((i) => i.pod === NOTEBOOK_POD.pod)!;
  const evalIdle = idle.find((i) => i.workload.id === EVAL_RUNS.v3ckpt)!;
  const straggleUsd = 64 * h100Day * 0.15;

  return [
    // ── Health ──────────────────────────────────────────────────────────────
    {
      id: FINDINGS.xid79,
      ruleId: "gpu-fatal-xid",
      category: C.HEALTH,
      severity: MlSeverity.CRITICAL,
      title: `Faulty GPU: ${BAD_NODE} GPU ${BAD_GPU_INDEX} fell off the bus (Xid 79)`,
      summary: "Cordoned automatically; 8 reserved H100s out of service until the board is reset or replaced.",
      why: `Xid 79 killed v3 sft attempt 2 at step 32,750 (750 steps since checkpoint 32,000 lost, 64 GPUs). The node is cordoned and its 8 reserved H100s ($${Math.round(8 * h100Day)}/day) do nothing; the hot spare that replaced it was the last one.`,
      impact: { unit: MlImpactUnit.USD_PER_DAY, value: 8 * h100Day, usdPerDay: 8 * h100Day, gpuHoursPerDay: 192, text: `8 H100 out of service · $${Math.round(8 * h100Day)}/day` },
      subjects: [gpu(BAD_NODE, BAD_GPU_INDEX), node(BAD_NODE), attempt("sft", 2), run(RUNS.v3)],
      clusterIds: [TRAIN],
      state: MlFindingState.OPEN,
      firstSeenAt: at(XID_AT, now),
      lastSeenAt: at(-2 * MINUTE, now),
      rule: "A fatal Xid (79, 48, 62, 64, 74, 94, 95, 119, 120) from the kernel log or DCGM_EXP_XID_ERRORS_TOTAL on any GPU",
      evidenceSeries: [...withGroup(deviceSeries(gpuUuid(BAD_NODE, 4), "gpu.util", now, 48), "GPU 4"), ...withGroup(deviceSeries(gpuUuid(BAD_NODE, BAD_GPU_INDEX), "gpu.util", now, 48), "GPU 5")],
      evidenceTable: {
        columns: ["Time", "Source", "What"],
        rows: [
          { cells: ["26h ago", "kernel", "NVRM: Xid (PCI:0000:ab:00): 79, GPU has fallen off the bus"], subject: gpu(BAD_NODE, BAD_GPU_INDEX) },
          { cells: ["26h ago", "npd", "GPUXidError=True (XidCriticalError)"], subject: node(BAD_NODE) },
          { cells: ["26h ago", "forge", `alert-xid-fatal: cordoned, swapped in ${SPARE_NODE}`], subject: node(SPARE_NODE) },
          { cells: ["25h ago", "dcgm", "dcgmi diag -r 3: GPU 5 not found; 7 of 8 passed"], subject: node(BAD_NODE) },
          { cells: ["26h ago", "forge", "sft attempt 3 running from checkpoint 32,000"], subject: attempt("sft", 3) },
        ],
      },
      evidenceFacts: [
        { label: "PCI bus id", value: "0000:ab:00.0" },
        { label: "Board history", value: `moved from ${SPARE_NODE} 40 days ago` },
        { label: "Allocatable", value: "nvidia.com/gpu 7 of 8" },
        { label: "Health score", value: "92 / 100" },
      ],
      evidenceEvents: eventsById(now, ["ev-xid79-r4vb", "ev-cordon-r4vb", "ev-diag-r4vb"]),
      fix: {
        kind: MlFixKind.COMMAND,
        summary: "Power-cycle the node to re-enumerate the PCIe bus, then run DCGM diag level 3 before uncordoning. If GPU 5 is still missing, report the host as faulty so GCE replaces it.",
        command: `gcloud compute instances reset ${BAD_NODE} --zone ${POOLS.h100.zone}\nreliant forge ml diag --node ${BAD_NODE} --level 3\ngcloud beta compute instances report-host-as-faulty ${BAD_NODE} --zone ${POOLS.h100.zone} --disruption-schedule=IMMEDIATE --fault-reasons=behavior=GPU_ERROR,description="Xid 79 on GPU 5"`,
        docsUrl: "https://docs.nvidia.com/deploy/xid-errors/analyzing-xid-catalog.html",
        approach: "",
      },
      verify: "Verified when nvidia-smi lists 8 GPUs, DCGM diag r3 passes and the node is uncordoned with no Xid for 24h.",
    },
    {
      id: FINDINGS.thermal,
      ruleId: "gpu-hw-thermal-slowdown",
      category: C.HEALTH,
      severity: MlSeverity.WARNING,
      title: `${HOT_NODE} GPU ${HOT_GPU_INDEX} in HW thermal slowdown (87°C, HBM 95°C)`,
      summary: `Its SM clock drops to 1,290 MHz; it is rank ${STRAGGLER_RANK} of v3, so the whole job waits on it.`,
      why: "HW thermal slowdown 47% of the last 12h; every allreduce of the 64-GPU job waits for this GPU, so v3 runs 15% slower (the straggler finding counts the money). HBM at its 95°C limit precedes ECC errors: SBE 14/h already.",
      impact: { unit: MlImpactUnit.RUN_HOURS_AT_RISK, value: 3.6, usdPerDay: 0, gpuHoursPerDay: 0, text: "3.6 run-hours/day lost (cost under the straggler)" },
      subjects: [gpu(HOT_NODE, HOT_GPU_INDEX), node(HOT_NODE), rank(STRAGGLER_RANK), run(RUNS.v3)],
      clusterIds: [TRAIN],
      state: MlFindingState.OPEN,
      firstSeenAt: at(STRAGGLER_SINCE, now),
      lastSeenAt: at(-MINUTE, now),
      rule: "HW thermal slowdown (DCGM_FI_DEV_CLOCKS_EVENT_REASONS & 0x40) for more than 10% of 15 min, or DCGM_FI_DEV_GPU_TEMP within 2°C of DCGM_FI_DEV_SLOWDOWN_TEMP",
      evidenceSeries: [
        ...withGroup(deviceSeries(gpuUuid(HOT_NODE, HOT_GPU_INDEX), "gpu.temp_c", now, 24), "GPU 5 temperature"),
        ...withGroup(deviceSeries(gpuUuid(HOT_NODE, HOT_GPU_INDEX), "gpu.hbm_temp_c", now, 24), "GPU 5 HBM"),
        ...withGroup(deviceSeries(gpuUuid(HOT_NODE, 4), "gpu.temp_c", now, 24), "GPU 4 (neighbour)"),
      ],
      evidenceTable: {
        columns: ["GPU", "Temp", "HBM", "SM clock", "Clock events"],
        rows: [3, 4, 5, 6].map((i) => {
          const d = deviceByUuid(gpuUuid(HOT_NODE, i))!;
          const s = sampleAt(d, 0, now);
          return { cells: [`GPU ${i}`, `${Math.round(s.tempC)}°C`, `${Math.round(s.hbmC)}°C`, `${Math.round(s.smClk)} MHz`, i === HOT_GPU_INDEX ? "HW slowdown, HW thermal slowdown" : "none"], subject: gpu(HOT_NODE, i) };
        }),
      },
      evidenceFacts: [
        { label: "Slowdown temperature", value: "87°C" },
        { label: "Thermal violation (24h)", value: "5h 42m" },
        { label: "Fan / airflow", value: "SXM board: no fan telemetry (N/A)" },
      ],
      evidenceEvents: eventsById(now, ["ev-thermal-c8dw-*", "ev-straggler-c8dw"]),
      fix: {
        kind: MlFixKind.COMMAND,
        summary: "The cooling is the cloud's: report the host for a performance fault so GCE migrates the VM at the next maintenance window; drain at a checkpoint first so v3 resumes on healthy hardware.",
        command: `reliant forge ml drain --node ${HOT_NODE} --at-next-checkpoint\ngcloud beta compute instances report-host-as-faulty ${HOT_NODE} --zone ${POOLS.h100.zone} --disruption-schedule=FUTURE --fault-reasons=behavior=PERFORMANCE,description="GPU 5 HW thermal slowdown at 87C"`,
        approach: "",
      },
      verify: "Verified when GPU 5 runs 24h under load with no HW thermal slowdown and its SM clock within 5% of the job median.",
    },
    {
      id: FINDINGS.rowRemap,
      ruleId: "gpu-row-remap-pending",
      category: C.HEALTH,
      severity: MlSeverity.WARNING,
      title: `${REMAP_NODE} GPU ${REMAP_GPU_INDEX}: row remap pending, needs a reset`,
      summary: "The HBM row is remapped only at the next GPU reset; until then an uncorrectable error on it kills the job.",
      why: "DCGM_FI_DEV_ROW_REMAP_PENDING=1 for 2 days, correctable remapped rows +3 in 7d, SBE 22/h. v3 runs on it: an uncorrectable error now would cost up to 1,000 steps (2.8h × 64 H100) and a restart.",
      impact: { unit: MlImpactUnit.RUN_HOURS_AT_RISK, value: 2.8, usdPerDay: 0, gpuHoursPerDay: 0, text: "up to 2.8 run-hours at risk (64 GPUs)" },
      subjects: [gpu(REMAP_NODE, REMAP_GPU_INDEX), node(REMAP_NODE), run(RUNS.v3)],
      clusterIds: [TRAIN],
      state: MlFindingState.OPEN,
      firstSeenAt: at(-2 * DAY, now),
      lastSeenAt: at(-MINUTE, now),
      rule: "DCGM_FI_DEV_ROW_REMAP_PENDING > 0 for 10 min (Ampere and later)",
      evidenceSeries: withGroup(deviceSeries(gpuUuid(REMAP_NODE, REMAP_GPU_INDEX), "gpu.ecc_sbe", now, 72), "SBE (volatile)"),
      evidenceTable: {
        columns: ["Field", "Value"],
        rows: [
          { cells: ["DCGM_FI_DEV_CORRECTABLE_REMAPPED_ROWS", "3"], subject: gpu(REMAP_NODE, REMAP_GPU_INDEX) },
          { cells: ["DCGM_FI_DEV_UNCORRECTABLE_REMAPPED_ROWS", "0"], subject: gpu(REMAP_NODE, REMAP_GPU_INDEX) },
          { cells: ["DCGM_FI_DEV_ROW_REMAP_PENDING", "1"], subject: gpu(REMAP_NODE, REMAP_GPU_INDEX) },
          { cells: ["DCGM_FI_DEV_ROW_REMAP_FAILURE", "0"], subject: gpu(REMAP_NODE, REMAP_GPU_INDEX) },
        ],
      },
      evidenceFacts: [{ label: "Last DCGM diag", value: "r2 warn, 2 days ago" }],
      evidenceEvents: eventsById(now, ["ev-ecc-m1xp", "ev-remap-m1xp"]),
      fix: {
        kind: MlFixKind.KCL_PR,
        summary: "Let forge drain the node at v3's next checkpoint and reset the GPU (nvidia-smi -r) so the remap applies, then resume. Opt-in per alert; this turns it on for row remaps.",
        kclPath: TRAIN_KCL,
        kclDiff: diff(
          TRAIN_KCL,
          `@@ -41,6 +41,10 @@ remediation = forge.MLRemediation {
     on_fatal_xid = forge.CordonAndReplace { spares = "h100-spare" }
     on_ecc_dbe = forge.CordonAndReplace { spares = "h100-spare" }
+    # A pending row remap applies only at a GPU reset: do it at a checkpoint
+    on_row_remap_pending = forge.DrainAndReset {
+        at = "next_checkpoint"
+        verify = forge.DcgmDiag { level = 2 }
+    }
 }`,
        ),
        approach: "",
      },
      verify: "Verified when ROW_REMAP_PENDING reads 0 after the reset and DCGM diag r2 passes.",
    },
    {
      id: FINDINGS.nvlinkCrc,
      ruleId: "nvlink-crc-rising",
      category: C.HEALTH,
      severity: MlSeverity.WARNING,
      title: `${NVLINK_NODE}: NVLink CRC errors rising between GPU 1 and GPU 3`,
      summary: "18,420 flit errors, ×4 in a day; replays slow NCCL on that pair.",
      why: "CRC flit errors 4,610 → 18,420 in 24h (fleet p99: 40), 96 replays. Each replay stalls the collective on this pair; a link that degrades further drops to PCIe and takes the job's allreduce with it.",
      impact: { unit: MlImpactUnit.RUN_HOURS_AT_RISK, value: 1.2, usdPerDay: 0, gpuHoursPerDay: 0, text: "1.2 run-hours/day at risk" },
      subjects: [node(NVLINK_NODE), gpu(NVLINK_NODE, 1), gpu(NVLINK_NODE, 3), run(RUNS.v3)],
      clusterIds: [TRAIN],
      state: MlFindingState.OPEN,
      firstSeenAt: at(-30 * HOUR, now),
      lastSeenAt: at(-6 * HOUR, now),
      rule: "rate(DCGM_FI_DEV_NVLINK_CRC_FLIT_ERROR_COUNT_TOTAL[1h]) above the fleet p99 for 30 min",
      evidenceSeries: [...withGroup(deviceSeries(gpuUuid(NVLINK_NODE, 1), "gpu.nvlink_crc", now, 48), "GPU 1"), ...withGroup(deviceSeries(gpuUuid(NVLINK_NODE, 3), "gpu.nvlink_crc", now, 48), "GPU 3")],
      evidenceTable: {
        columns: ["Pair", "State", "CRC flit", "Replays"],
        rows: [
          { cells: ["GPU 1 ↔ GPU 3", "degraded", "18,420", "96"], subject: gpu(NVLINK_NODE, 1) },
          { cells: ["GPU 1 ↔ GPU 2", "up", "2", "0"], subject: gpu(NVLINK_NODE, 2) },
        ],
      },
      evidenceFacts: [{ label: "NVSwitch", value: "4× NVSwitch gen3; no SXid" }],
      evidenceEvents: eventsById(now, ["ev-nvlink-t5hn-*"]),
      fix: {
        kind: MlFixKind.COMMAND,
        summary: "Run the NVLink diagnostics at the next checkpoint drain; if the pair fails, report the host so the board is serviced.",
        command: `reliant forge ml diag --node ${NVLINK_NODE} --level 3 --at-next-checkpoint --gpus 1,3`,
        approach: "",
      },
      verify: "Verified when the CRC flit rate stays below the fleet p99 for 24h.",
    },

    // ── Waste ───────────────────────────────────────────────────────────────
    {
      id: FINDINGS.idleNotebook,
      ruleId: "idle-allocated-gpu",
      category: C.WASTE,
      severity: MlSeverity.WARNING,
      title: `Notebook ${NOTEBOOK_POD.pod} holds 2 L4s at 0% for ${NOTEBOOK_POD.idleHours}h`,
      summary: "No kernel activity since yesterday; the GPUs are allocated and do nothing.",
      why: `$${Math.round(notebook.usdPerDay)}/day and 48 GPU-h/day while serving has 3 free L4s and no spare TP-2 pair on one node.`,
      impact: { unit: MlImpactUnit.USD_PER_DAY, value: notebook.usdPerDay, usdPerDay: notebook.usdPerDay, gpuHoursPerDay: 48, text: `$${Math.round(notebook.usdPerDay)}/day · 48 GPU-h/day` },
      subjects: [subj(K.POD, `${NOTEBOOK_POD.namespace}/${NOTEBOOK_POD.pod}`, NOTEBOOK_POD.pod), node(IDLE_NODE), gpu(IDLE_NODE, 0), gpu(IDLE_NODE, 1)],
      clusterIds: [SERVE],
      state: MlFindingState.OPEN,
      firstSeenAt: at(-(NOTEBOOK_POD.idleHours - 1) * HOUR, now),
      lastSeenAt: at(-MINUTE, now),
      rule: "Allocated GPUs with DCGM_FI_DEV_GPU_UTIL < 5% for ≥ 1h (DCGM_FI_PROF_GR_ENGINE_ACTIVE where collected)",
      evidenceSeries: [
        series(now, "fleet.allocated", "Allocated", "count", 72, 30, (off) => (off >= -(NOTEBOOK_POD.idleHours + 7) * HOUR ? 2 : 0), "allocated"),
        series(now, "fleet.busy", "Busy", "count", 72, 30, (off, r) => (off >= -(NOTEBOOK_POD.idleHours + 7) * HOUR && off < -NOTEBOOK_POD.idleHours * HOUR ? Math.max(0, 0.6 + 0.3 * noise(r)) : 0), "busy"),
      ],
      evidenceTable: {
        columns: ["Pod", "Namespace", "GPUs", "Idle", "Owner", "Last active"],
        rows: [{ cells: [NOTEBOOK_POD.pod, NOTEBOOK_POD.namespace, "2", `${NOTEBOOK_POD.idleHours}h`, ML_PEOPLE.notebooks, `${NOTEBOOK_POD.idleHours}h ago`], subject: node(IDLE_NODE) }],
      },
      evidenceFacts: [
        { label: "Node", value: IDLE_NODE },
        { label: "Kernel", value: "idle; no cell executed in 19h" },
      ],
      evidenceEvents: eventsById(now, ["ev-idle-notebook"]),
      fix: {
        kind: MlFixKind.KCL_PR,
        summary: "Turn on JupyterHub's idle culler, which the cluster already runs (adopt): stop GPU notebooks idle for 2h, keeping their volumes.",
        kclPath: SERVE_KCL,
        kclDiff: diff(
          SERVE_KCL,
          `@@ -63,5 +63,9 @@ notebooks = forge.Adopt {
     kind = "Deployment"
     namespace = "ml-notebooks"
     name = "jupyterhub"
+    idle_cull = forge.IdleCull {
+        gpu_util_below = 0.05
+        after = "2h"
+    }
 }`,
        ),
        approach: "adopt",
      },
      verify: "Verified when no notebook holds an idle GPU for more than 2h over 24h.",
    },
    {
      id: FINDINGS.idleEvalHolding,
      ruleId: "idle-allocated-gpu",
      category: C.WASTE,
      severity: MlSeverity.WARNING,
      title: "A finished eval's vLLM pod holds 4 A100s idle",
      summary: "The release-gate run on v3's sft 40k checkpoint finished; its model server never exited.",
      why: `4 A100 idle for ${evalIdle.idleHours.toFixed(1)}h: $${Math.round(evalIdle.usdPerDay)}/day and 96 GPU-h/day while it lasts, and the next periodic eval waits for a100-dev capacity.`,
      impact: { unit: MlImpactUnit.USD_PER_DAY, value: evalIdle.usdPerDay, usdPerDay: evalIdle.usdPerDay, gpuHoursPerDay: 96, text: `$${Math.round(evalIdle.usdPerDay)}/day · 96 GPU-h/day` },
      subjects: [subj(K.EVAL_RUN, EVAL_RUNS.v3ckpt, "release-gate · v3 sft 40k"), node("gke-dev-a100-k3p9"), ...[0, 1, 2, 3].map((i) => gpu("gke-dev-a100-k3p9", i))],
      clusterIds: [TRAIN],
      state: MlFindingState.OPEN,
      firstSeenAt: at(-HOUR, now),
      lastSeenAt: at(-MINUTE, now),
      rule: "Allocated GPUs with DCGM_FI_PROF_GR_ENGINE_ACTIVE < 0.05 for ≥ 1h",
      evidenceSeries: [0, 1, 2, 3].flatMap((i) => withGroup(deviceSeries(gpuUuid("gke-dev-a100-k3p9", i), "gpu.sm_active", now, 6), `GPU ${i}`)),
      evidenceTable: {
        columns: ["Pod", "Namespace", "GPUs", "Idle", "Owner"],
        rows: [{ cells: [evalIdle.pod, evalIdle.namespace, "4", `${evalIdle.idleHours.toFixed(1)}h`, evalIdle.owner], subject: subj(K.EVAL_RUN, EVAL_RUNS.v3ckpt, "release-gate · v3 sft 40k") }],
      },
      evidenceFacts: [{ label: "Job status", value: "Complete (eval container exited 0; vllm-server still Running)" }],
      evidenceEvents: [],
      fix: {
        kind: MlFixKind.KCL_PR,
        summary: "Run the model server as a native sidecar of the eval Job, so it stops when the eval exits, and garbage-collect finished eval Jobs after 10 minutes.",
        kclPath: TRAIN_KCL,
        kclDiff: diff(
          TRAIN_KCL,
          `@@ -88,7 +88,8 @@ evals = forge.MLEvals {
     suites = [release_gate, release_gate_lite]
     pool = "a100-dev"
-    server = forge.VllmServer { mode = "container" }
+    server = forge.VllmServer { mode = "sidecar" }
+    ttl_after_finished = "10m"
 }`,
        ),
        approach: "",
      },
      verify: "Verified when finished eval pods release their GPUs within 10 min for 7 days.",
    },
    {
      id: FINDINGS.rerankerOverprovisioned,
      ruleId: "serving-overprovisioned",
      category: C.WASTE,
      severity: MlSeverity.INFO,
      title: "reranker-prod uses 2 L4s at 6% busy",
      summary: "Peak load fits one L4 with 3× headroom.",
      why: "GPU util p95 9%, memory 9.6 of 24 GB on each L4 over 7 days; one L4 would serve the peak at 27%. $23.5/day.",
      impact: { unit: MlImpactUnit.USD_PER_DAY, value: 23.52, usdPerDay: 23.52, gpuHoursPerDay: 24, text: "$24/day · 24 GPU-h/day" },
      subjects: [service(SERVICES.reranker), replicaS(SERVICES.reranker.id, "reranker-prod-triton-0", "reranker-prod replica 0"), gpu("ip-10-40-2-14", 1), gpu("ip-10-40-2-14", 2)],
      clusterIds: [SERVE],
      state: MlFindingState.SNOOZED,
      snoozedUntil: at(5 * DAY, now),
      firstSeenAt: at(-6 * DAY, now),
      lastSeenAt: at(-MINUTE, now),
      rule: "An inference workload whose GPU util p95 over 7d is under 25% of what it holds",
      evidenceSeries: [1, 2].flatMap((i) => withGroup(deviceSeries(gpuUuid("ip-10-40-2-14", i), "gpu.util", now, 24), `GPU ${i}`)),
      evidenceTable: { columns: ["GPU", "Util p95", "Memory"], rows: [1, 2].map((i) => ({ cells: [`GPU ${i}`, "9%", "9.6 GB"], subject: gpu("ip-10-40-2-14", i) })) },
      evidenceFacts: [{ label: "Snoozed by", value: `${ML_PEOPLE.notebooks}: "until the v5 reranker lands"` }],
      evidenceEvents: [],
      fix: {
        kind: MlFixKind.KCL_PR,
        summary: "Serve the reranker from one L4 (instance group count 1).",
        kclPath: PROD_KCL,
        kclDiff: diff(
          PROD_KCL,
          `@@ -152,7 +152,7 @@ reranker = forge.InferenceService {
     engine = forge.Triton { version = "24.10" }
     model = "${MODELS.reranker.id}:v4"
-    gpus = 2
+    gpus = 1
 }`,
        ),
        approach: "",
      },
      verify: "Verified when p95 latency holds within 10% for 7 days on one L4.",
    },

    // ── Scheduling ──────────────────────────────────────────────────────────
    {
      id: FINDINGS.quotaStarved,
      ruleId: "queue-starved",
      category: C.SCHEDULING,
      severity: MlSeverity.WARNING,
      title: `team-research has waited ${Math.round(DPO_PENDING.waitingMs / MINUTE / 60)}h for 32 H100s`,
      summary: "The DPO ablation is pending on insufficient quota: v3 holds the cohort's nominal 48, and serving-batch lends nothing.",
      why: `${RUNS.dpo.name} needs ${DPO_PENDING.gpus} H100s; team-research's nominal quota is 48 and v3 uses all of it through borrowing. team-serving-batch's 16 sit unused 70% of the week but are not lendable.`,
      impact: { unit: MlImpactUnit.RUN_HOURS_AT_RISK, value: DPO_PENDING.waitingMs / HOUR, usdPerDay: 0, gpuHoursPerDay: 0, text: `1 run waiting 3h 12m · 32 GPUs` },
      subjects: [subj(K.QUEUE, QUEUES.research.id, QUEUES.research.name), subj(K.WORKLOAD, workloadName(RUNS.dpo.name, "dpo", 1), workloadName(RUNS.dpo.name, "dpo", 1)), run(RUNS.dpo), subj(K.QUEUE, QUEUES.servingBatch.id, QUEUES.servingBatch.name)],
      clusterIds: [TRAIN],
      state: MlFindingState.OPEN,
      firstSeenAt: at(-DPO_PENDING.waitingMs, now),
      lastSeenAt: at(-MINUTE, now),
      rule: "A Workload pending on InsufficientQuota for over 1h while the cohort has idle nominal quota (kueue_pending_workloads, kueue_cluster_queue_resource_usage)",
      evidenceSeries: [
        series(now, "queue.pending_gpus", "Pending GPUs", "count", 24, 15, (off) => (off >= -DPO_PENDING.waitingMs ? 32 : 0), "team-research"),
        series(now, "queue.idle_nominal", "Idle nominal (serving-batch)", "count", 24, 15, (off) => (new Date(secAt(off, now) * 1000).getUTCHours() % 8 < 6 ? 16 : 0), "team-serving-batch"),
      ],
      evidenceTable: {
        columns: ["ClusterQueue", "Nominal", "Used", "Borrowing"],
        rows: [
          { cells: ["team-research", "48", "64", "16 borrowed"], subject: subj(K.QUEUE, QUEUES.research.id, QUEUES.research.name) },
          { cells: ["team-serving-batch", "16", "0", "lendingLimit 0"], subject: subj(K.QUEUE, QUEUES.servingBatch.id, QUEUES.servingBatch.name) },
        ],
      },
      evidenceFacts: [{ label: "Priority class", value: DPO_PENDING.priorityClass }],
      evidenceEvents: [],
      fix: {
        kind: MlFixKind.KCL_PR,
        summary: "Let team-serving-batch lend 8 of its 16 H100s inside the cohort (Kueue lendingLimit), keeping 8 for its own bursts.",
        kclPath: TRAIN_KCL,
        kclDiff: diff(
          TRAIN_KCL,
          `@@ -112,6 +112,7 @@ serving_batch = forge.KueueClusterQueue {
     cohort = "ml-h100"
     flavor = "h100"
     nominal = 16
+    lending_limit = 8
 }`,
        ),
        approach: "adopt",
      },
      verify: "Verified when no team-research workload waits over 1h for 7 days.",
    },
    {
      id: FINDINGS.preemptionChurn,
      ruleId: "preemption-churn",
      category: C.SCHEDULING,
      severity: MlSeverity.INFO,
      title: "Cohort reclaims preempt v3 without a checkpoint first",
      summary: `${PREEMPTOR_WORKLOAD} reclaimed 16 H100s and ended pretrain attempt 1: 400 steps redone, 1h50m waiting.`,
      why: "InCohortReclamation gives the borrower no warning; the job dies with 400 steps since its checkpoint (51 GPU-h redone) and waits for quota. Three reclaims in 7 days in ml-h100.",
      impact: { unit: MlImpactUnit.GPU_HOURS_PER_DAY, value: 22, usdPerDay: 22 * POOLS.h100.usdPerGpuHour, gpuHoursPerDay: 22, text: `$${Math.round(22 * POOLS.h100.usdPerGpuHour)}/day · 22 GPU-h/day` },
      subjects: [attempt("pretrain", 1), run(RUNS.v3), subj(K.WORKLOAD, PREEMPTOR_WORKLOAD, PREEMPTOR_WORKLOAD), subj(K.QUEUE, QUEUES.research.id, QUEUES.research.name)],
      clusterIds: [TRAIN],
      state: MlFindingState.OPEN,
      firstSeenAt: at(-150 * HOUR, now),
      lastSeenAt: at(-150 * HOUR, now),
      rule: "A Workload evicted by Preempted/InCohortReclamation that lost more than 10 min of progress since its last checkpoint",
      evidenceSeries: [],
      evidenceTable: {
        columns: ["When", "Victim", "Preemptor", "Steps redone"],
        rows: [{ cells: ["6d ago", "v3 pretrain attempt 1", PREEMPTOR_WORKLOAD, "400"], subject: attempt("pretrain", 1) }],
      },
      evidenceFacts: [{ label: "Grace period", value: "30s (pod default)" }],
      evidenceEvents: [],
      fix: {
        kind: MlFixKind.KCL_PR,
        summary: "Give preempted training a checkpoint first: a 5-minute grace period and a save on SIGTERM, which the SDK already handles.",
        kclPath: TRAIN_KCL,
        kclDiff: diff(
          TRAIN_KCL,
          `@@ -72,5 +72,9 @@ training = forge.MLTraining {
     sdk = "reliant-ml==0.4.2"
     inject = true
+    preemption = forge.Preemption {
+        grace = "5m"
+        checkpoint_on_sigterm = true
+    }
 }`,
        ),
        approach: "",
      },
      verify: "Verified when the next reclaim loses under 10 steps.",
    },

    // ── Startup ─────────────────────────────────────────────────────────────
    {
      id: FINDINGS.imagePull,
      ruleId: "image-pull-slow",
      category: C.STARTUP,
      severity: MlSeverity.WARNING,
      title: "support-assistant replicas take 5m40s to start; 62% is the image pull",
      summary: "A 14.2 GB vLLM image pulled fresh on each scale-up node.",
      why: "When the HPA scales at the afternoon peak, a new replica serves after 5m40s p95; TTFT p95 rises 340 ms meanwhile because the 4 replicas queue.",
      impact: { unit: MlImpactUnit.LATENCY_MS, value: 340, usdPerDay: 0, gpuHoursPerDay: 0, text: "cold start 5m40s · TTFT p95 +340 ms at scale-up" },
      subjects: [service(SERVICES.prod), subj(K.REVISION, REVISIONS.prodCurrent, REVISIONS.prodCurrent), replicaS(SERVICES.prod.id, "support-assistant-prod-vllm-3", "support-assistant-prod replica 3")],
      clusterIds: [SERVE],
      state: MlFindingState.FIX_PROPOSED,
      prUrl: "https://github.com/acme/shop/pull/412",
      assignee: ML_PEOPLE.admin,
      firstSeenAt: at(-4 * DAY, now),
      lastSeenAt: at(-5 * HOUR, now),
      rule: "Pod start p95 over 3 min with image pull above 40% of it (kubelet_image_pull_duration_seconds, kubelet_pod_start_total_duration_seconds)",
      evidenceSeries: [series(now, "pod.image_pull_seconds", "Image pull", "s", 7 * 24, 360, (off, r) => 205 + 12 * noise(r) + (Math.abs((off / HOUR) % 24) < 6 ? 6 : 0), "image pull")],
      evidenceTable: {
        columns: ["Stage", "p50", "p95", "Share"],
        rows: [
          { cells: ["scheduling", "2s", "4s", "1%"], subject: service(SERVICES.prod) },
          { cells: ["node provisioning", "48s", "71s", "21%"], subject: service(SERVICES.prod) },
          { cells: ["image pull", "3m 31s", "3m 52s", "62%"], subject: service(SERVICES.prod) },
          { cells: ["model load", "41s", "52s", "13%"], subject: service(SERVICES.prod) },
          { cells: ["ready", "9s", "12s", "3%"], subject: service(SERVICES.prod) },
        ],
      },
      evidenceFacts: [
        { label: "Image", value: "vllm/vllm-openai:v0.8.5 (14.2 GB)" },
        { label: "Pull throughput", value: "67 MB/s" },
      ],
      evidenceEvents: [],
      fix: {
        kind: MlFixKind.KCL_PR,
        summary: "Lazy-load the image with SOCI (EKS supports it on containerd 1.7) and pre-pull it on the l4-serve node class.",
        kclPath: PROD_KCL,
        kclDiff: diff(
          PROD_KCL,
          `@@ -131,6 +131,10 @@ support_assistant = forge.InferenceService {
     engine = forge.Vllm { version = "0.8.5", tensor_parallel = 2 }
     pool = "l4-serve"
+    image_streaming = forge.Soci { min_layer_size = "10MB" }
+    prepull = forge.Prepull {
+        node_selector = { "karpenter.sh/nodepool" = "l4-serve" }
+    }
 }`,
        ),
        approach: "adopt",
      },
      verify: "Verified when replica start p95 is under 2 min for 7 days.",
      podStart: {
        workload: "support-assistant-prod",
        sampleCount: 23,
        stages: [
          { stage: "scheduling", p50Seconds: 2, p95Seconds: 4, share: 0.01 },
          { stage: "node provisioning", p50Seconds: 48, p95Seconds: 71, share: 0.21, topCause: "Karpenter g6.12xlarge launch" },
          { stage: "image pull", p50Seconds: 211, p95Seconds: 232, share: 0.62, topCause: "14.2 GB vLLM image, no layer cache on a new node", findingId: FINDINGS.imagePull },
          { stage: "model load", p50Seconds: 41, p95Seconds: 52, share: 0.13, topCause: "16 GB weights from the registry", findingId: FINDINGS.modelLoad },
          { stage: "ready", p50Seconds: 9, p95Seconds: 12, share: 0.03 },
        ],
      },
    },
    {
      id: FINDINGS.modelLoad,
      ruleId: "model-load-slow",
      category: C.STARTUP,
      severity: MlSeverity.INFO,
      title: "Each replica pulls 16 GB of weights from the registry on start",
      summary: "Model load 41s p50 from us-docker.pkg.dev across regions; a node-local cache makes it ~8s.",
      why: "Every restart and scale-up re-pulls v2's 16 GB OCI artifact from us-central1 into us-east-1 (cross-region egress $0.02/GB): 41s of the cold start and $19/day of egress.",
      impact: { unit: MlImpactUnit.LATENCY_MS, value: 41_000, usdPerDay: 19, gpuHoursPerDay: 0, text: "41s per start · $19/day egress" },
      subjects: [service(SERVICES.prod), subj(K.MODEL_VERSION, VERSIONS.v2.id, `${MODELS.support.name} v2`)],
      clusterIds: [SERVE],
      state: MlFindingState.OPEN,
      firstSeenAt: at(-4 * DAY, now),
      lastSeenAt: at(-5 * HOUR, now),
      rule: "Model load stage p50 over 30s (vllm:model_load_time_seconds, the pod-start waterfall)",
      evidenceSeries: [series(now, "pod.model_load_seconds", "Model load", "s", 7 * 24, 360, (_o, r) => 41 + 5 * noise(r), "model load")],
      evidenceTable: { columns: ["Artifact", "Size", "Source"], rows: [{ cells: [VERSIONS.v2.digest.slice(0, 19), "16.1 GB", MODELS.support.repository], subject: subj(K.MODEL_VERSION, VERSIONS.v2.id, `${MODELS.support.name} v2`) }] },
      evidenceFacts: [],
      evidenceEvents: [],
      fix: {
        kind: MlFixKind.KCL_PR,
        summary: "Cache model artifacts on the nodes' local NVMe and mirror them into us-east-1.",
        kclPath: PROD_KCL,
        kclDiff: diff(
          PROD_KCL,
          `@@ -135,4 +135,5 @@ support_assistant = forge.InferenceService {
     model = "${MODELS.support.id}@${VERSIONS.v2.digest.slice(0, 19)}…"
+    model_cache = forge.NodeCache { path = "/mnt/nvme/models", mirror = "us-east1-docker.pkg.dev" }
 }`,
        ),
        approach: "",
      },
      verify: "Verified when model load p50 is under 10s.",
    },

    // ── Training efficiency ────────────────────────────────────────────────
    {
      id: FINDINGS.straggler,
      ruleId: "training-straggler",
      category: C.TRAINING_EFFICIENCY,
      severity: MlSeverity.WARNING,
      title: `v3 runs 15% slower: rank ${STRAGGLER_RANK} straggles on ${HOT_NODE} GPU ${HOT_GPU_INDEX}`,
      summary: "Every step waits ~18% longer in allreduce for one thermally throttled GPU.",
      why: `Step time ${Math.round(v3StepTimeMs("sft", 41_000, true))} ms vs ${Math.round(v3StepTimeMs("sft", 36_000, false))} ms before, since 12h ago: 15% of 64 H100 = $${Math.round(straggleUsd)}/day and the run finishes ~2 days later.`,
      impact: { unit: MlImpactUnit.USD_PER_DAY, value: straggleUsd, usdPerDay: straggleUsd, gpuHoursPerDay: 64 * 24 * 0.15, text: `$${Math.round(straggleUsd)}/day · ${Math.round(64 * 24 * 0.15)} GPU-h/day` },
      subjects: [rank(STRAGGLER_RANK), attempt("sft", 3), run(RUNS.v3), gpu(HOT_NODE, HOT_GPU_INDEX), node(HOT_NODE)],
      clusterIds: [TRAIN],
      state: MlFindingState.ASSIGNED,
      assignee: ML_PEOPLE.trainer,
      firstSeenAt: at(STRAGGLER_SINCE, now),
      lastSeenAt: at(-MINUTE, now),
      rule: "A rank whose DCGM_FI_DEV_SM_CLOCK is under 0.9× the run's median for 30 min while step time rises (train/step_time)",
      evidenceSeries: [
        series(now, "train.step_time_ms", "Step time", "ms", 24, 10, (off) => v3StepTimeMs("sft", 32_000 + Math.round((off + 25.86 * HOUR) / HOUR) * 359, off >= STRAGGLER_SINCE), "step time"),
        ...withGroup(deviceSeries(gpuUuid(HOT_NODE, HOT_GPU_INDEX), "gpu.sm_clock_mhz", now, 24), `rank ${STRAGGLER_RANK} SM clock`),
      ],
      evidenceTable: {
        columns: ["Rank", "Node", "GPU", "SM clock", "Allreduce wait"],
        rows: [36, 37, 38].map((r) => {
          const i = r % 8;
          const s = sampleAt(deviceByUuid(gpuUuid(HOT_NODE, i))!, 0, now);
          return { cells: [`${r}`, HOT_NODE, `GPU ${i}`, `${Math.round(s.smClk)} MHz`, r === STRAGGLER_RANK ? "2 ms" : "184 ms"], subject: rank(r) };
        }),
      },
      evidenceFacts: [
        { label: "Root cause", value: "HW thermal slowdown (see the thermal finding)" },
        { label: "Since step", value: "36,900" },
      ],
      evidenceEvents: eventsById(now, ["ev-straggler-c8dw"]),
      fix: {
        kind: MlFixKind.KCL_PR,
        summary: "Opt v3 into straggler remediation: at the next checkpoint, replace a rank whose clock stays under 0.9× median for 1h (needs a spare; h100-spare is empty, so it uses the eval pool's preemptible H100 reservation).",
        kclPath: TRAIN_KCL,
        kclDiff: diff(
          TRAIN_KCL,
          `@@ -41,6 +41,10 @@ remediation = forge.MLRemediation {
     on_fatal_xid = forge.CordonAndReplace { spares = "h100-spare" }
     on_ecc_dbe = forge.CordonAndReplace { spares = "h100-spare" }
+    on_straggler = forge.ReplaceAtCheckpoint {
+        clock_ratio_below = 0.9
+        for = "1h"
+    }
 }`,
        ),
        approach: "",
      },
      verify: "Verified when v3's step time returns within 3% of its pre-straggler median for 6h.",
    },
    {
      id: FINDINGS.checkpointBlocking,
      ruleId: "checkpoint-blocking",
      category: C.TRAINING_EFFICIENCY,
      severity: MlSeverity.INFO,
      title: "v3 stops training for 95s at every checkpoint",
      summary: "Checkpoints save synchronously: all 64 GPUs wait for 212 GB to reach GCS.",
      why: "95s × 8.6 checkpoints/day × 64 H100 = 14.5 GPU-h/day; async save (torch.distributed.checkpoint async_save) overlaps it with the next steps.",
      impact: { unit: MlImpactUnit.GPU_HOURS_PER_DAY, value: 14.5, usdPerDay: 14.5 * POOLS.h100.usdPerGpuHour, gpuHoursPerDay: 14.5, text: `$${Math.round(14.5 * POOLS.h100.usdPerGpuHour)}/day · 14.5 GPU-h/day` },
      subjects: [run(RUNS.v3), subj(K.PHASE, phaseId(RUNS.v3.id, "sft"), "sft"), attempt("sft", 3)],
      clusterIds: [TRAIN],
      state: MlFindingState.OPEN,
      firstSeenAt: at(-118 * HOUR, now),
      lastSeenAt: at(-2 * HOUR, now),
      rule: "Checkpoint save blocks the training loop for more than 30s (ckpt.save_time with ckpt.async = false)",
      evidenceSeries: [series(now, "ckpt.save_seconds", "Checkpoint save", "s", 24, 20, (off, r) => (Math.abs((off / MINUTE) % 167) < 20 ? 95 + 6 * noise(r) : NaN), "blocking save")],
      evidenceTable: { columns: ["Checkpoint", "Size", "Blocking"], rows: [41_000, 40_000, 39_000].map((s) => ({ cells: [`sft ${s.toLocaleString("en-US")}`, "212 GB", "95s"], subject: subj(K.CHECKPOINT, `${RUNS.v3.id}/sft/${s}`, `sft ${s}`) })) },
      evidenceFacts: [],
      evidenceEvents: [],
      fix: {
        kind: MlFixKind.KCL_PR,
        summary: "Turn on async checkpointing in v3's run config.",
        kclPath: "ml/runs/llama-8b-support-v3.k",
        kclDiff: diff(
          "ml/runs/llama-8b-support-v3.k",
          `@@ -28,5 +28,6 @@ checkpoint = forge.Checkpoint {
     every_steps = 1000
     store = "gs://acme-ml-ckpt/support-8b"
+    async_save = true
 }`,
        ),
        approach: "",
      },
      verify: "Verified when training-loop blocking per checkpoint is under 10s.",
    },
    {
      id: FINDINGS.dataloaderShm,
      ruleId: "dataloader-shm",
      category: C.TRAINING_EFFICIENCY,
      severity: MlSeverity.WARNING,
      title: "DataLoader workers crash on a 64Mi /dev/shm",
      summary: "v1 failed with a Bus error; runs now mount a memory-backed /dev/shm.",
      why: `${RUNS.v1.name} died with "Bus error" in a DataLoader worker: the container default 64Mi /dev/shm cannot hold the pinned batches of 8 workers. Fixed: v3 runs with 64Gi and uses 41.6 GB.`,
      impact: { unit: MlImpactUnit.RUN_HOURS_AT_RISK, value: 0, usdPerDay: 0, gpuHoursPerDay: 0, text: "fixed · verified 14d" },
      subjects: [run(RUNS.v1), run(RUNS.v3)],
      clusterIds: [TRAIN],
      state: MlFindingState.FIXED,
      prUrl: "https://github.com/acme/shop/pull/377",
      firstSeenAt: at(-30 * DAY, now),
      lastSeenAt: at(-29 * DAY, now),
      rule: "A training pod with DataLoader workers and /dev/shm under 1 GiB (node_filesystem_size_bytes{mountpoint=\"/dev/shm\"})",
      evidenceSeries: [],
      evidenceFacts: [{ label: "Verified", value: "no Bus error in 14 days; /dev/shm 64Gi on every rank" }],
      evidenceEvents: [],
      fix: {
        kind: MlFixKind.KCL_PR,
        summary: "Mount a memory-backed /dev/shm on training pods.",
        kclPath: TRAIN_KCL,
        kclDiff: diff(TRAIN_KCL, `@@ -72,4 +72,5 @@ training = forge.MLTraining {\n     sdk = "reliant-ml==0.4.2"\n+    shm = "64Gi"\n }`),
        approach: "",
      },
      verify: "Verified: the rule has been clear for 14 days.",
    },

    // ── Inference ───────────────────────────────────────────────────────────
    {
      id: FINDINGS.fp8SafetyGate,
      ruleId: "serving-gate-failing",
      category: C.INFERENCE,
      severity: MlSeverity.CRITICAL,
      title: "10% of prod traffic is on v2.1-fp8, which fails the release gate",
      summary: "safety_refusals 0.979 < 0.98 on release-gate v7; the Changes arm keeps serving it.",
      why: "The fp8 arm saves 38% of serving GPU time but refuses 1.2 points fewer unsafe prompts than v2. The gate is the org's own rule; Reliant never rolls back on its own, so the arm stays at 10% until someone moves it.",
      impact: { unit: MlImpactUnit.UNSPECIFIED, value: 0.1, usdPerDay: 0, gpuHoursPerDay: 0, text: "10% of prod requests on a gate-failing model" },
      subjects: [subj(K.MODEL_VERSION, VERSIONS.v21fp8.id, `${MODELS.support.name} v2.1-fp8`), subj(K.REVISION, REVISIONS.prodFp8, REVISIONS.prodFp8), subj(K.EVAL_RUN, EVAL_RUNS.v21fp8, "release-gate · v2.1-fp8"), service(SERVICES.prod)],
      clusterIds: [SERVE],
      state: MlFindingState.OPEN,
      firstSeenAt: at(-3 * DAY, now),
      lastSeenAt: at(-MINUTE, now),
      rule: "A model version serving any production traffic whose latest release-gate eval failed a gate (eval/safety_refusals ≥ 0.98)",
      evidenceSeries: [],
      evidenceTable: {
        columns: ["Task", "v2", "v2.1-fp8", "Gate"],
        rows: [
          { cells: ["safety_refusals", "0.991", "0.979", "≥ 0.980 ✕"], subject: subj(K.EVAL_RUN, EVAL_RUNS.v21fp8, "release-gate · v2.1-fp8") },
          { cells: ["support_tickets_v3", "0.871", "0.869", "≥ 0.85 ✓"], subject: subj(K.EVAL_RUN, EVAL_RUNS.v21fp8, "release-gate · v2.1-fp8") },
        ],
      },
      evidenceFacts: [{ label: "Changes experiment", value: "support-assistant-fp8 · 10%" }],
      evidenceEvents: [],
      fix: {
        kind: MlFixKind.KCL_PR,
        summary: "Roll forward: set the fp8 arm to 0% until a re-quantized version passes the gate.",
        kclPath: PROD_KCL,
        kclDiff: diff(
          PROD_KCL,
          `@@ -140,7 +140,7 @@ fp8 = forge.Changes {
     name = "support-assistant-fp8"
     arm = "${MODELS.support.id}:v2.1-fp8"
-    traffic_share = 0.1
+    traffic_share = 0.0  # fails release-gate v7 safety_refusals (0.979 < 0.98)
 }`,
        ),
        approach: "",
      },
      verify: "Verified when no production revision serves a version with a failed gate.",
    },
    {
      id: FINDINGS.kvSaturation,
      ruleId: "kv-cache-saturation",
      category: C.INFERENCE,
      severity: MlSeverity.WARNING,
      title: "support-assistant-prod's KV cache hits 94% at peak and preempts requests",
      summary: "vLLM swaps out sequences for 2h each afternoon; TTFT p95 +410 ms.",
      why: "vllm:gpu_cache_usage_perc above 0.9 for 2h a day with 31 preemptions/min at the peak; each preempted request recomputes its prefix.",
      impact: { unit: MlImpactUnit.LATENCY_MS, value: 410, usdPerDay: 0, gpuHoursPerDay: 0, text: "TTFT p95 +410 ms at peak" },
      subjects: [service(SERVICES.prod), subj(K.REVISION, REVISIONS.prodCurrent, REVISIONS.prodCurrent)],
      clusterIds: [SERVE],
      state: MlFindingState.OPEN,
      firstSeenAt: at(-5 * DAY, now),
      lastSeenAt: at(-3 * HOUR, now),
      rule: "vllm:gpu_cache_usage_perc > 0.9 with vllm:num_preemptions_total rising, for 15 min",
      evidenceSeries: [series(now, "serve.kv_cache_usage", "KV cache usage", "ratio", 24, 15, (off, r) => Math.min(0.97, 0.55 + 0.4 * Math.max(0, Math.sin(((new Date(secAt(off, now) * 1000).getUTCHours() - 9) / 24) * 2 * Math.PI)) + 0.02 * noise(r)), "KV cache")],
      evidenceFacts: [{ label: "gpu_memory_utilization", value: "0.88" }],
      evidenceEvents: [],
      fix: {
        kind: MlFixKind.KCL_PR,
        summary: "Raise vLLM's gpu_memory_utilization to 0.94 and enable prefix caching; the L4s have 2.3 GB free at peak.",
        kclPath: PROD_KCL,
        kclDiff: diff(
          PROD_KCL,
          `@@ -131,6 +131,7 @@ support_assistant = forge.InferenceService {
-    engine = forge.Vllm { version = "0.8.5", tensor_parallel = 2, gpu_memory_utilization = 0.88 }
+    engine = forge.Vllm { version = "0.8.5", tensor_parallel = 2, gpu_memory_utilization = 0.94, enable_prefix_caching = true }
     pool = "l4-serve"`,
        ),
        approach: "",
      },
      verify: "Verified when KV cache usage stays under 0.9 at peak for 7 days.",
    },

    // ── Coverage ────────────────────────────────────────────────────────────
    {
      id: FINDINGS.dcgmProfilingOff,
      ruleId: "dcgm-profiling-off",
      category: C.COVERAGE,
      severity: MlSeverity.INFO,
      title: "DCGM profiling counters are off on eks-us-east-1-serve",
      summary: "No SM active, tensor or DRAM activity for 16 L4s: utilisation reads only as time-active.",
      why: "GPU util 70% on a prod L4 says a kernel ran, not how hard. Without DCGM_FI_PROF_* the inference pages cannot tell decode-bound from compute-bound, and the waste rules fall back to the coarse util.",
      impact: { unit: MlImpactUnit.BLIND_SPOT, value: 0, usdPerDay: 0, gpuHoursPerDay: 0, text: "blind spot · 16 GPUs" },
      subjects: [cluster(CLUSTERS.serve), pool(POOLS.l4)],
      clusterIds: [SERVE],
      state: MlFindingState.OPEN,
      firstSeenAt: at(-7 * DAY, now),
      lastSeenAt: at(-27 * HOUR, now),
      rule: "A GPU node pool whose dcgm-exporter exports no DCGM_FI_PROF_* field",
      evidenceSeries: [{ key: "gpu.sm_active", label: "SM active", unit: "ratio", axis: MlAxis.TIME, x: [], y: [], collection: MlCollection.NOT_COLLECTED, missingSource: DCGM_PROFILING_MISSING, fixFindingId: FINDINGS.dcgmProfilingOff }],
      evidenceFacts: [{ label: "Counters CSV", value: "default-counters.csv (GPU Operator v24.6.2)" }],
      evidenceEvents: [],
      fix: {
        kind: MlFixKind.KCL_PR,
        summary: "Switch the adopted exporter to the dcp counters CSV (the GPU Operator's own option; L4 supports DCP).",
        kclPath: SERVE_KCL,
        kclDiff: diff(
          SERVE_KCL,
          `@@ -12,6 +12,8 @@ ml = forge.MLStack {
     accelerator_metrics = forge.Adopt {
         kind = "DaemonSet"
         namespace = "gpu-operator"
         name = "nvidia-dcgm-exporter"
+        # DCGM_FI_PROF_*: SM active, tensor, DRAM, PCIe bytes at 1 Hz
+        counters = forge.DcgmCounters { profile = "dcp" }
     }`,
        ),
        approach: "adopt",
      },
      verify: "Verified when DCGM_FI_PROF_SM_ACTIVE reports from every L4 for 1h.",
    },
    {
      id: FINDINGS.kubeletTracingOff,
      ruleId: "kubelet-tracing-off",
      category: C.COVERAGE,
      severity: MlSeverity.INFO,
      title: "Kubelet tracing is off on both clusters",
      summary: "Pod start has stage metrics but no spans: sandbox, pull and start are not in any trace.",
      why: "A slow cold start shows as 5m40s in a histogram; with KubeletTracing the pod-start waterfall shows which CRI call took it, per pod.",
      impact: { unit: MlImpactUnit.BLIND_SPOT, value: 0, usdPerDay: 0, gpuHoursPerDay: 0, text: "blind spot · pod start" },
      subjects: [cluster(CLUSTERS.train), cluster(CLUSTERS.serve)],
      clusterIds: [TRAIN, SERVE],
      state: MlFindingState.OPEN,
      firstSeenAt: at(-7 * DAY, now),
      lastSeenAt: at(-3 * HOUR, now),
      rule: "A cluster whose KubeletConfiguration has no tracing endpoint",
      evidenceSeries: [],
      evidenceFacts: [{ label: "Kubernetes", value: "1.31 (KubeletTracing beta, on by default)" }],
      evidenceEvents: [],
      fix: {
        kind: MlFixKind.KCL_PR,
        summary: "Point the kubelet at the collector forge already runs, sampling 1 in 100.",
        kclPath: SERVE_KCL,
        kclDiff: diff(
          SERVE_KCL,
          `@@ -30,4 +30,8 @@ ml = forge.MLStack {
     tracing = forge.Install { component = "otel-collector", version = "0.114.0", sampling = 0.05 }
+    kubelet_tracing = forge.KubeletTracing {
+        endpoint = "otel-collector.forge-system:4317"
+        sampling_rate_per_million = 10000
+    }
 }`,
        ),
        approach: "adopt",
      },
      verify: "Verified when kubelet spans arrive for new pods on both clusters.",
    },
    {
      id: FINDINGS.inferenceTracingOff,
      ruleId: "inference-tracing-off",
      category: C.COVERAGE,
      severity: MlSeverity.INFO,
      title: "reranker-prod sends no request traces",
      summary: "Triton's tracing is off; the reranker is a dark hop inside support-assistant's requests.",
      why: "support-assistant's traces show 120 ms in an untraced call to the reranker at p95; nothing says whether it is queue, compute or the network.",
      impact: { unit: MlImpactUnit.BLIND_SPOT, value: 0, usdPerDay: 0, gpuHoursPerDay: 0, text: "blind spot · 1 service" },
      subjects: [service(SERVICES.reranker), subj(K.REVISION, REVISIONS.reranker, REVISIONS.reranker)],
      clusterIds: [SERVE],
      state: MlFindingState.OPEN,
      firstSeenAt: at(-7 * DAY, now),
      lastSeenAt: at(-27 * HOUR, now),
      rule: "An inference service with no spans in 24h while its callers' traces reach it",
      evidenceSeries: [],
      evidenceFacts: [],
      evidenceEvents: [],
      fix: {
        kind: MlFixKind.KCL_PR,
        summary: "Turn on Triton's OpenTelemetry trace mode to the collector.",
        kclPath: PROD_KCL,
        kclDiff: diff(PROD_KCL, `@@ -150,4 +150,5 @@ reranker = forge.InferenceService {\n     engine = forge.Triton { version = "24.10" }\n+    tracing = forge.TritonTracing { mode = "opentelemetry", rate = 20 }\n }`),
        approach: "adopt",
      },
      verify: "Verified when reranker spans join support-assistant traces.",
    },
    {
      id: FINDINGS.trainingUntracked,
      ruleId: "training-untracked",
      category: C.COVERAGE,
      severity: MlSeverity.INFO,
      title: "reranker-nightly trains outside forge: no step stats or checkpoints",
      summary: "Observed from its pods only: GPU time and cost, but no loss, step time or goodput.",
      why: "A CronJob in ml-search fine-tunes the reranker nightly on 2 L4s; it is in Training as 'untracked' with hardware only.",
      impact: { unit: MlImpactUnit.BLIND_SPOT, value: 0, usdPerDay: 0, gpuHoursPerDay: 0, text: "blind spot · 1 run/day" },
      subjects: [run(RUNS.reranker)],
      clusterIds: [SERVE],
      state: MlFindingState.OPEN,
      firstSeenAt: at(-7 * DAY, now),
      lastSeenAt: at(-9 * HOUR, now),
      rule: "A pod on GPUs running a training framework with no training id on its metrics",
      evidenceSeries: [],
      evidenceFacts: [{ label: "Workload", value: "CronJob ml-search/reranker-nightly" }],
      evidenceEvents: [],
      fix: {
        kind: MlFixKind.KCL_PR,
        summary: "Adopt the CronJob as a training run, so forge injects the SDK and its run id.",
        kclPath: SERVE_KCL,
        kclDiff: diff(SERVE_KCL, `@@ -70,3 +70,8 @@\n+reranker_nightly = forge.MLTraining {\n+    adopt = forge.Adopt { kind = "CronJob", namespace = "ml-search", name = "reranker-nightly" }\n+    experiment = "reranker"\n+    sdk = "reliant-ml==0.4.2"\n+}`),
        approach: "adopt",
      },
      verify: "Verified when the next nightly reports step stats.",
    },
    {
      id: FINDINGS.npdGpuRulesMissing,
      ruleId: "npd-gpu-rules-missing",
      category: C.COVERAGE,
      severity: MlSeverity.INFO,
      title: "node-problem-detector on eks-us-east-1-serve has no GPU rules",
      summary: "Xids, thermal slowdown and NVLink faults never become node conditions there.",
      why: "The Xid 13 on ip-10-40-1-37 GPU 3 came only from DCGM; a fatal Xid would show as failing pods, not a node condition anything can act on.",
      impact: { unit: MlImpactUnit.BLIND_SPOT, value: 0, usdPerDay: 0, gpuHoursPerDay: 0, text: "blind spot · 4 nodes" },
      subjects: [cluster(CLUSTERS.serve), pool(POOLS.l4)],
      clusterIds: [SERVE],
      state: MlFindingState.OPEN,
      firstSeenAt: at(-7 * DAY, now),
      lastSeenAt: at(-27 * HOUR, now),
      rule: "A GPU node whose node-problem-detector runs no Xid monitor",
      evidenceSeries: [],
      evidenceFacts: [{ label: "Monitors", value: "kernel, docker, systemd, health-checker-kubelet" }],
      evidenceEvents: [],
      fix: {
        kind: MlFixKind.KCL_PR,
        summary: "Add forge's GPU rules to the adopted NPD (a second config, no restart of theirs).",
        kclPath: SERVE_KCL,
        kclDiff: diff(SERVE_KCL, `@@ -20,3 +20,4 @@ node_health = forge.Adopt {\n     name = "node-problem-detector"\n+    extra_rules = ["gpu-xid", "gpu-health"]\n }`),
        approach: "adopt",
      },
      verify: "Verified when GPUXidError reports on every L4 node.",
    },

    // ── Reliability ────────────────────────────────────────────────────────
    {
      id: FINDINGS.checkpointInterval,
      ruleId: "checkpoint-interval",
      category: C.RELIABILITY,
      severity: MlSeverity.INFO,
      title: "Pretrain checkpoints every 4h: a failure costs 2h × 64 H100 on average",
      summary: "Every 2,000 steps at 500 steps/h.",
      why: "With ~1 interruption per 2 days in ml-h100, the expected loss is 128 GPU-h per failure ($182/day); every 1,000 steps halves it.",
      impact: { unit: MlImpactUnit.USD_PER_DAY, value: 182, usdPerDay: 182, gpuHoursPerDay: 64, text: "$182/day · 64 GPU-h/day expected" },
      subjects: [subj(K.PHASE, phaseId(RUNS.v3.id, "pretrain"), "pretrain"), run(RUNS.v3)],
      clusterIds: [TRAIN],
      state: MlFindingState.DISMISSED,
      dismissReason: `${ML_PEOPLE.trainer}: pretrain finished; sft already checkpoints every 1,000 steps`,
      firstSeenAt: at(-160 * HOUR, now),
      lastSeenAt: at(-120 * HOUR, now),
      rule: "Expected loss per interruption (half the checkpoint interval × GPUs) above 64 GPU-h",
      evidenceSeries: [],
      evidenceFacts: [],
      evidenceEvents: [],
      fix: { kind: MlFixKind.KCL_PR, summary: "Checkpoint pretrain every 1,000 steps.", kclPath: "ml/runs/llama-8b-support-v3.k", kclDiff: diff("ml/runs/llama-8b-support-v3.k", `@@ -18,3 +18,3 @@ pretrain = forge.Phase {\n-    checkpoint_every = 2000\n+    checkpoint_every = 1000\n }`), approach: "" },
      verify: "Verified when the interval rule clears.",
    },
    {
      id: FINDINGS.ncclTimeout,
      ruleId: "nccl-timeout-default",
      category: C.RELIABILITY,
      severity: MlSeverity.WARNING,
      title: "NCCL watchdog at the 10-minute default: a hang idles 64 H100s for 10 min",
      summary: "sft attempt 1 sat hung for 600s on allreduce seq 88213 before anything noticed.",
      why: "Step time is 10s; a 2-minute timeout catches the same hang 8 minutes sooner. With the flight recorder on, the dump names the rank (it was 37).",
      impact: { unit: MlImpactUnit.GPU_HOURS_PER_DAY, value: 1.5, usdPerDay: 1.5 * POOLS.h100.usdPerGpuHour, gpuHoursPerDay: 1.5, text: "10.7 GPU-h per hang · ~1.5 GPU-h/day" },
      subjects: [attempt("sft", 1), run(RUNS.v3)],
      clusterIds: [TRAIN],
      state: MlFindingState.OPEN,
      firstSeenAt: at(-84 * HOUR, now),
      lastSeenAt: at(-84 * HOUR, now),
      rule: "A training run on more than 8 GPUs with TORCH_NCCL timeout ≥ 10 × its step time",
      evidenceSeries: [],
      evidenceFacts: [
        { label: "TORCH_NCCL_TIMEOUT", value: "600s (default)" },
        { label: "Step time", value: "~10s" },
      ],
      evidenceEvents: [],
      fix: {
        kind: MlFixKind.KCL_PR,
        summary: "Set a 120s collective timeout and turn on the NCCL flight recorder.",
        kclPath: "ml/runs/llama-8b-support-v3.k",
        kclDiff: diff(
          "ml/runs/llama-8b-support-v3.k",
          `@@ -40,3 +40,6 @@ env = {
     NCCL_IB_HCA = "mlx5"
+    TORCH_NCCL_TIMEOUT = "120"
+    TORCH_NCCL_TRACE_BUFFER_SIZE = "20000"
+    TORCH_NCCL_DUMP_ON_TIMEOUT = "1"
 }`,
        ),
        approach: "",
      },
      verify: "Verified on the next run that sets it.",
    },
    {
      id: FINDINGS.noSpareServe,
      ruleId: "no-hot-spare",
      category: C.RELIABILITY,
      severity: MlSeverity.WARNING,
      title: "No hot spare: serving has none, and h100-spare is used up",
      summary: `l4-serve has no idle TP-2 pair on one node; ${SPARE_NODE} replaced r4vb, so the next H100 fault waits for a new node.`,
      why: "Losing an L4 node at peak drops support-assistant to 2 replicas until Karpenter launches one (~6 min with the cold start): TTFT p95 +900 ms. On training, the next fatal Xid has no spare: v3 waits for repair instead of resuming in minutes.",
      impact: { unit: MlImpactUnit.LATENCY_MS, value: 900, usdPerDay: 0, gpuHoursPerDay: 0, text: "TTFT p95 +900 ms on a node loss · 0 H100 spares" },
      subjects: [pool(POOLS.l4), service(SERVICES.prod), pool(POOLS.spare), node(SPARE_NODE)],
      clusterIds: [SERVE, TRAIN],
      state: MlFindingState.OPEN,
      firstSeenAt: at(-26 * HOUR + 515 * 1000, now),
      lastSeenAt: at(-MINUTE, now),
      rule: "A pool serving production, or running a training run on more than 32 GPUs, with no schedulable spare node",
      evidenceSeries: [],
      evidenceTable: {
        columns: ["Pool", "Spares", "Free GPUs"],
        rows: [
          { cells: ["h100-spare", "0 of 1", "0"], subject: pool(POOLS.spare) },
          { cells: ["l4-serve", "0", "3 (no pair on one node)"], subject: pool(POOLS.l4) },
        ],
      },
      evidenceFacts: [],
      evidenceEvents: eventsById(now, ["ev-spare-s01"]),
      fix: {
        kind: MlFixKind.KCL_PR,
        summary: "Keep one warm L4 node for serving (Karpenter, adopted) and replenish h100-spare when it is used.",
        kclPath: SERVE_KCL,
        kclDiff: diff(
          SERVE_KCL,
          `@@ -44,4 +44,8 @@ l4_serve = forge.Adopt {
     kind = "NodePool"
     name = "l4-serve"
+    hot_spare = forge.HotSpare {
+        nodes = 1
+        taint = "forge.dev/spare=true:NoSchedule"
+    }
 }`,
        ),
        approach: "adopt",
      },
      verify: "Verified when each production pool keeps a schedulable spare for 7 days.",
    },
  ];
}

const CLOSED = new Set([MlFindingState.FIXED, MlFindingState.DISMISSED]);

/** Every finding (closed ones too), for tests and other domains. */
export function allFindings(now: () => number): FindingInit[] {
  return build(now);
}

/** Telemetry coverage across both clusters (the Coverage category rolled up). */
export const findingsCoverage = () => {
  const states = [...STACK_ITEMS.train(), ...STACK_ITEMS.serve()].map((i) => i.state);
  return coverageRatio(states);
};

export const findingsAnswers: MlDomain = {
  listMlFindings: (req, now) => {
    const findings = build(now).filter((f) => (req.includeClosed || !CLOSED.has(f.state!)) && (!req.clusterId || f.clusterIds?.includes(req.clusterId)));
    return { findings, coverageRatio: findingsCoverage() };
  },
  listMlEvents: (req, now) => {
    if (req.subject?.kind !== MlSubjectKind.FINDING) return undefined;
    const f = build(now).find((x) => x.id === req.subject!.id);
    if (!f) return undefined;
    const opened: EventInit = {
      id: `ev-opened-${f.id}`,
      at: f.firstSeenAt,
      kind: MlEventKind.FINDING_OPENED,
      severity: f.severity,
      title: `Opened: ${f.title}`,
      subject: subj(K.FINDING, f.id!, f.title!),
      source: "forge",
      hookEventType: "ml.finding.opened",
    };
    return { events: [...(f.evidenceEvents ?? []), opened] };
  },
};

/** The rule ids the fleet alerts reuse (for links between them). */
export const FINDING_ALERTS = { [FINDINGS.xid79]: HW_ALERT_IDS.xidFatal, [FINDINGS.thermal]: HW_ALERT_IDS.gpuThermal, [FINDINGS.rowRemap]: HW_ALERT_IDS.rowRemap, [FINDINGS.nvlinkCrc]: HW_ALERT_IDS.nvlinkCrc } as const;
