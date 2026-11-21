// Copyright (c) 2026 Reliant Labs

/**
 * Tests › Coverage against the fixture TestsService and the fixture GitHub:
 * the tree and its drill-down, a file's annotated source, each no-source
 * state (numbers kept, one line of why, nothing highlighted), and the URL's
 * selectors.
 */

import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

import { forgeConsoleTestsCoverageSearchSchema } from "@/routeSchemas";
import { CoverageSourceProvider } from "@/services/coverageSource/CoverageSourceContext";
import { fetchCoverageSource, type FetchCoverageSource } from "@/services/coverageSource/coverageSource";
import { TestApiProvider, type TestApi } from "@/services/controlPlane/tests/TestApiContext";

import { FIXTURE_BASE, FIXTURE_INVOICE_PATH, FIXTURE_REFUND_BLOB, FIXTURE_REFUND_PATH, FIXTURE_TAX_PATH, fixtureCoverageSource, type CoverageSourceScenario } from "../coverageFixtures";
import type { CoverageSearch } from "../coverageModel";
import { FIXTURE_COMMIT, FIXTURE_PROJECT, fixtureSummaryResponse, fixtureTestApi } from "../fixtures";
import { TestsCoverageView } from "../TestsCoveragePage";

vi.mock("@/components/forge-ui/link", () => ({
  default: ({ href, children, ...rest }: { href: string; children: ReactNode }) => (
    <a href={href} {...rest}>
      {children}
    </a>
  ),
}));

/** The page's href for a search, as the console builds it: the keys that are set, in a fixed order. */
function hrefFor(search: CoverageSearch): string {
  const params = new URLSearchParams();
  for (const key of ["path", "commit", "lane", "base"] as const) if (search[key]) params.set(key, search[key]!);
  const query = params.toString();
  return `/forge/p/shopfront/tests/coverage${query ? `?${query}` : ""}`;
}

function renderCoverage(search: CoverageSearch, { api = fixtureTestApi(), source = fixtureCoverageSource(), environmentId }: { api?: TestApi; source?: FetchCoverageSource; environmentId?: string } = {}) {
  const onSelect = vi.fn();
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <TestApiProvider api={api}>
        <CoverageSourceProvider fetch={source}>
          <TestsCoverageView scope={{ project: FIXTURE_PROJECT, environmentId }} search={search} hrefFor={hrefFor} onSelect={onSelect} />
        </CoverageSourceProvider>
      </TestApiProvider>
    </QueryClientProvider>
  );
  return { onSelect };
}

const rowPaths = () => screen.getAllByTestId("tests-coverage-row").map((row) => row.getAttribute("data-path"));
const rowOf = (path: string) => screen.getAllByTestId("tests-coverage-row").find((row) => row.getAttribute("data-path") === path)!;

describe("Coverage: the tree", () => {
  it("lists the root's packages and directories with statements, combined and per-lane figures", async () => {
    renderCoverage({});
    await screen.findAllByTestId("tests-coverage-row");
    expect(rowPaths()).toEqual(["cmd/shopfront", "frontends/web/src", "internal/billing", "internal/catalog", "internal/checkout", "internal/webhooks"]);
    const billing = rowOf("internal/billing");
    expect(billing).toHaveTextContent("2.3k");
    expect(billing).toHaveTextContent("58.0%");
    expect(billing).toHaveTextContent("51.2%");
    expect(billing).toHaveTextContent("30.1%");
    expect(billing).toHaveTextContent("44.0%");
    const headers = screen.getAllByRole("columnheader").map((th) => th.textContent);
    expect(headers).toEqual(["Name", "Statements", "Combined", "unit", "integration", "e2e"]);
    expect(screen.getByTestId("tests-coverage-totals")).toHaveTextContent("9f3c2a1 · 74.2% combined · unit 61.4 · integration 38.0 · e2e 52.7 · 48,210 statements");
  });

  it("a row drills by path, keeping the selectors; a lane with no figure is a dash, never 0%", async () => {
    renderCoverage({ lane: undefined, base: FIXTURE_BASE });
    await screen.findAllByTestId("tests-coverage-row");
    expect(within(rowOf("internal/billing")).getByRole("link")).toHaveAttribute("href", `/forge/p/shopfront/tests/coverage?path=internal%2Fbilling&base=${FIXTURE_BASE}`);
    // cmd/shopfront has no integration figure.
    const cells = within(rowOf("cmd/shopfront")).getAllByRole("cell");
    expect(cells[4]).toHaveTextContent("—");
  });

  it("inside a package: its files, named under it, with diff % against the base", async () => {
    renderCoverage({ path: "internal/billing", base: FIXTURE_BASE });
    await screen.findAllByTestId("tests-coverage-row");
    expect(rowPaths()).toEqual([FIXTURE_INVOICE_PATH, FIXTURE_REFUND_PATH, FIXTURE_TAX_PATH]);
    const refund = rowOf(FIXTURE_REFUND_PATH);
    expect(refund).toHaveAttribute("data-kind", "file");
    expect(within(refund).getByRole("link")).toHaveTextContent("refund.go");
    expect(refund).toHaveTextContent("changed");
    expect(refund).toHaveTextContent("66.7% (2/3)");
    expect(screen.getAllByRole("columnheader").at(-1)).toHaveTextContent("Diff");
    expect(screen.getByTestId("tests-coverage-totals")).toHaveTextContent("diff 2/3 lines");
  });

  it("the breadcrumb walks back up, and the Raise coverage slot names the package and is disabled", async () => {
    renderCoverage({ path: "internal/billing" });
    const crumbs = await screen.findByTestId("tests-coverage-crumbs");
    expect(within(crumbs).getByRole("link", { name: "All code" })).toHaveAttribute("href", "/forge/p/shopfront/tests/coverage");
    expect(within(crumbs).getByRole("link", { name: "internal" })).toHaveAttribute("href", "/forge/p/shopfront/tests/coverage?path=internal");
    expect(within(crumbs).getByText("billing")).toHaveAttribute("aria-current", "page");
    const raise = screen.getByTestId("tests-coverage-raise");
    expect(within(raise).getByRole("button")).toHaveTextContent("Raise coverage of internal/billing");
    expect(within(raise).getByRole("button")).toBeDisabled();
    expect(raise).toHaveTextContent("Coming soon");
  });

  it("one lane's lens shows that lane alone", async () => {
    const api = fixtureTestApi();
    renderCoverage({ lane: "e2e" }, { api });
    await screen.findAllByTestId("tests-coverage-row");
    expect(screen.getAllByRole("columnheader").map((th) => th.textContent)).toEqual(["Name", "Statements", "e2e only"]);
    expect(rowOf("internal/billing")).toHaveTextContent("44.0%");
    expect(screen.getByTestId("tests-coverage-totals")).toHaveTextContent("52.7% e2e only");
    expect(api.calls.find((call) => call.method === "getCoverage")?.req).toMatchObject({ lane: "e2e", path: "" });
  });
});

describe("Coverage: a file with its source", () => {
  it("draws the verified source with each line's tone and the hits per lane in the gutter", async () => {
    const source = fixtureCoverageSource("github");
    renderCoverage({ path: FIXTURE_REFUND_PATH, base: FIXTURE_BASE }, { source });
    const view = await screen.findByTestId("tests-coverage-source");
    expect(source.calls).toEqual([{ project: FIXTURE_PROJECT, blobId: FIXTURE_REFUND_BLOB }]);
    expect(screen.getByTestId("tests-coverage-file")).toHaveAttribute("data-source", "github");
    const line = (n: number) => view.querySelector(`[data-line="${n}"]`)!;
    expect(line(15)).toHaveTextContent("func Refund(ctx context.Context, o Order) error {");
    expect(line(16)).toHaveAttribute("data-tone", "covered");
    expect(line(17)).toHaveAttribute("data-tone", "uncovered");
    expect(line(19)).toHaveAttribute("data-tone", "partial");
    expect(line(15)).not.toHaveAttribute("data-tone");
    // Hits per lane, in lane order: unit, integration, e2e.
    expect([...line(16).querySelectorAll("[data-lane]")].map((cell) => cell.textContent)).toEqual(["12", "0", "3"]);
    expect([...line(15).querySelectorAll("[data-lane]")].map((cell) => cell.textContent)).toEqual(["", "", ""]);
    // Changed against the base.
    expect(within(line(17) as HTMLElement).getByLabelText("changed")).toBeInTheDocument();
    expect(within(line(16) as HTMLElement).queryByLabelText("changed")).not.toBeInTheDocument();
    expect(view.querySelectorAll("[data-line]")).toHaveLength(36);
    expect(screen.getByTestId("tests-coverage-file-totals")).toHaveTextContent("75.0% combined · unit 75.0 · integration 25.0 · e2e 41.7 · diff 2/3 lines · 12 statements");
  });

  it("through one lane's lens a line that lane missed is uncovered, whatever the others did", async () => {
    renderCoverage({ path: FIXTURE_REFUND_PATH, lane: "integration" });
    const view = await screen.findByTestId("tests-coverage-source");
    expect(view.querySelector('[data-line="16"]')).toHaveAttribute("data-tone", "uncovered");
    expect(view.querySelector('[data-line="22"]')).toHaveAttribute("data-tone", "covered");
    expect([...view.querySelector('[data-line="22"]')!.querySelectorAll("[data-lane]")].map((cell) => cell.getAttribute("data-lane"))).toEqual(["integration"]);
  });
});

describe("Coverage: no source", () => {
  const cases: [CoverageSourceScenario, string][] = [
    ["no-repo", "no linked GitHub repository"],
    ["uncommitted", "uncommitted changes"],
    ["gone", "GitHub no longer has this version"],
    ["revoked", "connection that reads this repository was revoked"],
    ["not-yet-available", "coming"],
  ];

  it.each(cases)("%s: line numbers and hits, one line of why, the figures kept", async (scenario, why) => {
    renderCoverage({ path: FIXTURE_REFUND_PATH }, { source: fixtureCoverageSource(scenario) });
    const state = await screen.findByTestId("tests-coverage-no-source");
    expect(state).toHaveAttribute("data-reason", scenario);
    expect(within(state).getByTestId("tests-coverage-no-source-why")).toHaveTextContent(why);
    const rows = within(state).getAllByTestId("tests-coverage-hits-row");
    expect(rows).toHaveLength(10);
    expect(rows[0]).toHaveAttribute("data-line", "16");
    expect([...rows[0]!.querySelectorAll("[data-lane]")].map((cell) => cell.textContent)).toEqual(["12", "0", "3"]);
    expect(screen.getByTestId("tests-coverage-file-totals")).toHaveTextContent("75.0% combined · unit 75.0 · integration 25.0 · e2e 41.7 · 12 statements");
    expect(screen.queryByTestId("tests-coverage-source")).not.toBeInTheDocument();
  });

  it("a file the run changed without committing has no blob, and GitHub is never asked", async () => {
    const source = fixtureCoverageSource("github");
    renderCoverage({ path: FIXTURE_INVOICE_PATH }, { source });
    expect(await screen.findByTestId("tests-coverage-no-source")).toHaveAttribute("data-reason", "uncommitted");
    expect(source.calls).toEqual([]);
  });

  it("a blob GitHub lost says so", async () => {
    renderCoverage({ path: FIXTURE_TAX_PATH }, { source: fixtureCoverageSource("github") });
    expect(await screen.findByTestId("tests-coverage-no-source")).toHaveAttribute("data-reason", "gone");
  });

  it("the real fetch, until GetCoverageSource exists, says source is coming", async () => {
    renderCoverage({ path: FIXTURE_REFUND_PATH }, { source: fetchCoverageSource });
    expect(await screen.findByTestId("tests-coverage-no-source")).toHaveAttribute("data-reason", "not-yet-available");
  });

  it("draws NO highlight over source that is not the report's blob, nor in any no-source state", async () => {
    renderCoverage({ path: FIXTURE_REFUND_PATH }, { source: fixtureCoverageSource("tampered") });
    const state = await screen.findByTestId("tests-coverage-no-source");
    expect(state).toHaveAttribute("data-reason", "mismatch");
    expect(state).toHaveTextContent("not the version these tests ran on");
    // Neither the tampered text nor any tone reached the page.
    expect(document.querySelectorAll("[data-tone]")).toHaveLength(0);
    expect(document.body).not.toHaveTextContent("// refunded");
    expect(document.body).not.toHaveTextContent("func Refund");
  });

  it.each(["no-repo", "gone", "revoked", "not-yet-available"] as const)("%s draws no tone either", async (scenario) => {
    renderCoverage({ path: FIXTURE_REFUND_PATH }, { source: fixtureCoverageSource(scenario) });
    await screen.findByTestId("tests-coverage-no-source");
    expect(document.querySelectorAll("[data-tone]")).toHaveLength(0);
  });

  it("a fetch that fails is unreachable, never a blank page", async () => {
    const failing: FetchCoverageSource = () => Promise.reject(new Error("network"));
    renderCoverage({ path: FIXTURE_REFUND_PATH }, { source: failing });
    expect(await screen.findByTestId("tests-coverage-no-source")).toHaveAttribute("data-reason", "unreachable");
  });
});

describe("Coverage: the URL", () => {
  it("parses path, commit, lane and base, and drops what it cannot use", () => {
    expect(forgeConsoleTestsCoverageSearchSchema.parse({ path: "internal/billing/refund.go", commit: FIXTURE_COMMIT, lane: "e2e", base: FIXTURE_BASE })).toMatchObject({
      path: "internal/billing/refund.go",
      commit: FIXTURE_COMMIT,
      lane: "e2e",
      base: FIXTURE_BASE,
    });
    expect(forgeConsoleTestsCoverageSearchSchema.parse({ commit: 1234 })).toMatchObject({ commit: "1234" });
    expect(forgeConsoleTestsCoverageSearchSchema.parse({ lane: "" }).lane).toBeUndefined();
  });

  it("reads the commit, lane and base the URL names", async () => {
    const api = fixtureTestApi();
    renderCoverage({ path: "internal/billing", commit: FIXTURE_COMMIT, lane: "unit", base: FIXTURE_BASE }, { api });
    await screen.findAllByTestId("tests-coverage-row");
    const asked = api.calls.filter((call) => call.method === "getCoverage").map((call) => call.req);
    expect(asked).toContainEqual(expect.objectContaining({ target: { gitCommit: FIXTURE_COMMIT }, path: "internal/billing", lane: "unit", baseSha: FIXTURE_BASE }));
    expect((screen.getByTestId("tests-coverage-commit") as HTMLSelectElement).value).toBe(FIXTURE_COMMIT);
    expect((screen.getByTestId("tests-coverage-lane") as HTMLSelectElement).value).toBe("unit");
    expect((screen.getByTestId("tests-coverage-base") as HTMLSelectElement).value).toBe(FIXTURE_BASE);
  });

  it("with no commit, reads the default branch's newest report; with an environment explicitly chosen, its current release", async () => {
    const api = fixtureTestApi();
    renderCoverage({}, { api });
    await screen.findAllByTestId("tests-coverage-row");
    expect(api.calls.find((call) => call.method === "getCoverage")?.req).toMatchObject({ target: { gitCommit: FIXTURE_COMMIT } });

    const scoped = fixtureTestApi();
    renderCoverage({ environment: "prod" }, { api: scoped, environmentId: "env-shop-prod" });
    await vi.waitFor(() => expect(scoped.calls.some((call) => call.method === "getCoverage")).toBe(true));
    expect(await screen.findByRole("option", { name: "Current release of prod" })).toBeInTheDocument();
    expect(scoped.calls.find((call) => call.method === "getCoverage")?.req).toMatchObject({ target: { environmentId: "env-shop-prod" } });
  });

  it("the base the fixture diffs against is a commit the selector offers", () => {
    expect(fixtureSummaryResponse().coverageTrend.at(-2)?.gitCommit).toBe(FIXTURE_BASE);
  });

  it("each selector writes its own key, and clearing one drops it", async () => {
    const { onSelect } = renderCoverage({ path: "internal/billing" });
    await screen.findAllByTestId("tests-coverage-row");
    fireEvent.change(screen.getByTestId("tests-coverage-lane"), { target: { value: "e2e" } });
    expect(onSelect).toHaveBeenLastCalledWith({ lane: "e2e" });
    fireEvent.change(screen.getByTestId("tests-coverage-lane"), { target: { value: "" } });
    expect(onSelect).toHaveBeenLastCalledWith({ lane: undefined });
    fireEvent.change(screen.getByTestId("tests-coverage-base"), { target: { value: FIXTURE_BASE } });
    expect(onSelect).toHaveBeenLastCalledWith({ base: FIXTURE_BASE });
    fireEvent.change(screen.getByTestId("tests-coverage-commit"), { target: { value: FIXTURE_COMMIT } });
    expect(onSelect).toHaveBeenLastCalledWith({ commit: FIXTURE_COMMIT });
  });

  it("a control plane without the service is a state, not an error", async () => {
    renderCoverage({}, { api: fixtureTestApi("not-configured") });
    expect(await screen.findByTestId("tests-error")).toHaveAttribute("data-kind", "not-configured");
  });

  it("an empty record names the commands that fill it", async () => {
    renderCoverage({}, { api: fixtureTestApi("no-runs") });
    expect(await screen.findByTestId("tests-no-runs")).toHaveTextContent("No test runs yet");
  });
});
