// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";

import { obsViewOfOrgPage, orgPageOfObsView, sameOrgPage } from "../orgObsPages";

describe("orgPageOfObsView", () => {
  it("sends the overview to the Observability root", () => {
    expect(orgPageOfObsView(undefined)).toEqual({ kind: "observability" });
    expect(orgPageOfObsView("overview")).toEqual({ kind: "observability" });
  });

  it("sends every other view to its own page", () => {
    for (const view of ["errors", "logs", "traces", "profiles", "alerts"] as const) {
      expect(orgPageOfObsView(view)).toEqual({ kind: "obs-view", view });
    }
  });
});

describe("obsViewOfOrgPage", () => {
  it("hosts the panel on the overview and on the telemetry views", () => {
    expect(obsViewOfOrgPage({ kind: "observability" })).toBe("overview");
    expect(obsViewOfOrgPage({ kind: "obs-view", view: "traces" })).toBe("traces");
    expect(obsViewOfOrgPage({ kind: "obs-view", view: "alerts" })).toBe("alerts");
  });

  it("does not host it on Metrics or Dashboards", () => {
    expect(obsViewOfOrgPage({ kind: "obs-view", view: "metrics" })).toBeNull();
    expect(obsViewOfOrgPage({ kind: "obs-view", view: "dashboards" })).toBeNull();
  });
});

describe("sameOrgPage", () => {
  it("compares pages by where they live", () => {
    expect(sameOrgPage({ kind: "obs-view", view: "logs" }, { kind: "obs-view", view: "logs" })).toBe(true);
    expect(sameOrgPage({ kind: "obs-view", view: "logs" }, { kind: "obs-view", view: "traces" })).toBe(false);
    expect(sameOrgPage({ kind: "observability" }, { kind: "obs-view", view: "logs" })).toBe(false);
  });
});
