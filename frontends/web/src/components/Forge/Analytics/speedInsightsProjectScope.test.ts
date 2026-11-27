// Copyright (c) 2026 Reliant Labs

import { describe, expect, it } from "vitest";

import { siteServiceNames, sitesPanel, speedPanel } from "./speedInsightsModel";

const ENV_WHERE = "has({env_ids:Array(String)}, ResourceAttributes['reliant.env.id'])";

describe("site insights project boundary", () => {
  it("requires server-resolved environment ids on the picker and every selected-site query", () => {
    const panels = [sitesPanel("shop"), ...(["summary", "routes", "series"] as const).map((kind) => speedPanel(kind, "desktop", "p75", "7d", "shop"))];
    for (const panel of panels) {
      expect(panel.sql).toContain(`AND ${ENV_WHERE}`);
      expect(panel.sql).not.toContain("empty({env_ids:Array(String)})");
    }
  });

  it("does not mix identical service and namespace names from different projects, or widen an empty project", () => {
    // Both producers report the same namespace and bare frontend name. Only
    // the gateway's environment stamp distinguishes their project ownership.
    const spans = [
      { namespace: "shop", service: "web", envId: "project-a-prod", views: 3 },
      { namespace: "shop", service: "web", envId: "project-b-prod", views: 99 },
    ];
    const services = siteServiceNames("shop", "web");
    // Model the mandatory has(env_ids, reliant.env.id) predicate above using
    // QueryScope.project_id's server-resolved ids, not a producer's name.
    const read = (envIds: string[], selected: string[]) =>
      spans.filter((span) => envIds.includes(span.envId) && span.namespace === "shop" && (selected.length === 0 || selected.includes(span.service)));
    expect(read(["project-a-prod"], [])).toEqual([spans[0]]);
    expect(read(["project-a-prod"], services)).toEqual([spans[0]]);
    expect(read([], [])).toEqual([]);
    expect(read([], services)).toEqual([]);
  });
});
