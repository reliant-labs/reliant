// Copyright (c) 2026 Reliant Labs

/**
 * Tests › Overview against the fixture TestsService: the full report, an
 * empty record, a run still being processed, one lane only, and the two
 * refusals — a control plane without the service is a state, never an error.
 */

import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";


import { FIXTURE_NOW, FIXTURE_PROJECT, fixtureTests, type FixtureTests, type TestsScenario } from "../fixtures";
import { TestsOverviewView } from "../TestsOverviewPage";
import { renderWithTests } from "./testsTestUtils";

vi.mock("@/components/forge-ui/link", () => ({
  default: ({ href, children, ...rest }: { href: string; children: ReactNode }) => (
    <a href={href} {...rest}>
      {children}
    </a>
  ),
}));

// uPlot draws on a canvas jsdom does not have; the chart's inputs are what this page decides.
vi.mock("../../Observability/charts/UPlotChart", () => ({
  UPlotChart: ({ label, series, markers, timestamps }: { label: string; series: { label: string }[]; markers: { label: string }[]; timestamps: number[] }) => (
    <figure data-testid="chart" data-series={series.map((s) => s.label).join(",")} data-markers={markers.map((m) => m.label).join(",")} data-points={timestamps.length}>
      {label}
    </figure>
  ),
}));

const HREFS = {
  flaky: "/forge/p/shopfront/tests/flaky",
  runs: "/forge/p/shopfront/tests/runs",
  run: (id: string) => `/forge/p/shopfront/tests/runs/${id}`,
  coverage: "/forge/p/shopfront/tests/coverage",
};

/** The Overview over `fixture`, in its scope; `null`, on the app's own transport and query client (no scope). */
function renderOverview(fixture: FixtureTests | null, { environmentId, branch }: { environmentId?: string; branch?: string } = {}) {
  const view = <TestsOverviewView scope={{ project: FIXTURE_PROJECT, environmentId }} branch={branch} hrefs={HREFS} now={FIXTURE_NOW} />;
  if (fixture) return renderWithTests(fixture, view);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={client}>{view}</QueryClientProvider>);
}

const scenario = (name: TestsScenario) => fixtureTests(name);

describe("Tests overview: a full report", () => {
  it("leads with coverage, pass rate, flaky tests and the last run", async () => {
    renderOverview(scenario("report"));
    const coverage = await screen.findByTestId("tests-headline-coverage");
    expect(coverage).toHaveTextContent("74.2%");
    expect(within(coverage).getByTestId("tests-coverage-delta")).toHaveTextContent("+0.6");
    expect(within(coverage).getByTestId("tests-coverage-lanes")).toHaveTextContent("unit 61.4 · integration 38.0 · e2e 52.7");

    const passRate = screen.getByTestId("tests-headline-pass-rate");
    expect(passRate).toHaveTextContent("98.7%");
    expect(passRate).toHaveTextContent("over the last 50 runs");

    const flaky = screen.getByTestId("tests-headline-flaky");
    expect(flaky).toHaveTextContent("4");
    expect(flaky).toHaveTextContent("2 quarantined · 3 suspected");
    expect(flaky).toHaveTextContent("5 host failures in 30 days, not counted");
    expect(within(flaky).getByRole("link", { name: "Triage" })).toHaveAttribute("href", HREFS.flaky);

    const last = screen.getByTestId("tests-headline-last-run");
    expect(last).toHaveTextContent("12m ago");
    expect(last).toHaveTextContent("Passed");
    expect(last).toHaveTextContent("CI #4411 · e2e · main 9f3c2a1");
    expect(within(last).getByRole("link", { name: "Open run" })).toHaveAttribute("href", HREFS.run("01JD7Q4XK2M8N5P3R6T9V1W4Y7"));
  });

  it("draws coverage over time, combined and per lane, with the releases marked", async () => {
    renderOverview(scenario("report"));
    const chart = await screen.findByTestId("chart");
    expect(chart).toHaveAttribute("data-series", "Combined,unit,integration,e2e");
    expect(chart).toHaveAttribute("data-markers", "20261105.093000-77e0c1d2a3b4,20261118.121200-a1b2c3d4e5f6");
    expect(chart).toHaveAttribute("data-points", "30");
    expect(screen.getByTestId("tests-trend")).toHaveTextContent("30 commits on main · 2 releases marked");
  });

  it("asks for the branch and environment in scope", async () => {
    const api = scenario("report");
    renderOverview(api, { environmentId: "env-shop-pr12", branch: "pr-88" });
    await screen.findByTestId("tests-headline");
    expect(api.calls[0]).toEqual({ method: "getTestSummary", req: { orgId: null, project: FIXTURE_PROJECT, branch: "pr-88", environmentId: "env-shop-pr12" } });
  });
});

describe("Tests overview: partial records", () => {
  it("shows a run still being processed as processing, with no figures", async () => {
    renderOverview(scenario("pending"));
    const last = await screen.findByTestId("tests-headline-last-run");
    expect(last).toHaveTextContent("Processing");
    expect(last).not.toHaveTextContent("Passed");
    expect(screen.getByTestId("tests-headline-coverage")).toHaveTextContent("—");
    expect(screen.getByTestId("tests-headline-coverage")).toHaveTextContent("still being processed");
    expect(screen.getByTestId("tests-headline-pass-rate")).toHaveTextContent("No run judged yet");
    expect(screen.getByTestId("tests-headline-pass-rate")).not.toHaveTextContent("100%");
    expect(screen.getByTestId("tests-trend-short")).toBeInTheDocument();
  });

  it("says which lane a one-lane figure merges, and draws no trend from one commit", async () => {
    renderOverview(scenario("unit-only"));
    expect(await screen.findByTestId("tests-coverage-lanes")).toHaveTextContent("unit only so far");
    expect(screen.getByTestId("tests-headline-coverage")).toHaveTextContent("no base report to compare");
    expect(screen.queryByTestId("tests-coverage-delta")).not.toBeInTheDocument();
    expect(screen.queryByTestId("chart")).not.toBeInTheDocument();
  });
});

describe("Tests overview: nothing to show", () => {
  it("names the commands that record a first run when nothing was ever recorded", async () => {
    renderOverview(scenario("no-runs"));
    const empty = await screen.findByTestId("tests-no-runs");
    expect(empty).toHaveTextContent("No test runs yet");
    expect(within(empty).getByTestId("tests-no-runs-steps")).toHaveTextContent("reliant forge test run && reliant forge test report --upload");
    expect(within(empty).getByTestId("tests-no-runs-steps")).toHaveTextContent("reliant forge test capture --lane unit");
    expect(within(empty).getByTestId("learn-link-tests")).toHaveAttribute("href", "https://docs.reliantlabs.io/deep-dives/how-forge-tests-work");
    expect(screen.queryByTestId("tests-headline")).not.toBeInTheDocument();
  });

  it("says a control plane without the service doesn't store runs yet, and is no error", async () => {
    renderOverview(scenario("not-configured"));
    const state = await screen.findByTestId("tests-error");
    expect(state).toHaveAttribute("data-kind", "not-configured");
    expect(state).toHaveTextContent("This control plane doesn't store test runs yet");
    expect(within(state).getByRole("status")).toBeInTheDocument();
    expect(within(state).queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.queryByTestId("tests-no-runs")).not.toBeInTheDocument();
  });

  it("names the grant a reader without tests:read lacks", async () => {
    renderOverview(scenario("denied"));
    const state = await screen.findByTestId("tests-error");
    expect(state).toHaveAttribute("data-kind", "denied");
    expect(state).toHaveTextContent("tests:read");
  });

  it("with no control plane configured, the app's own reads answer as a control plane without the service", async () => {
    renderOverview(null);
    expect(await screen.findByTestId("tests-error")).toHaveAttribute("data-kind", "not-configured");
  });
});
