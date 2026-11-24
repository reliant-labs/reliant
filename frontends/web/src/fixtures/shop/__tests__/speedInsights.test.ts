/**
 * Speed insights over the shop pack's transport: the three SQL panels answer
 * shop-web's routes, the device and percentile toggles read out of the SQL,
 * and every other QueryPanel still reaches the pack's own handler.
 */
import { createClient } from "@connectrpc/connect";
import { beforeEach, describe, expect, it } from "vitest";

import { ObservabilityService } from "@/gen/controlplane/services/observability/v1/observability_pb";

import { createShopTransport } from "../index";
import { resetDemoNowForTests } from "../shopStory";

const FIXED_NOW = Date.UTC(2026, 10, 18, 15, 30);
const client = () => createClient(ObservabilityService, createShopTransport({ now: FIXED_NOW }));
const scope = { orgId: "org-acme", projectId: "shop", envIds: ["env-prod"] };
const panel = (id: string, sql = "SELECT quantileIf(0.75)(...) FROM webvitals") => ({ scope, panel: { id, kind: "sql", sql } });

beforeEach(() => resetDemoNowForTests());

describe("the shop pack's Speed insights", () => {
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

  it("leaves every other panel to the pack", async () => {
    const other = await client().queryPanel({ scope, panel: { id: "requests", kind: "line", signal: "traces", series: [{ label: "requests", agg: "count" }] } });
    expect(other.kind).not.toBe("");
  });
});
