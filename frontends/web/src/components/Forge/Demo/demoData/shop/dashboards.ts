// Copyright (c) 2025 Reliant Labs

/**
 * shop's dashboards: the ones its release bundle carries (forge's generated
 * overview, OrdersService, workloads and releases dashboards, and the two
 * the repo declares: the checkout funnel and the storefront), plus two the
 * team built in the console. Every author is the cast or CI. The `shop`
 * database answers Postgres panels.
 */

import { Code, ConnectError } from "@connectrpc/connect";

import type { DashboardDocument, DashboardRef, DashboardSummary, DashboardUser } from "@/services/controlPlane/observability/dashboardTypes";
import type { ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import type { Dashboard, Panel } from "@/services/controlPlane/observability/types";

import { DAY, DEMO_DB_TABLES, demoPerson, HOUR, MINUTE, RUNNING } from "../shopStory";
import forgeDashboardsRaw from "./forge-dashboards.json?raw";
import { shopDatabasePanel } from "./panels";
import { demoWriteRefused, type DashboardKeys } from "./shopApi";

const PROJECT = "shop";
const PROD = "env-prod";
const STAGING = "env-staging";

const user = (handle: string): DashboardUser => ({ id: demoPerson(handle)?.id ?? handle, name: handle, email: `${handle}@example.com` });
const CI: DashboardUser = { id: "token-ci", name: "github-actions", email: "" };

const RPC_SERVER = [{ field: "span.kind", op: "eq", value: "server" }];
const CREATE_ORDER = [...RPC_SERVER, { field: "span.name", op: "eq", value: "orders.v1.OrdersService/CreateOrder" }];

function repoDashboards(): Dashboard[] {
  const checkout: Dashboard = {
    id: "checkout",
    title: "Checkout funnel",
    description: "The project's own dashboard: deploy/observability/dashboards/checkout.json.",
    filters: [{ id: "service", label: "service.name", field: "service.name" }],
    annotations: [{ source: "releases", label: "Releases" }],
    panels: [
      { id: "orders", title: "Orders created", kind: "line", signal: "traces", where: CREATE_ORDER, series: [{ label: "orders", agg: "count" }], unit: "count", width: 12, height: 7 },
      { id: "checkout-p95", title: "CreateOrder p95", kind: "number", signal: "traces", where: CREATE_ORDER, series: [{ label: "p95", agg: "quantile", quantile: 0.95, field: "span.duration" }], unit: "ms", width: 12, height: 4 },
      { id: "funnel", title: "Checkout funnel", description: "Product events in order, within 1h, counted per session.", kind: "funnel", signal: "traces", series: [], funnel: { steps: ["view_product", "add_to_cart", "begin_checkout", "purchase"], window: "1h", by: "session" }, width: 24, height: 8 },
    ],
  };
  const frontend: Dashboard = {
    id: "frontend-web",
    title: "Storefront",
    description: "shop-web as its visitors see it: sessions, and the requests that failed them.",
    filters: [{ id: "service", label: "service.name", field: "service.name" }],
    annotations: [{ source: "releases", label: "Releases" }],
    panels: [
      { id: "sessions", title: "Sessions", kind: "sessions", signal: "traces", series: [], width: 24, height: 8 },
      { id: "sessions-with-errors", title: "Sessions with errors", description: "Sessions that hit an error, newest first.", kind: "sessions", signal: "traces", series: [], hasError: true, width: 24, height: 8 },
    ],
  };
  return [checkout, frontend];
}

function paymentsDashboard(): Dashboard {
  const charge = [{ field: "span.name", op: "eq", value: "payments.Charge" }];
  return {
    id: "payments-provider",
    title: "Payment provider",
    description: "The charge to the payment provider: how often it fails, and how long it takes.",
    filters: [{ id: "env", label: "deployment.environment.name", field: "deployment.environment.name" }],
    annotations: [{ source: "releases", label: "Releases" }],
    panels: [
      { id: "charges", title: "Charges", kind: "number", signal: "traces", where: charge, series: [{ label: "charges", agg: "count" }], unit: "count", width: 8, height: 4 },
      { id: "failed", title: "Failed charges", kind: "number", signal: "traces", where: [...charge, { field: "span.status", op: "eq", value: "error" }], series: [{ label: "failed", agg: "count" }], unit: "count", width: 8, height: 4 },
      { id: "p95", title: "Charge p95", kind: "number", signal: "traces", where: charge, series: [{ label: "p95", agg: "quantile", quantile: 0.95, field: "span.duration" }], unit: "ms", width: 8, height: 4 },
      { id: "latency", title: "Charge latency", kind: "line", signal: "traces", where: charge, series: [{ label: "p50", agg: "quantile", quantile: 0.5, field: "span.duration" }, { label: "p95", agg: "quantile", quantile: 0.95, field: "span.duration" }], unit: "ms", width: 24, height: 7 },
    ],
  };
}

function ordersDbDashboard(): Dashboard {
  return {
    id: "orders-db",
    title: "Orders by state",
    description: "Live from the shop database (read replica).",
    filters: [],
    annotations: [],
    panels: [
      { id: "by-state", title: "Orders by state", kind: "postgres", signal: "", series: [], postgres: { database: "shop", sql: "SELECT state, count(*) AS orders FROM orders WHERE created_at >= $1 AND created_at < $2 GROUP BY state ORDER BY orders DESC", draw: "table", group: ["state"] }, width: 12, height: 7 },
      { id: "per-hour", title: "Orders per hour", kind: "postgres", signal: "", series: [], postgres: { database: "shop", sql: "SELECT date_trunc('hour', created_at) AS t, count(*) AS orders FROM orders WHERE created_at >= $1 AND created_at < $2 GROUP BY t ORDER BY t", draw: "line", time: "t", values: ["orders"] }, width: 12, height: 7 },
    ],
  };
}

const GROUPING: Record<string, { group: string; tags?: string[] }> = {
  "release/overview": { group: "overview" },
  "release/service-orders": { group: "services", tags: ["rpc"] },
  "release/workloads": { group: "platform", tags: ["kubernetes"] },
  "release/releases": { group: "releases" },
  "release/checkout": { group: "product", tags: ["checkout", "funnel"] },
  "release/frontend-web": { group: "product", tags: ["web"] },
  "workspace/payments-provider": { group: "services", tags: ["payments"] },
  "workspace/orders-db": { group: "product", tags: ["database"] },
};

const previewsOf = (spec: Dashboard): Panel[] => [...spec.panels.filter((p) => p.kind === "number"), ...spec.panels.filter((p) => p.kind === "line")].slice(0, 3);
const keyOf = (ref: Pick<DashboardRef, "kind" | "key">) => `${ref.kind}/${ref.key}`;

interface Row {
  ref: DashboardRef;
  spec: Dashboard;
  source: DashboardSummary["source"];
  release: DashboardSummary["release"];
  workspace: DashboardSummary["workspace"];
  /** Release dashboards: the releases their content changed in. Workspace: their versions. */
  history: { version: number; releaseVersion: string; author: DashboardUser; message: string; atMs: number }[];
}

function rowsOf(now: number): Row[] {
  const generated = (JSON.parse(forgeDashboardsRaw) as { dashboards: (Dashboard & { annotations?: { source: string; label?: string }[] })[] }).dashboards.map((d) => ({ ...d, filters: d.filters ?? [], annotations: d.annotations ?? [] }));
  const shipped = (spec: Dashboard, source: "generated" | "repo", version: string, filePath: string): Row => {
    const promoted = now - RUNNING["env-staging"].age;
    return {
      ref: { kind: "release", projectId: PROJECT, key: spec.id },
      spec,
      source,
      release: { releaseVersion: version, shippedAtMs: promoted, filePath, envIds: [PROD, STAGING] },
      workspace: null,
      history: [
        { version: 0, releaseVersion: version, author: source === "generated" ? CI : user("priya"), message: "", atMs: promoted },
        { version: 0, releaseVersion: "v1.8.1", author: CI, message: "", atMs: now - 9 * DAY },
      ],
    };
  };
  const workspace = (spec: Dashboard, author: string, versions: [string, number][], envIds: string[]): Row => {
    const history = versions.map(([message, ago], i) => ({ version: i + 1, releaseVersion: "", author: user(author), message, atMs: now - ago }));
    const head = history[history.length - 1];
    return {
      ref: { kind: "workspace", projectId: PROJECT, key: spec.id },
      spec,
      source: "workspace",
      release: null,
      workspace: { version: head.version, createdBy: user(author), updatedBy: head.author, createdAtMs: history[0].atMs, updatedAtMs: head.atMs, forkedFrom: "", envIds },
      history,
    };
  };
  return [
    ...generated.map((spec) => shipped(spec, "generated", "v1.8.3", "")),
    ...repoDashboards().map((spec) => shipped(spec, "repo", "v1.8.2", `deploy/observability/dashboards/${spec.id}.json`)),
    workspace(paymentsDashboard(), "jon", [["Created", 12 * DAY], ["Charge latency", 30 * MINUTE]], [PROD]),
    workspace(ordersDbDashboard(), "priya", [["Created", 4 * DAY], ["Orders per hour", 2 * DAY + 3 * HOUR]], []),
  ];
}

export function shopDashboards(now: number): Pick<ObservabilityApi, DashboardKeys> {
  const rows = rowsOf(now);
  const stars = new Set(["release/overview", "workspace/payments-provider"]);
  const viewed = new Map<string, number>([
    ["workspace/payments-provider", now - 22 * MINUTE],
    ["release/service-orders", now - 36 * MINUTE],
    ["release/overview", now - 3 * HOUR],
  ]);
  const summaryOf = (row: Row): DashboardSummary => ({
    ref: row.ref,
    source: row.source,
    title: row.spec.title,
    description: row.spec.description ?? "",
    readOnly: row.source !== "workspace",
    starred: stars.has(keyOf(row.ref)),
    lastViewedAtMs: viewed.get(keyOf(row.ref)) ?? null,
    panelCount: row.spec.panels.length,
    previewPanels: previewsOf(row.spec),
    release: row.release,
    workspace: row.workspace,
    category: "",
    ...(GROUPING[keyOf(row.ref)] ?? {}),
  });
  const find = (ref: DashboardRef) => {
    const row = rows.find((r) => r.ref.kind === ref.kind && r.ref.key === ref.key);
    if (!row) throw new ConnectError(`no ${ref.kind} dashboard "${ref.key}"`, Code.NotFound);
    return row;
  };
  return {
    listDashboards: async (req) => ({
      dashboards: rows.filter((r) => !req.envId || (r.release?.envIds ?? r.workspace?.envIds ?? []).length === 0 || (r.release?.envIds ?? r.workspace?.envIds ?? []).includes(req.envId)).map(summaryOf),
      canWrite: false,
    }),
    listDashboardTemplates: async () => [],
    getDashboard: async (req): Promise<DashboardDocument> => {
      const row = find(req.ref);
      return { summary: summaryOf(row), spec: row.spec, version: row.workspace?.version ?? 0, canWrite: false };
    },
    listDashboardVersions: async (_orgId, ref) => {
      const row = find(ref);
      const list = [...row.history].reverse();
      return list.map((h, i) => ({ version: h.version, releaseVersion: h.releaseVersion, author: h.author, message: h.message, createdAtMs: h.atMs, current: i === 0 }));
    },
    listDashboardDatabases: async () => [
      { name: "shop", rowCap: 1000, timeoutMs: 5000, replica: "ro", tablesError: "", tables: DEMO_DB_TABLES.map((t) => ({ schema: "public", name: t.name, columns: t.columns.map(([name, type]) => ({ name, type })) })) },
    ],
    queryDatabasePanel: async (req) => shopDatabasePanel(req, now),
    saveDashboard: demoWriteRefused,
    saveDashboardAsCopy: demoWriteRefused,
    restoreDashboardVersion: demoWriteRefused,
    archiveDashboard: demoWriteRefused,
    starDashboard: demoWriteRefused,
    recordDashboardView: async () => undefined,
    promoteDashboardToCode: demoWriteRefused,
  };
}
