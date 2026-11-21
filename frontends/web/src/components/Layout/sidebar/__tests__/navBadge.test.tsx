// Copyright (c) 2026 Reliant Labs

/**
 * The nav's live badge seam (navConfig/types.ts NavBadgeOf): a group or an
 * area carries a badge read from the context, the usual one a console count
 * (GroupedNavInput.navCounts, countBadge), drawn after the label whether the
 * group is open or closed, and nothing at zero or with no console model.
 */

import { render, screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { inboxPath } from "@/lib/paths";

import { consolePageOf, orgPageOf } from "../../../Forge/Console/paths";
import { countBadge, type NavConfig, type NavCtx } from "../navConfig";
import { NavRenderer } from "../NavRenderer";

function ctxAt(href: string, navCounts?: Record<string, number>): NavCtx {
  const [pathname, query = ""] = href.split("?");
  return {
    pathname,
    search: Object.fromEntries(new URLSearchParams(query)),
    mode: "one",
    projectId: "p1",
    projectName: "shopfront",
    app: "shopfront",
    console: navCounts === undefined ? null : { project: "shopfront", page: { kind: "overview" }, env: "prod", envs: [], navCounts },
    page: consolePageOf(pathname)?.page ?? null,
    orgPage: orgPageOf(pathname),
  };
}

const widgets = countBadge("widgets", "broken widget", "broken widgets");

const config: NavConfig = {
  items: [
    {
      kind: "group",
      id: "widgets",
      label: "Widgets",
      badge: widgets,
      children: [{ kind: "row", id: "widgets-all", label: "All widgets", href: () => "/widgets" }],
    },
    { kind: "area", id: "gadgets", label: "Gadgets", match: () => false, href: () => "/gadgets", badge: countBadge("gadgets", "gadget", "gadgets", "danger"), children: [] },
  ],
  footer: [],
};

describe("a nav item's live badge", () => {
  it("counts the console's source, labelled for one and many", () => {
    expect(widgets(ctxAt(inboxPath(), { widgets: 7 }))).toEqual({ type: "count", value: 7, label: "broken widgets" });
    expect(widgets(ctxAt(inboxPath(), { widgets: 1 }))).toMatchObject({ value: 1, label: "broken widget" });
  });

  it("is nothing at zero, for an unknown source, or before the console model answers", () => {
    expect(widgets(ctxAt(inboxPath(), { widgets: 0 }))).toBeUndefined();
    expect(widgets(ctxAt(inboxPath(), {}))).toBeUndefined();
    expect(widgets(ctxAt(inboxPath()))).toBeUndefined();
  });

  it("sits after a closed group's label and an area's row", () => {
    render(<NavRenderer config={config} ctx={ctxAt(inboxPath(), { widgets: 7, gadgets: 2 })} slots={{}} onNavigate={vi.fn()} />);
    const group = screen.getByTestId("nav-widgets");
    expect(group).toHaveAttribute("aria-expanded", "false");
    expect(within(group).getByText("7")).toBeInTheDocument();
    expect(within(group).getByText("broken widgets")).toHaveClass("sr-only");
    expect(within(screen.getByTestId("nav-gadgets")).getByText("2")).toBeInTheDocument();
  });

  it("is absent with nothing to count", () => {
    render(<NavRenderer config={config} ctx={ctxAt(inboxPath(), { widgets: 0 })} slots={{}} onNavigate={vi.fn()} />);
    expect(screen.getByTestId("nav-widgets")).toHaveTextContent(/^Widgets$/);
  });
});
