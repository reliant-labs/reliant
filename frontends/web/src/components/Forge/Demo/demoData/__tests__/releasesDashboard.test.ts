// Copyright (c) 2025 Reliant Labs

/**
 * The Releases dashboard in the demo tells the story every other page tells
 * (shopStory.ts): it is forge's generated spec, its delivery panels read the
 * same ledger the Releases page reads, and its sql panels answer from the
 * ledger and the traffic model, so prod's v1.8.3 is the one release that
 * regressed, v1.8.2 before it held, and staging's rollback is in the history.
 */

import { describe, expect, it } from "vitest";

import type { Panel, PanelResult, QueryPanelRequest } from "@/services/controlPlane/observability/types";

import forgeDashboardsRaw from "../shop/forge-dashboards.json?raw";
import { shopDora, shopPromotions } from "../shop/delivery";
import { shopReleaseHealth } from "../shop/issues";
import { releasesWindow, shopReleasesSqlPanel } from "../shop/releases";
import { DEMO_INCIDENT } from "../shopStory";

// A fixed afternoon (UTC), well after every promotion in the ledger.
const NOW = Date.UTC(2026, 10, 11, 15, 0, 0);

const spec = (JSON.parse(forgeDashboardsRaw) as { dashboards: { id: string; defaultRange?: string; panels: Panel[] }[] }).dashboards.find((d) => d.id === "releases")!;
const panel = (id: string) => spec.panels.find((p) => p.id === id)!;
const BASE = { series: [], columns: [], rows: [], stepSeconds: 0, truncated: false, funnelSteps: [], issues: [], sessions: [], seriesMeta: [], seriesTotal: 0, warnings: [], compiledSql: [] };

function answer(id: string, envIds: string[] = []): PanelResult {
  const p = panel(id);
  const req = { orgId: "org-acme", projectId: "shop", envIds, filters: [], window: releasesWindow(NOW), panel: p, services: [], stepSeconds: 0 } as QueryPanelRequest;
  const out = shopReleasesSqlPanel(p, req, NOW, { kind: p.kind, unit: p.unit ?? "", ...BASE, window: req.window } as PanelResult);
  if (!out) throw new Error(`no answer for ${id}`);
  return out;
}

describe("the demo's Releases dashboard", () => {
  it("is forge's generated spec: a week by default, the verdict, delivery and adoption first", () => {
    expect(spec.defaultRange).toBe("7d");
    expect(spec.panels.slice(0, 3).map((p) => p.kind)).toEqual(["release_health", "delivery", "promotions"]);
    expect(JSON.stringify(spec)).not.toContain('"title":"resource.attr');
  });

  it("judges prod's running v1.8.3 regressed, and staging's good", () => {
    const prod = shopReleaseHealth(NOW, "env-prod", "", 15 * 60_000);
    expect(prod.promotion?.version).toBe(DEMO_INCIDENT.release);
    expect(prod.promotion?.previousVersion).toBe("v1.8.2");
    expect(prod.verdict).toBe("fail");
    expect(shopReleaseHealth(NOW, "env-staging", "", 15 * 60_000).verdict).toBe("pass");
  });

  it("puts staging's rollback in the promotion timeline and in DORA", () => {
    const rollbacks = shopPromotions(NOW).filter((p) => p.kind === "rollback");
    expect(rollbacks.map((p) => `${p.envName} ${p.version}`)).toEqual(["staging v1.8.1"]);
    const dora = shopDora(NOW, [], releasesWindow(NOW), 24);
    expect(dora.changeFailureRate.failedChanges).toBe(1);
    expect(dora.timeToRestore.medianMs).toBeGreaterThan(0);
  });

  it("shows v1.8.3 failing in prod and v1.8.2 clean, side by side", () => {
    const rows = answer("version-comparison", ["env-prod"]).rows;
    const errorRate = (v: string) => rows.find((r) => r.group.Version === v)!.values[1];
    expect(rows[0].group.Version).toBe("v1.8.3");
    expect(errorRate("v1.8.3")).toBeGreaterThan(0);
    expect(errorRate("v1.8.2")).toBe(0);
  });

  it("counts crash-free sessions per version: only v1.8.3's checkouts failed", () => {
    const rows = answer("crash-free-sessions-by-version", ["env-prod"]).rows;
    for (const r of rows) {
      const [sessions, errored, crashFree] = r.values as number[];
      expect(crashFree).toBeCloseTo((100 * (sessions - errored)) / sessions, 1);
      expect(errored > 0).toBe(r.group.Version === DEMO_INCIDENT.release);
    }
  });

  it("attributes the incident's issue to v1.8.3", () => {
    const rows = answer("new-issues-by-release", ["env-prod"]).rows;
    expect(rows.find((r) => r.group.Version === "v1.8.3")?.values[0]).toBe(1);
  });

  it("splits every bucket's traffic across versions as fractions that sum to one", () => {
    for (const id of ["backend-version-share", "browser-version-share"]) {
      const { series } = answer(id, ["env-prod"]);
      expect(series.map((s) => s.group.Version).sort()).toEqual(["v1.8.1", "v1.8.2", "v1.8.3"]);
      series[0].timestamps.forEach((_, i) => {
        const total = series.reduce((a, s) => a + (s.values[i] ?? 0), 0);
        if (total) expect(total).toBeCloseTo(1, 3);
      });
    }
  });
});
