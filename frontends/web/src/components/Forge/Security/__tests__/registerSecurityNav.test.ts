// Copyright (c) 2026 Reliant Labs

/**
 * Security ▾ (U6 follow-up): the Security pages' rows join the shell nav's
 * Security group, Vulnerabilities then SBOMs, linking to #3085's paths and lit
 * on them; without them the group is not drawn. forgeConsoleRoutes registers
 * them at load.
 */

import { afterEach, describe, expect, it } from "vitest";

import { activeArea, childrenOf, flattenIds, hrefOf, hybridNav, isCurrent, visibleItems, type NavCtx } from "../../../Layout/sidebar/navConfig";
import { consoleNavEntries } from "../../Console/consoleNavRegistry";
import { buildGroupedConsoleNav } from "../../Console/groupedNav";
import { consolePageOf, orgPageOf } from "../../Console/paths";
import { registerSecurityNav } from "../registerSecurityNav";

const VULNS = "/forge/p/shopfront/security/vulnerabilities";
const ADVISORY = "/forge/p/shopfront/security/vulnerabilities/GHSA-xxxx-yyyy-zzzz";
const SBOMS = "/forge/p/shopfront/security/sboms";

function ctxAt(pathname: string): NavCtx {
  const page = consolePageOf(pathname)?.page ?? null;
  return {
    pathname,
    search: {},
    mode: "one",
    projectId: "p1",
    projectName: "shopfront",
    app: "shopfront",
    console: { project: "shopfront", page: page ?? { kind: "overview" }, env: "prod", envs: [] },
    page,
    orgPage: orgPageOf(pathname),
  };
}

const security = (ctx: NavCtx) => visibleItems(hybridNav.items, ctx).find((item) => item.id === "security");
const rows = (ctx: NavCtx) => childrenOf(security(ctx)!, ctx);

let unregister: (() => void) | undefined;
afterEach(() => {
  unregister?.();
  unregister = undefined;
});

describe("Security ▾", () => {
  it("is not drawn until the Security rows register", () => {
    const ctx = ctxAt("/forge/p/shopfront/secrets");
    expect(flattenIds(hybridNav.items, ctx)).not.toContain("security");
  });

  it("lists Vulnerabilities then SBOMs, before Configure, linking to the Security pages", () => {
    unregister = registerSecurityNav();
    const ctx = ctxAt("/forge/p/shopfront/secrets");
    const top = visibleItems(hybridNav.items, ctx).map((item) => item.id);
    expect(top.indexOf("security")).toBe(top.indexOf("configure") - 1);
    expect(rows(ctx).map((row) => [row.id, row.kind === "row" ? row.label : null, hrefOf(row, ctx)])).toEqual([
      ["registered-vulnerabilities", "Vulnerabilities", `${VULNS}?env=prod`],
      ["registered-sboms", "SBOMs", `${SBOMS}?env=prod`],
    ]);
    // A Security row joins Security alone, not Analytics.
    const analytics = hybridNav.items.find((item) => item.id === "analytics")!;
    expect(childrenOf(analytics, ctx).map((item) => item.id)).not.toContain("registered-vulnerabilities");
  });

  it("lights Vulnerabilities on the list and on one advisory, and SBOMs on its page", () => {
    unregister = registerSecurityNav();
    const lit = (pathname: string) => {
      const ctx = ctxAt(pathname);
      return { group: isCurrent(security(ctx)!, ctx), rows: rows(ctx).filter((row) => isCurrent(row, ctx)).map((row) => row.id) };
    };
    expect(lit(VULNS)).toEqual({ group: true, rows: ["registered-vulnerabilities"] });
    expect(lit(ADVISORY)).toEqual({ group: true, rows: ["registered-vulnerabilities"] });
    expect(lit(SBOMS)).toEqual({ group: true, rows: ["registered-sboms"] });
    expect(lit("/forge/p/shopfront/secrets")).toEqual({ group: false, rows: [] });
    // The Security pages are top-level pages, not a drill-in.
    expect(activeArea(hybridNav, ctxAt(VULNS))).toBeNull();
  });

  it("puts both rows after More tools in the forge host's grouped nav, which draws no Security group", () => {
    unregister = registerSecurityNav();
    const items = buildGroupedConsoleNav({ project: "shopfront", page: { kind: "sboms" }, env: "prod", envs: [{ name: "prod", preview: false }] }).items;
    const labels = items.map((item) => item.label);
    const at = labels.indexOf("Vulnerabilities");
    expect(at).toBeGreaterThan(-1);
    expect(labels.slice(at, at + 2)).toEqual(["Vulnerabilities", "SBOMs"]);
    expect(items[at + 1]!.href).toBe(`${SBOMS}?env=prod`);
    expect(items[at + 1]!.active).toBe(true);
    expect(items[at]!.active).toBe(false);
  });

  it("is registered by the module that mounts the console's routes", async () => {
    expect(consoleNavEntries().map((entry) => entry.id)).toEqual([]);
    await import("../../../../forgeConsoleRoutes");
    expect(consoleNavEntries().map((entry) => entry.id)).toEqual(["vulnerabilities", "sboms"]);
  });
});
