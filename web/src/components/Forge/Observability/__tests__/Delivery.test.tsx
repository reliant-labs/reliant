// Copyright (c) 2025 Reliant Labs

/**
 * Release markers, the before/after comparison and the Delivery (DORA) view,
 * against the fixture ledger (fixtureDelivery.ts): prod got v1.8.3 forty
 * minutes ago, staging three hours ago; prod rolled v1.8.1 back to v1.8.0
 * three hours after promoting it nine days ago; nothing records a commit time.
 */

import { describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { DEFINITIONS, fixtureLedger } from "../__fixtures__/fixtureDelivery";
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

describe("Delivery: DORA from the release ledger", () => {
  it("shows the four metrics with their definitions, and lead time as unknown with how much is unknown", async () => {
    const onCall = vi.fn();
    renderPanel(createFixtureApi({ onCall }), { view: "delivery" }, PROJECT_PLACE);
    await waitFor(() => expect(screen.getByTestId("obs-delivery")).toHaveAttribute("data-state", "ready"));
    expect(screen.queryByTestId("obs-range")).not.toBeInTheDocument();
    const [req] = callsOf(onCall, "GetDoraMetrics");
    expect(req).toMatchObject({ projectId: "shop", envIds: [], failureWindowHours: 24 });
    expect(req.window!.endMs - req.window!.startMs).toBe(30 * 86_400_000);

    expect(screen.getByTestId("obs-dora-frequency-value")).toHaveTextContent("1 every 2.3 days");
    expect(screen.getByTestId("obs-dora-frequency")).toHaveTextContent("13 deployments in 30 days");
    expect(screen.getByTestId("obs-dora-frequency")).toHaveTextContent("Not counted: 1 rollback, 1 re-apply.");

    const lead = screen.getByTestId("obs-dora-lead-time");
    expect(lead).toHaveAttribute("data-state", "unknown");
    expect(screen.getByTestId("obs-dora-lead-time-value")).toHaveTextContent("Unknown");
    expect(screen.getByTestId("obs-dora-lead-time-value")).not.toHaveTextContent(/\d/);
    expect(screen.getByTestId("obs-dora-lead-time-unknown")).toHaveTextContent("13 deployments across 5 releases");
    expect(lead).toHaveTextContent("unknown is not zero");

    expect(screen.getByTestId("obs-dora-failure-rate-value")).toHaveTextContent("7.7%");
    expect(screen.getByTestId("obs-dora-failure-rate")).toHaveTextContent("1 of 13 deployments rolled back within 24 hours");
    expect(screen.getByTestId("obs-dora-restore-value")).toHaveTextContent("3h");
    expect(screen.getByTestId("obs-dora-restore-source")).toHaveTextContent("Measured from rollbacks in the release ledger.");
    expect(screen.queryByTestId("obs-dora-restore-alerts")).not.toBeInTheDocument();

    // The server's definitions, verbatim.
    expect(screen.getByTestId("obs-dora-frequency-definition").textContent).toBe(DEFINITIONS.deploymentFrequency);
    expect(screen.getByTestId("obs-dora-lead-time-definition").textContent).toBe(DEFINITIONS.leadTime);
    expect(screen.getByTestId("obs-dora-failure-rate-definition").textContent).toBe(DEFINITIONS.changeFailureRate(24));
    expect(screen.getByTestId("obs-dora-restore-definition").textContent).toBe(DEFINITIONS.timeToRestore(24));
  });

  it("re-asks with the failure window and the period the reader picks", async () => {
    const user = userEvent.setup();
    const onCall = vi.fn();
    const harness = renderPanel(createFixtureApi({ onCall }), { view: "delivery" }, PROJECT_PLACE);
    await waitFor(() => expect(screen.getByTestId("obs-delivery")).toHaveAttribute("data-state", "ready"));
    await user.selectOptions(screen.getByTestId("obs-delivery-failure-hours"), "1");
    expect(harness.current().failureHours).toBe(1);
    await waitFor(() => expect(screen.getByTestId("obs-dora-failure-rate-value")).toHaveTextContent("0%"));
    expect(callsOf(onCall, "GetDoraMetrics").at(-1)!.failureWindowHours).toBe(1);
    expect(screen.getByTestId("obs-dora-restore-value")).toHaveTextContent("—");
    expect(screen.getByTestId("obs-dora-failure-rate-definition")).toHaveTextContent("within 1h0m0s");

    await user.selectOptions(screen.getByTestId("obs-delivery-window"), "7d");
    expect(harness.current().doraWindow).toBe("7d");
    await waitFor(() => expect(screen.getByTestId("obs-dora-frequency")).toHaveTextContent("7 deployments in 7 days"));
  });

  it("lists the promotions newest first, each a way into a comparison", async () => {
    const user = userEvent.setup();
    const harness = renderPanel(createFixtureApi(), { view: "delivery", doraWindow: "7d" }, PROJECT_PLACE);
    const list = await screen.findByTestId("obs-delivery-releases");
    await waitFor(() => expect(within(list).getAllByRole("listitem").length).toBeGreaterThan(3));
    const [newest] = within(list).getAllByRole("listitem");
    expect(newest).toHaveTextContent("v1.8.4-rc.1");
    await user.click(within(newest).getByRole("button", { name: "Compare before / after" }));
    expect(harness.current().compare).toMatch(/^env-dev~/);
    expect(await screen.findByTestId("obs-compare-back")).toHaveTextContent("Back to Delivery");
  });

  it("says nothing was promoted rather than showing zeroes, and offers a wider window", async () => {
    const user = userEvent.setup();
    const harness = renderPanel(createFixtureApi({ delivery: "empty" }), { view: "delivery" }, PROJECT_PLACE);
    const empty = await screen.findByTestId("obs-delivery-empty");
    expect(empty).toHaveTextContent("Nothing was promoted to shop in the last 30 days");
    expect(screen.queryByTestId("obs-dora-frequency")).not.toBeInTheDocument();
    await user.click(screen.getByTestId("obs-delivery-widen"));
    expect(harness.current().doraWindow).toBe("90d");
  });

  it("tells a non-member the org is closed to them", async () => {
    renderPanel(createFixtureApi({ delivery: "not-member" }), { view: "delivery" }, PROJECT_PLACE);
    expect(await screen.findByTestId("obs-delivery-not-member")).toHaveTextContent("You're not a member of this organization");
  });

  it("refuses an environment it will not name, and offers every environment instead", async () => {
    const user = userEvent.setup();
    const harness = renderPanel(createFixtureApi({ delivery: "env-denied" }), { view: "delivery", envs: ["prod"], f: ["deployment.environment.name:prod", "service.name:api"] }, PROJECT_PLACE);
    const frame = await screen.findByTestId("obs-delivery-env-unavailable");
    expect(frame).toHaveTextContent("Reliant doesn't say which");
    await user.click(within(frame).getByTestId("obs-delivery-show-all-envs"));
    expect(harness.current().envs).toBeUndefined();
    expect(harness.current().f).toEqual(["service.name:api"]);
    await waitFor(() => expect(screen.getByTestId("obs-delivery")).toHaveAttribute("data-state", "ready"));
  });

  it("on an env page, explains the refusal without a way to widen", async () => {
    renderPanel(createFixtureApi({ delivery: "env-denied" }), { view: "delivery" }, ENV_PLACE);
    const frame = await screen.findByTestId("obs-delivery-env-unavailable");
    expect(within(frame).queryByTestId("obs-delivery-show-all-envs")).not.toBeInTheDocument();
    expect(frame).toHaveTextContent("If prod was deleted");
  });

  it("shows a partly known lead time with what it leaves out", async () => {
    renderPanel(createFixtureApi({ leadTime: "partial" }), { view: "delivery" }, PROJECT_PLACE);
    await waitFor(() => expect(screen.getByTestId("obs-dora-lead-time")).toHaveAttribute("data-state", "partial"));
    expect(screen.getByTestId("obs-dora-lead-time")).toHaveTextContent("Median over 2 deployments");
    expect(screen.getByTestId("obs-dora-lead-time-unknown")).toHaveTextContent("11 more deployments (3 releases) recorded no commit time");
  });

  it("puts an alert-based time to restore beside the ledger's when the server sends one", async () => {
    renderPanel(createFixtureApi({ alertMttr: true }), { view: "delivery" }, PROJECT_PLACE);
    const alerts = await screen.findByTestId("obs-dora-restore-alerts");
    expect(alerts).toHaveTextContent("From alerts: 38m");
    expect(alerts).toHaveTextContent("median over 4 resolved incidents");
    expect(screen.queryByTestId("obs-dora-restore-ongoing")).not.toBeInTheDocument();
    expect(screen.getByTestId("obs-dora-restore-value")).toHaveTextContent("3h");
  });

  it("counts ongoing incidents beside the resolved ones, without folding them into the figure", async () => {
    renderPanel(createFixtureApi({ alertMttr: "ongoing" }), { view: "delivery" }, PROJECT_PLACE);
    const ongoing = await screen.findByTestId("obs-dora-restore-ongoing");
    expect(ongoing).toHaveTextContent("2 ongoing incidents");
    expect(screen.getByTestId("obs-dora-restore-alerts-value")).toHaveTextContent("From alerts: 38m");
    expect(screen.getByTestId("obs-dora-restore-value")).toHaveTextContent("3h");
  });

  it("says a quiet alert history is zero incidents, which is not the same as no alert figure", async () => {
    renderPanel(createFixtureApi({ alertMttr: "quiet" }), { view: "delivery" }, PROJECT_PLACE);
    await screen.findByTestId("obs-dora-restore-alerts");
    expect(screen.getByTestId("obs-dora-restore-alerts-value")).toHaveTextContent("From alerts: nothing resolved");
    expect(screen.getByTestId("obs-dora-restore-alerts-quiet")).toBeInTheDocument();
    expect(screen.queryByTestId("obs-dora-restore-ongoing")).not.toBeInTheDocument();
  });
});
