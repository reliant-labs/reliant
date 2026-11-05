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
        expect.objectContaining({ rootSpanName: "GetOrder", errorsOnly: true, minDurationMs: 500 }),
        expect.objectContaining({ signal: expect.anything() })
      )
    );
  });

  it("shows who each trace served and where, and narrows to a user from the list or the box", async () => {
    const user = userEvent.setup();
    const api = createFixtureApi();
    const listTraces = vi.spyOn(api, "listTraces");
    const harness = renderPanel(api, { view: "traces" });
    const [first] = await screen.findAllByTestId("obs-trace-row");
    expect(first).toHaveTextContent("u_8812");
    expect(first).toHaveTextContent(/Berlin/);
    await user.click(within(first).getByTestId("obs-trace-user"));
    expect(harness.current()).toMatchObject({ user: "u_8812" });
    await waitFor(() =>
      expect(listTraces).toHaveBeenLastCalledWith(expect.objectContaining({ userId: "u_8812" }), expect.objectContaining({ signal: expect.anything() }))
    );
    await user.click(screen.getByTestId("obs-traces-user-clear"));
    expect(harness.current().user).toBeUndefined();
    await user.type(screen.getByTestId("obs-traces-user"), "u_42{Enter}");
    expect(harness.current()).toMatchObject({ user: "u_42" });
  });

  it("shows which client called and which builds served, and narrows by them", async () => {
    const user = userEvent.setup();
    const api = createFixtureApi();
    const listTraces = vi.spyOn(api, "listTraces");
    const harness = renderPanel(api, { view: "traces" });
    const [first, second] = await screen.findAllByTestId("obs-trace-row");
    // Fixture trace 0: a browser on Chrome / macOS made the call from build v1.8.3; api v1.7.91 and worker v1.7.92 served it.
    // Each service shows its own version.
    expect(within(first).getByTestId("obs-trace-client")).toHaveTextContent("Chrome 156 · macOS 15.1");
    const versions = within(first).getByTestId("obs-trace-versions");
    expect(versions).toHaveTextContent("shop-web v1.8.3");
    expect(versions).toHaveTextContent("api v1.7.91");
    expect(versions).toHaveTextContent("+1 more");
    // Trace 1: no browser started it, so no client and no frontend build.
    expect(within(second).queryByTestId("obs-trace-client")).not.toBeInTheDocument();

    await user.type(screen.getByTestId("obs-traces-client-fe"), "1.8.16");
    await user.type(screen.getByTestId("obs-traces-client-be"), "v1.7.92");
    await user.click(screen.getByTestId("obs-traces-client-apply"));
    expect(harness.current()).toMatchObject({ fe: "1.8.16", be: "v1.7.92" });
    await waitFor(() =>
      expect(listTraces).toHaveBeenLastCalledWith(
        expect.objectContaining({ frontendVersion: "1.8.16", backendVersion: "v1.7.92" }),
        expect.objectContaining({ signal: expect.anything() })
      )
    );
    await waitFor(() => expect(screen.getAllByTestId("obs-trace-versions").every((c) => c.textContent?.includes("1.8.16"))).toBe(true));
  });

  it("refuses rows a control plane answered without the client filters", async () => {
    const api = createFixtureApi();
    const real = api.listTraces.bind(api);
    // An older control plane ignores the unknown fields and answers unfiltered.
    vi.spyOn(api, "listTraces").mockImplementation((req, options) => real({ ...req, browser: undefined }, options));
    renderPanel(api, { view: "traces", browser: "Firefox" });
    expect(await screen.findByText(/can't filter traces by browser yet/)).toBeInTheDocument();
  });

  it("lists one browser session's traces, says so, and links back to it", async () => {
    const user = userEvent.setup();
    const api = createFixtureApi();
    const listTraces = vi.spyOn(api, "listTraces");
    const harness = renderPanel(api, { view: "traces", rsession: "sess-001003" });
    const bar = await screen.findByTestId("obs-traces-session");
    await waitFor(() =>
      expect(listTraces).toHaveBeenLastCalledWith(expect.objectContaining({ sessionId: "sess-001003" }), expect.objectContaining({ signal: expect.anything() }))
    );
    await user.click(within(bar).getByTestId("obs-traces-session-open"));
    expect(harness.current()).toMatchObject({ view: "sessions", session: "sess-001003" });
  });

  it.each([
    ["user", { user: "u_8812" }, "can't filter traces by user"],
    ["browser session", { rsession: "sess-001003" }, "can't filter traces by browser session"],
  ])("never shows an older control plane's unfiltered traces as one %s's", async (_, search, copy) => {
    const api = createFixtureApi();
    const listTraces = api.listTraces;
    // A control plane from before user_id/session_id: it ignores both fields, and fills neither.
    api.listTraces = async (req, options) => {
      const result = await listTraces({ ...req, userId: undefined, sessionId: undefined }, options);
      return { ...result, traces: result.traces.map((t) => ({ ...t, userId: "", sessionId: "" })) };
    };
    renderPanel(api, { view: "traces", ...search });
    expect(await screen.findByTestId("obs-error-filter-unsupported")).toHaveTextContent(copy);
    expect(screen.queryAllByTestId("obs-trace-row")).toHaveLength(0);
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
    await user.click(within(first).getByRole("button", { name: /^Open trace/ }));
    expect(harness.current().trace).toBe(FIXTURE_TRACE_ID);
    expect(await screen.findByTestId("trace-detail")).toBeInTheDocument();
  });
});

describe("trace title", () => {
  // A fixed instant: the fixtures never depend on the wall clock.
  const NOW = Date.parse("2026-10-27T21:30:00Z");
  const browserFetch = (traceId: string, now: number) => {
    const base = fixtureTrace(traceId, now);
    const root = base.spans[0]!;
    return {
      ...base,
      spans: [
        {
          ...root,
          spanId: "w1",
          parentSpanId: "",
          name: "HTTP POST",
          service: "reliant-web",
          kind: "client",
          attributes: { "http.method": "POST", "http.url": "https://api.reliantapi.com/reliant.v1.SettingsService/GetConfigHealth" },
          resource: { "service.name": "reliant-web", "reliant.ingest.class": "browser", "process.runtime.name": "browser" },
        },
      ],
    };
  };

  it("names a server-rooted trace Service.Method, keeping the raw name as its title", async () => {
    renderPanel(createFixtureApi(), { view: "traces", trace: FIXTURE_TRACE_ID });
    const title = await screen.findByTestId("trace-title");
    expect(title).toHaveTextContent("api OrdersService.CreateOrder");
    expect(title).toHaveAttribute("title", "orders.v1.OrdersService/CreateOrder");
    expect(screen.queryByTestId("trace-browser-only")).not.toBeInTheDocument();
  });

  it("names a browser fetch by the RPC it called, and says its backend spans were sampled out", async () => {
    const api = createFixtureApi();
    vi.spyOn(api, "getTrace").mockImplementation(async (req) => browserFetch(req.traceId, NOW));
    renderPanel(api, { view: "traces", trace: FIXTURE_TRACE_ID });
    expect(await screen.findByTestId("trace-title")).toHaveTextContent("reliant-web SettingsService.GetConfigHealth");
    expect(screen.getByTestId("trace-browser-only")).toHaveTextContent(/Logs for this trace/);
  });

  it("names a browser-started trace by the server span it caused, from the browser", async () => {
    const api = createFixtureApi();
    vi.spyOn(api, "getTrace").mockImplementation(async (req) => {
      const browser = browserFetch(req.traceId, NOW);
      const server = { ...fixtureTrace(req.traceId, NOW).spans[0]!, spanId: "s1", parentSpanId: "w1", name: "reliant.v1.SettingsService/GetConfigHealth", service: "reliant-api-server", kind: "server" };
      return { ...browser, spans: [...browser.spans, server] };
    });
    renderPanel(api, { view: "traces", trace: FIXTURE_TRACE_ID });
    expect(await screen.findByTestId("trace-title")).toHaveTextContent("reliant-api-server SettingsService.GetConfigHealth from reliant-web");
    expect(screen.queryByTestId("trace-browser-only")).not.toBeInTheDocument();
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
      }), expect.objectContaining({ signal: expect.anything() }));
      await user.selectOptions(screen.getByTestId("obs-range"), "15m");
      await waitFor(() => expect(getTrace).toHaveBeenLastCalledWith(expect.objectContaining({
        window: { startMs: now - 15 * 60_000, endMs: now },
      }), expect.objectContaining({ signal: expect.anything() })));
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
      await user.click(within(screen.getAllByTestId("obs-trace-row")[0]).getByRole("button", { name: /^Open trace/ }));
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

  it("shows each service's own build on the span where the trace enters it", async () => {
    renderPanel(createFixtureApi(), { view: "traces", trace: FIXTURE_TRACE_ID });
    const tree = await screen.findByRole("tree", { name: "Spans of this trace" });
    const items = within(tree).getAllByRole("treeitem");
    const entries = items.filter((item) => within(item).queryByTestId("waterfall-span-version"));
    // The root, and each span that crosses into another service; never a span inside one.
    expect(entries[0]).toBe(items[0]);
    expect(entries.length).toBeLessThan(items.length);
    for (const e of entries) expect(within(e).getByTestId("waterfall-span-version")).toHaveTextContent("v1.7.15");
    expect(screen.getByTestId("trace-builds")).toHaveTextContent("v1.7.15");
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
