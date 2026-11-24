// Copyright (c) 2026 Reliant Labs

/**
 * The Tests group's badge (navConfig/tests.tsx): the console's `tests` count
 * (navCounts, filled by useTestsNavCount by the project's flake policy),
 * drawn after the group's label whether the group is open or closed, and
 * nothing at zero or with no console model. The seam itself is
 * navBadge.test.tsx's.
 */

import { render, screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { inboxPath } from "@/lib/paths";

import { consolePageOf, orgPageOf } from "../../../Forge/Console/paths";
import { hybridNav, type NavCtx } from "../navConfig";
import { testsGroup } from "../navConfig/tests";
import { NavRenderer } from "../NavRenderer";

function ctxAt(href: string, tests?: number): NavCtx {
  const [pathname, query = ""] = href.split("?");
  return {
    pathname,
    search: Object.fromEntries(new URLSearchParams(query)),
    mode: "one",
    projectId: "p1",
    projectName: "shopfront",
    app: "shopfront",
    console: tests === undefined ? null : { project: "shopfront", page: { kind: "overview" }, env: "prod", envs: [], navCounts: { tests } },
    page: consolePageOf(pathname)?.page ?? null,
    orgPage: orgPageOf(pathname),
  };
}

describe("the Tests group's badge", () => {
  it("draws the console's tests count, worded for either flake policy", () => {
    expect(testsGroup.badge?.(ctxAt(inboxPath(), 7))).toEqual({ type: "count", value: 7, label: "tests needing attention" });
    expect(testsGroup.badge?.(ctxAt(inboxPath(), 1))).toMatchObject({ value: 1, label: "test needing attention" });
  });

  it("draws nothing at zero, or before the console model answers", () => {
    expect(testsGroup.badge?.(ctxAt(inboxPath(), 0))).toBeUndefined();
    expect(testsGroup.badge?.(ctxAt(inboxPath()))).toBeUndefined();
  });

  it("sits after the closed group's label", () => {
    render(<NavRenderer config={hybridNav} ctx={ctxAt(inboxPath(), 7)} slots={{}} onNavigate={vi.fn()} />);
    const group = screen.getByTestId("nav-tests");
    expect(group).toHaveTextContent("Tests");
    expect(within(group).getByText("7")).toBeInTheDocument();
    expect(within(group).getByText("tests needing attention")).toHaveClass("sr-only");
  });

  it("is absent with nothing to triage", () => {
    render(<NavRenderer config={hybridNav} ctx={ctxAt(inboxPath(), 0)} slots={{}} onNavigate={vi.fn()} />);
    expect(screen.getByTestId("nav-tests")).toHaveTextContent(/^Tests$/);
  });
});
