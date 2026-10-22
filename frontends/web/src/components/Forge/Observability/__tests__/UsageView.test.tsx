// Copyright (c) 2025 Reliant Labs

/**
 * Usage: the allowance bar, the money, and what the gateway is doing about it
 * — one assertion per rung the reader can be on.
 */

import { describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Code } from "@connectrpc/connect";

import { createFixtureApi, fixtureUsage, FIXTURE_ORG } from "../__fixtures__/fixtureApi";
import type { ObservabilityUsage } from "@/services/controlPlane/observability/types";
import { usageNotice } from "../usageModel";
import { UsageView } from "../UsageView";
import { renderPanel, renderWithApi } from "./obsTestUtils";

vi.mock("uplot", async () => (await import("./obsMocks")).obsMocks.uplot());
vi.mock("@tanstack/react-virtual", async () => (await import("./obsMocks")).obsMocks.virtual());

const GIB = 2 ** 30;

function renderUsage(usage: Partial<ObservabilityUsage> = {}, failWith?: Code) {
  const onCall = vi.fn();
  const api = createFixtureApi({ usage, onCall, failWith });
  renderWithApi(api, <UsageView orgId={FIXTURE_ORG} subject="prod" />);
  return { onCall };
}

describe("UsageView", () => {
  it("shows month-to-date against the allowance, the projection, and that it lags", async () => {
    const { onCall } = renderUsage();
    expect(await screen.findByTestId("obs-usage-used")).toHaveTextContent("6.4 GB");
    expect(screen.getByTestId("obs-usage-used")).toHaveTextContent("of 10 GB");
    expect(screen.getByTestId("obs-usage-percent")).toHaveTextContent("64%");
    expect(screen.getByRole("progressbar", { name: /allowance/i })).toHaveAttribute("aria-valuenow", "64");
    expect(screen.getByTestId("obs-usage-projected")).toHaveTextContent("12.8 GB");
    expect(screen.getByTestId("obs-usage-overage")).toHaveTextContent("$0.00");
    expect(screen.getByTestId("obs-usage-measured")).toHaveTextContent(/Measured through .*behind real time/);
    expect(screen.queryByTestId("obs-usage-notice")).not.toBeInTheDocument();
    expect(onCall).toHaveBeenCalledWith("GetObservabilityUsage", { orgId: FIXTURE_ORG });
  });

  it("names customer metadata and browser as their own lines, inside the signals", async () => {
    renderUsage();
    await screen.findByTestId("obs-usage");
    const signals = screen.getByTestId("obs-usage-signals");
    for (const label of ["Logs", "Traces", "Metrics", "Profiles"]) expect(signals).toHaveTextContent(label);
    const metadata = screen.getByTestId("obs-usage-metadata");
    expect(metadata).toHaveTextContent("Customer metadata");
    expect(metadata).toHaveTextContent("Billed like any other data");
    expect(metadata).toHaveTextContent("921.6 MB");
    expect(within(screen.getByTestId("obs-usage-browser")).getByText(/up to 2 GB a month/)).toBeInTheDocument();
    // "Infrastructure" is Reliant's own and is never shown to a customer.
    expect(screen.queryByText(/infrastructure/i)).not.toBeInTheDocument();
  });

  it("says when the browser cap has cut what is billed", async () => {
    renderUsage({ browserBytes: 3 * GIB, browserBilledBytes: 2 * GIB, browserCapBytes: 2 * GIB });
    expect(await screen.findByTestId("obs-usage-browser")).toHaveTextContent("Only 2 GB of 3 GB is billed");
  });

  it("lists the largest services and drops zero rows", async () => {
    renderUsage();
    const list = await screen.findByTestId("obs-usage-services");
    expect(list).toHaveTextContent("api");
    expect(list).toHaveTextContent("checkout");
  });

  it("warns at 80% and says what happens next for an unfunded org", async () => {
    renderUsage({ usedBytes: 8.5 * GIB });
    const notice = await screen.findByTestId("obs-usage-notice");
    expect(notice).toHaveAttribute("data-tone", "warning");
    expect(notice).toHaveTextContent("85% of your 10 GB allowance");
    expect(notice).toHaveTextContent("Settings → Billing");
  });

  it("says data is being dropped at over_100", async () => {
    renderUsage({ state: "over_100", usageState: "over_100", usedBytes: 11 * GIB });
    const notice = await screen.findByTestId("obs-usage-notice");
    expect(notice).toHaveAttribute("data-tone", "warning");
    expect(notice).toHaveTextContent(/some telemetry is being dropped/);
    expect(notice).toHaveTextContent("Settings → Billing");
  });

  it("says most telemetry is dropped at over_150", async () => {
    renderUsage({ state: "over_150", usageState: "over_150", usedBytes: 16 * GIB });
    const notice = await screen.findByTestId("obs-usage-notice");
    expect(notice).toHaveAttribute("data-tone", "destructive");
    expect(notice).toHaveTextContent(/most telemetry is being dropped/);
  });

  it("a funded org past its allowance is billed, not dropped", async () => {
    renderUsage({ state: "ok", usageState: "over_100", funded: true, usedBytes: 12 * GIB, overageCents: 50 });
    const notice = await screen.findByTestId("obs-usage-notice");
    expect(notice).toHaveAttribute("data-tone", "info");
    expect(notice).toHaveTextContent("billed at $0.25/GB");
    expect(notice).toHaveTextContent("$0.50 so far");
    expect(notice).toHaveTextContent("Nothing is dropped");
    expect(screen.getByTestId("obs-usage-overage")).toHaveTextContent("$0.50");
  });

  it("reaching the spend cap is stated as the reason for the drop", async () => {
    renderUsage({ state: "over_100", usageState: "over_100", funded: true, spendCapReached: true, usedBytes: 300 * GIB });
    expect(await screen.findByTestId("obs-usage-notice")).toHaveTextContent("reached your $50.00 monthly spend cap");
  });

  it("an exempt org sees usage but no charge", async () => {
    renderUsage({ exempt: true, usageState: "over_100", usedBytes: 40 * GIB, overageCents: 0, projectedOverageCents: 0 });
    expect(await screen.findByTestId("obs-usage-notice")).toHaveTextContent("not billed for this organization");
    expect(screen.getByTestId("obs-usage-overage")).toHaveTextContent("Not billed");
    expect(screen.getByTestId("obs-usage-cap")).toHaveTextContent("—");
  });

  it("explains the wait before the first hour is metered", async () => {
    renderUsage({ measuredThroughMs: null, usedBytes: 0, bySignal: [], byService: [] });
    expect(await screen.findByTestId("obs-usage-measured")).toHaveTextContent(/within about an hour/);
  });

  it("shows an error with a retry rather than a zero when the read fails", async () => {
    renderUsage({}, Code.ResourceExhausted);
    expect(await screen.findByRole("alert")).toBeInTheDocument();
    expect(screen.queryByTestId("obs-usage-used")).not.toBeInTheDocument();
  });
});

describe("usageNotice", () => {
  const base = fixtureUsage(Date.UTC(2026, 9, 12));
  it("ranks what the gateway is doing above what usage implies", () => {
    expect(usageNotice({ ...base, state: "over_150", usageState: "over_150" })?.tone).toBe("destructive");
    expect(usageNotice({ ...base, state: "over_100", usageState: "over_150" })?.tone).toBe("warning");
    expect(usageNotice({ ...base, state: "ok", usageState: "over_100", funded: true })?.tone).toBe("info");
  });
  it("is silent under 80%", () => {
    expect(usageNotice({ ...base, usedBytes: 7.9 * GIB })).toBeNull();
    expect(usageNotice({ ...base, usedBytes: 8 * GIB })?.tone).toBe("warning");
  });
  it("exempt wins over everything", () => {
    expect(usageNotice({ ...base, exempt: true, state: "over_150" })?.title).toMatch(/not billed/);
  });
});

describe("Usage tab", () => {
  it("is reachable from the panel and works before the tenant store exists", async () => {
    const user = userEvent.setup();
    const harness = renderPanel(createFixtureApi({ failWith: undefined }), {});
    await user.click(await screen.findByTestId("obs-view-usage"));
    expect(harness.current().view).toBe("usage");
    expect(await screen.findByTestId("obs-usage")).toBeInTheDocument();
  });

  it("is not hidden by the not-set-up state of the other views", async () => {
    const api = createFixtureApi({ usage: {} });
    const notSet = {
      ...api,
      getServiceOverview: async () => {
        const { ConnectError } = await import("@connectrpc/connect");
        throw new ConnectError("observability is not set up for this organization yet", Code.FailedPrecondition);
      },
    };
    renderPanel(notSet, { view: "usage" });
    await waitFor(() => expect(screen.getByTestId("obs-usage")).toBeInTheDocument());
    expect(screen.queryByTestId("obs-not-set-up")).not.toBeInTheDocument();
  });
});
