// Copyright (c) 2025 Reliant Labs

/**
 * FIXTURES for the Overview and the workload page: control-plane's own
 * shape — prod, e2e and dev plus three previews, the release ledger of
 * ../../Releases/__fixtures__ — with every state the pages must draw:
 *
 *   prod   admin-server healthy (verified by digest), reliant-api-server
 *          healthy, reliant-temporal-worker crash-looping (OOMKilled, 0/2),
 *          litellm degraded (1 of 2 ready), web healthy with a release that
 *          is DECLARED by the promotion (no digest observed), a migrate Job
 *          that succeeded.
 *   e2e    admin-server healthy, reliant-api-server pending (a rollout in
 *          flight), a migrate Job that FAILED (BackoffLimitExceeded).
 *   dev    two healthy services, a provisioning Job still running.
 *   pr-981 one healthy service.
 *   staging  SELF-MANAGED: nothing reports from it; its render declares three
 *          workloads, which the Overview lists as unobserved.
 *
 * The hosted observation (GetStatus) of prod: admin-server serves a URL,
 * litellm runs the WRONG VERSION (an observed digest no promotion pinned),
 * reliant-temporal-worker carries the platform's last error, docs is stopped
 * by its owner.
 *
 * GetWorkload answers with series (CPU and memory against limits), events,
 * versions, images, pods (one with no node reported) and the declared spec.
 */

import { ConnectError, Code } from "@connectrpc/connect";

import type { ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import type { PromotionList } from "@/services/controlPlane/observability/deliveryTypes";
import type {
  DeclaredWorkload,
  ListWorkloadsResult,
  ResourceUsage,
  TimeWindow,
  Workload,
  WorkloadDetail,
  WorkloadEvent,
  WorkloadHealth,
  WorkloadJob,
  WorkloadPod,
  WorkloadReleaseBasis,
  WorkloadSeries,
} from "@/services/controlPlane/observability/types";

import { createFixtureApi } from "../../Observability/__fixtures__/fixtureApi";
import { FIXTURE_DELIVERY, FIXTURE_NOW, RELEASE_ENVS, RELEASE_PROJECT, RELEASES } from "../../Releases/__fixtures__/releaseFixtures";
import type { ForgeDomain } from "@/services/forge/domains";
import type { ForgeHostedWorkload } from "@/services/forge/topology";

import type { StripEnv } from "../workloadModel";

export const WORKLOADS_PROJECT = RELEASE_PROJECT;
const V = RELEASES.map((r) => r.version);
const MIN = 60_000;
const HOUR = 60 * MIN;
const MiB = 1024 * 1024;

/** The roster as the Overview's strip reads it: prod first, previews last. */
export const STRIP_ENVS: StripEnv[] = [
  ...RELEASE_ENVS.filter((env) => env.kind === "long").map((env) => stripEnv(env)),
  {
    id: "env-staging",
    name: "staging",
    release: "",
    promotedBy: "",
    preview: false,
    placed: false,
    declared: ["admin-server", "reliant-api-server", "reliant-temporal-worker"],
  },
  ...RELEASE_ENVS.filter((env) => env.kind === "preview").map((env) => stripEnv(env)),
];

function stripEnv(env: (typeof RELEASE_ENVS)[number]): StripEnv {
  return {
    id: env.id,
    name: env.name,
    release: env.current,
    promotedAt: new Date(FIXTURE_NOW - (env.name === "prod" ? 5 : env.name === "e2e" ? 16 : 3) * HOUR).toISOString(),
    promotedBy: env.name === "e2e" ? "ci" : "sean",
    preview: env.kind === "preview",
    placed: true,
    declared: [],
  };
}

const DIGEST = (seed: string) => `sha256:${seed.repeat(64 / seed.length).slice(0, 64)}`;
const hosted = (name: string, over: Partial<ForgeHostedWorkload> = {}): ForgeHostedWorkload => ({
  name,
  tier: "backend",
  verdict: "converged",
  observed_state: "ready",
  observed_digest: DIGEST("e9a0c1d2"),
  desired_digest: DIGEST("e9a0c1d2"),
  deployment_id: `dep-${name}`,
  declared_run_state: "running",
  drifted: false,
  last_error: "",
  ...over,
});

/** GetStatus of each hosted environment, by environment then workload name. */
export const FIXTURE_HOSTED: ReadonlyMap<string, ReadonlyMap<string, ForgeHostedWorkload>> = new Map([
  [
    "prod",
    new Map([
      ["admin-server", hosted("admin-server", { url: "https://admin.reliantlabs.io" })],
      ["reliant-api-server", hosted("reliant-api-server", { url: "https://api.reliantlabs.io" })],
      [
        "reliant-temporal-worker",
        hosted("reliant-temporal-worker", { verdict: "degraded", observed_state: "degraded", last_error: "container worker OOMKilled: exceeded its 1Gi memory limit (14 restarts)" }),
      ],
      ["litellm", hosted("litellm", { verdict: "diverged", observed_digest: DIGEST("7f3b9a"), desired_digest: DIGEST("e9a0c1d2"), drifted: true })],
      // The same bindings FIXTURE_DOMAINS holds, as GetStatus reports them on the deployment.
      [
        "web",
        hosted("web", {
          tier: "static",
          url: "https://valiant-caribou.sites.reliantlabs.dev",
          domains: [{ hostname: "app.reliantlabs.io", state: "live", serving_path: "edge" }],
        }),
      ],
      [
        "docs",
        hosted("docs", {
          tier: "static",
          declared_run_state: "suspended",
          observed_state: "suspended",
          suspend_reason: "owner",
          url: "https://bright-otter.sites.reliantlabs.dev",
          domains: [{ hostname: "docs.reliantlabs.io", state: "pending-dns", serving_path: "unknown" }],
        }),
      ],
    ]),
  ],
  ["e2e", new Map([["admin-server", hosted("admin-server")], ["reliant-api-server", hosted("reliant-api-server", { verdict: "converging", observed_state: "progressing" })]])],
  // dev's site was declared but the platform has not allocated its address yet.
  ["dev", new Map([["admin-server", hosted("admin-server")], ["reliant-api-server", hosted("reliant-api-server")], ["web", hosted("web", { tier: "static", verdict: "converging", observed_state: "progressing" })]])],
]);

/** The org's domains: web is also served at app.reliantlabs.io, and one is still waiting for DNS. */
export const FIXTURE_DOMAINS: ForgeDomain[] = [
  {
    id: "dom-app",
    hostname: "app.reliantlabs.io",
    state: "live",
    origin: "external",
    requiredRecords: [],
    lastError: "",
    binding: { id: "bind-app", domainId: "dom-app", environmentId: "env-prod", target: "web", redirectTo: "" },
  },
  {
    id: "dom-docs",
    hostname: "docs.reliantlabs.io",
    state: "pending-dns",
    origin: "external",
    requiredRecords: [],
    lastError: "",
    binding: { id: "bind-docs", domainId: "dom-docs", environmentId: "env-prod", target: "docs", redirectTo: "" },
  },
  {
    id: "dom-www",
    hostname: "www.reliantlabs.io",
    state: "live",
    origin: "external",
    requiredRecords: [],
    lastError: "",
    binding: { id: "bind-www", domainId: "dom-www", environmentId: "env-prod", target: "", redirectTo: "app.reliantlabs.io" },
  },
];

/** What GetStatus says of one workload: the detail page's `hosted` prop. Null for an environment Reliant does not place. */
export function fixtureHostedObservation(envName: string, workload: string): { workload: ForgeHostedWorkload | null; loading: boolean } | null {
  const env = STRIP_ENVS.find((candidate) => candidate.name === envName);
  if (!env?.placed) return null;
  return { workload: FIXTURE_HOSTED.get(envName)?.get(workload) ?? null, loading: false };
}

/** The repository metadata seam: control-plane's default branch is main; `unknown` answers nothing. */
export function createRepoMetaFixture(defaultBranch: string | null = "main") {
  return { defaultBranch: async () => defaultBranch };
}

function usage(latest: number, p95: number, limit: number | null, request: number | null): ResourceUsage {
  return {
    latest,
    p95,
    limit,
    request,
    limitUtilizationLatest: limit ? latest / limit : null,
    limitUtilizationP95: limit ? p95 / limit : null,
    requestUtilizationLatest: request ? latest / request : null,
    requestUtilizationP95: request ? p95 / request : null,
  };
}

interface Seed {
  env: string;
  name: string;
  health: WorkloadHealth;
  reason?: string;
  detail?: string;
  cause?: Workload["healthCause"];
  ready: number;
  desired: number | null;
  release: string;
  basis?: WorkloadReleaseBasis;
  restarts?: number;
  exit?: string;
  cpu: [number, number | null];
  memory: [number, number | null];
  kind?: Workload["kind"];
}

const SEEDS: Seed[] = [
  { env: "prod", name: "admin-server", health: "healthy", ready: 2, desired: 2, release: V[0]!, cpu: [0.18, 0.5], memory: [212 * MiB, 512 * MiB] },
  { env: "prod", name: "reliant-api-server", health: "healthy", ready: 3, desired: 3, release: V[0]!, cpu: [0.41, 1], memory: [388 * MiB, 1024 * MiB] },
  {
    env: "prod",
    name: "reliant-temporal-worker",
    health: "crash_looping",
    reason: "Restarting repeatedly (14 times in the last 24 hours). It ran out of memory (limit 1 GiB). Raise its memory or reduce its usage",
    detail: "last exit OOMKilled; OOMKilling: Memory cgroup out of memory",
    cause: "workload",
    ready: 0,
    desired: 2,
    release: V[0]!,
    restarts: 14,
    exit: "OOMKilled",
    cpu: [0.92, 1],
    memory: [1004 * MiB, 1024 * MiB],
    kind: "statefulset",
  },
  { env: "prod", name: "litellm", health: "degraded", reason: "1 of 2 replicas ready. The app exited with an error (restarted 2 times)", cause: "workload", ready: 1, desired: 2, release: V[1]!, basis: "promotion", restarts: 2, exit: "Error", cpu: [0.3, 0.5], memory: [700 * MiB, 768 * MiB] },
  { env: "prod", name: "web", health: "healthy", ready: 2, desired: 2, release: V[0]!, basis: "promotion", cpu: [0.02, null], memory: [64 * MiB, null] },
  { env: "prod", name: "docs", health: "suspended", reason: "stopped by its owner", ready: 0, desired: 1, release: V[0]!, cpu: [0, 0.25], memory: [0, 256 * MiB] },
  { env: "e2e", name: "admin-server", health: "healthy", ready: 1, desired: 1, release: V[3]!, cpu: [0.06, 0.5], memory: [160 * MiB, 512 * MiB] },
  { env: "e2e", name: "reliant-api-server", health: "pending", reason: "rolling out: 1 of 2 updated", ready: 1, desired: 2, release: V[3]!, cpu: [0.1, 1], memory: [240 * MiB, 1024 * MiB] },
  { env: "dev", name: "admin-server", health: "healthy", ready: 1, desired: 1, release: V[0]!, cpu: [0.03, 0.5], memory: [140 * MiB, 512 * MiB] },
  { env: "dev", name: "reliant-api-server", health: "healthy", ready: 1, desired: 1, release: V[0]!, cpu: [0.05, 1], memory: [200 * MiB, 1024 * MiB] },
  { env: "pr-981", name: "admin-server", health: "healthy", ready: 1, desired: 1, release: V[4]!, cpu: [0.02, 0.5], memory: [120 * MiB, 512 * MiB] },
];

const envIdOf = (name: string) => STRIP_ENVS.find((env) => env.name === name)!.id;

function declaredOf(seed: Seed): DeclaredWorkload {
  return {
    name: seed.name,
    kind: seed.kind ?? "deployment",
    specRecorded: seed.name !== "litellm",
    replicas: seed.desired,
    cpuRequestMillicores: seed.cpu[1] ? Math.round(seed.cpu[1] * 500) : 0,
    cpuLimitMillicores: seed.cpu[1] ? Math.round(seed.cpu[1] * 1000) : 0,
    memoryRequestBytes: seed.memory[1] ? seed.memory[1] / 2 : 0,
    memoryLimitBytes: seed.memory[1] ?? 0,
    ports: seed.name === "web" ? [{ name: "http", port: 3000, protocol: "tcp" }] : [{ name: "http", port: 8080, protocol: "tcp" }, { name: "metrics", port: 9090, protocol: "tcp" }],
    envNames: ["DATABASE_URL", "LOG_LEVEL", "OTEL_EXPORTER_OTLP_ENDPOINT", "RELIANT_ENV", ...(seed.name.startsWith("reliant") ? ["TEMPORAL_HOST"] : [])],
    image: `us-docker.pkg.dev/reliant-labs/images/${seed.name}@sha256:${"e9a0c1d2b3f4a5b6".repeat(4)}`,
    sourcePath: `deploy/kcl/${seed.env}/main.k`,
    sourceRepo: "https://github.com/reliant-labs/control-plane.git",
    sourceCommit: RELEASES.find((r) => r.version === seed.release)?.gitCommit ?? "",
    bundleId: `bundle-${seed.env}`,
  };
}

function podsOf(seed: Seed, now: number): WorkloadPod[] {
  if (seed.health === "suspended") return [];
  const count = Math.max(seed.desired ?? 1, 1);
  return Array.from({ length: count }, (_, i) => {
    const bad = i === 0 && seed.health !== "healthy" && seed.health !== "pending";
    const warning: WorkloadEvent | null = bad
      ? { timeMs: now - 4 * MIN, envId: envIdOf(seed.env), type: "Warning", reason: "BackOff", plainReason: "Waiting before the next restart attempt", message: `Back-off restarting failed container ${seed.name}`, count: seed.restarts ?? 1, objectKind: "Pod", objectName: `${seed.name}-7d9f8c-${i}`, container: seed.name }
      : null;
    return {
      name: `${seed.name}-7d9f8c4b5-${["x2kqp", "m9tzc", "h4wrd"][i] ?? "z0000"}`,
      uid: `${seed.name}-uid-${i}`,
      state: "running",
      restarts: bad ? (seed.restarts ?? 0) : 0,
      lastExitReason: bad ? (seed.exit ?? "") : "",
      lastExitAtMs: bad ? now - 6 * MIN : null,
      // One pod on a node agent too old to report its node.
      node: i === 1 ? "" : `gke-prod-pool-${i + 1}-a3f9`,
      firstSeenMs: i === 0 ? now - 5 * HOUR : now - 24 * HOUR,
      lastSeenMs: now - 20_000,
      ageIsLowerBound: i !== 0,
      latestWarning: warning,
    };
  });
}

function workloadOf(seed: Seed, now: number, detail: boolean): Workload {
  const envId = envIdOf(seed.env);
  return {
    envId,
    envName: seed.env,
    workload: seed.name,
    namespace: `control-plane-${seed.env}`,
    serviceVersion: seed.release,
    replicasObserved: seed.ready,
    restartsInWindow: seed.restarts ?? 0,
    latestWarning: seed.exit
      ? { timeMs: now - 6 * MIN, envId, type: "Warning", reason: seed.exit === "OOMKilled" ? "OOMKilling" : "BackOff", plainReason: seed.exit === "OOMKilled" ? "Ran out of memory" : "Waiting before the next restart attempt", message: `${seed.name}: ${seed.exit}`, count: seed.restarts ?? 1, objectKind: "Pod", objectName: `${seed.name}-7d9f8c4b5-x2kqp`, container: seed.name }
      : null,
    containers: [
      {
        container: seed.name,
        imageName: `us-docker.pkg.dev/reliant-labs/images/${seed.name}`,
        imageTag: seed.release,
        restartsInWindow: seed.restarts ?? 0,
        lastTerminatedReason: seed.exit ?? "",
        lastTerminatedAtMs: seed.exit ? now - 6 * MIN : null,
        cpu: usage(seed.cpu[0], seed.cpu[0] * 1.2, seed.cpu[1], seed.cpu[1] ? seed.cpu[1] / 2 : null),
        memory: usage(seed.memory[0], seed.memory[0] * 1.05, seed.memory[1], seed.memory[1] ? seed.memory[1] / 2 : null),
      },
    ],
    kind: seed.kind ?? "deployment",
    health: seed.health,
    healthReason: seed.reason ?? "",
    healthCause: seed.cause ?? "none",
    healthDetail: seed.detail ?? "",
    readyReplicas: seed.ready,
    desiredReplicas: seed.desired,
    releaseVersion: seed.release,
    releaseBasis: seed.basis ?? "digest",
    pods: detail ? podsOf(seed, now) : [],
    declared: detail ? declaredOf(seed) : null,
  };
}

function job(env: string, workload: string, suffix: string, result: WorkloadJob["result"], release: string, startedAgoMs: number, durationSeconds: number, reason = ""): WorkloadJob {
  const startedAtMs = FIXTURE_NOW - startedAgoMs;
  return {
    envId: envIdOf(env),
    envName: env,
    name: `${workload}-${suffix}`,
    workload,
    result,
    reason,
    releaseVersion: release,
    startedAtMs,
    finishedAtMs: result === "running" ? null : startedAtMs + durationSeconds * 1000,
    durationSeconds: result === "running" ? 0 : durationSeconds,
  };
}

/** Newest first, as ListWorkloads returns them. */
export const FIXTURE_JOBS: WorkloadJob[] = [
  job("dev", "control-plane-provision", "4f1a2b", "running", V[0]!, 3 * MIN, 0),
  job("prod", "control-plane-migrate", "b9e1b9d6de", "succeeded", V[0]!, 5 * HOUR, 41),
  job("e2e", "control-plane-migrate", "c0a1f3e2d1", "failed", V[3]!, 16 * HOUR, 312, "BackoffLimitExceeded"),
  job("e2e", "control-plane-migrate", "a7d6e5c4b3", "succeeded", V[4]!, 18 * HOUR, 38),
  job("prod", "control-plane-migrate", "f1e2d3c4b5", "succeeded", V[1]!, 12 * HOUR, 44),
];

export function fixtureWorkloadList(now: number = FIXTURE_NOW): ListWorkloadsResult {
  return { workloads: SEEDS.map((seed) => workloadOf(seed, now, false)), truncated: false, jobs: FIXTURE_JOBS, jobsTruncated: false };
}

function buckets(window: TimeWindow, count = 48): number[] {
  const step = (window.endMs - window.startMs) / count;
  return Array.from({ length: count }, (_, i) => Math.round((window.startMs + i * step) / 1000));
}

export function fixtureWorkloadDetail(envIds: string[], name: string, window: TimeWindow): WorkloadDetail {
  const seeds = SEEDS.filter((seed) => seed.name === name && (envIds.length === 0 || envIds.includes(envIdOf(seed.env))));
  const timestamps = buckets(window);
  const series: WorkloadSeries[] = [];
  const events: WorkloadEvent[] = [];
  const pivot = window.startMs + (window.endMs - window.startMs) * 0.7;
  for (const seed of seeds) {
    const envId = envIdOf(seed.env);
    const wave = (base: number, i: number, k: number) => base * (0.72 + 0.18 * Math.sin(i / 4 + k) + (i > timestamps.length * 0.7 ? 0.12 : 0));
    series.push(
      { envId, container: seed.name, metric: "cpu_usage", unit: "{cpu}", timestamps, values: timestamps.map((_, i) => wave(seed.cpu[0], i, 1)) },
      { envId, container: seed.name, metric: "memory_working_set", unit: "By", timestamps, values: timestamps.map((_, i) => Math.min(wave(seed.memory[0], i, 2), seed.memory[1] ?? Infinity)) }
    );
    if (seed.exit) {
      events.push(
        { timeMs: window.endMs - 6 * MIN, envId, type: "Warning", reason: seed.exit === "OOMKilled" ? "OOMKilling" : "BackOff", plainReason: seed.exit === "OOMKilled" ? "Ran out of memory" : "Waiting before the next restart attempt", message: `Container ${seed.name} ${seed.exit === "OOMKilled" ? `was OOMKilled (limit ${Math.round((seed.memory[1] ?? 0) / MiB)}Mi)` : "exited with an error"}`, count: seed.restarts ?? 1, objectKind: "Pod", objectName: `${seed.name}-7d9f8c4b5-x2kqp`, container: seed.name },
        { timeMs: window.endMs - 5 * MIN, envId, type: "Warning", reason: "BackOff", plainReason: "Waiting before the next restart attempt", message: "Back-off restarting failed container", count: seed.restarts ?? 1, objectKind: "Pod", objectName: `${seed.name}-7d9f8c4b5-x2kqp`, container: seed.name }
      );
    }
    events.push(
      { timeMs: pivot + 40_000, envId, type: "Normal", reason: "Pulled", plainReason: "Image fetched", message: `Successfully pulled image "us-docker.pkg.dev/reliant-labs/images/${seed.name}@sha256:e9a0c1d2b3f4" in 1.8s`, count: 1, objectKind: "Pod", objectName: `${seed.name}-7d9f8c4b5-m9tzc`, container: seed.name },
      { timeMs: pivot, envId, type: "Normal", reason: "ScalingReplicaSet", plainReason: "", message: `Scaled up replica set ${seed.name}-7d9f8c4b5 to ${seed.desired ?? 1}`, count: 1, objectKind: "Deployment", objectName: seed.name, container: "" },
      { timeMs: pivot + 90_000, envId, type: "Warning", reason: "Unhealthy", plainReason: "Not passing its readiness check", message: "Readiness probe failed: HTTP probe failed with statuscode: 503", count: 1, objectKind: "Pod", objectName: `${seed.name}-7d9f8c4b5-m9tzc`, container: seed.name }
    );
  }
  events.sort((a, b) => b.timeMs - a.timeMs);
  return {
    series,
    events,
    imageChanges: seeds.flatMap((seed) => [
      { envId: envIdOf(seed.env), container: seed.name, imageName: `us-docker.pkg.dev/reliant-labs/images/${seed.name}`, imageTag: V[1]!, firstSeenMs: window.startMs, lastSeenMs: pivot },
      { envId: envIdOf(seed.env), container: seed.name, imageName: `us-docker.pkg.dev/reliant-labs/images/${seed.name}`, imageTag: seed.release, firstSeenMs: pivot, lastSeenMs: window.endMs },
    ]),
    versionChanges: seeds.flatMap((seed) => [
      { envId: envIdOf(seed.env), serviceVersion: V[1]!, firstSeenMs: window.startMs, lastSeenMs: pivot },
      { envId: envIdOf(seed.env), serviceVersion: seed.release, firstSeenMs: pivot, lastSeenMs: window.endMs },
    ]),
    stepSeconds: Math.round((window.endMs - window.startMs) / 1000 / 48),
    truncated: false,
    workloads: seeds.map((seed) => workloadOf(seed, window.endMs, true)),
  };
}

export type WorkloadsFixtureState = "data" | "empty" | "loading" | "error" | "not-set-up";

/**
 * The observability fixture API with the workload reads (and the delivery
 * ledger the KPI markers and recent activity read) answering for this
 * project.
 */
export function createWorkloadsFixtureApi(state: WorkloadsFixtureState = "data", onCall?: (rpc: string, request: unknown) => void): ObservabilityApi {
  const base = createFixtureApi({ onCall, alerting: "ok" });
  const answer = async <T,>(rpc: string, request: unknown, value: () => T): Promise<T> => {
    onCall?.(rpc, request);
    if (state === "loading") return new Promise<T>(() => {});
    if (state === "error") throw new ConnectError("upstream connect error", Code.Unavailable);
    if (state === "not-set-up") throw new ConnectError("observability is not set up for this organization yet", Code.FailedPrecondition);
    return value();
  };
  return {
    ...base,
    listWorkloads: (req) => answer("ListWorkloads", req, () => (state === "empty" ? { workloads: [], truncated: false, jobs: [], jobsTruncated: false } : fixtureWorkloadList(req.window.endMs))),
    getWorkload: (req) => answer("GetWorkload", req, () => fixtureWorkloadDetail(req.envIds, req.workload, req.window)),
    listReleases: async (req: { window: { startMs: number; endMs: number }; envIds: string[] }): Promise<PromotionList> => ({
      promotions: FIXTURE_DELIVERY.filter(
        (p) => p.promotedAtMs >= req.window.startMs && p.promotedAtMs <= req.window.endMs && (req.envIds.length === 0 || req.envIds.includes(p.envId))
      ),
      truncated: false,
    }),
  } as ObservabilityApi;
}
