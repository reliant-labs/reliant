// Copyright (c) 2025 Reliant Labs

/**
 * The replay player against scripted replay pages and a recording Replayer
 * (fakeReplayer.ts): it starts on page one and appends the rest, buffers
 * toward a seek before landing it, follows a live session with resume_token,
 * pins the sandbox and the masked badge, and says plainly when a recording is
 * empty, unplayable, partial or has gaps — never unmasking anything.
 */

import { describe, expect, it, vi, beforeEach } from "vitest";
import { useState } from "react";
import { act, fireEvent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import type { ReplayPage, ReplayRequest, RrwebEvent, SessionSummary } from "@/services/controlPlane/observability/rumTypes";

import { createFixtureApi } from "../../__fixtures__/fixtureApi";
import { renderWithApi } from "../../__tests__/obsTestUtils";
import { ReplayPlayer, type ReplayerCtor, type ReplayPlayerProps } from "../ReplayPlayer";
import { FakeReplayer } from "./fakeReplayer";
import { isReplayKnownUnplayable, resetReplayVerdictsForTests } from "../sessionModel";

vi.mock("uplot", async () => (await import("../../__tests__/obsMocks")).obsMocks.uplot());
vi.mock("@tanstack/react-virtual", async () => (await import("../../__tests__/obsMocks")).obsMocks.virtual());

const T = Date.UTC(2026, 9, 24, 12);
const SCOPE = { orgId: "org-fixture", projectId: "shop", envIds: ["env-prod"] };

const SESSION: SessionSummary = {
  sessionId: "sess-1",
  service: "shop-web",
  serviceVersion: "v1.8.3",
  envId: "env-prod",
  envName: "prod",
  userId: "u_1",
  entryPath: "/checkout",
  startedMs: T,
  endedMs: T + 30_000,
  pageViews: 2,
  interactions: 4,
  errors: 1,
  hasReplay: true,
  country: "",
  region: "",
  city: "",
  clientAddress: "",
};

const meta = (at: number): RrwebEvent => ({ type: 4, timestamp: T + at, data: { href: "https://shop.example/", width: 1280, height: 720 } });
const snapshot = (at: number): RrwebEvent => ({ type: 2, timestamp: T + at, data: { node: { type: 0, id: 1, childNodes: [] }, initialOffset: { left: 0, top: 0 } } });
const step = (at: number): RrwebEvent => ({ type: 3, timestamp: T + at, data: { source: 1, positions: [] } });

/** An API whose GetSessionReplay answers `pages` in turn (the rest from the fixture). */
function scripted(pages: (ReplayPage | Error)[]) {
  const getSessionReplay = vi.fn(async (_req: ReplayRequest): Promise<ReplayPage> => {
    const next = pages.shift();
    if (!next) return { events: [], unreadable: 0, nextPageToken: "", resumeToken: "" };
    if (next instanceof Error) throw next;
    return next;
  });
  return { api: { ...createFixtureApi(), getSessionReplay }, getSessionReplay };
}

const page = (events: RrwebEvent[], over: Partial<ReplayPage> = {}): ReplayPage => ({ events, unreadable: 0, nextPageToken: "", resumeToken: events.length ? "r" : "", ...over });

const loadFake: () => Promise<ReplayerCtor> = async () => FakeReplayer as unknown as ReplayerCtor;

function renderPlayer(api: ReturnType<typeof scripted>["api"], props: Partial<ReplayPlayerProps> = {}) {
  return renderWithApi(api, <ReplayPlayer scope={SCOPE} session={SESSION} live={false} loadReplayer={loadFake} {...props} />);
}

beforeEach(() => {
  FakeReplayer.reset();
  resetReplayVerdictsForTests();
});

describe("playback", () => {
  it("starts from page one, appends later pages, and pins the sandbox and the masked badge", async () => {
    const { api, getSessionReplay } = scripted([
      page([meta(0), snapshot(10), step(1000)], { nextPageToken: "p2", resumeToken: "r1" }),
      page([step(2000), step(3000)], { resumeToken: "r2" }),
    ]);
    renderPlayer(api, { pageBytes: 1024 });
    await waitFor(() => expect(screen.getByTestId("replay-player")).toHaveAttribute("data-state", "ready"));
    await waitFor(() => expect(screen.getByTestId("replay-player")).toHaveAttribute("data-phase", "complete"));

    const replayer = FakeReplayer.last();
    expect(FakeReplayer.instances).toHaveLength(1);
    // The Replayer gets page one as recorded, then the rest appended in order.
    expect(replayer.events.map((e) => e.timestamp - T)).toEqual([0, 10, 1000, 2000, 3000]);
    expect(replayer.config).toMatchObject({ UNSAFE_replayCanvas: false, skipInactive: true, speed: 1 });
    expect(replayer.iframe.getAttribute("sandbox")).toBe("allow-same-origin");
    expect(replayer.iframe.getAttribute("title")).toBe("Session replay (masked)");
    expect(screen.getByTestId("replay-masked-badge")).toHaveTextContent("Masked recording");

    const [first, second] = getSessionReplay.mock.calls.map(([req]) => req);
    expect(first).toMatchObject({ sessionId: "sess-1", maxBytes: 1024, pageToken: undefined, orgId: "org-fixture" });
    expect(second.pageToken).toBe("p2");
  });

  it("starts playback at the recording's own snapshot, past the previous session's tail", async () => {
    const { api } = scripted([page([step(0), step(500), meta(1000), snapshot(1010), step(2000)])]);
    renderPlayer(api);
    await waitFor(() => expect(screen.getByTestId("replay-player")).toHaveAttribute("data-state", "ready"));
    expect(FakeReplayer.last().events.map((e) => e.timestamp - T)).toEqual([1000, 1010, 2000]);
    expect(isReplayKnownUnplayable("sess-1")).toBe(false);
  });

  it("plays, pauses, changes speed and skips inactivity through the Replayer", async () => {
    const { api } = scripted([page([meta(0), snapshot(10), step(5000)])]);
    renderPlayer(api);
    await waitFor(() => expect(screen.getByTestId("replay-player")).toHaveAttribute("data-state", "ready"));
    const replayer = FakeReplayer.last();
    const user = userEvent.setup();

    // It opens just past the full snapshot (10 ms after the meta event) and plays from there.
    expect(replayer.calls).toEqual(["pause:11"]);
    await user.click(screen.getByTestId("replay-play"));
    expect(replayer.calls).toContain("play:11");
    expect(screen.getByTestId("replay-play")).toHaveAttribute("aria-pressed", "true");
    await user.click(screen.getByTestId("replay-play"));
    expect(replayer.calls.at(-1)).toBe("pause:");

    await user.click(screen.getByTestId("replay-speed-4"));
    expect(replayer.config.speed).toBe(4);
    await user.click(screen.getByTestId("replay-skip-inactive"));
    expect(replayer.config.skipInactive).toBe(false);

    act(() => replayer.emit("finish"));
    expect(screen.getByTestId("replay-play")).toHaveAttribute("aria-pressed", "false");
  });

  it("opens at the linked moment, reading ahead until the recording reaches it", async () => {
    const { api, getSessionReplay } = scripted([
      page([meta(0), snapshot(10), step(1000)], { nextPageToken: "p2" }),
      page([step(10_000), step(20_000)], { nextPageToken: "p3" }),
      page([step(25_000)]),
    ]);
    // A link from an error at T+15s: the player opens at 15s, not at the start.
    renderPlayer(api, { startAtMs: T + 15_000, feedTiming: { aheadMs: 1_000 } });
    await waitFor(() => expect(screen.getByTestId("replay-player")).toHaveAttribute("data-state", "ready"));
    await waitFor(() => expect(FakeReplayer.last().calls).toContain("pause:15000"));
    // 20s is far enough past 15s: the third page waits until the reader gets near it.
    expect(getSessionReplay.mock.calls.map(([req]) => req.pageToken)).toEqual([undefined, "p2"]);
  });

  it("seeks from the scrubber, and from the timeline", async () => {
    const { api } = scripted([page([meta(0), snapshot(10), step(20_000)])]);
    const onTime = vi.fn();
    function Harness() {
      const [seek, setSeek] = useState<{ atMs: number; nonce: number } | undefined>(undefined);
      return (
        <>
          <button type="button" onClick={() => setSeek({ atMs: T + 12_000, nonce: 1 })}>
            timeline entry
          </button>
          <ReplayPlayer scope={SCOPE} session={SESSION} live={false} loadReplayer={loadFake} onTime={onTime} seek={seek} />
        </>
      );
    }
    renderWithApi(api, <Harness />);
    await waitFor(() => expect(screen.getByTestId("replay-player")).toHaveAttribute("data-state", "ready"));
    const replayer = FakeReplayer.last();

    fireEvent.change(screen.getByTestId("replay-seek"), { target: { value: "8000" } });
    expect(replayer.calls).toContain("pause:8000");
    expect(onTime).toHaveBeenLastCalledWith(T + 8000);

    await userEvent.setup().click(screen.getByText("timeline entry"));
    await waitFor(() => expect(replayer.calls).toContain("pause:12000"));
    expect(onTime).toHaveBeenLastCalledWith(T + 12_000);
  });

  it("never seeks into the gap between the meta event and the full snapshot", async () => {
    // rrweb's clock starts at the meta event; a seek before the snapshot,
    // after playback, leaves the last played frame on screen (see
    // replayRealRrweb.test.tsx for the real Replayer doing so).
    const { api } = scripted([page([meta(0), snapshot(17), step(20_000)])]);
    renderPlayer(api);
    await waitFor(() => expect(screen.getByTestId("replay-player")).toHaveAttribute("data-state", "ready"));
    const replayer = FakeReplayer.last();

    fireEvent.change(screen.getByTestId("replay-seek"), { target: { value: "8000" } });
    fireEvent.change(screen.getByTestId("replay-seek"), { target: { value: "0" } });
    expect(replayer.calls.at(-1)).toBe("pause:18");

    // Played to the end, Play starts over from the snapshot too.
    fireEvent.change(screen.getByTestId("replay-seek"), { target: { value: "20000" } });
    await userEvent.setup().click(screen.getByTestId("replay-play"));
    expect(replayer.calls.at(-1)).toBe("play:18");
  });
});

describe("a live session", () => {
  it("polls with resume_token and appends what was recorded since", async () => {
    const { api, getSessionReplay } = scripted([
      page([meta(0), snapshot(10)], { resumeToken: "after-2" }),
      page([], { resumeToken: "" }),
      page([step(6000)], { resumeToken: "after-3" }),
    ]);
    renderPlayer(api, { live: true, feedTiming: { pollMs: 10 } });
    await waitFor(() => expect(screen.getByTestId("replay-player")).toHaveAttribute("data-state", "ready"));
    expect(screen.getByTestId("replay-live")).toHaveTextContent("Live");
    expect(screen.getByTestId("replay-notice-live")).toBeInTheDocument();
    await waitFor(() => expect(FakeReplayer.last().events.map((e) => e.timestamp - T)).toEqual([0, 10, 6000]));
    expect(getSessionReplay.mock.calls.slice(0, 3).map(([req]) => req.pageToken)).toEqual([undefined, "after-2", "after-2"]);
  });

  it("waits for the recording when nothing has finished uploading yet", async () => {
    const { api } = scripted([page([])]);
    renderPlayer(api, { live: true, feedTiming: { pollMs: 60_000 } });
    expect(await screen.findByTestId("replay-waiting")).toBeInTheDocument();
  });
});

describe("an incomplete recording", () => {
  it("says it cannot be played without a full snapshot, and never builds a Replayer", async () => {
    const { api } = scripted([page([meta(0), step(1000), step(2000)])]);
    renderPlayer(api);
    expect(await screen.findByTestId("replay-incomplete")).toHaveTextContent(/no full snapshot/);
    expect(screen.getByTestId("replay-player")).toHaveAttribute("data-state", "incomplete");
    expect(FakeReplayer.instances).toHaveLength(0);
  });

  it("does not offer a one-event recording, and tells the list it is unplayable", async () => {
    // A session boundary's leftover: one incremental event, stored under the next session id.
    const { api } = scripted([page([step(1000)])]);
    renderPlayer(api);
    expect(await screen.findByTestId("replay-incomplete")).toHaveTextContent(/It holds 1 event but no full snapshot/);
    expect(screen.queryByTestId("replay-play")).not.toBeInTheDocument();
    expect(FakeReplayer.instances).toHaveLength(0);
    // Noted by a passive effect after the render that shows the notice, so it
    // can land a tick after findByTestId resolves (1 run in 4 under load).
    await waitFor(() => expect(isReplayKnownUnplayable("sess-1")).toBe(true));
  });

  it("does not offer a snapshot with nothing recorded after it, even after a stray earlier event", async () => {
    const { api } = scripted([page([step(0), meta(10), snapshot(20)])]);
    renderPlayer(api);
    expect(await screen.findByTestId("replay-incomplete")).toHaveTextContent(/single snapshot of the page and nothing recorded after it/);
    expect(FakeReplayer.instances).toHaveLength(0);
    // The same passive effect as above: wait for it, do not race it.
    await waitFor(() => expect(isReplayKnownUnplayable("sess-1")).toBe(true));
  });

  it("says nothing was kept when a recorded session has no events left", async () => {
    const { api } = scripted([page([])]);
    renderPlayer(api);
    expect(await screen.findByTestId("replay-empty")).toHaveTextContent(/retention|uploaded/);
  });

  it("notes a recording that stops well before its session, and events it could not read", async () => {
    const { api } = scripted([page([meta(0), snapshot(10), step(4000)], { unreadable: 2 })]);
    renderPlayer(api, { session: { ...SESSION, endedMs: T + 120_000 } });
    expect(await screen.findByTestId("replay-notice-partial")).toHaveTextContent(/stops .* before the session's last activity/);
    expect(screen.getByTestId("replay-notice-unreadable")).toHaveTextContent("2 recorded events could not be read");
  });

  it("keeps what arrived when a later page fails, and resumes on retry", async () => {
    const { api, getSessionReplay } = scripted([
      page([meta(0), snapshot(10), step(1000)], { nextPageToken: "p2" }),
      new Error("network"),
      page([step(2000)]),
    ]);
    renderPlayer(api);
    const retry = await screen.findByTestId("replay-notice-retry");
    expect(screen.getByTestId("replay-player")).toHaveAttribute("data-state", "ready");
    await userEvent.setup().click(retry);
    await waitFor(() => expect(screen.getByTestId("replay-player")).toHaveAttribute("data-phase", "complete"));
    expect(getSessionReplay.mock.calls.map(([req]) => req.pageToken)).toEqual([undefined, "p2", "p2"]);
    expect(FakeReplayer.last().events.map((e) => e.timestamp - T)).toEqual([0, 10, 1000, 2000]);
  });

  it("offers a retry when the player itself fails to load", async () => {
    const { api } = scripted([page([meta(0), snapshot(10), step(1000)]), page([meta(0), snapshot(10), step(1000)])]);
    let fail = true;
    const loadReplayer = async () => {
      if (fail) throw new Error("Failed to fetch dynamically imported module");
      return FakeReplayer as unknown as ReplayerCtor;
    };
    renderPlayer(api, { loadReplayer });
    const retry = await screen.findByTestId("replay-player-error-retry");
    fail = false;
    await userEvent.setup().click(retry);
    await waitFor(() => expect(screen.getByTestId("replay-player")).toHaveAttribute("data-state", "ready"));
  });
});
