// Copyright (c) 2026 Reliant Labs

/**
 * The Overview of several projects' totals strip: errors and p95 from ONE
 * observability read over the projects in scope (QueryScope.project_ids,
 * the environments the rows read), and the tests' pass rate and coverage
 * from GetTestPortfolio, as the server computed them.
 */

import { cleanup, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { ObsScope } from "@/services/controlPlane/observability/types";

const reads = vi.hoisted(() => ({ scopes: [] as (ObsScope | null)[], portfolio: [] as unknown[] }));

vi.mock("../../../Layout/shellScopeNav", () => ({ navigateWithShellScope: vi.fn() }));
vi.mock("../../../Layout/sidebar/shellProject", () => ({
  useShellLastPick: (select: (state: { set: (key: string) => void }) => unknown) => select({ set: vi.fn() }),
  useShellSeveral: () => ({ mode: null, rows: [] }),
  sectionsAppOf: () => null,
  deployedAppOf: () => null,
}));
vi.mock("@/hooks/apps-queries", () => ({
  useApps: () => ({
    apps: [
      { name: "shop", envs: [{ id: "shop-staging", name: "staging", verdict: "converged" }, { id: "shop-prod", name: "prod", verdict: "converged", release: "v1.2.0" }] },
      { name: "blog", envs: [{ id: "blog-dev", name: "dev", verdict: "converged" }] },
    ],
    loading: false,
  }),
}));
vi.mock("@/hooks/organization-usage-queries", () => ({ useProjectCost: () => ({ data: undefined }) }));
vi.mock("@/hooks/observability-queries", () => ({
  useObservabilityOrg: () => ({ orgId: "org-1" }),
  usePanelQuery: (scope: ObsScope | null) => {
    reads.scopes.push(scope);
    return { data: scope ? { series: [{ label: "errors", value: 12 }, { label: "p95", value: 250 }] } : undefined };
  },
}));
vi.mock("@/hooks/test-queries", () => ({
  useTestPortfolio: (projects: unknown) => {
    reads.portfolio.push(projects);
    return {
      data: {
        projects: [],
        totals: {
          projects: 2,
          projectsWithRuns: 1,
          projectsWithReport: 1,
          passRate: 0.95,
          passRateRuns: 40,
          suspected: 0,
          flaky: 0,
          quarantined: 0,
          latestTotals: { passed: 1, failed: 0, skipped: 0, retryPassed: 0 },
          coverage: [{ lang: "go", granularity: "statement", combined: { statements: 17_300, covered: 12_345, percent: 71.358 }, projects: 1 }],
        },
        truncated: false,
      },
      error: null,
      capped: false,
    };
  },
}));

const { ProjectsOverviewPage } = await import("../ProjectsOverview");
const { wireScope } = await import("@/services/controlPlane/observability/api");

const APPS = [
  { key: "p1", name: "Shop", app: "shop", deployed: true },
  { key: "p2", name: "Blog", app: "blog", deployed: true },
  { key: "p3", name: "Docs", app: "docs", deployed: false },
];

beforeEach(() => {
  reads.scopes.length = 0;
  reads.portfolio.length = 0;
});
afterEach(cleanup);

/** The read the totals made: the scope no row makes (rows name one projectId). */
const totalsScope = () => reads.scopes.find((scope) => scope && !scope.projectId);

describe("the Overview's totals across several projects", () => {
  it("reads errors and p95 once, scoped by project_ids to the environments the rows read", () => {
    render(<ProjectsOverviewPage page={{ kind: "overview" }} apps={APPS} mode="many" />);
    // The deployed projects' apps, and the environment each row reads: prod, else the first.
    expect(totalsScope()).toEqual({ orgId: "org-1", projectIds: ["blog", "shop"], envIds: ["blog-dev", "shop-prod"], filters: [] });
    const totals = screen.getByTestId("aggregate-totals");
    expect(within(totals).getByText("12")).toBeInTheDocument();
    expect(within(totals).getByText("250ms")).toBeInTheDocument();
    expect(within(totals).getByText("Test pass rate over 40 runs")).toBeInTheDocument();
    expect(within(totals).getByText("95.0%")).toBeInTheDocument();
    expect(within(totals).getByText("Coverage · Go · statements (12,345 / 17,300)")).toBeInTheDocument();
    expect(within(totals).getByText("71.4%")).toBeInTheDocument();
    // Cost is the rows' alone: never summed here.
    expect(within(totals).queryByText(/Cost/)).toBeNull();
    expect(reads.portfolio).toContainEqual(["shop", "blog", "docs"]);
  });

  it("names no project for every project: the org, narrowed by the rows' environments", () => {
    render(<ProjectsOverviewPage page={{ kind: "overview" }} apps={APPS} mode="all" />);
    const scope = totalsScope();
    expect(scope).toEqual({ orgId: "org-1", envIds: ["blog-dev", "shop-prod"], filters: [] });
    expect(scope).not.toHaveProperty("projectIds");
  });
});

describe("QueryScope on the wire", () => {
  const WINDOW = { startMs: 0, endMs: 1 };

  it("carries project_ids for several projects", () => {
    const wire = wireScope({ orgId: "org-1", projectIds: ["blog", "shop"], envIds: ["shop-prod"], filters: [] }, WINDOW);
    expect(wire).toMatchObject({ orgId: "org-1", projectIds: ["blog", "shop"], envIds: ["shop-prod"] });
    expect(wire.projectId).toBeUndefined();
  });

  it("never sends project_ids beside a project_id (the server refuses both), nor an empty list", () => {
    expect(wireScope({ orgId: "o", projectId: "shop", projectIds: ["blog"], envIds: [], filters: [] }, WINDOW)).not.toHaveProperty("projectIds");
    expect(wireScope({ orgId: "o", projectIds: [], envIds: [], filters: [] }, WINDOW)).not.toHaveProperty("projectIds");
  });
});
