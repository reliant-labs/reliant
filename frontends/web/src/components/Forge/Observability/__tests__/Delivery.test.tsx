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

describe("before and after a promotion (GetReleaseHealth)", () => {
  it("opens from a marker, asks the server for the promotion, renders its FAIL verdict and findings, and goes back", async () => {
    const user = userEvent.setup();
    const onCall = vi.fn();
    const harness = renderPanel(createFixtureApi({ onCall }), { view: "overview" }, ENV_PLACE);
    await user.click(await screen.findByTestId("obs-releases-toggle"));
    const row = screen.getByTestId("obs-releases").querySelector("li")!;
    await user.click(within(row).getByRole("button", { name: "Compare before / after" }));
    expect(harness.current().compare).toMatch(/^env-prod~\d+~promo-\d+$/);

    const compare = await screen.findByTestId("obs-compare");
    await waitFor(() => expect(compare).toHaveAttribute("data-state", "ready"));
    expect(compare).toHaveAttribute("data-verdict", "fail");
    expect(screen.getByTestId("obs-compare-header")).toHaveTextContent("Before and after v1.8.3 in prod");
    expect(screen.getByTestId("obs-compare-replaced")).toHaveTextContent("replaced v1.8.2");
    // No range picker: the windows are the server's.
    expect(screen.queryByTestId("obs-range")).not.toBeInTheDocument();

    // One request: the promotion's environment and id, the default 15-minute width. No client window maths.
    const [req] = callsOf(onCall, "GetReleaseHealth") as unknown as { envIds: string[]; promotionId: string; windowMs: number }[];
    expect(req.envIds).toEqual(["env-prod"]);
    expect(req.promotionId).toBe(harness.current().compare!.split("~").pop());
    expect(req.windowMs).toBe(15 * 60_000);

    expect(screen.getByTestId("obs-compare-verdict-title")).toHaveTextContent("This release made things worse");
    const findings = screen.getAllByTestId("obs-compare-finding");
    expect(findings.map((f) => f.getAttribute("data-code"))).toEqual(["error_rate", "new_issue", "regressed_issue", "alert_firing", "latency", "workload_restarts"]);
    expect(findings[0]).toHaveTextContent("1.2% vs 0.5%");
    expect(screen.getByTestId("obs-compare-p95-api")).toHaveAttribute("data-movement", "up-bad");
    expect(screen.getByTestId("obs-compare-p95-api")).toHaveTextContent("+45%");
    expect(screen.getByTestId("obs-compare-share-api")).toHaveTextContent("97");
    expect(screen.getByTestId("obs-compare-issue-iss-orders-deadline")).toHaveAttribute("data-kind", "new");
    expect(screen.getByTestId("obs-compare-issue-iss-checkout-total")).toHaveAttribute("data-kind", "regressed");
    expect(screen.getByTestId("obs-compare-alert-HighErrorRate")).toBeInTheDocument();
    expect(screen.getByTestId("obs-compare-restarts-api")).toHaveTextContent("OOMKilled");
    expect(screen.getByTestId("obs-compare-threshold-max_error_rate_increase")).toHaveTextContent("declared in the release shape");
    expect(screen.queryByTestId("obs-compare-not-evaluated")).not.toBeInTheDocument();

    // Resources are still a panel query, over the server's own windows.
    await waitFor(() => expect(screen.getByTestId("obs-compare-memory-api")).toHaveAttribute("data-movement", "up-bad"));
    const panels = onCall.mock.calls.filter(([rpc, r]) => rpc === "QueryPanel" && String(r.panel.id).startsWith("compare-"));
    expect(panels.map(([, r]) => r.panel.id).sort()).toEqual(["compare-resources-after", "compare-resources-before"]);

    await user.click(screen.getByTestId("obs-compare-window-1h"));
    expect(harness.current().cmpWindow).toBe("1h");
    await waitFor(() => expect(callsOf(onCall, "GetReleaseHealth").at(-1)).toMatchObject({ windowMs: 3_600_000 }));

    await user.click(screen.getByTestId("obs-compare-back"));
    expect(harness.current().compare).toBeUndefined();
    expect(await screen.findByTestId("obs-overview")).toBeInTheDocument();
  });

  it("links an issue the release opened into Errors", async () => {
    const user = userEvent.setup();
    const promo = fixtureLedger(Date.now()).filter((p) => p.envId === "env-prod").at(-1)!;
    const harness = renderPanel(createFixtureApi(), { view: "overview", compare: compareParam(promo) }, ENV_PLACE);
    await user.click(await screen.findByTestId("obs-compare-issue-open-iss-orders-deadline"));
    expect(harness.current()).toMatchObject({ view: "errors", issue: "iss-orders-deadline" });
    expect(harness.current().compare).toBeUndefined();
    expect(await screen.findByTestId("obs-issue")).toBeInTheDocument();
  });

  it.each([
    ["pass", "No regression found"],
    ["inconclusive", "Not proven either way"],
  ] as const)("renders a %s verdict", async (health, title) => {
    const promo = fixtureLedger(Date.now()).filter((p) => p.envId === "env-prod").at(-1)!;
    renderPanel(createFixtureApi({ health }), { view: "overview", compare: compareParam(promo) }, ENV_PLACE);
    expect(await screen.findByTestId("obs-compare-verdict-title")).toHaveTextContent(title);
    expect(screen.getByTestId("obs-compare-verdict")).toHaveAttribute("data-verdict", health);
    if (health === "inconclusive") expect(screen.getByTestId("obs-compare-open")).toBeInTheDocument();
  });

  it("says what a PASS does not cover (not_evaluated)", async () => {
    const promo = fixtureLedger(Date.now()).filter((p) => p.envId === "env-prod").at(-1)!;
    renderPanel(createFixtureApi({ health: "not-evaluated" }), { view: "overview", compare: compareParam(promo) }, ENV_PLACE);
    const note = await screen.findByTestId("obs-compare-not-evaluated");
    expect(note).toHaveTextContent("Not checked: Firing alerts, New issues, Regressed issues");
    expect(note).toHaveTextContent("the pass doesn't cover them");
    expect(screen.getByTestId("obs-compare-issues-empty")).toHaveTextContent("Not checked");
  });

  it("explains an older control plane inline, not as an error page", async () => {
    const promo = fixtureLedger(Date.now()).filter((p) => p.envId === "env-prod").at(-1)!;
    renderPanel(createFixtureApi({ health: "unimplemented" }), { view: "overview", compare: compareParam(promo) }, ENV_PLACE);
    expect(await screen.findByTestId("obs-compare-unavailable")).toHaveTextContent("Release health isn't available on this control plane");
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("says so when the promotion is not in the ledger", async () => {
    renderPanel(createFixtureApi(), { view: "overview", compare: "env-prod~1700000000000~promo-nope" }, ENV_PLACE);
    expect(await screen.findByTestId("obs-compare-not-found")).toBeInTheDocument();
  });
});
