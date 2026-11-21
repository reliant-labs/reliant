// Copyright (c) 2026 Reliant Labs

/**
 * Tests ▾ (navConfig/tests.tsx): its four rows in order, each pointing at its
 * page in the app's env scope and lit on that page and the pages under it.
 * Where the group sits in the nav (after Deployments) is navConfig.test.tsx's.
 */

import { describe, expect, it } from "vitest";

import { inboxPath } from "@/lib/paths";

import { consolePageOf, orgPageOf } from "../../../Forge/Console/paths";
import { childrenOf, flattenIds, hrefOf, isCurrent, visibleItems, type NavCtx } from "../navConfig";
import { testsGroup } from "../navConfig/tests";

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

const row = (id: string, ctx: NavCtx) => childrenOf(testsGroup, ctx).find((item) => item.id === id)!;

describe("the Tests nav group", () => {
  it("is Overview, Coverage, Runs, Flaky tests", () => {
    const ctx = ctxAt(inboxPath());
    expect(flattenIds([testsGroup], ctx)).toEqual(["tests", "tests-overview", "tests-coverage", "tests-runs", "tests-flaky"]);
  });

  it("points each row at its page, keeping the env scope", () => {
    const ctx = ctxAt(inboxPath(), { console: { env: "prod" } as NavCtx["console"] });
    expect(hrefOf(row("tests-overview", ctx), ctx)).toBe("/forge/p/shopfront/tests?env=prod");
    expect(hrefOf(row("tests-coverage", ctx), ctx)).toBe("/forge/p/shopfront/tests/coverage?env=prod");
    expect(hrefOf(row("tests-runs", ctx), ctx)).toBe("/forge/p/shopfront/tests/runs?env=prod");
    expect(hrefOf(row("tests-flaky", ctx), ctx)).toBe("/forge/p/shopfront/tests/flaky?env=prod");
  });

  it("lights Runs on a run's page and Flaky tests on a test's", () => {
    const onRun = ctxAt("/forge/p/shopfront/tests/runs/01JD7Q");
    expect(isCurrent(row("tests-runs", onRun), onRun)).toBe(true);
    expect(isCurrent(row("tests-overview", onRun), onRun)).toBe(false);
    const onTest = ctxAt(`/forge/p/shopfront/tests/flaky/${encodeURIComponent("go:example.com/shop/internal/billing:TestRefund")}`);
    expect(isCurrent(row("tests-flaky", onTest), onTest)).toBe(true);
    const onOverview = ctxAt("/forge/p/shopfront/tests");
    expect(isCurrent(row("tests-overview", onOverview), onOverview)).toBe(true);
    expect(isCurrent(row("tests-coverage", onOverview), onOverview)).toBe(false);
  });

  it("shows only with an app", () => {
    expect(visibleItems([testsGroup], ctxAt(inboxPath())).map((item) => item.id)).toEqual(["tests"]);
    expect(visibleItems([testsGroup], ctxAt(inboxPath(), { app: null }))).toEqual([]);
  });
});
