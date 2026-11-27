// Copyright (c) 2026 Reliant Labs

import { describe, expect, it } from "vitest";

import { sitesPanel, speedPanel } from "./speedInsightsModel";

const panels = (project?: string) => [sitesPanel(project), ...(["summary", "routes", "series"] as const).map((kind) => speedPanel(kind, "desktop", "p75", "7d", project))];

describe("Site insights project scoping", () => {
  it("constrains every SQL panel independently of the optional site and environment filters", () => {
    for (const panel of panels("shop")) {
      expect(panel.sql).toContain("AND ResourceAttributes['service.namespace'] = 'shop'");
      expect(panel.sql).toContain("{services:Array(String)}");
      expect(panel.sql).toContain("{env_ids:Array(String)}");
    }
  });

  it("fails closed without a project", () => {
    for (const panel of panels()) expect(panel.sql).toContain("\n  AND 0\n");
  });

  it("escapes project names as ClickHouse string literals", () => {
    for (const panel of panels("shop\\' OR 1=1 --")) {
      expect(panel.sql).toContain("AND ResourceAttributes['service.namespace'] = 'shop\\\\\\' OR 1=1 --'");
    }
  });
});
