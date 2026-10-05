// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";

import { defaultTab, resolveTab, tabParam, tabsFor } from "../envTabs";

describe("an environment page's tabs", () => {
  it("leads a LOCAL env with what is running, and gives it no release history", () => {
    // dev runs the working tree via `forge env up`; it is never built or
    // released, so "Releases" and "not built yet" are both the wrong question.
    expect(defaultTab("local")).toBe("running");
    expect(tabsFor("local").map((tab) => tab.id)).toEqual(["running", "secrets", "changes", "checks"]);
  });

  it("leads a deployed env with its overview, and has releases", () => {
    expect(defaultTab("deployed")).toBe("overview");
    expect(tabsFor("deployed").map((tab) => tab.id)).toEqual([
      "overview",
      "releases",
      "secrets",
      "changes",
      "checks",
    ]);
  });

  it("keeps the backend tabs daemon-free and marks the checkout ones", () => {
    const needs = Object.fromEntries(tabsFor("deployed").map((tab) => [tab.id, tab.needsDaemon]));
    expect(needs).toEqual({ overview: false, releases: false, secrets: false, changes: true, checks: true });
  });

  it("maps the retired two-tab links somewhere sensible", () => {
    expect(resolveTab("live", "deployed")).toBe("overview");
    expect(resolveTab("live", "local")).toBe("running");
    expect(resolveTab("preview", "deployed")).toBe("changes");
  });

  it("falls back to the default for a tab this env does not have", () => {
    expect(resolveTab("running", "deployed")).toBe("overview");
    expect(resolveTab("releases", "local")).toBe("running");
    expect(resolveTab("nonsense", "deployed")).toBe("overview");
  });

  it("keeps the default tab out of the URL", () => {
    expect(tabParam("overview", "deployed")).toBeUndefined();
    expect(tabParam("running", "local")).toBeUndefined();
    expect(tabParam("releases", "deployed")).toBe("releases");
  });
});
