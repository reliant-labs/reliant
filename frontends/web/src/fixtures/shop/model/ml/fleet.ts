// Copyright (c) 2026 Reliant Labs

/**
 * The ML fixture world's FLEET (world.ts tells the story): GetMlFleet,
 * GetMlPool, GetMlNode, GetMlDevice, and the merged reads for NODE, DEVICE,
 * POOL, CLUSTER subjects (series, histograms, events, alerts), plus the ORG
 * aggregates the Overview reads ("pool.busy" by pool, "fleet.allocated",
 * "fleet.busy", "fleet.alloc_vs_busy"), the ORG hardware events and rules.
 *
 * ONE MODEL. Every device has a list of HOLDERS over the last week (the v3
 * attempts' ranks on the H100s, the evaluator and the gate's leftover pod on
 * the A100s, serving replicas and priya's notebook on the L4s), and one
 * `sampleAt(device, offset)` that every number is read from: the latest
 * field values, the series, the pool percentile bands, the fleet totals and
 * the Gantt of each node. So the KPI strip, the node table and the charts
 * can never disagree.
 *
 * Allocation now (sums checked by Fleet/__tests__/fleetFixture.test.ts):
 *   h100-train  56 of 64: v3 sft attempt 3, every node but r4vb (cordoned after
 *               Xid 79 on GPU 5; GPU 5 invisible to the driver since)
 *   h100-spare   8 of 8:  s01 swapped in for r4vb (ranks 16–23): 0 spares left
 *   a100-dev    12 of 16: x7qd 8 to v3's eval-periodic evaluator; k3p9 0–3 held
 *               idle by the release-gate eval's vLLM pod (finished 2h ago)
 *   l4-serve    13 of 16: 8 prod replicas (TP 2 × 4), 1 staging, 2 reranker,
 *               2 priya's notebook at 0% for 19h; 3 unallocated
 */

import type { DescMessage, MessageInitShape } from "@bufbuild/protobuf";

import {
  MlAxis,
  MlCapacityType,
  MlCollection,
  MlCostCategory,
  MlEventKind,
  MlFieldGroup,
  MlHealth,
  MlSeverity,
  MlSubjectKind,
  type MlDeviceSchema,
  type MlEventSchema,
  type MlFieldValueSchema,
  type MlNodeConditionSchema,
  type MlNodeSchema,
  type MlNodeWorkloadSchema,
  type MlPoolSchema,
  type MlSeriesSchema,
  type MlSubject,
} from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";

import { prodRps } from "./curves";
import type { MlDomain } from "./domain";
import { MlAlertState, type AlertRuleInit, type MlTelemetryDomain } from "./telemetryTypes";
import {
  ALERTS,
  ALL_POOLS,
  at,
  attemptId,
  BAD_GPU_INDEX,
  BAD_NODE,
  CLUSTERS,
  DAY,
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
  msAt,
  noise,
  NOTEBOOK_POD,
  NVLINK_NODE,
  phaseId,
  POOLS,
  PREEMPTOR_WORKLOAD,
  rankId,
  rankPod,
  REMAP_GPU_INDEX,
  REMAP_NODE,
  rng,
  RUNS,
  SERVICES,
  SPARE_NODE,
  STRAGGLER_RANK,
  STRAGGLER_SINCE,
  V3_ATTEMPTS,
  workloadName,
  type PoolDef,
} from "./world";

type Init<S extends DescMessage> = MessageInitShape<S>;
type Subj = Pick<MlSubject, "kind" | "id" | "label">;
type SeriesInit = Init<typeof MlSeriesSchema>;
type EventInit = Init<typeof MlEventSchema>;

export const subj = (kind: MlSubjectKind, id: string, label: string): Subj => ({ kind, id, label });

// ── Hardware specs ────────────────────────────────────────────────────────────

interface Spec {
  memGb: number;
  tdpW: number;
  idleW: number;
  maxClk: number;
  memClk: number;
  slowdownC: number;
  shutdownC: number;
  pcieGen: number;
  nvlinks: number;
  nvlinkGBs: number;
  vbios: string;
  bar1Gb: number;
  pci: readonly string[];
}

const SPECS: Record<"h100" | "a100" | "l4", Spec> = {
  h100: {
    memGb: 80,
    tdpW: 700,
    idleW: 72,
    maxClk: 1980,
    memClk: 2619,
    slowdownC: 87,
    shutdownC: 95,
    pcieGen: 5,
    nvlinks: 18,
    nvlinkGBs: 900,
    vbios: "96.00.89.00.01",
    bar1Gb: 128,
    pci: ["0000:18:00.0", "0000:2a:00.0", "0000:3a:00.0", "0000:5d:00.0", "0000:9a:00.0", "0000:ab:00.0", "0000:ba:00.0", "0000:db:00.0"],
  },
  a100: {
    memGb: 40,
    tdpW: 400,
    idleW: 55,
    maxClk: 1410,
    memClk: 1215,
    slowdownC: 89,
    shutdownC: 93,
    pcieGen: 4,
    nvlinks: 12,
    nvlinkGBs: 600,
    vbios: "92.00.36.00.01",
    bar1Gb: 64,
    pci: ["0000:00:04.0", "0000:00:05.0", "0000:00:06.0", "0000:00:07.0", "0000:00:80.0", "0000:00:81.0", "0000:00:82.0", "0000:00:83.0"],
  },
  l4: {
    memGb: 24,
    tdpW: 72,
    idleW: 16,
    maxClk: 2040,
    memClk: 6251,
    slowdownC: 92,
    shutdownC: 98,
    pcieGen: 4,
    nvlinks: 0,
    nvlinkGBs: 0,
    vbios: "95.04.29.00.06",
    bar1Gb: 32,
    pci: ["0000:38:00.0", "0000:3a:00.0", "0000:3c:00.0", "0000:3e:00.0"],
  },
};

const specOf = (pool: PoolDef): Spec => (pool.product.includes("H100") ? SPECS.h100 : pool.product.includes("A100") ? SPECS.a100 : SPECS.l4);
const isServe = (pool: PoolDef) => pool.clusterId === CLUSTERS.serve.id;

export const DCGM_PROFILING_MISSING = "DCGM profiling metrics (DCGM_FI_PROF_*) — not enabled in the exporter's counters CSV";
export const XID_GONE = `GPU ${BAD_GPU_INDEX} is not visible to the driver since Xid 79 (nvidia-smi lists 7 GPUs)`;
const XID_AT = V3_ATTEMPTS.find((a) => a.phase === "sft" && a.n === 2)!.endedAt!;

/** Devices whose clocks are held by the software power cap (benign, but counted as throttled). */
const POWER_CAPPED = new Set([`gke-train-h100-7f2k#2`, `gke-train-h100-9qzc#6`, `gke-train-h100-w2jl#1`, `gke-train-h100-b6ve#4`]);
/** The hot GPU's HW thermal slowdown episodes over the last 12h, [start, end) in hours (end null: now). */
export const THERMAL_EPISODES: readonly [number, number | null][] = [
  [-12, -11.2],
  [-10.1, -9.3],
  [-8, -7.1],
  [-6.2, -5.4],
  [-4.3, -3.2],
  [-2.2, -1.5],
  [-0.6, null],
];
const inThermal = (off: number) => THERMAL_EPISODES.some(([a, b]) => off >= a * HOUR && off < (b === null ? 1 : b * HOUR));

// ── Holders: who held each device, over the last week ─────────────────────────

export type HolderKind = "training" | "inference" | "eval" | "notebook" | "other";

export interface Holder {
  kind: HolderKind;
  subject: Subj;
  pod: string;
  namespace: string;
  owner: string;
  /** Offsets from now (ms); `to` null while it lasts. */
  from: number;
  to: number | null;
  /** From here on it holds the device and does nothing. */
  idleFrom?: number;
  /** A tag for the load profile. */
  profile: "train" | "preempt" | "evaluator" | "gate" | "prod" | "staging" | "reranker" | "notebook";
}

const H100_NODES = new Set<string>([...POOLS.h100.nodes, ...POOLS.spare.nodes]);
const replica = (service: string, pod: string, label: string) => subj(MlSubjectKind.REPLICA, `${service}/${pod}`, label);

/** The L4 layout: [node][gpu] → holder. */
function l4Holder(node: string, i: number): Holder | null {
  const prod = (n: number): Holder => ({
    kind: "inference",
    subject: replica(SERVICES.prod.id, `support-assistant-prod-vllm-${n}`, `support-assistant-prod replica ${n}`),
    pod: `support-assistant-prod-vllm-${n}`,
    namespace: "ml-serving",
    owner: ML_PEOPLE.admin,
    from: -9 * DAY,
    to: null,
    profile: "prod",
  });
  const layout: Record<string, (Holder | null)[]> = {
    "ip-10-40-1-21": [prod(0), prod(0), prod(1), prod(1)],
    "ip-10-40-1-37": [prod(2), prod(2), prod(3), prod(3)],
    "ip-10-40-2-14": [
      {
        kind: "inference",
        subject: replica(SERVICES.staging.id, "support-assistant-staging-vllm-0", "support-assistant-staging replica 0"),
        pod: "support-assistant-staging-vllm-0",
        namespace: "ml-serving",
        owner: ML_PEOPLE.admin,
        from: -3 * DAY,
        to: null,
        profile: "staging",
      },
      ...[0, 1].map(
        (): Holder => ({
          kind: "inference",
          subject: replica(SERVICES.reranker.id, "reranker-prod-triton-0", "reranker-prod replica 0"),
          pod: "reranker-prod-triton-0",
          namespace: "ml-serving",
          owner: ML_PEOPLE.notebooks,
          from: -9 * DAY,
          to: null,
          profile: "reranker",
        }),
      ),
      null,
    ],
    [IDLE_NODE]: [
      ...[0, 1].map(
        (): Holder => ({
          kind: "notebook",
          subject: subj(MlSubjectKind.POD, `${NOTEBOOK_POD.namespace}/${NOTEBOOK_POD.pod}`, NOTEBOOK_POD.pod),
          pod: NOTEBOOK_POD.pod,
          namespace: NOTEBOOK_POD.namespace,
          owner: ML_PEOPLE.notebooks,
          from: -(NOTEBOOK_POD.idleHours + 7) * HOUR,
          to: null,
          idleFrom: -NOTEBOOK_POD.idleHours * HOUR,
          profile: "notebook",
        }),
      ),
      null,
      null,
    ],
  };
  return layout[node]?.[i] ?? null;
}

const HOLDERS = new Map<string, Holder[]>();

/** Every holder of one device over the last week, oldest first. */
export function holdersOf(node: string, i: number): Holder[] {
  const key = `${node}#${i}`;
  let hs = HOLDERS.get(key);
  if (!hs) HOLDERS.set(key, (hs = buildHolders(node, i)));
  return hs;
}

function buildHolders(node: string, i: number): Holder[] {
  const out: Holder[] = [];
  if (H100_NODES.has(node)) {
    if (node === "gke-train-h100-w2jl" || node === "gke-train-h100-b6ve") {
      out.push({
        kind: "other",
        subject: subj(MlSubjectKind.WORKLOAD, PREEMPTOR_WORKLOAD, PREEMPTOR_WORKLOAD),
        pod: `${PREEMPTOR_WORKLOAD}-${node.slice(-4)}`,
        namespace: "ml-serving",
        owner: "team-serving-batch",
        from: -150 * HOUR,
        to: -148.2 * HOUR,
        profile: "preempt",
      });
    }
    for (const a of V3_ATTEMPTS) {
      const k = a.nodes.indexOf(node);
      if (k < 0) continue;
      const rank = k * 8 + i;
      out.push({
        kind: "training",
        subject: subj(MlSubjectKind.RANK, rankId(RUNS.v3.id, a.phase, a.n, rank), `${RUNS.v3.name} ${a.phase} a${a.n} · rank ${rank}`),
        pod: rankPod(RUNS.v3.name, a.phase, a.n, rank),
        namespace: "ml-research",
        owner: ML_PEOPLE.trainer,
        from: a.runningAt,
        to: a.endedAt,
        profile: "train",
      });
    }
    return out.sort((a, b) => a.from - b.from);
  }
  if (node === "gke-dev-a100-x7qd") {
    const sft1 = V3_ATTEMPTS.find((a) => a.phase === "sft" && a.n === 1)!;
    return [
      {
        kind: "eval",
        subject: subj(MlSubjectKind.PHASE, phaseId(RUNS.v3.id, "eval-periodic"), `${RUNS.v3.name} eval-periodic`),
        pod: `${workloadName(RUNS.v3.name, "eval-periodic", 1)}-0`,
        namespace: "ml-research",
        owner: ML_PEOPLE.trainer,
        from: sft1.runningAt,
        to: null,
        profile: "evaluator",
      },
    ];
  }
  if (node === "gke-dev-a100-k3p9") {
    if (i > 3) return [];
    return [
      {
        kind: "eval",
        subject: subj(MlSubjectKind.EVAL_RUN, EVAL_RUNS.v3ckpt, "release-gate · v3 sft 40k"),
        pod: "eval-release-gate-v3-sft40k-vllm-0",
        namespace: "ml-research",
        owner: ML_PEOPLE.trainer,
        from: -3.2 * HOUR,
        to: null,
        idleFrom: -2.05 * HOUR,
        profile: "gate",
      },
    ];
  }
  const h = l4Holder(node, i);
  return h ? [h] : [];
}

/** The holder at an offset, or null. */
export function holderAt(node: string, i: number, off: number): Holder | null {
  if (node === BAD_NODE && off >= XID_AT) return null;
  return holdersOf(node, i).find((h) => off >= h.from && off <= (h.to ?? 1)) ?? null;
}

// ── Health ────────────────────────────────────────────────────────────────────

export interface ScoreInput {
  input: string;
  value: string;
  points: number;
}

interface DeviceHealth {
  health: MlHealth;
  score: number;
  reason: string;
  inputs: ScoreInput[];
  findingId?: string;
}

function deviceHealth(node: string, i: number): DeviceHealth {
  if (node === BAD_NODE && i === BAD_GPU_INDEX)
    return {
      health: MlHealth.FAULTY,
      score: 92,
      reason: "Xid 79: GPU has fallen off the bus",
      findingId: FINDINGS.xid79,
      inputs: [
        { input: "Xid 79 (fatal) in 7d", value: "1, 26h ago", points: 60 },
        { input: "DCGM diag r3 fail", value: "GPU not found", points: 32 },
      ],
    };
  if (node === HOT_NODE && i === HOT_GPU_INDEX)
    return {
      health: MlHealth.DEGRADED,
      score: 61,
      reason: "HW thermal slowdown at 87°C (HBM 95°C)",
      findingId: FINDINGS.thermal,
      inputs: [
        { input: "HW thermal slowdown time", value: "47% of the last 12h", points: 26 },
        { input: "HBM temperature", value: "95°C (max operating 95°C)", points: 15 },
        { input: "Clock straggler in ≥ 2 runs", value: "v3 sft a1, a3 (rank 37)", points: 10 },
        { input: "SBE rate (24h)", value: "14/h", points: 10 },
      ],
    };
  if (node === REMAP_NODE && i === REMAP_GPU_INDEX)
    return {
      health: MlHealth.DEGRADED,
      score: 44,
      reason: "Row remap pending: needs a GPU reset",
      findingId: FINDINGS.rowRemap,
      inputs: [
        { input: "Row remap pending", value: "1", points: 25 },
        { input: "Correctable remapped rows (7d)", value: "+3", points: 9 },
        { input: "SBE rate (24h)", value: "22/h", points: 10 },
      ],
    };
  if (node === NVLINK_NODE && (i === 1 || i === 3))
    return {
      health: MlHealth.DEGRADED,
      score: 38,
      reason: "NVLink CRC errors rising (GPU 1 ↔ GPU 3)",
      findingId: FINDINGS.nvlinkCrc,
      inputs: [
        { input: "NVLink error rate", value: "above fleet p99", points: 15 },
        { input: "CRC flit errors, day over day", value: "×4", points: 13 },
        { input: "NVLink replay errors (24h)", value: "96", points: 10 },
      ],
    };
  const s = 2 + (hash(`${node}#${i}#score`) % 8);
  return {
    health: MlHealth.HEALTHY,
    score: s,
    reason: "",
    inputs: [
      { input: "SBE rate (24h)", value: `${(s / 10).toFixed(1)}/h`, points: Math.min(s, 4) },
      ...(s > 4 ? [{ input: "PCIe replays (24h)", value: `${s * 3}`, points: s - 4 }] : []),
    ],
  };
}

// ── The model ─────────────────────────────────────────────────────────────────

export interface DeviceModel {
  uuid: string;
  node: string;
  nodeId: string;
  pool: PoolDef;
  index: number;
  spec: Spec;
  serial: string;
  pci: string;
  health: DeviceHealth;
}

export interface NodeModel {
  name: string;
  id: string;
  pool: PoolDef;
  devices: DeviceModel[];
  health: MlHealth;
  score: number;
  reason: string;
  cordoned: boolean;
  incarnation: number;
  bootedDaysAgo: number;
  osImage: string;
  kernel: string;
  findingId?: string;
}

const deviceLabel = (node: string, i: number) => `${node} GPU ${i}`;

function buildNode(pool: PoolDef, name: string): NodeModel {
  const spec = specOf(pool);
  const id = `${pool.clusterId}/${name}`;
  const devices = Array.from({ length: pool.gpusPerNode }, (_, i): DeviceModel => {
    const uuid = gpuUuid(name, i);
    return {
      uuid,
      node: name,
      nodeId: id,
      pool,
      index: i,
      spec,
      serial: `${spec === SPECS.h100 ? "1654" : spec === SPECS.a100 ? "1323" : "1325"}${String(hash(uuid) % 1_000_000_000).padStart(9, "0")}`,
      pci: spec.pci[i],
      health: deviceHealth(name, i),
    };
  });
  const worst = devices.reduce((a, b) => (b.health.score > a.health.score ? b : a));
  const serve = isServe(pool);
  return {
    name,
    id,
    pool,
    devices,
    health: worst.health.health,
    score: worst.health.score,
    reason: worst.health.reason ? `GPU ${worst.index}: ${worst.health.reason}${name === BAD_NODE ? "; cordoned" : ""}` : "",
    cordoned: name === BAD_NODE,
    incarnation: name === BAD_NODE ? 3 : name === SPARE_NODE ? 2 : 1,
    bootedDaysAgo: name === BAD_NODE ? 14 : 18 + (hash(name) % 9),
    osImage: serve ? "amazon-eks-node-al2023-x86_64-nvidia-1.31-v20241121" : name === "gke-train-h100-w2jl" || name === "gke-train-h100-b6ve" ? "cos-113-18244-151-96" : "cos-113-18244-236-26",
    kernel: serve ? "6.1.115-126.197.amzn2023.x86_64" : name === "gke-train-h100-w2jl" || name === "gke-train-h100-b6ve" ? "6.1.100+" : "6.1.112+",
    findingId: worst.health.findingId,
  };
}

let MODEL: NodeModel[] | null = null;
/** Every node of every pool. */
export function fleetNodes(): NodeModel[] {
  MODEL ??= ALL_POOLS.flatMap((p) => p.nodes.map((n) => buildNode(p, n)));
  return MODEL;
}
export const nodeByName = (name: string) => fleetNodes().find((n) => n.name === name);
export const nodeById = (id: string) => fleetNodes().find((n) => n.id === id);
export const allDevices = () => fleetNodes().flatMap((n) => n.devices);
export const deviceByUuid = (uuid: string) => allDevices().find((d) => d.uuid === uuid);
export const poolById = (id: string) => ALL_POOLS.find((p) => p.id === id);

// ── Samples: every number comes from here ─────────────────────────────────────

export interface Sample {
  util: number;
  sm: number;
  tensor: number;
  dram: number;
  memGb: number;
  powerW: number;
  limitW: number;
  tempC: number;
  hbmC: number;
  smClk: number;
  hwSlow: number;
  nvlinkGBs: number;
  pcieGBs: number;
  sbe: number;
  remapPending: number;
  pcieReplay: number;
  nvlinkCrc: number;
}

const NAN_SAMPLE: Sample = {
  util: NaN,
  sm: NaN,
  tensor: NaN,
  dram: NaN,
  memGb: NaN,
  powerW: NaN,
  limitW: NaN,
  tempC: NaN,
  hbmC: NaN,
  smClk: NaN,
  hwSlow: NaN,
  nvlinkGBs: NaN,
  pcieGBs: NaN,
  sbe: NaN,
  remapPending: NaN,
  pcieReplay: NaN,
  nvlinkCrc: NaN,
};

/** The load (≈ SM active) and memory share a holder puts on a device at an offset. */
function loadOf(h: Holder | null, off: number, now: () => number): { load: number; mem: number } {
  if (!h) return { load: 0, mem: 0.008 };
  switch (h.profile) {
    case "train":
      return { load: h.subject.id.includes("/sft/3/") && off >= STRAGGLER_SINCE ? 0.64 : 0.75, mem: 0.9 };
    case "preempt":
      return { load: 0.7, mem: 0.8 };
    case "evaluator":
      return { load: 0.58, mem: 0.86 };
    case "gate":
      return off < (h.idleFrom ?? 1) ? { load: 0.68, mem: 0.78 } : { load: 0, mem: 0.78 };
    case "prod": {
      const hour = new Date(msAt(off, now)).getUTCHours() + new Date(msAt(off, now)).getUTCMinutes() / 60;
      return { load: 0.18 + 0.5 * (prodRps(hour) / 45), mem: 0.9 };
    }
    case "staging":
      return { load: 0.08, mem: 0.84 };
    case "reranker":
      return { load: 0.055, mem: 0.4 };
    case "notebook":
      return off < (h.idleFrom ?? 1) ? { load: 0.22, mem: 0.13 } : { load: 0, mem: 0.13 };
  }
}

/** A device at an offset from now (ms ≤ 0). `r` is the caller's seeded noise. */
export function sampleAt(d: DeviceModel, off: number, now: () => number, r: () => number = () => 0.5): Sample {
  if (d.node === BAD_NODE && d.index === BAD_GPU_INDEX && off >= XID_AT) return NAN_SAMPLE;
  const h = holderAt(d.node, d.index, off);
  const { load: base, mem } = loadOf(h, off, now);
  const n = noise(r);
  const load = Math.max(0, base * (1 + 0.05 * n));
  const sp = d.spec;
  const serve = isServe(d.pool);
  const hot = d.node === HOT_NODE && d.index === HOT_GPU_INDEX;
  const capped = POWER_CAPPED.has(`${d.node}#${d.index}`) && load > 0.5;
  const thermal = hot && off >= STRAGGLER_SINCE && inThermal(off);
  let tempC = 31 + 46 * load + 1.2 * n;
  let smClk = load > 0 ? sp.maxClk : 345;
  let powerW = sp.idleW + (sp.tdpW - sp.idleW) * Math.min(1, load * 1.22);
  let hbmC = tempC + 5;
  let hwSlow = 0;
  if (hot && h) {
    tempC = off >= STRAGGLER_SINCE ? (thermal ? 87.4 : 84.6) + 0.6 * n : 78 + n;
    hbmC = off >= STRAGGLER_SINCE ? (thermal ? 95 : 92.5) : 85;
    smClk = thermal ? 1290 + 60 * n : off >= STRAGGLER_SINCE ? 1755 : 1980;
    hwSlow = thermal ? 0.86 + 0.08 * n : off >= STRAGGLER_SINCE ? 0.04 : 0;
    powerW *= thermal ? 0.82 : 0.93;
  }
  if (capped) {
    smClk = 1830 + 20 * n;
    powerW = sp.tdpW - 2;
  }
  const xmitScale = h?.kind === "training" ? 380 : h?.kind === "eval" ? 95 : 0;
  const crcBase = d.node === NVLINK_NODE && (d.index === 1 || d.index === 3) ? 18_420 * Math.exp(off / (1.6 * DAY)) : (hash(d.uuid) % 7) * 1;
  return {
    util: load > 0 ? Math.min(0.995, 0.42 + load * 0.78) : 0,
    sm: serve ? NaN : load,
    tensor: serve ? NaN : load * (h?.kind === "training" ? 0.68 : 0.46),
    dram: serve ? NaN : load * (h?.kind === "inference" || h?.kind === "eval" ? 0.82 : 0.52),
    memGb: sp.memGb * mem * (1 + 0.004 * n),
    powerW,
    limitW: sp.tdpW,
    tempC,
    hbmC,
    smClk,
    hwSlow,
    nvlinkGBs: sp.nvlinks === 0 ? NaN : xmitScale * load * (1 + 0.08 * n),
    pcieGBs: load * (h?.kind === "inference" ? 2.1 : 6.4) * (1 + 0.1 * n),
    sbe: d.node === REMAP_NODE && d.index === REMAP_GPU_INDEX ? Math.round(412 + off / HOUR) : hot ? Math.max(0, Math.round(37 + (off / HOUR) * 1.4)) : 0,
    remapPending: d.node === REMAP_NODE && d.index === REMAP_GPU_INDEX && off >= -2 * DAY ? 1 : 0,
    pcieReplay: hash(d.uuid) % 13,
    nvlinkCrc: Math.round(crcBase),
  };
}

/** Busy share (DCGM GPU util) of a device now. */
export const busyNow = (d: DeviceModel, now: () => number) => sampleAt(d, 0, now).util;

// ── Series ────────────────────────────────────────────────────────────────────

export interface SeriesKeyDef {
  key: string;
  label: string;
  unit: string;
  read: (s: Sample) => number;
  /** A profiling field: not collected on the serving cluster. */
  profiling?: boolean;
  nvlink?: boolean;
}

export const DEVICE_SERIES: readonly SeriesKeyDef[] = [
  { key: "gpu.util", label: "GPU utilization", unit: "ratio", read: (s) => s.util },
  { key: "gpu.sm_active", label: "SM active", unit: "ratio", read: (s) => s.sm, profiling: true },
  { key: "gpu.tensor_active", label: "Tensor active", unit: "ratio", read: (s) => s.tensor, profiling: true },
  { key: "gpu.dram_active", label: "DRAM active", unit: "ratio", read: (s) => s.dram, profiling: true },
  { key: "gpu.mem_used_gb", label: "Memory used", unit: "GB", read: (s) => s.memGb },
  { key: "gpu.power_w", label: "Power", unit: "W", read: (s) => s.powerW },
  { key: "gpu.power_limit_w", label: "Power limit (enforced)", unit: "W", read: (s) => s.limitW },
  { key: "gpu.power_ratio", label: "Power / limit", unit: "ratio", read: (s) => s.powerW / s.limitW },
  { key: "gpu.temp_c", label: "GPU temperature", unit: "C", read: (s) => s.tempC },
  { key: "gpu.hbm_temp_c", label: "HBM temperature", unit: "C", read: (s) => s.hbmC },
  { key: "gpu.sm_clock_mhz", label: "SM clock", unit: "MHz", read: (s) => s.smClk },
  { key: "gpu.clock_event_hw_slowdown", label: "HW slowdown share", unit: "ratio", read: (s) => s.hwSlow },
  { key: "gpu.ecc_sbe", label: "ECC SBE (volatile)", unit: "count", read: (s) => s.sbe },
  { key: "gpu.row_remap_pending", label: "Row remap pending", unit: "count", read: (s) => s.remapPending },
  { key: "gpu.pcie_replay", label: "PCIe replays", unit: "count", read: (s) => s.pcieReplay },
  { key: "gpu.pcie_gbps", label: "PCIe throughput", unit: "GB/s", read: (s) => s.pcieGBs },
  { key: "gpu.nvlink_tx_gbps", label: "NVLink TX", unit: "GB/s", read: (s) => s.nvlinkGBs, nvlink: true },
  { key: "gpu.nvlink_crc", label: "NVLink CRC flit errors", unit: "count", read: (s) => s.nvlinkCrc, nvlink: true },
];

interface HostSample {
  cpu: number;
  mem: number;
  psiCpu: number;
  psiMem: number;
  psiIo: number;
  disk: number;
  netRx: number;
  netTx: number;
  ibX: number;
  ibR: number;
}

function hostAt(n: NodeModel, off: number, now: () => number, r: () => number): HostSample {
  const loads = n.devices.map((d) => sampleAt(d, off, now, r).util).filter(Number.isFinite);
  const load = loads.length ? loads.reduce((a, b) => a + b, 0) / loads.length : 0;
  const training = n.pool.purpose === "training" || n.pool.purpose === "spare";
  const ckpt = training && load > 0.3 && Math.abs((off / MINUTE) % 167) < 6;
  const z = noise(r);
  return {
    cpu: 0.04 + load * (training ? 0.38 : 0.24) + 0.02 * z,
    mem: 0.12 + load * 0.5 + 0.01 * z,
    psiCpu: 0.002 + load * 0.018 + 0.002 * z,
    psiMem: 0.0005,
    psiIo: ckpt ? 0.09 + 0.02 * z : 0.004 + 0.002 * z,
    disk: 0.41 + (off / (7 * DAY)) * 0.04,
    netRx: Math.max(0, load * (training ? (ckpt ? 18 : 4.2) : 1.4) + 0.2 * z),
    netTx: Math.max(0, load * (training ? (ckpt ? 22 : 3.1) : 1.9) + 0.2 * z),
    ibX: training ? Math.max(0, load * 142 * (1 + 0.06 * z)) : NaN,
    ibR: training ? load * 139 * (1 + 0.06 * z) : NaN,
  };
}

export const HOST_SERIES: readonly { key: string; label: string; unit: string; read: (h: HostSample) => number; ib?: boolean }[] = [
  { key: "host.cpu", label: "CPU", unit: "ratio", read: (h) => h.cpu },
  { key: "host.mem", label: "Memory", unit: "ratio", read: (h) => h.mem },
  { key: "host.psi_cpu", label: "PSI cpu (some)", unit: "ratio", read: (h) => h.psiCpu },
  { key: "host.psi_mem", label: "PSI memory (some)", unit: "ratio", read: (h) => h.psiMem },
  { key: "host.psi_io", label: "PSI io (some)", unit: "ratio", read: (h) => h.psiIo },
  { key: "host.disk", label: "Root filesystem used", unit: "ratio", read: (h) => h.disk },
  { key: "host.net_rx", label: "Network RX", unit: "GB/s", read: (h) => h.netRx / 8 },
  { key: "host.net_tx", label: "Network TX", unit: "GB/s", read: (h) => h.netTx / 8 },
  { key: "ib.xmit_gbps", label: "InfiniBand xmit (8 HCAs)", unit: "Gb/s", read: (h) => h.ibX * 8, ib: true },
  { key: "ib.rcv_gbps", label: "InfiniBand rcv (8 HCAs)", unit: "Gb/s", read: (h) => h.ibR * 8, ib: true },
];

interface Window {
  xs: number[];
  offs: number[];
  res: number;
}

function windowOf(req: { from?: { seconds: bigint }; to?: { seconds: bigint }; maxPoints?: number }, now: () => number, defaultPoints = 288): Window {
  const nowS = Math.floor(now() / 1000);
  const toS = req.to ? Math.min(nowS, Number(req.to.seconds)) : nowS;
  const fromS = req.from ? Number(req.from.seconds) : toS - 24 * 3600;
  const points = Math.max(12, Math.min(req.maxPoints || defaultPoints, 600));
  const res = Math.max(60, Math.ceil((toS - fromS) / points / 60) * 60);
  const xs: number[] = [];
  for (let x = toS - Math.floor((toS - fromS) / res) * res; x <= toS; x += res) xs.push(x);
  return { xs, offs: xs.map((x) => x * 1000 - now()), res };
}

const wantKey = (keys: readonly string[], key: string) => keys.length === 0 || keys.some((k) => k === key || (k.endsWith("*") && key.startsWith(k.replace(/\/?\*$/, ""))));

function deviceSeriesOne(d: DeviceModel, def: SeriesKeyDef, w: Window, now: () => number, group = ""): SeriesInit {
  const subject = subj(MlSubjectKind.DEVICE, d.uuid, deviceLabel(d.node, d.index));
  const common = { key: def.key, label: def.label, unit: def.unit, axis: MlAxis.TIME, group, resolution: w.res, subject };
  if (def.profiling && isServe(d.pool))
    return { ...common, x: [], y: [], collection: MlCollection.NOT_COLLECTED, missingSource: DCGM_PROFILING_MISSING, fixFindingId: FINDINGS.dcgmProfilingOff };
  if (def.nvlink && d.spec.nvlinks === 0) return { ...common, x: [], y: [], collection: MlCollection.NOT_APPLICABLE, missingSource: "The L4 has no NVLink (PCIe only)" };
  const r = rng(hash(`${d.uuid}|${def.key}`));
  const y = w.offs.map((off) => def.read(sampleAt(d, off, now, r)));
  if (y.every((v) => !Number.isFinite(v)))
    return { ...common, x: [], y: [], collection: MlCollection.NOT_COLLECTED, missingSource: d.node === BAD_NODE && d.index === BAD_GPU_INDEX ? XID_GONE : "no samples in the window" };
  return { ...common, x: w.xs, y, collection: MlCollection.COLLECTED };
}

/** One device's series for a key over the last `hours` (findings' evidence charts). */
export function deviceSeries(uuid: string, key: string, now: () => number, hours = 24): SeriesInit | undefined {
  const d = deviceByUuid(uuid);
  const def = DEVICE_SERIES.find((k) => k.key === key);
  if (!d || !def) return undefined;
  const w = windowOf({ from: { seconds: BigInt(Math.floor(now() / 1000) - hours * 3600) } }, now, Math.min(288, hours * 12));
  return deviceSeriesOne(d, def, w, now);
}

function quantile(sorted: number[], q: number): number {
  if (!sorted.length) return NaN;
  const pos = (sorted.length - 1) * q;
  const lo = Math.floor(pos);
  const hi = Math.ceil(pos);
  return sorted[lo] + (sorted[hi] - sorted[lo]) * (pos - lo);
}

/** p5/p50/p95 across `devices` for a key: three series, one stat each. */
function bandSeries(devices: DeviceModel[], def: SeriesKeyDef, w: Window, now: () => number, subject: Subj): SeriesInit[] {
  const common = { key: def.key, label: def.label, unit: def.unit, axis: MlAxis.TIME, resolution: w.res, subject };
  if (def.profiling && devices.every((d) => isServe(d.pool)))
    return [{ ...common, x: [], y: [], collection: MlCollection.NOT_COLLECTED, missingSource: DCGM_PROFILING_MISSING, fixFindingId: FINDINGS.dcgmProfilingOff }];
  const rs = devices.map((d) => rng(hash(`${d.uuid}|${def.key}|band`)));
  const cols = w.offs.map((off) =>
    devices
      .map((d, i) => def.read(sampleAt(d, off, now, rs[i])))
      .filter(Number.isFinite)
      .sort((a, b) => a - b),
  );
  return ["p5", "p50", "p95"].map((stat) => ({ ...common, group: stat, stat, x: w.xs, y: cols.map((c) => quantile(c, stat === "p5" ? 0.05 : stat === "p50" ? 0.5 : 0.95)), collection: MlCollection.COLLECTED }));
}

function sumSeries(devices: DeviceModel[], w: Window, key: string, label: string, read: (d: DeviceModel, off: number) => number, subject: Subj, group = "", unit = "count"): SeriesInit {
  return { key, label, unit, axis: MlAxis.TIME, group, resolution: w.res, subject, collection: MlCollection.COLLECTED, x: w.xs, y: w.offs.map((off) => devices.reduce((sum, d) => sum + (read(d, off) || 0), 0)) };
}

const allocatedAt = (d: DeviceModel, off: number) => (holderAt(d.node, d.index, off) ? 1 : 0);

/** Series for an aggregate (ORG, CLUSTER, POOL) of devices. */
function aggregateSeries(devices: DeviceModel[], keys: readonly string[], groupBy: string | undefined, w: Window, now: () => number, subject: Subj): SeriesInit[] {
  const out: SeriesInit[] = [];
  const busyAt = (d: DeviceModel, off: number) => {
    const s = sampleAt(d, off, now, rng(hash(`${d.uuid}|${Math.round(off / w.res / 1000)}`)));
    return holderAt(d.node, d.index, off) && Number.isFinite(s.util) ? s.util : 0;
  };
  if (keys.includes("fleet.allocated")) out.push(sumSeries(devices, w, "fleet.allocated", "Allocated", allocatedAt, subject));
  if (keys.includes("fleet.busy")) out.push(sumSeries(devices, w, "fleet.busy", "Busy", busyAt, subject));
  if (keys.includes("fleet.alloc_vs_busy")) {
    out.push(sumSeries(devices, w, "fleet.alloc_vs_busy", "Allocated GPUs", allocatedAt, subject, "allocated"));
    out.push(sumSeries(devices, w, "fleet.alloc_vs_busy", "Busy GPUs", busyAt, subject, "busy"));
  }
  if (keys.includes("fleet.idle_allocated"))
    out.push(sumSeries(devices, w, "fleet.idle_allocated", "Idle allocated", (d, off) => (allocatedAt(d, off) && busyAt(d, off) < 0.05 ? 1 : 0), subject));
  if (keys.includes("pool.busy")) {
    const pools = ALL_POOLS.filter((p) => devices.some((d) => d.pool.id === p.id));
    for (const p of pools) {
      const ds = devices.filter((d) => d.pool.id === p.id);
      out.push({
        ...sumSeries(ds, w, "pool.busy", "Busy", busyAt, subj(MlSubjectKind.POOL, p.id, p.name), groupBy === "pool" || !groupBy ? p.name : "", "ratio"),
        y: w.offs.map((off) => ds.reduce((s, d) => s + busyAt(d, off), 0) / ds.length),
      });
    }
  }
  if (keys.includes("pool.allocated_by_namespace")) {
    const namespaces = [...new Set(devices.flatMap((d) => holdersOf(d.node, d.index).map((h) => h.namespace)))].sort();
    for (const ns of namespaces) out.push(sumSeries(devices, w, "pool.allocated_by_namespace", "Allocated GPUs", (d, off) => (holderAt(d.node, d.index, off)?.namespace === ns ? 1 : 0), subject, ns));
  }
  for (const def of DEVICE_SERIES) if (keys.includes(def.key)) out.push(...bandSeries(devices, def, w, now, subject));
  return out;
}

// ── Fields: the latest of everything ──────────────────────────────────────────

type FieldInit = Init<typeof MlFieldValueSchema>;

const F = (group: MlFieldGroup, key: string, label: string, unit: string, sourceField: string, value: number, alarm = false, text?: string): FieldInit => ({
  key,
  label,
  group,
  unit,
  sourceField,
  value: text !== undefined ? NaN : value,
  text: text ?? "",
  alarm,
  collection: Number.isFinite(value) || text !== undefined ? MlCollection.COLLECTED : MlCollection.NOT_COLLECTED,
});

export function clockReasons(d: DeviceModel, s: Sample): string[] {
  if (!Number.isFinite(s.util)) return [];
  if (d.node === HOT_NODE && d.index === HOT_GPU_INDEX && s.hwSlow > 0.5) return ["HW slowdown", "HW thermal slowdown"];
  if (POWER_CAPPED.has(`${d.node}#${d.index}`) && s.util > 0.5) return ["SW power cap"];
  if (s.util === 0) return ["GPU idle"];
  return [];
}

/** Every DCGM field of a device, latest value, grouped (~58 rows). */
export function deviceFields(d: DeviceModel, now: () => number): FieldInit[] {
  const s = sampleAt(d, 0, now);
  const sp = d.spec;
  const gone = d.node === BAD_NODE && d.index === BAD_GPU_INDEX;
  const hot = d.node === HOT_NODE && d.index === HOT_GPU_INDEX;
  const remap = d.node === REMAP_NODE && d.index === REMAP_GPU_INDEX;
  const link = d.node === NVLINK_NODE && (d.index === 1 || d.index === 3);
  const h = hash(d.uuid);
  const G = MlFieldGroup;
  const reasons = clockReasons(d, s);
  const uptimeH = 24 * (d.node === BAD_NODE ? 14 : 20);
  const fields: FieldInit[] = [
    F(G.UTILIZATION, "gpu.util", "GPU utilization", "ratio", "DCGM_FI_DEV_GPU_UTIL", s.util),
    F(G.UTILIZATION, "gpu.mem_copy_util", "Memory copy utilization", "ratio", "DCGM_FI_DEV_MEM_COPY_UTIL", s.util * 0.41),
    F(G.UTILIZATION, "gpu.enc_util", "Encoder utilization", "ratio", "DCGM_FI_DEV_ENC_UTIL", gone ? NaN : 0),
    F(G.UTILIZATION, "gpu.dec_util", "Decoder utilization", "ratio", "DCGM_FI_DEV_DEC_UTIL", gone ? NaN : 0),
    F(G.PROFILING, "gpu.gr_engine_active", "Graphics engine active", "ratio", "DCGM_FI_PROF_GR_ENGINE_ACTIVE", s.sm * 1.12),
    F(G.PROFILING, "gpu.sm_active", "SM active", "ratio", "DCGM_FI_PROF_SM_ACTIVE", s.sm),
    F(G.PROFILING, "gpu.sm_occupancy", "SM occupancy", "ratio", "DCGM_FI_PROF_SM_OCCUPANCY", s.sm * 0.47),
    F(G.PROFILING, "gpu.tensor_active", "Tensor pipe active", "ratio", "DCGM_FI_PROF_PIPE_TENSOR_ACTIVE", s.tensor),
    F(G.PROFILING, "gpu.fp64_active", "FP64 pipe active", "ratio", "DCGM_FI_PROF_PIPE_FP64_ACTIVE", s.sm * 0.001),
    F(G.PROFILING, "gpu.fp32_active", "FP32 pipe active", "ratio", "DCGM_FI_PROF_PIPE_FP32_ACTIVE", s.sm * 0.11),
    F(G.PROFILING, "gpu.fp16_active", "FP16 pipe active", "ratio", "DCGM_FI_PROF_PIPE_FP16_ACTIVE", s.sm * 0.06),
    F(G.PROFILING, "gpu.dram_active", "DRAM active", "ratio", "DCGM_FI_PROF_DRAM_ACTIVE", s.dram),
    F(G.PROFILING, "gpu.pcie_tx_bytes", "PCIe TX", "GB/s", "DCGM_FI_PROF_PCIE_TX_BYTES", s.pcieGBs * 0.46),
    F(G.PROFILING, "gpu.pcie_rx_bytes", "PCIe RX", "GB/s", "DCGM_FI_PROF_PCIE_RX_BYTES", s.pcieGBs * 0.54),
    F(G.PROFILING, "gpu.nvlink_tx_bytes", "NVLink TX", "GB/s", "DCGM_FI_PROF_NVLINK_TX_BYTES", s.nvlinkGBs),
    F(G.PROFILING, "gpu.nvlink_rx_bytes", "NVLink RX", "GB/s", "DCGM_FI_PROF_NVLINK_RX_BYTES", s.nvlinkGBs * 0.98),
    F(G.MEMORY, "gpu.fb_used", "Framebuffer used", "GB", "DCGM_FI_DEV_FB_USED", s.memGb),
    F(G.MEMORY, "gpu.fb_free", "Framebuffer free", "GB", "DCGM_FI_DEV_FB_FREE", sp.memGb - s.memGb),
    F(G.MEMORY, "gpu.fb_total", "Framebuffer total", "GB", "DCGM_FI_DEV_FB_TOTAL", gone ? NaN : sp.memGb),
    F(G.MEMORY, "gpu.bar1_used", "BAR1 used", "GB", "DCGM_FI_DEV_BAR1_USED", gone ? NaN : s.util > 0 ? sp.bar1Gb * 0.03 : 0.002),
    F(G.MEMORY, "gpu.bar1_total", "BAR1 total", "GB", "DCGM_FI_DEV_BAR1_TOTAL", gone ? NaN : sp.bar1Gb),
    F(G.POWER, "gpu.power", "Power usage", "W", "DCGM_FI_DEV_POWER_USAGE", s.powerW, Number.isFinite(s.powerW) && s.powerW >= sp.tdpW - 5),
    F(G.POWER, "gpu.power_limit", "Power limit (enforced)", "W", "DCGM_FI_DEV_ENFORCED_POWER_LIMIT", s.limitW),
    F(G.POWER, "gpu.energy", "Energy since driver load", "kWh", "DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION", gone ? NaN : ((s.powerW + sp.idleW) / 2) * uptimeH * 0.001),
    F(G.POWER, "gpu.power_violation", "Power violation time (24h)", "s", "DCGM_FI_DEV_POWER_VIOLATION", gone ? NaN : POWER_CAPPED.has(`${d.node}#${d.index}`) ? 3_140 : 0),
    F(G.POWER, "gpu.thermal_violation", "Thermal violation time (24h)", "s", "DCGM_FI_DEV_THERMAL_VIOLATION", gone ? NaN : hot ? 20_520 : 0, hot),
    F(G.POWER, "gpu.sync_boost_violation", "Sync boost violation time (24h)", "s", "DCGM_FI_DEV_SYNC_BOOST_VIOLATION", gone ? NaN : 0),
    F(G.THERMAL, "gpu.temp", "GPU temperature", "C", "DCGM_FI_DEV_GPU_TEMP", s.tempC, s.tempC >= sp.slowdownC - 1),
    F(G.THERMAL, "gpu.memory_temp", "Memory (HBM) temperature", "C", "DCGM_FI_DEV_MEMORY_TEMP", s.hbmC, s.hbmC >= 92),
    F(G.THERMAL, "gpu.slowdown_temp", "Slowdown temperature", "C", "DCGM_FI_DEV_SLOWDOWN_TEMP", gone ? NaN : sp.slowdownC),
    F(G.THERMAL, "gpu.shutdown_temp", "Shutdown temperature", "C", "DCGM_FI_DEV_SHUTDOWN_TEMP", gone ? NaN : sp.shutdownC),
    F(G.CLOCKS, "gpu.sm_clock", "SM clock", "MHz", "DCGM_FI_DEV_SM_CLOCK", s.smClk, hot),
    F(G.CLOCKS, "gpu.mem_clock", "Memory clock", "MHz", "DCGM_FI_DEV_MEM_CLOCK", gone ? NaN : sp.memClk),
    F(G.CLOCKS, "gpu.max_sm_clock", "Max SM clock", "MHz", "DCGM_FI_DEV_MAX_SM_CLOCK", gone ? NaN : sp.maxClk),
    gone
      ? F(G.CLOCKS, "gpu.clock_event_reasons", "Clock-event reasons", "", "DCGM_FI_DEV_CLOCKS_EVENT_REASONS", NaN)
      : F(G.CLOCKS, "gpu.clock_event_reasons", "Clock-event reasons", "", "DCGM_FI_DEV_CLOCKS_EVENT_REASONS", NaN, hot, reasons.length ? reasons.join(", ") : "none"),
    F(G.ECC, "gpu.ecc.sbe_volatile", "SBE (volatile)", "count", "DCGM_FI_DEV_ECC_SBE_VOL_TOTAL", gone ? NaN : s.sbe, remap || hot),
    F(G.ECC, "gpu.ecc.sbe_aggregate", "SBE (aggregate)", "count", "DCGM_FI_DEV_ECC_SBE_AGG_TOTAL", gone ? NaN : remap ? 1_904 : hot ? 211 : h % 41),
    F(G.ECC, "gpu.ecc.dbe_volatile", "DBE (volatile)", "count", "DCGM_FI_DEV_ECC_DBE_VOL_TOTAL", gone ? NaN : 0),
    F(G.ECC, "gpu.ecc.dbe_aggregate", "DBE (aggregate)", "count", "DCGM_FI_DEV_ECC_DBE_AGG_TOTAL", gone ? NaN : 0),
    ...(["sbe", "dbe", "pending"] as const).map(
      (k): FieldInit => ({
        key: `gpu.retired.${k}`,
        label: `Retired pages (${k === "pending" ? "pending" : k.toUpperCase()})`,
        group: G.ECC,
        unit: "count",
        sourceField: `DCGM_FI_DEV_RETIRED_${k.toUpperCase()}`,
        value: NaN,
        collection: MlCollection.NOT_APPLICABLE,
        missingSource: "Ampere and later remap rows instead of retiring pages",
      }),
    ),
    F(G.ROW_REMAP, "gpu.row_remap.correctable", "Correctable remapped rows", "count", "DCGM_FI_DEV_CORRECTABLE_REMAPPED_ROWS", gone ? NaN : remap ? 3 : h % 2),
    F(G.ROW_REMAP, "gpu.row_remap.uncorrectable", "Uncorrectable remapped rows", "count", "DCGM_FI_DEV_UNCORRECTABLE_REMAPPED_ROWS", gone ? NaN : 0),
    F(G.ROW_REMAP, "gpu.row_remap.pending", "Row remap pending", "count", "DCGM_FI_DEV_ROW_REMAP_PENDING", gone ? NaN : s.remapPending, remap),
    F(G.ROW_REMAP, "gpu.row_remap.failure", "Row remap failure", "count", "DCGM_FI_DEV_ROW_REMAP_FAILURE", gone ? NaN : 0),
    F(G.PCIE, "gpu.pcie.link_gen", "PCIe link generation", "", "DCGM_FI_DEV_PCIE_LINK_GEN", gone ? NaN : sp.pcieGen),
    F(G.PCIE, "gpu.pcie.link_width", "PCIe link width", "", "DCGM_FI_DEV_PCIE_LINK_WIDTH", gone ? NaN : 16),
    F(G.PCIE, "gpu.pcie.replay", "PCIe replay counter", "count", "DCGM_FI_DEV_PCIE_REPLAY_COUNTER", s.pcieReplay),
    F(G.PCIE, "gpu.pcie.replay_rollover", "PCIe replay rollover", "count", "DCGM_FI_DEV_PCIE_REPLAY_ROLLOVER_COUNTER", gone ? NaN : 0),
  ];
  if (sp.nvlinks === 0) {
    fields.push({ key: "gpu.nvlink.bandwidth", label: "NVLink bandwidth", group: G.NVLINK, unit: "GB/s", sourceField: "DCGM_FI_DEV_NVLINK_BANDWIDTH_TOTAL", value: NaN, collection: MlCollection.NOT_APPLICABLE, missingSource: "The L4 has no NVLink (PCIe only)" });
  } else {
    fields.push(
      F(G.NVLINK, "gpu.nvlink.bandwidth", "NVLink bandwidth (total)", "GB/s", "DCGM_FI_DEV_NVLINK_BANDWIDTH_TOTAL", s.nvlinkGBs * 1.98),
      F(G.NVLINK, "gpu.nvlink.links_up", "Links up", "", "DCGM_FI_DEV_NVLINK_LINK_STATUS", gone ? 0 : sp.nvlinks, gone),
      F(G.NVLINK, "gpu.nvlink.crc_flit", "CRC flit errors", "count", "DCGM_FI_DEV_NVLINK_CRC_FLIT_ERROR_COUNT_TOTAL", gone ? NaN : s.nvlinkCrc, link),
      F(G.NVLINK, "gpu.nvlink.crc_data", "CRC data errors", "count", "DCGM_FI_DEV_NVLINK_CRC_DATA_ERROR_COUNT_TOTAL", gone ? NaN : link ? 212 : 0, link),
      F(G.NVLINK, "gpu.nvlink.replay", "Replay errors", "count", "DCGM_FI_DEV_NVLINK_REPLAY_ERROR_COUNT_TOTAL", gone ? NaN : link ? 96 : 0, link),
      F(G.NVLINK, "gpu.nvlink.recovery", "Recovery errors", "count", "DCGM_FI_DEV_NVLINK_RECOVERY_ERROR_COUNT_TOTAL", gone ? NaN : link ? 4 : 0, link),
    );
  }
  fields.push(
    F(G.XID, "gpu.xid.last", "Last Xid", "", "DCGM_FI_DEV_XID_ERRORS", gone ? 79 : xidLast(d), gone),
    F(G.XID, "gpu.xid.count_7d", "Xids (7d)", "count", "DCGM_EXP_XID_ERRORS_TOTAL", gone ? 1 : xidLast(d) ? 1 : 0, gone),
  );
  if (isServe(d.pool)) {
    for (const f of fields) {
      if (f.group !== G.PROFILING) continue;
      f.value = NaN;
      f.collection = MlCollection.NOT_COLLECTED;
      f.missingSource = DCGM_PROFILING_MISSING;
      f.fixFindingId = FINDINGS.dcgmProfilingOff;
    }
  }
  if (gone) {
    for (const f of fields) {
      if (f.group === G.XID || f.collection !== MlCollection.NOT_COLLECTED) continue;
      f.missingSource = XID_GONE;
      f.fixFindingId = FINDINGS.xid79;
    }
  }
  return fields;
}

/** The one benign app Xid in the fleet: Xid 13 on a prod L4. */
const xidLast = (d: DeviceModel) => (d.node === "ip-10-40-1-37" && d.index === 3 ? 13 : 0);

// ── Device and node messages ──────────────────────────────────────────────────

type DeviceInit = Init<typeof MlDeviceSchema>;

export function deviceMessage(d: DeviceModel, now: () => number, full: boolean): DeviceInit {
  const s = sampleAt(d, 0, now);
  const h = holderAt(d.node, d.index, 0);
  return {
    uuid: d.uuid,
    nodeId: d.nodeId,
    index: d.index,
    product: d.pool.product,
    serial: d.serial,
    pciBusId: d.pci,
    vbios: d.spec.vbios,
    migMode: "disabled",
    health: d.health.health,
    healthScore: d.health.score,
    healthReason: d.health.reason,
    busyRatio: s.util,
    smActive: s.sm,
    memoryUsedGb: s.memGb,
    memoryTotalGb: d.spec.memGb,
    powerW: s.powerW,
    powerLimitW: s.limitW,
    temperatureC: s.tempC,
    memoryTemperatureC: s.hbmC,
    clockEventReasons: clockReasons(d, s),
    xidLast: d.node === BAD_NODE && d.index === BAD_GPU_INDEX ? 79 : xidLast(d),
    xids24h: xidLast(d) ? 1 : 0,
    ...(h ? { allocatedTo: h.subject } : {}),
    ...(full ? { fields: deviceFields(d, now), scoreInputs: d.health.inputs } : { scoreInputs: d.health.inputs }),
  };
}

function conditions(n: NodeModel, now: () => number): Init<typeof MlNodeConditionSchema>[] {
  const since = at(-n.bootedDaysAgo * DAY, now);
  const c = (type: string, status: string, bad: boolean, reason: string, message: string, source: string, sinceTs = since): Init<typeof MlNodeConditionSchema> => ({ type, status, bad, reason, message, since: sinceTs, source });
  const serve = isServe(n.pool);
  const out = [
    c("Ready", "True", false, "KubeletReady", "kubelet is posting ready status", "kubelet"),
    c("MemoryPressure", "False", false, "KubeletHasSufficientMemory", "kubelet has sufficient memory available", "kubelet"),
    c("DiskPressure", "False", false, "KubeletHasNoDiskPressure", "kubelet has no disk pressure", "kubelet"),
    c("PIDPressure", "False", false, "KubeletHasSufficientPID", "kubelet has sufficient PID available", "kubelet"),
    c("NetworkUnavailable", "False", false, serve ? "VpcCniReady" : "RouteCreated", serve ? "aws-node (VPC CNI) is ready" : "NodeController create implicit route", "kubelet"),
    c("KernelDeadlock", "False", false, "KernelHasNoDeadlock", "kernel has no deadlock", "npd"),
    c("ReadonlyFilesystem", "False", false, "FilesystemIsNotReadOnly", "Filesystem is not read-only", "npd"),
    c(
      "FrequentKubeletRestart",
      "False",
      false,
      "NoFrequentKubeletRestart",
      n.name === "ip-10-40-1-37" ? "kubelet restarted once in 24h (threshold: 5 in 20m)" : "kubelet is functioning properly",
      "npd",
    ),
    c("FrequentContainerdRestart", "False", false, "NoFrequentContainerdRestart", "containerd is functioning properly", "npd"),
    c("CorruptDockerOverlay2", "False", false, "NoCorruptDockerOverlay2", "docker overlay2 is functioning properly", "npd"),
  ];
  if (!serve) {
    out.push(
      n.name === BAD_NODE
        ? c("GPUXidError", "True", true, "XidCriticalError", `Xid 79 on GPU ${BAD_GPU_INDEX} (PCI:0000:ab:00): GPU has fallen off the bus`, "npd", at(XID_AT + 30_000, now))
        : c("GPUXidError", "False", false, "NoXidCriticalError", "no critical Xid in the kernel log", "npd"),
      n.name === HOT_NODE
        ? c("GPUThermalThrottle", "True", true, "HwThermalSlowdown", `GPU ${HOT_GPU_INDEX}: HW thermal slowdown active (DCGM_FI_DEV_CLOCKS_EVENT_REASONS 0x48)`, "npd", at(STRAGGLER_SINCE, now))
        : c("GPUThermalThrottle", "False", false, "NoThermalThrottle", "no GPU in HW thermal slowdown", "npd"),
      n.name === NVLINK_NODE
        ? c("NVLinkError", "True", true, "NVLinkCRCErrorsRising", "GPU 1 ↔ GPU 3: CRC flit errors 18,420 (×4 in 24h)", "npd", at(-30 * HOUR, now))
        : c("NVLinkError", "False", false, "NVLinkHealthy", "all NVLinks up, error rate below threshold", "npd"),
      n.name === REMAP_NODE
        ? c("GPURowRemapPending", "True", true, "RowRemapPending", `GPU ${REMAP_GPU_INDEX}: a row remap is pending; reset the GPU to apply it`, "npd", at(-2 * DAY, now))
        : c("GPURowRemapPending", "False", false, "NoRowRemapPending", "no pending row remaps", "npd"),
    );
  }
  return out;
}

export function nodeMessage(n: NodeModel, now: () => number, full: boolean): Init<typeof MlNodeSchema> {
  const devices = n.devices.map((d) => deviceMessage(d, now, full));
  const allocated = n.devices.filter((d) => holderAt(d.node, d.index, 0)).length;
  const busies = n.devices.filter((d) => holderAt(d.node, d.index, 0)).map((d) => busyNow(d, now));
  const serve = isServe(n.pool);
  return {
    id: n.id,
    name: n.name,
    clusterId: n.pool.clusterId,
    poolId: n.pool.id,
    providerId: serve ? `aws:///${n.pool.zone}/i-0${(hash(n.name) >>> 0).toString(16).padStart(8, "0")}a1b2c3d4` : `gce://acme-ml-prod/${n.pool.zone}/${n.name}`,
    instanceType: n.pool.instanceType,
    zone: n.pool.zone,
    incarnation: n.incarnation,
    healthState: n.health,
    healthScore: n.score,
    healthReason: n.reason,
    cordoned: n.cordoned,
    gpus: n.devices.length,
    gpusAllocatable: n.name === BAD_NODE ? 7 : n.devices.length,
    gpusAllocated: allocated,
    busyRatio: busies.length ? busies.reduce((a, b) => a + b, 0) / busies.length : 0,
    driverVersion: n.pool.driver,
    cudaVersion: n.pool.cuda,
    kernelVersion: n.kernel,
    osImage: n.osImage,
    bootedAt: at(-n.bootedDaysAgo * DAY, now),
    devices,
    conditions: conditions(n, now),
    sharing: "none",
    nvlinkDomain: n.devices[0].spec.nvlinks ? `${n.name}/nvswitch (4× NVSwitch, ${n.devices[0].spec === SPECS.h100 ? "NVLink 4" : "NVLink 3"})` : "",
  };
}

function workloadsOf(devices: DeviceModel[], now: () => number): Init<typeof MlNodeWorkloadSchema>[] {
  const byKey = new Map<string, { h: Holder; idx: number[] }>();
  for (const d of devices) {
    for (const h of holdersOf(d.node, d.index)) {
      if (h.to !== null && h.to < -7 * DAY) continue;
      const key = `${h.subject.id}|${h.pod}|${h.from}`;
      const e = byKey.get(key) ?? { h, idx: [] };
      e.idx.push(d.index);
      byKey.set(key, e);
    }
  }
  return [...byKey.values()]
    .sort((a, b) => a.h.from - b.h.from)
    .map(({ h, idx }) => ({
      subject: h.subject,
      pod: `${h.namespace}/${h.pod}`,
      kind: h.kind,
      deviceIndexes: idx,
      start: at(Math.max(h.from, -7 * DAY), now),
      ...(h.to !== null ? { end: at(h.to, now) } : {}),
    }));
}

/** The node's NVLink pairs (every GPU pair through the NVSwitch fabric). */
function nvlinkPairs(n: NodeModel) {
  const sp = n.devices[0].spec;
  if (!sp.nvlinks) return [];
  const out = [];
  for (let a = 0; a < n.devices.length; a++)
    for (let b = a + 1; b < n.devices.length; b++) {
      const gone = n.name === BAD_NODE && (a === BAD_GPU_INDEX || b === BAD_GPU_INDEX);
      const degraded = n.name === NVLINK_NODE && a === 1 && b === 3;
      out.push({
        fromIndex: a,
        toIndex: b,
        links: sp.nvlinks,
        state: gone ? "down" : degraded ? "degraded" : "up",
        bandwidthGbps: gone ? 0 : degraded ? sp.nvlinkGBs * 0.71 : sp.nvlinkGBs,
        crcErrors: BigInt(degraded ? 18_420 : gone ? 0 : hash(`${n.name}${a}${b}`) % 4),
        replayErrors: BigInt(degraded ? 96 : 0),
      });
    }
  return out;
}

function ibPorts(n: NodeModel, now: () => number) {
  if (n.devices[0].spec !== SPECS.h100) return [];
  const host = hostAt(n, 0, now, rng(hash(n.name)));
  return Array.from({ length: 8 }, (_, i) => {
    const r = rng(hash(`${n.name}ib${i}`));
    return {
      hca: `mlx5_${i}`,
      port: 1,
      state: "ACTIVE",
      rateGbps: 400,
      xmitGbps: Number.isFinite(host.ibX) ? host.ibX * (1 + 0.05 * noise(r)) : 0,
      rcvGbps: Number.isFinite(host.ibR) ? host.ibR * (1 + 0.05 * noise(r)) : 0,
      symbolErrors: BigInt(n.name === BAD_NODE && i === BAD_GPU_INDEX ? 37 : hash(`${n.name}sym${i}`) % 3),
      linkDowned: BigInt(n.name === BAD_NODE && i === BAD_GPU_INDEX ? 1 : 0),
    };
  });
}

type HostField = Init<typeof MlFieldValueSchema>;
function hostResources(n: NodeModel, now: () => number) {
  const s = hostAt(n, 0, now, rng(hash(`${n.name}host`)));
  const G = MlFieldGroup;
  const training = n.pool.purpose === "training" || n.pool.purpose === "spare";
  const serve = isServe(n.pool);
  const memGb = n.devices[0].spec === SPECS.h100 ? 1872 : n.devices[0].spec === SPECS.a100 ? 680 : 192;
  const cpus = n.devices[0].spec === SPECS.h100 ? 208 : n.devices[0].spec === SPECS.a100 ? 96 : 48;
  const shmTotal = training ? 512 : 64;
  const shmUsed = training && n.name !== BAD_NODE ? 41.6 : 0.2;
  const f = (group: MlFieldGroup, key: string, label: string, unit: string, sourceField: string, value: number, alarm = false): HostField => ({ key, label, group, unit, sourceField, value, alarm, collection: MlCollection.COLLECTED });
  const fields: HostField[] = [
    f(G.HOST_CPU, "host.cpu", "CPU busy", "ratio", 'node_cpu_seconds_total{mode!="idle"}', s.cpu),
    f(G.HOST_CPU, "host.cpus", "CPUs", "", "machine_cpu_cores", cpus),
    f(G.HOST_CPU, "host.load1", "Load (1m)", "", "node_load1", s.cpu * cpus * 1.1),
    f(G.HOST_CPU, "host.iowait", "iowait", "ratio", 'node_cpu_seconds_total{mode="iowait"}', s.psiIo * 0.6),
    f(G.HOST_CPU, "host.psi_cpu", "PSI cpu (some)", "ratio", "node_pressure_cpu_waiting_seconds_total", s.psiCpu),
    f(G.HOST_MEMORY, "host.mem", "Memory used", "ratio", "node_memory_MemAvailable_bytes", s.mem),
    f(G.HOST_MEMORY, "host.mem_total", "Memory total", "GB", "node_memory_MemTotal_bytes", memGb),
    f(G.HOST_MEMORY, "host.psi_memory", "PSI memory (some)", "ratio", "node_pressure_memory_stalled_seconds_total", s.psiMem),
    f(G.HOST_MEMORY, "host.hugepages_free", "HugePages free", "count", "node_memory_HugePages_Free", 0),
    f(G.HOST_MEMORY, "host.oom_kills", "OOM kills (24h)", "count", "node_vmstat_oom_kill", 0),
    f(G.HOST_MEMORY, "host.shm_used", "/dev/shm used", "GB", 'node_filesystem_avail_bytes{mountpoint="/dev/shm"}', shmUsed),
    f(G.HOST_DISK, "host.root_fs", "Root filesystem used", "ratio", 'node_filesystem_avail_bytes{mountpoint="/"}', s.disk),
    f(G.HOST_DISK, "host.image_fs", "Image filesystem used", "ratio", 'node_filesystem_avail_bytes{mountpoint="/var/lib/containerd"}', serve ? 0.71 : 0.38),
    f(G.HOST_DISK, "host.scratch_fs", "Local NVMe scratch used", "ratio", 'node_filesystem_avail_bytes{mountpoint="/mnt/stateful_partition/kube-ephemeral-ssd"}', training ? 0.52 : 0.08),
    f(G.HOST_DISK, "host.disk_read", "Disk read", "GB/s", "node_disk_read_bytes_total", training ? 0.9 : 0.05),
    f(G.HOST_DISK, "host.disk_write", "Disk write", "GB/s", "node_disk_written_bytes_total", training ? 2.6 : 0.03),
    f(G.HOST_DISK, "host.psi_io", "PSI io (some)", "ratio", "node_pressure_io_stalled_seconds_total", s.psiIo),
    f(G.HOST_NETWORK, "host.net_rx", "Network RX", "GB/s", "node_network_receive_bytes_total", s.netRx / 8),
    f(G.HOST_NETWORK, "host.net_tx", "Network TX", "GB/s", "node_network_transmit_bytes_total", s.netTx / 8),
    f(G.HOST_NETWORK, "host.net_errs", "Receive errors (24h)", "count", "node_network_receive_errs_total", 0),
    f(G.HOST_NETWORK, "host.net_drop", "Receive drops (24h)", "count", "node_network_receive_drop_total", serve ? 14 : 2),
    f(G.HOST_NETWORK, "host.timex_offset", "Clock offset", "ms", "node_timex_offset_seconds", 0.21 + (hash(n.name) % 5) * 0.03),
    f(G.KUBELET, "kubelet.running_pods", "Running pods", "count", "kubelet_running_pods", training ? 14 : 19),
    f(G.KUBELET, "kubelet.pleg_relist_p99", "PLEG relist p99", "s", "kubelet_pleg_relist_duration_seconds", 0.042),
    f(G.KUBELET, "kubelet.runtime_errors", "CRI operation errors (24h)", "count", "kubelet_runtime_operations_errors_total", n.name === BAD_NODE ? 12 : 0, n.name === BAD_NODE),
    f(G.KUBELET, "kubelet.evictions", "Evictions (24h)", "count", "kubelet_evictions", 0),
    f(G.KUBELET, "kubelet.container_errors", "Container start errors (24h)", "count", "kubelet_started_containers_errors_total", 0),
    f(G.KUBELET, "kubelet.gpu_capacity", "nvidia.com/gpu capacity", "", "node.status.capacity", n.devices.length),
    f(G.KUBELET, "kubelet.gpu_allocatable", "nvidia.com/gpu allocatable", "", "node.status.allocatable", n.name === BAD_NODE ? 7 : n.devices.length, n.name === BAD_NODE),
  ];
  if (n.devices[0].spec === SPECS.h100) {
    fields.push(
      f(G.INFINIBAND, "ib.xmit", "Xmit (8 HCAs)", "Gb/s", "node_infiniband_port_data_transmitted_bytes_total", s.ibX * 8),
      f(G.INFINIBAND, "ib.rcv", "Rcv (8 HCAs)", "Gb/s", "node_infiniband_port_data_received_bytes_total", s.ibR * 8),
      f(G.INFINIBAND, "ib.symbol_errors", "Symbol errors (24h)", "count", "node_infiniband_symbol_error_total", n.name === BAD_NODE ? 37 : 1),
      f(G.INFINIBAND, "ib.link_downed", "Link downed (24h)", "count", "node_infiniband_link_downed_total", n.name === BAD_NODE ? 1 : 0, n.name === BAD_NODE),
      f(G.INFINIBAND, "ib.xmit_wait", "Xmit wait", "count", "node_infiniband_port_transmit_wait_total", training ? 184_220 : 0),
      f(G.INFINIBAND, "ib.link_error_recovery", "Link error recovery (24h)", "count", "node_infiniband_link_error_recovery_total", 0),
    );
  }
  return {
    cpuRatio: s.cpu,
    memoryRatio: s.mem,
    psiCpu: s.psiCpu,
    psiMemory: s.psiMem,
    psiIo: s.psiIo,
    diskRatio: s.disk,
    imageFsRatio: serve ? 0.71 : 0.38,
    shmUsedGb: shmUsed,
    shmTotalGb: shmTotal,
    kubeletRestarts24h: n.name === "ip-10-40-1-37" ? 1 : 0,
    containerdRestarts24h: 0,
    fields,
    npdMonitors: serve
      ? ["kernel-monitor", "docker-monitor", "systemd-monitor", "health-checker-kubelet"]
      : ["kernel-monitor", "docker-monitor", "systemd-monitor", "health-checker-kubelet", "gpu-xid-monitor", "custom-plugin-monitor (gpu-health: thermal, nvlink, row-remap)"],
  };
}

function diagnostics(n: NodeModel, now: () => number) {
  const weekly = (d: number) => ({ at: at(-d * DAY - (hash(n.name) % 5) * HOUR, now), level: 3, result: "pass", detail: `dcgmi diag -r 3: ${n.devices.length} of ${n.devices.length} GPUs passed (targeted stress, targeted power, memory bandwidth)` });
  if (n.name === BAD_NODE)
    return [
      { at: at(XID_AT + 40 * MINUTE, now), level: 3, result: "fail", detail: `dcgmi diag -r 3: GPU ${BAD_GPU_INDEX} not found (Xid 79); 7 of 8 GPUs passed` },
      weekly(6),
      { at: at(-14 * DAY + 2 * HOUR, now), level: 1, result: "pass", detail: "dcgmi diag -r 1 at node admission after re-image: deployment and software checks passed" },
    ];
  if (n.name === HOT_NODE) return [{ at: at(-6 * DAY, now), level: 3, result: "warn", detail: `dcgmi diag -r 3: GPU ${HOT_GPU_INDEX} targeted power: thermal margin 2°C under load` }, weekly(13)];
  if (n.name === REMAP_NODE) return [{ at: at(-2 * DAY + HOUR, now), level: 2, result: "warn", detail: `dcgmi diag -r 2: GPU ${REMAP_GPU_INDEX} memory: row remap pending, reset required` }, weekly(6)];
  return [weekly(6), weekly(13)];
}

function incarnations(n: NodeModel, now: () => number) {
  if (n.name === BAD_NODE)
    return [
      { incarnation: 3, from: at(-14 * DAY, now), note: "Re-imaged by node auto-upgrade to cos-113-18244-236-26" },
      { incarnation: 2, from: at(-40 * DAY, now), to: at(-14 * DAY, now), note: `GPU ${BAD_GPU_INDEX} board swapped in from ${SPARE_NODE} after an RMA of the original board (Xid 79 then too)` },
      { incarnation: 1, from: at(-120 * DAY, now), to: at(-40 * DAY, now), note: "Provisioned with the h100-train pool" },
    ];
  if (n.name === SPARE_NODE)
    return [
      { incarnation: 2, from: at(-40 * DAY, now), note: "Re-imaged after lending its GPU 2 board to gke-train-h100-r4vb; new board fitted" },
      { incarnation: 1, from: at(-120 * DAY, now), to: at(-40 * DAY, now), note: "Provisioned as the hot spare" },
    ];
  return [{ incarnation: 1, from: at(-n.bootedDaysAgo * DAY, now), note: `Provisioned with the ${n.pool.name} pool` }];
}

// ── Pools and the fleet ───────────────────────────────────────────────────────

const capacityOf = (p: PoolDef) => (p.capacity === "reserved" ? MlCapacityType.RESERVED : p.capacity === "spot" ? MlCapacityType.SPOT : MlCapacityType.ON_DEMAND);

export interface PoolStats {
  pool: PoolDef;
  nodes: number;
  gpus: number;
  allocated: number;
  idleAllocated: number;
  faulty: number;
  throttled: number;
  busy: number;
  smP50: number;
  nodesUnhealthy: number;
  usdPerDay: number;
}

export function poolStats(p: PoolDef, now: () => number): PoolStats {
  const nodes = fleetNodes().filter((n) => n.pool.id === p.id);
  const devices = nodes.flatMap((n) => n.devices);
  const held = devices.filter((d) => holderAt(d.node, d.index, 0));
  const samples = held.map((d) => sampleAt(d, 0, now));
  const sm = samples
    .map((s) => s.sm)
    .filter(Number.isFinite)
    .sort((a, b) => a - b);
  return {
    pool: p,
    nodes: nodes.length,
    gpus: devices.length,
    allocated: held.length,
    idleAllocated: samples.filter((s) => s.util < 0.05).length,
    faulty: devices.filter((d) => d.health.health === MlHealth.FAULTY).length,
    throttled: devices.filter((d) => clockReasons(d, sampleAt(d, 0, now)).some((r) => r !== "GPU idle")).length,
    busy: samples.length ? samples.reduce((a, s) => a + s.util, 0) / samples.length : 0,
    smP50: sm.length ? quantile(sm, 0.5) : NaN,
    nodesUnhealthy: nodes.filter((n) => n.health !== MlHealth.HEALTHY).length,
    usdPerDay: devices.length * p.usdPerGpuHour * 24,
  };
}

function poolMessage(s: PoolStats): Init<typeof MlPoolSchema> {
  return {
    id: s.pool.id,
    clusterId: s.pool.clusterId,
    name: s.pool.name,
    vendor: "nvidia",
    product: s.pool.product,
    memoryGb: s.pool.memoryGb,
    capacityType: capacityOf(s.pool),
    nodeCount: s.nodes,
    gpusTotal: s.gpus,
    gpusAllocated: s.allocated,
    gpusIdleAllocated: s.idleAllocated,
    gpusFaulty: s.faulty,
    smActiveP50: s.smP50,
    busyRatio: s.busy,
    nodesUnhealthy: s.nodesUnhealthy,
    usdPerGpuHour: s.pool.usdPerGpuHour,
    usdPerDay: s.usdPerDay,
    purpose: s.pool.purpose === "spare" ? "training" : s.pool.purpose,
  };
}

/** The idle-allocated holders now, worst (most money so far) first. */
export function idleAllocations(now: () => number) {
  const seen = new Map<string, { h: Holder; d: DeviceModel; gpus: number }>();
  for (const d of allDevices()) {
    const h = holderAt(d.node, d.index, 0);
    if (!h || h.idleFrom === undefined) continue;
    const key = `${h.subject.id}|${h.pod}`;
    const e = seen.get(key) ?? { h, d, gpus: 0 };
    e.gpus++;
    seen.set(key, e);
  }
  return [...seen.values()]
    .map(({ h, d, gpus }) => {
      const idleHours = -h.idleFrom! / HOUR;
      return {
        namespace: h.namespace,
        workload: h.subject,
        owner: h.owner,
        gpus,
        idleHours,
        usd: idleHours * gpus * d.pool.usdPerGpuHour,
        usdPerDay: gpus * d.pool.usdPerGpuHour * 24,
        nodeId: d.nodeId,
        lastActiveAt: at(h.idleFrom!, now),
        pod: h.pod,
      };
    })
    .sort((a, b) => b.usd - a.usd);
}

function fleetAnswer(clusterId: string | undefined, now: () => number) {
  const pools = ALL_POOLS.filter((p) => !clusterId || p.clusterId === clusterId);
  if (pools.length === 0) return undefined;
  const stats = pools.map((p) => poolStats(p, now));
  const total = stats.reduce((a, s) => a + s.gpus, 0);
  const allocated = stats.reduce((a, s) => a + s.allocated, 0);
  const devices = fleetNodes()
    .filter((n) => pools.includes(n.pool))
    .flatMap((n) => n.devices);
  const busyOfHeld = devices.filter((d) => holderAt(d.node, d.index, 0)).map((d) => busyNow(d, now));
  const idle = idleAllocations(now).filter((i) => pools.some((p) => i.nodeId.startsWith(`${p.clusterId}/`) && p.nodes.some((n) => i.nodeId.endsWith(`/${n}`))));
  const usdPerDay = stats.reduce((a, s) => a + s.usdPerDay, 0);
  const idleUsdPerDay = idle.reduce((a, i) => a + i.usdPerDay, 0);
  const unallocatedUsd = stats.reduce((a, s) => a + (s.gpus - s.allocated) * s.pool.usdPerGpuHour * 24, 0);
  const unallocatedGpuH = stats.reduce((a, s) => a + (s.gpus - s.allocated) * 24, 0);
  const idleGpuH = idle.reduce((a, i) => a + i.gpus * 24, 0);
  // Of the allocated rest: training's badput (the straggler's 15% of 64 H100 for 12h,
  // checkpoint pauses) and serving's own headroom are not idle; startup was small.
  const h100 = POOLS.h100.usdPerGpuHour;
  const stalledGpuH = 64 * 12 * 0.15;
  const checkpointGpuH = 64 * 24 * 0.021;
  const startupGpuH = 8 * 0.4;
  const productiveGpuH = total * 24 - unallocatedGpuH - idleGpuH - stalledGpuH - checkpointGpuH - startupGpuH;
  const productiveUsd = usdPerDay - unallocatedUsd - idleUsdPerDay - (stalledGpuH + checkpointGpuH + startupGpuH) * h100;
  const clusters = Object.values(CLUSTERS).filter((c) => pools.some((p) => p.clusterId === c.id));
  return {
    summary: {
      gpusTotal: total,
      gpusAllocated: allocated,
      busyRatio: busyOfHeld.length ? busyOfHeld.reduce((a, b) => a + b, 0) / busyOfHeld.length : 0,
      gpusIdleAllocated: stats.reduce((a, s) => a + s.idleAllocated, 0),
      gpusUnallocated: total - allocated,
      gpusFaulty: stats.reduce((a, s) => a + s.faulty, 0),
      gpusThrottled: stats.reduce((a, s) => a + s.throttled, 0),
      usdPerDay,
      idleUsdPerDay,
      cost24h: {
        gpuHours: total * 24,
        usd: usdPerDay,
        priced: true,
        byCategory: [
          { category: MlCostCategory.PRODUCTIVE, gpuHours: productiveGpuH, usd: productiveUsd },
          { category: MlCostCategory.IDLE_ALLOCATED, gpuHours: idleGpuH, usd: idleUsdPerDay },
          { category: MlCostCategory.UNALLOCATED, gpuHours: unallocatedGpuH, usd: unallocatedUsd },
          { category: MlCostCategory.STALLED, gpuHours: stalledGpuH, usd: stalledGpuH * h100 },
          { category: MlCostCategory.CHECKPOINT, gpuHours: checkpointGpuH, usd: checkpointGpuH * h100 },
          { category: MlCostCategory.STARTUP, gpuHours: startupGpuH, usd: startupGpuH * h100 },
        ],
      },
      trainingGoodput: 0.83,
    },
    clusters: clusters.map((c) => {
      const cn = fleetNodes().filter((n) => n.pool.clusterId === c.id);
      return {
        id: c.id,
        name: c.name,
        provider: c.provider,
        location: c.location,
        kubernetesVersion: c.kubernetesVersion,
        lastSeenAt: at(-40_000, now),
        nodeCount: cn.length,
        gpuCount: cn.reduce((a, n) => a + n.devices.length, 0),
        health: cn.some((n) => n.health === MlHealth.FAULTY) ? MlHealth.FAULTY : cn.some((n) => n.health === MlHealth.DEGRADED) ? MlHealth.DEGRADED : MlHealth.HEALTHY,
      };
    }),
    pools: stats.map(poolMessage),
    topIdle: idle.map((i) => ({ namespace: i.namespace, workload: i.workload, owner: i.owner, gpus: i.gpus, idleHours: i.idleHours, usd: i.usd, nodeId: i.nodeId, lastActiveAt: i.lastActiveAt })),
    unhealthy: unhealthyItems(now).filter((u) => pools.some((p) => u.nodeId.startsWith(`${p.clusterId}/`))),
    observedAt: at(-40_000, now),
  };
}

/** Devices and nodes with open health problems, worst first. */
export function unhealthyItems(now: () => number) {
  const evs = fleetEvents(now);
  const items = [];
  for (const n of fleetNodes()) {
    for (const d of n.devices) {
      if (d.health.health === MlHealth.HEALTHY) continue;
      items.push({
        subject: subj(MlSubjectKind.DEVICE, d.uuid, deviceLabel(n.name, d.index)),
        score: d.health.score,
        health: d.health.health,
        reason: d.health.reason,
        eventsTotal: 0,
        events24h: evs.filter((e) => e.subject?.id === d.uuid && (e.at ? Number(e.at.seconds) * 1000 >= now() - DAY : false)).length,
        nodeId: n.id,
        findingId: d.health.findingId ?? "",
      });
    }
    if (n.health !== MlHealth.HEALTHY)
      items.push({
        subject: subj(MlSubjectKind.NODE, n.id, n.name),
        score: n.score,
        health: n.health,
        reason: n.reason,
        eventsTotal: 0,
        events24h: evs.filter((e) => e.subject?.id === n.id || e.related?.some((r) => r.id === n.id)).length,
        nodeId: n.id,
        findingId: n.findingId ?? "",
      });
  }
  return items.sort((a, b) => b.score - a.score || (a.subject.kind === MlSubjectKind.DEVICE ? -1 : 1)).map(({ eventsTotal: _e, ...rest }) => rest);
}

function poolAnswer(poolId: string, now: () => number) {
  const p = poolById(poolId);
  if (!p) return undefined;
  const nodes = fleetNodes().filter((n) => n.pool.id === p.id);
  const devices = nodes.flatMap((n) => n.devices);
  const skew = new Map<string, number>();
  for (const n of nodes) skew.set(`${p.driver}|${p.cuda}|${n.osImage}`, (skew.get(`${p.driver}|${p.cuda}|${n.osImage}`) ?? 0) + 1);
  // Throttle reasons: share of device-time over 24h, sampled hourly.
  const counts = new Map<string, number>();
  let samples = 0;
  for (const d of devices)
    for (let hh = 0; hh < 24; hh++) {
      const s = sampleAt(d, -hh * HOUR - 17 * MINUTE, now);
      if (!Number.isFinite(s.util)) continue;
      samples++;
      const rs = clockReasons(d, s);
      const r = rs.length ? rs[rs.length - 1] : "none";
      counts.set(r, (counts.get(r) ?? 0) + 1);
    }
  const throttle = [...counts.entries()].sort((a, b) => b[1] - a[1]).map(([name, v]) => ({ name, value: v, share: v / Math.max(1, samples) }));
  const ns = new Map<string, number>();
  for (const d of devices) {
    const h = holderAt(d.node, d.index, 0);
    ns.set(h ? h.namespace : "unallocated", (ns.get(h ? h.namespace : "unallocated") ?? 0) + 1);
  }
  return {
    pool: poolMessage(poolStats(p, now)),
    nodes: nodes.map((n) => nodeMessage(n, now, false)),
    skew: [...skew.entries()].map(([k, count]) => {
      const [driver, cuda, osImage] = k.split("|");
      return { driver, cuda, osImage, nodes: count };
    }),
    throttleReasons: throttle,
    allocationByNamespace: [...ns.entries()].sort((a, b) => b[1] - a[1]).map(([name, v]) => ({ name, value: v, share: v / devices.length })),
  };
}

// ── Events ────────────────────────────────────────────────────────────────────

const devSubj = (node: string, i: number) => subj(MlSubjectKind.DEVICE, gpuUuid(node, i), deviceLabel(node, i));
const nodeSubj = (node: string) => {
  const n = nodeByName(node)!;
  return subj(MlSubjectKind.NODE, n.id, node);
};
const findingSubj = (id: string, label: string) => subj(MlSubjectKind.FINDING, id, label);

let EVENTS_CACHE: { at: number; events: EventInit[] } | null = null;

/** Every fleet event of the last week, newest first. */
export function fleetEvents(now: () => number): EventInit[] {
  if (EVENTS_CACHE && EVENTS_CACHE.at === now()) return EVENTS_CACHE.events;
  const a2 = attemptId(RUNS.v3.id, "sft", 2);
  const a3 = attemptId(RUNS.v3.id, "sft", 3);
  const run = subj(MlSubjectKind.RUN, RUNS.v3.id, RUNS.v3.name);
  const ev: EventInit[] = [
    {
      id: "ev-xid79-r4vb",
      at: at(XID_AT, now),
      kind: MlEventKind.XID,
      severity: MlSeverity.CRITICAL,
      title: `Xid 79 on ${BAD_NODE} GPU ${BAD_GPU_INDEX}: GPU has fallen off the bus`,
      detail: `NVRM: Xid (PCI:0000:ab:00): 79, pid=2817734, name=python, GPU has fallen off the bus. The GPU is no longer visible to the driver; sft attempt 2 died at step 32,750.`,
      subject: devSubj(BAD_NODE, BAD_GPU_INDEX),
      related: [nodeSubj(BAD_NODE), run, subj(MlSubjectKind.ATTEMPT, a2, "sft attempt 2"), findingSubj(FINDINGS.xid79, "Faulty GPU")],
      step: 32750n,
      source: "dcgm",
      attributes: { xid: "79", pci_bus_id: "0000:ab:00.0", process: "python (pid 2817734)" },
      hookEventType: "ml.device.xid",
    },
    {
      id: "ev-npd-xid-r4vb",
      at: at(XID_AT + 30_000, now),
      kind: MlEventKind.NPD_CONDITION,
      severity: MlSeverity.CRITICAL,
      title: "NPD: GPUXidError=True (XidCriticalError)",
      detail: `gpu-xid-monitor matched "NVRM: Xid (PCI:0000:ab:00): 79" in the kernel log`,
      subject: nodeSubj(BAD_NODE),
      related: [devSubj(BAD_NODE, BAD_GPU_INDEX)],
      source: "npd",
      attributes: { condition: "GPUXidError", reason: "XidCriticalError" },
    },
    {
      id: "ev-cordon-r4vb",
      at: at(XID_AT + 60_000, now),
      kind: MlEventKind.NODE_NOT_READY,
      severity: MlSeverity.WARNING,
      title: `Cordoned by ${ALERTS.xidFatal} (auto action)`,
      detail: `${BAD_NODE} marked unschedulable; hot spare ${SPARE_NODE} admitted to the attempt's topology; sft attempt 3 queued from checkpoint 32,000.`,
      subject: nodeSubj(BAD_NODE),
      related: [run, subj(MlSubjectKind.ATTEMPT, a3, "sft attempt 3"), nodeSubj(SPARE_NODE)],
      source: "forge",
      attributes: { alert: ALERTS.xidFatal, action: "cordon+replace" },
      hookEventType: "ml.node.cordoned",
    },
    {
      id: "ev-spare-s01",
      at: at(V3_ATTEMPTS[4].runningAt, now),
      kind: MlEventKind.RESTART,
      severity: MlSeverity.INFO,
      title: `Hot spare ${SPARE_NODE} running sft attempt 3 (ranks 16–23)`,
      detail: `Replaced ${BAD_NODE}; the h100-spare pool has 0 spares left.`,
      subject: nodeSubj(SPARE_NODE),
      related: [run, subj(MlSubjectKind.ATTEMPT, a3, "sft attempt 3"), nodeSubj(BAD_NODE), findingSubj(FINDINGS.noSpareServe, "No hot spare")],
      step: 32000n,
      source: "forge",
      hookEventType: "ml.attempt.started",
    },
    {
      id: "ev-diag-r4vb",
      at: at(XID_AT + 40 * MINUTE, now),
      kind: MlEventKind.DCGM_DIAG,
      severity: MlSeverity.CRITICAL,
      title: "DCGM diag level 3: FAIL",
      detail: `GPU ${BAD_GPU_INDEX} not found; 7 of 8 GPUs passed targeted stress, targeted power and memory bandwidth.`,
      subject: nodeSubj(BAD_NODE),
      related: [devSubj(BAD_NODE, BAD_GPU_INDEX), findingSubj(FINDINGS.xid79, "Faulty GPU")],
      source: "dcgm",
      attributes: { level: "3", result: "fail" },
    },
    {
      id: "ev-straggler-c8dw",
      at: at(STRAGGLER_SINCE, now),
      kind: MlEventKind.STRAGGLER,
      severity: MlSeverity.WARNING,
      title: `Rank ${STRAGGLER_RANK} straggling on ${HOT_NODE} GPU ${HOT_GPU_INDEX}`,
      detail: "SM clock 1,290–1,755 MHz against a 1,980 MHz median; every allreduce waits on it, step time +18%.",
      subject: devSubj(HOT_NODE, HOT_GPU_INDEX),
      related: [run, subj(MlSubjectKind.RANK, rankId(RUNS.v3.id, "sft", 3, STRAGGLER_RANK), `rank ${STRAGGLER_RANK}`), nodeSubj(HOT_NODE), findingSubj(FINDINGS.straggler, "Straggler")],
      step: 36900n,
      source: "dcgm",
    },
    ...THERMAL_EPISODES.map(
      ([a, b], i): EventInit => ({
        id: `ev-thermal-c8dw-${i}`,
        at: at(a * HOUR, now),
        ...(b !== null ? { endAt: at(b * HOUR, now) } : {}),
        kind: MlEventKind.THROTTLE,
        severity: MlSeverity.WARNING,
        title: `HW thermal slowdown on GPU ${HOT_GPU_INDEX}${b === null ? " (ongoing)" : ""}`,
        detail: `DCGM_FI_DEV_CLOCKS_EVENT_REASONS 0x48 (HW slowdown, HW thermal slowdown) at 87°C, HBM 95°C; SM clock 1,290 MHz.`,
        subject: devSubj(HOT_NODE, HOT_GPU_INDEX),
        related: [nodeSubj(HOT_NODE), run, findingSubj(FINDINGS.thermal, "Thermal")],
        source: "dcgm",
        attributes: { reasons: "HW_SLOWDOWN|HW_THERMAL_SLOWDOWN", bitmask: "0x48" },
        hookEventType: "ml.device.throttle",
      }),
    ),
    {
      id: "ev-npd-thermal-c8dw",
      at: at(STRAGGLER_SINCE + 2 * MINUTE, now),
      kind: MlEventKind.NPD_CONDITION,
      severity: MlSeverity.WARNING,
      title: "NPD: GPUThermalThrottle=True (HwThermalSlowdown)",
      detail: "custom-plugin-monitor gpu-health: GPU 5 in HW thermal slowdown",
      subject: nodeSubj(HOT_NODE),
      related: [devSubj(HOT_NODE, HOT_GPU_INDEX)],
      source: "npd",
    },
    {
      id: "ev-ecc-m1xp",
      at: at(-2 * DAY - 25 * MINUTE, now),
      kind: MlEventKind.ECC,
      severity: MlSeverity.WARNING,
      title: `SBE burst on ${REMAP_NODE} GPU ${REMAP_GPU_INDEX}: 188 correctable in 10 min`,
      detail: "HBM stack 3; corrected. Followed by a row remap.",
      subject: devSubj(REMAP_NODE, REMAP_GPU_INDEX),
      related: [nodeSubj(REMAP_NODE), findingSubj(FINDINGS.rowRemap, "Row remap")],
      source: "dcgm",
    },
    {
      id: "ev-remap-m1xp",
      at: at(-2 * DAY, now),
      kind: MlEventKind.ROW_REMAP,
      severity: MlSeverity.WARNING,
      title: `Row remap pending on ${REMAP_NODE} GPU ${REMAP_GPU_INDEX}`,
      detail: "Correctable remapped rows 2 → 3; DCGM_FI_DEV_ROW_REMAP_PENDING=1: the remap applies at the next GPU reset.",
      subject: devSubj(REMAP_NODE, REMAP_GPU_INDEX),
      related: [nodeSubj(REMAP_NODE), run, findingSubj(FINDINGS.rowRemap, "Row remap")],
      source: "dcgm",
      hookEventType: "ml.device.row_remap",
    },
    ...[-30, -18, -6].map(
      (hh, i): EventInit => ({
        id: `ev-nvlink-t5hn-${i}`,
        at: at(hh * HOUR, now),
        kind: MlEventKind.LINK_FLAP,
        severity: MlSeverity.WARNING,
        title: `NVLink CRC errors rising: GPU 1 ↔ GPU 3 (${["4,610", "9,870", "18,420"][i]} flit errors)`,
        detail: "DCGM_FI_DEV_NVLINK_CRC_FLIT_ERROR_COUNT_TOTAL above the fleet p99; replays 96. NCCL falls back to slower retries on this pair.",
        subject: nodeSubj(NVLINK_NODE),
        related: [devSubj(NVLINK_NODE, 1), devSubj(NVLINK_NODE, 3), findingSubj(FINDINGS.nvlinkCrc, "NVLink CRC")],
        source: "dcgm",
      }),
    ),
    {
      id: "ev-npd-nvlink-t5hn",
      at: at(-30 * HOUR + 3 * MINUTE, now),
      kind: MlEventKind.NPD_CONDITION,
      severity: MlSeverity.WARNING,
      title: "NPD: NVLinkError=True (NVLinkCRCErrorsRising)",
      detail: "custom-plugin-monitor gpu-health",
      subject: nodeSubj(NVLINK_NODE),
      source: "npd",
    },
    {
      id: "ev-kubelet-restart-l4",
      at: at(-9 * HOUR - 12 * MINUTE, now),
      kind: MlEventKind.NPD_CONDITION,
      severity: MlSeverity.INFO,
      title: "kubelet restarted (KubeletStart)",
      detail: "systemd-monitor: kubelet.service restarted after a config reload; pods kept running.",
      subject: nodeSubj("ip-10-40-1-37"),
      source: "npd",
      attributes: { reason: "KubeletStart" },
    },
    {
      id: "ev-xid13-l4",
      at: at(-5 * HOUR - 40 * MINUTE, now),
      kind: MlEventKind.XID,
      severity: MlSeverity.INFO,
      title: "Xid 13 on ip-10-40-1-37 GPU 3: graphics engine exception",
      detail: "An application fault (illegal address) in the vLLM worker; the GPU stays usable. Flagged only if it repeats across apps.",
      subject: devSubj("ip-10-40-1-37", 3),
      related: [nodeSubj("ip-10-40-1-37"), replica(SERVICES.prod.id, "support-assistant-prod-vllm-3", "support-assistant-prod replica 3")],
      source: "dcgm",
      attributes: { xid: "13" },
    },
    {
      id: "ev-idle-notebook",
      at: at(-NOTEBOOK_POD.idleHours * HOUR + HOUR, now),
      kind: MlEventKind.FINDING_OPENED,
      severity: MlSeverity.WARNING,
      title: `${NOTEBOOK_POD.pod} idle for 1h on 2 L4s`,
      subject: nodeSubj(IDLE_NODE),
      related: [findingSubj(FINDINGS.idleNotebook, "Idle notebook")],
      source: "forge",
      hookEventType: "ml.finding.opened",
    },
    ...fleetNodes()
      .filter((n) => n.name !== BAD_NODE)
      .slice(0, 6)
      .map(
        (n, i): EventInit => ({
          id: `ev-diag-weekly-${n.name}`,
          at: at(-6 * DAY - (hash(n.name) % 5) * HOUR - i * MINUTE, now),
          kind: MlEventKind.DCGM_DIAG,
          severity: MlSeverity.INFO,
          title: "DCGM diag level 3: pass (weekly)",
          subject: nodeSubj(n.name),
          source: "dcgm",
        }),
      ),
  ];
  ev.sort((x, y) => Number(y.at!.seconds) - Number(x.at!.seconds));
  EVENTS_CACHE = { at: now(), events: ev };
  return ev;
}

const touches = (e: EventInit, pred: (id: string) => boolean) => [e.subject, ...(e.related ?? [])].some((s) => s && pred(s.id ?? ""));

function eventsFor(subject: Subj, now: () => number): EventInit[] | undefined {
  const all = fleetEvents(now);
  switch (subject.kind) {
    case MlSubjectKind.ORG:
      return all.filter((e) => e.severity !== MlSeverity.INFO || e.kind === MlEventKind.XID);
    case MlSubjectKind.CLUSTER:
      if (!Object.values(CLUSTERS).some((c) => c.id === subject.id)) return undefined;
      return all.filter((e) => touches(e, (id) => id.startsWith(`${subject.id}/`) || !!deviceByUuid(id)?.nodeId.startsWith(`${subject.id}/`)));
    case MlSubjectKind.POOL: {
      const p = poolById(subject.id);
      if (!p) return undefined;
      const ids = new Set(fleetNodes().filter((n) => n.pool.id === p.id).flatMap((n) => [n.id, ...n.devices.map((d) => d.uuid)]));
      return all.filter((e) => touches(e, (id) => ids.has(id)));
    }
    case MlSubjectKind.NODE: {
      const n = nodeById(subject.id);
      if (!n) return undefined;
      const ids = new Set([n.id, ...n.devices.map((d) => d.uuid)]);
      return all.filter((e) => touches(e, (id) => ids.has(id)));
    }
    case MlSubjectKind.DEVICE:
      if (!deviceByUuid(subject.id)) return undefined;
      return all.filter((e) => touches(e, (id) => id === subject.id));
    default:
      return undefined;
  }
}

// ── Alerts ────────────────────────────────────────────────────────────────────

export const HW_ALERT_IDS = { ...ALERTS, rowRemap: "alert-row-remap-pending", nvlinkCrc: "alert-nvlink-crc", eccDbe: "alert-ecc-dbe" } as const;

function alertsFor(subject: Subj, now: () => number): AlertRuleInit[] | undefined {
  const scope = subject.kind;
  const node =
    scope === MlSubjectKind.NODE ? nodeById(subject.id) : scope === MlSubjectKind.DEVICE ? nodeByName(deviceByUuid(subject.id)?.node ?? "") : undefined;
  if ((scope === MlSubjectKind.NODE || scope === MlSubjectKind.DEVICE) && !node) return undefined;
  if (scope === MlSubjectKind.POOL && !poolById(subject.id)) return undefined;
  if (scope === MlSubjectKind.CLUSTER && !Object.values(CLUSTERS).some((c) => c.id === subject.id)) return undefined;
  if (![MlSubjectKind.ORG, MlSubjectKind.CLUSTER, MlSubjectKind.POOL, MlSubjectKind.NODE, MlSubjectKind.DEVICE].includes(scope)) return undefined;
  const cluster = subj(MlSubjectKind.CLUSTER, CLUSTERS.train.id, CLUSTERS.train.name);
  const firingHere = (names: string[]) => (node ? names.includes(node.name) : true);
  const rules: (AlertRuleInit & { state: MlAlertState; nodes: string[] })[] = [
    {
      id: HW_ALERT_IDS.xidFatal,
      name: "Fatal Xid",
      expression: "a kernel-log Xid in {48, 62, 64, 74, 79, 94, 95, 119, 120} on any GPU (DCGM_EXP_XID_ERRORS_TOTAL)",
      severity: MlSeverity.CRITICAL,
      state: MlAlertState.OK,
      subject: cluster,
      declaredIn: "deploy/kcl/prod/main.k",
      lastFiredAt: at(XID_AT, now),
      fired7d: 1,
      autoAction: "cordon the node, swap in a hot spare, start a new attempt from the last checkpoint",
      autoActionEnabled: true,
      routedTo: "Slack #ml-oncall",
      lastAction: `cordoned ${BAD_NODE}, swapped in ${SPARE_NODE}, started sft attempt 3 from step 32,000`,
      nodes: [],
    },
    {
      id: HW_ALERT_IDS.gpuThermal,
      name: "GPU thermal slowdown",
      expression: "HW thermal slowdown (DCGM_FI_DEV_CLOCKS_EVENT_REASONS & 0x40) > 10% of 15m, or DCGM_FI_DEV_GPU_TEMP ≥ slowdown − 2°C",
      severity: MlSeverity.WARNING,
      state: MlAlertState.FIRING,
      subject: devSubj(HOT_NODE, HOT_GPU_INDEX),
      declaredIn: "forge default",
      lastFiredAt: at(STRAGGLER_SINCE, now),
      fired7d: 7,
      autoAction: "",
      routedTo: "Slack #ml-oncall",
      lastAction: "",
      nodes: [HOT_NODE],
    },
    {
      id: HW_ALERT_IDS.rowRemap,
      name: "Row remap pending",
      expression: "DCGM_FI_DEV_ROW_REMAP_PENDING > 0 for 10m",
      severity: MlSeverity.WARNING,
      state: MlAlertState.FIRING,
      subject: devSubj(REMAP_NODE, REMAP_GPU_INDEX),
      declaredIn: "forge default",
      lastFiredAt: at(-2 * DAY, now),
      fired7d: 1,
      autoAction: "drain at the next checkpoint and reset the GPU",
      autoActionEnabled: false,
      routedTo: "Inbox",
      lastAction: "",
      nodes: [REMAP_NODE],
    },
    {
      id: HW_ALERT_IDS.nvlinkCrc,
      name: "NVLink CRC errors",
      expression: "rate(DCGM_FI_DEV_NVLINK_CRC_FLIT_ERROR_COUNT_TOTAL[1h]) above the fleet p99 for 30m",
      severity: MlSeverity.WARNING,
      state: MlAlertState.FIRING,
      subject: nodeSubj(NVLINK_NODE),
      declaredIn: "forge default",
      lastFiredAt: at(-30 * HOUR, now),
      fired7d: 3,
      autoAction: "",
      routedTo: "Inbox",
      lastAction: "",
      nodes: [NVLINK_NODE],
    },
    {
      id: HW_ALERT_IDS.eccDbe,
      name: "Uncorrectable ECC",
      expression: "increase(DCGM_FI_DEV_ECC_DBE_VOL_TOTAL[5m]) > 0",
      severity: MlSeverity.CRITICAL,
      state: MlAlertState.OK,
      subject: cluster,
      declaredIn: "deploy/kcl/prod/main.k",
      fired7d: 0,
      autoAction: "cordon the node and start a new attempt from the last checkpoint",
      autoActionEnabled: true,
      routedTo: "Slack #ml-oncall",
      lastAction: "",
      nodes: [],
    },
  ];
  return rules.map(({ nodes, ...r }) => ({ ...r, state: r.state === MlAlertState.FIRING && !firingHere(nodes) ? MlAlertState.OK : r.state }));
}

/** The devices a subject covers: the org's, a cluster's, a pool's, a node's, or one. */
function devicesOf(subject: Subj): DeviceModel[] | undefined {
  switch (subject.kind) {
    case MlSubjectKind.ORG:
      return allDevices();
    case MlSubjectKind.CLUSTER:
      return Object.values(CLUSTERS).some((c) => c.id === subject.id) ? allDevices().filter((d) => d.pool.clusterId === subject.id) : undefined;
    case MlSubjectKind.POOL:
      return poolById(subject.id) ? allDevices().filter((d) => d.pool.id === subject.id) : undefined;
    case MlSubjectKind.NODE: {
      const n = nodeById(subject.id);
      return n?.devices;
    }
    case MlSubjectKind.DEVICE: {
      const d = deviceByUuid(subject.id);
      return d ? [d] : undefined;
    }
    default:
      return undefined;
  }
}

// ── The answers ───────────────────────────────────────────────────────────────

export const fleetAnswers: MlDomain = {
  getMlFleet: (req, now) => fleetAnswer(req.clusterId, now),
  getMlPool: (req, now) => poolAnswer(req.poolId, now),
  getMlNode: (req, now) => {
    const n = nodeById(req.nodeId) ?? nodeByName(req.nodeId);
    if (!n) return undefined;
    return {
      node: nodeMessage(n, now, true),
      workloads: workloadsOf(n.devices, now),
      nvlink: nvlinkPairs(n),
      ibPorts: ibPorts(n, now),
      host: hostResources(n, now),
      diagnostics: diagnostics(n, now),
      incarnations: incarnations(n, now),
    };
  },
  getMlDevice: (req, now) => {
    const d = deviceByUuid(req.uuid);
    if (!d) return undefined;
    const placements =
      d.node === BAD_NODE && d.index === BAD_GPU_INDEX
        ? [
            { nodeId: d.nodeId, index: d.index, from: at(-40 * DAY, now) },
            { nodeId: nodeByName(SPARE_NODE)!.id, index: 2, from: at(-120 * DAY, now), to: at(-40 * DAY, now) },
          ]
        : [{ nodeId: d.nodeId, index: d.index, from: at(-(nodeByName(d.node)!.bootedDaysAgo + 30) * DAY, now) }];
    return { device: deviceMessage(d, now, true), workloads: workloadsOf([d], now), placements };
  },
};

/** DCGM, node-exporter and the cluster's events, by device, node, pool and the org; served by ./telemetry.ts. */
export const fleetTelemetry: MlTelemetryDomain = {
  series: (req, now) => {
    const subject = req.subject;
    if (!subject) return undefined;
    const devices = devicesOf(subject);
    if (!devices) return undefined;
    const keys = req.keys;
    const groupBy = req.groupBy;
    const w = windowOf(req, now, keys.length && keys.every((k) => k.startsWith("pool.") || k.startsWith("fleet.")) ? 48 : 288);
    const series: SeriesInit[] = [];
    if (subject.kind === MlSubjectKind.DEVICE) {
      for (const def of DEVICE_SERIES) if (wantKey(keys, def.key)) series.push(deviceSeriesOne(devices[0], def, w, now));
      return { series };
    }
    if (subject.kind === MlSubjectKind.NODE) {
      const n = nodeById(subject.id)!;
      for (const def of DEVICE_SERIES) {
        if (!wantKey(keys, def.key)) continue;
        if (groupBy === "device") series.push(...n.devices.map((d) => deviceSeriesOne(d, def, w, now, `GPU ${d.index}`)));
        else series.push(...bandSeries(n.devices, def, w, now, subject));
      }
      for (const def of HOST_SERIES) {
        if (!wantKey(keys, def.key)) continue;
        const common = { key: def.key, label: def.label, unit: def.unit, axis: MlAxis.TIME, resolution: w.res, subject };
        if (def.ib && n.devices[0].spec !== SPECS.h100) {
          series.push({ ...common, x: [], y: [], collection: MlCollection.NOT_APPLICABLE, missingSource: "no InfiniBand HCAs on this node" });
          continue;
        }
        const r = rng(hash(`${n.name}|${def.key}`));
        series.push({ ...common, x: w.xs, y: w.offs.map((off) => def.read(hostAt(n, off, now, r))), collection: MlCollection.COLLECTED });
      }
      return { series };
    }
    return { series: aggregateSeries(devices, keys, groupBy, w, now, subject) };
  },
  events: (req, now) => {
    if (!req.subject) return undefined;
    let events = eventsFor(req.subject, now);
    if (!events) return undefined;
    const from = req.from ? Number(req.from.seconds) : -Infinity;
    const to = req.to ? Number(req.to.seconds) : Infinity;
    events = events.filter((e) => {
      const s = Number(e.at!.seconds);
      const end = e.endAt ? Number(e.endAt.seconds) : s;
      return end >= from && s <= to && (req.kinds.length === 0 || req.kinds.includes(e.kind!));
    });
    return { events: req.limit ? events.slice(0, req.limit) : events };
  },
  alerts: (req, now) => {
    if (!req.subject) return undefined;
    const alerts = alertsFor(req.subject, now);
    return alerts ? { alerts } : undefined;
  },
};
