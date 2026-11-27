// Copyright (c) 2026 Reliant Labs

/**
 * The ML fixture world's SETUP: GetMlStack, the per-cluster coverage
 * checklist discovery found (world.ts tells the story). Each row says who
 * covers an area (adopted: found and theirs; installed by forge: declared in
 * KCL; missing; partial), with the KCL that declares it and, for a gap, the
 * finding that fixes it (findings.ts). The training cluster is well covered;
 * the serving cluster lacks DCGM profiling counters, NPD's GPU rules,
 * continuous profiling, and tracing for the reranker.
 */

import { MlComponentState, MlHealth } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";

import type { MlDomain } from "./domain";
import { fleetNodes } from "./fleet";
import { at, CLUSTERS, FINDINGS, HOUR, MINUTE } from "./world";

type State = MlComponentState;
const { ADOPTED, INSTALLED, MISSING, PARTIAL, NOT_APPLICABLE } = MlComponentState;

interface Item {
  key: string;
  title: string;
  state: State;
  component: string;
  version: string;
  namespace: string;
  detail: string;
  kcl: string;
  findingId?: string;
}

const kcl = (lines: string[]) => lines.join("\n");

function trainItems(): Item[] {
  const c = CLUSTERS.train.id;
  return [
    {
      key: "accelerator-metrics",
      title: "Accelerator metrics",
      state: ADOPTED,
      component: "DCGM exporter (GPU Operator v24.9.1)",
      version: "3.3.9-3.6.1",
      namespace: "gpu-operator",
      detail: "DaemonSet nvidia-dcgm-exporter on 11 GPU nodes, scraped every 15s; KUBERNETES=true with the PodResources mount, so every series carries pod and namespace.",
      kcl: kcl([
        `ml = forge.MLStack {`,
        `    cluster = "${c}"`,
        `    accelerator_metrics = forge.Adopt {`,
        `        kind = "DaemonSet"`,
        `        namespace = "gpu-operator"`,
        `        name = "nvidia-dcgm-exporter"`,
        `        scrape = { port = 9400, interval = "15s" }`,
        `    }`,
        `}`,
      ]),
    },
    {
      key: "profiling-counters",
      title: "Profiling counters (DCP)",
      state: ADOPTED,
      component: "DCGM exporter dcp-metrics-included.csv",
      version: "3.3.9",
      namespace: "gpu-operator",
      detail: "DCGM_FI_PROF_* (SM active, tensor, DRAM, PCIe, NVLink bytes) at 1 Hz; SM occupancy and the FP pipes added in a custom collectors CSV.",
      kcl: kcl([`accelerator_metrics = forge.Adopt {`, `    # …`, `    counters = forge.DcgmCounters { profile = "dcp", extra = ["DCGM_FI_PROF_SM_OCCUPANCY", "DCGM_FI_PROF_PIPE_FP16_ACTIVE"] }`, `}`]),
    },
    {
      key: "pod-attribution",
      title: "Pod attribution",
      state: ADOPTED,
      component: "kubelet PodResources API",
      version: "v1",
      namespace: "gpu-operator",
      detail: "/var/lib/kubelet/pod-resources mounted into the exporter; allocated vs idle per pod comes from here.",
      kcl: "",
    },
    {
      key: "node-health",
      title: "Node health",
      state: INSTALLED,
      component: "node-problem-detector + GPU rules",
      version: "v0.8.20",
      namespace: "forge-system",
      detail: "kernel, docker, systemd monitors plus forge's gpu-xid-monitor and gpu-health custom plugin (thermal, NVLink, row remap). GKE's own NPD stays; forge's runs beside it with only the GPU rules.",
      kcl: kcl([`node_health = forge.Install {`, `    component = "node-problem-detector"`, `    version = "v0.8.20"`, `    rules = ["kernel", "gpu-xid", "gpu-health"]`, `}`]),
    },
    {
      key: "events",
      title: "Kubernetes events",
      state: INSTALLED,
      component: "OTel Collector k8sobjects receiver",
      version: "0.114.0",
      namespace: "forge-system",
      detail: "Events, Kueue Workload status and node conditions, kept 30 days.",
      kcl: kcl([`events = forge.Install { component = "otel-k8s-events", version = "0.114.0" }`]),
    },
    {
      key: "queueing",
      title: "Queueing",
      state: ADOPTED,
      component: "Kueue",
      version: "0.10.1",
      namespace: "kueue-system",
      detail: "ClusterQueues team-research, team-serving-batch (cohort ml-h100) and eval (ml-a100); metrics on :8443 with waitForPodsReady on.",
      kcl: kcl([`queueing = forge.Adopt {`, `    kind = "Deployment"`, `    namespace = "kueue-system"`, `    name = "kueue-controller-manager"`, `}`]),
    },
    {
      key: "jobset",
      title: "Multi-node jobs",
      state: INSTALLED,
      component: "JobSet",
      version: "0.7.1",
      namespace: "jobset-system",
      detail: "Each phase attempt is one JobSet (one Job per node, 8 ranks each) admitted by Kueue as one Workload.",
      kcl: kcl([`jobset = forge.Install { component = "jobset", version = "0.7.1" }`]),
    },
    {
      key: "trainer",
      title: "Training operator",
      state: INSTALLED,
      component: "Kubeflow Trainer v2",
      version: "2.0.0",
      namespace: "kubeflow-system",
      detail: "TrainJob → JobSet with torchrun rendezvous; forge sets RUN_ID, PHASE, ATTEMPT and the mesh as env.",
      kcl: kcl([`trainer = forge.Install { component = "kubeflow-trainer", version = "2.0.0" }`]),
    },
    {
      key: "serving-metrics",
      title: "Serving metrics",
      state: NOT_APPLICABLE,
      component: "",
      version: "",
      namespace: "",
      detail: "Nothing serves on this cluster.",
      kcl: "",
    },
    {
      key: "training-instrumentation",
      title: "Training instrumentation",
      state: INSTALLED,
      component: "reliant-ml Python SDK (HF Trainer, torchtitan callbacks)",
      version: "0.4.2",
      namespace: "ml-research",
      detail: "Every forge-launched run reports step stats, timers, loss/grad norm, goodput and checkpoints with the training id on each sample.",
      kcl: kcl([`training = forge.MLTraining { sdk = "reliant-ml==0.4.2", inject = true }`]),
    },
    {
      key: "tracing",
      title: "Tracing",
      state: INSTALLED,
      component: "OTel Collector (traces → ClickHouse)",
      version: "0.114.0",
      namespace: "forge-system",
      detail: "Step, data, collective and checkpoint spans carry run, phase, attempt, rank, node and GPU UUID.",
      kcl: kcl([`tracing = forge.Install { component = "otel-collector", version = "0.114.0", sampling = 0.05 }`]),
    },
    {
      key: "profiling",
      title: "Continuous profiling",
      state: INSTALLED,
      component: "eBPF CPU profiler + CUPTI GPU sampler",
      version: "0.6.0",
      namespace: "forge-system",
      detail: "CPU stacks at 19 Hz on every node; CUPTI kernel sampling 1% on training pods; Kineto traces on demand.",
      kcl: kcl([`profiling = forge.Install { component = "forge-profiler", version = "0.6.0", gpu = "cupti", cpu_hz = 19 }`]),
    },
    {
      key: "kubelet-metrics",
      title: "Kubelet metrics",
      state: ADOPTED,
      component: "kubelet /metrics, /metrics/cadvisor",
      version: CLUSTERS.train.kubernetesVersion,
      namespace: "kube-system",
      detail: "Pod start SLIs, image pull durations, CRI errors, evictions; cAdvisor container CPU throttling and memory.",
      kcl: "",
    },
    {
      key: "kubelet-tracing",
      title: "Kubelet tracing",
      state: MISSING,
      component: "KubeletConfiguration tracing",
      version: "",
      namespace: "",
      detail: "No OTLP endpoint in the kubelet config, so pod start (sandbox → pull → start) has no spans.",
      kcl: "",
      findingId: FINDINGS.kubeletTracingOff,
    },
    {
      key: "node-exporter",
      title: "Host metrics",
      state: ADOPTED,
      component: "node-exporter (GKE managed Prometheus)",
      version: "1.8.2",
      namespace: "gmp-public",
      detail: "CPU, memory, PSI, filesystems (incl. image fs and /dev/shm), network.",
      kcl: kcl([`host_metrics = forge.Adopt { kind = "DaemonSet", namespace = "gmp-public", name = "node-exporter" }`]),
    },
    {
      key: "infiniband",
      title: "InfiniBand fabric",
      state: INSTALLED,
      component: "node-exporter infiniband collector (ConnectX-7)",
      version: "1.8.2",
      namespace: "forge-system",
      detail: "8× mlx5 ports per H100 node: data, symbol errors, link downed, xmit wait.",
      kcl: kcl([`fabric = forge.Install { component = "node-exporter", collectors = ["infiniband"] }`]),
    },
  ];
}

function serveItems(): Item[] {
  const c = CLUSTERS.serve.id;
  return [
    {
      key: "accelerator-metrics",
      title: "Accelerator metrics",
      state: ADOPTED,
      component: "DCGM exporter (GPU Operator v24.6.2)",
      version: "3.3.7-3.5.0",
      namespace: "gpu-operator",
      detail: "DaemonSet nvidia-dcgm-exporter on 4 L4 nodes with the default counters CSV.",
      kcl: kcl([`ml = forge.MLStack {`, `    cluster = "${c}"`, `    accelerator_metrics = forge.Adopt {`, `        kind = "DaemonSet"`, `        namespace = "gpu-operator"`, `        name = "nvidia-dcgm-exporter"`, `    }`, `}`]),
    },
    {
      key: "profiling-counters",
      title: "Profiling counters (DCP)",
      state: MISSING,
      component: "DCGM exporter counters CSV",
      version: "",
      namespace: "gpu-operator",
      detail: "The exporter runs default-counters.csv: no DCGM_FI_PROF_* fields, so SM active, tensor and DRAM activity are blind on every L4.",
      kcl: "",
      findingId: FINDINGS.dcgmProfilingOff,
    },
    {
      key: "pod-attribution",
      title: "Pod attribution",
      state: ADOPTED,
      component: "kubelet PodResources API",
      version: "v1",
      namespace: "gpu-operator",
      detail: "Pod and namespace labels on every DCGM series.",
      kcl: "",
    },
    {
      key: "node-health",
      title: "Node health",
      state: PARTIAL,
      component: "EKS node monitoring agent + node-problem-detector",
      version: "v0.8.19",
      namespace: "kube-system",
      detail: "Kernel, docker and systemd monitors run; no Xid, thermal or NVLink rules, so a GPU fault shows only when a pod fails.",
      kcl: kcl([`node_health = forge.Adopt { kind = "DaemonSet", namespace = "kube-system", name = "node-problem-detector" }`]),
      findingId: FINDINGS.npdGpuRulesMissing,
    },
    {
      key: "events",
      title: "Kubernetes events",
      state: INSTALLED,
      component: "OTel Collector k8sobjects receiver",
      version: "0.114.0",
      namespace: "forge-system",
      detail: "Events and node conditions, kept 30 days.",
      kcl: kcl([`events = forge.Install { component = "otel-k8s-events", version = "0.114.0" }`]),
    },
    {
      key: "queueing",
      title: "Queueing",
      state: NOT_APPLICABLE,
      component: "",
      version: "",
      namespace: "",
      detail: "Online serving scales by HPA; no batch queue on this cluster.",
      kcl: "",
    },
    {
      key: "serving-metrics",
      title: "Serving metrics",
      state: PARTIAL,
      component: "vLLM /metrics (adopted) · Triton :8002 (adopted)",
      version: "vLLM 0.8.5 · Triton 24.10",
      namespace: "ml-serving",
      detail: "vLLM: TTFT, ITL, queue, KV cache, prefix-cache hits. Triton (reranker): request counts and queue time only; no per-request tracing.",
      kcl: kcl([`serving_metrics = forge.Adopt { selector = { "app.kubernetes.io/component" = "inference" }, ports = ["metrics", "triton-metrics"] }`]),
      findingId: FINDINGS.inferenceTracingOff,
    },
    {
      key: "training-instrumentation",
      title: "Training instrumentation",
      state: PARTIAL,
      component: "reliant-ml Python SDK",
      version: "0.4.2",
      namespace: "ml-search",
      detail: "The reranker's nightly fine-tune (CronJob reranker-nightly) runs outside forge: observed by its pods, no step stats or checkpoints.",
      kcl: "",
      findingId: FINDINGS.trainingUntracked,
    },
    {
      key: "tracing",
      title: "Tracing",
      state: PARTIAL,
      component: "OTel Collector",
      version: "0.114.0",
      namespace: "forge-system",
      detail: "support-assistant (vLLM, OTLP) traces every request at 5%; reranker-prod (Triton) sends none.",
      kcl: kcl([`tracing = forge.Install { component = "otel-collector", version = "0.114.0", sampling = 0.05 }`]),
      findingId: FINDINGS.inferenceTracingOff,
    },
    {
      key: "profiling",
      title: "Continuous profiling",
      state: MISSING,
      component: "eBPF CPU profiler + CUPTI GPU sampler",
      version: "",
      namespace: "",
      detail: "Not installed: no CPU stacks for the tokenizer or the API server, no kernel sampling on the L4s.",
      kcl: "",
    },
    {
      key: "kubelet-metrics",
      title: "Kubelet metrics",
      state: ADOPTED,
      component: "kubelet /metrics, /metrics/cadvisor",
      version: CLUSTERS.serve.kubernetesVersion,
      namespace: "kube-system",
      detail: "Pod start SLIs and image pull durations (by image size).",
      kcl: "",
    },
    {
      key: "kubelet-tracing",
      title: "Kubelet tracing",
      state: MISSING,
      component: "KubeletConfiguration tracing",
      version: "",
      namespace: "",
      detail: "No OTLP endpoint in the kubelet config: a replica's cold start has no sandbox/pull/start spans.",
      kcl: "",
      findingId: FINDINGS.kubeletTracingOff,
    },
    {
      key: "node-exporter",
      title: "Host metrics",
      state: INSTALLED,
      component: "node-exporter",
      version: "1.8.2",
      namespace: "forge-system",
      detail: "CPU, memory, PSI, filesystems, network.",
      kcl: kcl([`host_metrics = forge.Install { component = "node-exporter", version = "1.8.2" }`]),
    },
    {
      key: "infiniband",
      title: "InfiniBand fabric",
      state: NOT_APPLICABLE,
      component: "",
      version: "",
      namespace: "",
      detail: "g6 instances have no InfiniBand or EFA.",
      kcl: "",
    },
  ];
}

const isCovered = (s: State) => s === ADOPTED || s === INSTALLED;
/** Covered over applicable; a partial item counts half. */
export function coverageRatio(states: readonly State[]): number {
  const applicable = states.filter((s) => s !== NOT_APPLICABLE && s !== MlComponentState.UNSPECIFIED);
  if (!applicable.length) return 1;
  return applicable.reduce((sum, s) => sum + (isCovered(s) ? 1 : s === PARTIAL ? 0.5 : 0), 0) / applicable.length;
}

export const STACK_ITEMS = { train: trainItems, serve: serveItems };

function clusterSetup(which: "train" | "serve", now: () => number) {
  const c = CLUSTERS[which];
  const items = STACK_ITEMS[which]();
  const nodes = fleetNodes().filter((n) => n.pool.clusterId === c.id);
  const products = new Map<string, number>();
  for (const n of nodes) products.set(n.pool.product, (products.get(n.pool.product) ?? 0) + n.devices.length);
  const totalGpus = [...products.values()].reduce((a, b) => a + b, 0);
  const crds =
    which === "train"
      ? [
          { name: "clusterqueues.kueue.x-k8s.io", version: "v1beta1", owner: "Kueue 0.10.1 (adopted)" },
          { name: "localqueues.kueue.x-k8s.io", version: "v1beta1", owner: "Kueue 0.10.1 (adopted)" },
          { name: "workloads.kueue.x-k8s.io", version: "v1beta1", owner: "Kueue 0.10.1 (adopted)" },
          { name: "resourceflavors.kueue.x-k8s.io", version: "v1beta1", owner: "Kueue 0.10.1 (adopted)" },
          { name: "jobsets.jobset.x-k8s.io", version: "v1alpha2", owner: "JobSet 0.7.1 (forge)" },
          { name: "trainjobs.trainer.kubeflow.org", version: "v1alpha1", owner: "Trainer 2.0.0 (forge)" },
          { name: "clusterpolicies.nvidia.com", version: "v1", owner: "GPU Operator v24.9.1 (adopted)" },
          { name: "podmonitors.monitoring.googleapis.com", version: "v1", owner: "GKE managed Prometheus" },
        ]
      : [
          { name: "clusterpolicies.nvidia.com", version: "v1", owner: "GPU Operator v24.6.2 (adopted)" },
          { name: "nodepools.karpenter.sh", version: "v1", owner: "Karpenter 1.1.0 (adopted)" },
          { name: "ec2nodeclasses.karpenter.k8s.aws", version: "v1", owner: "Karpenter 1.1.0 (adopted)" },
          { name: "servicemonitors.monitoring.coreos.com", version: "v1", owner: "prometheus-operator 0.78 (adopted)" },
        ];
  const unhealthy = nodes.some((n) => n.health === MlHealth.FAULTY) ? MlHealth.FAULTY : nodes.some((n) => n.health === MlHealth.DEGRADED) ? MlHealth.DEGRADED : MlHealth.HEALTHY;
  return {
    cluster: {
      id: c.id,
      name: c.name,
      provider: c.provider,
      location: c.location,
      kubernetesVersion: c.kubernetesVersion,
      lastSeenAt: at(-40_000, now),
      nodeCount: nodes.length,
      gpuCount: totalGpus,
      health: unhealthy,
    },
    coverage: items.map((i) => ({ ...i, findingId: i.findingId ?? "" })),
    inventory: {
      nodeCount: nodes.length,
      products: [...products.entries()].map(([name, v]) => ({ name, value: v, share: v / totalGpus })),
      driverVersions: [...new Set(nodes.map((n) => `${n.pool.driver} (CUDA ${n.pool.cuda})`))],
      crds,
    },
    discoveredAt: at(which === "train" ? -3 * HOUR - 12 * MINUTE : -27 * HOUR, now),
    discoverCommand: `reliant forge ml discover --cluster ${c.id}`,
    coverageRatio: coverageRatio(items.map((i) => i.state)),
  };
}

export const setupAnswers: MlDomain = {
  getMlStack: (req, now) => {
    const which = (["train", "serve"] as const).filter((w) => !req.clusterId || CLUSTERS[w].id === req.clusterId);
    if (!which.length) return undefined;
    return { clusters: which.map((w) => clusterSetup(w, now)) };
  },
};
