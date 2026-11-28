// Copyright (c) 2026 Reliant Labs

/**
 * The Tests pages ignore the console's environment scope: with the top bar
 * on prod (its default), the Overview's last run and pass rate, Coverage's
 * default report and the Flaky page read every run. Only an environment the
 * reader chose for the Tests pages (`?environment=`) narrows them. And the
 * fixtures run on the clock the visual harness and the preview freeze.
 */

import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";

import { forgeConsoleTestsCoverageSearchSchema, forgeConsoleTestsFlakySearchSchema, forgeConsoleTestsSearchSchema } from "@/routeSchemas";

import { FIXTURE_COMMIT, FIXTURE_NOW, fixtureTests, type FixtureTests } from "../fixtures";
import { FIXTURE_PREVIEW_ENV_ID } from "../runFlakeFixtures";
import { TestsCoveragePage } from "../TestsCoveragePage";
import { TestsFlakyPage } from "../TestsFlakyPage";
import { TestsOverviewPage } from "../TestsOverviewPage";
import { renderRoutedPage } from "./testsTestUtils";

vi.mock("@/components/forge-ui/link", () => ({
  default: ({ href, children, ...rest }: { href: string; children: ReactNode }) => (
    <a href={href} {...rest}>
      {children}
    </a>
  ),
}));

// uPlot draws on a canvas jsdom does not have.
vi.mock("../../Observability/charts/UPlotChart", () => ({ UPlotChart: () => <figure data-testid="chart" /> }));

vi.mock("../../Console/consoleContext", () => ({
  useForgeConsole: () => ({
    forgeProject: "shopfront",
    projectId: "proj-shopfront",
    // The console's default scope: every Tests page must read past it.
    env: "prod",
    setEnv: vi.fn(),
    roster: {
      isLoading: false,
      resolvingName: false,
      envs: [
        { name: "prod", live: { id: "env-shopfront-prod" } },
        { name: "pr-88", live: { id: FIXTURE_PREVIEW_ENV_ID } },
      ],
    },
    href: (page: { kind: string }, search?: Record<string, string>) => `/forge/p/shopfront/${page.kind}${search && Object.keys(search).length ? `?${new URLSearchParams(search)}` : ""}`,
  }),
}));

const requestsOf = (api: FixtureTests, method: string) => api.calls.filter((call) => call.method === method).map((call) => call.req as Record<string, unknown>);

function renderPage(component: () => ReactNode, path: string, url: string, schema: { parse: (search: unknown) => unknown }) {
  const api = fixtureTests("report");
  const utils = renderRoutedPage(api, { path: `/forge/p/$project/${path}`, url: `/forge/p/shopfront/${url}`, validateSearch: (search) => schema.parse(search), component });
  return { ...utils, api };
}

describe("the Tests pages under the console's prod scope", () => {
  it("the Overview's last run and pass rate are over every run", async () => {
    const { api } = renderPage(TestsOverviewPage, "tests", "tests?env=prod", forgeConsoleTestsSearchSchema);
    expect(await screen.findByTestId("tests-headline-last-run")).toHaveTextContent("Passed");
    expect(screen.getByTestId("tests-headline-pass-rate")).toHaveTextContent("over the last 50 runs");
    expect(requestsOf(api, "getTestSummary")).toEqual([{ orgId: null, project: "shopfront", branch: undefined, environmentId: undefined }]);
    expect(screen.getByTestId("tests-branch")).toHaveTextContent("every run");
    expect(screen.queryByTestId("tests-environment")).not.toBeInTheDocument();
  });

  it("the Overview narrows to an environment the reader chose, says so, and carries it to Runs", async () => {
    const { api } = renderPage(TestsOverviewPage, "tests", "tests?env=prod&environment=pr-88", forgeConsoleTestsSearchSchema);
    expect(await screen.findByTestId("tests-environment")).toHaveTextContent("only runs recorded against pr-88");
    expect(requestsOf(api, "getTestSummary")[0]).toMatchObject({ environmentId: FIXTURE_PREVIEW_ENV_ID });
    expect(screen.getByRole("link", { name: "See runs" })).toHaveAttribute("href", "/forge/p/shopfront/tests-runs?environment=pr-88");
  });

  it("Coverage's default report is the default branch's newest, not prod's current release", async () => {
    const { api } = renderPage(TestsCoveragePage, "tests/coverage", "tests/coverage?env=prod", forgeConsoleTestsCoverageSearchSchema);
    await waitFor(() => expect(requestsOf(api, "getCoverage").length).toBeGreaterThan(0));
    expect(requestsOf(api, "getCoverage")[0]).toMatchObject({ target: { gitCommit: FIXTURE_COMMIT } });
    expect(requestsOf(api, "getTestSummary")[0]).toMatchObject({ environmentId: undefined });
    expect(await screen.findByRole("option", { name: "Newest report on the default branch · 9f3c2a1" })).toBeInTheDocument();
  });

  it("Coverage reads a chosen environment's current release", async () => {
    const { api } = renderPage(TestsCoveragePage, "tests/coverage", "tests/coverage?env=prod&environment=pr-88", forgeConsoleTestsCoverageSearchSchema);
    await waitFor(() => expect(requestsOf(api, "getCoverage").length).toBeGreaterThan(0));
    expect(requestsOf(api, "getCoverage")[0]).toMatchObject({ target: { environmentId: FIXTURE_PREVIEW_ENV_ID } });
  });

  it("the Flaky page lists every flaky test, and its summary is not narrowed", async () => {
    const { api } = renderPage(TestsFlakyPage, "tests/flaky", "tests/flaky?env=prod", forgeConsoleTestsFlakySearchSchema);
    await screen.findByTestId("tests-flaky-list");
    expect(screen.getAllByTestId(/^tests-flake-row-/)).toHaveLength(10);
    expect(requestsOf(api, "getTestSummary")[0]).toMatchObject({ environmentId: undefined });
  });
});

describe("the Tests fixtures' clock", () => {
  it("is the visual harness's FIXED_NOW (e2e/visual/clock.ts), which the preview freezes too", () => {
    // src cannot import e2e/ (a composite project includes only src), so the literal is read and compared.
    const base = readFileSync(resolve(__dirname, "../../../../../e2e/visual/clock.ts"), "utf8");
    const args = /export const FIXED_NOW = Date\.UTC\(([^)]*)\)/.exec(base)?.[1];
    expect(args, "FIXED_NOW is no longer a Date.UTC literal in e2e/visual/clock.ts").toBeDefined();
    const [year, month, ...rest] = args!.split(",").map((part) => Number(part.trim()));
    expect(FIXTURE_NOW).toBe(Date.UTC(year!, month!, ...rest));
  });
});
