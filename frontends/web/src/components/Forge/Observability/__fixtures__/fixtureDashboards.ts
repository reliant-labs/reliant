// Copyright (c) 2025 Reliant Labs

/**
 * An in-memory DashboardService for the fixture app (`shop`): the release
 * dashboards its bundle carries (forge's generated set, and two from the
 * repo), three workspace dashboards with version histories, stars, recently
 * viewed, and a template gallery — and every write behaving as control-plane
 * documents it: optimistic versions (a stale base is Aborted), slugs that
 * collide (AlreadyExists), release dashboards and templates read-only
 * (FailedPrecondition), archive, restore as a new version, and promote to
 * code returning the file with no pull request yet.
 *
 * State lives in the fixture instance, so a test or the preview harness can
 * star, save and restore and see the result on every surface.
 */

import { Code, ConnectError } from "@connectrpc/connect";

import type {
  DashboardDocument,
  DashboardRef,
  DashboardSummary,
  DashboardUser,
  DashboardVersion,
  PromotedDashboard,
} from "@/services/controlPlane/observability/dashboardTypes";
import type { ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import type { Dashboard, Panel } from "@/services/controlPlane/observability/types";

import { FIXTURE_ENVS, FIXTURE_PROJECT, fixtureDashboards, fixtureDayGrainedDashboard, fixtureReleaseDashboards } from "./fixtureApi";

export interface DashboardFixtureOptions {
  /** The caller is not an org admin: reads work, every write is PermissionDenied. */
  dashboardsReadOnly?: boolean;
  /** Nothing shipped, nothing saved: an empty index (templates still answer). */
  dashboardsEmpty?: boolean;
  /** Every DashboardService call fails with this code. */
  dashboardsFailWith?: Code;
  /** Ships control-plane's day-grained product-usage dashboard (a 7d defaultRange, sql panels bucketed by day). */
  dashboardsDayGrained?: boolean;
}

type Call = <T>(rpc: string, request: unknown, answer: () => T) => Promise<T>;

const SEAN: DashboardUser = { id: "u-ada", name: "ada", email: "ada@example.com" };
const ROBOT: DashboardUser = { id: "u-reliant-01", name: "reliant-01", email: "reliant-01@reliant.dev" };
const YOU: DashboardUser = { id: "u-you", name: "you", email: "you@reliant.dev" };

const MINUTE = 60_000;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;

interface WorkspaceRow {
  slug: string;
  envIds: string[];
  forkedFrom: string;
  createdBy: DashboardUser;
  createdAtMs: number;
  versions: { version: number; spec: Dashboard; author: DashboardUser; message: string; atMs: number }[];
  archived: boolean;
}

interface ReleaseRow {
  spec: Dashboard;
  source: "generated" | "repo";
  releaseVersion: string;
  shippedAtMs: number;
  filePath: string;
  envIds: string[];
  /** Releases in which this dashboard's content changed, newest first. */
  changes: { releaseVersion: string; author: DashboardUser; atMs: number }[];
}

const RPC_SERVER = [{ field: "span.kind", op: "eq", value: "server" }];

function checkoutLatency(version: number): Dashboard {
  const panels: Panel[] = [
    { id: "p95", title: "p95 checkout", kind: "number", signal: "traces", where: [...RPC_SERVER, { field: "span.name", op: "eq", value: "orders.v1.OrdersService/CreateOrder" }], series: [{ label: "p95", agg: "quantile", quantile: 0.95, field: "span.duration" }], unit: "ms", width: 6, height: 4 },
    { id: "orders", title: "Orders per bucket", kind: "number", signal: "traces", where: [{ field: "span.name", op: "eq", value: "orders.v1.OrdersService/CreateOrder" }], series: [{ label: "orders", agg: "count" }], unit: "count", width: 6, height: 4 },
    { id: "error-rate", title: "Checkout error rate", kind: "number", signal: "traces", where: RPC_SERVER, series: [{ label: "errors", agg: "count", where: [{ field: "span.status", op: "eq", value: "error" }] }], unit: "count", width: 6, height: 4 },
    { id: "conversion", title: "Conversion", kind: "funnel", signal: "traces", series: [], funnel: { steps: ["begin_checkout", "purchase"], window: "1h", by: "session" }, width: 6, height: 4 },
    { id: "latency-by-route", title: "p95 latency by route", description: "Traces · top routes", kind: "line", signal: "traces", where: RPC_SERVER, series: [{ label: "p95", agg: "quantile", quantile: 0.95, field: "span.duration" }], groupBy: ["span.name"], unit: "ms", width: 18, height: 7 },
    { id: "slowest", title: "Slowest routes", description: "p95, requests", kind: "table", signal: "traces", where: RPC_SERVER, series: [{ label: "p95", agg: "quantile", quantile: 0.95, field: "span.duration" }, { label: "requests", agg: "count" }], groupBy: ["span.name"], width: 6, height: 7 },
    { id: "issues", title: "Checkout issues", kind: "issues", signal: "traces", series: [], where: [{ field: "service.name", op: "eq", value: "web" }], width: 12, height: 8 },
    { id: "recent-errors", title: "Recent errors", description: "level ≥ error", kind: "search", signal: "logs", where: [{ field: "log.severity", op: "eq", value: "error" }], series: [], columns: ["timestamp", "service.name", "log.body"], width: 12, height: 4 },
  ];
  // v1–v3 had fewer panels: a history that restores to something visibly different.
  const rpcPanels = ["p95", "orders", "error-rate", "latency-by-route", "slowest"];
  return {
    id: "checkout-latency", title: "Checkout latency", description: "What a slow checkout looks like, and where it comes from.",
    sections: [{ id: "requests", title: "Requests" }, { id: "experience", title: "Experience" }],
    filters: [
      { id: "service", label: "Process", field: "service.name" },
      { id: "rpc-service", label: "RPC service", field: "span.attr.rpc.service", signal: "traces", panelIds: rpcPanels },
      { id: "rpc-method", label: "RPC method", field: "span.attr.rpc.method", signal: "traces", panelIds: rpcPanels },
    ],
    annotations: [{ source: "releases", label: "Releases" }],
    panels: (version >= 4 ? panels : panels.slice(0, 2 + version)).map((panel) => ({ ...panel, section: rpcPanels.includes(panel.id) ? "requests" : "experience" })),
  };
}

function daemonFleet(): Dashboard {
  return {
    id: "daemon-fleet",
    title: "Daemon fleet",
    filters: [],
    annotations: [{ source: "releases" }],
    panels: [
      { id: "online", title: "Requests by service", kind: "line", signal: "traces", where: RPC_SERVER, series: [{ label: "requests", agg: "count" }], groupBy: ["service.name"], unit: "count", width: 12, height: 7 },
      { id: "goroutines", title: "Goroutines", kind: "line", signal: "metrics", series: [{ label: "goroutines", agg: "sum", field: "go.goroutine.count", metricType: "sum" }], unit: "count", width: 12, height: 7 },
    ],
  };
}

function billingFunnel(): Dashboard {
  return {
    id: "billing-funnel",
    title: "Billing funnel",
    filters: [],
    panels: [
      { id: "plans", title: "Plan to payment", kind: "funnel", signal: "traces", series: [], funnel: { steps: ["view_pricing", "choose_plan", "enter_card", "subscribed"], window: "7d", by: "user" }, width: 24, height: 8 },
    ],
  };
}

/** What the index shows small: up to three number and line panels, numbers first. */
function previewsOf(spec: Dashboard): Panel[] {
  const pick = (kind: string) => spec.panels.filter((p) => p.kind === kind);
  return [...pick("number"), ...pick("line")].slice(0, 3);
}

function seed(now: number) {
  const { generated, repo } = fixtureReleaseDashboards();
  const [, serviceApi] = fixtureDashboards();
  const prod = FIXTURE_ENVS.find((e) => e.name === "prod")!.id;
  const staging = FIXTURE_ENVS.find((e) => e.name === "staging")!.id;
  const releases: ReleaseRow[] = [
    ...[...generated, serviceApi].map((spec) => ({
      spec,
      source: "generated" as const,
      releaseVersion: "v1.8.3",
      shippedAtMs: now - 5 * HOUR,
      filePath: "",
      envIds: [prod, staging],
      changes: [
        { releaseVersion: "v1.8.3", author: SEAN, atMs: now - 5 * HOUR },
        { releaseVersion: "v1.8.1", author: ROBOT, atMs: now - 3 * DAY },
      ],
    })),
    ...repo.map((spec) => ({
      spec,
      source: "repo" as const,
      releaseVersion: "v1.8.2",
      shippedAtMs: now - 16 * HOUR,
      filePath: `deploy/observability/dashboards/${spec.id}.json`,
      envIds: [prod, staging],
      changes: [{ releaseVersion: "v1.8.2", author: ROBOT, atMs: now - 16 * HOUR }],
    })),
  ];
  const workspace: WorkspaceRow[] = [
    {
      slug: "checkout-latency",
      envIds: [],
      forkedFrom: "",
      createdBy: SEAN,
      createdAtMs: now - 5 * DAY,
      archived: false,
      versions: [
        { version: 1, spec: checkoutLatency(1), author: SEAN, message: "Created", atMs: now - 5 * DAY },
        { version: 2, spec: checkoutLatency(2), author: ROBOT, message: "Add orders", atMs: now - 3 * DAY },
        { version: 3, spec: checkoutLatency(3), author: SEAN, message: "Error rate", atMs: now - DAY },
        { version: 4, spec: checkoutLatency(4), author: SEAN, message: "Routes, issues and recent errors", atMs: now - 2 * HOUR },
      ],
    },
    {
      slug: "daemon-fleet",
      envIds: [prod],
      forkedFrom: "template:service-red",
      createdBy: SEAN,
      createdAtMs: now - 9 * DAY,
      archived: false,
      versions: Array.from({ length: 7 }, (_, i) => ({ version: i + 1, spec: daemonFleet(), author: i % 2 ? ROBOT : SEAN, message: "", atMs: now - (9 - i) * DAY })),
    },
    {
      slug: "billing-funnel",
      envIds: [],
      forkedFrom: "",
      createdBy: ROBOT,
      createdAtMs: now - 6 * DAY,
      archived: false,
      versions: [1, 2, 3].map((version) => ({ version, spec: billingFunnel(), author: ROBOT, message: "", atMs: now - (8 - version) * DAY })),
    },
  ];
  const stars = new Set(["workspace/checkout-latency", "release/overview", "release/frontend-web"]);
  const viewed = new Map<string, number>([
    ["workspace/checkout-latency", now - 10 * MINUTE],
    ["release/service-orders", now - 3 * HOUR],
    ["release/workloads", now - DAY],
    ["workspace/daemon-fleet", now - 2 * DAY],
  ]);
  const templates: { id: string; title: string; description: string; spec: Dashboard }[] = [
    { id: "service-red", title: "Service health (RED)", description: "Rate, errors and duration per rpc, with release markers", spec: { ...serviceApi, id: "service-red", title: "Service health" } },
    ...generated
      .filter((d) => d.id === "workloads" || d.id === "releases")
      .map((d) => ({
        id: d.id,
        title: d.id === "workloads" ? "Kubernetes workloads" : "Release health",
        description: d.id === "workloads" ? "Ready replicas, restarts, CPU and memory against limits" : "Before and after a release: errors, latency, saturation",
        spec: d,
      })),
    { id: "frontend", title: "Frontend", description: "Issues, sessions with errors, web vitals and a funnel", spec: repo[1] },
  ];
  return { releases, workspace, stars, viewed, templates };
}

const keyOf = (ref: Pick<DashboardRef, "kind" | "key">) => `${ref.kind}/${ref.key}`;

export function createDashboardFixture(options: DashboardFixtureOptions, call: Call): Pick<
  ObservabilityApi,
  | "listDashboards"
  | "listDashboardTemplates"
  | "getDashboard"
  | "saveDashboard"
  | "saveDashboardAsCopy"
  | "listDashboardVersions"
  | "restoreDashboardVersion"
  | "archiveDashboard"
  | "starDashboard"
  | "recordDashboardView"
  | "promoteDashboardToCode"
> {
  const state = seed(Date.now());
  if (options.dashboardsEmpty) {
    state.releases = [];
    state.workspace = [];
  }
  if (options.dashboardsDayGrained) {
    const prod = FIXTURE_ENVS.find((e) => e.name === "prod")!.id;
    state.releases.push({
      spec: fixtureDayGrainedDashboard(),
      source: "repo",
      releaseVersion: "v1.8.2",
      shippedAtMs: Date.now() - 16 * HOUR,
      filePath: "deploy/observability/dashboards/product-usage.json",
      envIds: [prod],
      changes: [{ releaseVersion: "v1.8.2", author: ROBOT, atMs: Date.now() - 16 * HOUR }],
    });
  }
  const canWrite = !options.dashboardsReadOnly;

  const guarded: Call = (rpc, request, answer) =>
    call(rpc, request, () => {
      if (options.dashboardsFailWith !== undefined) throw new ConnectError("dashboards fixture failure", options.dashboardsFailWith);
      return answer();
    });
  const mustWrite = () => {
    if (!canWrite) throw new ConnectError("authoring dashboards needs the admin role", Code.PermissionDenied);
  };
  const live = () => state.workspace.filter((w) => !w.archived);
  const findWorkspace = (slug: string) => {
    const row = live().find((w) => w.slug === slug);
    if (!row) throw new ConnectError(`no workspace dashboard "${slug}"`, Code.NotFound);
    return row;
  };
  const findRelease = (key: string) => {
    const row = state.releases.find((r) => r.spec.id === key);
    if (!row) throw new ConnectError(`no release dashboard "${key}"`, Code.NotFound);
    return row;
  };
  const current = (row: WorkspaceRow) => row.versions[row.versions.length - 1];

  const releaseSummary = (row: ReleaseRow, envId?: string): DashboardSummary | null => {
    if (envId && !row.envIds.includes(envId)) return null;
    const ref: DashboardRef = { kind: "release", projectId: FIXTURE_PROJECT, key: row.spec.id };
    return {
      ref,
      source: row.source,
      title: row.spec.title,
      description: row.spec.description ?? "",
      readOnly: true,
      starred: state.stars.has(keyOf(ref)),
      lastViewedAtMs: state.viewed.get(keyOf(ref)) ?? null,
      panelCount: row.spec.panels.length,
      previewPanels: previewsOf(row.spec),
      release: { releaseVersion: row.releaseVersion, shippedAtMs: row.shippedAtMs, filePath: row.filePath, envIds: row.envIds },
      workspace: null,
      category: "",
    };
  };
  const workspaceSummary = (row: WorkspaceRow): DashboardSummary => {
    const ref: DashboardRef = { kind: "workspace", projectId: FIXTURE_PROJECT, key: row.slug };
    const head = current(row);
    return {
      ref,
      source: "workspace",
      title: head.spec.title,
      description: head.spec.description ?? "",
      readOnly: false,
      starred: state.stars.has(keyOf(ref)),
      lastViewedAtMs: state.viewed.get(keyOf(ref)) ?? null,
      panelCount: head.spec.panels.length,
      previewPanels: previewsOf(head.spec),
      release: null,
      workspace: {
        version: head.version,
        createdBy: row.createdBy,
        updatedBy: head.author,
        createdAtMs: row.createdAtMs,
        updatedAtMs: head.atMs,
        forkedFrom: row.forkedFrom,
        envIds: row.envIds,
      },
      category: "",
    };
  };
  const templateSummary = (t: (typeof state.templates)[number]): DashboardSummary => ({
    ref: { kind: "template", projectId: "", key: t.id },
    source: "template",
    title: t.title,
    description: t.description,
    readOnly: true,
    starred: false,
    lastViewedAtMs: null,
    panelCount: t.spec.panels.length,
    previewPanels: previewsOf(t.spec),
    release: null,
    workspace: null,
    category: "generated",
  });
  const versionInfo = (row: WorkspaceRow, v: WorkspaceRow["versions"][number]): DashboardVersion => ({
    version: v.version,
    releaseVersion: "",
    author: v.author,
    message: v.message,
    createdAtMs: v.atMs,
    current: v.version === current(row).version,
  });
  const write = (row: WorkspaceRow, spec: Dashboard, message: string) => {
    const version = current(row).version + 1;
    row.versions.push({ version, spec: { ...spec, id: row.slug }, author: YOU, message, atMs: Date.now() });
    return { summary: workspaceSummary(row), version: versionInfo(row, current(row)) };
  };
  const uniqueSlug = (base: string) => {
    const taken = new Set(live().map((w) => w.slug));
    if (!taken.has(base)) return base;
    for (let i = 2; ; i++) if (!taken.has(`${base}-${i}`)) return `${base}-${i}`;
  };
  const specOf = (ref: DashboardRef): Dashboard => {
    if (ref.kind === "release") return findRelease(ref.key).spec;
    if (ref.kind === "template") {
      const t = state.templates.find((candidate) => candidate.id === ref.key);
      if (!t) throw new ConnectError(`no template "${ref.key}"`, Code.NotFound);
      return t.spec;
    }
    return current(findWorkspace(ref.key)).spec;
  };

  return {
    listDashboards: (req) =>
      guarded("ListDashboards", req, () => ({
        dashboards: [
          ...state.releases.flatMap((row) => releaseSummary(row, req.envId) ?? []),
          ...live()
            .filter((row) => !req.envId || row.envIds.length === 0 || row.envIds.includes(req.envId))
            .map(workspaceSummary),
        ],
        canWrite,
      })),
    listDashboardTemplates: (orgId) => guarded("ListDashboardTemplates", { orgId }, () => state.templates.map(templateSummary)),
    getDashboard: (req) =>
      guarded("GetDashboard", req, (): DashboardDocument => {
        if (req.ref.kind === "release") {
          const row = findRelease(req.ref.key);
          return { summary: releaseSummary(row)!, spec: row.spec, version: 0, canWrite };
        }
        if (req.ref.kind === "template") {
          const t = state.templates.find((candidate) => candidate.id === req.ref.key);
          if (!t) throw new ConnectError(`no template "${req.ref.key}"`, Code.NotFound);
          return { summary: templateSummary(t), spec: t.spec, version: 0, canWrite };
        }
        const row = findWorkspace(req.ref.key);
        const v = req.version ? row.versions.find((candidate) => candidate.version === req.version) : current(row);
        if (!v) throw new ConnectError(`no version ${req.version}`, Code.NotFound);
        return { summary: workspaceSummary(row), spec: v.spec, version: v.version, canWrite };
      }),
    saveDashboard: (req) =>
      guarded("SaveDashboard", req, () => {
        mustWrite();
        if (req.spec.panels.length === 0) throw new ConnectError("panels: a dashboard needs at least one panel", Code.InvalidArgument);
        if (req.baseVersion === 0) {
          if (live().some((w) => w.slug === req.slug)) throw new ConnectError(`slug "${req.slug}" is taken`, Code.AlreadyExists);
          const row: WorkspaceRow = {
            slug: req.slug,
            envIds: req.envScope?.envIds ?? [],
            forkedFrom: "",
            createdBy: YOU,
            createdAtMs: Date.now(),
            archived: false,
            versions: [{ version: 1, spec: { ...req.spec, id: req.slug }, author: YOU, message: req.message, atMs: Date.now() }],
          };
          state.workspace.push(row);
          return { summary: workspaceSummary(row), version: versionInfo(row, current(row)) };
        }
        const row = findWorkspace(req.slug);
        if (current(row).version !== req.baseVersion) throw new ConnectError("version_conflict", Code.Aborted);
        if (req.envScope) row.envIds = req.envScope.envIds;
        return write(row, req.spec, req.message);
      }),
    saveDashboardAsCopy: (req) =>
      guarded("SaveAsCopy", req, () => {
        mustWrite();
        const spec = specOf(req.from);
        const slug = req.slug || uniqueSlug(req.from.key);
        if (live().some((w) => w.slug === slug)) throw new ConnectError(`slug "${slug}" is taken`, Code.AlreadyExists);
        const row: WorkspaceRow = {
          slug,
          envIds: req.envScope?.envIds ?? [],
          forkedFrom: `${req.from.kind}:${req.from.key}`,
          createdBy: YOU,
          createdAtMs: Date.now(),
          archived: false,
          versions: [{ version: 1, spec: { ...spec, id: slug, title: req.title || spec.title }, author: YOU, message: req.message || `Copied from ${spec.title}`, atMs: Date.now() }],
        };
        state.workspace.push(row);
        return { summary: workspaceSummary(row), version: versionInfo(row, current(row)) };
      }),
    listDashboardVersions: (orgId, ref) =>
      guarded("ListDashboardVersions", { orgId, ref }, () => {
        if (ref.kind === "release") {
          const row = findRelease(ref.key);
          return row.changes.map((change, index) => ({
            version: 0,
            releaseVersion: change.releaseVersion,
            author: change.author,
            message: "",
            createdAtMs: change.atMs,
            current: index === 0,
          }));
        }
        const row = findWorkspace(ref.key);
        return [...row.versions].reverse().map((v) => versionInfo(row, v));
      }),
    restoreDashboardVersion: (orgId, ref, version, baseVersion) =>
      guarded("RestoreDashboardVersion", { orgId, ref, version, baseVersion }, () => {
        mustWrite();
        const row = findWorkspace(ref.key);
        if (current(row).version !== baseVersion) throw new ConnectError("version_conflict", Code.Aborted);
        const old = row.versions.find((v) => v.version === version);
        if (!old) throw new ConnectError(`no version ${version}`, Code.NotFound);
        return write(row, old.spec, `Restored v${version}`);
      }),
    archiveDashboard: (orgId, ref) =>
      guarded("ArchiveDashboard", { orgId, ref }, () => {
        mustWrite();
        if (ref.kind !== "workspace") throw new ConnectError("only a workspace dashboard can be archived", Code.FailedPrecondition);
        findWorkspace(ref.key).archived = true;
      }),
    starDashboard: (orgId, ref, starred) =>
      guarded(starred ? "StarDashboard" : "UnstarDashboard", { orgId, ref }, () => {
        if (starred) state.stars.add(keyOf(ref));
        else state.stars.delete(keyOf(ref));
      }),
    recordDashboardView: (orgId, ref) =>
      guarded("RecordDashboardView", { orgId, ref }, () => {
        state.viewed.set(keyOf(ref), Date.now());
      }),
    promoteDashboardToCode: (orgId, ref, version) =>
      guarded("PromoteDashboardToCode", { orgId, ref, version }, (): PromotedDashboard => {
        mustWrite();
        if (ref.kind !== "workspace") throw new ConnectError("only a workspace dashboard is promoted", Code.FailedPrecondition);
        const row = findWorkspace(ref.key);
        const v = version ? row.versions.find((candidate) => candidate.version === version) ?? current(row) : current(row);
        const replaces = state.releases.find((r) => r.source === "generated" && r.spec.id === row.slug);
        return {
          path: `deploy/observability/dashboards/${row.slug}.json`,
          content: `${JSON.stringify({ version: "forge.dashboards/v1", ...v.spec }, null, 2)}\n`,
          prUrl: "",
          warnings: replaces ? [`${row.slug}.json replaces the dashboard forge generates with the same name (${replaces.spec.title}).`] : [],
        };
      }),
  };
}
