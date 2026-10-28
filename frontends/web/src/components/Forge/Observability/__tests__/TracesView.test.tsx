// Copyright (c) 2025 Reliant Labs

/**
 * Traces: the list (server RED summary, sortable columns, filters as URL
 * state) and one trace's waterfall — a keyboard-operable tree — with the
 * selected span's details and the ways to its logs and its CPU profile.
 */

import { describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { createFixtureApi, fixtureTrace, FIXTURE_TRACE_ID } from "../__fixtures__/fixtureApi";
import { renderPanel } from "./obsTestUtils";

vi.mock("uplot", async () => (await import("./obsMocks")).obsMocks.uplot());
vi.mock("@tanstack/react-virtual", async () => (await import("./obsMocks")).obsMocks.virtual());

describe("trace list", () => {
  it("shows the server's RED summary above the traces", async () => {
    renderPanel(createFixtureApi(), { view: "traces" });
    const red = await screen.findByTestId("obs-trace-red");
    expect(within(red).getByText("Traces").nextElementSibling).toHaveTextContent("40");
    expect(screen.getAllByTestId("obs-trace-row").length).toBeGreaterThan(0);
  });

  it("sends root operation, errors-only and minimum duration as ListTraces filters", async () => {
    const user = userEvent.setup();
    const api = createFixtureApi();
    // GetServiceSignals retries independently of the trace filters. Assert the
    // latest ListTraces request, not the latest RPC across the whole panel.
    const listTraces = vi.spyOn(api, "listTraces");
    const harness = renderPanel(api, { view: "traces" });
    await screen.findAllByTestId("obs-trace-row");
    await user.type(screen.getByTestId("obs-trace-query"), "GetOrder{Enter}");
    await user.click(screen.getByTestId("obs-errors-only"));
    await user.selectOptions(screen.getByTestId("obs-min-duration"), "500ms");
    expect(harness.current()).toMatchObject({ q: "GetOrder", errors: true, minDuration: "500ms" });
    await waitFor(() =>
      expect(listTraces).toHaveBeenLastCalledWith(
        expect.objectContaining({ rootSpanName: "GetOrder", errorsOnly: true, minDurationMs: 500 })
      )
    );
  });

  it("shows the root span's duration, and the whole trace's when work outlived it", async () => {
    renderPanel(createFixtureApi(), { view: "traces" });
    const [first] = await screen.findAllByTestId("obs-trace-row");
    // Trace 0 in the fixture has 120ms of queue work after its root returned.
    expect(first).toHaveTextContent(/ms total/);
  });

  it("sorts by duration from a column header, and says so to assistive tech", async () => {
    const user = userEvent.setup();
    renderPanel(createFixtureApi(), { view: "traces" });
    await screen.findAllByTestId("obs-trace-row");
    const header = screen.getByRole("columnheader", { name: /Duration/ });
    expect(header).toHaveAttribute("aria-sort", "none");
    await user.click(within(header).getByRole("button"));
    expect(header).toHaveAttribute("aria-sort", expect.stringMatching(/ascending|descending/));
  });

  it("opens a trace from its root operation", async () => {
    const user = userEvent.setup();
    const harness = renderPanel(createFixtureApi(), { view: "traces" });
    const [first] = await screen.findAllByTestId("obs-trace-row");
    await user.click(within(first).getByRole("button"));
    expect(harness.current().trace).toBe(FIXTURE_TRACE_ID);
    expect(await screen.findByTestId("trace-detail")).toBeInTheDocument();
  });
});

describe("trace waterfall", () => {
  it("sends the selected range to GetTrace and refetches when the range changes", async () => {
    const now = Date.parse("2026-10-27T21:30:00Z");
    const clock = vi.spyOn(Date, "now").mockReturnValue(now);
    try {
      const user = userEvent.setup();
      const api = createFixtureApi();
      const getTrace = vi.spyOn(api, "getTrace");
      renderPanel(api, { view: "traces", trace: FIXTURE_TRACE_ID, range: "1h" });
      await screen.findByTestId("trace-detail");
      expect(getTrace).toHaveBeenLastCalledWith(expect.objectContaining({
        envIds: ["env-prod"],
        window: { startMs: now - 60 * 60_000, endMs: now },
      }));
      await user.selectOptions(screen.getByTestId("obs-range"), "15m");
      await waitFor(() => expect(getTrace).toHaveBeenLastCalledWith(expect.objectContaining({
        window: { startMs: now - 15 * 60_000, endMs: now },
      })));
      expect(getTrace).toHaveBeenCalledTimes(2);
    } finally { clock.mockRestore(); }
  });

  it("finds late incoming links on stale revisit and on explicit Refresh without polling", async () => {
    let now = Date.parse("2026-10-27T21:30:00Z");
    const clock = vi.spyOn(Date, "now").mockImplementation(() => now);
    try {
      const user = userEvent.setup();
      const api = createFixtureApi();
      let arrived = false;
      const getTrace = vi.spyOn(api, "getTrace").mockImplementation(async (req) => ({
        ...fixtureTrace(req.traceId, now),
        relatedTraces: arrived ? [{ traceId: "c".repeat(32), spanId: "d".repeat(16), linkedSpanId: "b0" }] : [],
      }));
      renderPanel(api, { view: "traces", trace: FIXTURE_TRACE_ID, range: "1h" });
      expect(await screen.findByRole("region", { name: "Related traces" })).toHaveTextContent("No incoming links found");
      await user.click(screen.getByTestId("trace-back"));
      await screen.findAllByTestId("obs-trace-row");
      arrived = true;
      now += 16_000;
      await user.click(within(screen.getAllByTestId("obs-trace-row")[0]).getByRole("button"));
      await waitFor(() => expect(within(screen.getByRole("region", { name: "Related traces" })).getByRole("button")).toBeInTheDocument());
      expect(getTrace).toHaveBeenCalledTimes(2);
      arrived = false;
      await user.click(screen.getByRole("button", { name: "Refresh" }));
      await waitFor(() => expect(screen.getByRole("region", { name: "Related traces" })).toHaveTextContent("No incoming links found"));
      expect(getTrace).toHaveBeenCalledTimes(3);
    } finally { clock.mockRestore(); }
  });

  it("navigates outgoing span links without inventing a parent or losing scope/time", async () => {
    const user = userEvent.setup();
    const api = createFixtureApi();
    const linkedTrace = "a".repeat(32);
    api.getTrace = vi.fn(async (req) => {
      const result = fixtureTrace(req.traceId, Date.now());
      result.spans[0].links = [{ traceId: linkedTrace, spanId: "b".repeat(16) }];
      return result;
    });
    const harness = renderPanel(api, { view: "traces", trace: FIXTURE_TRACE_ID, envs: ["prod"], apps: ["control-plane"], range: "1h" });
    const tree = await screen.findByRole("tree");
    await user.click(within(tree).getAllByRole("treeitem")[0]);
    const links = screen.getByRole("region", { name: "Span links" });
    expect(links).toHaveTextContent("not parent/child edges");
    await user.click(within(links).getByRole("button"));
    expect(harness.current()).toMatchObject({ trace: linkedTrace, span: "b".repeat(16), envs: ["prod"], apps: ["control-plane"], range: "1h" });
  });

  it("offers incoming related traces separately from the waterfall with bounded-result guidance", async () => {
    const user = userEvent.setup();
    const api = createFixtureApi();
    const linkedTrace = "c".repeat(32);
    api.getTrace = vi.fn(async (req) => ({
      ...fixtureTrace(req.traceId, Date.now()),
      relatedTraces: [{ traceId: linkedTrace, spanId: "d".repeat(16), linkedSpanId: "b0" }],
      relatedTracesTruncated: true,
    }));
    const harness = renderPanel(api, { view: "traces", trace: FIXTURE_TRACE_ID, envs: ["prod"], range: "1h" });
    const related = await screen.findByRole("region", { name: "Related traces" });
    expect(related).toHaveTextContent("not parent/child spans");
    expect(related).toHaveTextContent("first 100");
    expect(within(await screen.findByRole("tree")).getAllByRole("treeitem")).toHaveLength(9);
    await user.click(within(related).getByRole("button"));
    expect(harness.current()).toMatchObject({ trace: linkedTrace, span: "d".repeat(16), envs: ["prod"], range: "1h" });
  });

  it("draws every span as a tree item, with the failed ones said aloud", async () => {
    renderPanel(createFixtureApi(), { view: "traces", trace: FIXTURE_TRACE_ID });
    const tree = await screen.findByRole("tree", { name: "Spans of this trace" });
    const items = within(tree).getAllByRole("treeitem");
    expect(items).toHaveLength(9);
    expect(items[0]).toHaveAttribute("aria-level", "1");
    expect(items[1]).toHaveAttribute("aria-level", "2");
    expect(items[3]).toHaveAccessibleName(/payments\.v1\.PaymentsService\/Charge.*failed/);
    expect(screen.getByTestId("trace-waterfall-summary")).toHaveTextContent("9 spans · 2 errors · Total 182.4ms");
  });

  it("selects a span into the URL and shows its details and links", async () => {
    const user = userEvent.setup();
    const harness = renderPanel(createFixtureApi(), { view: "traces", trace: FIXTURE_TRACE_ID });
    const tree = await screen.findByRole("tree");
    await user.click(within(tree).getAllByRole("treeitem")[3]);
    expect(harness.current().span).toBe("b3");
    const details = await screen.findByTestId("span-details");
    expect(details).toHaveTextContent("unavailable: payment provider timeout");
    await user.click(within(details).getByTestId("span-logs"));
    expect(harness.current()).toMatchObject({ view: "logs", trace: FIXTURE_TRACE_ID, span: "b3" });
  });

  it("follows the tree pattern: Left collapses, Right expands, Down moves", async () => {
    const user = userEvent.setup();
    renderPanel(createFixtureApi(), { view: "traces", trace: FIXTURE_TRACE_ID, span: "b3" });
    const tree = await screen.findByRole("tree");
    const charge = within(tree).getAllByRole("treeitem")[3];
    // A deep link to a span puts the tab stop on it.
    await waitFor(() => expect(charge).toHaveAttribute("tabindex", "0"));
    charge.focus();
    await user.keyboard("{ArrowLeft}");
    expect(within(tree).getAllByRole("treeitem")).toHaveLength(7);
    expect(within(tree).getAllByRole("treeitem")[3]).toHaveAttribute("aria-expanded", "false");
    await user.keyboard("{ArrowRight}");
    expect(within(tree).getAllByRole("treeitem")).toHaveLength(9);
    await user.keyboard("{ArrowDown}");
    await waitFor(() => expect(within(tree).getAllByRole("treeitem")[4]).toHaveFocus());
  });

  it("links the trace to its service's flamegraph", async () => {
    const user = userEvent.setup();
    const harness = renderPanel(createFixtureApi(), { view: "traces", trace: FIXTURE_TRACE_ID });
    await user.click(await screen.findByTestId("trace-flamegraph"));
    expect(harness.current()).toMatchObject({ view: "profiles", trace: FIXTURE_TRACE_ID, service: "api" });
  });
});
