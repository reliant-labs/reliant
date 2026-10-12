// Copyright (c) 2025 Reliant Labs

/**
 * Logs: the query box is the server's (sent verbatim, its parse error shown
 * under it), filters are URL state, rows are keyboard-navigable disclosures,
 * and a log's trace id leads to the trace.
 */

import { describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Code, ConnectError } from "@connectrpc/connect";

import { createFixtureApi } from "../__fixtures__/fixtureApi";
import { renderPanel } from "./obsTestUtils";

vi.mock("uplot", async () => (await import("./obsMocks")).obsMocks.uplot());
vi.mock("@tanstack/react-virtual", async () => (await import("./obsMocks")).obsMocks.virtual());

describe("LogsView", () => {
  it("sends the typed query verbatim, with the service and levels as structured filters", async () => {
    const user = userEvent.setup();
    const onCall = vi.fn();
    const harness = renderPanel(createFixtureApi({ onCall }), { view: "logs", service: "api", severity: "warn,error" });
    await screen.findAllByTestId("obs-log-row");
    await user.type(screen.getByTestId("obs-log-query"), "payment{Enter}");
    expect(harness.current().q).toBe("payment");
    await waitFor(() =>
      expect(onCall).toHaveBeenCalledWith(
        "SearchLogs",
        expect.objectContaining({ query: "payment", services: ["api"], severities: ["warn", "error"] })
      )
    );
  });

  it("toggles a level filter in the URL", async () => {
    const user = userEvent.setup();
    const harness = renderPanel(createFixtureApi(), { view: "logs" });
    await screen.findAllByTestId("obs-log-row");
    await user.click(screen.getByTestId("obs-severity-error"));
    expect(screen.getByTestId("obs-severity-error")).toHaveAttribute("aria-pressed", "true");
    expect(harness.current().severity).toBe("error");
  });

  it("shows the server's parse error under the box that caused it", async () => {
    const api = createFixtureApi();
    api.searchLogs = async () => {
      throw new ConnectError("unexpected ')' at position 14", Code.InvalidArgument);
    };
    renderPanel(api, { view: "logs", q: "(timeout OR))" });
    const error = await screen.findByTestId("obs-log-query-error");
    expect(error).toHaveTextContent("unexpected ')' at position 14");
    expect(screen.getByTestId("obs-log-query")).toHaveAttribute("aria-invalid", "true");
  });

  it("expands a row to its attributes, and links its trace", async () => {
    const user = userEvent.setup();
    const harness = renderPanel(createFixtureApi(), { view: "logs" });
    const [first] = await screen.findAllByTestId("obs-log-row");
    await user.click(first);
    expect(first).toHaveAttribute("aria-expanded", "true");
    const details = screen.getByTestId("obs-log-details");
    expect(within(details).getByRole("region", { name: "Attributes" })).toHaveTextContent("rpc.method");
    await user.click(within(details).getByTestId("obs-log-open-trace"));
    expect(harness.current()).toMatchObject({ view: "traces", trace: expect.stringMatching(/^[0-9a-f]{32}$/) });
  });

  it("is one tab stop, with arrow keys, Home and End moving between rows", async () => {
    const user = userEvent.setup();
    renderPanel(createFixtureApi(), { view: "logs" });
    const rows = await screen.findAllByTestId("obs-log-row");
    expect(rows.filter((row) => row.tabIndex === 0)).toHaveLength(1);
    rows[0].focus();
    await user.keyboard("{ArrowDown}");
    await waitFor(() => expect(screen.getAllByTestId("obs-log-row")[1]).toHaveFocus());
    await user.keyboard("{End}");
    await waitFor(() => expect(screen.getAllByTestId("obs-log-row").at(-1)).toHaveFocus());
  });

  it("tails live, and stops offering older pages while it does", async () => {
    const user = userEvent.setup();
    const harness = renderPanel(createFixtureApi(), { view: "logs" });
    await screen.findAllByTestId("obs-log-row");
    expect(screen.getByTestId("obs-logs-more")).toBeInTheDocument();
    await user.click(screen.getByTestId("obs-live-tail"));
    expect(harness.current().live).toBe(true);
    expect(screen.getByTestId("obs-live-tail")).toHaveAttribute("aria-pressed", "true");
    expect(screen.queryByTestId("obs-logs-more")).not.toBeInTheDocument();
  });

  it("scopes to one trace from a trace link, and says so", async () => {
    const onCall = vi.fn();
    renderPanel(createFixtureApi({ onCall }), { view: "logs", trace: "4bf92f3577b34da6a3ce929d0e0e4736" });
    expect(await screen.findByTestId("obs-logs-trace-scope")).toHaveTextContent("4bf92f35");
    expect(onCall).toHaveBeenCalledWith("SearchLogs", expect.objectContaining({ traceId: "4bf92f3577b34da6a3ce929d0e0e4736" }));
  });

  it("offers to clear filters when they are what emptied the result", async () => {
    const user = userEvent.setup();
    const harness = renderPanel(createFixtureApi(), { view: "logs", q: "no-such-thing" });
    expect(await screen.findByTestId("obs-no-data")).toHaveTextContent("No logs match");
    await user.click(screen.getByTestId("obs-clear-filters"));
    expect(harness.current().q).toBeUndefined();
  });
});
