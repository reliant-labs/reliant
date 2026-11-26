// Copyright (c) 2026 Reliant Labs

/**
 * Tests › Flaky tests and a test's page against the fixture TestsService.
 *
 * The list: one line per test, ranked by flake rate (its n in the
 * tooltip, never a verdict), with its state's dot, the reason in plain words,
 * its last 20 runs, a quiet tag for a quarantine ("Overdue" past its expiry)
 * and its issue; the actions in its ⋯ menu (Ask an agent to fix, seeded from
 * the test's own record and left out where no agent can start; the
 * quarantine command to copy; the issue); the counts and a sparkline above,
 * host health in one line below, and no row for a test that only failed on
 * the host. The state tabs are held in the URL. A test's page: its short
 * name as the title, the full id behind a copy button, one row of figures,
 * the run strip, its events in plain words (host and correlated folded
 * behind "Show N not counted"), its failure signatures collapsed. And the
 * empty,
 * unimplemented and denied states of both.
 */

import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";

import { useProject } from "@/hooks/project-queries";
import { forgeConsoleTestsFlakySearchSchema } from "@/routeSchemas";

import type { AskFlakyTestSubject } from "../../Observability/AskReliant/askReliantPrompt";
import { encodeTestIdSegment, flakyTestPath } from "../../Console/paths";
import { AGENT_REASON } from "../testsAgent";

import { FIXTURE_NOW, FIXTURE_PROJECT, fixtureTests, type FixtureTests, type TestsScenario } from "../fixtures";
import { FIXTURE_RUN_IDS } from "../runFlakeFixtures";
import { TestsFlakyPage } from "../TestsFlakyPage";
import { TestsFlakyTestPage, TestsFlakyTestView } from "../TestsFlakyTestPage";
import { renderRoutedPage, renderWithTests } from "./testsTestUtils";

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
      page.kind === "tests-run" ? `/forge/p/shopfront/tests/runs/${page.run}` : `/forge/p/shopfront/tests/flaky/${encodeTestIdSegment(page.test ?? "")}`,
  }),
}));

const MODULE = "github.com/acme/shopfront";
const idOf = (dir: string, name: string) => `go:${MODULE}/${dir}:${name}`;

function renderFlaky(scenario: TestsScenario, query = "") {
  const api = fixtureTests(scenario);
  const utils = renderRoutedPage(api, {
    path: "/forge/p/$project/tests/flaky",
    url: `/forge/p/shopfront/tests/flaky${query}`,
    validateSearch: (search) => forgeConsoleTestsFlakySearchSchema.parse(search),
    component: TestsFlakyPage,
  });
  return { ...utils, api };
}

const flakyRequests = (api: FixtureTests) => api.calls.filter((call) => call.method === "listFlakyTests").map((call) => (call.req as { filters: unknown }).filters);
const rowNames = () => screen.getAllByTestId(/^tests-flake-row-/).map((row) => row.getAttribute("data-testid")!.replace("tests-flake-row-", ""));

/** Opens a row's ⋯ menu and returns its items' names. */
async function openMenu(name: string): Promise<string[]> {
  fireEvent.click(await screen.findByRole("button", { name: `Actions for ${name}` }));
  const menu = await screen.findByRole("menu");
  return within(menu)
    .getAllByRole("menuitem")
    .map((item) => item.textContent?.trim() ?? "");
}

describe("Tests › Flaky tests: the full list", () => {
  it("ranks by flake rate, one line per test, with its state, plain-language reason and the row as the link", async () => {
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
    // The rate is a ranking: its n and what it means are in its tooltip, never beside it as a verdict.
    expect(screen.getByTestId("tests-flake-rate-TestSyncJob")).toHaveTextContent("1.1%");
    expect(screen.getByTestId("tests-flake-rate-TestSyncJob")).toHaveAttribute("title", expect.stringContaining("1.1% (412 runs)"));
    expect(screen.getByTestId("tests-flake-rate-TestSyncJob")).toHaveAttribute("title", expect.stringContaining("A ranking, never a verdict"));
    // Plain words, not the signal's jargon.
    expect(sync).toHaveTextContent("internal/jobs · Passed and failed on the same commit");
    expect(sync).not.toHaveTextContent("same-tree split");
    expect(screen.getByTestId("tests-flake-row-TestStockSync")).toHaveTextContent("Failed, then passed on retry");
    // No command box and no button in a row: the actions are in its menu.
    expect(within(sync).queryByTestId("getting-started-command")).not.toBeInTheDocument();
    expect(within(sync).queryByTestId("tests-fix-with-agent")).not.toBeInTheDocument();
    // The routed page reads the wall clock; never "0s ago".
    expect(sync).not.toHaveTextContent(/\b0s ago/);
    // The name is the row's link, stretched over it, named with the state and rate.
    const link = within(sync).getByRole("link", { name: /^TestSyncJob: flaky, flake rate 1\.1% \(412 runs\)/ });
    expect(link).toHaveAttribute("href", `/forge/p/shopfront/tests/flaky/${encodeTestIdSegment(idOf("internal/jobs", "TestSyncJob"))}`);
    expect(within(sync).getByTestId("tests-flake-issue-TestSyncJob")).toHaveAttribute("href", "https://github.com/acme/shopfront/issues/212");
    expect(screen.getByTestId("tests-flake-rate-TestWebhookSign/rotated key")).toHaveAttribute("title", expect.stringContaining("0.21% (22 runs)"));
    expect(screen.getByTestId("tests-flake-row-TestCheckoutTimeout")).toHaveAttribute("data-state", "suspected");
  });

  it("draws each row's last 20 runs from the test's own read", async () => {
    const { api } = renderFlaky("report");
    await screen.findByTestId("tests-flaky-list");
    const sync = screen.getByTestId("tests-flake-row-TestSyncJob");
    const strip = await within(sync).findByTestId("tests-mini-strip");
    expect(strip.children).toHaveLength(20);
    expect(strip).toHaveAccessibleName(/^Last 20 runs: \d+ passed on retry/);
    expect(api.calls.filter((call) => call.method === "getTest").length).toBeGreaterThanOrEqual(10);
  });

  it("marks a quarantine with a quiet tag, and one past its expiry as overdue", async () => {
    renderFlaky("report");
    const overdue = await screen.findByTestId("tests-flake-quarantine-TestInventoryReserve");
    expect(overdue).toHaveTextContent("Overdue");
    const current = screen.getByTestId("tests-flake-quarantine-TestSessionRefresh");
    expect(current).toHaveTextContent("Quarantined");
    expect(current).not.toHaveTextContent("Overdue");
    expect(screen.getByTestId("tests-flaky-count-overdue")).toHaveTextContent("1 overdue");
  });

  it("puts the actions in each row's menu: fix, copy the quarantine command, open the issue", async () => {
    renderFlaky("report");
    await screen.findByTestId("tests-flaky-list");
    expect(await openMenu("TestCheckoutTimeout")).toEqual(["Ask an agent to fix", "Copy quarantine command", "Open issue #231", "Copy test ID"]);
    fireEvent.keyDown(document.activeElement!, { key: "Escape" });
    await waitFor(() => expect(screen.queryByRole("menu")).not.toBeInTheDocument());
    // Quarantined already: no command. Fixed: nothing to fix.
    expect(await openMenu("TestInventoryReserve")).toEqual(["Ask an agent to fix", "Open issue #198", "Copy test ID"]);
    fireEvent.keyDown(document.activeElement!, { key: "Escape" });
    await waitFor(() => expect(screen.queryByRole("menu")).not.toBeInTheDocument());
    expect(await openMenu("TestEmailDigest")).toEqual(["Open issue #176", "Copy test ID"]);
  });

  it("copies the quarantine command, quoted when the id needs it", async () => {
    const writeText = vi.fn(async () => undefined);
    Object.assign(navigator, { clipboard: { writeText } });
    renderFlaky("report");
    await screen.findByTestId("tests-flaky-list");
    await openMenu("TestWebhookSign/rotated key");
    fireEvent.click(screen.getByTestId("tests-flake-menu-quarantine"));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(`reliant forge test quarantine add '${idOf("internal/webhooks", "TestWebhookSign/rotated key")}'`));
  });

  it("leaves Ask an agent to fix out of the menu where no agent can start", async () => {
    vi.mocked(useProject).mockReturnValue({ is_git_repo: false } as ReturnType<typeof useProject>);
    try {
      renderFlaky("report");
      await screen.findByTestId("tests-flaky-list");
      expect(await openMenu("TestCheckoutTimeout")).toEqual(["Copy quarantine command", "Open issue #231", "Copy test ID"]);
    } finally {
      vi.mocked(useProject).mockReturnValue(undefined);
    }
  });

  it("Ask an agent to fix hands Ask Reliant the test's record and its failure signatures, to send at once", async () => {
    ask.mockClear();
    const { api } = renderFlaky("report");
    await screen.findByTestId("tests-flaky-list");
    await openMenu("TestSyncJob");
    fireEvent.click(screen.getByTestId("tests-flake-menu-fix"));
    await waitFor(() => expect(ask).toHaveBeenCalledTimes(1));
    const [subject, , intent] = ask.mock.calls[0] as unknown as [AskFlakyTestSubject, unknown, string];
    expect(intent).toBe("fix");
    expect(subject).toMatchObject({ kind: "flaky-test", testId: idOf("internal/jobs", "TestSyncJob"), state: "flaky", runs: 412, defaultBranch: "main", issueUrl: "https://github.com/acme/shopfront/issues/212" });
    // The signatures come from the test's own read: the list has none.
    expect(api.calls.some((call) => call.method === "getTest")).toBe(true);
    expect(subject.signatures.length).toBeGreaterThan(0);
    expect(subject.signatures[0].sampleOutput).toContain("TestSyncJob");
  });

  it("leads with the counts and a 30-day sparkline of counted flake events", async () => {
    renderFlaky("report");
    const summary = await screen.findByTestId("tests-flaky-summary");
    await waitFor(() => expect(within(summary).getByTestId("tests-flaky-count-flaky")).toHaveTextContent("4 flaky"));
    expect(within(summary).getByTestId("tests-flaky-count-suspected")).toHaveTextContent("3 suspected");
    expect(within(summary).getByTestId("tests-flaky-count-quarantined")).toHaveTextContent("2 quarantined");
    const spark = await within(summary).findByTestId("tests-flaky-sparkline");
    // Host and infrastructure events never count, so TestSyncJob adds 3, not 5.
    expect(within(spark).getByRole("img")).toHaveAccessibleName(/^Flake events per day over 30 days: \d+ in all$/);
  });

  it("keeps host health to one line under the list, and lists no test that only failed on the host", async () => {
    renderFlaky("report");
    const health = await screen.findByTestId("tests-flaky-host-health");
    expect(health).toHaveTextContent("Host and infrastructure failures never count against a test.");
    await waitFor(() => expect(within(health).getByTestId("tests-flaky-host-failures")).toHaveTextContent("5 in the last 30 days."));
    expect(screen.queryByTestId("tests-flake-row-TestWebhookDeliver")).not.toBeInTheDocument();
    // The policy is behind the "?", not a paragraph on the page.
    expect(screen.getByTestId("tests-flaky-rule")).toHaveAccessibleName("How flaky tests are judged");
  });
});

describe("Tests › Flaky tests: the state tabs held in the URL", () => {
  it("reads the URL's state into the request", async () => {
    const { api } = renderFlaky("report", "?state=suspected");
    await screen.findByTestId("tests-flaky-list");
    expect(rowNames()).toEqual(["TestCheckoutTimeout", "TestCartMerge", "TestWebhookSign/rotated key"]);
    expect(flakyRequests(api)).toContainEqual({ state: "suspected" });
    expect(screen.getByRole("tab", { name: /Suspected/ })).toHaveAttribute("aria-selected", "true");
  });

  it("writes a picked tab to the URL, and All clears it", async () => {
    const { search } = renderFlaky("report", "?env=prod");
    await screen.findByTestId("tests-flaky-list");
    // The default branch's counts, from the summary.
    await waitFor(() => expect(screen.getByRole("tab", { name: /Flaky/ })).toHaveTextContent("4"));
    fireEvent.click(screen.getByRole("tab", { name: /Quarantined/ }));
    await waitFor(() => expect(search()).toEqual({ env: "prod", state: "quarantined" }));
    await waitFor(() => expect(rowNames()).toEqual(["TestInventoryReserve", "TestSessionRefresh"]));
    fireEvent.click(screen.getByRole("tab", { name: /All/ }));
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
    await waitFor(() => expect(screen.getByTestId("tests-flaky-host-failures")).toHaveTextContent("None in the last 30 days."));
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

function renderTest(scenario: TestsScenario, testId: string, { projectId = "proj-shopfront" }: { projectId?: string | null } = {}) {
  return renderWithTests(fixtureTests(scenario), <TestsFlakyTestView project={FIXTURE_PROJECT} projectId={projectId} testId={testId} runHref={runHref} now={FIXTURE_NOW} />);
}

describe("a test's page", () => {
  it("titles it by its short name, with package, file:line and state under it, and the full id behind a copy button", async () => {
    renderTest("report", idOf("internal/jobs", "TestSyncJob"));
    await screen.findByTestId("tests-test-state");
    const header = screen.getByTestId("tests-test-header");
    expect(within(header).getByRole("heading", { level: 1 })).toHaveTextContent(/^TestSyncJob$/);
    expect(header).toHaveTextContent("internal/jobs");
    expect(within(header).getByTestId("tests-test-place")).toHaveTextContent("internal/jobs/jobs_test.go:88");
    expect(within(header).getByTestId("tests-test-state")).toHaveTextContent("Flaky");
    expect(within(header).getByTestId("tests-test-copy-id")).toHaveAccessibleName(`Copy test ID ${idOf("internal/jobs", "TestSyncJob")}`);
    expect(within(header).getByTestId("tests-fix-with-agent")).toBeEnabled();
    expect(within(header).getByTestId("tests-fix-with-agent")).toHaveTextContent("Ask an agent to fix");
  });

  it("shows one row of figures: the flake rate with its n, flakes, first seen, last flaked", async () => {
    renderTest("report", idOf("internal/jobs", "TestSyncJob"));
    expect(await screen.findByTestId("tests-test-rate")).toHaveTextContent("1.1%of 412 runs");
    expect(screen.getByTestId("tests-test-rate")).toHaveAttribute("title", expect.stringContaining("A ranking, never a verdict"));
    expect(screen.getByTestId("tests-test-events-count")).toHaveTextContent("3");
  });

  it("keeps the quarantine command and the issue in the ⋯ menu", async () => {
    renderTest("report", idOf("internal/jobs", "TestSyncJob"));
    fireEvent.click(await screen.findByRole("button", { name: "More actions for TestSyncJob" }));
    const menu = await screen.findByRole("menu");
    expect(within(menu).getAllByRole("menuitem").map((item) => item.textContent)).toEqual(["Copy quarantine command", "Open issue #212", "Copy test ID"]);
  });

  it("disables Ask an agent to fix, its reason in the tooltip and description, where no agent can start in Reliant", async () => {
    const id = idOf("internal/jobs", "TestSyncJob");
    const reasonOf = async () => {
      const fix = await screen.findByTestId("tests-fix-with-agent");
      expect(fix).toBeDisabled();
      return screen.getByTestId("tests-fix-with-agent-reason").textContent;
    };

    const noRepo = renderTest("report", id, { projectId: null });
    expect(await reasonOf()).toBe(AGENT_REASON.noProject);
    noRepo.unmount();

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
    expect(strip).toHaveTextContent("3 passed on retry · 2 failed · 45 passed");
    const cells = within(strip).getByRole("list", { name: "Outcomes, oldest first" }).querySelectorAll("li");
    expect(cells).toHaveLength(50);
    const newest = cells[cells.length - 1]!;
    expect(newest).toHaveAttribute("data-kind", "retry");
    expect(within(newest as HTMLElement).getByRole("link")).toHaveAttribute("href", runHref(FIXTURE_RUN_IDS.integration));
    expect(within(newest as HTMLElement).getByRole("link")).toHaveAccessibleName("failed, then passed on retry: integration on main 9f3c2a1, 14m ago");
  });

  it("lists its flake events in plain words, host and infra ones folded behind Show N not counted", async () => {
    renderTest("report", idOf("internal/jobs", "TestSyncJob"));
    const events = await screen.findByTestId("tests-test-events");
    expect(screen.getByTestId("tests-test-event-0")).toHaveAttribute("data-verdict", "confirmed");
    expect(screen.getByTestId("tests-test-event-0")).toHaveTextContent("Failed, then passed on retry");
    expect(screen.getByTestId("tests-test-event-1")).toHaveTextContent("Passed and failed on the same commit");
    expect(within(screen.getByTestId("tests-test-event-1")).getAllByRole("link")).toHaveLength(2);
    expect(screen.queryByTestId("tests-test-event-2")).not.toBeInTheDocument();
    expect(events).not.toHaveTextContent("retry pass");

    fireEvent.click(within(events).getByTestId("tests-test-events-toggle"));
    expect(within(events).getByTestId("tests-test-events-toggle")).toHaveTextContent("Hide 2 not counted");
    expect(screen.getByTestId("tests-test-event-2")).toHaveAttribute("data-verdict", "host");
    expect(screen.getByTestId("tests-test-event-2")).toHaveTextContent("Host problem (network, disk, port in use): not counted");
    const infra = screen.getByTestId("tests-test-event-4");
    expect(infra).toHaveTextContent("Part of a wider infrastructure failure: not counted");
    expect(infra).toHaveTextContent("infra:pgtest-template-busy:7f3a");
  });

  it("groups its failures by signature, collapsed, each with its first line of output", async () => {
    renderTest("report", idOf("internal/jobs", "TestSyncJob"));
    const first = await screen.findByTestId("tests-test-signature-0");
    expect(first).toHaveTextContent("3 failures");
    expect(first).toHaveTextContent("sync_test.go:88: job 7 did not finish within 2s: context deadline exceeded");
    expect(first.querySelector("details")).not.toHaveAttribute("open");
    expect(screen.getByTestId("tests-test-signature-0-output").querySelector("pre")).toHaveTextContent("context deadline exceeded");
    expect(screen.getByTestId("tests-test-signature-1")).toHaveAttribute("data-verdict", "host");
    expect(screen.getByTestId("tests-test-signature-1")).toHaveTextContent("Host problem");
    expect(screen.getByTestId("tests-test-signature-2")).toHaveAttribute("data-verdict", "correlated");
  });


  it("shows a quarantine past its expiry as overdue and still applied, with no command", async () => {
    renderTest("report", idOf("internal/inventory", "TestInventoryReserve"));
    const quarantine = await screen.findByTestId("tests-test-quarantine");
    expect(quarantine).toHaveTextContent("ci/quarantine/go/internal/inventory/TestInventoryReserve.yaml");
    expect(quarantine).toHaveTextContent("is still applied");
    expect(screen.getByTestId("tests-test-header")).toHaveTextContent("Quarantine overdue");
    expect(quarantine).toHaveTextContent("times out under -race");
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

  it("reads the test named in the route, from its base64url segment", async () => {
    const api = fixtureTests("report");
    const id = idOf("internal/webhooks", "TestWebhookSign/rotated key");
    const url = flakyTestPath("shopfront", id);
    expect(url).not.toMatch(/%2F/i);
    renderRoutedPage(api, { path: "/forge/p/$project/tests/flaky/$test", url, validateSearch: (s) => s, component: TestsFlakyTestPage });
    expect(await screen.findByTestId("tests-test-state")).toHaveTextContent("Suspected");
    expect(api.calls.find((call) => call.method === "getTest")!.req).toEqual({ orgId: null, project: "shopfront", testId: id });
    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent(/^TestWebhookSign\/rotated key$/);
    expect(screen.getByTestId("tests-test-copy-id")).toHaveAccessibleName(`Copy test ID ${id}`);
  });

  it("still reads a link in the old percent-encoded form", async () => {
    const api = fixtureTests("report");
    const id = idOf("internal/webhooks", "TestWebhookSign/rotated key");
    renderRoutedPage(api, { path: "/forge/p/$project/tests/flaky/$test", url: `/forge/p/shopfront/tests/flaky/${encodeURIComponent(id)}`, validateSearch: (s) => s, component: TestsFlakyTestPage });
    expect(await screen.findByTestId("tests-test-state")).toHaveTextContent("Suspected");
    expect(api.calls.find((call) => call.method === "getTest")!.req).toEqual({ orgId: null, project: "shopfront", testId: id });
  });
});
