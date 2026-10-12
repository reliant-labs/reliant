// Copyright (c) 2025 Reliant Labs

/**
 * Traces: the list (server RED summary, sortable columns, filters as URL
 * state) and one trace's waterfall — a keyboard-operable tree — with the
 * selected span's details and the ways to its logs and its CPU profile.
 */

import { describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { createFixtureApi, FIXTURE_TRACE_ID } from "../__fixtures__/fixtureApi";
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
    const onCall = vi.fn();
    const harness = renderPanel(createFixtureApi({ onCall }), { view: "traces" });
    await screen.findAllByTestId("obs-trace-row");
    await user.type(screen.getByTestId("obs-trace-query"), "GetOrder{Enter}");
    await user.click(screen.getByTestId("obs-errors-only"));
    await user.selectOptions(screen.getByTestId("obs-min-duration"), "500ms");
    expect(harness.current()).toMatchObject({ q: "GetOrder", errors: true, minDuration: "500ms" });
    await waitFor(() =>
      expect(onCall).toHaveBeenLastCalledWith(
        "ListTraces",
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
