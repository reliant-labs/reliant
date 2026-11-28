// Copyright (c) 2026 Reliant Labs

/**
 * The release surfaces of Tests against the fixture TestsService: a
 * release's Tests tab (ok with its delta and links pinned to the commit,
 * partial, unavailable as a designed state, and the two refusals), and the
 * Releases list's Tests column (a mark per release; no column at all on a
 * control plane without the service or without tests:read). Then both
 * mounted where they live: release detail's `?tab=tests` and ReleasesList.
 */

import { useState, type ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";

import { useReleaseTests } from "@/hooks/test-queries";
import { releaseTestsOf } from "@/services/controlPlane/tests/wire";
import { createTestQueryClient } from "@/test/renderWithQuery";
import { ObservabilityApiProvider } from "@/services/controlPlane/observability/ObservabilityApiContext";
import { observabilityFixtureTransport } from "@/test/observabilityFixtures";
import { createTransportScope, TransportScopeProvider } from "@/lib/transportScope";
import { mockTransport } from "@/lib/test-utils";
import { TestsService } from "@/gen/controlplane/services/tests/v1/tests_pb";

import { ReleaseDetail, type ReleaseDetailSearch } from "../../Releases/ReleaseDetail";
import { ReleasesList, type ReleasesSearch } from "../../Releases/ReleasesList";
import { RELEASE_ENVS, RELEASE_PROJECT, RELEASES, createReleasesFixtureApi } from "../../Releases/__fixtures__/releaseFixtures";
import { fixtureObsApi } from "../../Releases/__tests__/releasesTestUtils";
import { releasesFixtureHandlers } from "../../Releases/__fixtures__/releaseFixtureTransport";
import { FIXTURE_COMMIT, FIXTURE_NOW, FIXTURE_PROJECT, FIXTURE_RELEASES, fixtureSummaryResponse, fixtureTests, type FixtureTests, type TestsScenario } from "../fixtures";
import { FIXTURE_RELEASE_COMMITS, FIXTURE_RELEASE_VERSIONS, fixtureReleaseCommit } from "../fixtureReleases";
import { fixtureReleaseTestsWire } from "../releaseTestsFixtures";
import { releaseTestsColumnShown, useReleaseTestsColumn } from "../surfaces/ReleaseTestsColumn";
import { ReleaseTestsTab, releaseTestsHrefs } from "../surfaces/ReleaseTestsTab";
import { fixtureScope, renderWithTests } from "./testsTestUtils";

vi.mock("@tanstack/react-router", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-router")>()),
  useNavigate: () => vi.fn(),
}));

vi.mock("@/components/forge-ui/link", () => ({
  default: ({ href, children, ...rest }: { href: string; children: ReactNode }) => (
    <a href={href} {...rest}>
      {children}
    </a>
  ),
}));

const V = FIXTURE_RELEASE_VERSIONS;
const HREFS = releaseTestsHrefs(FIXTURE_PROJECT, (version) => `/r/${version}`);

function tab(version: string, scenario: TestsScenario = "report") {
  return renderWithTests(fixtureTests(scenario), <ReleaseTestsTab project={FIXTURE_PROJECT} version={version} hrefs={HREFS} now={FIXTURE_NOW} />);
}

describe("the fixture", () => {
  it("puts the trend's release markers on the commits the releases were cut from", () => {
    const markers = fixtureSummaryResponse().releaseMarkers;
    expect(markers.map((m) => m.releaseVersion)).toEqual([...FIXTURE_RELEASES]);
    expect(markers.map((m) => m.gitCommit)).toEqual([FIXTURE_RELEASE_COMMITS.first, FIXTURE_RELEASE_COMMITS.prod]);
    // A version ends in its commit's short sha, but staging's (it runs main's newest commit).
    for (const key of ["prod", "partial", "untested", "first"] as const) expect(FIXTURE_RELEASE_COMMITS[key].startsWith(V[key].split("-").pop()!)).toBe(true);
    expect(FIXTURE_RELEASE_COMMITS.staging).toBe(FIXTURE_COMMIT);
  });

  it("answers each release's Tests with the commit its record names", async () => {
    for (const key of Object.keys(V) as (keyof typeof V)[]) {
      const tests = releaseTestsOf(fixtureReleaseTestsWire("report", { orgId: null, project: FIXTURE_PROJECT, version: V[key] }));
      expect(tests.gitCommit, key).toBe(fixtureReleaseCommit(V[key]));
    }
  });
});

describe("a release's Tests tab", () => {
  it("shows the release commit's report, its delta on the release before, and links pinned to the commit", async () => {
    tab(V.prod);
    const view = await screen.findByTestId("release-tests");
    expect(view).toHaveAttribute("data-status", "ok");
    expect(within(screen.getByTestId("release-tests-coverage")).getByText("73.4%")).toBeInTheDocument();
    const delta = screen.getByTestId("release-tests-delta");
    expect(delta).toHaveTextContent("+1.6 since");
    expect(within(delta).getByRole("link")).toHaveAttribute("href", `/r/${V.partial}`);
    expect(screen.getByTestId("release-tests-totals")).toHaveTextContent("Passed");
    expect(screen.getByTestId("release-tests-totals")).toHaveTextContent("1,398 passed · 36 skipped · 1 passed on retry");
    expect(screen.getByTestId("release-tests-lanes")).toHaveTextContent("unit · integration · e2e");
    expect(screen.getByTestId("release-tests-lane-e2e")).toHaveTextContent("52.0%");
    // The two tests that flaked since, each linking to its page.
    const since = screen.getByTestId("release-tests-since");
    expect(within(since).getByText("TestSyncJob")).toBeInTheDocument();
    expect(within(since).getByText("TestCartMerge")).toBeInTheDocument();
    // Runs and Coverage, pinned to the commit the release was cut from.
    const commit = FIXTURE_RELEASE_COMMITS.prod;
    expect(screen.getByTestId("release-tests-runs-link")).toHaveAttribute("href", `/forge/p/${FIXTURE_PROJECT}/tests/runs?commit=${commit}`);
    expect(screen.getByTestId("release-tests-coverage-link")).toHaveAttribute("href", `/forge/p/${FIXTURE_PROJECT}/tests/coverage?commit=${commit}`);
  });

  it("says partial evidence is partial, and never marks it passed", async () => {
    tab(V.partial);
    expect(await screen.findByTestId("release-tests")).toHaveAttribute("data-status", "partial");
    expect(screen.getByTestId("release-tests-partial")).toHaveTextContent("The e2e lane was not recorded for this commit");
    expect(screen.getByTestId("release-tests-totals")).toHaveTextContent("2 failed");
    // The release before it has no report: no delta, and it says why.
    expect(screen.getByTestId("release-tests-delta")).toHaveTextContent("has no report to compare");
    expect(within(screen.getByTestId("release-tests-since")).getByText("TestRefundPartial")).toBeInTheDocument();
  });

  it("draws a release nobody tested as a state with its reason, not an error", async () => {
    tab(V.untested);
    const state = await screen.findByTestId("release-tests-unavailable");
    expect(state).toHaveAttribute("role", "status");
    expect(screen.getByText("No test report for this release")).toBeInTheDocument();
    expect(screen.getByTestId("release-tests-reason")).toHaveTextContent("No test report for commit 5eed5eed5eed");
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.getByTestId("release-tests-runs-link")).toBeInTheDocument();
  });

  it("says the first release with a report has nothing to compare", async () => {
    tab(V.first);
    expect(await screen.findByTestId("release-tests-delta")).toHaveTextContent("the first release with a report");
    expect(screen.getByTestId("release-tests-since-none")).toBeInTheDocument();
  });

  it("on a control plane without TestsService, says it doesn't store test runs yet", async () => {
    tab(V.prod, "not-configured");
    expect(await screen.findByTestId("release-tests-error")).toHaveAttribute("data-kind", "not-configured");
    expect(screen.getByText("This control plane doesn't store test runs yet")).toBeInTheDocument();
  });

  it("without tests:read, names the grant", async () => {
    tab(V.prod, "denied");
    expect(await screen.findByTestId("release-tests-error")).toHaveAttribute("data-kind", "denied");
  });
});

function ColumnProbe({ versions }: { versions: string[] }) {
  const column = useReleaseTestsColumn<{ version: string }>(FIXTURE_PROJECT, versions[0]);
  // The probe's own read (the same cache entry), so a test can wait for it to settle.
  const probe = useReleaseTests(FIXTURE_PROJECT, versions[0] ?? "");
  if (!column) return <p data-testid="no-tests-column" data-settled={probe.isError || probe.isSuccess ? "true" : undefined} />;
  return (
    <table>
      <thead>
        <tr>
          <th>{column.header}</th>
        </tr>
      </thead>
      <tbody>
        {versions.map((version) => (
          <tr key={version}>
            <td>{column.render!(undefined, { version })}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

describe("the Releases list's Tests column", () => {
  const versions = [V.staging, V.prod, V.partial, V.untested];

  it("marks each release: passed with its coverage, failed, and a dash with the reason when there is no report", async () => {
    renderWithTests(fixtureTests("report"), <ColumnProbe versions={versions} />);
    expect(await screen.findByRole("columnheader", { name: "Tests" })).toBeInTheDocument();
    await waitFor(() => expect(screen.getByTestId(`release-tests-mark-${V.untested}`)).toHaveAttribute("data-state", "unavailable"));
    const staging = screen.getByTestId(`release-tests-mark-${V.staging}`);
    expect(staging).toHaveAttribute("data-tone", "success");
    expect(staging).toHaveTextContent("74.2%");
    const partial = screen.getByTestId(`release-tests-mark-${V.partial}`);
    expect(partial).toHaveAttribute("data-tone", "danger");
    expect(partial).toHaveTextContent("71.8%");
    expect(screen.getByTestId(`release-tests-mark-${V.untested}`)).toHaveAttribute("title", expect.stringContaining("No test report for commit 5eed5eed5eed"));
  });

  it("asks only about the releases it draws", async () => {
    const api = fixtureTests("report");
    renderWithTests(api, <ColumnProbe versions={[V.prod]} />);
    await screen.findByTestId(`release-tests-mark-${V.prod}`);
    await waitFor(() => expect(screen.getByTestId(`release-tests-mark-${V.prod}`)).toHaveAttribute("data-state", "ok"));
    // The probe and the cell are one read.
    expect(api.calls.filter((call) => call.method === "getReleaseTests")).toHaveLength(1);
  });

  it.each(["not-configured", "denied"] as const)("hides itself when the control plane answers %s", async (scenario) => {
    renderWithTests(fixtureTests(scenario), <ColumnProbe versions={versions} />);
    // The probe settles; the column never appears.
    await waitFor(() => expect(screen.getByTestId("no-tests-column")).toHaveAttribute("data-settled", "true"));
    expect(screen.queryByRole("columnheader", { name: "Tests" })).not.toBeInTheDocument();
  });

  it("shows on any other failure, which is each cell's own dash", () => {
    expect(releaseTestsColumnShown({ error: undefined })).toBe(false);
    expect(releaseTestsColumnShown({ data: {}, error: null })).toBe(true);
    expect(releaseTestsColumnShown({ error: new Error("network") })).toBe(true);
  });
});

// ── Mounted where they live ─────────────────────────────────────────────────

function Providers({ api, children }: { api: FixtureTests; children: ReactNode }) {
  const [obsApi] = useState(() => fixtureObsApi());
  const [scope] = useState(() => {
    const tests = fixtureScope(api).transport;
    const obs = observabilityFixtureTransport(() => obsApi, Date.now());
    const handlers = releasesFixtureHandlers(createReleasesFixtureApi());
    const releases = mockTransport(handlers);
    const pick = (method: { parent: { typeName: string }; name: string }) =>
      method.parent.typeName === TestsService.typeName ? tests : `${method.parent.typeName}/${method.name}` in handlers ? releases : obs;
    return createTransportScope({
      id: "release-tests-fixtures",
      queryClient: createTestQueryClient(),
      transport: {
        unary: (method, ...rest) => pick(method).unary(method, ...rest),
        stream: (method, ...rest) => pick(method).stream(method, ...rest),
      },
    });
  });
  return (
    <TransportScopeProvider scope={scope}>
      <ObservabilityApiProvider api={obsApi}>{children}</ObservabilityApiProvider>
    </TransportScopeProvider>
  );
}

function Detail({ version, initial }: { version: string; initial: ReleaseDetailSearch }) {
  const [search, setSearch] = useState(initial);
  return (
    <ReleaseDetail
      project={RELEASE_PROJECT}
      version={version}
      env="prod"
      envs={RELEASE_ENVS}
      envsLoading={false}
      search={search}
      onSearch={(next) => setSearch((prev) => ({ ...prev, ...next }))}
      listHref="/releases"
      releaseHref={(v) => `/r/${v}`}
      activityHref={() => "/activity"}
      promote={{ onPromote: vi.fn(), blockedReason: null }}
    />
  );
}

function List({ initial = {} }: { initial?: ReleasesSearch }) {
  const [search, setSearch] = useState(initial);
  return (
    <ReleasesList
      project={RELEASE_PROJECT}
      env="prod"
      envs={RELEASE_ENVS}
      envsLoading={false}
      search={search}
      onSearch={(next) => setSearch((prev) => ({ ...prev, ...next }))}
      releaseHref={(version) => `/r/${version}`}
      promote={{ onPromote: vi.fn(), blockedReason: null }}
    />
  );
}

describe("where they live", () => {
  it("release detail has a Tests tab, and ?tab=tests opens it", async () => {
    render(
      <Providers api={fixtureTests("report")}>
        <Detail version={RELEASES[0]!.version} initial={{ tab: "tests" }} />
      </Providers>
    );
    await waitFor(() => expect(screen.getByTestId("release-detail")).toHaveAttribute("data-state", "ready"));
    expect(screen.getByRole("tab", { name: "Tests" })).toHaveAttribute("aria-selected", "true");
    // This project's releases have no report in the fixture: the designed state, never an error.
    expect(await screen.findByTestId("release-tests-unavailable")).toBeInTheDocument();
  });

  it("the Releases list gains a Tests column once the service answers", async () => {
    render(
      <Providers api={fixtureTests("report")}>
        <List />
      </Providers>
    );
    expect(await screen.findByRole("columnheader", { name: "Tests" })).toBeInTheDocument();
  });

  it("the Releases list has no Tests column on a control plane without the service", async () => {
    const api = fixtureTests("not-configured");
    render(
      <Providers api={api}>
        <List />
      </Providers>
    );
    await waitFor(() => expect(screen.getByTestId("releases-table")).toBeInTheDocument());
    await waitFor(() => expect(api.calls.some((call) => call.method === "getReleaseTests")).toBe(true));
    expect(screen.getByRole("columnheader", { name: "Checks" })).toBeInTheDocument();
    expect(screen.queryByRole("columnheader", { name: "Tests" })).not.toBeInTheDocument();
  });
});
