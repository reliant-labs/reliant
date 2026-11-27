// Copyright (c) 2026 Reliant Labs

/**
 * ML › (navConfig/items.tsx mlArea): placed right after Observability, shown
 * to every signed-in org with or without an app, its six rows in order, each
 * lit on its page, and Runs, Models and Inference labelled "Early access".
 * Where the rest of the nav sits is navConfig.test.tsx's.
 */

import { describe, expect, it, vi } from "vitest";

import { inboxPath } from "@/lib/paths";

import { consolePageOf, orgPageOf, orgPath } from "../../../Forge/Console/paths";
import { activeArea, childrenOf, hrefOf, hybridNav, isCurrent, visibleItems, type NavCtx } from "../navConfig";
import { mlArea } from "../navConfig/items";

vi.mock("@/lib/changesFeature", () => ({ changesUIEnabled: () => false }));

function ctxAt(href: string, over: Partial<NavCtx> = {}): NavCtx {
  const [pathname, query = ""] = href.split("?");
  return {
    pathname,
    search: Object.fromEntries(new URLSearchParams(query)),
    mode: "one",
    projectId: "p1",
    projectName: "shopfront",
    app: "shopfront",
    console: null,
    page: consolePageOf(pathname)?.page ?? null,
    orgPage: orgPageOf(pathname),
    ...over,
  };
}

const rows = (ctx: NavCtx) => childrenOf(mlArea, ctx);

describe("the ML nav area", () => {
  it("sits right after Observability", () => {
    const ids = visibleItems(hybridNav.items, ctxAt(inboxPath())).map((item) => item.id);
    expect(ids.indexOf("ml")).toBe(ids.indexOf("observability") + 1);
  });

  it("shows for every signed-in org: one app, several, none chosen", () => {
    for (const over of [{}, { mode: "all" as const, app: null }, { app: null, projectId: null }] satisfies Partial<NavCtx>[]) {
      expect(visibleItems(hybridNav.items, ctxAt(inboxPath(), over)).map((item) => item.id)).toContain("ml");
    }
  });

  it("is Machines, Stack, Recommendations, Runs, Models, Inference, each an org page", () => {
    const ctx = ctxAt(inboxPath());
    expect(rows(ctx).map((row) => row.id)).toEqual(["ml-machines", "ml-stack", "ml-recommendations", "ml-runs", "ml-models", "ml-inference"]);
    expect(rows(ctx).map((row) => hrefOf(row, ctx))).toEqual([
      "/forge/ml/machines",
      "/forge/ml/stack",
      "/forge/ml/recommendations",
      "/forge/ml/runs",
      "/forge/ml/models",
      "/forge/ml/inference",
    ]);
    expect(hrefOf(mlArea, ctx)).toBe("/forge/ml/machines");
  });

  it("labels only Runs, Models and Inference Early access", () => {
    const ctx = ctxAt(inboxPath());
    const badges = Object.fromEntries(rows(ctx).map((row) => [row.id, row.kind === "row" ? row.badge?.(ctx) : undefined]));
    expect(badges["ml-runs"]).toEqual({ type: "label", text: "Early access" });
    expect(badges["ml-models"]).toEqual({ type: "label", text: "Early access" });
    expect(badges["ml-inference"]).toEqual({ type: "label", text: "Early access" });
    expect(badges["ml-machines"]).toBeUndefined();
    expect(badges["ml-stack"]).toBeUndefined();
    expect(badges["ml-recommendations"]).toBeUndefined();
  });

  it("claims all six pages and lights each one's row", () => {
    for (const view of ["machines", "stack", "recommendations", "runs", "models", "inference"] as const) {
      const ctx = ctxAt(orgPath({ kind: "ml", view }));
      expect(activeArea(hybridNav, ctx)?.id).toBe("ml");
      expect(rows(ctx).filter((row) => isCurrent(row, ctx)).map((row) => row.id)).toEqual([`ml-${view}`]);
    }
  });

  it("claims the org form of its pages too", () => {
    const ctx = ctxAt("/o/acme/ml/stack");
    expect(activeArea(hybridNav, ctx)?.id).toBe("ml");
  });

  it("does not claim Observability's org pages", () => {
    expect(activeArea(hybridNav, ctxAt(orgPath({ kind: "observability" })))?.id).toBe("observability");
  });
});
