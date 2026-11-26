// Copyright (c) 2026 Reliant Labs

/**
 * shop's ML pages (Machines, Stack, Recommendations, Runs, Models, Inference;
 * forge docs/proposals/ml-workloads.md §8), told in shopStory's terms, as
 * WIRE messages: handlers/ml.ts serves them as MlEarlyAccessService's
 * answers. Only the demo carries this data; the real service never invents
 * any (it answers FEATURE_NOT_ENTITLED or Unimplemented).
 *
 *   clusters   two connected: gke-us-central1-train (GKE, trains) and
 *              eks-us-east-1-serve (EKS, serves).
 *   machines   train's H100 pool: 4 nodes × 8 NVIDIA H100 80GB HBM3. Two
 *              run maya's fine-tune (16 GPUs); r4vb's GPU-5 fell off the bus
 *              two hours ago (Xid 79), so it is BAD with 7 of 8 allocatable,
 *              still uncordoned, and still running the eval job on 4 GPUs.
 *              serve's L4 pool: 3 nodes × 4 NVIDIA L4. vLLM's three replicas
 *              hold 2 each; priya's notebook has held 2 more at 0% for 19h.
 *   stack      the GPU Operator's DCGM exporter, device plugin and feature
 *              discovery, and Kueue: theirs, adopted. JobSet, Kubeflow
 *              Trainer and node-problem-detector (with its GPU log rule):
 *              installed by forge. serve has no Trainer and no KServe: vLLM
 *              runs as a plain Deployment.
 *   findings   worst first: the Xid-79 node; the idle notebook; vLLM's slow
 *              replica start (image pull is most of it); the training
 *              template's 64Mi /dev/shm (which killed the failed run);
 *              DCGM profiling off; kubelet tracing off.
 *   runs       v3 of the support assistant's fine-tune, RUNNING; v2,
 *              SUCCEEDED (it produced the served model version); v2's first
 *              attempt, FAILED (a Bus error in a DataLoader worker after a
 *              Kueue preemption and three restarts).
 *   models     support-assistant: v2 (from the v2 run, serving in prod) and
 *              v1 (pushed by hand before forge trained it).
 *   inference  support-assistant on vLLM 0.6.3, in prod on the L4 pool,
 *              serving v2 since the release prod runs (RUNNING["env-prod"]);
 *              its own release check PASSED.
 *
 * Times are offsets from demoNow() (the page load), like every other demo
 * page; the vLLM load follows the shop's own daily traffic (dailyLoad).
 */

import type { MessageInitShape } from "@bufbuild/protobuf";
import { timestampFromMs } from "@bufbuild/protobuf/wkt";

import {
  MlComponentState,
  MlFindingSeverity,
  MlNodeHealth,
  MlReleaseVerdict,
  MlRunState,
  type GetMlMachinesResponseSchema,
  type GetMlStackResponseSchema,
  type ListMlFindingsResponseSchema,
  type ListMlInferenceServicesResponseSchema,
  type ListMlModelsResponseSchema,
  type ListMlRunsResponseSchema,
  type MlClusterSchema,
  type MlFindingSchema,
  type MlMetricSeriesSchema,
  type MlNodePoolSchema,
  type MlNodeSchema,
  type MlPodStartBreakdownSchema,
  type MlStackComponentSchema,
  type MlUtilizationPointSchema,
} from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";

import { DAY, HOUR, MINUTE, RUNNING, dailyLoad, demoNow, demoPerson } from "../shopStory";

type MachinesInit = MessageInitShape<typeof GetMlMachinesResponseSchema>;
type ClusterInit = MessageInitShape<typeof MlClusterSchema>;
type PoolInit = MessageInitShape<typeof MlNodePoolSchema>;
type StackInit = MessageInitShape<typeof GetMlStackResponseSchema>;
type FindingsInit = MessageInitShape<typeof ListMlFindingsResponseSchema>;
type RunsInit = MessageInitShape<typeof ListMlRunsResponseSchema>;
type ModelsInit = MessageInitShape<typeof ListMlModelsResponseSchema>;
type InferenceInit = MessageInitShape<typeof ListMlInferenceServicesResponseSchema>;
type NodeInit = MessageInitShape<typeof MlNodeSchema>;
type ComponentInit = MessageInitShape<typeof MlStackComponentSchema>;
type FindingInit = MessageInitShape<typeof MlFindingSchema>;
type SeriesInit = MessageInitShape<typeof MlMetricSeriesSchema>;
type PointInit = MessageInitShape<typeof MlUtilizationPointSchema>;
type BreakdownInit = MessageInitShape<typeof MlPodStartBreakdownSchema>;

// ── The cast of the ML story (the pages' tests may name these) ──────────────

export const ML_TRAIN_CLUSTER = "gke-us-central1-train";
export const ML_SERVE_CLUSTER = "eks-us-east-1-serve";

export const ML_H100 = "NVIDIA H100 80GB HBM3";
export const ML_L4 = "NVIDIA L4";
export const ML_H100_POOL = "h100-pool";
export const ML_L4_POOL = "l4-pool";

const H100_NODE = (suffix: string) => `gke-us-central1-train-h100-pool-8f2c41d0-${suffix}`;
/** The H100 nodes: two run the fine-tune, r4vb lost GPU-5 (Xid 79), w8tz is free. */
export const ML_H100_NODES = [H100_NODE("2x7k"), H100_NODE("9qmd"), H100_NODE("r4vb"), H100_NODE("w8tz")] as const;
/** The node whose GPU-5 fell off the bus. */
export const ML_BAD_NODE = ML_H100_NODES[2];
export const ML_BAD_DEVICE = "GPU-5";

/** The L4 nodes: two hold vLLM's replicas, the second also priya's idle notebook; the third is free. */
export const ML_L4_NODES = ["ip-10-42-17-201.ec2.internal", "ip-10-42-21-88.ec2.internal", "ip-10-42-30-145.ec2.internal"] as const;
/** The L4 node with 2 allocated, idle GPUs. */
export const ML_IDLE_NODE = ML_L4_NODES[1];
export const ML_IDLE_POD = { namespace: "research", pod: "jupyter-priya-0" } as const;

export const ML_RUN_IDS = { running: "mlrun-sft-v3-a41c7e", succeeded: "mlrun-sft-v2-93be20", failed: "mlrun-sft-v2-5d0f6a" } as const;

export const ML_MODEL_NAME = "support-assistant";
export const ML_MODEL_REPOSITORY = "registry.reliantlabs.io/acme/models/support-assistant";

/** A clearly synthetic, stable hex string of `length` from `seed` (FNV-1a over counters). */
function hexOf(seed: string, length: number): string {
  let out = "";
  for (let i = 0; out.length < length; i++) {
    let h = 0x811c9dc5 ^ i;
    for (const c of `${seed}#${i}`) h = Math.imul(h ^ c.charCodeAt(0), 0x01000193) >>> 0;
    out += h.toString(16).padStart(8, "0");
  }
  return out.slice(0, length);
}

/** The model's two versions, newest first: v2 (produced by the v2 run, serving) and v1 (pushed by hand). */
export const ML_MODEL_DIGESTS = {
  v2: `sha256:${hexOf("support-assistant:v2", 64)}`,
  v1: `sha256:${hexOf("support-assistant:v1", 64)}`,
} as const;

export const ML_INFERENCE_SERVICE = "support-assistant";
export const ML_VLLM_RUNTIME = "vLLM 0.6.3";
export const ML_RELEASE_SUMMARY = "TTFT p95 within 4% of the previous release";

// ── Machines ────────────────────────────────────────────────────────────────

const H100_DRIVER = "550.127.05";
const L4_DRIVER = "550.90.07";

/** When r4vb's GPU-5 fell off the bus. */
const XID_AGO = 2 * HOUR + 14 * MINUTE;
/** The running fine-tune (16 H100s), the eval job on r4vb (4), and v2's eval that came before it (8). */
const RUN_V3_AGO = 9 * HOUR;
const EVAL_V3_AGO = 5 * HOUR;
const EVAL_V2 = { from: 30 * HOUR, to: 20 * HOUR };
/** priya's notebook took its 2 L4s 19h ago and has not used them since. */
const NOTEBOOK_AGO = 19 * HOUR;
/** vLLM's GPU utilisation at the shop's peak traffic. */
const VLLM_PEAK_UTIL = 72;

const round1 = (v: number) => Math.round(v * 10) / 10;
const wiggle = (i: number, amplitude: number) => amplitude * Math.sin(i * 1.7);

/** The last 24 hourly points (oldest first), each on the hour. */
function hourly(now: number, at: (t: number, i: number) => { util: number; allocated: number }): PointInit[] {
  const top = Math.floor(now / HOUR) * HOUR;
  return Array.from({ length: 24 }, (_, i) => {
    const t = top - (23 - i) * HOUR;
    const { util, allocated } = at(t, i);
    return { time: timestampFromMs(t), utilizationPercent: round1(Math.max(0, util)), allocatedPercent: round1(allocated) };
  });
}

function machines(now: number): { clusters: ClusterInit[]; pools: PoolInit[]; observedAt: ReturnType<typeof timestampFromMs> } {
  const seen = timestampFromMs(now - MINUTE);
  const load = dailyLoad(now);
  const node = (n: NodeInit): NodeInit => ({ sharing: "none", cordoned: false, ...n });
  const h100Nodes: NodeInit[] = [
    node({ name: ML_H100_NODES[0], acceleratorCount: 8, acceleratorsAllocatable: 8, acceleratorsAllocated: 8, utilizationPercent: 93.4, memoryUsedPercent: 88.2, driverVersion: H100_DRIVER, health: MlNodeHealth.HEALTHY }),
    node({ name: ML_H100_NODES[1], acceleratorCount: 8, acceleratorsAllocatable: 8, acceleratorsAllocated: 8, utilizationPercent: 92.1, memoryUsedPercent: 87.6, driverVersion: H100_DRIVER, health: MlNodeHealth.HEALTHY }),
    node({
      name: ML_BAD_NODE,
      acceleratorCount: 8,
      acceleratorsAllocatable: 7,
      acceleratorsAllocated: 4,
      utilizationPercent: 38.5,
      memoryUsedPercent: 41.3,
      driverVersion: H100_DRIVER,
      health: MlNodeHealth.BAD,
      flags: [
        { kind: "xid", code: "79", device: ML_BAD_DEVICE, message: "GPU has fallen off the bus", firstSeenAt: timestampFromMs(now - XID_AGO), lastSeenAt: timestampFromMs(now - XID_AGO) },
        { kind: "allocatable_below_capacity", code: "", device: "", message: "nvidia.com/gpu allocatable 7 of 8: the device plugin no longer advertises GPU-5", firstSeenAt: timestampFromMs(now - XID_AGO + MINUTE), lastSeenAt: seen },
      ],
    }),
    node({ name: ML_H100_NODES[3], acceleratorCount: 8, acceleratorsAllocatable: 8, acceleratorsAllocated: 0, utilizationPercent: 0, memoryUsedPercent: 0, driverVersion: H100_DRIVER, health: MlNodeHealth.HEALTHY }),
  ];
  const vllm = VLLM_PEAK_UTIL * load;
  const l4Nodes: NodeInit[] = [
    node({ name: ML_L4_NODES[0], acceleratorCount: 4, acceleratorsAllocatable: 4, acceleratorsAllocated: 4, utilizationPercent: round1(vllm), memoryUsedPercent: 91.0, driverVersion: L4_DRIVER, health: MlNodeHealth.HEALTHY }),
    // One vLLM replica (2 GPUs, busy) and the notebook (2 GPUs, idle): half the node's GPUs work.
    node({ name: ML_IDLE_NODE, acceleratorCount: 4, acceleratorsAllocatable: 4, acceleratorsAllocated: 4, utilizationPercent: round1(vllm / 2), memoryUsedPercent: 46.8, driverVersion: L4_DRIVER, health: MlNodeHealth.HEALTHY }),
    node({ name: ML_L4_NODES[2], acceleratorCount: 4, acceleratorsAllocatable: 4, acceleratorsAllocated: 0, utilizationPercent: 0, memoryUsedPercent: 0, driverVersion: L4_DRIVER, health: MlNodeHealth.HEALTHY }),
  ];
  const sum = (nodes: NodeInit[], key: "acceleratorCount" | "acceleratorsAllocated") => nodes.reduce((a, n) => a + (n[key] ?? 0), 0);
  return {
    clusters: [
      { clusterId: ML_TRAIN_CLUSTER, name: ML_TRAIN_CLUSTER, provider: "gke", location: "us-central1", lastSeenAt: seen },
      { clusterId: ML_SERVE_CLUSTER, name: ML_SERVE_CLUSTER, provider: "eks", location: "us-east-1", lastSeenAt: seen },
    ],
    pools: [
      {
        clusterId: ML_TRAIN_CLUSTER,
        name: ML_H100_POOL,
        acceleratorVendor: "nvidia",
        acceleratorProduct: ML_H100,
        acceleratorsTotal: sum(h100Nodes, "acceleratorCount"),
        acceleratorsAllocated: sum(h100Nodes, "acceleratorsAllocated"),
        acceleratorsIdle: 0,
        nodes: h100Nodes,
        utilization: hourly(now, (t, i) => {
          const ago = now - t;
          const train = ago <= RUN_V3_AGO ? 16 : 0;
          const evalV3 = ago <= EVAL_V3_AGO ? 4 : 0;
          const evalV2 = ago <= EVAL_V2.from && ago > EVAL_V2.to ? 8 : 0;
          const busy = train * 0.93 + evalV3 * (ago <= XID_AGO ? 0.38 : 0.52) + evalV2 * 0.55;
          return { util: (busy / 32) * 100 + (busy ? wiggle(i, 1.2) : 0), allocated: ((train + evalV3 + evalV2) / 32) * 100 };
        }),
      },
      {
        clusterId: ML_SERVE_CLUSTER,
        name: ML_L4_POOL,
        acceleratorVendor: "nvidia",
        acceleratorProduct: ML_L4,
        acceleratorsTotal: sum(l4Nodes, "acceleratorCount"),
        acceleratorsAllocated: sum(l4Nodes, "acceleratorsAllocated"),
        acceleratorsIdle: 2,
        nodes: l4Nodes,
        utilization: hourly(now, (t, i) => {
          const notebook = now - t <= NOTEBOOK_AGO ? 2 : 0;
          return { util: ((6 * VLLM_PEAK_UTIL * dailyLoad(t)) / 12) + wiggle(i, 0.8), allocated: ((6 + notebook) / 12) * 100 };
        }),
      },
    ],
    observedAt: seen,
  };
}

// ── Stack ───────────────────────────────────────────────────────────────────

const GPU_OPERATOR = "GPU Operator v24.9.0";

/**
 * The KCL `forge ml discover` is planned to propose for each component
 * (forge docs/proposals/ml-workloads.md). None of it is released: the Stack
 * page labels every declaration as planned.
 */
const adopt = (component: string, namespace: string) => `forge.Adopt { component = "${component}", namespace = "${namespace}" }`;
const install = (component: string, version: string, namespace: string, extra = "") =>
  `forge.Install {\n    component = "${component}"\n    version = "${version}"\n    namespace = "${namespace}"${extra}\n}`;

function stackOf(clusterId: string): ComponentInit[] {
  const c = (rest: ComponentInit): ComponentInit => ({ ...rest, clusterId });
  const operator: ComponentInit[] = [
    c({ name: "dcgm-exporter", category: "telemetry", state: MlComponentState.ADOPTED, version: "3.3.9", via: GPU_OPERATOR, namespace: "gpu-operator", declaration: adopt("dcgm-exporter", "gpu-operator"), note: "Found DCGM exporter 3.3.9 via GPU Operator: forge reads its metrics and leaves it as it is." }),
    c({ name: "nvidia-device-plugin", category: "drivers", state: MlComponentState.ADOPTED, version: "v0.17.0", via: GPU_OPERATOR, namespace: "gpu-operator", declaration: adopt("nvidia-device-plugin", "gpu-operator"), note: "Advertises nvidia.com/gpu to the scheduler." }),
    c({ name: "gpu-feature-discovery", category: "drivers", state: MlComponentState.ADOPTED, version: "v0.17.0", via: GPU_OPERATOR, namespace: "gpu-operator", declaration: adopt("gpu-feature-discovery", "gpu-operator"), note: "Labels each node with its GPU product, memory and driver." }),
  ];
  const npd = c({
    name: "node-problem-detector",
    category: "health",
    state: MlComponentState.INSTALLED,
    version: "v0.8.20",
    via: "forge",
    namespace: "forge-ml",
    declaration: install("node-problem-detector", "v0.8.20", "forge-ml", "\n    gpu_log_rules = True"),
    note: "Installed by forge with its GPU log rule: an NVRM Xid in the kernel log raises the node's GPUXidError condition.",
  });
  if (clusterId === ML_TRAIN_CLUSTER) {
    return [
      ...operator,
      npd,
      c({ name: "kueue", category: "scheduling", state: MlComponentState.ADOPTED, version: "v0.9.1", via: "Helm release kueue", namespace: "kueue-system", declaration: adopt("kueue", "kueue-system"), note: "Admits training runs to the h100 ClusterQueue; forge launches through it." }),
      c({ name: "jobset", category: "training", state: MlComponentState.INSTALLED, version: "v0.7.2", via: "forge", namespace: "jobset-system", declaration: install("jobset", "v0.7.2", "jobset-system"), note: "Not found, so forge installed it from the pinned upstream chart." }),
      c({ name: "kubeflow-trainer", category: "training", state: MlComponentState.INSTALLED, version: "v2.0.0", via: "forge", namespace: "kubeflow-system", declaration: install("kubeflow-trainer", "v2.0.0", "kubeflow-system"), note: "Runs TrainJobs over JobSet." }),
    ];
  }
  return [
    ...operator,
    npd,
    c({ name: "vllm", category: "serving", state: MlComponentState.ADOPTED, version: "0.6.3", via: "Deployment serving/support-assistant", namespace: "serving", declaration: adopt("vllm", "serving"), note: "Plain Deployments, no serving framework: forge reads their pods and vLLM's /metrics." }),
    c({ name: "kserve", category: "serving", state: MlComponentState.MISSING, version: "", via: "", namespace: "", declaration: install("kserve", "v0.14.1", "kserve"), note: "Not found. vLLM runs as plain Deployments, which forge serves as they are; KServe is optional." }),
    c({ name: "kubeflow-trainer", category: "training", state: MlComponentState.MISSING, version: "", via: "", namespace: "", declaration: install("kubeflow-trainer", "v2.0.0", "kubeflow-system"), note: "Not found: this cluster only serves. Declare it to train here." }),
  ];
}

function stack(): ComponentInit[] {
  return [...stackOf(ML_TRAIN_CLUSTER), ...stackOf(ML_SERVE_CLUSTER)];
}

// ── Findings ────────────────────────────────────────────────────────────────

/** Where a new support-assistant replica's start goes (14 recent starts): the pod-start finding's and the inference service's cold start. */
export const ML_POD_START: BreakdownInit = {
  workload: "serving/support-assistant",
  sampleCount: 14,
  stages: [
    { name: "scheduling", p50Seconds: 4, p95Seconds: 41 },
    { name: "image pull", p50Seconds: 96, p95Seconds: 210 },
    { name: "container start", p50Seconds: 3, p95Seconds: 6 },
    { name: "model load", p50Seconds: 58, p95Seconds: 80 },
    { name: "ready", p50Seconds: 2, p95Seconds: 3 },
  ],
};

// The fixes that are not a command. forge has no ML declarations yet, so each is
// what an operator applies today: a Kubernetes manifest, a pod-template patch,
// or the kubelet's own config. (MlFinding.fix_kcl carries them; the page
// labels a fix by what it is.)

/** pod-start-slow: pull vLLM's image onto every L4 node ahead of a scale-up. */
const PREPULL_DAEMONSET = `apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: prepull-vllm-openai
  namespace: serving
spec:
  selector:
    matchLabels:
      app: prepull-vllm-openai
  template:
    metadata:
      labels:
        app: prepull-vllm-openai
    spec:
      nodeSelector:
        eks.amazonaws.com/nodegroup: ${ML_L4_POOL}
      tolerations:
        - key: nvidia.com/gpu
          operator: Exists
          effect: NoSchedule
      initContainers:
        # Pulling the image is the point; the container exits at once.
        - name: pull
          image: vllm/vllm-openai:v0.6.3
          command: ["true"]
      containers:
        - name: pause
          image: registry.k8s.io/pause:3.10
          resources:
            requests: { cpu: 1m, memory: 8Mi }`;

/** shm-too-small: a memory-backed /dev/shm on the TrainJob's pod template. */
const SHM_PATCH = `# Merge into the trainer pod's spec: the support-assistant-sft TrainingRuntime's JobSet template,
# replicatedJobs[].template.spec.template.spec.
spec:
  volumes:
    - name: dshm
      emptyDir:
        medium: Memory
        sizeLimit: 16Gi
  containers:
    - name: trainer
      volumeMounts:
        - name: dshm
          mountPath: /dev/shm`;

/** kubelet-tracing-off: the kubelet's tracing, sent to forge's collector. */
const KUBELET_TRACING_CONFIG = `# On EKS: set it in the ${ML_L4_POOL} node group's launch template (nodeadm NodeConfig, spec.kubelet.config).
apiVersion: kubelet.config.k8s.io/v1beta1
kind: KubeletConfiguration
featureGates:
  KubeletTracing: true
tracing:
  endpoint: otel-collector.forge-system:4317
  samplingRatePerMillion: 10000`;

const SEVERITY_ORDER: Record<MlFindingSeverity, number> = {
  [MlFindingSeverity.CRITICAL]: 0,
  [MlFindingSeverity.WARNING]: 1,
  [MlFindingSeverity.INFO]: 2,
  [MlFindingSeverity.UNSPECIFIED]: 3,
};

function findings(now: number): FindingInit[] {
  const at = (ago: number) => timestampFromMs(now - ago);
  const ev = (label: string, value: string) => ({ label, value });
  const priya = demoPerson("priya")!.email;
  const list: FindingInit[] = [
    {
      id: "mlf-bad-accelerator-r4vb",
      ruleId: "bad-accelerator",
      severity: MlFindingSeverity.CRITICAL,
      title: `${ML_BAD_DEVICE} fell off the bus on ${ML_BAD_NODE}`,
      summary: "An Xid 79 means the GPU stopped answering on PCIe. The node still takes new work on its other 7 GPUs, and the eval job on it is running degraded. Cordon and drain it, then have the provider repair or replace it.",
      clusterId: ML_TRAIN_CLUSTER,
      subject: ML_BAD_NODE,
      evidence: [
        ev("Xid", "79: GPU has fallen off the bus"),
        ev("Device", `${ML_BAD_DEVICE} (PCI 0000:9a:00.0)`),
        ev("Kernel log", "NVRM: Xid (PCI:0000:9a:00): 79, pid='<unknown>', name=<unknown>, GPU has fallen off the bus."),
        ev("Condition", "GPUXidError=True (node-problem-detector)"),
        ev("Allocatable", "7 of 8 nvidia.com/gpu"),
        ev("Cordoned", "no"),
        ev("Running on it", "ml-train/support-assistant-eval-0 (4 GPUs)"),
      ],
      fixKcl: "",
      fixCommand: `kubectl cordon ${ML_BAD_NODE} && kubectl drain ${ML_BAD_NODE} --ignore-daemonsets --delete-emptydir-data`,
      firstSeenAt: at(XID_AGO),
      lastSeenAt: at(MINUTE),
    },
    {
      id: "mlf-accel-idle-jupyter-priya-0",
      ruleId: "accel-idle",
      severity: MlFindingSeverity.WARNING,
      title: `2 L4 GPUs allocated and idle on ${ML_IDLE_NODE}`,
      summary: `${ML_IDLE_POD.namespace}/${ML_IDLE_POD.pod} has held 2 GPUs at 0% utilisation for 19 hours. Scale it down, or give it a time-sliced GPU.`,
      clusterId: ML_SERVE_CLUSTER,
      subject: ML_IDLE_NODE,
      evidence: [
        ev("Pod", ML_IDLE_POD.pod),
        ev("Namespace", ML_IDLE_POD.namespace),
        ev("Owner", `StatefulSet jupyter-priya (created by ${priya})`),
        ev("GPUs", `2 × ${ML_L4}`),
        ev("Utilisation", "0% for 19h (DCGM_FI_DEV_GPU_UTIL)"),
        ev("Pool", `${ML_L4_POOL}: 8 of 12 allocated, 2 of them idle`),
      ],
      fixKcl: "",
      fixCommand: `kubectl -n ${ML_IDLE_POD.namespace} scale statefulset jupyter-priya --replicas=0`,
      firstSeenAt: at(NOTEBOOK_AGO - HOUR),
      lastSeenAt: at(MINUTE),
    },
    {
      id: "mlf-pod-start-slow-support-assistant",
      ruleId: "pod-start-slow",
      severity: MlFindingSeverity.WARNING,
      title: "New support-assistant replicas take 5m40s to become Ready (p95)",
      summary: "Image pull is 62% of a replica's start: vllm/vllm-openai:v0.6.3 is 9.4 GB and the L4 nodes pull it cold. Pre-pull it on the pool with a DaemonSet, so a new replica starts from a warm image.",
      clusterId: ML_SERVE_CLUSTER,
      subject: "serving/support-assistant",
      evidence: [
        ev("Pod start p95", "340s (p50 163s), over 14 starts"),
        ev("Largest stage", "image pull: 96s p50, 210s p95"),
        ev("Image", "vllm/vllm-openai:v0.6.3 (9.4 GB)"),
        ev("Model load", "58s p50, 80s p95 (16 GB from the registry)"),
      ],
      fixKcl: PREPULL_DAEMONSET,
      fixCommand: "",
      firstSeenAt: at(30 * HOUR),
      lastSeenAt: at(RUNNING["env-prod"].age - 2 * MINUTE),
      podStart: ML_POD_START,
    },
    {
      id: "mlf-shm-too-small-support-assistant-sft",
      ruleId: "shm-too-small",
      severity: MlFindingSeverity.WARNING,
      title: "support-assistant-sft runs with a 64Mi /dev/shm",
      summary: "PyTorch DataLoader workers share batches through /dev/shm. At the container default (64Mi) the v2 run's first attempt died with a Bus error; later runs set dataloader_workers=0, which costs about 18% in step time.",
      clusterId: ML_TRAIN_CLUSTER,
      subject: "template support-assistant-sft",
      evidence: [
        ev("/dev/shm", "64Mi (the container default)"),
        ev("Failed run", `${ML_RUN_IDS.failed}: RuntimeError: DataLoader worker (pid 412) is killed by signal: Bus error.`),
        ev("Workaround in use", "dataloader_workers=0 (step time +18%)"),
      ],
      fixKcl: SHM_PATCH,
      fixCommand: "",
      firstSeenAt: at(67 * HOUR),
      lastSeenAt: at(RUN_V3_AGO),
    },
    {
      id: "mlf-dcgm-profiling-off-train",
      ruleId: "dcgm-profiling-off",
      severity: MlFindingSeverity.INFO,
      title: "DCGM profiling metrics are off",
      summary: "The adopted DCGM exporter collects the default counters only, so SM occupancy, tensor-core activity and NVLink bandwidth are not recorded. Utilisation alone cannot tell a memory-bound run from a compute-bound one.",
      clusterId: ML_TRAIN_CLUSTER,
      subject: "gpu-operator/nvidia-dcgm-exporter",
      evidence: [ev("Counters", "default-counters.csv"), ev("Missing", "DCGM_FI_PROF_SM_ACTIVE, DCGM_FI_PROF_PIPE_TENSOR_ACTIVE, DCGM_FI_PROF_NVLINK_TX_BYTES"), ev("Owner", GPU_OPERATOR)],
      fixKcl: "",
      fixCommand: "helm upgrade gpu-operator nvidia/gpu-operator -n gpu-operator --reuse-values --set dcgmExporter.config.name=dcgm-profiling-counters",
      firstSeenAt: at(12 * DAY),
      lastSeenAt: at(MINUTE),
    },
    {
      id: "mlf-kubelet-tracing-off-serve",
      ruleId: "kubelet-tracing-off",
      severity: MlFindingSeverity.INFO,
      title: "Kubelet tracing is off",
      summary: "Without the KubeletTracing feature gate, a pod's start is measured from its events only: image pull and container start cannot be split into their spans, and a slow start has no trace.",
      clusterId: ML_SERVE_CLUSTER,
      subject: ML_L4_POOL,
      evidence: [ev("Feature gate", "KubeletTracing=false"), ev("Kubelet", "v1.30.4-eks-a737599"), ev("Nodes", `${ML_L4_NODES.length} in ${ML_L4_POOL}`)],
      fixKcl: KUBELET_TRACING_CONFIG,
      fixCommand: "",
      firstSeenAt: at(12 * DAY),
      lastSeenAt: at(MINUTE),
    },
  ];
  const ms = (f: FindingInit) => Number(f.lastSeenAt?.seconds ?? 0);
  return list.sort((a, b) => SEVERITY_ORDER[a.severity!] - SEVERITY_ORDER[b.severity!] || ms(b) - ms(a));
}

// ── Runs ────────────────────────────────────────────────────────────────────

/** Seconds per training step on 16 H100s (dataloader_workers=0). */
const STEP_SECONDS = 12;
const TOTAL_STEPS = 5400;

const lossAt = (step: number, floor: number) => floor + 0.95 * Math.exp(-step / 450) + 0.015 * Math.sin(step / 37);

/** A run's metric series from `start`, every `every` steps up to `last`. */
function seriesOf(start: number, last: number, every: number, lossFloor: number, utilization: number): SeriesInit[] {
  const steps = Array.from({ length: Math.floor(last / every) + 1 }, (_, i) => i * every);
  const point = (step: number, value: number) => ({ step: BigInt(step), time: timestampFromMs(start + step * STEP_SECONDS * 1000), value: Math.round(value * 1000) / 1000 });
  return [
    { name: "loss", unit: "", points: steps.map((s) => point(s, lossAt(s, lossFloor))) },
    { name: "eval/loss", unit: "", points: steps.filter((s) => s % 500 === 0).map((s) => point(s, lossAt(s, lossFloor) + 0.07)) },
    { name: "accelerator_utilization", unit: "%", points: steps.map((s, i) => point(s, s === 0 ? 0 : utilization + wiggle(i, 1.5))) },
  ];
}

function runs(app: string, now: number): RunsInit {
  const maya = demoPerson("maya")!.email;
  const params = (version: string, workers: string) => ({
    base_model: "meta-llama/Llama-3.1-8B-Instruct",
    dataset: `gs://acme-ml/datasets/support-tickets/${version}`,
    learning_rate: "2e-5",
    epochs: "3",
    global_batch_size: "128",
    max_seq_len: "4096",
    dataloader_workers: workers,
  });
  const base = { project: app, template: "support-assistant-sft", orchestrator: "TrainJob", clusterId: ML_TRAIN_CLUSTER, launchedBy: maya, acceleratorCount: 16, acceleratorProduct: ML_H100 };
  const v3Start = now - RUN_V3_AGO;
  const v3Steps = Math.floor(RUN_V3_AGO / 1000 / STEP_SECONDS);
  const v2Start = now - 48 * HOUR;
  const v2End = v2Start + TOTAL_STEPS * STEP_SECONDS * 1000;
  const failedStart = now - 72 * HOUR;
  return {
    runs: [
      { ...base, id: ML_RUN_IDS.running, name: "llama-3.1-8b-support-sft-v3", state: MlRunState.RUNNING, params: { ...params("2026-11-v3", "0"), total_steps: String(TOTAL_STEPS) }, startedAt: timestampFromMs(v3Start), metrics: seriesOf(v3Start, Math.floor(v3Steps / 100) * 100, 100, 0.93, 93) },
      { ...base, id: ML_RUN_IDS.succeeded, name: "llama-3.1-8b-support-sft-v2", state: MlRunState.SUCCEEDED, params: { ...params("2026-11-v2", "0"), total_steps: String(TOTAL_STEPS) }, startedAt: timestampFromMs(v2Start), finishedAt: timestampFromMs(v2End), metrics: seriesOf(v2Start, TOTAL_STEPS, 150, 0.97, 92), outputModelDigest: ML_MODEL_DIGESTS.v2 },
      // v2's first attempt: preempted once by Kueue, restarted three times, and killed by a Bus error in a DataLoader worker (64Mi /dev/shm).
      { ...base, id: ML_RUN_IDS.failed, name: "llama-3.1-8b-support-sft-v2-attempt-1", state: MlRunState.FAILED, params: { ...params("2026-11-v2", "8"), total_steps: String(TOTAL_STEPS) }, startedAt: timestampFromMs(failedStart), finishedAt: timestampFromMs(now - 67 * HOUR), restarts: 3, preemptions: 1, metrics: seriesOf(failedStart, 800, 100, 0.97, 81) },
    ],
  };
}

// ── Models ──────────────────────────────────────────────────────────────────

function models(app: string, now: number): ModelsInit {
  const v2Created = now - 48 * HOUR + TOTAL_STEPS * STEP_SECONDS * 1000 + 6 * MINUTE;
  return {
    models: [
      {
        name: ML_MODEL_NAME,
        project: app,
        repository: ML_MODEL_REPOSITORY,
        versions: [
          { digest: ML_MODEL_DIGESTS.v2, tag: "v2", createdAt: timestampFromMs(v2Created), runId: ML_RUN_IDS.succeeded, sizeBytes: 16_060_522_496n, evalMetrics: { "eval/loss": 1.041, "eval/accuracy": 0.871, rouge_l: 0.462 }, servingIn: [`${app}/prod/${ML_INFERENCE_SERVICE}`] },
          // Pushed by hand, before forge trained it: no run.
          { digest: ML_MODEL_DIGESTS.v1, tag: "v1", createdAt: timestampFromMs(now - 12 * DAY), runId: "", sizeBytes: 16_060_518_400n, evalMetrics: { "eval/loss": 1.214, "eval/accuracy": 0.834, rouge_l: 0.418 }, servingIn: [] },
        ],
      },
    ],
  };
}

// ── Inference ───────────────────────────────────────────────────────────────

function inference(app: string, now: number): InferenceInit {
  const load = dailyLoad(now);
  const prod = RUNNING["env-prod"];
  return {
    services: [
      {
        name: ML_INFERENCE_SERVICE,
        project: app,
        environment: "prod",
        clusterId: ML_SERVE_CLUSTER,
        runtime: ML_VLLM_RUNTIME,
        release: prod.version,
        modelDigest: ML_MODEL_DIGESTS.v2,
        replicas: 3,
        ttftP95Ms: Math.round(380 + 60 * load),
        queueWaitP95Ms: Math.round(12 + 30 * load),
        tokensPerSecond: Math.round(3600 * load),
        acceleratorUtilizationPercent: round1(VLLM_PEAK_UTIL * load),
        kvCacheUsagePercent: round1(30 + 40 * load),
        coldStart: ML_POD_START,
        releaseVerdict: MlReleaseVerdict.PASS,
        releaseVerdictSummary: ML_RELEASE_SUMMARY,
        releaseCheckedAt: timestampFromMs(now - prod.age + 15 * MINUTE),
      },
    ],
  };
}

// ── The answers ─────────────────────────────────────────────────────────────

const onCluster = <T extends { clusterId?: string }>(items: T[], clusterId: string | undefined) => (clusterId ? items.filter((i) => i.clusterId === clusterId) : items);

/** MlEarlyAccessService's answers for `app`, on the pack clock (`now`). A `clusterId` narrows to one cluster. */
export function shopMlAnswers(app: string, now: () => number = demoNow) {
  return {
    machines: (clusterId?: string): MachinesInit => {
      const all = machines(now());
      return { ...all, clusters: onCluster(all.clusters, clusterId), pools: onCluster(all.pools, clusterId) };
    },
    stack: (clusterId?: string): StackInit => ({ components: onCluster(stack(), clusterId) }),
    findings: (clusterId?: string): FindingsInit => ({ findings: onCluster(findings(now()), clusterId) }),
    runs: (): RunsInit => runs(app, now()),
    models: (): ModelsInit => models(app, now()),
    inference: (): InferenceInit => inference(app, now()),
  };
}
