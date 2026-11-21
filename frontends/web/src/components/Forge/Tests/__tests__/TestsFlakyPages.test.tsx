// Copyright (c) 2026 Reliant Labs

/**
 * Tests › Flaky tests and a test's page against the fixture TestsService.
 *
 * The list: ranked by flake rate shown as "x% (n runs)", never a verdict;
 * suspected is the state to act on, with the quarantine command to copy and
 * the agent that fixes it (Ask Reliant, seeded from the test's own record,
 * and disabled with its reason where no agent can start); quarantine path and "overdue"; the issue;
 * host health apart, and no row for a test that only failed on the host.
 * The state filter is held in the URL. A test's page: its record, the
 * outcome strip, its events with verdict and signal (host and correlated
 * shown as never counting), its failure signatures. And the empty,
 * unimplemented and denied states of both.
 */

import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";

import { useProject } from "@/hooks/project-queries";
import { forgeConsoleTestsFlakySearchSchema } from "@/routeSchemas";

import { AskAgentModeProvider } from "../../Observability/AskReliant/askAgentMode";
import type { AskFlakyTestSubject } from "../../Observability/AskReliant/askReliantPrompt";
import { AGENT_REASON } from "../testsAgent";

import { FIXTURE_NOW, FIXTURE_PROJECT, fixtureTestApi, type FixtureTestApi, type TestsScenario } from "../fixtures";
import { FIXTURE_RUN_IDS } from "../runFlakeFixtures";
import { TestsFlakyPage } from "../TestsFlakyPage";
import { TestsFlakyTestPage, TestsFlakyTestView } from "../TestsFlakyTestPage";
import { renderRoutedPage, renderWithApi } from "./testsTestUtils";

vi.mock("@/components/forge-ui/link", () => ({
  default: ({ href, children, ...rest }: { href: string; children: ReactNode }) => (
    <a href={href} {...rest}>
      {children}
    </a>
  ),
}));

// The agent start is Ask Reliant's (its own tests); here, what it is handed.
const ask = vi.fn(async () => true);
vi.mock("../../Observability/AskReliant/AskReliant", () => ({
  useAskReliant: () => ({ ask, busy: false, starting: null, dialog: null, mode: "chat" }),
}));

vi.mock("@/hooks/project-queries", async (importOriginal) => ({ ...(await importOriginal<typeof import("@/hooks/project-queries")>()), useProject: vi.fn() }));

vi.mock("../../Console/consoleContext", () => ({
  useForgeConsole: () => ({
    forgeProject: "shopfront",
    projectId: "proj-shopfront",
    env: "prod",
    href: (page: { kind: string; run?: string; test?: string }) =>
      page.kind === "tests-run" ? `/forge/p/shopfront/tests/runs/${page.run}` : `/forge/p/shopfront/tests/flaky/${encodeURIComponent(page.test ?? "")}`,
  }),
}));

const MODULE = "github.com/acme/shopfront";
const idOf = (dir: string, name: string) => `go:${MODULE}/${dir}:${name}`;

function renderFlaky(scenario: TestsScenario, query = "") {
  const api = fixtureTestApi(scenario);
  const utils = renderRoutedPage(api, {
    path: "/forge/p/$project/tests/flaky",
    url: `/forge/p/shopfront/tests/flaky${query}`,
    validateSearch: (search) => forgeConsoleTestsFlakySearchSchema.parse(search),
    component: TestsFlakyPage,
  });
  return { ...utils, api };
}

const flakyRequests = (api: FixtureTestApi) => api.calls.filter((call) => call.method === "listFlakyTests").map((call) => (call.req as { filters: unknown }).filters);
const rowNames = () => screen.getAllByTestId(/^tests-flake-row-/).map((row) => row.getAttribute("data-testid")!.replace("tests-flake-row-", ""));

describe("Tests › Flaky tests: the full list", () => {
  it("ranks by flake rate, shown with its n, and states each test's state", async () => {
    const { api } = renderFlaky("report");
    await screen.findByTestId("tests-flaky-list");
    expect(rowNames()).toEqual([
      "TestSyncJob",
      "TestInventoryReserve",
      "TestPriceRounding",
      "TestStockSync",
      "TestSessionRefresh",
      "TestOrderExport",
      "TestCheckoutTimeout",
      "TestCartMerge",
      "TestWebhookSign/rotated key",
      "TestEmailDigest",
    ]);
    expect(flakyRequests(api)).toEqual([{}]);

    const sync = screen.getByTestId("tests-flake-row-TestSyncJob");
    expect(sync).toHaveAttribute("data-state", "flaky");
    expect(sync).toHaveTextContent("Flaky");
    expect(screen.getByTestId("tests-flake-rate-TestSyncJob")).toHaveTextContent("1.1% (412 runs)");
    expect(screen.getByTestId("tests-flake-rate-TestSyncJob")).toHaveAttribute("title", expect.stringContaining("A ranking, never a verdict"));
    expect(sync).toHaveTextContent("same-tree split · retry pass");
    // The routed page reads the wall clock; the fixture event is 14 minutes before the fixture's "now".
    expect(sync).toHaveTextContent(/\d+[smhd] ago/);
    expect(within(sync).getByRole("link", { name: /#212/ })).toHaveAttribute("href", "https://github.com/acme/shopfront/issues/212");
    expect(within(sync).getAllByRole("link")[0]).toHaveAttribute("href", `/forge/p/shopfront/tests/flaky/${encodeURIComponent(idOf("internal/jobs", "TestSyncJob"))}`);
    // A test with few runs ranks low, and still says its n.
    expect(screen.getByTestId("tests-flake-rate-TestWebhookSign/rotated key")).toHaveTextContent("0.21% (22 runs)");
    expect(screen.getByTestId("tests-flake-row-TestCheckoutTimeout")).toHaveTextContent("Suspected");
  });

  it("shows a quarantine's path, and flags one past its expiry as overdue", async () => {
    renderFlaky("report");
    const overdue = await screen.findByTestId("tests-flake-quarantine-TestInventoryReserve");
    expect(overdue).toHaveTextContent("ci/quarantine/go/internal/inventory/TestInventoryReserve.yaml");
    expect(overdue).toHaveTextContent("overdue");
    const current = screen.getByTestId("tests-flake-quarantine-TestSessionRefresh");
    expect(current).toHaveTextContent("ci/quarantine/go/internal/auth/TestSessionRefresh.yaml");
    expect(current).not.toHaveTextContent("overdue");
  });

  it("suggests the quarantine command for a test to act on, beside the agent that fixes it", async () => {
    renderFlaky("report");
    const suspected = await screen.findByTestId("tests-flake-actions-TestCheckoutTimeout");
    expect(within(suspected).getByTestId("getting-started-command")).toHaveTextContent(
      `reliant forge test quarantine add ${idOf("internal/checkout", "TestCheckoutTimeout")} --issue https://github.com/acme/shopfront/issues/231`
    );
    expect(within(suspected).getByRole("button", { name: /Copy reliant forge test quarantine add/ })).toBeInTheDocument();
    const fix = within(suspected).getByTestId("tests-fix-with-agent");
    expect(fix).toBeEnabled();
    expect(fix).toHaveTextContent("Fix this flaky test");

    expect(within(screen.getByTestId("tests-flake-actions-TestWebhookSign/rotated key")).getByTestId("getting-started-command")).toHaveTextContent(
      `reliant forge test quarantine add '${idOf("internal/webhooks", "TestWebhookSign/rotated key")}'`
    );
    // Quarantined already: no command, still the fix slot. Fixed: nothing to do.
    const quarantined = screen.getByTestId("tests-flake-actions-TestInventoryReserve");
    expect(within(quarantined).queryByTestId("getting-started-command")).not.toBeInTheDocument();
    expect(within(quarantined).getByTestId("tests-fix-with-agent")).toBeEnabled();
    expect(screen.queryByTestId("tests-flake-actions-TestEmailDigest")).not.toBeInTheDocument();
  });

  it("Fix this flaky test hands Ask Reliant the test's record and its failure signatures, to send at once", async () => {
    ask.mockClear();
    const { api } = renderFlaky("report");
    const sync = await screen.findByTestId("tests-flake-actions-TestSyncJob");
    fireEvent.click(within(sync).getByTestId("tests-fix-with-agent"));
    await waitFor(() => expect(ask).toHaveBeenCalledTimes(1));
    const [subject, , intent] = ask.mock.calls[0] as unknown as [AskFlakyTestSubject, unknown, string];
    expect(intent).toBe("fix");
    expect(subject).toMatchObject({ kind: "flaky-test", testId: idOf("internal/jobs", "TestSyncJob"), state: "flaky", runs: 412, defaultBranch: "main", issueUrl: "https://github.com/acme/shopfront/issues/212" });
    // The signatures come from the test's own read: the list has none.
    expect(api.calls.some((call) => call.method === "getTest")).toBe(true);
    expect(subject.signatures.length).toBeGreaterThan(0);
    expect(subject.signatures[0].sampleOutput).toContain("TestSyncJob");
  });

  it("keeps host health apart, and lists no test that only failed on the host", async () => {
    renderFlaky("report");
    const health = await screen.findByTestId("tests-flaky-host-health");
    expect(health).toHaveTextContent("Never counted against a test");
    expect(within(health).getByTestId("tests-flaky-host-failures")).toHaveTextContent("5");
    expect(within(health).getByTestId("tests-flaky-infra-groups")).toHaveTextContent("one infrastructure event");
    expect(screen.queryByTestId("tests-flake-row-TestWebhookDeliver")).not.toBeInTheDocument();
    expect(screen.getByTestId("tests-flaky-rule")).toHaveTextContent("Suspected (one confirmed flake) is the state to act on");
    expect(screen.getByTestId("tests-flaky-rule")).toHaveTextContent("The flake rate ranks the list and decides nothing");
  });
});

describe("Tests › Flaky tests: the state filter held in the URL", () => {
  it("reads the URL's state into the request", async () => {
    const { api } = renderFlaky("report", "?state=suspected");
    await screen.findByTestId("tests-flaky-list");
    expect(rowNames()).toEqual(["TestCheckoutTimeout", "TestCartMerge", "TestWebhookSign/rotated key"]);
    expect(flakyRequests(api)).toEqual([{ state: "suspected" }]);
    expect(screen.getByTestId("tests-flaky-filter-suspected")).toHaveAttribute("aria-pressed", "true");
  });

  it("writes a picked state to the URL, and a second click clears it", async () => {
    const { search } = renderFlaky("report", "?env=prod");
    await screen.findByTestId("tests-flaky-list");
    // The default branch's counts, from the summary.
    expect(screen.getByTestId("tests-flaky-filter-flaky")).toHaveTextContent("4");
    fireEvent.click(screen.getByTestId("tests-flaky-filter-quarantined"));
    await waitFor(() => expect(search()).toEqual({ env: "prod", state: "quarantined" }));
    await waitFor(() => expect(rowNames()).toEqual(["TestInventoryReserve", "TestSessionRefresh"]));
    fireEvent.click(screen.getByTestId("tests-flaky-filter-quarantined"));
    await waitFor(() => expect(search()).toEqual({ env: "prod" }));
  });

  it("says when no test is in the state asked for", async () => {
    const { search } = renderFlaky("no-flakes", "?state=fixed");
    const none = await screen.findByTestId("tests-flaky-no-match");
    expect(none).toHaveTextContent("No fixed tests");
    fireEvent.click(within(none).getByTestId("tests-flaky-no-match-clear"));
    await waitFor(() => expect(search()).toEqual({}));
  });
});

describe("Tests › Flaky tests: nothing to show", () => {
  it("says no test flaked when runs exist and none did, with host health at zero", async () => {
    renderFlaky("no-flakes");
    expect(await screen.findByTestId("tests-flaky-none")).toHaveTextContent("No flaky tests");
    await waitFor(() => expect(screen.getByTestId("tests-flaky-host-failures")).toHaveTextContent("0"));
  });

  it("names the commands that record a first run when nothing was ever recorded", async () => {
    renderFlaky("no-runs");
    expect(await screen.findByTestId("tests-flaky-no-runs")).toHaveTextContent("No test runs yet");
  });

  it("says a control plane without the service doesn't store runs yet", async () => {
    renderFlaky("not-configured");
    expect(await screen.findByTestId("tests-error")).toHaveAttribute("data-kind", "not-configured");
  });

  it("names the grant a reader without tests:read lacks", async () => {
    renderFlaky("denied");
    expect(await screen.findByTestId("tests-error")).toHaveAttribute("data-kind", "denied");
  });
});

// ── One test ────────────────────────────────────────────────────────────────

const runHref = (runId: string) => `/forge/p/shopfront/tests/runs/${runId}`;

function renderTest(scenario: TestsScenario, testId: string, { projectId = "proj-shopfront", mode = "chat" }: { projectId?: string | null; mode?: "chat" | "handoff" } = {}) {
  return renderWithApi(
    fixtureTestApi(scenario),
    <AskAgentModeProvider mode={mode}>
      <TestsFlakyTestView project={FIXTURE_PROJECT} projectId={projectId} testId={testId} runHref={runHref} now={FIXTURE_NOW} />
    </AskAgentModeProvider>
  );
}

describe("a test's page", () => {
  it("shows its record: state, the flake rate with its n, its issue, the command and the agent", async () => {
    renderTest("report", idOf("internal/jobs", "TestSyncJob"));
    const record = await screen.findByTestId("tests-test-record");
    expect(record).toHaveTextContent("TestSyncJob");
    expect(record).toHaveTextContent("…/internal/jobs");
    expect(within(record).getByTestId("tests-test-state")).toHaveTextContent("Flaky");
    expect(within(record).getByTestId("tests-test-rate")).toHaveTextContent("1.1% (412 runs)");
    expect(within(record).getByRole("link", { name: /#212/ })).toHaveAttribute("href", "https://github.com/acme/shopfront/issues/212");
    expect(within(record).getByTestId("tests-test-quarantine-command")).toHaveTextContent(`reliant forge test quarantine add ${idOf("internal/jobs", "TestSyncJob")}`);
    expect(within(record).getByTestId("tests-fix-with-agent")).toBeEnabled();
  });

  it("disables Fix this flaky test, saying why, where no agent can start", async () => {
    const id = idOf("internal/jobs", "TestSyncJob");
    const reasonOf = async () => {
      const fix = await screen.findByTestId("tests-fix-with-agent");
      expect(fix).toBeDisabled();
      return screen.getByTestId("tests-fix-with-agent-reason").textContent;
    };

    const noRepo = renderTest("report", id, { projectId: null });
    expect(await reasonOf()).toBe(AGENT_REASON.noProject);
    noRepo.unmount();

    const handoff = renderTest("report", id, { mode: "handoff" });
    expect(await reasonOf()).toBe(AGENT_REASON.handoff);
    handoff.unmount();

    vi.mocked(useProject).mockReturnValue({ is_git_repo: false } as ReturnType<typeof useProject>);
    renderTest("report", id);
    expect(await reasonOf()).toBe(AGENT_REASON.notGit);
    expect(screen.getByTestId("tests-fix-with-agent")).toHaveAccessibleDescription(AGENT_REASON.notGit);
    vi.mocked(useProject).mockReturnValue(undefined);
  });

  it("draws its last 50 runs, oldest first, each a link to the run", async () => {
    renderTest("report", idOf("internal/jobs", "TestSyncJob"));
    const strip = await screen.findByTestId("tests-test-strip");
    expect(strip).toHaveTextContent("Last 50 runs");
    expect(strip).toHaveTextContent("45 passed · 3 failed, then passed on retry · 2 failed");
    const cells = within(strip).getByRole("list", { name: "Outcomes, oldest first" }).querySelectorAll("li");
    expect(cells).toHaveLength(50);
    const newest = cells[cells.length - 1]!;
    expect(newest).toHaveAttribute("data-kind", "retry");
    expect(within(newest as HTMLElement).getByRole("link")).toHaveAttribute("href", runHref(FIXTURE_RUN_IDS.integration));
    expect(within(newest as HTMLElement).getByRole("link")).toHaveAccessibleName("failed, then passed on retry: integration on main 9f3c2a1, 14m ago");
  });

  it("lists its flake events with signal and verdict, host and infra ones shown as never counting", async () => {
    renderTest("report", idOf("internal/jobs", "TestSyncJob"));
    const events = await screen.findByTestId("tests-test-events");
    expect(events).toHaveTextContent("2 host or infrastructure, never counted");
    expect(screen.getByTestId("tests-test-event-0")).toHaveAttribute("data-verdict", "confirmed");
    expect(screen.getByTestId("tests-test-event-0")).toHaveTextContent("Flaky: passed on rerun");
    expect(screen.getByTestId("tests-test-event-0")).toHaveTextContent("retry pass");
    expect(screen.getByTestId("tests-test-event-1")).toHaveTextContent("same-tree split");
    expect(within(screen.getByTestId("tests-test-event-1")).getAllByRole("link")).toHaveLength(2);
    expect(screen.getByTestId("tests-test-event-2")).toHaveAttribute("data-verdict", "host");
    expect(screen.getByTestId("tests-test-event-2")).toHaveTextContent("Host failure: not counted");
    const infra = screen.getByTestId("tests-test-event-4");
    expect(infra).toHaveTextContent("Infra group: not counted");
    expect(infra).toHaveTextContent("infra:pgtest-template-busy:7f3a");
  });

  it("groups its failures by signature, most frequent first, the first sample open", async () => {
    renderTest("report", idOf("internal/jobs", "TestSyncJob"));
    const first = await screen.findByTestId("tests-test-signature-0");
    expect(first).toHaveTextContent("3 failures");
    expect(first).toHaveTextContent("Flaky: passed on rerun");
    expect(screen.getByTestId("tests-test-signature-0-output")).toHaveAttribute("open");
    expect(screen.getByTestId("tests-test-signature-0-output").querySelector("pre")).toHaveTextContent("context deadline exceeded");
    expect(screen.getByTestId("tests-test-signature-1")).toHaveAttribute("data-verdict", "host");
    expect(screen.getByTestId("tests-test-signature-2")).toHaveAttribute("data-verdict", "correlated");
  });

  it("shows a quarantine past its expiry as overdue and still applied, with no command", async () => {
    renderTest("report", idOf("internal/inventory", "TestInventoryReserve"));
    const quarantine = await screen.findByTestId("tests-test-quarantine");
    expect(quarantine).toHaveTextContent("ci/quarantine/go/internal/inventory/TestInventoryReserve.yaml");
    expect(quarantine).toHaveTextContent("overdue");
    expect(quarantine).toHaveTextContent("It is still applied");
    expect(quarantine).toHaveTextContent("Reason: times out under -race");
    expect(screen.queryByTestId("tests-test-quarantine-command")).not.toBeInTheDocument();
  });

  it("opens a test that never flaked as healthy, with its outcomes and nothing to act on", async () => {
    renderTest("report", idOf("internal/billing", "TestRefund/partial"));
    expect(await screen.findByTestId("tests-test-state")).toHaveTextContent("Healthy");
    expect(screen.getByTestId("tests-test-strip")).toHaveTextContent("50 passed");
    expect(screen.getByTestId("tests-test-events")).toHaveTextContent("No flake event in the last 30 days.");
    expect(screen.getByTestId("tests-test-signatures")).toHaveTextContent("No failure recorded in its recent runs.");
    expect(screen.queryByTestId("tests-fix-with-agent")).not.toBeInTheDocument();
  });

  it("says a test this project doesn't have isn't in it", async () => {
    renderTest("report", "go:nowhere:TestNothing");
    expect(await screen.findByTestId("tests-test-error")).toHaveAttribute("data-kind", "not-found");
  });

  it("says a control plane without the service doesn't store runs yet", async () => {
    renderTest("not-configured", idOf("internal/jobs", "TestSyncJob"));
    expect(await screen.findByTestId("tests-test-error")).toHaveAttribute("data-kind", "not-configured");
  });

  it("reads the test named in the route, decoded", async () => {
    const api = fixtureTestApi("report");
    const id = idOf("internal/webhooks", "TestWebhookSign/rotated key");
    renderRoutedPage(api, { path: "/forge/p/$project/tests/flaky/$test", url: `/forge/p/shopfront/tests/flaky/${encodeURIComponent(id)}`, validateSearch: (s) => s, component: TestsFlakyTestPage });
    expect(await screen.findByTestId("tests-test-state")).toHaveTextContent("Suspected");
    expect(api.calls.find((call) => call.method === "getTest")!.req).toEqual({ orgId: null, project: "shopfront", testId: id });
  });
});
