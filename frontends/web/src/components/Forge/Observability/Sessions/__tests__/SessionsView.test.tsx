// Copyright (c) 2025 Reliant Labs

/**
 * The Sessions view against the sessions fixture (__fixtures__/fixtureSessions.ts):
 * the list with its filters and pages in the URL; a session's header,
 * masked replay (through the real lazy loader, with rrweb's Replayer stood in
 * by fakeReplayer.ts) and timeline, whose errors open their issue and whose
 * requests open their trace; and the links INTO a session from an error's
 * event, from a trace, and from a dashboard's sessions panel.
 */

import { describe, expect, it, vi, afterEach, beforeEach } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import type { PanelResult } from "@/services/controlPlane/observability/types";

import { createFixtureApi, fixtureTrace } from "../../__fixtures__/fixtureApi";
import { FIXTURE_LIVE_SESSION, FIXTURE_RELEASE, FIXTURE_STORY_SESSION } from "../../__fixtures__/fixtureSessions";
import { ENV_PLACE, PROJECT_PLACE, renderPanel } from "../../__tests__/obsTestUtils";
import { PanelBody } from "../../Dashboards/PanelBody";
import { FakeReplayer } from "./fakeReplayer";
import { noteReplayUnplayable, resetReplayVerdictsForTests } from "../sessionModel";

vi.mock("uplot", async () => (await import("../../__tests__/obsMocks")).obsMocks.uplot());
vi.mock("@tanstack/react-virtual", async () => (await import("../../__tests__/obsMocks")).obsMocks.virtual());
vi.mock("@rrweb/replay", async () => ({ Replayer: (await import("./fakeReplayer")).FakeReplayer }));
vi.mock("@rrweb/replay/dist/style.css", () => ({}));

type ListReq = { envIds: string[]; hasError: boolean; hasReplay: boolean; userId: string; pathContains: string; browser?: string; os?: string; pageToken?: string };
const listCalls = (onCall: ReturnType<typeof vi.fn>) => onCall.mock.calls.filter(([rpc]) => rpc === "ListSessions").map(([, req]) => req as ListReq);
const rowIds = () => screen.getAllByTestId(/^obs-session-sess-/).map((row) => row.getAttribute("data-testid")!.replace("obs-session-", ""));

beforeEach(() => {
  FakeReplayer.reset();
  resetReplayVerdictsForTests();
});
afterEach(() => vi.useRealTimers());

describe("the session list", () => {
  it("lists the scope's sessions newest first, the live one on top", async () => {
    const onCall = vi.fn();
    renderPanel(createFixtureApi({ onCall }), { view: "sessions", range: "24h" }, PROJECT_PLACE);
    await screen.findByTestId("obs-sessions-list");
    const ids = rowIds();
    expect(ids.slice(0, 2)).toEqual([FIXTURE_LIVE_SESSION, FIXTURE_STORY_SESSION]);
    expect(ids).toHaveLength(25);
    expect(listCalls(onCall)[0]).toMatchObject({ hasError: false, hasReplay: true, userId: "", pathContains: "" });
    expect(within(screen.getByTestId(`obs-session-${FIXTURE_STORY_SESSION}`)).getByText("replay")).toBeInTheDocument();
  });

  it("lists only sessions with a replay, and dims one the player found unplayable", async () => {
    const api = createFixtureApi();
    const all = (await api.listSessions({ orgId: "o", envIds: [], window: { startMs: 0, endMs: Date.now() + 1 }, hasError: false, hasReplay: false, userId: "", pathContains: "", pageSize: 25 })).sessions;
    const unrecorded = all.find((s) => !s.hasReplay)!;
    const opened = all.find((s) => s.hasReplay && s.sessionId !== FIXTURE_STORY_SESSION && s.sessionId !== FIXTURE_LIVE_SESSION)!;
    // The player found this one's replay unplayable earlier in the tab.
    noteReplayUnplayable(opened.sessionId);
    renderPanel(api, { view: "sessions", range: "24h" }, PROJECT_PLACE);
    await screen.findByTestId("obs-sessions-list");

    const story = screen.getByTestId(`obs-session-${FIXTURE_STORY_SESSION}`);
    expect(story).toHaveAttribute("data-replay", "playable");
    expect(story.className).not.toMatch(/opacity-60/);

    // A session with nothing to replay is not a session: it is never listed.
    expect(screen.queryByTestId(`obs-session-${unrecorded.sessionId}`)).not.toBeInTheDocument();
    expect(rowIds().every((id) => all.find((s) => s.sessionId === id)?.hasReplay !== false)).toBe(true);

    const incomplete = screen.getByTestId(`obs-session-${opened.sessionId}`);
    expect(incomplete).toHaveAttribute("data-replay", "unplayable");
    expect(within(incomplete).queryByText("replay")).not.toBeInTheDocument();
    expect(screen.getByTestId(`obs-session-replay-incomplete-${opened.sessionId}`)).toHaveTextContent("replay incomplete");
  });

  it("names each session by its frontend and start time, with the id as copyable metadata", async () => {
    renderPanel(createFixtureApi(), { view: "sessions", range: "24h" }, PROJECT_PLACE);
    await screen.findByTestId("obs-sessions-list");
    const open = screen.getByTestId(`obs-session-open-${FIXTURE_STORY_SESSION}`);
    expect(open.textContent).toMatch(/^shop-web · (Today|Yesterday) \d{2}:\d{2}$/);
    expect(open).not.toHaveTextContent(FIXTURE_STORY_SESSION);
    const row = screen.getByTestId(`obs-session-${FIXTURE_STORY_SESSION}`);
    expect(within(row).getByText("u_8812")).toBeInTheDocument();
    expect(within(row).getByTitle(FIXTURE_STORY_SESSION)).toHaveTextContent(`${FIXTURE_STORY_SESSION.slice(0, 8)}…`);
    expect(within(row).getByRole("button", { name: `Copy ${FIXTURE_STORY_SESSION}` })).toBeInTheDocument();
  });

  it("pages Older and Newer through the URL's token trail", async () => {
    const onCall = vi.fn();
    const harness = renderPanel(createFixtureApi({ onCall }), { view: "sessions", range: "24h" }, PROJECT_PLACE);
    await screen.findByTestId("obs-sessions-list");
    expect(screen.getByTestId("obs-sessions-prev")).toBeDisabled();
    const user = userEvent.setup();

    await user.click(screen.getByTestId("obs-sessions-next"));
    expect(harness.current().cursor).toEqual(["s:25"]);
    await waitFor(() => expect(screen.getByTestId("obs-sessions-page")).toHaveTextContent("Page 2"));
    await waitFor(() => expect(rowIds()).toHaveLength(3));
    expect(listCalls(onCall).at(-1)?.pageToken).toBe("s:25");
    expect(screen.getByTestId("obs-sessions-next")).toBeDisabled();

    await user.click(screen.getByTestId("obs-sessions-prev"));
    expect(harness.current().cursor).toBeUndefined();
    await waitFor(() => expect(rowIds()).toHaveLength(25));
  });

  it("narrows by errors, entry path and user — each in the URL, each back to page one", async () => {
    const onCall = vi.fn();
    const harness = renderPanel(createFixtureApi({ onCall }), { view: "sessions", range: "24h", cursor: ["s:25"] }, PROJECT_PLACE);
    await screen.findByTestId("obs-sessions-list");
    const user = userEvent.setup();

    await user.click(screen.getByTestId("obs-sessions-with-errors"));
    expect(harness.current()).toMatchObject({ errors: true });
    expect(harness.current().cursor).toBeUndefined();
    await waitFor(() => expect(listCalls(onCall).at(-1)).toMatchObject({ hasError: true, pageToken: undefined }));

    expect(screen.queryByTestId("obs-sessions-with-replay")).not.toBeInTheDocument();

    await user.type(screen.getByTestId("obs-sessions-path"), "/checkout");
    await user.type(screen.getByTestId("obs-sessions-user"), "u_8812");
    await user.click(screen.getByTestId("obs-sessions-search"));
    expect(harness.current()).toMatchObject({ q: "/checkout", user: "u_8812" });
    await waitFor(() => expect(listCalls(onCall).at(-1)).toMatchObject({ hasError: true, hasReplay: true, pathContains: "/checkout", userId: "u_8812" }));
    await waitFor(() => expect(rowIds()[0]).toBe(FIXTURE_STORY_SESSION));
  });

  it("shows each session's device, client and app version", async () => {
    renderPanel(createFixtureApi(), { view: "sessions", range: "24h" }, PROJECT_PLACE);
    await screen.findByTestId("obs-sessions-list");
    // The fixture's live and story sessions are base(0): Chrome on macOS.
    const device = screen.getByTestId(`obs-session-device-${FIXTURE_STORY_SESSION}`);
    expect(device).toHaveTextContent("macOS 15.1");
    expect(device).toHaveTextContent("desktop · 1440×900");
    expect(device).toHaveAttribute("title", expect.stringContaining("derived by the platform from the request's User-Agent"));
    expect(screen.getByTestId(`obs-session-client-${FIXTURE_STORY_SESSION}`)).toHaveTextContent("Chrome 156");
    // The release the build shipped in first, the build id (what source maps are keyed by) beneath.
    const version = screen.getByTestId(`obs-session-version-${FIXTURE_STORY_SESSION}`);
    expect(within(version).getByTestId("obs-session-release")).toHaveTextContent(FIXTURE_RELEASE);
    expect(version).toHaveTextContent("build v1.8.3");
    // A build no release carries is shown by its build id alone.
    const unreleased = screen.getAllByTestId(/^obs-session-version-/).find((cell) => cell.textContent === "v1.8.2");
    expect(unreleased).toBeDefined();
    // A desktop session: the app's own version under the build, and its Electron.
    const desktop = screen.getAllByTestId(/^obs-session-client-/).find((cell) => cell.textContent?.startsWith("Desktop app"));
    expect(desktop).toHaveTextContent("Desktop app 1.8.16Electron 41.10.7");
  });

  it("narrows by browser, OS and frontend build, in the URL and back to page one", async () => {
    const onCall = vi.fn();
    const harness = renderPanel(createFixtureApi({ onCall }), { view: "sessions", range: "24h", cursor: ["s:25"] }, PROJECT_PLACE);
    await screen.findByTestId("obs-sessions-list");
    const user = userEvent.setup();
    await user.type(screen.getByTestId("obs-sessions-client-browser"), "Firefox");
    await user.type(screen.getByTestId("obs-sessions-client-os"), "Windows");
    await user.click(screen.getByTestId("obs-sessions-client-apply"));
    expect(harness.current()).toMatchObject({ browser: "Firefox", os: "Windows" });
    expect(harness.current().cursor).toBeUndefined();
    await waitFor(() => expect(listCalls(onCall).at(-1)).toMatchObject({ browser: "Firefox", os: "Windows", pageToken: undefined }));
    await waitFor(() => expect(screen.getAllByTestId(/^obs-session-client-/).every((c) => c.textContent === "Firefox 133")).toBe(true));
    await user.click(screen.getByTestId("obs-sessions-client-clear"));
    expect(harness.current().browser).toBeUndefined();
    expect(harness.current().os).toBeUndefined();
  });

  it("explains an empty window, and an older control plane", async () => {
    renderPanel(createFixtureApi({ sessions: "empty" }), { view: "sessions" }, ENV_PLACE);
    expect(await screen.findByTestId("obs-no-data")).toHaveTextContent("No browser sessions");
    // Other telemetry flowing says nothing about sessions: the empty state says how they and replays arrive.
    const source = screen.getByTestId("obs-no-data-source");
    expect(source).toHaveTextContent("A forge frontend sends sessions on its own");
    expect(source).toHaveTextContent("VITE_OTEL_REPLAY=true");
    expect(source).toHaveTextContent("no unmasked mode");
  });

  it("explains a control plane without sessions", async () => {
    renderPanel(createFixtureApi({ sessions: "not-configured" }), { view: "sessions" }, ENV_PLACE);
    expect(await screen.findByTestId("obs-sessions-not-configured")).toBeInTheDocument();
  });
});

describe("one session", () => {
  // The fixture's event timestamps are epoch milliseconds taken from the clock, so a
  // clock whose timestamps contain "4242" must not read as a leaked card number.
  it.each([
    ["the current clock", undefined],
    ["a clock whose timestamps contain the card digits", 1793344242000 + 6 * 60_000],
  ])("opens from the list into its header, masked replay and timeline (%s)", async (_name, pinnedNow) => {
    if (pinnedNow !== undefined) vi.useFakeTimers({ toFake: ["Date"], now: pinnedNow });
    const harness = renderPanel(createFixtureApi(), { view: "sessions", range: "24h" }, PROJECT_PLACE);
    await screen.findByTestId("obs-sessions-list");
    await userEvent.setup().click(screen.getByTestId(`obs-session-open-${FIXTURE_STORY_SESSION}`));
    expect(harness.current().session).toBe(FIXTURE_STORY_SESSION);

    const header = await screen.findByTestId("session-header");
    expect(screen.getByTestId("session-title").textContent).toMatch(/^shop-web · (Today|Yesterday) \d{2}:\d{2}$/);
    expect(within(header).getByRole("button", { name: `Copy ${FIXTURE_STORY_SESSION}` })).toBeInTheDocument();
    expect(header).toHaveTextContent("u_8812");
    expect(header).toHaveTextContent("/checkout/payment");
    expect(header).toHaveTextContent("1 error");
    await waitFor(() => expect(screen.getByTestId("replay-player")).toHaveAttribute("data-state", "ready"));
    expect(screen.getByTestId("replay-masked-badge")).toBeInTheDocument();
    // The fixture recording is masked as recorded: no readable page text reaches the Replayer.
    const recorded = recordedText(FakeReplayer.last().events);
    expect(recorded).not.toMatch(/Checkout|Pay now|4242/);
    expect(recorded).toContain("********");

    const timeline = screen.getByTestId("session-timeline");
    expect(within(timeline).getAllByTestId(/^session-entry-/).length).toBeGreaterThan(4);
    expect(within(timeline).getByTestId("session-entry-request_failed")).toHaveTextContent("500");
  });

  it("says where the browser was, and opens every trace of the session", async () => {
    // Opened from a list narrowed to its frontend: the traces must not stay
    // narrowed to it, or the backend work the session caused is excluded.
    // Nor a client or build filter: fe would exclude every backend trace.
    const harness = renderPanel(createFixtureApi(), { view: "sessions", session: FIXTURE_STORY_SESSION, service: "shop-web", browser: "Chrome", os: "macOS", fe: "v1" }, PROJECT_PLACE);
    const place = await screen.findByTestId("session-place");
    expect(place).toHaveTextContent(/Berlin/);
    expect(place.getAttribute("title")).toContain("DB-IP");
    await userEvent.setup().click(screen.getByTestId("session-traces"));
    expect(harness.current()).toMatchObject({ view: "traces", rsession: FIXTURE_STORY_SESSION });
    expect(harness.current().service).toBeUndefined();
    expect(harness.current().session).toBeUndefined();
    expect(harness.current().trace).toBeUndefined();
    expect(harness.current().browser).toBeUndefined();
    expect(harness.current().os).toBeUndefined();
    expect(harness.current().fe).toBeUndefined();
  });

  it("links an error to its issue and a failed request to its trace", async () => {
    const harness = renderPanel(createFixtureApi(), { view: "sessions", session: FIXTURE_STORY_SESSION }, PROJECT_PLACE);
    const user = userEvent.setup();
    await user.click(await screen.findByTestId("session-entry-issue-iss-checkout-total"));
    expect(harness.navigations.at(-1)).toMatchObject({ view: "errors", issue: "iss-checkout-total", session: undefined, t: undefined });
    expect(harness.current()).toMatchObject({ view: "errors", issue: "iss-checkout-total" });
    expect(harness.current().session).toBeUndefined();
  });

  it("opens a request's trace", async () => {
    const harness = renderPanel(createFixtureApi(), { view: "sessions", session: FIXTURE_STORY_SESSION }, PROJECT_PLACE);
    const request = await screen.findByTestId("session-entry-request_failed");
    await userEvent.setup().click(within(request).getByTestId(/^session-entry-trace-/));
    expect(harness.current()).toMatchObject({ view: "traces", trace: "4bf92f3577b34da6a3ce929d0e0e4736", span: "00f067aa0ba902b7" });
    expect(harness.current().session).toBeUndefined();
  });

  it("seeks the replay to a timeline entry", async () => {
    renderPanel(createFixtureApi(), { view: "sessions", session: FIXTURE_STORY_SESSION }, PROJECT_PLACE);
    await waitFor(() => expect(screen.getByTestId("replay-player")).toHaveAttribute("data-state", "ready"));
    const click = (await screen.findAllByTestId("session-entry-click"))[1];
    await userEvent.setup().click(within(click).getByTestId("session-entry-seek"));
    await waitFor(() => expect(FakeReplayer.last().calls).toContain("pause:8000"));
  });

  it("opens a session without a replay (by id, from a trace) as its timeline alone", async () => {
    const api = createFixtureApi();
    const unrecorded = (await api.listSessions({ orgId: "o", envIds: [], window: { startMs: 0, endMs: Date.now() + 1 }, hasError: false, hasReplay: false, userId: "", pathContains: "", pageSize: 100 })).sessions.find((s) => !s.hasReplay)!;
    renderPanel(api, { view: "sessions", session: unrecorded.sessionId }, PROJECT_PLACE);
    expect(await screen.findByTestId("session-timeline")).toBeInTheDocument();
    expect(screen.queryByTestId("session-no-replay")).not.toBeInTheDocument();
    expect(screen.queryByText(/No replay was recorded/)).not.toBeInTheDocument();
    // No empty replay panel beside it: the timeline takes the width.
    expect(screen.queryByTestId("session-replay")).not.toBeInTheDocument();
  });

  it("says a session outside the scope isn't here", async () => {
    renderPanel(createFixtureApi(), { view: "sessions", session: "nope" }, PROJECT_PLACE);
    expect(await screen.findByTestId("session-not-found")).toBeInTheDocument();
  });
});

describe("links into a session", () => {
  it("from an error's latest event, a few seconds before it", async () => {
    const harness = renderPanel(createFixtureApi(), { view: "errors", issue: "iss-checkout-total" }, ENV_PLACE);
    const link = (await screen.findAllByTestId(/^obs-issue-session-sess-/))[0];
    const sessionId = link.getAttribute("data-testid")!.replace("obs-issue-session-", "");
    // Named by the session it opens, once its summary is read — not by the id.
    await waitFor(() => expect(within(link).getByTestId("session-name")).toHaveAttribute("data-state", "ready"));
    expect(link.textContent).toMatch(/^shop-web · /);
    await userEvent.setup().click(link);
    const next = harness.current();
    expect(next).toMatchObject({ view: "sessions", session: sessionId });
    expect(next.issue).toBeUndefined();
    expect(typeof next.t).toBe("number");
  });

  it("from a trace whose browser spans carry rum.sessionId", async () => {
    const base = createFixtureApi();
    const api = {
      ...base,
      getTrace: async (req: Parameters<typeof base.getTrace>[0]) => {
        const trace = fixtureTrace(req.traceId, req.window.endMs);
        trace.spans[0] = { ...trace.spans[0], resource: { ...trace.spans[0].resource, "rum.sessionId": FIXTURE_STORY_SESSION } };
        return trace;
      },
    };
    const harness = renderPanel(api, { view: "traces", trace: "4bf92f3577b34da6a3ce929d0e0e4736" }, ENV_PLACE);
    const button = await screen.findByTestId("trace-session");
    // The story session has a recording, so the link says it opens a replay.
    await waitFor(() => expect(button.textContent).toMatch(/^Replay · shop-web · (Today|Yesterday) \d{2}:\d{2}$/));
    await userEvent.setup().click(button);
    expect(harness.current()).toMatchObject({ view: "sessions", session: FIXTURE_STORY_SESSION });
    expect(harness.current().trace).toBeUndefined();
  });

  it("from a backend-only trace whose server span carries the session.id baggage", async () => {
    const base = createFixtureApi();
    const api = {
      ...base,
      getTrace: async (req: Parameters<typeof base.getTrace>[0]) => {
        const trace = fixtureTrace(req.traceId, req.window.endMs);
        trace.spans[0] = { ...trace.spans[0], attributes: { ...trace.spans[0].attributes, "session.id": FIXTURE_STORY_SESSION } };
        return trace;
      },
    };
    const harness = renderPanel(api, { view: "traces", trace: "4bf92f3577b34da6a3ce929d0e0e4736" }, ENV_PLACE);
    await userEvent.setup().click(await screen.findByTestId("trace-session"));
    expect(harness.current()).toMatchObject({ view: "sessions", session: FIXTURE_STORY_SESSION });
  });

  it("not from a trace with no browser session", async () => {
    renderPanel(createFixtureApi(), { view: "traces", trace: "4bf92f3577b34da6a3ce929d0e0e4736" }, ENV_PLACE);
    await screen.findByTestId("trace-detail");
    expect(screen.queryByTestId("trace-session")).not.toBeInTheDocument();
  });

  it("from a dashboard's sessions panel row, named like the list names it", () => {
    const startMs = new Date(2024, 2, 9, 14, 32).getTime();
    const data = {
      sessions: [
        { sessionId: FIXTURE_STORY_SESSION, serviceName: "shop-web", userId: "u_8812", entryPath: "/checkout", startMs, endMs: startMs + 1, pageViews: 2, errors: 1, hasReplay: true },
        { sessionId: "sess-nameless", serviceName: "", userId: "", entryPath: "", startMs, endMs: startMs + 1, pageViews: 1, errors: 0, hasReplay: false },
      ],
    } as unknown as PanelResult;
    render(
      <PanelBody
        panel={{ id: "s", title: "Sessions", kind: "sessions", signal: "traces", series: [], width: 8, height: 6 } as never}
        query={{ data: { ...emptyResult(), ...data }, isLoading: false, error: null }}
        links={{ sessionHref: (id) => `/forge/p/shop/observability/sessions?session=${id}` }}
      />
    );
    const link = screen.getByTestId(`obs-panel-session-${FIXTURE_STORY_SESSION}`);
    expect(link).toHaveAttribute("href", `/forge/p/shop/observability/sessions?session=${FIXTURE_STORY_SESSION}`);
    expect(link.textContent).toMatch(/^shop-web · Mar 9, 2024, 14:32$/);
    // A frontend that did not say its name is an unknown app — never its bare id.
    expect(screen.getByTestId("obs-panel-session-sess-nameless").textContent).toMatch(/^Unknown app · Mar 9, 2024, 14:32$/);
  });
});

/** Every string the recording carries (page text, attributes, CSS), never its numbers: timestamps are not page content. */
function recordedText(value: unknown): string {
  if (typeof value === "string") return value;
  if (Array.isArray(value)) return value.map(recordedText).join("\n");
  if (value && typeof value === "object") return Object.values(value).map(recordedText).join("\n");
  return "";
}

function emptyResult(): PanelResult {
  return { series: [], rows: [], columns: [], funnelSteps: [], issues: [], sessions: [] } as unknown as PanelResult;
}
