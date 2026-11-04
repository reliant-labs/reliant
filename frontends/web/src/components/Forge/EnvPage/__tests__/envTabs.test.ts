// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";

import { defaultTab, resolveTab, retiredTabOf, RETIRED_TABS, tabNeedsDaemon, tabParam, tabsFor } from "../envTabs";

describe("an environment page's tabs", () => {
  it("leads a LOCAL env with what is running", () => {
    expect(defaultTab("local")).toBe("running");
    expect(tabsFor("local").map((tab) => tab.id)).toEqual(["running", "identity"]);
  });

  it("is the environment as an object: overview and identity, nothing the project pages already have", () => {
    expect(defaultTab("deployed")).toBe("overview");
    expect(tabsFor("deployed").map((tab) => tab.id)).toEqual(["overview", "identity"]);
  });

  it("keeps every deployed-env tab daemon-free", () => {
    expect(tabsFor("deployed").every((tab) => !tab.needsDaemon)).toBe(true);
    expect(tabNeedsDaemon("running")).toBe(true);
  });

  it("maps the retired two-tab links somewhere sensible", () => {
    expect(resolveTab("live", "deployed")).toBe("overview");
    expect(resolveTab("live", "local")).toBe("running");
    for (const retired of ["preview", "changes", "checks", "observability"]) {
      expect(resolveTab(retired, "deployed")).toBe("overview");
      expect(resolveTab(retired, "local")).toBe("running");
    }
  });

  it("falls back to the default for a tab this env does not have", () => {
    expect(resolveTab("running", "deployed")).toBe("overview");
    expect(resolveTab("nonsense", "deployed")).toBe("overview");
  });

  it("keeps the default tab out of the URL", () => {
    expect(tabParam("overview", "deployed")).toBeUndefined();
    expect(tabParam("running", "local")).toBeUndefined();
    expect(tabParam("identity", "deployed")).toBe("identity");
  });
});

describe("the tabs that became project pages", () => {
  it("name the page each one became, so an old link lands there scoped", () => {
    expect(RETIRED_TABS).toEqual({ releases: "releases", activity: "activity", secrets: "secrets", domains: "domains" });
    expect(retiredTabOf("secrets")).toBe("secrets");
    expect(retiredTabOf("identity")).toBeNull();
    expect(retiredTabOf(undefined)).toBeNull();
    expect(retiredTabOf("constructor")).toBeNull();
  });
});
