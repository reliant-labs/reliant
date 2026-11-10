// Copyright (c) 2025 Reliant Labs

/**
 * The demo tells ONE story (shopStory.ts), whichever page a visitor opens.
 * Every read the console makes is asked here — over the demo transport where
 * the console reads it over the wire (Releases, Environments, Secrets,
 * Activity, Data, and the scenario's observability RPCs), and from the
 * demo's ObservabilityApi for the rest — and the answers are held to the
 * story:
 *
 *  - each release has ONE commit, and each promotion ONE promoter and ONE
 *    time, on the release page, the environment cards, the chart markers,
 *    release health, workloads and config alike;
 *  - every current promotion has a release-health verdict (the Health tab
 *    renders), and only prod's v1.8.3 fails it;
 *  - one service set (api, worker, inventory, the shop-web storefront) across
 *    traces, logs, profiles, metrics, workloads, the overview and the graph,
 *    and every overview process has a workload;
 *  - every person is the cast (jon, maya, priya), CI or the platform;
 *  - one incident: one open issue, one firing alert, nothing failing outside
 *    prod's api, the same error text everywhere;
 *  - no name from the console's unit-test fixtures reaches the demo, and no
 *    file the demo ships imports them.
 */

import { readdirSync, readFileSync, statSync } from "node:fs";
import path from "node:path";

import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";
import { createClient } from "@connectrpc/connect";

vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));

import { ActivityService } from "@/gen/controlplane/services/activity/v1/activity_pb";
import { AppDatabaseService } from "@/gen/controlplane/services/app_database/v1/app_database_pb";
import { DeployService } from "@/gen/controlplane/services/deploy/v1/deploy_pb";
import { SecretStoreService } from "@/gen/controlplane/services/secret_store/v1/secret_store_pb";
import { installShellTransport, setTransport } from "@/lib/connect";
import { controlPlaneObservabilityApi, type ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";

import { DEMO_PROFILE_TYPES } from "../busyApp";
import { createDemoTransport } from "../demoTransport";
import { createShopApi } from "../shop/shopApi";
import { DEMO_BACKENDS, DEMO_CAST, DEMO_DEPENDENCIES, DEMO_ENV_IDS, DEMO_FRONTEND, DEMO_INCIDENT, DEMO_RELEASES, DEMO_RPC_METHODS, DEMO_SERVICE_NAMES, demoNow, resetDemoNowForTests, RUNNING } from "../shopStory";

// A fixed afternoon (the story's load varies by hour): the same assertions hold whenever the suite runs.
resetDemoNowForTests(Date.UTC(2026, 10, 10, 15, 0, 0));
const NOW = demoNow();
const HOUR = 3_600_000;
const hour = { startMs: NOW - HOUR, endMs: NOW };
const day = { startMs: NOW - 24 * HOUR, endMs: NOW };
const scope = (envIds: string[] = []) => ({ orgId: "org-acme", projectId: "shop", envIds, filters: [] });

const CAST = new Set<string>(DEMO_CAST.map((p) => p.handle));
const ACTORS = new Set([...CAST, "github-actions", "reliant", ""]);
const SERVICES = new Set<string>(DEMO_SERVICE_NAMES);
const DEPENDENCIES = new Set<string>(Object.values(DEMO_DEPENDENCIES));

/** Strings from the console's unit-test fixtures (and Reliant's own product) that must never reach the demo. */
const LEAKS = [
  "Ada Lovelace",
  "Grace Hopper",
  "ada@",
  "grace@",
  "alan@",
  "user-ada",
  "\"batch\"",
  "Daemon fleet",
  "LLM usage",
  "Billing funnel",
  "Cannot read properties of undefined",
  "reliant-daemon",
  "admin-web",
  "u_8812",
  "v1.7.92",
  "v1.8.4-rc",
  "OOMKill",
  "search-reindex",
  "nightly-report",
  "CheckoutP95High",
  "packing-station",
  "Where is my order",
  "Linen tote /",
  "public.chats",
  "public.machines",
  "org-fixture",
  "too many clients",
  "release-fixture",
];

const json = (value: unknown) => JSON.stringify(value, (_k, v) => (typeof v === "bigint" ? Number(v) : v));

let transport: ReturnType<typeof createDemoTransport>;
let model: ObservabilityApi;
const answers: unknown[] = [];
const seen = <T>(value: T): T => {
  answers.push(value);
  return value;
};

beforeAll(() => {
  setTransport(null);
  transport = createDemoTransport({ source: "fixtures", url: "" });
  installShellTransport(transport);
  model = createShopApi(NOW);
});

afterAll(() => {
  installShellTransport(null as never);
  resetDemoNowForTests();
});

/** The demo's ObservabilityApi as the demo host composes it: the scenario's reads over the wire, the rest from the model. */
const obs = (): ObservabilityApi => ({
  ...model,
  listTraces: controlPlaneObservabilityApi.listTraces,
  getTrace: controlPlaneObservabilityApi.getTrace,
  searchLogs: controlPlaneObservabilityApi.searchLogs,
  getServiceOverview: controlPlaneObservabilityApi.getServiceOverview,
  getFlamegraph: controlPlaneObservabilityApi.getFlamegraph,
  getServiceSignals: controlPlaneObservabilityApi.getServiceSignals,
});

describe("the demo's one story", () => {
  it("gives each release one commit and each promotion one promoter and one time, everywhere", async () => {
    const deploy = createClient(DeployService, transport);
    const releases = seen(await deploy.listReleases({ project: "shop" })).releases;
    expect(releases.map((r) => r.version)).toEqual(DEMO_RELEASES.map((r) => r.version));
    const commitOf = new Map(releases.map((r) => [r.version, r.gitCommit]));
    for (const r of releases) {
      // Real changes: commits, a commit time (lead time), digests.
      expect(r.commits?.commits.length, r.version).toBeGreaterThan(0);
      expect(r.provenance?.commitTime, r.version).toBeDefined();
      expect(r.artifacts.every((a) => /^sha256:[0-9a-f]{64}$/.test(a.digest))).toBe(true);
      for (const c of r.commits!.commits) expect(CAST.has(c.author), c.author).toBe(true);
    }
    const compare = seen(await deploy.compareReleases({ project: "shop", environmentId: "env-prod", fromVersion: "v1.8.2", toVersion: "v1.8.3" }));
    expect(compare.commits?.commits.length).toBeGreaterThan(0);
    expect(compare.artifacts.length).toBeGreaterThan(0);
    expect(compare.objects?.deltas.length).toBeGreaterThan(0);

    const markers = seen(await model.listReleases({ ...scope(), window: { startMs: NOW - 30 * 24 * HOUR, endMs: NOW } })).promotions;
    for (const env of DEMO_ENV_IDS) {
      const promotions = seen(await deploy.listPromotions({ environmentId: env })).promotions;
      for (const p of promotions) {
        expect(ACTORS.has(p.promotedByActor), p.promotedByActor).toBe(true);
        expect(p.releaseProvenance?.commit).toBe(commitOf.get(p.releaseVersion));
        if (p.targets.length) continue;
        const marker = markers.find((m) => m.promotionId === p.id);
        expect(marker, p.id).toBeDefined();
        expect(marker!.gitCommit).toBe(commitOf.get(p.releaseVersion));
        expect(marker!.promotedBy).toBe(p.promotedByActor);
        expect(marker!.promotedAtMs).toBe(Number(p.createdAt!.seconds) * 1000 + Math.round(p.createdAt!.nanos / 1e6));
      }
      // No promotion the chart shows that the console's ledger does not.
      for (const m of markers.filter((x) => x.envId === env)) expect(promotions.some((p) => p.id === m.promotionId), m.promotionId).toBe(true);
    }

    // The environment cards, release health, workloads and config name the same commit.
    const live = seen(await deploy.getLiveView({ project: "shop" }));
    for (const e of live.environments) {
      const env = e.environment!.id as (typeof DEMO_ENV_IDS)[number];
      const current = e.currentPromotion!;
      expect(current.releaseVersion).toBe(RUNNING[env].version);
      const health = seen(await model.getReleaseHealth({ ...scope([env]), promotionId: current.id, windowMs: 15 * 60_000 }));
      expect(health.promotion?.gitCommit).toBe(commitOf.get(current.releaseVersion));
      expect(health.verdict).toBe(env === "env-prod" ? "fail" : "pass");
      const workloads = seen(await model.getWorkload({ ...scope([env]), window: hour, workload: "api" }));
      expect(workloads.workloads[0].declared?.sourceCommit).toBe(commitOf.get(current.releaseVersion));
      const config = seen(await deploy.getWorkloadConfig({ environmentId: env, workload: "api" }));
      expect(config.bundleId).toBe(workloads.workloads[0].declared?.bundleId);
      expect(config.bundleDigest).toBe(current.bundleDigest);
    }
  });

  it("has one service set, and every overview process has a workload", async () => {
    const api = obs();
    const traces = seen(await api.listTraces({ ...scope(), window: hour, rootSpanName: "", services: [], errorsOnly: false, minDurationMs: 0, limit: 500 }));
    for (const t of traces.traces) for (const s of [t.rootService, ...t.services]) expect(SERVICES.has(s), s).toBe(true);
    const logs = seen(await api.searchLogs({ ...scope(), window: hour, query: "", services: [], severities: [], limit: 1000 }));
    for (const r of logs.records) expect(SERVICES.has(r.service), r.service).toBe(true);
    const signals = seen(await api.getServiceSignals({ ...scope(), window: hour }));
    expect(signals.services.map((s) => s.service).sort()).toEqual([...DEMO_BACKENDS].sort());
    const catalog = seen(await model.listMetricCatalog({ ...scope(), window: hour }));
    for (const m of catalog.metrics) for (const s of m.services) expect(SERVICES.has(s), `${m.name}: ${s}`).toBe(true);
    const graph = seen(await model.getServiceGraph({ ...scope(["env-prod"]), window: hour, rpcIds: [], includeInternal: false }));
    for (const n of graph.nodes) expect(SERVICES.has(n.id) || DEPENDENCIES.has(n.id), n.id).toBe(true);
    for (const env of DEMO_ENV_IDS) {
      const overview = seen(await api.getServiceOverview({ ...scope([env]), window: hour }));
      const workloads = seen(await model.listWorkloads({ ...scope([env]), window: hour }));
      for (const s of overview.services) expect(workloads.workloads.some((w) => w.workload === s.name), `${s.name} in ${env}`).toBe(true);
      for (const w of workloads.workloads) {
        expect(SERVICES.has(w.workload)).toBe(true);
        expect(w.health).toBe("healthy");
      }
      expect(workloads.jobs.every((j) => j.result === "succeeded")).toBe(true);
    }
    // The release's rollout lists the same workloads (plus the static storefront).
    const deploy = createClient(DeployService, transport);
    const rollout = seen(await deploy.getRollout({ environmentId: "env-prod", promotionId: "promo-prod-v183" }));
    expect(rollout.rollout?.workloads.map((w) => w.name).sort()).toEqual([...DEMO_SERVICE_NAMES].sort());
    // Profiles: every service and type distinct.
    const flames = new Set<string>();
    for (const service of DEMO_BACKENDS) for (const profileType of DEMO_PROFILE_TYPES) flames.add((await api.getFlamegraph({ ...scope(), window: hour, service, profileType })).speedscopeJson);
    expect(flames.size).toBe(DEMO_BACKENDS.length * DEMO_PROFILE_TYPES.length);
  });

  it("has one incident: one open issue, one firing alert, nothing failing outside prod's charging path", async () => {
    const api = obs();
    const issues = seen(await model.listIssues({ ...scope(), window: day, statuses: [], sort: "last" }));
    expect(issues.issues.map((i) => i.id)).toEqual([DEMO_INCIDENT.issueId]);
    expect(issues.issues[0].title).toBe(DEMO_INCIDENT.error);
    expect(CAST.has(issues.issues[0].assignee?.name ?? "")).toBe(true);
    const detail = seen(await model.getIssue({ ...scope(), issueId: DEMO_INCIDENT.issueId }));
    expect(detail.firstRelease?.gitCommit).toBe(DEMO_RELEASES.find((r) => r.version === "v1.8.3")!.commit);
    const alerts = seen(await model.listAlerts("org-acme", ""));
    expect(alerts.filter((a) => a.state === "firing").map((a) => a.rule)).toEqual([DEMO_INCIDENT.alert]);
    const notifications = seen(await model.listNotifications("org-acme", {}));
    expect(notifications.unread).toBe(alerts.length);
    // Every route the shape declares has a receiver.
    const shape = seen(await createClient(DeployService, transport).getEnvironmentShape({ environmentId: "env-prod" }));
    const receivers = seen(await model.listReceivers("org-acme"));
    for (const route of (shape.declaredShape as { alert_routes: { receiver: string }[] }).alert_routes) expect(receivers.map((r) => r.name)).toContain(route.receiver);
    for (const env of ["env-dev", "env-staging"]) {
      const overview = await api.getServiceOverview({ ...scope([env]), window: day });
      expect(overview.services.every((s) => s.errorCount === 0), env).toBe(true);
    }
    const prod = seen(await api.getServiceOverview({ ...scope(["env-prod"]), window: hour }));
    expect(prod.services.filter((s) => s.errorCount > 0).map((s) => s.name)).toEqual(["api"]);
    const errors = seen(await api.searchLogs({ ...scope(), window: hour, query: "", services: [], severities: ["error"], limit: 500 }));
    expect(new Set(errors.records.map((r) => r.body))).toEqual(new Set([DEMO_INCIDENT.error]));
    // The headline, its sparkline and the table read the same number.
    const apiRow = prod.services.find((s) => s.name === "api")!;
    const sparkline = apiRow.errorTrend.reduce<number>((a, b) => a + (b ?? 0), 0);
    expect(sparkline).toBe(apiRow.errorCount);
  });

  it("names only the cast: activity, promotions, deploys, secrets, issues, dashboards, silences", async () => {
    const activity = seen(await createClient(ActivityService, transport).listActivity({ scope: { projectId: "shop", envIds: [] } }));
    for (const e of activity.events) expect(ACTORS.has(e.actor?.name ?? ""), e.actor?.name).toBe(true);
    const attempts = seen(await createClient(DeployService, transport).listDeployAttempts({ project: "shop" }));
    for (const a of attempts.attempts) expect(ACTORS.has(a.startedByActor), a.startedByActor).toBe(true);
    for (const env of DEMO_ENV_IDS) {
      const secrets = seen(await createClient(SecretStoreService, transport).listSecrets({ environmentId: env }));
      for (const s of secrets.secrets) {
        const versions = seen(await createClient(SecretStoreService, transport).getSecretVersions({ environmentId: env, name: s.name }));
        for (const v of versions.versions) expect(CAST.has(v.setBy?.email.split("@")[0] ?? ""), v.setBy?.email).toBe(true);
      }
    }
    const members = seen(await model.listOrgMembers("org-acme"));
    expect(members.users.map((u) => u.name).sort()).toEqual([...CAST].sort());
    const dashboards = seen(await model.listDashboards({ orgId: "org-acme", projectId: "shop" }));
    for (const d of dashboards.dashboards) {
      if (d.workspace) expect(CAST.has(d.workspace.createdBy?.name ?? "")).toBe(true);
      const versions = seen(await model.listDashboardVersions("org-acme", d.ref));
      for (const v of versions) expect(ACTORS.has(v.author?.name ?? ""), v.author?.name).toBe(true);
    }
    const silences = seen(await model.listSilences("org-acme", true));
    for (const s of silences) expect(CAST.has(s.createdBy.split("@")[0])).toBe(true);
    const timeline = seen(await model.listIssueTimeline({ issueId: DEMO_INCIDENT.issueId }));
    for (const e of timeline.entries) if (e.actor?.kind === "user") expect(CAST.has(e.actor.name)).toBe(true);
  });

  it("lists the shop's real RPC methods, freshly aggregated, with plausible metrics", async () => {
    const catalog = seen(await model.getServiceGraphCatalog({ ...scope(["env-prod"]), window: hour }));
    expect(catalog.methods.map((m) => m.id).sort()).toEqual(DEMO_RPC_METHODS.map((m) => m.id).sort());
    expect(catalog.quality!.aggregatedThroughMs).toBeGreaterThanOrEqual(NOW - 60_000);
    expect(new Set(catalog.methods.map((m) => m.observedCount)).size).toBe(catalog.methods.length);
    const graph = seen(await model.getServiceGraph({ ...scope(["env-prod"]), window: hour, rpcIds: [], includeInternal: false }));
    expect(new Set(graph.edges.map((e) => e.count)).size).toBeGreaterThan(graph.edges.length - 3);
    const metrics = seen(await model.listMetricCatalog({ ...scope(), window: hour }));
    for (const m of metrics.metrics) {
      const panel = { id: m.name, title: m.name, kind: "line", signal: "metrics", series: [{ label: "v", agg: m.monotonic ? "rate" : m.kind === "histogram" ? "quantile" : "avg", quantile: 0.95, field: m.name }] };
      const res = seen(await model.queryPanel({ ...scope(["env-prod"]), window: hour, panel, services: [], stepSeconds: 0 }));
      const values = res.series.flatMap((s) => s.values).filter((v): v is number => v !== null);
      expect(values.length, `${m.name} has data`).toBeGreaterThan(0);
      if (m.unit === "By" && !m.monotonic) expect(Math.min(...values), m.name).toBeGreaterThan(10 * 2 ** 20);
      if (m.name === "go.goroutine.count") expect(Math.min(...values)).toBeGreaterThan(5);
    }
  });

  it("puts every browser session on the release its environment ran", async () => {
    const sessions = seen(await model.listSessions({ ...scope(), window: day, hasError: false, hasReplay: false, userId: "", pathContains: "", pageSize: 100 }));
    expect(sessions.sessions.length).toBeGreaterThan(10);
    const versions = new Set<string>(DEMO_RELEASES.map((r) => r.version));
    for (const s of sessions.sessions) {
      expect(versions.has(s.serviceVersion), s.serviceVersion).toBe(true);
      expect(s.release).toBe(s.serviceVersion);
      expect(s.service).toBe(DEMO_FRONTEND);
    }
    expect(sessions.sessions.filter((s) => s.errors > 0).every((s) => s.envId === "env-prod" && s.startedMs >= NOW - RUNNING["env-prod"].age)).toBe(true);
  });

  it("reads a shop's database, not Reliant's", async () => {
    const data = createClient(AppDatabaseService, transport);
    const desc = seen(await data.describeDatabase({ environmentId: "env-staging", database: "shop" }));
    expect(desc.tables.map((t) => t.name).sort()).toEqual(["customers", "order_items", "orders", "products", "stock"]);
  });

  it("leaks no fixture or Reliant-product name into anything the demo answered", () => {
    expect(answers.length).toBeGreaterThan(30);
    const all = json(answers);
    for (const leak of LEAKS) expect(all.includes(leak), leak).toBe(false);
  });

  it("ships no demo data file that imports the console's unit-test fixtures", () => {
    const root = path.resolve(__dirname, "..");
    const files: string[] = [];
    const walk = (dir: string) => {
      for (const name of readdirSync(dir)) {
        const full = path.join(dir, name);
        if (name === "__tests__") continue;
        if (statSync(full).isDirectory()) walk(full);
        else if (/\.tsx?$/.test(name)) files.push(full);
      }
    };
    walk(root);
    expect(files.length).toBeGreaterThan(10);
    for (const file of files) expect(readFileSync(file, "utf8").includes("__fixtures__"), path.relative(root, file)).toBe(false);
  });
});
