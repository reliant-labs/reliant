// Copyright (c) 2026 Reliant Labs

/**
 * The console's aggregate pages (owner, 2026-11-27): which pages have one
 * (severalModeOf), the frame they share (scope, totals, hint, a row per
 * project), and how a row or the hint drills down into one project alone —
 * the same page of its app, the shell's several dropped from the URL.
 */

import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const nav = vi.hoisted(() => ({ navigateWithShellScope: vi.fn(), setLastPick: vi.fn() }));

vi.mock("../../../../Layout/shellScopeNav", () => ({ navigateWithShellScope: nav.navigateWithShellScope }));
vi.mock("../../../../Layout/sidebar/shellProject", () => ({
  useShellLastPick: (select: (state: { set: (key: string) => void }) => unknown) => select({ set: nav.setLastPick }),
  useShellSeveral: () => ({ mode: null, rows: [] }),
  sectionsAppOf: () => null,
  deployedAppOf: () => null,
}));

// The Overview's reads, answered per app: shop is live in prod, blog is not deployed.
vi.mock("@/hooks/apps-queries", () => ({
  useApps: () => ({
    apps: [{ name: "shop", envs: [{ id: "shop-prod", name: "prod", verdict: "converged", release: "v1.2.0", deployedAt: undefined }] }],
    loading: false,
  }),
}));
vi.mock("@/hooks/organization-usage-queries", () => ({
  useProjectCost: (orgId: string | null, app: string | null) => ({ data: orgId && app ? { monthToDate: { usdNanos: 12_340_000_000n } } : undefined }),
}));
vi.mock("@/hooks/observability-queries", () => ({
  useObservabilityOrg: () => ({ orgId: "org-1" }),
  usePanelQuery: (scope: { projectId: string } | null) => ({
    data: scope ? { series: [{ label: "errors", value: 3 }, { label: "p95", value: 0.25 }] } : undefined,
  }),
}));
// The portfolio read the Overview's totals take their tests figures from (TestsAggregatePages.test.tsx covers it).
vi.mock("@/hooks/test-queries", () => ({
  useTestPortfolio: () => ({ data: undefined, error: null, isPending: true, isFetching: true, refetch: () => undefined, capped: false }),
}));

const { severalModeOf } = await import("../../severalProjects");
const { aggregateKinds, aggregatePageOf, hasAggregatePage } = await import("../registry");
const { AggregatePage, ProjectBreakdown, scopeLabel } = await import("../AggregatePage");
const { DrillDownHint } = await import("../DrillDownHint");
const { ProjectsOverviewPage } = await import("../../ProjectsOverview");

const APPS = [
  { key: "p1", name: "Shop", app: "shop", deployed: true },
  { key: "p2", name: "Blog", app: "blog-site", deployed: false },
];

beforeEach(() => {
  nav.navigateWithShellScope.mockClear();
  nav.setLastPick.mockClear();
});
afterEach(cleanup);

describe("severalModeOf", () => {
  it("answers aggregate for a page with a registered aggregate page, and leaves the rest as they were", () => {
    expect(severalModeOf({ kind: "overview" })).toBe("aggregate");
    expect(severalModeOf({ kind: "services" })).toBe("self");
    expect(severalModeOf({ kind: "storage" })).toBe("self");
    expect(severalModeOf({ kind: "obs-overview" })).toBe("self");
    expect(severalModeOf({ kind: "tests" })).toBe("aggregate");
    expect(severalModeOf({ kind: "tests-coverage" })).toBe("aggregate");
    expect(severalModeOf({ kind: "tests-runs" })).toBe("sections");
    expect(severalModeOf({ kind: "release", version: "v1" })).toBe("one");
  });

  it("agrees with the registry", () => {
    expect(aggregateKinds()).toEqual(["overview", "tests", "tests-coverage"]);
    for (const kind of aggregateKinds()) {
      const page = { kind } as Parameters<typeof hasAggregatePage>[0];
      expect(hasAggregatePage(page)).toBe(true);
      expect(aggregatePageOf(page)).not.toBeNull();
      expect(severalModeOf(page)).toBe("aggregate");
    }
    expect(aggregatePageOf({ kind: "releases" })).toBeNull();
  });
});

describe("the aggregate frame", () => {
  function Cells({ project }: { project: { app: string } }) {
    return <span>{`${project.app} cell`}</span>;
  }

  it("says the scope and what it covers, with no totals strip unless the backend gave totals", () => {
    render(
      <AggregatePage title="Tests" mode="many" count={2} coverage="on main">
        <p>breakdown</p>
      </AggregatePage>,
    );
    expect(screen.getByRole("heading", { level: 1, name: "Tests" })).toBeInTheDocument();
    expect(screen.getByTestId("aggregate-scope")).toHaveTextContent("2 projects, on main.");
    expect(screen.queryByTestId("aggregate-totals")).toBeNull();
    expect(screen.getByText("breakdown")).toBeInTheDocument();
    expect(scopeLabel("all", 12)).toBe("Every project");
    expect(scopeLabel("many", 1)).toBe("1 project");
  });

  it("shows the totals it is given, and the hint", () => {
    render(
      <AggregatePage
        title="Tests"
        mode="all"
        count={5}
        totals={[{ label: "Passing", value: "92.4%" }, { label: "Runs", value: "1,204" }]}
        hint={<span>the hint</span>}
      >
        <p>breakdown</p>
      </AggregatePage>,
    );
    expect(screen.getByTestId("aggregate-scope")).toHaveTextContent("Every project.");
    const totals = screen.getByTestId("aggregate-totals");
    expect(within(totals).getByText("92.4%")).toBeInTheDocument();
    expect(within(totals).getByText("Runs")).toBeInTheDocument();
    expect(screen.getByText("the hint")).toBeInTheDocument();
  });

  it("links each row to that project's own page, and drills down into that project alone", async () => {
    const user = userEvent.setup();
    render(<ProjectBreakdown projects={APPS} page={{ kind: "tests-coverage" }} columns={[{ header: "Coverage" }]} Cells={Cells} />);
    const shop = screen.getByTestId("aggregate-row-p1");
    // The href is the one-project page: it carries no several (`?projects=`).
    expect(shop).toHaveAttribute("href", "/forge/p/shop/tests/coverage");
    expect(screen.getByTestId("aggregate-row-p2")).toHaveAttribute("href", "/forge/p/blog-site/tests/coverage");
    expect(within(shop).getByText("shop cell")).toBeInTheDocument();
    await user.click(shop);
    expect(nav.setLastPick).toHaveBeenCalledWith("p1");
    // A scope of undefined drops the shell's several: the page there is one project's.
    expect(nav.navigateWithShellScope).toHaveBeenCalledWith(undefined, { to: "/forge/p/shop/tests/coverage" });
  });

  it("says when a project has no access or no data, and opens nothing it cannot show", () => {
    render(
      <ProjectBreakdown
        projects={APPS}
        page={{ kind: "tests" }}
        columns={[{ header: "Pass rate", align: "end" }]}
        Cells={Cells}
        accessOf={(project) => (project.key === "p1" ? "no-access" : "no-data")}
      />,
    );
    const shop = screen.getByTestId("aggregate-row-p1");
    expect(shop.tagName).toBe("DIV");
    expect(within(shop).getByText("No access")).toBeInTheDocument();
    expect(within(shop).queryByText("shop cell")).toBeNull();
    const blog = screen.getByTestId("aggregate-row-p2");
    expect(blog).toHaveAttribute("href", "/forge/p/blog-site/tests");
    expect(within(blog).getByText("No data yet")).toBeInTheDocument();
  });
});

describe("the drill-down hint", () => {
  it("says what one project shows, and opens the choice of the projects in scope", async () => {
    const user = userEvent.setup();
    render(<DrillDownHint purpose="browse its package and file tree" projects={APPS} page={{ kind: "tests-coverage" }} />);
    expect(screen.getByRole("note")).toHaveTextContent("Select one project to browse its package and file tree.");
    await user.click(screen.getByRole("button", { name: "Choose a project" }));
    const menu = screen.getByRole("menu");
    const items = within(menu).getAllByRole("menuitem");
    expect(items).toHaveLength(2);
    expect(items[0]).toHaveTextContent("Shop");
    expect(items[1]).toHaveTextContent("Blog");
    // The app, where it is not the project's name.
    expect(items[1]).toHaveTextContent("blog-site");
    await user.click(within(menu).getByRole("menuitem", { name: /Blog/ }));
    expect(nav.setLastPick).toHaveBeenCalledWith("p2");
    expect(nav.navigateWithShellScope).toHaveBeenCalledWith(undefined, { to: "/forge/p/blog-site/tests/coverage" });
  });
});

describe("the Overview's aggregate page", () => {
  it("keeps the Overview's columns and reads, one row per project, each opening its Overview", () => {
    render(<ProjectsOverviewPage page={{ kind: "overview" }} apps={APPS} mode="many" />);
    expect(screen.getByTestId("projects-overview")).toBeInTheDocument();
    expect(screen.getByTestId("aggregate-scope")).toHaveTextContent("2 projects, in prod over the last 24 hours; tests on each default branch; cost is this month to date.");
    // The one observability read across them has answered; the tests figures have not, so they are left out.
    const totals = screen.getByTestId("aggregate-totals");
    expect(within(totals).getByText("Errors")).toBeInTheDocument();
    expect(within(totals).getByText("p95")).toBeInTheDocument();
    expect(within(totals).queryByText(/Test pass rate/)).toBeNull();
    expect(screen.getByTestId("drill-down-hint")).toBeInTheDocument();

    const shop = screen.getByTestId("projects-overview-row-p1");
    expect(shop).toHaveAttribute("href", "/forge/p/shop");
    expect(within(shop).getByText("Healthy")).toBeInTheDocument();
    expect(within(shop).getByText("v1.2.0")).toBeInTheDocument();
    expect(within(shop).getByText("$12.34")).toBeInTheDocument();
    expect(within(shop).getByText("3")).toBeInTheDocument();

    const blog = screen.getByTestId("projects-overview-row-p2");
    expect(blog).toHaveAttribute("href", "/forge/p/blog-site");
    expect(within(blog).getByText("Not deployed")).toBeInTheDocument();
    expect(within(blog).getByText("None yet")).toBeInTheDocument();
  });
});
