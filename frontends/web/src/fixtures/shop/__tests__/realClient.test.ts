// Copyright (c) 2025 Reliant Labs

/**
 * Every read the console's observability views make, asked of the REAL client
 * (controlPlaneObservabilityApi: api.ts and friends, their mappers, the read
 * scheduler) over the shop pack's transport, answers exactly what shop's
 * model answers. The handlers (../handlers/*.ts) encode the model on the
 * wire and the client decodes it, so this is the proof that each encoding is
 * the client's mapping run backwards: nothing the story says is lost or
 * changed on the way to a page.
 *
 * A read that names a value the client does not send is asked with what the
 * client does send (`limit`s it fills in), on both sides.
 */

import { createClient, type Transport } from "@connectrpc/connect";
import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";

import { RumService } from "@/gen/controlplane/services/rum/v1/rum_pb";

vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));

import { sqlSubtitlePanel, sqlTrendPanel } from "@/components/Forge/Observability/Dashboards/viewerModel";
import type { ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import type { DashboardRef } from "@/services/controlPlane/observability/dashboardTypes";
import type { Panel } from "@/services/controlPlane/observability/types";

import { SHOP_READ_ONLY_MESSAGE } from "../index";
import { DEMO_INCIDENT, DEMO_ORG, RUNNING } from "../shopStory";
import { installOverWire, uninstallOverWire } from "./overWire";

const HOUR = 3_600_000;
const WHEN = [
  ["on a weekday afternoon", Date.UTC(2026, 10, 10, 15, 0, 0)],
  ["in the middle of the night", Date.UTC(2026, 10, 10, 3, 0, 0)],
] as const;

/**
 * JSON-equal: what a page could ever tell apart. An absent key and an
 * undefined one are the same; a double is compared to 9 significant digits (a
 * Duration carries nanoseconds, not a float's tail); a series' `partial` that
 * flags no point is the same as none (api.ts sends it only when one is).
 */
const plain = (value: unknown, ignore: readonly string[] = []) =>
  JSON.parse(
    JSON.stringify(value, (k, v) => {
      if (ignore.includes(k)) return undefined;
      if (k === "partial" && Array.isArray(v) && !v.some(Boolean)) return undefined;
      if (typeof v === "bigint") return `${v}n`;
      if (typeof v === "number" && !Number.isInteger(v)) return Number(v.toPrecision(9));
      return v;
    }),
  ) as unknown;

const isEmpty = (v: unknown) => v === null || v === "" || v === 0 || v === false || (Array.isArray(v) && v.length === 0) || (typeof v === "object" && v !== null && !Array.isArray(v) && Object.keys(v).length === 0);

/**
 * `received` without the keys `expected` leaves out whose value is the
 * wire's empty default: a field the model never sets (a span's `links`)
 * decodes as `[]`, which a page shows exactly as it shows nothing.
 */
function withoutDefaults(received: unknown, expected: unknown): unknown {
  if (Array.isArray(received) && Array.isArray(expected)) return received.map((r, i) => withoutDefaults(r, expected[i]));
  if (received && expected && typeof received === "object" && typeof expected === "object" && !Array.isArray(received)) {
    const out: Record<string, unknown> = {};
    for (const [k, v] of Object.entries(received)) {
      if (!(k in (expected as object)) && isEmpty(v)) continue;
      out[k] = withoutDefaults(v, (expected as Record<string, unknown>)[k]);
    }
    return out;
  }
  return received;
}

describe.each(WHEN)("the real client over the shop pack, %s", (_when, at) => {
  let wire: ObservabilityApi;
  let model: ObservabilityApi;
  let transport: Transport;
  let now = 0;
  const scope = (envIds: string[] = []) => ({ orgId: DEMO_ORG, projectId: "shop", envIds, filters: [] });
  const hour = () => ({ startMs: now - HOUR, endMs: now });
  const day = () => ({ startMs: now - 24 * HOUR, endMs: now });

  /**
   * `read` asked of both sides, held to one answer. `ignore` names what the
   * CLIENT computes rather than reads (a log line's id is its place in the
   * page), so the model's own value for it is never on the wire.
   */
  async function same<T>(read: (api: ObservabilityApi) => Promise<T>, ignore: readonly string[] = []): Promise<T> {
    const fromModel = await read(model);
    const overWire = await read(wire);
    const expected = plain(fromModel, ignore);
    expect(withoutDefaults(plain(overWire, ignore), expected)).toEqual(expected);
    return overWire;
  }

  beforeAll(() => {
    ({ wire, model, transport, now } = installOverWire(at));
  });
  afterAll(uninstallOverWire);

  it("names the demo's org", async () => {
    expect(await wire.currentOrgId()).toBe(DEMO_ORG);
  });

  it("answers telemetry: traces, a trace, logs, the overview, profiles and signals", async () => {
    const traces = await same((api) => api.listTraces({ ...scope(["env-prod"]), window: hour(), services: [], rootSpanName: "", errorsOnly: false, minDurationMs: 0, limit: 50, groupBy: [] }));
    expect(traces.traces.length).toBeGreaterThan(0);
    await same((api) => api.getTrace({ ...scope(["env-prod"]), window: hour(), traceId: traces.traces[0].traceId }));
    // A record's id is the client's (api.ts: its place in the page).
    await same((api) => api.searchLogs({ ...scope(["env-prod"]), window: hour(), services: [], query: "", severities: [], limit: 100 }), ["id"]);
    await same((api) => api.getServiceOverview({ ...scope(["env-prod"]), window: hour() }));
    await same((api) => api.getServiceSignals({ ...scope(["env-prod"]), window: hour() }));
  });

  it("answers panels and attribute values", async () => {
    const panels: Panel[] = [
      { id: "rate", title: "Requests", kind: "line", signal: "traces", series: [{ label: "Requests", agg: "count" }], groupBy: ["service.name"] },
      { id: "p95", title: "p95", kind: "number", signal: "traces", series: [{ label: "p95", agg: "quantile", quantile: 0.95, field: "duration" }] },
      { id: "top", title: "Top routes", kind: "table", signal: "traces", series: [{ label: "Requests", agg: "count" }], groupBy: ["span.name"], limit: 5 },
    ];
    for (const panel of panels) {
      await same((api) => api.queryPanel({ ...scope(["env-prod"]), window: hour(), panel, services: [], stepSeconds: 0 }));
    }
    await same((api) => api.listAttributeValues({ ...scope(["env-prod"]), window: hour(), key: "service.name", prefix: "", limit: 50 }));
  });

  it("answers a panel over several projects (project_ids) with shop's figures, and nothing for projects without shop", async () => {
    const panel: Panel = { id: "p95", title: "p95", kind: "number", signal: "traces", series: [{ label: "p95", agg: "quantile", quantile: 0.95, field: "duration" }] };
    const read = (projectIds: string[]) => wire.queryPanel({ orgId: DEMO_ORG, projectIds, envIds: ["env-prod"], filters: [], window: hour(), panel, services: [], stepSeconds: 0 });
    const shop = await read(["docs", "shop"]);
    const alone = await wire.queryPanel({ ...scope(["env-prod"]), window: hour(), panel, services: [], stepSeconds: 0 });
    // The union of their environments: only shop has any, so it reads as shop alone does.
    expect(plain(shop.series)).toEqual(plain(alone.series));
    expect(shop.series[0]?.value ?? null).not.toBeNull();
    const none = await read(["docs", "mobile-api"]);
    expect(none.series.every((series) => series.value === null || series.value === undefined)).toBe(true);
  });

  it("answers every panel of every dashboard shop ships, as its dashboard asks", async () => {
    let answeredSql = 0;
    const index = await same((api) => api.listDashboards({ orgId: DEMO_ORG, projectId: "shop" }));
    expect(index.dashboards.length).toBeGreaterThan(0);
    for (const summary of index.dashboards) {
      const doc = await same((api) => api.getDashboard({ orgId: DEMO_ORG, ref: summary.ref }));
      await same((api) => api.listDashboardVersions(DEMO_ORG, summary.ref));
      // Each panel as the viewer reads it: the figure, its sparkline and its subtitle (viewerModel.ts),
      // and the figures an attention panel watches (DashboardView.tsx).
      // As the spec carries them: a figure of its own leaves out what a sql panel does not use (series).
      const panels = doc.spec.panels.flatMap((p) => [p, ...(p.attention?.figures ?? [])]);
      const reads = panels.flatMap((p) => [p, sqlTrendPanel(p), sqlSubtitlePanel(p)].filter((r): r is Panel => !!r));
      for (const panel of reads) {
        if (panel.kind === "postgres" || panel.kind === "text" || panel.kind === "search") continue;
        // The console names the dashboard (Forge-Panel); the pack must answer the same without it.
        const ask = (api: ObservabilityApi) => api.queryPanel({ ...scope(["env-prod"]), window: day(), panel, services: [], stepSeconds: 0, dashboardId: doc.spec.id });
        const fromModel = await ask(model);
        const overWire = await ask(wire);
        if (panel.kind === "sql" && (overWire.rows.length || overWire.series.length)) answeredSql++;
        const expected = plain({ dashboard: doc.spec.id, panel: panel.id, result: fromModel });
        expect(withoutDefaults(plain({ dashboard: doc.spec.id, panel: panel.id, result: overWire }), expected)).toEqual(expected);
      }
    }
    // The built-in analytics dashboards' sql panels are answered by their dashboard (productAnalytics.ts): not blank.
    expect(answeredSql).toBeGreaterThan(40);
    await same((api) => api.listDashboardTemplates(DEMO_ORG));
    // Every panel of every shipped dashboard, asked twice (model and wire): 3.5-5s on a CI runner,
    // so the 5s default failed main under load. Its own budget, not a smaller sweep.
  }, 30_000);

  it("answers the database panels and the databases a panel may read", async () => {
    await same((api) => api.listDashboardDatabases("env-prod"));
    await same((api) => api.queryDatabasePanel({ environmentId: "env-prod", window: day(), query: { database: "shop", sql: "select state, count(*) from orders group by 1" } }));
    // A postgres panel's bucket is its step (appDatabaseApi.ts), whatever the model says.
    await same((api) => api.queryDatabasePanel({ environmentId: "env-prod", window: day(), query: { database: "shop", sql: "select created_at, count(*) from orders group by 1", draw: "line", time: "created_at", values: ["orders"] } }), ["bucketSeconds"]);
  });

  it("answers workloads, one workload and the service graph", async () => {
    for (const env of ["env-dev", "env-staging", "env-prod"]) {
      const list = await same((api) => api.listWorkloads({ ...scope([env]), window: hour() }));
      for (const w of list.workloads) await same((api) => api.getWorkload({ ...scope([env]), window: hour(), workload: w.workload }));
    }
    await same((api) => api.getServiceGraph({ ...scope(["env-prod"]), window: hour(), rpcIds: [], includeInternal: false }));
    await same((api) => api.getServiceGraph({ ...scope(["env-prod"]), window: hour(), rpcIds: [], includeInternal: true, includeDeclared: true }));
    await same((api) => api.getServiceGraphCatalog({ ...scope(["env-prod"]), window: hour() }));
  });

  it("answers the metric catalog, one metric and its exemplars", async () => {
    // A metric's catalog group is the client's (metricsTypes.ts catalogGroup, from its name and whether it is declared).
    const catalog = await same((api) => api.listMetricCatalog({ ...scope(["env-prod"]), window: hour() }), ["group"]);
    expect(catalog.metrics.length).toBeGreaterThan(0);
    for (const m of catalog.metrics.slice(0, 6)) {
      await same((api) => api.getMetric({ ...scope(["env-prod"]), window: hour(), name: m.name, kind: m.kind }), ["group"]);
      await same((api) => api.listMetricExemplars({ ...scope(["env-prod"]), window: hour(), name: m.name, kind: m.kind }));
    }
  });

  it("answers delivery: the release ledger, DORA and release health", async () => {
    const ledger = await same((api) => api.listReleases({ ...scope(), window: { startMs: now - 30 * 24 * HOUR, endMs: now } }));
    await same((api) => api.getDoraMetrics({ ...scope(), window: { startMs: now - 30 * 24 * HOUR, endMs: now } }));
    await same((api) => api.getDoraMetrics(scope()));
    for (const env of ["env-dev", "env-staging", "env-prod"] as const) {
      const current = ledger.promotions.filter((p) => p.envId === env && p.version === RUNNING[env].version).at(-1);
      expect(current, env).toBeDefined();
      await same((api) => api.getReleaseHealth({ ...scope([env]), promotionId: current!.promotionId, windowMs: 15 * 60_000 }));
      await same((api) => api.getReleaseHealth(scope([env])));
    }
  });

  it("answers issues: the inbox, one issue, its events and timeline, the roster and the reader", async () => {
    const list = await same((api) => api.listIssues({ ...scope(), window: day(), statuses: [], sort: "last" }));
    expect(list.issues.length).toBeGreaterThan(0);
    await same((api) => api.listIssues({ ...scope(["env-prod"]), window: day(), statuses: ["new", "unresolved", "regressed", "resolved", "ignored"], sort: "events", platform: "server" }));
    for (const issue of list.issues) {
      await same((api) => api.getIssue({ issueId: issue.id }));
      await same((api) => api.listIssueEvents({ issueId: issue.id }));
      await same((api) => api.listIssueTimeline({ issueId: issue.id }));
    }
    await same((api) => api.getIssue({ issueId: DEMO_INCIDENT.issueId, window: day() }));
    await same((api) => api.listOrgMembers(DEMO_ORG));
    // The reader is refused: UserService.GetCurrentUser writes on read (control-plane#3513's audit), so it
    // declares no NO_SIDE_EFFECTS, and the demo pack answers nothing outside DEMO_PACK_ANSWERS.
    await expect(wire.getCurrentIssueUser()).rejects.toThrow(SHOP_READ_ONLY_MESSAGE);
  });

  it("answers the activity feed, an issue's history included", async () => {
    await same((api) => api.listActivity({ ...scope(), pageSize: 0 }));
    await same((api) => api.listActivity({ ...scope(["env-prod"]) }));
    await same((api) => api.listActivity({ ...scope(), targetKind: "issue", targetId: DEMO_INCIDENT.issueId }));
  });

  it("answers alerting: alerts, history, silences, receivers and notifications", async () => {
    await same((api) => api.listAlerts(DEMO_ORG, ""));
    await same((api) => api.listAlerts(DEMO_ORG, "env-prod"));
    await same((api) => api.listAlertHistory(DEMO_ORG, { limit: 200 }));
    await same((api) => api.listAlertHistory(DEMO_ORG, { envId: "env-prod", sinceMs: now - 24 * HOUR, limit: 200 }));
    await same((api) => api.listSilences(DEMO_ORG, true));
    await same((api) => api.listSilences(DEMO_ORG, false));
    await same((api) => api.listReceivers(DEMO_ORG));
    await same((api) => api.listNotifications(DEMO_ORG, { limit: 50 }));
  });

  it("answers sessions: the list, one session, its timeline and replay, and each frontend's replay switch", async () => {
    const list = await same((api) => api.listSessions({ ...scope(), window: day(), hasError: false, hasReplay: false, userId: "", pathContains: "", pageSize: 100 }));
    expect(list.sessions.length).toBeGreaterThan(0);
    for (const s of list.sessions.slice(0, 5)) {
      await same((api) => api.getSession({ ...scope(), sessionId: s.sessionId }));
      await same((api) => api.getSessionTimeline({ ...scope(), sessionId: s.sessionId }));
      if (s.hasReplay) await same((api) => api.getSessionReplay({ ...scope(), sessionId: s.sessionId }));
    }
    await same((api) => api.listFrontendReplay({ orgId: DEMO_ORG, projectId: "shop", envIds: [] }));
  });

  it("preserves the recording's event counter across replay pages and resumes", async () => {
    const list = await model.listSessions({ ...scope(), window: day(), hasReplay: true, hasError: false, userId: "", pathContains: "", pageSize: 100 });
    const sessionId = list.sessions[0].sessionId;
    const client = createClient(RumService, transport);
    let pageToken = "";
    let count = 0;
    let pages = 0;
    do {
      const request = { ...scope(), sessionId, pageToken };
      const expected = await model.getSessionReplay(request);
      const encoded = await client.getSessionReplay({ scope: scope(), sessionId, pageToken });
      expect(encoded.events.length).toBeGreaterThan(0);
      expect(encoded.events.map((e) => e.index)).toEqual(expected.events.map((e) => e.index));
      expect(encoded.events.map((e) => e.index)).toEqual(expected.events.map((_, i) => count + i));
      await same((api) => api.getSessionReplay(request));
      count += expected.events.length;
      pages++;
      if (expected.nextPageToken) {
        // A resume read and a next-page read must retain the same original counters.
        const resumed = await client.getSessionReplay({ scope: scope(), sessionId, pageToken: expected.resumeToken });
        expect(resumed.events[0].index).toBe(count);
      }
      pageToken = expected.nextPageToken;
    } while (pageToken);
    expect(pages).toBeGreaterThan(1);
  });

  it("refuses every write, as the demo does", async () => {
    const ref: DashboardRef = { kind: "workspace", projectId: "shop", key: "checkout" };
    await expect(wire.createSilence(DEMO_ORG, { matchers: [], endsAtMs: now + HOUR, comment: "" })).rejects.toThrow(/demo/i);
    await expect(wire.archiveDashboard(DEMO_ORG, ref)).rejects.toThrow(/demo/i);
    await expect(wire.markNotificationsRead(DEMO_ORG, [])).rejects.toThrow(/demo/i);
  });
});
