// Copyright (c) 2025 Reliant Labs

/**
 * The sample app's console data, on the wire: what control-plane would answer
 * for shop's Releases, Environments, Secrets, Domains and Activity tabs, for
 * the demo's mock transport (demoScenario.ts spreads these handlers in).
 *
 * The same story as the observability fixtures (Observability/__fixtures__):
 *
 *   shop has an api, a worker, an inventory service and a static storefront,
 *   in dev, staging and prod (env-dev / env-staging / env-prod). prod and
 *   staging run v1.8.3, the release the CreateOrder deadline issue first
 *   appeared in (prod got it 40 minutes ago, the observability fixtures'
 *   ledger marker, fixtureDelivery.ts); dev already runs v1.8.4, the fix.
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
  DeployCustomDomainState,
  DeployEnvironmentKind,
  DeployObservedState,
  DeployRolloutPhase,
  DeployRunState,
  DeployServingPath,
  DeployTier,
  DeployVerdict,
  DomainSource,
} from "@/gen/controlplane/controlplane/v1/deploy_pb";
import { ActivityActorKind, ActivityEventSchema, ActivityService } from "@/gen/controlplane/services/activity/v1/activity_pb";
import { DeployService } from "@/gen/controlplane/services/deploy/v1/deploy_pb";
import { DomainService } from "@/gen/controlplane/services/domain/v1/domain_pb";
import { SecretAuthorKind, SecretStoreService } from "@/gen/controlplane/services/secret_store/v1/secret_store_pb";

import { completed } from "./demoServiceDefaults";
import { DEMO_ENVS, DEMO_PROJECT, demoEnvId, inDemoProject, type DemoEnvId } from "./demoScope";
import { mockService, type MockHandlers } from "./forgeMockValues/mock-values";

const REPO = "github.com/acme/shop";
const FORGE_VERSION = "v1.0.17";

const MINUTE = 60_000;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;

/** `ms` ago, from this page load. */
const ago = (ms: number, now = Date.now()): Timestamp => timestampFromMs(now - ms);

export { DEMO_ENVS, DEMO_PROJECT };

type EnvId = DemoEnvId;

/** The releases, newest first: what each changed, and when it was cut. */
export const DEMO_RELEASES = [
  {
    version: "v1.8.4",
    commit: "9c2e41d07a3f",
    subject: "fix(orders): give the payment call its own deadline and retry budget",
    author: "maya",
    age: 30 * MINUTE,
    commits: [
      {
        sha: "9c2e41d07a3f",
        subject: "fix(orders): give the payment call its own deadline and retry budget",
        author: "maya",
      },
      {
        sha: "51ab0e9c11d2",
        subject: "test(orders): CreateOrder survives a slow payment provider",
        author: "maya",
      },
    ],
  },
  {
    version: "v1.8.3",
    commit: "e7d93a2b5c18",
    subject: "feat(orders): retry the payment charge once on a timeout",
    author: "jon",
    age: 26 * HOUR,
    commits: [
      {
        sha: "e7d93a2b5c18",
        subject: "feat(orders): retry the payment charge once on a timeout",
        author: "jon",
      },
      {
        sha: "a40f6c2d9e77",
        subject: "feat(inventory): reserve stock from the order event",
        author: "priya",
      },
      {
        sha: "3b81d5f0e2c4",
        subject: "chore(deps): bump connect-go to v1.19",
        author: "jon",
      },
    ],
  },
  {
    version: "v1.8.2",
    commit: "c51f08e3a96b",
    subject: "feat(web): show the order's status on the confirmation page",
    author: "priya",
    age: 6 * DAY,
    commits: [
      {
        sha: "c51f08e3a96b",
        subject: "feat(web): show the order's status on the confirmation page",
        author: "priya",
      },
    ],
  },
  {
    version: "v1.8.1",
    commit: "07e2b9d4f1a0",
    subject: "fix(worker): ack the order event only after inventory answers",
    author: "maya",
    age: 9 * DAY,
    commits: [
      {
        sha: "07e2b9d4f1a0",
        subject: "fix(worker): ack the order event only after inventory answers",
        author: "maya",
      },
    ],
  },
] as const;

/** Which release each environment runs, and when it got it. */
const RUNNING: Record<EnvId, { version: string; age: number; by: string }> = {
  "env-dev": { version: "v1.8.4", age: 20 * MINUTE, by: "maya" },
  "env-staging": { version: "v1.8.3", age: 3 * HOUR, by: "jon" },
  "env-prod": { version: "v1.8.3", age: 40 * MINUTE, by: "jon" },
};

/** Earlier promotions, newest first (beyond the one running). */
const HISTORY: Record<EnvId, { version: string; age: number; by: string }[]> = {
  "env-dev": [
    { version: "v1.8.3", age: 26 * HOUR, by: "jon" },
    { version: "v1.8.2", age: 6 * DAY, by: "priya" },
  ],
  "env-staging": [
    { version: "v1.8.2", age: 5.8 * DAY, by: "priya" },
    { version: "v1.8.1", age: 9 * DAY, by: "maya" },
  ],
  "env-prod": [
    { version: "v1.8.2", age: 5.5 * DAY, by: "priya" },
    { version: "v1.8.1", age: 8.5 * DAY, by: "maya" },
  ],
};

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

const releaseOf = (version: string) => DEMO_RELEASES.find((r) => r.version === version) ?? DEMO_RELEASES[0];
const promotionId = (env: string, version: string) => `promo-${env.replace("env-", "")}-${version.replace(/\./g, "")}`;
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
  };
}

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

function promotion(envId: EnvId, p: { version: string; age: number; by: string }, from?: string) {
  const r = releaseOf(p.version);
  return {
    id: promotionId(envId, p.version),
    environmentId: envId,
    releaseId: `rel-${r.version}`,
    releaseVersion: r.version,
    promotedByActor: p.by,
    createdAt: ago(p.age),
    releaseProvenance: provenance(r.version),
    fromEnvironmentName: from ?? "",
    resolvedArtifacts: Object.fromEntries(DEMO_WORKLOADS.map((w) => [w.name, digest(`${w.name}@${r.version}`)])),
    gates: [
      {
        name: "forge obs check",
        status: "passed",
        summary: "error rate and p95 within the previous release's window",
        finishedAt: ago(p.age - 12 * MINUTE),
      },
      {
        name: "ci",
        status: "passed",
        summary: "build, lint (27 linters) and tests",
        finishedAt: ago(p.age + 20 * MINUTE),
      },
    ],
  };
}

const PROMOTED_FROM: Record<EnvId, string | undefined> = {
  "env-dev": undefined,
  "env-staging": "dev",
  "env-prod": "staging",
};

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
      digest: digest(`${w.name}@${r.version}`),
      uri: w.tier === DeployTier.STATIC ? `sites/shop-web/${r.version}` : `registry.example.com/shop/${w.name}`,
      version: r.version,
      platforms: w.tier === DeployTier.STATIC ? [] : ["linux/amd64", "linux/arm64"],
    })),
    commits: {
      baseRelease: base?.version ?? "",
      baseCommit: base?.commit ?? "",
      commits: r.commits.map((c, i) => ({
        ...c,
        time: ago(r.age + (i + 1) * 40 * MINUTE),
      })),
      total: r.commits.length,
    },
  };
}

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
    alerts: [
      {
        alert: "OrdersErrorRateHigh",
        expr: 'rate(rpc_server_errors{service="api"}[5m]) / rate(rpc_server_requests{service="api"}[5m]) > 0.05',
        for: "5m",
        labels: { severity: "page" },
        annotations: { summary: "More than 5% of api requests are failing" },
        source: "user",
        default_id: "",
      },
      {
        alert: "OrdersLatencyHigh",
        expr: 'histogram_quantile(0.95, rpc_server_duration{service="api"}) > 0.5',
        for: "10m",
        labels: { severity: "ticket" },
        annotations: { summary: "api p95 over 500 ms" },
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
    ],
    alert_routes: [
      { match: { severity: "page" }, receiver: "oncall" },
      { match: { severity: "ticket" }, receiver: "github-issues" },
    ],
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
        imageDigest: digest(`${w.name}@${running.version}`),
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
    desiredDigest: digest(`${w.name}@${running.version}`),
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

function activity(): MessageInitShape<typeof ActivityEventSchema>[] {
  const env = (id: EnvId) => ({
    id,
    name: DEMO_ENVS.find((e) => e.id === id)!.name,
  });
  const user = (name: string) => ({
    id: `user-${name}`,
    name,
    kind: ActivityActorKind.USER,
  });
  const agent = {
    id: "token-ci",
    name: "github-actions",
    kind: ActivityActorKind.MACHINE,
  };
  return [
    {
      id: "act-1",
      at: ago(20 * MINUTE),
      actor: user("maya"),
      verb: "promote",
      target: { kind: "release", id: "v1.8.4", label: "v1.8.4" },
      env: env("env-dev"),
      via: "cli",
      detail: { from: "v1.8.3" },
    },
    {
      id: "act-2",
      at: ago(30 * MINUTE),
      actor: agent,
      verb: "cut",
      target: { kind: "release", id: "v1.8.4", label: "v1.8.4" },
      env: env("env-dev"),
      via: "ci",
      detail: {},
    },
    {
      id: "act-3",
      at: ago(35 * MINUTE),
      actor: user("jon"),
      verb: "assign",
      target: {
        kind: "issue",
        id: "iss-orders-deadline",
        label: "CreateOrder: context deadline exceeded",
      },
      env: env("env-prod"),
      via: "console",
      detail: { assignee: "maya" },
    },
    {
      id: "act-4",
      at: ago(40 * MINUTE),
      actor: user("jon"),
      verb: "promote",
      target: { kind: "release", id: "v1.8.3", label: "v1.8.3" },
      env: env("env-prod"),
      via: "cli",
      detail: { from: "v1.8.2" },
    },
    {
      id: "act-5",
      at: ago(3 * HOUR),
      actor: user("jon"),
      verb: "promote",
      target: { kind: "release", id: "v1.8.3", label: "v1.8.3" },
      env: env("env-staging"),
      via: "cli",
      detail: { from: "v1.8.2" },
    },
    {
      id: "act-6",
      at: ago(3 * DAY),
      actor: user("priya"),
      verb: "rotate",
      target: {
        kind: "secret",
        id: "SESSION_SIGNING_KEY",
        label: "SESSION_SIGNING_KEY",
      },
      env: env("env-prod"),
      via: "cli",
      detail: {},
    },
  ].map((e) => ({
    ...e,
    detail: e.detail as Record<string, string>,
    project: DEMO_PROJECT,
  }));
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
            [RUNNING[id], ...HISTORY[id]].find((candidate) => promotionId(id, candidate.version) === req.promotionId) ?? RUNNING[id];
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
                desiredDigest: digest(`${w.name}@${p.version}`),
                observedDigest: digest(`${w.name}@${p.version}`),
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
            unchangedArtifacts: 0,
          };
        },
        listDeployAttempts: () => ({ attempts: [] }),
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
                  updatedTime: ago(s.name === "SESSION_SIGNING_KEY" ? 3 * DAY : 20 * DAY),
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
              updatedTime: ago(20 * DAY),
            },
            versions: Array.from({ length: s.versions }, (_, i) => ({
              version: s.versions - i,
              createdTime: ago((i + 1) * 9 * DAY),
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
