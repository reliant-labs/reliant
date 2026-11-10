// Copyright (c) 2025 Reliant Labs

import { describe, expect, it, vi } from "vitest";

vi.mock("uplot", async () => (await import("../../Observability/__tests__/obsMocks")).obsMocks.uplot());
vi.mock("@tanstack/react-router", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-router")>()),
  useNavigate: () => vi.fn(),
}));

import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClientProvider } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";

import { createTestQueryClient } from "@/test/renderWithQuery";
import { ObservabilityApiProvider } from "@/services/controlPlane/observability/ObservabilityApiContext";
import { ReleasesApiProvider, type ReleasesApi } from "@/services/forge/ReleasesApiContext";
import { PROMOTIONS_PAGE, type ReleaseGate, type ReleasePromotion } from "@/services/forge/releases";

import { createWorkloadsFixtureApi } from "../../Workloads/__fixtures__/workloadFixtures";
import { createReleasesFixtureApi, RELEASES } from "../../Releases/__fixtures__/releaseFixtures";
import { HEALTH_GATE } from "../../Releases/envPromotionsModel";
import { EnvPromotions, PROMOTIONS_SHOWN } from "../EnvPromotions";

// Cut order, newest first: NEW is newer than OLD.
const NEW = RELEASES[0]!.version;
const OLD = RELEASES[1]!.version;
const ENV = { id: "env-prod", name: "prod" };

const health = (status: ReleaseGate["status"]): ReleaseGate => ({
  name: HEALTH_GATE,
  status,
  rawStatus: "",
  summary: `release health ${status}`,
  url: "",
  recordedBy: "",
  finishedAt: "2026-11-01T12:00:00Z",
  source: "recorded",
});

let seq = 0;
const promo = (version: string, over: Partial<ReleasePromotion> = {}): ReleasePromotion => ({
  id: `p${++seq}`,
  environmentId: ENV.id,
  releaseVersion: version,
  fromEnvironmentName: "",
  promotedByUserId: "",
  promotedByActor: "",
  approvedBy: "",
  note: "",
  createdAt: "2026-11-01T12:00:00Z",
  targets: [],
  gates: [],
  bundleId: "b",
  bundleDigest: `sha256:${"b".repeat(64)}`,
  resolvedArtifacts: {},
  ...over,
});

function renderCard(listPromotions: ReleasesApi["listPromotions"], over: Partial<ReleasesApi> = {}) {
  const calls = { listPromotions: vi.fn(listPromotions), getRollout: vi.fn(), getBundle: vi.fn(), compareReleases: vi.fn(), getRelease: vi.fn() };
  const api: ReleasesApi = { ...createReleasesFixtureApi(), ...calls, ...over } as ReleasesApi;
  const client = createTestQueryClient();
  const ui = (env: { id: string; name: string }) => (
    <QueryClientProvider client={client}>
      <ObservabilityApiProvider api={createWorkloadsFixtureApi()}>
        <ReleasesApiProvider api={api}>
          <EnvPromotions project="control-plane" env={env} releaseHref={(version) => `/releases/${version}?env=${env.name}`} releasesHref={`/releases?env=${env.name}`} />
        </ReleasesApiProvider>
      </ObservabilityApiProvider>
    </QueryClientProvider>
  );
  const result = render(ui(ENV));
  return { ...calls, switchTo: (env: { id: string; name: string }) => result.rerender(ui(env)) };
}

describe("EnvPromotions", () => {
  it("renders every promotion from this environment's ListPromotions — every workload's, scoped and full — and reads nothing per row", async () => {
    const calls = renderCard(async () => [
      promo(OLD, { promotedByActor: "bot", gates: [health("failed")] }),
      promo(NEW, { promotedByActor: "alice", gates: [health("passed")], targets: ["admin-web"] }),
      promo(NEW, { promotedByActor: "alice", targets: ["api"] }),
      promo(OLD, {
        promotedByActor: "ci",
        fromEnvironmentName: "staging",
        bundleDigest: "",
        resolvedArtifacts: { web: `sha256:${"e".repeat(64)}`, api: `sha256:${"a".repeat(64)}` },
      }),
    ]);
    const rows = await screen.findAllByTestId("env-promotion-row");
    // Unfiltered by workload: admin-web's scoped deploy and api's are both here.
    expect(rows).toHaveLength(4);
    expect(calls.listPromotions).toHaveBeenCalledWith(ENV.id);
    expect(calls.listPromotions).toHaveBeenCalledTimes(1);
    for (const perRow of [calls.getRollout, calls.getBundle, calls.compareReleases, calls.getRelease]) expect(perRow).not.toHaveBeenCalled();

    expect(within(rows[1]!).getByText("scoped: admin-web")).toBeInTheDocument();
    expect(within(rows[3]!).getByText("full")).toBeInTheDocument();
    expect(rows[3]).toHaveTextContent("promoted from staging");
    expect(within(rows[0]!).getByRole("link", { name: /./ })).toHaveAttribute("href", `/releases/${OLD}?env=prod`);
    expect(rows[0]).toHaveTextContent("bot");
    expect(rows[0]).toHaveTextContent("bundle");
    // No bundle recorded: a frozen artifact digest is still on screen, not just a count.
    const artifacts = within(rows[3]!).getByTestId("env-promotion-artifacts");
    expect(artifacts).toHaveTextContent(/^api sha256:a+.*\+1 more$/);
    expect(artifacts).toHaveAttribute("title", expect.stringContaining(`web: sha256:${"e".repeat(64)}`));
  });

  it("marks the rollback, and its health as recorded — unknown when none was, never healthy", async () => {
    renderCard(async () => [promo(OLD, { gates: [health("failed")] }), promo(NEW, { gates: [health("passed")] }), promo(OLD)]);
    const rows = await screen.findAllByTestId("env-promotion-row");
    await waitFor(() => expect(rows[0]).toHaveAttribute("data-rollback"));
    expect(within(rows[0]!).getByText("rollback")).toBeInTheDocument();
    expect(rows[0]).toHaveTextContent(`rolled back from`);
    expect(rows[0]).toHaveTextContent("health: regressed");
    expect(rows[1]).not.toHaveAttribute("data-rollback");
    expect(rows[1]).toHaveTextContent("health: healthy");
    expect(rows[2]).toHaveTextContent("health: unknown");
    expect(rows[2]).toHaveTextContent("checks —");
  });

  it("re-reads for a new environment when the env switches while mounted, and never shows the previous env's rows", async () => {
    let resolveStaging: (value: ReleasePromotion[]) => void = () => {};
    const card = renderCard((environmentId) =>
      environmentId === ENV.id
        ? Promise.resolve(Array.from({ length: PROMOTIONS_SHOWN + 2 }, () => promo(NEW, { promotedByActor: "prod-bot" })))
        : new Promise((resolve) => (resolveStaging = resolve))
    );
    await waitFor(() => expect(screen.getAllByTestId("env-promotion-row")).toHaveLength(PROMOTIONS_SHOWN));
    await userEvent.click(screen.getByTestId("env-promotions-show-all"));
    expect(screen.getAllByTestId("env-promotion-row")).toHaveLength(PROMOTIONS_SHOWN + 2);

    card.switchTo({ id: "env-staging", name: "staging" });
    expect(card.listPromotions).toHaveBeenLastCalledWith("env-staging");
    // While staging's ledger is in flight, nothing of prod's is on screen.
    expect(screen.getByTestId("env-promotions-loading")).toBeInTheDocument();
    expect(screen.queryAllByTestId("env-promotion-row")).toHaveLength(0);

    resolveStaging([promo(OLD, { environmentId: "env-staging", promotedByActor: "staging-bot" })]);
    const rows = await screen.findAllByTestId("env-promotion-row");
    expect(rows).toHaveLength(1);
    expect(rows[0]).toHaveTextContent("staging-bot");
    expect(screen.getByTestId("env-promotions")).not.toHaveTextContent("prod-bot");
    expect(within(rows[0]!).getByRole("link")).toHaveAttribute("href", `/releases/${OLD}?env=staging`);
  });

  it("labels a scoped deploy that rolls one target back and redeploys another as mixed, naming each", async () => {
    renderCard(async () => [promo(OLD, { targets: ["api", "web"] }), promo(OLD, { targets: ["web"] }), promo(NEW, { targets: ["api"] }), promo(OLD)]);
    const [row] = await screen.findAllByTestId("env-promotion-row");
    await waitFor(() => expect(row).toHaveAttribute("data-rollback"));
    expect(within(row!).getByText("rollback: api")).toBeInTheDocument();
    expect(within(row!).getByText("redeploy: web")).toBeInTheDocument();
    expect(within(row!).queryByText("redeploy")).toBeNull();
    expect(within(row!).queryByText("rollback")).toBeNull();
    expect(within(row!).getByTestId("env-promotion-context")).toHaveTextContent("rolled back from");
    expect(within(row!).getByTestId("env-promotion-context")).toHaveTextContent(": api · the same release again: web");
  });

  it("shows a loading state, then an empty one", async () => {
    let resolve: (value: ReleasePromotion[]) => void = () => {};
    renderCard(() => new Promise((r) => (resolve = r)));
    expect(screen.getByTestId("env-promotions-loading")).toHaveAttribute("aria-busy", "true");
    resolve([]);
    expect(await screen.findByTestId("env-promotions-empty")).toHaveTextContent("Nothing has been promoted to prod yet.");
  });

  it("says when the ledger could not be read, and links to the Releases page", async () => {
    renderCard(async () => {
      throw new ConnectError("ledger unavailable", Code.InvalidArgument);
    });
    const failed = await screen.findByTestId("env-promotions-failed", {}, { timeout: 5000 });
    expect(failed).toHaveTextContent("prod's promotion ledger could not be read right now.");
    expect(within(failed).getByRole("link")).toHaveAttribute("href", "/releases?env=prod");
  });

  it("lists the newest few, expands to every loaded row, and is honest that a full page may not be the whole ledger", async () => {
    renderCard(async () => Array.from({ length: PROMOTIONS_PAGE }, () => promo(NEW)));
    await waitFor(() => expect(screen.getAllByTestId("env-promotion-row")).toHaveLength(PROMOTIONS_SHOWN));
    expect(screen.getByTestId("env-promotions-more")).toHaveTextContent(`The ${PROMOTIONS_SHOWN} newest of ${PROMOTIONS_PAGE} loaded.`);
    expect(screen.getByTestId("env-promotions-truncated")).toHaveTextContent(`Only the newest ${PROMOTIONS_PAGE} promotions are loaded`);
    await userEvent.click(screen.getByTestId("env-promotions-show-all"));
    expect(screen.getAllByTestId("env-promotion-row")).toHaveLength(PROMOTIONS_PAGE);
    expect(screen.getByTestId("env-promotions-truncated")).toBeInTheDocument();
  });
});
