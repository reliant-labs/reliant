/**
 * Site insights › Speed over the shop pack's transport: the SQL panels answer
 * shop-web's routes, the device and percentile toggles read out of the SQL,
 * the site filter (scope.services) is honoured, and every other QueryPanel
 * still reaches the pack's own handler.
 */
import { createClient } from "@connectrpc/connect";
import { beforeEach, describe, expect, it } from "vitest";

import { ObservabilityService } from "@/gen/controlplane/services/observability/v1/observability_pb";

import { createShopTransport } from "../index";
import { resetDemoNowForTests } from "../shopStory";

const FIXED_NOW = Date.UTC(2026, 10, 18, 15, 30);
const client = () => createClient(ObservabilityService, createShopTransport({ now: FIXED_NOW }));
const scope = { orgId: "org-acme", projectId: "shop", envIds: ["env-prod"] };
const panel = (id: string, sql = "SELECT quantileIf(0.75)(...) FROM webvitals", services: string[] = []) => ({ scope: { ...scope, services }, panel: { id, kind: "sql", sql } });

beforeEach(() => resetDemoNowForTests());

describe("the shop pack's Site insights › Speed", () => {
  it("answers each route of shop-web, mobile slower than desktop", async () => {
    const desktop = await client().queryPanel(panel("speed-insights-routes"));
    expect(desktop.rows.map((r) => r.group.page)).toEqual(["/", "/products", "/products/view", "/cart", "/checkout", "/search"]);
    const mobile = await client().queryPanel(panel("speed-insights-routes", "SELECT quantileIf(0.75)(...) WHERE device = 'mobile'"));
    const lcp = (res: typeof desktop, page: string) => res.rows.find((r) => r.group.page === page)!.values[0];
    expect(lcp(mobile, "/checkout")).toBeGreaterThan(lcp(desktop, "/checkout"));
  });

  it("answers the summary and a time series", async () => {
    expect((await client().queryPanel(panel("speed-insights-summary"))).rows).toHaveLength(1);
    const series = await client().queryPanel(panel("speed-insights-series", "SELECT toStartOfInterval(t, INTERVAL 86400 SECOND) quantileIf(0.9)"));
    expect(series.columns[0]).toBe("t");
    expect(series.rows.length).toBeGreaterThanOrEqual(7);
  });

  it("lists the storefront as the one site", async () => {
    const sites = await client().queryPanel(panel("speed-insights-sites"));
    expect(sites.rows.map((r) => r.group.service)).toEqual(["shop-web"]);
    expect(sites.rows[0]!.values[0]).toBeGreaterThan(0);
  });

  it("honours the site filter: the storefront measured, any other site nothing", async () => {
    const mine = await client().queryPanel(panel("speed-insights-routes", undefined, ["shop-web"]));
    expect(mine.rows).toHaveLength(6);
    const other = await client().queryPanel(panel("speed-insights-routes", undefined, ["shop-admin"]));
    expect(other.rows).toHaveLength(0);
    const summary = await client().queryPanel(panel("speed-insights-summary", undefined, ["shop-admin"]));
    expect(summary.rows).toHaveLength(1);
    const views = summary.columns.indexOf("views");
    expect(summary.rows[0]!.values[views]).toBe(0);
    expect((await client().queryPanel(panel("speed-insights-sites", undefined, ["shop-admin"]))).rows).toHaveLength(0);
  });

  it("leaves every other panel to the pack", async () => {
    const other = await client().queryPanel({ scope, panel: { id: "requests", kind: "line", signal: "traces", series: [{ label: "requests", agg: "count" }] } });
    expect(other.kind).not.toBe("");
  });
});
