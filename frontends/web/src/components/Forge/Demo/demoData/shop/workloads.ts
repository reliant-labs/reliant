// Copyright (c) 2025 Reliant Labs

/**
 * shop's workloads (ListWorkloads / GetWorkload): api, worker and inventory
 * in each environment, every one healthy, on the release its environment
 * runs (RUNNING), at the replica count its release declares
 * (consoleScenario DEMO_WORKLOADS), with the declared spec, bundle and
 * commit the release ledger records — so a workload page agrees with the
 * release page, the overview's processes and the config tab. The storefront
 * (shop-web) is a static site on the edge: no pods, so no workload row.
 *
 * Resource series come from the metrics model (metrics.ts), and the only
 * events are each rollout's pods being scheduled and started.
 */

import type { ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import type { ImageChange, ResourceUsage, VersionChange, Workload, WorkloadDetail, WorkloadEvent, WorkloadJob, WorkloadMetric, WorkloadSeries } from "@/services/controlPlane/observability/types";

import { DEMO_WORKLOADS } from "../consoleScenario";
import { DEMO_BACKENDS, DEMO_ENV_IDS, DEMO_REPO, demoBundleId, demoLedger, demoRelease, MINUTE, RUNNING, type DemoEnvKey } from "../shopStory";
import { shopMetricPanel } from "./metrics";
import type { WorkloadKeys } from "./shopApi";

const MiB = 2 ** 20;
const ENV_NAME: Record<DemoEnvKey, string> = { "env-dev": "dev", "env-staging": "staging", "env-prod": "prod" };
const CPU_REQUEST: Record<string, number> = { api: 0.25, worker: 0.1, inventory: 0.1 };
const MEMORY_REQUEST = 256 * MiB;
const MEMORY_LIMIT = 512 * MiB;
const CPU_LIMIT = 1;

const envsOf = (envIds: readonly string[]) => (envIds.length ? DEMO_ENV_IDS.filter((e) => envIds.includes(e)) : [...DEMO_ENV_IDS]) as DemoEnvKey[];
const replicasOf = (workload: string) => DEMO_WORKLOADS.find((w) => w.name === workload)?.replicas ?? 1;

const podSuffix = (workload: string, env: string, version: string, i: number) => {
  let h = 0x811c9dc5;
  for (const c of `${workload}${env}${version}${i}`) h = Math.imul(h ^ c.charCodeAt(0), 0x01000193) >>> 0;
  return h.toString(36).slice(0, 5).padEnd(5, "x");
};
const replicaSet = (workload: string, version: string) => podSuffix(workload, "rs", version, 0).slice(0, 4) + "c9";

/** One metric of one workload at `now`, over the last 15 minutes: its latest and p95. */
function sample(field: string, workload: string, env: DemoEnvKey, now: number) {
  const res = shopMetricPanel({ id: "w", title: "", kind: "line", signal: "metrics", series: [{ label: "v", agg: "avg", field }] }, { startMs: now - 15 * MINUTE, endMs: now }, now, { envIds: [env], services: [workload] });
  const values = (res?.series[0]?.values ?? []).filter((v): v is number => v !== null);
  const sorted = [...values].sort((a, b) => a - b);
  return { latest: values.at(-1) ?? 0, p95: sorted[Math.floor(sorted.length * 0.95)] ?? 0 };
}

const usage = ({ latest, p95 }: { latest: number; p95: number }, request: number, limit: number): ResourceUsage => ({
  latest,
  p95,
  limit,
  request,
  limitUtilizationLatest: latest / limit,
  limitUtilizationP95: p95 / limit,
  requestUtilizationLatest: latest / request,
  requestUtilizationP95: p95 / request,
});

function workloadRow(workload: string, env: DemoEnvKey, now: number, detail: boolean): Workload {
  const running = RUNNING[env];
  const version = running.version;
  const release = demoRelease(version)!;
  const replicas = replicasOf(workload);
  const promotedAt = now - running.age;
  const image = `registry.example.com/shop/${workload}`;
  const tag = version;
  return {
    envId: env,
    envName: ENV_NAME[env],
    workload,
    namespace: `shop-${ENV_NAME[env]}`,
    serviceVersion: version,
    replicasObserved: replicas,
    restartsInWindow: 0,
    containers: [
      {
        container: workload,
        imageName: image,
        imageTag: tag,
        restartsInWindow: 0,
        lastTerminatedReason: "",
        lastTerminatedAtMs: null,
        cpu: usage(sample("container.cpu.usage", workload, env, now), CPU_REQUEST[workload] ?? 0.1, CPU_LIMIT),
        memory: usage(sample("container.memory.working_set", workload, env, now), MEMORY_REQUEST, MEMORY_LIMIT),
      },
    ],
    latestWarning: null,
    kind: "deployment",
    health: "healthy",
    healthReason: "",
    healthCause: "none",
    healthDetail: "",
    readyReplicas: replicas,
    desiredReplicas: replicas,
    releaseVersion: version,
    releaseBasis: "digest",
    pods: Array.from({ length: replicas }, (_, i) => ({
      name: `${workload}-${replicaSet(workload, version)}-${podSuffix(workload, env, version, i)}`,
      uid: `${podSuffix(workload, env, version, i)}-${i}`,
      state: "running" as const,
      restarts: 0,
      lastExitReason: "",
      lastExitAtMs: null,
      node: `pool-shop-${["a", "b", "c"][i % 3]}${(i + 1) * 3}`,
      firstSeenMs: promotedAt + (i + 1) * 20_000,
      lastSeenMs: now,
      ageIsLowerBound: false,
      latestWarning: null,
    })),
    declared: detail
      ? {
          name: workload,
          kind: "deployment",
          specRecorded: true,
          specGap: null,
          replicas,
          cpuRequestMillicores: Math.round((CPU_REQUEST[workload] ?? 0.1) * 1000),
          cpuLimitMillicores: CPU_LIMIT * 1000,
          memoryRequestBytes: MEMORY_REQUEST,
          memoryLimitBytes: MEMORY_LIMIT,
          ports: workload === "worker" ? [] : [{ name: "http", port: 8080, protocol: "TCP" }],
          envNames: ["LOG_LEVEL", "ORDER_TIMEOUT_MS", "DATABASE_URL", ...(workload === "api" ? ["PAYMENTS_API_KEY", "SESSION_SIGNING_KEY"] : []), ...(workload !== "inventory" ? ["NATS_CREDENTIALS"] : [])],
          image: `${image}:${tag}`,
          sourcePath: `deploy/kcl/${ENV_NAME[env]}/main.k`,
          sourceRepo: DEMO_REPO,
          sourceCommit: release.commit,
          bundleId: demoBundleId(env, version),
        }
      : null,
  };
}

/** The migration job each release that changed the schema ran, per environment. */
function jobsOf(envs: DemoEnvKey[], now: number): WorkloadJob[] {
  return demoLedger(now)
    .filter((p) => envs.includes(p.envId) && !p.targets.length && p.kind === "promote" && (p.version === "v1.8.2" || p.version === "v1.8.3"))
    .map((p) => ({
      envId: p.envId,
      envName: p.envName,
      name: `shop-migrate-${p.version.replace(/\./g, "")}-${podSuffix("migrate", p.envId, p.promotionId, 0)}`,
      workload: "shop-migrate",
      result: "succeeded" as const,
      reason: "",
      releaseVersion: p.version,
      startedAtMs: p.atMs - 90_000,
      finishedAtMs: p.atMs - 90_000 + (p.version === "v1.8.2" ? 41_000 : 12_000),
      durationSeconds: p.version === "v1.8.2" ? 41 : 12,
    }))
    .sort((a, b) => (b.finishedAtMs ?? 0) - (a.finishedAtMs ?? 0));
}

const SERIES: [WorkloadMetric, string, string, (v: number, w: string) => number][] = [
  ["cpu_usage", "{cpu}", "container.cpu.usage", (v) => v],
  ["cpu_request_utilization", "1", "container.cpu.usage", (v, w) => v / (CPU_REQUEST[w] ?? 0.1)],
  ["cpu_limit_utilization", "1", "container.cpu.usage", (v) => v / CPU_LIMIT],
  ["memory_working_set", "By", "container.memory.working_set", (v) => v],
  ["memory_request_utilization", "1", "container.memory.working_set", (v) => v / MEMORY_REQUEST],
  ["memory_limit_utilization", "1", "container.memory.working_set", (v) => v / MEMORY_LIMIT],
];

function detailOf(envs: DemoEnvKey[], workload: string, window: { startMs: number; endMs: number }, now: number): WorkloadDetail {
  if (!(DEMO_BACKENDS as readonly string[]).includes(workload)) return { series: [], events: [], imageChanges: [], versionChanges: [], stepSeconds: 60, truncated: false, workloads: [] };
  const rows = envs.map((env) => workloadRow(workload, env, now, true));
  const series: WorkloadSeries[] = [];
  const events: WorkloadEvent[] = [];
  const imageChanges: ImageChange[] = [];
  const versionChanges: VersionChange[] = [];
  let stepSeconds = 60;
  for (const env of envs) {
    const raw = new Map<string, { timestamps: number[]; values: (number | null)[] }>();
    for (const field of ["container.cpu.usage", "container.memory.working_set"]) {
      const res = shopMetricPanel({ id: field, title: "", kind: "line", signal: "metrics", series: [{ label: field, agg: "avg", field }] }, window, now, { envIds: [env], services: [workload] });
      stepSeconds = res?.stepSeconds ?? stepSeconds;
      raw.set(field, { timestamps: res?.series[0]?.timestamps ?? [], values: res?.series[0]?.values ?? [] });
    }
    for (const [metric, unit, field, f] of SERIES) {
      const r = raw.get(field)!;
      series.push({ envId: env, container: workload, metric, unit, timestamps: r.timestamps, values: r.values.map((v) => (v === null ? null : f(v, workload))) });
    }
    const t = raw.get("container.cpu.usage")!.timestamps;
    series.push({ envId: env, container: workload, metric: "restarts", unit: "{restart}", timestamps: t, values: t.map(() => 0) });
    // The whole-environment promotions in (or running at the start of) the window.
    const ledger = demoLedger(now).filter((p) => p.envId === env && !p.targets.length);
    ledger.forEach((p, i) => {
      const until = ledger[i + 1]?.atMs ?? now;
      if (until < window.startMs || p.atMs > window.endMs) return;
      const first = Math.max(p.atMs, window.startMs);
      const last = Math.min(until, window.endMs);
      versionChanges.push({ envId: env, serviceVersion: p.version, firstSeenMs: first, lastSeenMs: last });
      imageChanges.push({ envId: env, container: workload, imageName: `registry.example.com/shop/${workload}`, imageTag: p.version, firstSeenMs: first, lastSeenMs: last });
      if (p.atMs >= window.startMs) {
        for (let k = 0; k < replicasOf(workload); k++) {
          const pod = `${workload}-${replicaSet(workload, p.version)}-${podSuffix(workload, env, p.version, k)}`;
          const at = p.atMs + (k + 1) * 20_000;
          const base = { envId: env, type: "Normal", count: 1, objectKind: "Pod", objectName: pod };
          events.push(
            { ...base, timeMs: at, reason: "Scheduled", plainReason: "Placed on a machine", message: `Successfully assigned shop-${ENV_NAME[env]}/${pod} to pool-shop-${["a", "b", "c"][k % 3]}${(k + 1) * 3}`, container: "" },
            { ...base, timeMs: at + 2_100, reason: "Pulled", plainReason: "Image fetched", message: `Successfully pulled image "registry.example.com/shop/${workload}:${p.version}" in 1.8s`, container: workload },
            { ...base, timeMs: at + 3_000, reason: "Started", plainReason: "Container started", message: `Started container ${workload}`, container: workload },
          );
        }
      }
    });
  }
  events.sort((a, b) => b.timeMs - a.timeMs);
  return { series, events, imageChanges, versionChanges, stepSeconds, truncated: false, workloads: rows };
}

export function shopWorkloads(now: number): Pick<ObservabilityApi, WorkloadKeys> {
  return {
    listWorkloads: async (req) => {
      const envs = envsOf(req.envIds);
      const workloads = envs.flatMap((env) => DEMO_BACKENDS.filter((w) => !req.services?.length || req.services.includes(w)).map((w) => workloadRow(w, env, now, false)));
      return { workloads, truncated: false, jobs: jobsOf(envs, now), jobsTruncated: false };
    },
    getWorkload: async (req) => detailOf(envsOf(req.envIds), req.workload, req.window, now),
  };
}
