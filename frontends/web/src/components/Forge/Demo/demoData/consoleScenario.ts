// Copyright (c) 2025 Reliant Labs

/**
 * The sample app's console data, on the wire: what control-plane would answer
 * for shop's Releases, Environments, Secrets, Domains and Activity tabs, for
 * the demo's mock transport (demoScenario.ts spreads these handlers in).
 *
 * The story is shopStory.ts's, the same the observability views read:
 *
 *   shop has an api, a worker, an inventory service and a static storefront,
 *   in dev, staging and prod (env-dev / env-staging / env-prod). prod and
 *   staging run v1.8.3, the release the payment-timeout incident started
 *   with in prod (40 minutes ago); dev already runs v1.8.4, the fix. The
 *   releases, their promotions and who made them are shopStory's ledger.
 *
 * Overrides only: forge's generated service defaults answer every other read
 * (demoServiceDefaults.ts); these say what shop's console is ABOUT.
 *
 * Read-only by construction: every handler here is a read (the transport
 * refuses the rest first, demoTransport.ts), and secrets are NAMES only. No
 * value, digest of a value, or anything like one is in this file.
 *
 * Times are relative to the page load, so the data never looks stale.
 */

import type { JsonObject, MessageInitShape } from "@bufbuild/protobuf";
import { timestampFromMs, type Timestamp } from "@bufbuild/protobuf/wkt";

import {
  DeployAttemptState,
  DeployCustomDomainState,
  DeployEnvironmentKind,
  DeployObservedState,
  DeployPromotionSchema,
  DeployRolloutPhase,
  DeployRunState,
  DeployServingPath,
  DeployTier,
  DeployVerdict,
  DomainSource,
} from "@/gen/controlplane/controlplane/v1/deploy_pb";
import { ActivityActorKind, ActivityEventSchema, ActivityService } from "@/gen/controlplane/services/activity/v1/activity_pb";
import { DeployChangeKind, DeployCommitComparisonState, DeployObjectComparisonState, DeployService } from "@/gen/controlplane/services/deploy/v1/deploy_pb";
import { DomainService } from "@/gen/controlplane/services/domain/v1/domain_pb";
import { SecretAuthorKind, SecretStoreService } from "@/gen/controlplane/services/secret_store/v1/secret_store_pb";

import { completed } from "./demoServiceDefaults";
import { shopActivity } from "./shop/activity";
import { DEMO_ENVS, DEMO_PROJECT, demoEnvId, inDemoProject, type DemoEnvId } from "./demoScope";
import { mockService, type MockHandlers } from "@reliantlabs/forge-web-runtime/mock-transport";
import {
  artifactVersion,
  commitAgeMs,
  demoBundleDigest,
  demoBundleId,
  DEMO_RELEASES,
  DEMO_REPO,
  demoNow,
  demoRelease,
  HISTORY,
  PASSED_GATE,
  PROMOTED_FROM,
  promotionId,
  RUNNING,
  type DemoPromotion,
} from "./shopStory";

const REPO = DEMO_REPO;
const FORGE_VERSION = "v1.0.17";

const MINUTE = 60_000;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;

/** `ms` ago, from this page load. */
const ago = (ms: number, now = demoNow()): Timestamp => timestampFromMs(now - ms);

export { DEMO_ENVS, DEMO_PROJECT, DEMO_RELEASES, RUNNING };

type EnvId = DemoEnvId;

/** Observability calls the storefront `web`; the release ledger calls it
 * `shop-web`. Keep this alias at the fixture boundary, not in production UI. */
export const demoWorkloadName = (name: string): string => name === "web" ? "shop-web" : name;

export const DEMO_WORKLOADS = [
  { name: "api", tier: DeployTier.BACKEND, replicas: 2, path: "api" },
  { name: "worker", tier: DeployTier.BACKEND, replicas: 1, path: "" },
  { name: "inventory", tier: DeployTier.BACKEND, replicas: 2, path: "" },
  { name: "shop-web", tier: DeployTier.STATIC, replicas: 1, path: "web" },
] as const;

/** Secret NAMES the app declares, who reads each, and which environments have a value. */
export const DEMO_SECRETS = [
  {
    name: "DATABASE_URL",
    declaredBy: ["api", "inventory", "worker"],
    set: ["env-dev", "env-staging", "env-prod"],
    versions: 3,
  },
  {
    name: "PAYMENTS_API_KEY",
    declaredBy: ["api"],
    set: ["env-dev", "env-staging", "env-prod"],
    versions: 2,
  },
  {
    name: "PAYMENTS_WEBHOOK_SECRET",
    declaredBy: ["api"],
    set: ["env-staging", "env-prod"],
    versions: 1,
  },
  {
    name: "NATS_CREDENTIALS",
    declaredBy: ["api", "worker"],
    set: ["env-dev", "env-staging", "env-prod"],
    versions: 1,
  },
  {
    name: "SESSION_SIGNING_KEY",
    declaredBy: ["api"],
    set: ["env-dev", "env-staging", "env-prod"],
    versions: 4,
  },
  {
    name: "RESEND_API_KEY",
    declaredBy: ["worker"],
    set: ["env-prod"],
    versions: 1,
  },
] as const;

/** When each secret's current value was set (the rotations the activity feed and promotion notes mention). */
const SECRET_UPDATED: Record<string, number> = {
  DATABASE_URL: 31 * DAY,
  PAYMENTS_API_KEY: 12 * DAY,
  PAYMENTS_WEBHOOK_SECRET: 4.6 * DAY,
  NATS_CREDENTIALS: 27 * DAY,
  SESSION_SIGNING_KEY: 3 * DAY,
  RESEND_API_KEY: 18 * DAY,
};

export const DEMO_DOMAINS = [
  {
    id: "dom-shop",
    hostname: "shop.example.com",
    env: "env-prod",
    target: "shop-web",
    state: DeployCustomDomainState.LIVE,
    age: 30 * DAY,
  },
  {
    id: "dom-api",
    hostname: "api.shop.example.com",
    env: "env-prod",
    target: "api",
    state: DeployCustomDomainState.LIVE,
    age: 30 * DAY,
  },
  {
    id: "dom-www",
    hostname: "www.shop.example.com",
    env: "env-prod",
    target: "",
    redirect: "shop.example.com",
    state: DeployCustomDomainState.LIVE,
    age: 30 * DAY,
  },
  {
    id: "dom-staging",
    hostname: "staging.shop.example.com",
    env: "env-staging",
    target: "shop-web",
    state: DeployCustomDomainState.VERIFYING,
    age: 20 * MINUTE,
  },
] as const;

const releaseOf = (version: string) => demoRelease(version) ?? DEMO_RELEASES[0];
const digest = (seed: string) => {
  let h = 0x811c9dc5;
  for (const c of seed) h = Math.imul(h ^ c.charCodeAt(0), 0x01000193) >>> 0;
  return `sha256:${h.toString(16).padStart(8, "0").repeat(8)}`;
};

function provenance(version: string) {
  const r = releaseOf(version);
  return {
    repo: REPO,
    commit: r.commit,
    branch: "main",
    tag: r.version,
    forgeVersion: FORGE_VERSION,
    commitSubject: r.subject,
    commitAuthor: r.author,
    commitTime: ago(commitAgeMs(r, 0)),
  };
}

/** A workload's artifact digest in `version`: unchanged since the release that last changed it. */
const artifactDigest = (workload: string, version: string) => digest(`${workload}@${artifactVersion(workload, version)}`);

function environment(env: (typeof DEMO_ENVS)[number]) {
  const running = RUNNING[env.id];
  return {
    id: env.id,
    name: env.name,
    project: DEMO_PROJECT,
    kind: DeployEnvironmentKind.PERSISTENT,
    namespace: `shop-${env.name}`,
    currentPromotionId: promotionId(env.id, running.version),
    createdAt: ago(40 * DAY),
    updatedAt: ago(running.age),
    declaredAt: ago(running.age),
    declaredBy: provenance(running.version),
    convergesPromotions: true,
    protected: env.name === "prod",
  };
}

function promotion(envId: EnvId, p: DemoPromotion, from?: string): MessageInitShape<typeof DeployPromotionSchema> {
  const r = releaseOf(p.version);
  const health = p.health ?? PASSED_GATE;
  return {
    id: promotionId(envId, p.version, p.seq),
    environmentId: envId,
    releaseId: `rel-${r.version}`,
    releaseVersion: r.version,
    promotedByActor: p.by,
    createdAt: ago(p.age),
    releaseProvenance: provenance(r.version),
    fromEnvironmentName: p.from === undefined ? (from ?? "") : (p.from ?? ""),
    note: p.note ?? "",
    targets: [...(p.targets ?? [])],
    resolvedArtifacts: Object.fromEntries(DEMO_WORKLOADS.map((w) => [w.name, artifactDigest(w.name, r.version)])),
    bundleId: demoBundleId(envId, r.version),
    bundleDigest: demoBundleDigest(envId, r.version),
    gates: [
      ...(health === "none"
        ? []
        : [
            {
              name: "forge obs check",
              status: health.status,
              summary: health.summary,
              finishedAt: ago(p.age - 12 * MINUTE),
            },
          ]),
      {
        name: "ci",
        status: "passed",
        summary: "build, lint (27 linters) and tests",
        finishedAt: ago(p.age + 20 * MINUTE),
      },
    ],
  };
}


function release(r: (typeof DEMO_RELEASES)[number], index: number) {
  const base = DEMO_RELEASES[index + 1];
  return {
    id: `rel-${r.version}`,
    version: r.version,
    gitCommit: r.commit,
    gitTag: r.version,
    project: DEMO_PROJECT,
    createdAt: ago(r.age),
    provenance: provenance(r.version),
    artifacts: DEMO_WORKLOADS.map((w) => ({
      name: w.name,
      kind: w.tier === DeployTier.STATIC ? "static" : "image",
      digest: artifactDigest(w.name, r.version),
      uri: w.tier === DeployTier.STATIC ? `sites/shop-web/${r.version}` : `registry.example.com/shop/${w.name}`,
      version: r.version,
      platforms: w.tier === DeployTier.STATIC ? [] : ["linux/amd64", "linux/arm64"],
    })),
    commits: {
      baseRelease: base?.version ?? "",
      baseCommit: base?.commit ?? "",
      commits: r.commits.map((c, i) => ({
        ...c,
        time: ago(commitAgeMs(r, i)),
      })),
      total: r.commits.length,
    },
  };
}

/** The alert rules every environment's shape declares (the simple view's Alerts page reads them too). */
export const DEMO_ALERT_RULES = [
  {
    alert: "OrdersErrorRateHigh",
    expr: 'rate(rpc_server_errors{service="api",rpc_method="CreateOrder"}[5m]) / rate(rpc_server_requests{service="api",rpc_method="CreateOrder"}[5m]) > 0.05',
    for: "5m",
    labels: { severity: "page" },
    annotations: { summary: "More than 5% of CreateOrder requests are failing" },
    source: "user",
    default_id: "",
  },
  {
    alert: "ListOrdersSlow",
    expr: 'histogram_quantile(0.95, rpc_server_duration{service="api",rpc_method="ListOrders"}) > 0.5',
    for: "10m",
    labels: { severity: "ticket" },
    annotations: { summary: "ListOrders p95 over 500 ms" },
    source: "default",
    default_id: "rpc-latency",
  },
  {
    alert: "WorkerBacklog",
    expr: 'nats_consumer_pending{consumer="fulfilment"} > 500',
    for: "10m",
    labels: { severity: "ticket" },
    annotations: {
      summary: "The worker is falling behind on order events",
    },
    source: "user",
    default_id: "",
  },
] as const;

/** Where each severity is sent. */
export const DEMO_ALERT_ROUTES = [
  { match: { severity: "page" }, receiver: "oncall" },
  { match: { severity: "ticket" }, receiver: "github-issues" },
] as const;

/** The declared shape of one environment (forge's release.Shape, snake_case). */
function shape(envId: EnvId): JsonObject {
  return {
    kind: "persistent",
    workloads: DEMO_WORKLOADS.map((w) => ({
      name: w.name as string,
      runtime: w.tier === DeployTier.STATIC ? "static" : "hosted",
      cluster: "",
    })),
    secrets: DEMO_SECRETS.map((s) => ({
      name: s.name,
      provider: "hosted",
      declared_by: [...s.declaredBy] as string[],
    })),
    domains: DEMO_DOMAINS.filter((d) => d.env === envId).map((d) => d.hostname as string),
    clusters: [],
    alerts: DEMO_ALERT_RULES.map((rule) => ({ ...rule, labels: { ...rule.labels }, annotations: { ...rule.annotations } })),
    alert_routes: DEMO_ALERT_ROUTES.map((route) => ({ match: { ...route.match }, receiver: route.receiver })),
    observability_policy: { enabled: true, disabled: [] },
  };
}

function deployments(envId: EnvId) {
  const running = RUNNING[envId];
  const env = DEMO_ENVS.find((e) => e.id === envId)!;
  return DEMO_WORKLOADS.map((w) => ({
    deployment: {
      id: `dep-${env.name}-${w.name}`,
      environmentId: envId,
      name: w.name,
      tier: w.tier,
      runState: DeployRunState.RUNNING,
      artifact: w.name,
      appliedPromotionId: promotionId(envId, running.version),
      createdAt: ago(40 * DAY),
      updatedAt: ago(running.age),
      observed: {
        state: DeployObservedState.READY,
        replicas: w.replicas,
        imageDigest: artifactDigest(w.name, running.version),
        reconciledAt: ago(2 * MINUTE),
        stableSince: ago(running.age - 3 * MINUTE),
        url: w.path
          ? `https://${env.name === "prod" ? "" : `${env.name}.`}${w.path === "web" ? "" : "api."}shop.example.com`.replace(
              "https://.",
              "https://",
            )
          : "",
        urlServingPath: w.tier === DeployTier.STATIC ? DeployServingPath.EDGE : DeployServingPath.INGRESS,
      },
    },
    verdict: DeployVerdict.CONVERGED,
    desiredDigest: artifactDigest(w.name, running.version),
    observedAt: ago(2 * MINUTE),
  }));
}

function domain(d: (typeof DEMO_DOMAINS)[number]) {
  const live = d.state === DeployCustomDomainState.LIVE;
  return {
    id: d.id,
    hostname: d.hostname,
    state: d.state,
    source: DomainSource.EXTERNAL,
    createdAt: ago(d.age),
    updatedAt: ago(live ? d.age - HOUR : 2 * MINUTE),
    verifiedAt: live ? ago(d.age - 20 * MINUTE) : undefined,
    liveSince: live ? ago(d.age - HOUR) : undefined,
    servingPath: d.target === "api" ? DeployServingPath.INGRESS : DeployServingPath.EDGE,
    requiredRecords: [
      {
        type: "TXT",
        name: `_reliant-challenge.${d.hostname}`,
        value: `reliant-verify=${d.id}-7f3a`,
        resolved: true,
      },
      {
        type: "CNAME",
        name: d.hostname,
        value: "sites-edge.example.net",
        resolved: live,
        detail: live ? "" : "Not visible from our resolvers yet. DNS can take a few minutes.",
      },
    ],
    binding: {
      id: `bind-${d.id}`,
      domainId: d.id,
      environmentId: d.env,
      target: d.target,
      redirectTo: "redirect" in d ? d.redirect : "",
      createdAt: ago(d.age),
      updatedAt: ago(d.age),
    },
  };
}

/** The activity feed on the wire: the same events the observability views read (shop/activity.ts). */
function activity(): MessageInitShape<typeof ActivityEventSchema>[] {
  const kinds = { user: ActivityActorKind.USER, machine: ActivityActorKind.MACHINE, system: ActivityActorKind.SYSTEM, unknown: ActivityActorKind.UNSPECIFIED } as const;
  return shopActivity(demoNow()).map((e) => ({
    id: e.id,
    at: timestampFromMs(e.atMs),
    actor: { ...e.actor, kind: kinds[e.actor.kind] },
    verb: e.verb,
    target: e.target,
    env: e.env ?? undefined,
    via: e.via,
    detail: e.detail,
    project: e.project,
  }));
}

// ── Deploys ─────────────────────────────────────────────────────────────────

const DEPLOY_PHASES = ["start", "preflight", "build", "plan", "promote", "rollout"] as const;

/** The deploy that broke before it could cut a release: staging, before the one that shipped v1.8.3. */
const FAILED_DEPLOY = {
  id: "dep-run-staging-failed",
  env: "env-staging" as EnvId,
  age: 4 * HOUR,
  by: "jon",
  failedPhase: "build",
  error: "go build ./cmd/worker: internal/inventory/reserve.go:42:9: undefined: inventory.ReserveFromEvent",
  log: [
    "forge env deploy staging",
    "  preflight  ok · staging has room for 4 workloads",
    "  build      api ✓  inventory ✓  shop-web ✓",
    "  build      worker ✗",
    "internal/inventory/reserve.go:42:9: undefined: inventory.ReserveFromEvent",
    "error: build failed for worker; nothing was cut or promoted",
  ].join("\n"),
};

/**
 * One `forge env deploy` per promotion (RUNNING and HISTORY), newest first,
 * and the failed one. The promotions to dev and staging ran in CI; prod's
 * from a terminal, so their output stayed there.
 */
function deployAttempts() {
  const runs = (Object.keys(RUNNING) as EnvId[]).flatMap((envId) =>
    [RUNNING[envId], ...HISTORY[envId]].map((p): DemoPromotion & { envId: EnvId; failed: typeof FAILED_DEPLOY | null } => ({
      envId,
      ...p,
      failed: null,
    })),
  );
  runs.push({ envId: FAILED_DEPLOY.env, version: "", age: FAILED_DEPLOY.age, by: FAILED_DEPLOY.by, failed: FAILED_DEPLOY });
  runs.sort((a, b) => a.age - b.age);
  return runs.map((r) => {
    const env = DEMO_ENVS.find((e) => e.id === r.envId)!;
    const ci = !r.failed && env.name !== "prod";
    // Deploys take a few minutes, varying with what they rolled out.
    const took = r.failed ? 3 * MINUTE : (4 + (Math.round(r.age / MINUTE) % 4)) * MINUTE + (Math.round(r.age / 1000) % 50) * 1000;
    const phases = r.failed ? DEPLOY_PHASES.slice(0, DEPLOY_PHASES.indexOf("build") + 1) : DEPLOY_PHASES;
    return {
      id: r.failed ? r.failed.id : `dep-run-${env.name}-${r.version.replace(/\./g, "")}${r.seq ? `-${r.seq}` : ""}`,
      project: DEMO_PROJECT,
      environmentName: env.name,
      mode: "apply",
      source: ci ? "ci" : "terminal",
      releaseVersion: r.version,
      promotionId: r.failed ? "" : promotionId(r.envId, r.version, r.seq),
      run: ci ? { url: `https://${REPO}/actions/runs/${9_400_000 + Math.round(r.age / MINUTE)}` } : undefined,
      forgeVersion: FORGE_VERSION,
      startedByUserId: `user-${r.by}`,
      startedByActor: ci ? "github-actions" : r.by,
      phase: phases[phases.length - 1],
      phases: phases.map((name, i) => ({ name, startedAt: ago(r.age + took - (i * took) / phases.length) })),
      state: r.failed ? DeployAttemptState.FAILED : DeployAttemptState.SUCCEEDED,
      failedPhase: r.failed?.failedPhase ?? "",
      errorMessage: r.failed?.error ?? "",
      exitCode: r.failed ? 1 : 0,
      startedAt: ago(r.age + took),
      lastHeartbeatAt: ago(r.age),
      finishedAt: ago(r.age),
      hasLog: !!r.failed || ci,
      logTruncated: false,
      hasReport: !r.failed,
    };
  });
}

function deployLog(attempt: ReturnType<typeof deployAttempts>[number]): string {
  if (attempt.id === FAILED_DEPLOY.id) return FAILED_DEPLOY.log;
  return [
    `forge env deploy ${attempt.environmentName} --version ${attempt.releaseVersion}`,
    `  preflight  ok`,
    `  build      ${DEMO_WORKLOADS.length} artifacts, reused from ${attempt.releaseVersion}'s cut`,
    `  plan       ${DEMO_WORKLOADS.length} workloads to update, 0 to create, 0 to delete`,
    `  promote    ${attempt.promotionId}`,
    `  rollout    ${DEMO_WORKLOADS.map((w) => `${w.name} ✓`).join("  ")}`,
    `deployed ${attempt.releaseVersion} to ${attempt.environmentName}`,
  ].join("\n");
}

/**
 * What changed between two releases (indexes into DEMO_RELEASES, newest
 * first): the commits cut in between, the artifacts whose digest moved (an
 * artifact a release did not change keeps its digest, artifactVersion), and —
 * in one environment — the objects of its bundle that changed.
 */
function comparison(from: number, to: number, envId: EnvId | undefined) {
  const newer = Math.min(from, to);
  const older = Math.max(from, to);
  const between = DEMO_RELEASES.slice(newer, older);
  const commits = between.flatMap((r) => r.commits.map((c, i) => ({ ...c, time: ago(commitAgeMs(r, i)) })));
  const fromVersion = DEMO_RELEASES[from].version;
  const toVersion = DEMO_RELEASES[to].version;
  const changed = DEMO_WORKLOADS.filter((w) => artifactVersion(w.name, fromVersion) !== artifactVersion(w.name, toVersion));
  const env = envId ? DEMO_ENVS.find((e) => e.id === envId) : undefined;
  const timeoutChanged = [fromVersion, toVersion].includes("v1.8.4") && fromVersion !== toVersion;
  return {
    commits: {
      state: from === to ? DeployCommitComparisonState.SAME : DeployCommitComparisonState.COMPUTED,
      reverts: from < to,
      commits,
      total: commits.length,
      releases: between.map((r) => r.version),
    },
    artifacts: changed.map((w) => ({
      name: w.name,
      kind: w.tier === DeployTier.STATIC ? "static" : "image",
      change: DeployChangeKind.MODIFIED,
      from: artifactDigest(w.name, fromVersion),
      to: artifactDigest(w.name, toVersion),
    })),
    unchangedArtifacts: DEMO_WORKLOADS.length - changed.length,
    objects: env
      ? {
          state: DeployObjectComparisonState.COMPARED,
          environmentId: env.id,
          fromBundleDigest: demoBundleDigest(env.id, fromVersion),
          toBundleDigest: demoBundleDigest(env.id, toVersion),
          deltas: [
            ...changed
              .filter((w) => w.tier !== DeployTier.STATIC)
              .map((w) => ({
                change: DeployChangeKind.MODIFIED,
                kind: "Deployment",
                namespace: `shop-${env.name}`,
                name: w.name,
                workload: w.name,
                summary: `image ${fromVersion} → ${toVersion}`,
                images: [{ artifact: w.name, from: artifactDigest(w.name, fromVersion), to: artifactDigest(w.name, toVersion) }],
              })),
            ...(timeoutChanged
              ? [{ change: DeployChangeKind.MODIFIED, kind: "ConfigMap", namespace: `shop-${env.name}`, name: "api-config", workload: "api", summary: "ORDER_TIMEOUT_MS changed" }]
              : []),
          ],
          unchanged: 9 - changed.length,
        }
      : { state: DeployObjectComparisonState.NO_ENVIRONMENT },
  };
}

// ── Handlers ────────────────────────────────────────────────────────────────

// Scope, in every handler as well as around them all (demoScope.ts scopedToDemo): an environment
// id that is not one of shop's, or a project that is not shop, gets nothing.
const envIdOf = demoEnvId;
const forProject = inDemoProject;

/**
 * The story's overrides, by "pkg.Service/Method": what shop's console says.
 * Every other read is answered by forge's generated defaults
 * (demoServiceDefaults.ts), so only what the demo is ABOUT is written here.
 */
export function consoleScenarioHandlers(): MockHandlers {
  return {
    ...completed(
      DeployService,
      mockService(DeployService, {
        getLiveView: (req) => ({
          environments: forProject(req.project)
            ? DEMO_ENVS.map((env) => ({
                environment: environment(env),
                currentPromotion: promotion(env.id, RUNNING[env.id], PROMOTED_FROM[env.id]),
                phase: DeployRolloutPhase.SUCCEEDED,
                drift: { state: "in_sync", observedAt: ago(4 * MINUTE) },
                treeHealth: {
                  total: DEMO_WORKLOADS.length,
                  converged: DEMO_WORKLOADS.length,
                },
                timings: {
                  promotedAt: ago(RUNNING[env.id].age),
                  healthyAt: ago(RUNNING[env.id].age - 3 * MINUTE),
                },
              }))
            : [],
        }),
        listEnvironments: (req) => ({
          environments: forProject(req.project) ? DEMO_ENVS.map(environment) : [],
        }),
        getEnvironment: (req) => {
          const env = DEMO_ENVS.find((e) => e.id === envIdOf(req.environmentId));
          return env
            ? {
                environment: environment(env),
                currentPromotion: promotion(env.id, RUNNING[env.id], PROMOTED_FROM[env.id]),
              }
            : {};
        },
        getEnvironmentShape: (req) => {
          const id = envIdOf(req.environmentId);
          return id
            ? {
                declaredShape: shape(id),
                declaredBy: provenance(RUNNING[id].version),
                declaredAt: ago(RUNNING[id].age),
              }
            : {};
        },
        listConvergences: () => ({ convergences: [] }),
        getStatus: (req) => {
          const id = envIdOf(req.environmentId);
          if (!id) return {};
          // One deployment when the request names one: only shop's, in this environment.
          const rows = deployments(id).filter((d) => !req.deploymentId || d.deployment.id === req.deploymentId);
          if (rows.length === 0) return {};
          return {
            deployments: rows,
            currentPromotion: promotion(id, RUNNING[id], PROMOTED_FROM[id]),
            environmentVerdict: DeployVerdict.CONVERGED,
            observedAt: ago(2 * MINUTE),
          };
        },
        listReleases: (req) => ({
          releases:
            forProject(req.project) && (!req.promotedToEnvironmentId || envIdOf(req.promotedToEnvironmentId))
              ? DEMO_RELEASES.map(release).slice(0, req.limit && req.limit > 0 ? req.limit : undefined) : [],
        }),
        getRelease: (req) => {
          if (!forProject(req.project)) return {};
          const index = DEMO_RELEASES.findIndex((r) => r.version === req.version);
          if (index < 0) return {};
          const version = DEMO_RELEASES[index].version;
          return {
            release: release(DEMO_RELEASES[index], index),
            currentEnvironmentIds: DEMO_ENVS.filter((e) => RUNNING[e.id].version === version).map((e) => e.id),
          };
        },
        listPromotions: (req) => {
          const id = envIdOf(req.environmentId);
          if (!id) return { promotions: [] };
          const all = [RUNNING[id], ...HISTORY[id]].map((p) => promotion(id, p, PROMOTED_FROM[id]));
          return {
            promotions: req.limit && req.limit > 0 ? all.slice(0, req.limit) : all,
          };
        },
        getRollout: (req) => {
          const id = envIdOf(req.environmentId);
          if (!id) return {};
          const p =
            [RUNNING[id], ...HISTORY[id]].find((candidate) => promotionId(id, candidate.version, candidate.seq) === req.promotionId) ?? RUNNING[id];
          return {
            rollout: {
              promotion: promotion(id, p, PROMOTED_FROM[id]),
              phase: DeployRolloutPhase.SUCCEEDED,
              startedAt: ago(p.age),
              finishedAt: ago(p.age - 3 * MINUTE),
              stabilityWindowMs: BigInt(2 * MINUTE),
              convergesPromotions: true,
              workloads: DEMO_WORKLOADS.map((w) => ({
                deploymentId: `dep-${id}-${w.name}`,
                name: w.name,
                artifact: w.name,
                desiredDigest: artifactDigest(w.name, p.version),
                observedDigest: artifactDigest(w.name, p.version),
                observedState: DeployObservedState.READY,
                verdict: DeployVerdict.CONVERGED,
                phase: DeployRolloutPhase.SUCCEEDED,
                updatedReplicas: w.replicas,
                desiredReplicas: w.replicas,
                convergedAt: ago(p.age - 2 * MINUTE),
              })),
            },
          };
        },
        compareReleases: (req) => {
          if (!forProject(req.project) || (req.environmentId && !envIdOf(req.environmentId))) return {};
          const to = DEMO_RELEASES.findIndex((r) => r.version === req.toVersion);
          const from = DEMO_RELEASES.findIndex((r) => r.version === req.fromVersion);
          if (to < 0 || from < 0) return {};
          return {
            from: release(DEMO_RELEASES[from], from),
            to: release(DEMO_RELEASES[to], to),
            ...comparison(from, to, envIdOf(req.environmentId)),
          };
        },
        listDeployAttempts: (req) => {
          if (!forProject(req.project)) return { attempts: [] };
          const attempts = deployAttempts().filter((a) => !req.environmentName || a.environmentName === req.environmentName);
          return { attempts: req.limit > 0 ? attempts.slice(0, req.limit) : attempts };
        },
        getDeployAttempt: (req) => {
          const attempt = deployAttempts().find((a) => a.id === req.id);
          if (!attempt) return {};
          return {
            attempt,
            log: attempt.hasLog ? deployLog(attempt) : "",
            reportJson: attempt.hasReport ? JSON.stringify({ environment: attempt.environmentName, version: attempt.releaseVersion, promotion: attempt.promotionId, result: "deployed" }) : "",
          };
        },
        listGates: () => ({ gates: [] }),
      }),
    ),
    ...completed(
      SecretStoreService,
      mockService(SecretStoreService, {
        listSecrets: (req) => {
          const id = envIdOf(req.environmentId);
          return {
            protected: id === "env-prod",
            secrets: id
              ? DEMO_SECRETS.filter((s) => (s.set as readonly string[]).includes(id)).map((s) => ({
                  name: s.name,
                  currentVersion: s.versions,
                  oldestVersion: 1,
                  maxVersions: 10,
                  createdTime: ago(38 * DAY),
                  updatedTime: ago(SECRET_UPDATED[s.name] ?? 20 * DAY),
                }))
              : [],
          };
        },
        getSecretVersions: (req) => {
          // Only a secret this environment holds a value for: names never cross environments.
          const id = envIdOf(req.environmentId);
          const s = DEMO_SECRETS.find((candidate) => candidate.name === req.name);
          if (!id || !s || !(s.set as readonly string[]).includes(id)) return {};
          return {
            summary: {
              name: s.name,
              currentVersion: s.versions,
              oldestVersion: 1,
              maxVersions: 10,
              createdTime: ago(38 * DAY),
              updatedTime: ago(SECRET_UPDATED[s.name] ?? 20 * DAY),
            },
            versions: Array.from({ length: s.versions }, (_, i) => ({
              version: s.versions - i,
              createdTime: ago((SECRET_UPDATED[s.name] ?? 20 * DAY) + i * 9 * DAY),
              setBy: {
                kind: SecretAuthorKind.USER,
                email: `${["maya", "jon", "priya"][i % 3]}@example.com`,
              },
            })),
          };
        },
        listSecretAccess: () => ({ entries: [] }),
      }),
    ),
    ...completed(
      DomainService,
      mockService(DomainService, {
        listDomains: () => ({ domains: DEMO_DOMAINS.map(domain) }),
        getDomain: (req) => {
          const d = DEMO_DOMAINS.find((candidate) => candidate.id === req.domainId || candidate.hostname === req.hostname);
          return { domain: d ? domain(d) : undefined };
        },
        listDomainBindings: (req) => ({
          bindings: DEMO_DOMAINS.filter((d) => !req.environmentId || d.env === envIdOf(req.environmentId)).map((d) => domain(d).binding),
        }),
      }),
    ),
    ...completed(
      ActivityService,
      mockService(ActivityService, {
        listActivity: (req) => {
          const scope = req.scope;
          if (!forProject(scope?.projectId) || scope?.envIds.some((id) => !envIdOf(id))) return { events: [] };
          const envs = scope?.envIds.length ? new Set(scope.envIds) : null;
          return { events: activity().filter((e) => !envs || (e.env && envs.has(String(e.env.id)))) };
        },
      }),
    ),
  };
}
