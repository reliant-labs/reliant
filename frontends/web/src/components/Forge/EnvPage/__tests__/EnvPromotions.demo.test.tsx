// Copyright (c) 2025 Reliant Labs

/**
 * The environment page's Promotions card on the public demo's data: the real
 * ReleasesApi (services/forge/releases.ts) over the demo's mock transport, so
 * what renders here is what demo visitors see on shop's staging page — every
 * kind of ledger row, from consoleScenario.ts.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));
vi.mock("uplot", async () => (await import("../../Observability/__tests__/obsMocks")).obsMocks.uplot());
vi.mock("@tanstack/react-router", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-router")>()),
  useNavigate: () => vi.fn(),
}));

import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClientProvider } from "@tanstack/react-query";

import { installShellTransport, setTransport } from "@/lib/connect";
import { createTestQueryClient } from "@/test/renderWithQuery";
import { ObservabilityApiProvider } from "@/services/controlPlane/observability/ObservabilityApiContext";

import { DEMO_PROJECT } from "../../Demo/demoData/consoleScenario";
import { createDemoTransport } from "../../Demo/demoData/demoTransport";
import { createWorkloadsFixtureApi } from "../../Workloads/__fixtures__/workloadFixtures";
import { EnvPromotions, PROMOTIONS_SHOWN } from "../EnvPromotions";

beforeEach(() => {
  setTransport(null);
  installShellTransport(createDemoTransport({ source: "fixtures", url: "" }));
});

afterEach(() => {
  installShellTransport(null as never);
});

function renderStaging() {
  renderEnv("staging");
}

function renderEnv(name: "staging" | "prod") {
  render(
    <QueryClientProvider client={createTestQueryClient()}>
      <ObservabilityApiProvider api={createWorkloadsFixtureApi()}>
        <EnvPromotions
          project={DEMO_PROJECT}
          env={{ id: `env-${name}`, name }}
          releaseHref={(version) => `/releases/${version}?env=${name}`}
          releasesHref={`/releases?env=${name}`}
        />
      </ObservabilityApiProvider>
    </QueryClientProvider>
  );
}

/** The row's tags (forge-ui Tag), by their text. */
const tags = (row: HTMLElement) => [...row.querySelectorAll("span")].map((el) => el.textContent ?? "");

describe("EnvPromotions on the demo's staging ledger", () => {
  it("renders every kind of row: healthy, a whole-release rollback, a scoped canary, a mixed rollback + redeploy, and unknown health", async () => {
    renderStaging();
    await waitFor(() => expect(screen.getAllByTestId("env-promotion-row")).toHaveLength(PROMOTIONS_SHOWN));
    // Rollbacks are judged once the release list (cut order) has loaded.
    await waitFor(() => expect(screen.getAllByTestId("env-promotion-row").some((row) => row.hasAttribute("data-rollback"))).toBe(true));
    const rows = screen.getAllByTestId("env-promotion-row");

    // The running release: whole, healthy, from dev, with a digest on screen.
    expect(rows[0]).toHaveAttribute("data-version", "v1.8.3");
    expect(rows[0]).toHaveTextContent("full");
    expect(rows[0]).toHaveTextContent("jon");
    expect(rows[0]).toHaveTextContent("health: healthy");
    expect(rows[0]).toHaveTextContent("replaced v1.8.2 · promoted from dev");
    // The demo's promotions apply a bundle: its digest is the one on screen,
    // and the artifacts it pinned are counted beside it.
    expect(rows[0]).toHaveTextContent(/bundle sha256:/);
    expect(within(rows[0]!).getByTestId("env-promotion-artifacts")).toHaveTextContent(/\d+ artifacts pinned/);

    // The mixed row: api back from v1.8.3, the storefront the same release again.
    const mixed = rows[1]!;
    expect(mixed).toHaveAttribute("data-rollback");
    expect(tags(mixed)).toEqual(expect.arrayContaining(["rollback: api", "scoped: api +1", "redeploy: shop-web"]));
    expect(tags(mixed)).not.toContain("rollback");
    expect(within(mixed).getByTestId("env-promotion-context")).toHaveTextContent("rolled back from v1.8.3: api · the same release again: shop-web");

    // A scoped redeploy with no health check recorded: unknown, never healthy.
    expect(rows[2]).toHaveTextContent("scoped: shop-web");
    expect(rows[2]).toHaveTextContent("health: unknown");

    // The canary: scoped to the api, forward. It passed its check: the demo's
    // one regression is prod's v1.8.3, the incident (below).
    expect(rows[3]).toHaveTextContent("scoped: api");
    expect(rows[3]).not.toHaveAttribute("data-rollback");
    expect(rows[3]).toHaveTextContent("health: healthy");

    // CI's re-apply: a whole-release redeploy, no health check.
    expect(rows[5]).toHaveTextContent("github-actions");
    expect(tags(rows[5]!)).toEqual(expect.arrayContaining(["full", "redeploy"]));
    expect(rows[5]).toHaveTextContent("health: unknown");

    // The whole-release rollback, with its reason.
    const rollback = rows[7]!;
    expect(rollback).toHaveAttribute("data-version", "v1.8.1");
    expect(rollback).toHaveAttribute("data-rollback");
    expect(tags(rollback)).toEqual(expect.arrayContaining(["rollback", "full"]));
    expect(rollback).toHaveTextContent("rolled back from v1.8.2");
    expect(rollback).toHaveTextContent("held a lock on orders");
    expect(within(rollback).getByRole("link", { name: "v1.8.1" })).toHaveAttribute("href", "/releases/v1.8.1?env=staging");
  });

  it("shows the incident as prod's regressed promotion", async () => {
    renderEnv("prod");
    await waitFor(() => expect(screen.getAllByTestId("env-promotion-row").length).toBeGreaterThan(0));
    const [newest] = screen.getAllByTestId("env-promotion-row");
    expect(newest).toHaveAttribute("data-version", "v1.8.3");
    expect(newest).toHaveTextContent("health: regressed");
  });

  it("lists ten and shows the rest on request", async () => {
    renderStaging();
    const more = await screen.findByTestId("env-promotions-more");
    expect(more).toHaveTextContent(`The ${PROMOTIONS_SHOWN} newest of 11 loaded.`);
    await userEvent.click(screen.getByTestId("env-promotions-show-all"));
    const rows = screen.getAllByTestId("env-promotion-row");
    expect(rows).toHaveLength(11);
    expect(rows[10]).toHaveAttribute("data-version", "v1.8.1");
    expect(rows[10]).toHaveTextContent("maya");
    // Eleven loaded is not a full page: nothing claims the ledger was cut off.
    expect(screen.queryByTestId("env-promotions-truncated")).toBeNull();
  });
});
