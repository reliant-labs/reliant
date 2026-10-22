// Copyright (c) 2025 Reliant Labs

/**
 * Release markers and the before/after comparison (the DORA figures moved to
 * the console's Releases page, D9), against the fixture ledger
 * (fixtureDelivery.ts): prod got v1.8.3 forty
 * minutes ago, staging three hours ago; prod rolled v1.8.1 back to v1.8.0
 * three hours after promoting it nine days ago; nothing records a commit time.
 */

import { describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { fixtureLedger } from "../__fixtures__/fixtureDelivery";
import { createFixtureApi } from "../__fixtures__/fixtureApi";
import { compareParam } from "../deliveryScope";
import { ENV_PLACE, PROJECT_PLACE, renderPanel } from "./obsTestUtils";

vi.mock("uplot", async () => (await import("./obsMocks")).obsMocks.uplot());
vi.mock("@tanstack/react-virtual", async () => (await import("./obsMocks")).obsMocks.virtual());

type Req = { envIds: string[]; projectId?: string; window?: { startMs: number; endMs: number }; failureWindowHours?: number };
const callsOf = (onCall: ReturnType<typeof vi.fn>, rpc: string) => onCall.mock.calls.filter(([name]) => name === rpc).map(([, req]) => req as Req);

describe("release markers follow the scope", () => {
  it("asks the ledger for the picked environments and the project, over the panel's window", async () => {
    const onCall = vi.fn();
    renderPanel(createFixtureApi({ onCall }), { view: "overview", envs: ["staging"], range: "6h" }, PROJECT_PLACE);
    await waitFor(() => expect(screen.getByTestId("obs-releases")).toHaveAttribute("data-status", "ready"));
    const strip = screen.getByTestId("obs-releases");
    const [req] = callsOf(onCall, "ListReleases");
    expect(req).toMatchObject({ orgId: "org-fixture", projectId: "shop", envIds: ["env-staging"] });
    expect(req.window!.endMs - req.window!.startMs).toBe(6 * 3_600_000);
    expect(strip).toHaveAttribute("data-count", "1");
  });

  it("narrows to an environment named by a filter, and lists who promoted what from which commit", async () => {
    const user = userEvent.setup();
    const onCall = vi.fn();
    renderPanel(createFixtureApi({ onCall }), { view: "overview", f: ["deployment.environment.name:prod"] }, PROJECT_PLACE);
    await user.click(await screen.findByTestId("obs-releases-toggle"));
    expect(callsOf(onCall, "ListReleases")[0].envIds).toEqual(["env-prod"]);
    const row = screen.getByTestId("obs-releases").querySelector("li")!;
    expect(row).toHaveTextContent("prod");
    expect(row).toHaveTextContent("v1.8.3");
    expect(row).toHaveTextContent("by Ada Lovelace");
    // The commit, short, as it is usually read.
    expect(row.textContent).toMatch(/v1\.8\.3[0-9a-f]{7}by/);
  });

  it("says when the filters leave no environment", async () => {
    renderPanel(createFixtureApi(), { view: "overview", envs: ["prod"], f: ["deployment.environment.name:dev"] }, PROJECT_PLACE);
    expect(await screen.findByTestId("obs-releases")).toHaveAttribute("data-status", "excluded");
  });

  it("says when the list was cut to the newest", async () => {
    const user = userEvent.setup();
    renderPanel(createFixtureApi({ releaseLimit: 1 }), { view: "overview", range: "6h" }, PROJECT_PLACE);
    await user.click(await screen.findByTestId("obs-releases-toggle"));
    expect(screen.getByTestId("obs-releases-toggle")).toHaveTextContent("The newest 1 release");
    expect(screen.getByTestId("obs-releases-truncated")).toHaveTextContent("only the newest 1 are listed");
  });

  it("explains a refusal in one line and leaves the charts alone", async () => {
    renderPanel(createFixtureApi({ delivery: "not-member" }), { view: "overview" }, PROJECT_PLACE);
    await waitFor(() => expect(screen.getByTestId("obs-releases")).toHaveAttribute("data-kind", "not-member"));
    expect(screen.getByTestId("obs-releases")).toHaveTextContent("you're not a member of this organization");
    expect(screen.getByTestId("obs-overview")).toBeInTheDocument();
  });
});

describe("before and after a promotion", () => {
  it("opens from a marker, compares RED, versions and resources over equal windows, and goes back", async () => {
    const user = userEvent.setup();
    const onCall = vi.fn();
    const harness = renderPanel(createFixtureApi({ onCall }), { view: "overview" }, ENV_PLACE);
    await user.click(await screen.findByTestId("obs-releases-toggle"));
    const row = screen.getByTestId("obs-releases").querySelector("li")!;
    await user.click(within(row).getByRole("button", { name: "Compare before / after" }));
    expect(harness.current().compare).toMatch(/^env-prod~\d+~promo-\d+$/);

    const compare = await screen.findByTestId("obs-compare");
    await waitFor(() => expect(compare).toHaveAttribute("data-state", "ready"));
    expect(screen.getByTestId("obs-compare-header")).toHaveTextContent("Before and after v1.8.3 in prod");
    expect(screen.getByTestId("obs-compare-header")).toHaveTextContent("by Ada Lovelace");
    expect(screen.getByTestId("obs-compare-replaced")).toHaveTextContent("replaced v1.8.2");
    expect(screen.getByTestId("obs-compare-windows")).toHaveAttribute("data-clamped", "none");
    // No range picker: the windows are the comparison's own.
    expect(screen.queryByTestId("obs-range")).not.toBeInTheDocument();

    await waitFor(() => expect(screen.getByTestId("obs-compare-p95-api")).toHaveAttribute("data-movement", "up-bad"));
    expect(screen.getByTestId("obs-compare-p95-api")).toHaveTextContent("+45%");
    expect(screen.getByTestId("obs-compare-errors-api")).toHaveAttribute("data-movement", "up-bad");
    expect(screen.getByTestId("obs-compare-versions-after-api")).toHaveTextContent(/v1\.8\.3\s*97%/);
    expect(screen.getByTestId("obs-compare-memory-api")).toHaveAttribute("data-movement", "up-bad");
    expect(screen.getByTestId("obs-compare-workload-worker")).toBeInTheDocument();

    // Every compare query: the promotion's environment, two 30-minute windows meeting at the promotion.
    const panels = onCall.mock.calls.filter(([rpc, req]) => rpc === "QueryPanel" && String(req.panel.id).startsWith("compare-"));
    expect(panels).toHaveLength(6);
    const before = panels.find(([, req]) => req.panel.id === "compare-red-before")![1];
    const after = panels.find(([, req]) => req.panel.id === "compare-red-after")![1];
    expect(before.envIds).toEqual(["env-prod"]);
    expect(before.window.endMs).toBe(after.window.startMs);
    expect(after.window.endMs - after.window.startMs).toBe(30 * 60_000);
    expect(before.window.endMs - before.window.startMs).toBe(30 * 60_000);

    // An hour is longer than the release has run: both sides shrink to what has passed.
    await user.click(screen.getByTestId("obs-compare-window-1h"));
    expect(harness.current().cmpWindow).toBe("1h");
    await waitFor(() => expect(screen.getByTestId("obs-compare-windows")).toHaveAttribute("data-clamped", "now"));
    expect(screen.getByTestId("obs-compare-clamped")).toHaveTextContent("Only 40m has passed since the promotion");

    await user.click(screen.getByTestId("obs-compare-back"));
    expect(harness.current().compare).toBeUndefined();
    expect(await screen.findByTestId("obs-overview")).toBeInTheDocument();
  });

  it("finds a rollback from its link, and stops the before side at the release it undid", async () => {
    const rollback = fixtureLedger(Date.now()).find((p) => p.kind === "rollback")!;
    renderPanel(createFixtureApi(), { view: "overview", compare: compareParam(rollback), cmpWindow: "6h" }, PROJECT_PLACE);
    const header = await screen.findByTestId("obs-compare-header");
    expect(header).toHaveTextContent("Before and after the rollback to v1.8.0 in prod");
    expect(screen.getByTestId("obs-compare-replaced")).toHaveTextContent("replaced v1.8.1");
    expect(screen.getByTestId("obs-compare-windows")).toHaveAttribute("data-clamped", "previous");
    expect(screen.getByTestId("obs-compare-clamped")).toHaveTextContent("v1.8.1 had been promoted only 3h earlier");
  });

  it("says so when the promotion is not in the ledger", async () => {
    renderPanel(createFixtureApi(), { view: "overview", compare: "env-prod~1700000000000~promo-nope" }, ENV_PLACE);
    expect(await screen.findByTestId("obs-compare-not-found")).toBeInTheDocument();
  });
});

