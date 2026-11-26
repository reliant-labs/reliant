// Copyright (c) 2026 Reliant Labs

import { describe, expect, it } from "vitest";

import { learnSectionOf } from "@/components/Forge/Console/consoleLearn";

import { LEARN_SLUGS, learnUrl } from "../learnLinks";

describe("learnLinks", () => {
  it("opens each section's page on the docs site", () => {
    expect(learnUrl("sandboxes")).toBe("https://docs.reliantlabs.io/machines/overview");
    expect(learnUrl("migrate")).toBe("https://docs.reliantlabs.io/forge/migrate-an-existing-app");
    for (const slug of Object.values(LEARN_SLUGS)) expect(slug).toMatch(/^[a-z0-9-]+(\/[a-z0-9-]+)*$/);
  });

  it("names a guide for the console's pages", () => {
    expect(learnSectionOf({ kind: "services" })).toBe("services");
    expect(learnSectionOf({ kind: "obs-view", view: "logs" })).toBe("observability");
    expect(learnSectionOf({ kind: "obs-view", view: "dashboards" })).toBe("dashboards");
    expect(learnSectionOf({ kind: "site-insights" })).toBe("site-insights");
    expect(learnSectionOf({ kind: "site-insights", view: "accessibility" })).toBe("site-insights");
  });

  it("keeps the old Speed insights section, on the guide it moved to", () => {
    expect(learnUrl("speed-insights")).toBe(learnUrl("site-insights"));
    expect(learnUrl("site-insights")).toBe("https://docs.reliantlabs.io/forge/observability/site-insights");
    expect(learnSectionOf({ kind: "users" })).toBe("auth");
  });
});
