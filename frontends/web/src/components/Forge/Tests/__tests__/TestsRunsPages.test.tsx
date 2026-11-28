// Copyright (c) 2026 Reliant Labs

/**
 * Tests › Runs and a run's page against the fixture TestsService: the full
 * list (paged), an empty record, a control plane without the service, a
 * reader without tests:read, filters that match nothing, and the filters
 * held in the URL (a real router, the route's own schema). A run's page:
 * failures with their output and classification (host and correlated never
 * count), retry passes, the slowest tests, zero-evidence packages, a run
 * still being processed, and one that does not exist.
 */

import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";

import { forgeConsoleTestsRunsSearchSchema } from "@/routeSchemas";

import { FIXTURE_NOW, FIXTURE_PROJECT, fixtureTests, type FixtureTests, type TestsScenario } from "../fixtures";
import { FIXTURE_PR_BRANCH, FIXTURE_PREVIEW_ENV_ID, FIXTURE_RUN_IDS } from "../runFlakeFixtures";
import { TestsRunPage, TestsRunView } from "../TestsRunPage";
import { TestsRunsPage } from "../TestsRunsPage";
import { renderRoutedPage, renderWithTests } from "./testsTestUtils";

vi.mock("@/components/forge-ui/link", () => ({
  default: ({ href, children, ...rest }: { href: string; children: ReactNode }) => (
    <a href={href} {...rest}>
      {children}
    </a>
  ),
}));

const consoleState = vi.hoisted(() => ({ env: "all", setEnv: vi.fn() }));
vi.mock("../../Console/consoleContext", () => ({
  useForgeConsole: () => ({
    forgeProject: "shopfront",
    projectId: "proj-shopfront",
    env: consoleState.env,
    setEnv: consoleState.setEnv,
    roster: {
      isLoading: false,
      resolvingName: false,
      envs: [
        { name: "prod", live: { id: "env-shopfront-prod" } },
        { name: "pr-88", live: { id: FIXTURE_PREVIEW_ENV_ID } },
      ],
    },
    href: (page: { kind: string; run?: string; test?: string }, search?: Record<string, string>) => {
      const base = `/forge/p/shopfront/tests/${page.kind === "tests-flaky-test" ? `flaky/${encodeURIComponent(page.test!)}` : page.kind === "tests-run" ? `runs/${page.run}` : "runs"}`;
      return search && Object.keys(search).length ? `${base}?${new URLSearchParams(search)}` : base;
    },
  }),
}));

beforeEach(() => {
  consoleState.env = "all";
  consoleState.setEnv.mockReset();
});

const RUNS_PATH = "/forge/p/$project/tests/runs";

function renderRuns(scenario: TestsScenario | FixtureTests, query = "") {
  const api = typeof scenario === "string" ? fixtureTests(scenario) : scenario;
  const utils = renderRoutedPage(api, {
    path: RUNS_PATH,
    url: `/forge/p/shopfront/tests/runs${query}`,
    validateSearch: (search) => forgeConsoleTestsRunsSearchSchema.parse(search),
    component: TestsRunsPage,
  });
  return { ...utils, api };
}

const lastRunsRequest = (api: FixtureTests) => [...api.calls].reverse().find((call) => call.method === "listTestRuns")!.req as { filters: Record<string, unknown> };

async function pick(menuTestId: string, option: string) {
  fireEvent.click(screen.getByTestId(menuTestId));
  fireEvent.click(await screen.findByRole("menuitemradio", { name: option }));
}

describe("Tests › Runs: the full list", () => {
  it("shows each run's status, lane, source, commit, totals, retry passes, duration and CI link, newest first", async () => {
    const { api } = renderRuns("report");
    const list = await screen.findByTestId("tests-runs-list");
    const rows = within(list).getAllByTestId(/^tests-run-row-/);
    expect(rows).toHaveLength(6);
    expect(rows[0]).toHaveAttribute("data-testid", `tests-run-row-${FIXTURE_RUN_IDS.pending}`);

    // A run still being ingested is processing, with no counts: never passed.
    expect(rows[0]).toHaveTextContent("Processing");
    expect(rows[0]).not.toHaveTextContent("Passed");

    const e2e = screen.getByTestId(`tests-run-row-${FIXTURE_RUN_IDS.e2e}`);
    expect(e2e).toHaveTextContent("Passed");
    expect(e2e).toHaveTextContent("e2e");
    expect(e2e).toHaveTextContent("CI");
    expect(e2e).toHaveTextContent("9f3c2a1");
    expect(e2e).toHaveTextContent("main");
    expect(e2e).toHaveTextContent("214 passed · 3 skipped");
    expect(e2e).toHaveTextContent("9m");
    expect(within(e2e).getByTestId(`tests-run-ci-${FIXTURE_RUN_IDS.e2e}`)).toHaveAttribute("href", "https://github.com/acme/shopfront/actions/runs/4411");
    expect(within(e2e).getAllByRole("link")[0]).toHaveAttribute("href", `/forge/p/shopfront/tests/runs/${FIXTURE_RUN_IDS.e2e}`);

    const integration = screen.getByTestId(`tests-run-row-${FIXTURE_RUN_IDS.integration}`);
    expect(integration).toHaveTextContent("388 passed");
    expect(integration.querySelector(".text-warning-ink")).toHaveTextContent("1");

    const preview = screen.getByTestId(`tests-run-row-${FIXTURE_RUN_IDS.previewE2e}`);
    expect(preview).toHaveTextContent("Environment");
    expect(preview).toHaveTextContent("pr-88");

    const failed = screen.getByTestId(`tests-run-row-${FIXTURE_RUN_IDS.branchUnit}`);
    expect(failed).toHaveTextContent("5 failed");
    expect(failed).toHaveTextContent(FIXTURE_PR_BRANCH);

    expect(lastRunsRequest(api).filters).toEqual({});
  });

  it("pages with the server's token", async () => {
    const { api } = renderRuns("report");
    fireEvent.click(await screen.findByTestId("tests-runs-more"));
    await waitFor(() => expect(screen.getAllByTestId(/^tests-run-row-/)).toHaveLength(9));
    expect(screen.queryByTestId("tests-runs-more")).not.toBeInTheDocument();
    const local = screen.getByTestId(`tests-run-row-${FIXTURE_RUN_IDS.local}`);
    expect(local).toHaveTextContent("Local");
    expect(local).toHaveTextContent("uncommitted");
    expect(screen.getByTestId(`tests-run-row-${FIXTURE_RUN_IDS.confirm}`)).toHaveTextContent("Flake confirm");
    expect(api.calls.filter((call) => call.method === "listTestRuns").map((call) => (call.req as { pageToken: string }).pageToken)).toEqual(["", "fixture-page-2"]);
  });
});

describe("Tests › Runs: filters held in the URL", () => {
  it("reads the URL's filters into the request", async () => {
    const { api } = renderRuns("report", `?branch=${encodeURIComponent(FIXTURE_PR_BRANCH)}&status=failed`);
    await screen.findByTestId("tests-runs-list");
    expect(lastRunsRequest(api).filters).toEqual({ branch: FIXTURE_PR_BRANCH, outcome: "failed" });
    expect(screen.getAllByTestId(/^tests-run-row-/).map((row) => row.getAttribute("data-testid"))).toEqual([
      `tests-run-row-${FIXTURE_RUN_IDS.branchUnit}`,
      `tests-run-row-${FIXTURE_RUN_IDS.local}`,
    ]);
    expect(screen.getByTestId("tests-runs-filter-branch")).toHaveTextContent(`Branch: ${FIXTURE_PR_BRANCH}`);
    expect(screen.getByTestId("tests-runs-filter-status")).toHaveTextContent("Status: Failed");
  });

  it("writes a picked filter to the URL and keeps the others", async () => {
    const { api, search } = renderRuns("report", "?status=failed&env=all");
    await screen.findByTestId("tests-runs-list");
    await pick("tests-runs-filter-source", "Local");
    await waitFor(() => expect(search()).toEqual({ env: "all", status: "failed", source: "local" }));
    await waitFor(() => expect(lastRunsRequest(api).filters).toEqual({ outcome: "failed", source: "local" }));
    expect(screen.getAllByTestId(/^tests-run-row-/)).toHaveLength(1);

    await pick("tests-runs-filter-lane", "unit");
    await waitFor(() => expect(search()).toMatchObject({ lane: "unit" }));
  });

  it("takes a typed commit, and clears every filter at once", async () => {
    const { search } = renderRuns("report", "?lane=unit");
    await screen.findByTestId("tests-runs-list");
    fireEvent.click(screen.getByTestId("tests-runs-filter-commit"));
    const input = screen.getByTestId("tests-runs-filter-commit-input");
    fireEvent.change(input, { target: { value: " 1a2b3c4 " } });
    fireEvent.keyDown(input, { key: "Enter" });
    await waitFor(() => expect(search()).toEqual({ lane: "unit", commit: "1a2b3c4" }));
    await waitFor(() => expect(screen.getAllByTestId(/^tests-run-row-/)).toHaveLength(2));

    fireEvent.click(screen.getByTestId("tests-runs-filters-clear"));
    await waitFor(() => expect(search()).toEqual({}));
  });

  it("says when filters match nothing, and clears them", async () => {
    const { search } = renderRuns("report", "?lane=e2e&status=failed");
    const none = await screen.findByTestId("tests-runs-no-match");
    expect(none).toHaveTextContent("No runs match these filters");
    fireEvent.click(within(none).getByTestId("tests-runs-no-match-clear"));
    await waitFor(() => expect(search()).toEqual({}));
  });

  it("ignores the console's environment scope: every run, whatever the top bar says", async () => {
    consoleState.env = "prod";
    const { api, search } = renderRuns("report", "?env=prod");
    await screen.findByTestId("tests-runs-list");
    expect(screen.getAllByTestId(/^tests-run-row-/)).toHaveLength(6);
    expect(lastRunsRequest(api).filters).toEqual({});
    expect(screen.getByTestId("tests-runs-filter-env")).toHaveTextContent(/^Environment$/);
    expect(search()).toEqual({ env: "prod" });
  });

  it("narrows to an environment only when picked, kept under its own URL key beside the console's", async () => {
    consoleState.env = "prod";
    const { api, search } = renderRuns("report", "?env=prod");
    await screen.findByTestId("tests-runs-list");
    await pick("tests-runs-filter-env", "pr-88");
    await waitFor(() => expect(search()).toEqual({ env: "prod", environment: "pr-88" }));
    await waitFor(() => expect(lastRunsRequest(api).filters).toEqual({ environmentId: FIXTURE_PREVIEW_ENV_ID }));
    expect(screen.getAllByTestId(/^tests-run-row-/).map((row) => row.getAttribute("data-testid"))).toEqual([`tests-run-row-${FIXTURE_RUN_IDS.previewE2e}`]);
    expect(screen.getByTestId("tests-runs-filter-env")).toHaveTextContent("Environment: pr-88");
    // The console's scope is never written.
    expect(consoleState.setEnv).not.toHaveBeenCalled();

    fireEvent.click(screen.getByTestId("tests-runs-filter-env-clear"));
    await waitFor(() => expect(search()).toEqual({ env: "prod" }));
  });

  it("says a picked environment with no runs has none, and offers every environment", async () => {
    const { api, search } = renderRuns("report", "?environment=prod");
    const none = await screen.findByTestId("tests-runs-none-in-env");
    expect(none).toHaveTextContent("No runs recorded against prod");
    expect(none).toHaveTextContent("CI and local runs carry no environment");
    expect(lastRunsRequest(api).filters).toEqual({ environmentId: "env-shopfront-prod" });
    fireEvent.click(within(none).getByTestId("tests-runs-all-envs"));
    await waitFor(() => expect(search()).toEqual({}));
    await screen.findByTestId("tests-runs-list");
  });

  it("says when a linked environment does not exist, and offers every environment", async () => {
    const { search } = renderRuns("report", "?environment=staging&lane=unit");
    const note = await screen.findByTestId("tests-env-note");
    expect(note).toHaveTextContent("There is no environment named staging");
    fireEvent.click(within(note).getByTestId("tests-env-note-all"));
    await waitFor(() => expect(search()).toEqual({ lane: "unit" }));
  });
});

describe("Tests › Runs: nothing to show", () => {
  it("names the commands that record a first run when nothing was ever recorded", async () => {
    renderRuns("no-runs");
    const empty = await screen.findByTestId("tests-no-runs");
    expect(empty).toHaveTextContent("No test runs yet");
    expect(screen.queryByTestId("tests-runs-filters")).not.toBeInTheDocument();
  });

  it("says a control plane without the service doesn't store runs yet", async () => {
    renderRuns("not-configured");
    expect(await screen.findByTestId("tests-error")).toHaveAttribute("data-kind", "not-configured");
    expect(screen.queryByTestId("tests-no-runs")).not.toBeInTheDocument();
  });

  it("names the grant a reader without tests:read lacks", async () => {
    renderRuns("denied");
    const state = await screen.findByTestId("tests-error");
    expect(state).toHaveAttribute("data-kind", "denied");
    expect(state).toHaveTextContent("tests:read");
  });
});

// ── One run ─────────────────────────────────────────────────────────────────

const RUN_HREFS = {
  runs: (search?: Record<string, string>) => `/forge/p/shopfront/tests/runs${search ? `?${new URLSearchParams(search)}` : ""}`,
  test: (id: string) => `/forge/p/shopfront/tests/flaky/${encodeURIComponent(id)}`,
};

function renderRun(scenario: TestsScenario, runId: string) {
  return renderWithTests(fixtureTests(scenario), <TestsRunView project={FIXTURE_PROJECT} runId={runId} hrefs={RUN_HREFS} now={FIXTURE_NOW} />);
}

describe("a run's page", () => {
  it("shows a failed run's facts, totals and failures, the first output open", async () => {
    renderRun("report", FIXTURE_RUN_IDS.branchUnit);
    const facts = await screen.findByTestId("tests-run-facts");
    expect(facts).toHaveTextContent("5 failed");
    expect(within(facts).getByTestId("tests-run-ci")).toHaveAttribute("href", "https://github.com/acme/shopfront/actions/runs/4409");
    expect(within(facts).getByRole("link", { name: "1a2b3c4" })).toHaveAttribute("href", RUN_HREFS.runs({ commit: "1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d" }));
    expect(screen.getByTestId("tests-run-passed")).toHaveTextContent("806");
    expect(screen.getByTestId("tests-run-failed")).toHaveTextContent("5");
    expect(screen.getByTestId("tests-run-packages")).toHaveTextContent("41");

    const failures = screen.getByTestId("tests-run-failures");
    expect(failures).toHaveTextContent("4 of 5 are host or infrastructure failures, never counted against their tests");
    const first = screen.getByTestId("tests-run-failure-0");
    expect(first).toHaveAttribute("data-verdict", "real");
    expect(first).toHaveTextContent("TestRefund/partial");
    expect(first).toHaveTextContent("Real failure");
    expect(first).toHaveTextContent("2 attempts");
    expect(first).toHaveTextContent("internal/billing/billing_test.go:64");
    expect(screen.getByTestId("tests-run-failure-0-output")).toHaveAttribute("open");
    expect(screen.getByTestId("tests-run-failure-0-output").querySelector("pre")).toHaveTextContent("Refund(order 42, partial) = 1250, want 1200");
    expect(within(first).getAllByRole("link")[0]).toHaveAttribute("href", RUN_HREFS.test("go:github.com/acme/shopfront/internal/billing:TestRefund/partial"));

    const host = screen.getByTestId("tests-run-failure-1");
    expect(host).toHaveAttribute("data-verdict", "host");
    expect(host).toHaveTextContent("Host failure: not counted");
    expect(screen.getByTestId("tests-run-failure-1-output")).not.toHaveAttribute("open");
    expect(screen.getByTestId("tests-run-failure-2")).toHaveTextContent("Infra group: not counted");

    expect(screen.getByTestId("tests-run-zero-evidence")).toHaveTextContent("internal/legacyexport");
    expect(screen.getByTestId("tests-run-zero-evidence")).toHaveTextContent("of the 41 packages it ran");
    const slowest = screen.getByTestId("tests-run-slowest");
    expect(within(slowest).getAllByRole("listitem")[0]).toHaveTextContent("TestOrderExport");
    expect(within(slowest).getAllByRole("listitem")[0]).toHaveTextContent("30.02s");
  });

  it("lists the tests that passed only on a retry", async () => {
    renderRun("report", FIXTURE_RUN_IDS.integration);
    const retries = await screen.findByTestId("tests-run-retry-passes");
    expect(retries).toHaveTextContent("TestSyncJob");
    expect(screen.getByTestId("tests-run-retry")).toHaveTextContent("1");
    expect(screen.getByTestId("tests-run-no-failures")).toHaveTextContent("No test failed.");
    expect(screen.getByTestId("tests-run-zero-evidence")).toHaveTextContent("Every package it ran recorded a test.");
  });

  it("says a run still being processed has no figures yet", async () => {
    renderRun("report", FIXTURE_RUN_IDS.pending);
    expect(await screen.findByTestId("tests-run-processing")).toBeInTheDocument();
    expect(screen.getByTestId("tests-run-facts")).toHaveTextContent("Processing");
    expect(screen.queryByTestId("tests-run-totals")).not.toBeInTheDocument();
    expect(screen.queryByTestId("tests-run-failures")).not.toBeInTheDocument();
  });

  it("says a run this project doesn't have isn't in it", async () => {
    renderRun("report", "01NOSUCHRUN");
    const state = await screen.findByTestId("tests-run-error");
    expect(state).toHaveAttribute("data-kind", "not-found");
    expect(state).toHaveTextContent("Run 01NOSUCHRUN isn't in this project");
  });

  it("says a control plane without the service doesn't store runs yet", async () => {
    renderRun("not-configured", FIXTURE_RUN_IDS.e2e);
    expect(await screen.findByTestId("tests-run-error")).toHaveAttribute("data-kind", "not-configured");
  });

  it("reads the run named in the route", async () => {
    const api = fixtureTests("report");
    renderRoutedPage(api, { path: "/forge/p/$project/tests/runs/$run", url: `/forge/p/shopfront/tests/runs/${FIXTURE_RUN_IDS.e2e}`, validateSearch: (s) => s, component: TestsRunPage });
    expect(await screen.findByTestId("tests-run-facts")).toHaveTextContent("e2e");
    expect(api.calls.find((call) => call.method === "getTestRun")!.req).toEqual({ orgId: null, project: "shopfront", runId: FIXTURE_RUN_IDS.e2e });
  });
});
