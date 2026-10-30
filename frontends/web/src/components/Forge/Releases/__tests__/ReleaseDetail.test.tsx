// Copyright (c) 2025 Reliant Labs

/**
 * One release against the fixture ledger: the header's status and the two
 * env-naming actions, Changes (commits with a truncated range said out
 * loud; images; objects; the deploy note; and the answers a release cut
 * before commit ranges or bundles gets), Rollout, Checks, Artifacts, the
 * promotion timeline, and not-found / error.
 */

import { describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { shortVersion } from "../releaseModel";
import { RELEASES, createReleasesFixtureApi } from "../__fixtures__/releaseFixtures";
import { renderDetail } from "./releasesTestUtils";

vi.mock("@tanstack/react-router", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-router")>()),
  useNavigate: () => vi.fn(),
}));

const V = RELEASES.map((release) => release.version);
const ready = async () => waitFor(() => expect(screen.getByTestId("release-detail")).toHaveAttribute("data-state", "ready"));

describe("the header", () => {
  it("says where the release is live, its scoped deploy, and who cut it from where", async () => {
    renderDetail(createReleasesFixtureApi(), V[0]!);
    await ready();
    const header = screen.getByRole("heading", { level: 1 });
    expect(header).toHaveTextContent(V[0]!);
    // Previews are counted, not named.
    expect(screen.getByText("Live on prod, dev and 2 previews")).toBeInTheDocument();
    expect(screen.getByText("scoped: admin-web, internal-console")).toBeInTheDocument();
    expect(screen.getByText("cut by sean from main")).toBeInTheDocument();
    // Prod runs it: the way back is to what prod ran before, named.
    expect(screen.getByRole("button", { name: `Roll back prod to ${shortVersion(V[1]!)}…` })).toBeInTheDocument();
  });

  it("rolls the environment back to what it ran before through the plan flow", async () => {
    const user = userEvent.setup();
    const harness = renderDetail(createReleasesFixtureApi(), V[0]!);
    await ready();
    await user.click(screen.getByRole("button", { name: `Roll back prod to ${shortVersion(V[1]!)}…` }));
    expect(harness.promote).toHaveBeenCalledWith({ version: V[1]!, env: "prod", intent: "rollback" });
  });

  it("offers to roll the scoped env back to an older release, naming it", async () => {
    const user = userEvent.setup();
    const harness = renderDetail(createReleasesFixtureApi(), V[2]!);
    await ready();
    expect(screen.getByText("rolled back · prod")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Roll back prod to this…" }));
    expect(harness.promote).toHaveBeenCalledWith({ version: V[2], env: "prod", intent: "rollback" });
  });

  it("offers no rollback when the env's live release is outside the loaded list", async () => {
    renderDetail({ ...createReleasesFixtureApi(), listReleases: async () => RELEASES.slice(1) }, V[2]!);
    await ready();
    expect(screen.queryByRole("button", { name: /Roll back/ })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Promote…" })).toBeEnabled();
  });

  it("names the scoped env in the rollback when it is not prod", async () => {
    renderDetail(createReleasesFixtureApi(), V[5]!, { env: "e2e" });
    await ready();
    expect(screen.getByRole("button", { name: "Roll back e2e to this…" })).toBeInTheDocument();
  });

  it("promotes through the target dialog", async () => {
    const user = userEvent.setup();
    const harness = renderDetail(createReleasesFixtureApi(), V[1]!);
    await ready();
    await user.click(screen.getByRole("button", { name: "Promote…" }));
    await user.click((await screen.findByTestId("promote-target-confirm")));
    expect(harness.promote).toHaveBeenCalledWith({ version: V[1], env: "prod", intent: "promote" });
  });

  it("disables the actions and says why when promoting is impossible here", async () => {
    renderDetail(createReleasesFixtureApi(), V[2]!, { blocked: "Open the project in Reliant to promote." });
    await ready();
    expect(screen.getByRole("button", { name: "Promote…" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Roll back prod to this…" })).toBeDisabled();
    expect(screen.getByTestId("release-promote-blocked")).toHaveTextContent("Open the project in Reliant to promote.");
  });
});

describe("Changes", () => {
  it("compares with what prod ran before it: commits, images, objects, the deploy note", async () => {
    renderDetail(createReleasesFixtureApi(), V[1]!);
    await ready();
    const changes = await screen.findByTestId("release-changes");
    await waitFor(() => expect(changes).toHaveAttribute("data-state", "ready"));
    expect(screen.getByTestId("release-base")).toHaveTextContent(shortVersion(V[3]!));
    expect(screen.getByTestId("release-compare-with")).toHaveTextContent("the release prod ran before");
    expect(screen.getByTestId("release-commits-panel")).toHaveTextContent("feat(identity): identity reconciler goes apply (#987)");
    expect(screen.getByTestId("release-images-panel")).toHaveTextContent(/1 changed · 4 unchanged|2 changed · 3 unchanged/);
    expect(screen.getByTestId("release-objects-panel")).toHaveTextContent("Modified");
    expect(screen.getByTestId("release-deploy-note")).toHaveTextContent("No deploy note.");
  });

  it("says a truncated range is truncated, never complete", async () => {
    renderDetail(createReleasesFixtureApi(), V[3]!, { search: { base: V[4] } });
    await ready();
    expect(await screen.findByTestId("release-commits-truncated")).toHaveTextContent("showing 4 of an unknown number of commits");
  });

  it("says the commits are unknown when nothing between them recorded any, and the objects when a bundle is missing", async () => {
    renderDetail(createReleasesFixtureApi(), V[6]!);
    await ready();
    expect(await screen.findByTestId("release-commits-unrecorded")).toHaveTextContent("nothing cut between them recorded its commits");
    expect(screen.getByTestId("release-objects-unavailable")).toHaveTextContent("No bundle was recorded");
  });

  it("calls a range composed by cut time approximate, and truncated", async () => {
    renderDetail(createReleasesFixtureApi(), V[4]!, { search: { base: V[7] } });
    await ready();
    expect(await screen.findByTestId("release-commits-by-cut-time")).toHaveTextContent("Approximate");
    expect(screen.getByTestId("release-commits-truncated")).toBeInTheDocument();
  });

  it("says the first release has nothing to compare with", async () => {
    renderDetail(createReleasesFixtureApi(), V[7]!);
    await ready();
    expect(screen.getByTestId("release-changes")).toHaveAttribute("data-state", "first");
    expect(screen.getByTestId("release-compare-with")).toHaveTextContent("nothing earlier to compare it with");
  });

  it("changes what it compares with", async () => {
    const user = userEvent.setup();
    const harness = renderDetail(createReleasesFixtureApi(), V[1]!);
    await ready();
    await user.click(await screen.findByTestId("release-base"));
    await user.click(screen.getByRole("menuitemradio", { name: new RegExp(shortVersion(V[4]!)) }));
    expect(harness.current().base).toBe(V[4]);
    expect(screen.getByTestId("release-compare-with")).toHaveTextContent("chosen by you");
  });

  it("explains a failed comparison and leaves the rest of the page alone", async () => {
    renderDetail(createReleasesFixtureApi({ compareFails: true }), V[1]!);
    await ready();
    expect(await screen.findByTestId("release-changes-error", undefined, { timeout: 5000 })).toHaveTextContent("compare timed out");
    expect(screen.getByTestId("release-timeline")).toBeInTheDocument();
  });
});

describe("the other tabs", () => {
  it("shows the rollout per workload in the env the reader picks", async () => {
    const user = userEvent.setup();
    const harness = renderDetail(createReleasesFixtureApi(), V[0]!, { search: { tab: "rollout" } });
    await ready();
    const rollout = await screen.findByTestId("release-rollout");
    expect(rollout).toHaveTextContent("Rollout in prod");
    expect(rollout).toHaveTextContent("Converged");
    expect(rollout).toHaveTextContent("admin-server");
    await user.click(within(screen.getByTestId("release-env-picker")).getByRole("button", { name: "dev" }));
    expect(harness.current().in).toBe("dev");
  });

  it("lists forge-applied slots in their own group, by their reason, never as unobservable", async () => {
    const reason = "applied by forge (not observed by the control plane)";
    renderDetail(
      createReleasesFixtureApi({ forgeApplied: [{ name: "edge-us/web/api", artifact: "web", pinnedDigest: "sha256:abc123", reason }] }),
      V[0]!,
      { search: { tab: "rollout" } }
    );
    await ready();
    const rollout = await screen.findByTestId("release-rollout");
    expect(rollout).toHaveTextContent("1 applied by forge");
    const group = within(rollout).getByTestId("release-rollout-forge-applied");
    expect(within(group).getByRole("table", { name: "Applied by forge" })).toBeInTheDocument();
    expect(group).toHaveTextContent("edge-us/web/api");
    expect(group).toHaveTextContent(reason);
    expect(group).not.toHaveTextContent("Cannot be observed");
    // The judged workloads keep their own table; the slot is not among them.
    expect(within(rollout).getByRole("table", { name: "Workloads" })).not.toHaveTextContent("edge-us/web/api");
  });

  it("lists every check with its result, a failed one tinted", async () => {
    renderDetail(createReleasesFixtureApi(), V[2]!, { search: { tab: "checks" } });
    await ready();
    const checks = await screen.findByTestId("release-checks");
    expect(checks).toHaveTextContent("smoke");
    expect(checks).toHaveTextContent("admin-server /healthz returned 503 for 6 minutes");
    expect(within(checks).getByText("failed")).toHaveClass("text-danger-ink");
  });

  it("says when no check was recorded", async () => {
    renderDetail(createReleasesFixtureApi(), V[7]!, { search: { tab: "checks" } });
    await ready();
    expect(screen.getByTestId("release-checks-none")).toBeInTheDocument();
  });

  it("lists every artifact's digest and the bundle the promotion applied", async () => {
    renderDetail(createReleasesFixtureApi(), V[0]!, { search: { tab: "artifacts" } });
    await ready();
    expect(screen.getByTestId("release-artifacts")).toHaveTextContent("workspace-base");
    await waitFor(() => expect(screen.getByTestId("release-bundle")).toHaveTextContent("Reference"));
  });
});

describe("the rail and the edges", () => {
  it("links Activity pre-filtered to the release", async () => {
    renderDetail(createReleasesFixtureApi(), V[1]!);
    await ready();
    expect(within(screen.getByTestId("release-history")).getByRole("link", { name: /Activity in prod/ })).toHaveAttribute(
      "href",
      `/activity?env=prod&target=release:${V[1]}`
    );
  });

  it("keeps the env picker on Changes, whose objects are env-bound", async () => {
    renderDetail(createReleasesFixtureApi(), V[0]!);
    await ready();
    expect(screen.getByTestId("release-env-picker")).toBeInTheDocument();
  });

  it("tells the promotion story", async () => {
    renderDetail(createReleasesFixtureApi(), V[2]!);
    await ready();
    const timeline = screen.getByTestId("release-timeline");
    expect(within(timeline).getAllByRole("listitem").map((li) => li.querySelector("[data-part=title]")?.textContent)).toEqual([
      "Cut",
      "Promoted to dev",
      "Promoted to prod",
      "Rolled back in prod",
      "Superseded in dev",
    ]);
    expect(screen.getByTestId("release-facts")).toHaveTextContent("Forge version");
  });

  it("says a release does not exist rather than showing an empty page", async () => {
    renderDetail(createReleasesFixtureApi(), "v9.9.9");
    await waitFor(() => expect(screen.getByTestId("release-detail")).toHaveAttribute("data-state", "not-found"));
    expect(screen.getByText("No such release")).toBeInTheDocument();
  });

  it("explains a failed read", async () => {
    renderDetail(createReleasesFixtureApi({ state: "error" }), V[0]!);
    await waitFor(() => expect(screen.getByTestId("release-detail")).toHaveAttribute("data-state", "error"), { timeout: 5000 });
    expect(screen.getByText("This release could not be read")).toBeInTheDocument();
  });
});
