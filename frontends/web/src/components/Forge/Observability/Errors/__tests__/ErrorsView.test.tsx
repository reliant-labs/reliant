// Copyright (c) 2025 Reliant Labs

/**
 * The Errors view against the issues fixture (__fixtures__/fixtureIssues.ts):
 * prod has a regressed browser TypeError, a new server timeout from v1.8.3 and
 * an old worker connection error; staging an unsymbolicated chunk error and a
 * resolved nil pointer; dev an ignored deprecation.
 */

import { describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { createFixtureApi } from "../../__fixtures__/fixtureApi";
import { ENV_PLACE, PROJECT_PLACE, renderPanel } from "../../__tests__/obsTestUtils";

vi.mock("uplot", async () => (await import("../../__tests__/obsMocks")).obsMocks.uplot());
vi.mock("@tanstack/react-virtual", async () => (await import("../../__tests__/obsMocks")).obsMocks.virtual());

type ListReq = { envIds: string[]; statuses: string[]; platform?: string; services?: string[]; sort: string; text?: string; pageToken?: string };
const listCalls = (onCall: ReturnType<typeof vi.fn>) => onCall.mock.calls.filter(([rpc]) => rpc === "ListIssues").map(([, req]) => req as ListReq);
const rowIds = () => screen.getAllByTestId(/^obs-issue-iss-/).map((row) => row.getAttribute("data-testid")!.replace("obs-issue-", ""));

describe("the issue list", () => {
  it("lists the open issues of the scope — browser and server — with the server's open default", async () => {
    const onCall = vi.fn();
    renderPanel(createFixtureApi({ onCall }), { view: "errors" }, ENV_PLACE);
    await screen.findByTestId("obs-errors-list");
    expect(rowIds()).toEqual(["iss-orders-deadline", "iss-checkout-total", "iss-worker-conns"]);
    const [req] = listCalls(onCall);
    expect(req).toMatchObject({ envIds: ["env-prod"], statuses: [], sort: "last" });
    const regressed = screen.getByTestId("obs-issue-iss-checkout-total");
    expect(regressed).toHaveAttribute("data-status", "regressed");
    expect(regressed).toHaveTextContent("Browser");
    expect(regressed).toHaveTextContent("sessions");
    expect(screen.getByTestId("obs-issue-iss-orders-deadline")).toHaveTextContent("traces");
    expect(screen.getByTestId("obs-errors-status-open")).toHaveAttribute("aria-pressed", "true");
  });

  it("filters by status, platform and sort, all kept in the URL", async () => {
    const user = userEvent.setup();
    const onCall = vi.fn();
    const harness = renderPanel(createFixtureApi({ onCall }), { view: "errors" }, PROJECT_PLACE);
    await screen.findByTestId("obs-errors-list");

    await user.click(screen.getByTestId("obs-errors-status-resolved"));
    await user.click(screen.getByTestId("obs-errors-status-ignored"));
    expect(harness.current().istatus).toBe("resolved,ignored");
    await waitFor(() => expect(rowIds()).toEqual(["iss-dev-deprecated", "iss-ledger-nil"]));
    expect(listCalls(onCall).at(-1)!.statuses).toEqual(["resolved", "ignored"]);

    await user.click(screen.getByTestId("obs-errors-status-open"));
    expect(harness.current().istatus).toBeUndefined();
    await user.selectOptions(screen.getByTestId("obs-errors-platform"), "browser");
    expect(harness.current().platform).toBe("browser");
    await waitFor(() => expect(rowIds()).toEqual(["iss-checkout-total", "iss-cart-chunk"]));
    await user.selectOptions(screen.getByTestId("obs-errors-sort"), "events");
    expect(harness.current().isort).toBe("events");
    await waitFor(() => expect(listCalls(onCall).at(-1)).toMatchObject({ platform: "browser", sort: "events" }));
  });

  it("searches title and culprit through the server", async () => {
    const user = userEvent.setup();
    const onCall = vi.fn();
    const harness = renderPanel(createFixtureApi({ onCall }), { view: "errors" }, ENV_PLACE);
    await screen.findByTestId("obs-errors-list");
    await user.type(screen.getByTestId("obs-errors-query"), "pool.go{Enter}");
    expect(harness.current().q).toBe("pool.go");
    await waitFor(() => expect(rowIds()).toEqual(["iss-worker-conns"]));
    expect(listCalls(onCall).at(-1)!.text).toBe("pool.go");
  });

  it("pages through next_page_token", async () => {
    const user = userEvent.setup();
    const onCall = vi.fn();
    const api = createFixtureApi({ onCall });
    const listIssues = api.listIssues;
    // Two to a page, so the fixture's three prod issues need a second.
    api.listIssues = (req) => listIssues({ ...req, pageSize: 2 });
    renderPanel(api, { view: "errors" }, ENV_PLACE);
    await screen.findByTestId("obs-errors-list");
    expect(rowIds()).toHaveLength(2);
    await user.click(screen.getByTestId("obs-errors-more"));
    await waitFor(() => expect(rowIds()).toHaveLength(3));
    expect(listCalls(onCall).at(-1)!.pageToken).toBe("2");
    expect(screen.queryByTestId("obs-errors-more")).not.toBeInTheDocument();
  });

  it("says so, with a way out, when the filters leave nothing", async () => {
    renderPanel(createFixtureApi(), { view: "errors", q: "nothing-matches-this" }, ENV_PLACE);
    expect(await screen.findByTestId("obs-no-data")).toHaveTextContent("No open issues match");
    expect(screen.getByTestId("obs-clear-filters")).toBeInTheDocument();
  });

  it("explains a control plane without issues inline", async () => {
    renderPanel(createFixtureApi({ issues: "not-configured" }), { view: "errors" }, ENV_PLACE);
    expect(await screen.findByTestId("obs-errors-not-configured")).toHaveTextContent("Error issues aren't available on this control plane");
  });
});

describe("one issue", () => {
  it("opens from the list with culprit, frames, versions, first release, related issues, events and history", async () => {
    const user = userEvent.setup();
    const harness = renderPanel(createFixtureApi(), { view: "errors" }, ENV_PLACE);
    await user.click(await screen.findByTestId("obs-issue-open-iss-orders-deadline"));
    expect(harness.current().issue).toBe("iss-orders-deadline");

    const detail = await screen.findByTestId("obs-issue");
    await waitFor(() => expect(detail).toHaveAttribute("data-state", "ready"));
    expect(screen.getByTestId("obs-issue-culprit")).toHaveTextContent("(*Service).CreateOrder in internal/handlers/orders/rpc_create_order.go");
    const frames = screen.getAllByTestId("obs-issue-frame");
    expect(frames[0]).toHaveAttribute("data-in-app", "true");
    expect(frames.at(-1)).toHaveAttribute("data-in-app", "false");
    expect(screen.getByTestId("obs-issue-version-v1.8.3")).toBeInTheDocument();
    expect(screen.getByTestId("obs-issue-release")).toHaveTextContent("v1.8.3");
    expect(screen.getByTestId("obs-issue-related-iss-worker-conns")).toBeInTheDocument();
    await waitFor(() => expect(screen.getAllByTestId("obs-issue-event").length).toBeGreaterThan(0));
    const history = await screen.findByTestId("obs-issue-history-feed");
    expect(within(history).getByTestId("activity-act-opened-1")).toHaveTextContent("Reliant saw a new issue");

    await user.click(screen.getByTestId("obs-issue-related-iss-worker-conns"));
    expect(harness.current().issue).toBe("iss-worker-conns");
    await user.click(await screen.findByTestId("obs-issue-back"));
    expect(harness.current().issue).toBeUndefined();
  });

  it("opens the latest event's trace in the trace view", async () => {
    const user = userEvent.setup();
    const harness = renderPanel(createFixtureApi(), { view: "errors", issue: "iss-orders-deadline", istatus: "new" }, ENV_PLACE);
    const latest = await screen.findByTestId("obs-issue-latest");
    await user.click(within(latest).getByTestId("obs-issue-trace-4bf92f3577b34da6a3ce929d0e0e4736"));
    expect(harness.current()).toMatchObject({ view: "traces", trace: "4bf92f3577b34da6a3ce929d0e0e4736", span: "00f067aa0ba902b7" });
    expect(harness.current().issue).toBeUndefined();
    expect(harness.current().istatus).toBeUndefined();
  });

  it("says an unsymbolicated browser stack shows bundle locations", async () => {
    renderPanel(createFixtureApi(), { view: "errors", issue: "iss-cart-chunk" }, PROJECT_PLACE);
    expect(await screen.findByTestId("obs-issue-frames")).toHaveTextContent("could not be mapped back to source");
  });

  it("says so when the issue is not there", async () => {
    renderPanel(createFixtureApi(), { view: "errors", issue: "iss-nope" }, ENV_PLACE);
    expect(await screen.findByTestId("obs-issue-not-found")).toBeInTheDocument();
  });
});

describe("triage", () => {
  it("resolves in a version, echoing the version read, and the issue reads resolved with its history", async () => {
    const user = userEvent.setup();
    const onCall = vi.fn();
    renderPanel(createFixtureApi({ onCall }), { view: "errors", issue: "iss-worker-conns" }, ENV_PLACE);
    const triage = await screen.findByTestId("obs-issue-triage");
    expect(triage).toHaveAttribute("data-version", "1");
    await user.selectOptions(screen.getByTestId("obs-issue-resolve-in"), "version");
    expect(screen.getByTestId("obs-issue-resolve")).toBeDisabled();
    await user.type(screen.getByTestId("obs-issue-resolve-version"), "v1.8.4");
    await user.click(screen.getByTestId("obs-issue-resolve"));

    const [, req] = onCall.mock.calls.find(([rpc]) => rpc === "UpdateIssueStatus")!;
    expect(req).toEqual({ issueId: "iss-worker-conns", envId: "env-prod", expectedVersion: 1, change: { status: "resolved", resolveIn: { kind: "version", version: "v1.8.4" } } });
    await waitFor(() => expect(screen.getByTestId("obs-issue")).toHaveAttribute("data-status", "resolved"));
    expect(screen.getByTestId("obs-issue-triage")).toHaveAttribute("data-version", "2");
    expect(screen.getByTestId("obs-issue-unresolve")).toBeInTheDocument();
    expect(screen.queryByTestId("obs-issue-resolve")).not.toBeInTheDocument();
    await waitFor(() => expect(screen.getByTestId("obs-issue-history-feed")).toHaveTextContent("Ada Lovelace resolved pq: sorry, too many clients already in v1.8.4"));
  });

  it("ignores for a while, then reopens", async () => {
    const user = userEvent.setup();
    const onCall = vi.fn();
    renderPanel(createFixtureApi({ onCall }), { view: "errors", issue: "iss-checkout-total" }, ENV_PLACE);
    await user.selectOptions(await screen.findByTestId("obs-issue-ignore-for"), "24h");
    await user.click(screen.getByTestId("obs-issue-ignore"));
    await waitFor(() => expect(screen.getByTestId("obs-issue")).toHaveAttribute("data-status", "ignored"));
    const ignore = onCall.mock.calls.filter(([rpc]) => rpc === "UpdateIssueStatus").map(([, r]) => r)[0];
    expect(ignore.change.status).toBe("ignored");
    expect(ignore.change.untilMs - Date.now()).toBeGreaterThan(23 * 3_600_000);

    await user.click(screen.getByTestId("obs-issue-unresolve"));
    await waitFor(() => expect(screen.getByTestId("obs-issue")).toHaveAttribute("data-status", "unresolved"));
    expect(onCall.mock.calls.filter(([rpc]) => rpc === "UpdateIssueStatus").at(-1)![1]).toMatchObject({ change: { status: "unresolved" }, expectedVersion: 2 });
  });

  it("on a stale version, re-reads the issue and says what changed — and never re-sends", async () => {
    const user = userEvent.setup();
    const onCall = vi.fn();
    renderPanel(createFixtureApi({ onCall, issueConflict: true }), { view: "errors", issue: "iss-worker-conns" }, ENV_PLACE);
    await user.click(await screen.findByTestId("obs-issue-ignore"));
    const conflict = await screen.findByTestId("obs-issue-conflict");
    expect(conflict).toHaveTextContent("Someone changed this issue while you were looking, so nothing was changed.");
    await waitFor(() => expect(conflict).toHaveTextContent("It is resolved in v1.8.4 now, not unresolved."));
    // The re-read shows the other person's change; the reader's ignore was not applied or retried.
    expect(screen.getByTestId("obs-issue")).toHaveAttribute("data-status", "resolved");
    expect(screen.getByTestId("obs-issue-triage")).toHaveAttribute("data-version", "2");
    expect(onCall.mock.calls.filter(([rpc]) => rpc === "UpdateIssueStatus")).toHaveLength(1);
    expect(screen.getByTestId("obs-issue-unresolve")).toBeInTheDocument();
  });
});
