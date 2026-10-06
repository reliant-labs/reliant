// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";

import { defaultTab, resolveTab, tabNeedsDaemon, tabParam, tabsFor } from "../envTabs";

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
      "activity",
      "secrets",
      "domains",
      "changes",
      "checks",
    ]);
  });

  it("keeps the backend tabs daemon-free and marks the checkout ones", () => {
    const needs = Object.fromEntries(tabsFor("deployed").map((tab) => [tab.id, tab.needsDaemon]));
    expect(needs).toEqual({
      overview: false,
      releases: false,
      activity: false,
      secrets: false,
      domains: false,
      changes: true,
      checks: true,
    });
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

describe("releases, activity and domains are their own tabs", () => {
  it("separates release history from convergence activity, and adds domains, for a deployed env", () => {
    const ids = tabsFor("deployed").map((tab) => tab.id);
    expect(ids.indexOf("activity")).toBe(ids.indexOf("releases") + 1);
    expect(ids).toContain("domains");
  });

  it("keeps them off a local env, which has no releases and binds no domains", () => {
    const ids = tabsFor("local").map((tab) => tab.id);
    expect(ids).not.toContain("activity");
    expect(ids).not.toContain("domains");
  });

  it("resolves their URL values and marks them daemon-free", () => {
    expect(resolveTab("activity", "deployed")).toBe("activity");
    expect(resolveTab("domains", "deployed")).toBe("domains");
    expect(resolveTab("activity", "local")).toBe("running");
    expect(tabNeedsDaemon("activity")).toBe(false);
    expect(tabNeedsDaemon("domains")).toBe(false);
    expect(tabNeedsDaemon("changes")).toBe(true);
    expect(tabNeedsDaemon("preview")).toBe(true);
    expect(tabNeedsDaemon(undefined)).toBe(false);
  });
});
