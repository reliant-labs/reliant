// Copyright (c) 2026 Reliant Labs

/**
 * The project Overview's Tests surfaces and the nav badge against the
 * fixture TestsService: the Tests card (the report, an empty record, a
 * failed read, and no card at all without the service or the grant), the
 * Tests rows of "Needs attention" (suspected when the project acts at
 * suspected, newly flaky, an overdue
 * quarantine, coverage that fell), and the count the nav draws.
 */

import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import { screen, within } from "@testing-library/react";
import { Code, ConnectError } from "@connectrpc/connect";

import { useTestSummary } from "@/hooks/test-queries";
import { flakeOf, summaryOf } from "@/services/controlPlane/tests/wire";

import { FIXTURE_NOW, FIXTURE_PROJECT, fixtureSummaryWire, fixtureTests, type TestsScenario } from "../fixtures";
import { fixtureFlakes } from "../runFlakeFixtures";
import { OverviewTestsCard, TestsAttentionRows, testsAttentionHrefs } from "../surfaces/OverviewTests";
import { useTestsNavCount } from "../surfaces/useTestsNavCount";
import { COVERAGE_DROP_ATTENTION_POINTS, testsAttentionCount, testsAttentionItems } from "../surfaces/surfacesModel";
import { renderWithTests } from "./testsTestUtils";

// The Tests card draws its coverage line with UPlotChart; uPlot needs a canvas jsdom lacks.
vi.mock("uplot", async () => (await import("../../Observability/__tests__/obsMocks")).obsMocks.uplot());

vi.mock("@/components/forge-ui/link", () => ({
  default: ({ href, children, ...rest }: { href: string; children: ReactNode }) => (
    <a href={href} {...rest}>
      {children}
    </a>
  ),
}));

const frame = (card: ReactNode) => <div data-testid="frame">{card}</div>;

/** The summary's own read (the same cache entry), drawn beside a surface so a test can wait for it to settle. */
function Settled() {
  const summary = useTestSummary(FIXTURE_PROJECT);
  return summary.isError || summary.isSuccess ? <i data-testid="settled" /> : null;
}
const card = (scenario: TestsScenario) => renderWithTests(fixtureTests(scenario), <OverviewTestsCard project={FIXTURE_PROJECT} frame={frame} />);
const rows = (scenario: TestsScenario) => renderWithTests(fixtureTests(scenario), <TestsAttentionRows project={FIXTURE_PROJECT} now={FIXTURE_NOW} />);

describe("the Overview's Tests card", () => {
  it("shows the default branch's coverage, pass rate and flaky count, linking into Tests", async () => {
    card("report");
    expect(await screen.findByTestId("overview-tests-ready")).toBeInTheDocument();
    expect(screen.getByTestId("overview-tests-coverage")).toHaveTextContent("74.2%");
    expect(screen.getByTestId("overview-tests-delta")).toHaveTextContent("+0.6");
    expect(screen.getByTestId("overview-tests-pass-rate")).toHaveTextContent("98.7%");
    const flaky = screen.getByTestId("overview-tests-flaky");
    expect(flaky).toHaveTextContent("4");
    expect(flaky).toHaveTextContent("3 suspected · 2 quarantined");
    expect(within(flaky).getByRole("link")).toHaveAttribute("href", `/forge/p/${FIXTURE_PROJECT}/tests/flaky`);
    expect(screen.getByTestId("overview-tests-link")).toHaveAttribute("href", `/forge/p/${FIXTURE_PROJECT}/tests`);
  });

  it("says so when nothing was recorded, with the command that records a run", async () => {
    card("no-runs");
    expect(await screen.findByTestId("overview-tests-empty")).toHaveTextContent("reliant forge test report --upload");
  });

  it.each(["not-configured", "denied"] as const)("renders nothing, frame included, when the control plane answers %s", async (scenario) => {
    renderWithTests(
      fixtureTests(scenario),
      <>
        <OverviewTestsCard project={FIXTURE_PROJECT} frame={frame} />
        <Settled />
      </>
    );
    await screen.findByTestId("settled");
    expect(screen.queryByTestId("frame")).not.toBeInTheDocument();
  });

  it("names a failed read rather than hiding it", async () => {
    // A refused page token: not retried (a transport blip would be), and not a state that hides the card.
    const api = fixtureTests("report");
    api.handlers.getTestSummary = () => {
      throw new ConnectError("bad token", Code.InvalidArgument);
    };
    renderWithTests(api, <OverviewTestsCard project={FIXTURE_PROJECT} frame={frame} />);
    expect(await screen.findByTestId("overview-tests-error")).toBeInTheDocument();
  });
});

describe("Needs attention's Tests rows", () => {
  it("lists newly flaky tests, an overdue quarantine and a package whose coverage fell; not suspected ones by default", async () => {
    rows("report");
    const block = await screen.findByTestId("overview-attention-tests");
    // The flaky list is its own read: wait for its rows before saying what is absent.
    expect(await within(block).findByTestId("attention-tests/flaky-recent")).toHaveTextContent("4 flaky tests flaked this week");
    // The customer default acts at flaky: a suspected test is not its work yet.
    expect(within(block).queryByTestId("attention-tests/suspected")).not.toBeInTheDocument();
    expect(within(block).getByText("TestInventoryReserve").closest("li")).toHaveTextContent("quarantine overdue");
    expect(within(block).getByTestId("attention-tests/coverage/internal/billing")).toBeInTheDocument();
  });

  it("lists suspected tests too when the project acts at suspected", async () => {
    rows("acts-on-suspected");
    const block = await screen.findByTestId("overview-attention-tests");
    expect(block).toHaveTextContent("main, every environment");
    const suspected = await within(block).findByTestId("attention-tests/suspected");
    expect(suspected).toHaveTextContent("3 tests suspected flaky");
    expect(suspected).toHaveTextContent("newest TestCheckoutTimeout, 3h ago");
    expect(within(suspected).getByRole("link")).toHaveAttribute("href", `/forge/p/${FIXTURE_PROJECT}/tests/flaky?state=suspected`);
    expect(within(block).getByTestId("attention-tests/flaky-recent")).toHaveTextContent("4 flaky tests flaked this week");
    const quarantine = within(block).getByText("TestInventoryReserve").closest("li")!;
    expect(quarantine).toHaveTextContent("quarantine overdue");
    expect(quarantine).toHaveTextContent("expired 2d ago");
    const billing = within(block).getByTestId("attention-tests/coverage/internal/billing");
    expect(billing).toHaveTextContent("internal/billing coverage fell 8.0 points");
    expect(within(billing).getByRole("link").getAttribute("href")).toContain("path=internal%2Fbilling");
    // main itself rose 0.6 on its base report: nothing to say.
    expect(within(block).queryByTestId("attention-tests/coverage")).not.toBeInTheDocument();
  });

  it("with no flaky test, keeps only the coverage that fell", async () => {
    rows("no-flakes");
    const block = await screen.findByTestId("overview-attention-tests");
    expect(within(block).getAllByRole("listitem")).toHaveLength(1);
    expect(within(block).getByTestId("attention-tests/coverage/internal/billing")).toBeInTheDocument();
  });

  it.each(["not-configured", "denied", "no-runs"] as const)("adds nothing when the control plane answers %s", async (scenario) => {
    renderWithTests(
      fixtureTests(scenario),
      <>
        <TestsAttentionRows project={FIXTURE_PROJECT} now={FIXTURE_NOW} />
        <Settled />
      </>
    );
    await screen.findByTestId("settled");
    expect(screen.queryByTestId("overview-attention-tests")).not.toBeInTheDocument();
  });
});

describe("testsAttentionItems", () => {
  const hrefs = testsAttentionHrefs(FIXTURE_PROJECT);
  const summary = summaryOf(fixtureSummaryWire("report"));
  const flakes = fixtureFlakes().map(flakeOf);

  it("says the branch's coverage fell once it fell the named number of points", () => {
    const latest = summary.latest!;
    const at = (delta: number) =>
      testsAttentionItems({ summary: { ...summary, lowCoverage: [], latest: { ...latest, coverage: { ...latest.coverage!, deltaPercent: delta } } }, flakes: [], now: FIXTURE_NOW, hrefs });
    expect(at(-(COVERAGE_DROP_ATTENTION_POINTS - 0.1))).toEqual([]);
    const fell = at(-COVERAGE_DROP_ATTENTION_POINTS - 0.4).find((item) => item.key === "tests/coverage");
    expect(fell).toMatchObject({ subject: "Coverage on main", what: "fell 2.4 points", detail: "74.2% at 9f3c2a1" });
  });

  it("says 'at least' when the flaky list is longer than its first page", () => {
    const items = testsAttentionItems({ summary, flakes, flakesMore: true, now: FIXTURE_NOW, hrefs });
    expect(items.find((item) => item.key === "tests/flaky-recent")?.subject).toBe("at least 4 flaky tests");
  });
});

describe("the nav badge's count", () => {
  it("is flaky only by default, and suspected plus flaky when the project acts at suspected; never quarantined", () => {
    expect(testsAttentionCount({ suspected: 3, flaky: 4 }, "flaky")).toBe(4);
    expect(testsAttentionCount({ suspected: 3, flaky: 4 }, "suspected")).toBe(7);
    expect(testsAttentionCount({ suspected: 0, flaky: 0 }, "suspected")).toBe(0);
    expect(testsAttentionCount(undefined, "suspected")).toBe(0);
  });

  it.each([
    ["report", "4"],
    ["acts-on-suspected", "7"],
    ["not-configured", "0"],
  ] as const)("is what useTestsNavCount hands the nav in %s", async (scenario, expected) => {
    function Probe() {
      const summary = useTestSummary(FIXTURE_PROJECT);
      const count = useTestsNavCount(FIXTURE_PROJECT);
      return summary.isSuccess || summary.isError ? <i data-testid="count" data-count={count} /> : null;
    }
    renderWithTests(fixtureTests(scenario), <Probe />);
    expect(await screen.findByTestId("count")).toHaveAttribute("data-count", expected);
  });

  it("reads the policy from the summary, flaky when the server does not say", () => {
    expect(summaryOf(fixtureSummaryWire("report")).flakePolicy).toBe("flaky");
    expect(summaryOf(fixtureSummaryWire("acts-on-suspected")).flakePolicy).toBe("suspected");
  });
});
