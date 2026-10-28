// Copyright (c) 2025 Reliant Labs

/**
 * The Releases list against the fixture ledger (releaseFixtures.ts): the
 * pipeline, the env columns, the DORA strip, the filters, the row menu's
 * env-naming actions, and the empty / loading / error / degraded states.
 */

import { describe, expect, it, vi } from "vitest";
import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Code, ConnectError } from "@connectrpc/connect";

import { releaseKeys } from "@/hooks/release-queries";

import { PROMOTIONS, RELEASES, createReleasesFixtureApi } from "../__fixtures__/releaseFixtures";
import { fixtureObsApi, renderList } from "./releasesTestUtils";

vi.mock("@tanstack/react-router", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-router")>()),
  useNavigate: () => vi.fn(),
}));

const V = RELEASES.map((release) => release.version);
const ready = async () => waitFor(() => expect(screen.getByTestId("releases")).toHaveAttribute("data-state", "ready"));
const row = (version: string) => screen.getByTestId(`release-row-${version}`);

describe("the list", () => {
  it("shows what is where: the pipeline, one column per long-lived env, previews in one", async () => {
    renderList(createReleasesFixtureApi());
    await ready();
    const pipeline = screen.getByTestId("releases-pipeline");
    expect(within(pipeline).getByTestId("pipeline-prod")).toHaveTextContent(V[0]!);
    // Names arrive from DeliveryInsights (the ledger records only user ids).
    await waitFor(() => expect(within(pipeline).getByTestId("pipeline-prod")).toHaveTextContent("promoted by sean"));
    expect(within(pipeline).getByTestId("pipeline-prod")).toHaveAttribute("data-selected", "true");
    expect(within(pipeline).getByTestId("pipeline-e2e")).toHaveTextContent("3 behind prod");
    expect(within(pipeline).getByTestId("pipeline-previews")).toHaveTextContent("3 running");

    const table = screen.getByTestId("releases-table");
    expect(within(table).getAllByRole("columnheader").map((th) => th.textContent)).toEqual([
      "Release",
      "Commit",
      "Checks",
      "prod",
      "e2e",
      "dev",
      "previews",
      "Author",
      "Actions",
    ]);
    expect(within(row(V[0]!)).getByText("live in prod")).toBeInTheDocument();
    expect(within(row(V[0]!)).getByText("Live in pr-984, pr-986")).toBeInTheDocument();
    expect(within(row(V[1]!)).getByText("ran before in prod")).toBeInTheDocument();
  });

  it("carries the commit subject and author, and tags scoped and rolled-back releases", async () => {
    renderList(createReleasesFixtureApi());
    await ready();
    expect(row(V[0]!)).toHaveTextContent(RELEASES[0]!.subject);
    expect(row(V[0]!)).toHaveTextContent("sean");
    expect(row(V[0]!)).toHaveTextContent("scoped: admin-web +1");
    // Current-state scope remains visible, but promotion events live on the release.
    expect(screen.queryByTestId(`release-followup-${V[0]}`)).not.toBeInTheDocument();
    const identity = within(row(V[0]!)).getAllByRole("cell")[0];
    expect(identity.querySelector("a")).toHaveTextContent(RELEASES[0]!.subject);
    expect(identity.querySelector("a")).toHaveAttribute("aria-label", `${RELEASES[0]!.subject} · ${V[0]}`);
    expect(identity).toHaveTextContent(V[0]!.split("-")[0]);
    expect(row(V[2]!)).toHaveTextContent("rolled back · prod");
    expect(row(V[2]!)).toHaveTextContent("1/2");
    // A release cut before subjects were recorded says so instead of inventing one.
    expect(row(V[6]!)).toHaveTextContent("No commit subject recorded");
  });

  it("states the scoped environment's delivery figures, each with the server's definition", async () => {
    renderList(createReleasesFixtureApi());
    const dora = await screen.findByTestId("releases-dora");
    expect(dora).toHaveTextContent("23 deployments to prod in the last 30 days");
    expect(dora).toHaveTextContent("Lead time, median 32m");
    expect(dora).toHaveTextContent("Change failure 2 of 23");
    expect(within(dora).getByText(/Change failure/)).toHaveAttribute("title", "Deployments rolled back within 24 hours.");
  });

  it("says when the delivery figures cannot be read, without failing the page", async () => {
    renderList(createReleasesFixtureApi(), { obs: fixtureObsApi({ getDoraMetrics: async () => Promise.reject(new ConnectError("no", Code.PermissionDenied)) }) });
    expect(await screen.findByTestId("releases-dora-unavailable")).toHaveTextContent("Delivery figures for prod are unavailable right now");
    await ready();
  });

  it("narrows to the releases that ran in the scoped env, and offers the rest", async () => {
    const user = userEvent.setup();
    const harness = renderList(createReleasesFixtureApi(), { env: "e2e" });
    await ready();
    expect(screen.queryByTestId(`release-row-${V[0]}`)).not.toBeInTheDocument();
    expect(screen.getByTestId(`release-row-${V[3]}`)).toBeInTheDocument();
    await user.click(screen.getByTestId("releases-show-hidden"));
    expect(harness.current().every).toBe(true);
    expect(await screen.findByTestId(`release-row-${V[0]}`)).toBeInTheDocument();
  });

  it("filters by text, author and status, and clears", async () => {
    const user = userEvent.setup();
    const harness = renderList(createReleasesFixtureApi());
    await ready();
    await user.type(screen.getByTestId("releases-filter"), "fluxobserve");
    expect(screen.getAllByTestId(/^release-row-/)).toHaveLength(1);
    await user.click(screen.getByRole("button", { name: "Clear" }));
    expect(harness.current().q).toBeUndefined();

    await user.click(screen.getByTestId("releases-chip-status"));
    await user.click(screen.getByRole("menuitemradio", { name: "Rolled back" }));
    expect(harness.current().status).toBe("rolled-back");
    // Rolled back anywhere: .063708 in prod, v20261017 in e2e (it ran in prod too, so the scope keeps it).
    expect(screen.getAllByTestId(/^release-row-/).map((tr) => tr.dataset.testid)).toEqual([`release-row-${V[2]}`, `release-row-${V[5]}`]);
    await user.click(screen.getByTestId("releases-chip-author"));
    await user.click(screen.getByRole("menuitemradio", { name: "sean" }));
    expect(harness.current().author).toBe("sean");
    expect(screen.getAllByTestId(/^release-row-/).map((tr) => tr.dataset.testid)).toEqual([`release-row-${V[5]}`]);
    await user.type(screen.getByTestId("releases-filter"), "no such thing");
    expect(await screen.findByTestId("releases-empty-filtered")).toHaveTextContent("No release matches these filters");
  });

  it("uses the pipeline only for current state, leaving scope selection to the top bar", async () => {
    renderList(createReleasesFixtureApi());
    await ready();
    const pipeline = screen.getByTestId("releases-pipeline");
    expect(within(pipeline).queryAllByRole("button")).toHaveLength(0);
    expect(pipeline.querySelector("[aria-pressed]")).toBeNull();
    expect(within(pipeline).getByTestId("pipeline-e2e")).toHaveTextContent("e2e");
    expect(within(pipeline).getAllByRole("link").length).toBeGreaterThan(0);
  });
});

describe("row actions name their environment", () => {
  it("promotes through the target dialog, defaulting to the scoped env", async () => {
    const user = userEvent.setup();
    const harness = renderList(createReleasesFixtureApi());
    await ready();
    await user.click(screen.getByTestId(`release-menu-${V[1]}`));
    await user.click(screen.getByTestId("release-menu-promote"));
    const dialog = await screen.findByTestId("promote-target");
    expect(within(dialog).getAllByText("now 20261018.155157")).toHaveLength(2);
    expect(within(dialog).getByText("now 20261018.052711")).toBeInTheDocument();
    await user.click(screen.getByTestId("promote-target-confirm"));
    expect(harness.promote).toHaveBeenCalledWith({ version: V[1], env: "prod", intent: "promote" });
  });

  it("does not offer an environment the release already runs in", async () => {
    const user = userEvent.setup();
    renderList(createReleasesFixtureApi());
    await ready();
    await user.click(screen.getByTestId(`release-menu-${V[0]}`));
    await user.click(screen.getByTestId("release-menu-promote"));
    const dialog = await screen.findByTestId("promote-target");
    expect(within(dialog).getByRole("radio", { name: /prod/ })).toBeDisabled();
    expect(screen.getByTestId("promote-target-confirm")).toHaveTextContent("Promote to e2e…");
  });

  it("offers a rollback only to an older release, naming the env and what runs there now", async () => {
    const user = userEvent.setup();
    const harness = renderList(createReleasesFixtureApi());
    await ready();
    await user.click(screen.getByTestId(`release-menu-${V[0]}`));
    expect(screen.queryByTestId("release-menu-rollback")).not.toBeInTheDocument();
    await user.click(screen.getByTestId(`release-menu-${V[0]}`));

    await user.click(screen.getByTestId(`release-menu-${V[2]}`));
    const rollback = screen.getByTestId("release-menu-rollback");
    expect(rollback).toHaveTextContent("Roll back prod to this");
    expect(rollback).toHaveTextContent("now 20261018.155157");
    expect(screen.getByTestId("release-menu-compare")).toHaveAttribute("href", expect.stringContaining(`/r/${V[2]}?tab=changes&base=${V[0]}`));
    await user.click(rollback);
    expect(harness.promote).toHaveBeenCalledWith({ version: V[2], env: "prod", intent: "rollback" });
  });

  it("disables promote and rollback, with the reason, when promoting is impossible here", async () => {
    const user = userEvent.setup();
    renderList(createReleasesFixtureApi(), { blocked: "Open the project in Reliant to promote." });
    await ready();
    await user.click(screen.getByTestId(`release-menu-${V[2]}`));
    expect(screen.getByTestId("release-menu-promote")).toBeDisabled();
    expect(screen.getByTestId("release-menu-promote")).toHaveAttribute("title", "Open the project in Reliant to promote.");
    expect(screen.getByTestId("release-menu-rollback")).toBeDisabled();
  });
});

describe("review edge cases", () => {
  it("offers no rollback when the env's live release is outside the loaded list", async () => {
    const user = userEvent.setup();
    // prod runs V[0], which this list does not hold: nothing here is known to be older than it.
    renderList({ ...createReleasesFixtureApi(), listReleases: async () => RELEASES.slice(1) });
    await ready();
    await user.click(screen.getByTestId(`release-menu-${V[2]}`));
    expect(screen.getByTestId("release-menu-promote")).toBeInTheDocument();
    expect(screen.queryByTestId("release-menu-rollback")).not.toBeInTheDocument();
  });

  it("shows a check recorded after the promotion as soon as the ledger has it", async () => {
    const harness = renderList(createReleasesFixtureApi());
    await ready();
    expect(row(V[1]!)).toHaveTextContent("2/2");
    // A gate recorded later changes neither the newest promotion's id nor the ledger's length.
    const ledger = PROMOTIONS["env-prod"]!;
    const index = ledger.findIndex((p) => p.releaseVersion === V[1]);
    const late = { ...ledger[index]!, gates: [...ledger[index]!.gates, { name: "smoke", status: "failed" as const, rawStatus: "", summary: "", url: "", recordedBy: "", finishedAt: new Date().toISOString(), source: "recorded" as const }] };
    act(() => {
      harness.client.setQueryData(releaseKeys.promotions("env-prod"), ledger.map((p, i) => (i === index ? late : p)));
    });
    await waitFor(() => expect(row(V[1]!)).toHaveTextContent("2/3"));
  });
});

describe("states", () => {
  it("loads with skeletons at the table's height", async () => {
    renderList(createReleasesFixtureApi({ state: "loading" }));
    expect(screen.getByTestId("releases")).toHaveAttribute("data-state", "loading");
    expect(screen.getByTestId("releases-pipeline-loading")).toBeInTheDocument();
  });

  it("says nothing was cut yet, and how to cut one", async () => {
    renderList(createReleasesFixtureApi({ state: "empty" }));
    await waitFor(() => expect(screen.getByTestId("releases")).toHaveAttribute("data-state", "empty"));
    expect(screen.getByText("No releases yet")).toBeInTheDocument();
    const state = screen.getByTestId("releases-getting-started");
    expect(within(state).getByTestId("releases-getting-started-about")).toHaveTextContent("every build of");
    expect(within(state).getByText("forge env deploy prod")).toBeInTheDocument();
    expect(within(state).getByText("forge env deploy prod --from staging")).toBeInTheDocument();
    // A local environment's missing releases are by design, and the page says so.
    expect(state).toHaveTextContent("A local environment (forge env up) runs your working tree and never has releases");
    expect(within(state).getByTestId("releases-getting-started-docs")).toHaveAttribute("href", "https://docs.reliantlabs.io/forge/environments-and-deploys");
  });

  it("says a local environment has no releases by design, not that none has run yet", async () => {
    // A local environment is never one of the release envs (ReleasesPages drops it).
    renderList(createReleasesFixtureApi(), { env: "laptop", scopeIsLocal: true });
    expect(await screen.findByTestId("releases-local-env")).toHaveTextContent("laptop runs locally from your working tree (forge env up), so it never has releases");
    expect(screen.queryByText(/No release has run in laptop yet/)).not.toBeInTheDocument();
  });

  it("explains a failed read and retries", async () => {
    const user = userEvent.setup();
    renderList(createReleasesFixtureApi({ state: "error" }));
    // forgeRetry retries a transport failure once before saying so.
    const error = await screen.findByTestId("releases-error", undefined, { timeout: 5000 });
    expect(error).toHaveTextContent("The release ledger could not be read");
    await user.click(within(error).getByRole("button", { name: /Try again|Retrying/ }));
  });

  it("falls back to the live view when promotion history is unreadable, and says so", async () => {
    renderList(createReleasesFixtureApi({ promotionsFail: true }));
    await ready();
    // Each env's promotion read fails on its own (after forgeRetry's retry),
    // so the note appears with the first and names the rest as they fail:
    // wait for the whole list, not just the note.
    await waitFor(
      () => expect(screen.getByTestId("releases-ledger-failed")).toHaveTextContent("prod, e2e, dev"),
      { timeout: 5000 },
    );
    expect(within(row(V[0]!)).getByText("live in prod")).toBeInTheDocument();
  });
});
