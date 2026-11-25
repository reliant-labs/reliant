// Copyright (c) 2026 Reliant Labs

/**
 * The flat nav (a phone's drawer; owner, 2026-11-24): every row a drill-in or
 * an accordion holds is drawn exactly like a top-level row — same indent,
 * colour and an icon — while the nested (desktop) nav keeps its hierarchy.
 */

import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { runsPath } from "@/lib/paths";

import { registerConsoleNavEntry } from "../../../Forge/Console/consoleNavRegistry";
import { consolePageOf, consolePath, orgPageOf } from "../../../Forge/Console/paths";
import { childrenOf, hybridNav, type NavCtx, type NavItem } from "../navConfig";
import { NavRenderer } from "../NavRenderer";

vi.mock("@/lib/changesFeature", () => ({ changesUIEnabled: () => true }));

function ctxAt(pathname: string, over: Partial<NavCtx> = {}): NavCtx {
  return {
    pathname,
    search: {},
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

/** Every row, group and area the config can draw for `ctx`, at any depth (availability aside). */
function everyRow(items: readonly NavItem[], ctx: NavCtx): NavItem[] {
  return items.flatMap((item) => {
    if (item.kind === "heading" || item.kind === "slot") return [];
    if (item.kind === "row") return [item];
    return [item, ...everyRow(childrenOf(item, ctx), ctx)];
  });
}

describe("the flat nav", () => {
  it("has an icon for every row it can draw", () => {
    // An app with a custom dashboard and a registered Security row, so the dynamic rows are there too.
    const console = {
      project: "shopfront",
      page: { kind: "secrets" as const },
      env: "prod",
      envs: [],
      customDashboards: [{ dashKind: "repo", key: "checkout", title: "Checkout" }],
    } as unknown as NavCtx["console"];
    const ctx = ctxAt(consolePath("shopfront", { kind: "secrets" }), { console });
    const unregister = registerConsoleNavEntry({ id: "advisories", label: "Advisories", segment: "advisories", after: "more-tools", group: "security" });
    try {
      const rows = everyRow([...hybridNav.items, ...hybridNav.footer, ...(hybridNav.sectionPages ?? [])], ctx)
        // A section page's own entry is never a row: it is the column's back header.
        .filter((item) => !(hybridNav.sectionPages ?? []).includes(item as never));
      expect(rows.length).toBeGreaterThan(40);
      const bare = rows.filter((item) => item.kind !== "heading" && item.kind !== "slot" && !item.icon && !item.flatIcon).map((item) => item.id);
      expect(bare).toEqual([]);
    } finally {
      unregister();
    }
  });

  it("draws an accordion's rows in a drill-in like top-level rows", () => {
    render(<NavRenderer config={hybridNav} ctx={ctxAt(runsPath())} slots={{}} onNavigate={vi.fn()} flat />);
    // Library: in the open Workflows accordion, and not the page on screen.
    const library = screen.getByTestId("nav-workflows-library");
    const sandboxes = screen.getByTestId("nav-sandboxes");
    expect(library.querySelector("svg")).not.toBeNull();
    for (const cls of ["pl-2", "text-ui", "text-foreground"]) expect(library.className.split(" ")).toContain(cls);
    expect(library.className).not.toContain("text-foreground/75");
    // The same classes as a top-level row of the drill-in.
    expect(library.className).toBe(sandboxes.className);
  });

  it("leaves the nested nav's hierarchy alone", () => {
    render(<NavRenderer config={hybridNav} ctx={ctxAt(runsPath())} slots={{}} onNavigate={vi.fn()} />);
    const library = screen.getByTestId("nav-workflows-library");
    expect(library.querySelector("svg")).toBeNull();
    expect(library.className).toContain("pl-8");
    expect(library.className).toContain("text-foreground/75");
  });
});
