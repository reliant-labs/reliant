// Copyright (c) 2026 Reliant Labs

import { describe, expect, it } from "vitest";

import { sitesPanel, speedPanel } from "./speedInsightsModel";

const ENV_WHERE = "has({env_ids:Array(String)}, ResourceAttributes['reliant.env.id'])";

describe("site insights project boundary", () => {
  it("requires server-resolved environment ids on the picker and every selected-site query", () => {
    const panels = [sitesPanel("shop"), ...(["summary", "routes", "series"] as const).map((kind) => speedPanel(kind, "desktop", "p75", "7d", "shop"))];
    for (const panel of panels) {
      expect(panel.sql).toContain(`AND ${ENV_WHERE}`);
      expect(panel.sql).not.toContain("empty({env_ids:Array(String)})");
    }
  });
});
