// Copyright (c) 2026 Reliant Labs

/**
 * Tests ▾ (owner): the project's own test suite — Overview, Coverage, Runs,
 * Flaky tests — after Deployments (forge docs/proposals/tests-coverage-flakes.md
 * §3.1). Tests are per commit and branch and most runs never become a
 * release, so they get a group of their own rather than a release tab.
 *
 * Like Security it shows in every project with an app and never hides on an
 * empty read: an empty page says "No test runs yet" with the commands. Its
 * own file so the Tests work and the shell's items.tsx never collide;
 * hybrid.ts places it.
 */

import { FlaskConical } from "lucide-react";

import { withSearch } from "@/lib/paths";

import { testsPath, TESTS_VIEW_KINDS, TESTS_VIEW_LABELS, TESTS_VIEWS, type TestsView } from "../../../Forge/Console/paths";
import type { NavCtx, NavGroup, NavRow } from "./types";

const hasApp = (ctx: NavCtx) => !!ctx.app;

function testsRow(view: TestsView): NavRow {
  const kinds = TESTS_VIEW_KINDS[view];
  return {
    kind: "row",
    id: `tests-${view}`,
    label: TESTS_VIEW_LABELS[view],
    href: (ctx) => (ctx.app ? withSearch(testsPath(ctx.app, view), { env: ctx.console?.env }) : undefined),
    // A run is under Runs, a test under Flaky tests.
    match: (ctx) => !!ctx.page && kinds.includes(ctx.page.kind),
    available: hasApp,
  };
}

export const testsGroup: NavGroup = {
  kind: "group",
  id: "tests",
  label: "Tests",
  icon: <FlaskConical className="size-4" aria-hidden="true" />,
  available: hasApp,
  children: TESTS_VIEWS.map(testsRow),
};
