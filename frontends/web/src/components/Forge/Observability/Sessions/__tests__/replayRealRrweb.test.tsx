// Copyright (c) 2025 Reliant Labs

/**
 * The player against the REAL rrweb Replayer (@rrweb/replay, the version the
 * console ships), not FakeReplayer: an event stream shaped like a stored prod
 * recording, read through GetSessionReplay pages, must build its first frame
 * into the sandboxed iframe and then PLAY — the clock moves past 0:00 and the
 * page changes as recorded.
 *
 * The other player tests pin the contract with rrweb through a fake. This one
 * proves the contract is the one rrweb actually honours, on the shape the
 * recorder (forge-web-runtime's HyperDX session recorder, rrweb 2.x) uploads:
 *
 *   - the previous session's tail first: incremental events from before this
 *     recording's meta event, exported under its id at a session boundary;
 *   - meta (type 4, the recorded viewport), then a full snapshot (type 2) a
 *     few ms later, holding a masked page with inline styles;
 *   - incremental events (type 3) with millisecond timestamps: a mutation
 *     that adds a node, a text change, mouse moves (timeOffset <= 0), and a
 *     scroll;
 *   - more than one page (a small byte budget), so the feed's paging and the
 *     Replayer's addEvent path are both on it.
 *
 * jsdom builds rrweb's iframe document (about:blank, same origin) and its
 * DOM; it lays nothing out, so this asserts on the rebuilt DOM and the clock,
 * not on pixels. jsdom also lacks HTMLIFrameElement.sandbox (the DOMTokenList
 * every browser has), and rrweb refuses to rebuild into a frame it cannot
 * prove is sandboxed, so the test gives jsdom that one property, read from the
 * attribute exactly as a browser reads it. The player's own sandbox is still
 * asserted, on the attribute.
 */

import { beforeAll, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import type { ReplayPage, ReplayRequest, RrwebEvent, SessionSummary } from "@/services/controlPlane/observability/rumTypes";

import { createFixtureApi } from "../../__fixtures__/fixtureApi";
import { renderWithApi } from "../../__tests__/obsTestUtils";
import { ReplayPlayer, type ReplayerCtor } from "../ReplayPlayer";
import { NO_CLIENT } from "@/services/controlPlane/observability/client";

vi.mock("uplot", async () => (await import("../../__tests__/obsMocks")).obsMocks.uplot());
vi.mock("@tanstack/react-virtual", async () => (await import("../../__tests__/obsMocks")).obsMocks.virtual());

// A recording's own clock: epoch milliseconds, as the recorder stamps them.
const T = Date.UTC(2026, 10, 3, 18, 9, 31, 640);
const SCOPE = { orgId: "org-fixture", projectId: "shop", envIds: ["env-prod"] };
const SESSION: SessionSummary = {
  sessionId: "8060432f94db5780f71a9e3c36a549df",
  service: "reliant-web",
  serviceVersion: "1.8.15",
  envId: "env-prod",
  envName: "prod",
  userId: "",
  entryPath: "/",
  startedMs: T - 24_000,
  endedMs: T + 8_000,
  pageViews: 0,
  interactions: 0,
  errors: 0,
  hasReplay: true,
  country: "",
  region: "",
  city: "",
  clientAddress: "",
  client: NO_CLIENT,
};

// rrweb's serialized node kinds and incremental sources (rrweb-snapshot / @rrweb/types).
const DOCUMENT = 0;
const DOCTYPE = 1;
const ELEMENT = 2;
const TEXT = 3;
const MUTATION = 0;
const MOUSE_MOVE = 1;
const SCROLL = 3;

const el = (id: number, tagName: string, attributes: Record<string, string>, childNodes: unknown[] = []) => ({ type: ELEMENT, id, tagName, attributes, childNodes });
const text = (id: number, textContent: string) => ({ type: TEXT, id, textContent });

/** A stored prod recording's shape: tail of the previous session, meta, full snapshot, then activity. */
function prodShapedRecording(): RrwebEvent[] {
  const tail: RrwebEvent[] = [
    { type: 3, timestamp: T - 2_000, data: { source: MUTATION, texts: [{ id: 999_001, value: "*****" }], attributes: [], removes: [], adds: [] } },
    { type: 3, timestamp: T - 1_500, data: { source: MOUSE_MOVE, positions: [{ x: 10, y: 10, id: 999_002, timeOffset: 0 }] } },
  ];
  const meta: RrwebEvent = { type: 4, timestamp: T, data: { href: "app://bundle/", width: 1846, height: 1290 } };
  const snapshot: RrwebEvent = {
    type: 2,
    timestamp: T + 17,
    data: {
      initialOffset: { left: 0, top: 0 },
      node: {
        type: DOCUMENT,
        id: 1,
        childNodes: [
          { type: DOCTYPE, id: 2, name: "html", publicId: "", systemId: "" },
          el(3, "html", { lang: "en", class: "dark" }, [
            el(4, "head", {}, [el(5, "style", {}, [{ type: TEXT, id: 6, textContent: "body { margin: 0; background: rgb(21, 19, 24); } .row { height: 24px; }", isStyle: true }])]),
            el(7, "body", {}, [
              el(8, "div", { id: "root" }, [
                el(9, "header", { class: "row" }, [text(10, "******* *******")]),
                el(11, "main", { id: "chat" }, [el(12, "p", { class: "row" }, [text(13, "**** ** *****")])]),
              ]),
            ]),
          ]),
        ],
      },
    },
  };
  const later: RrwebEvent[] = [
    { type: 3, timestamp: T + 400, data: { source: MOUSE_MOVE, positions: [{ x: 1698, y: 1661, id: 12, timeOffset: -218 }] } },
    // A new message arrives: a node added under <main>, with its text.
    {
      type: 3,
      timestamp: T + 1_200,
      data: {
        source: MUTATION,
        texts: [],
        attributes: [],
        removes: [],
        adds: [
          { parentId: 11, nextId: null, node: el(14, "p", { class: "row", "data-testid": "recorded-message" }, []) },
          { parentId: 14, nextId: null, node: text(15, "***** ****") },
        ],
      },
    },
    // Its text streams in.
    { type: 3, timestamp: T + 2_400, data: { source: MUTATION, texts: [{ id: 15, value: "***** **** *** *****" }], attributes: [], removes: [], adds: [] } },
    { type: 3, timestamp: T + 3_000, data: { source: SCROLL, id: 11, x: 0, y: 48 } },
    { type: 3, timestamp: T + 4_000, data: { source: MOUSE_MOVE, positions: [{ x: 900, y: 600, id: 14, timeOffset: 0 }] } },
  ];
  return [...tail, meta, snapshot, ...later];
}

/** GetSessionReplay over `events`, a byte budget per page, as the server pages it. */
function pagedApi(events: RrwebEvent[], maxBytes: number) {
  const getSessionReplay = vi.fn(async (req: ReplayRequest): Promise<ReplayPage> => {
    const offset = req.pageToken ? Number(req.pageToken) : 0;
    let end = offset;
    let bytes = 0;
    while (end < events.length && (end === offset || bytes + JSON.stringify(events[end]).length <= maxBytes)) {
      bytes += JSON.stringify(events[end]).length;
      end++;
    }
    return { events: events.slice(offset, end), unreadable: 0, nextPageToken: end < events.length ? String(end) : "", resumeToken: String(end) };
  });
  return { api: { ...createFixtureApi(), getSessionReplay }, getSessionReplay };
}

const realReplayer = async (): Promise<ReplayerCtor> => (await import("@rrweb/replay")).Replayer as unknown as ReplayerCtor;

beforeAll(() => {
  if ("sandbox" in HTMLIFrameElement.prototype) return;
  // The browser's HTMLIFrameElement.sandbox: the attribute's tokens, live.
  Object.defineProperty(HTMLIFrameElement.prototype, "sandbox", {
    configurable: true,
    get(this: HTMLIFrameElement) {
      const tokens = (this.getAttribute("sandbox") ?? "").split(/\s+/).filter(Boolean);
      return Object.assign(tokens, { contains: (token: string) => tokens.includes(token), value: tokens.join(" ") });
    },
  });
});

const replayDocument = () => {
  const iframe = screen.getByTestId("replay-mount").querySelector("iframe");
  expect(iframe).not.toBeNull();
  return iframe!.contentDocument!;
};

describe("the player on the real rrweb Replayer", () => {
  it("builds the first frame of a prod-shaped recording and plays it past 0:00", async () => {
    const { api, getSessionReplay } = pagedApi(prodShapedRecording(), 1_500);
    renderWithApi(api, <ReplayPlayer scope={SCOPE} session={SESSION} live={false} loadReplayer={realReplayer} feedTiming={{ aheadMs: 60_000 }} />);

    const player = await screen.findByTestId("replay-player");
    await waitFor(() => expect(player).toHaveAttribute("data-state", "ready"));
    // More than one page was read, and every page was appended.
    await waitFor(() => expect(player).toHaveAttribute("data-phase", "complete"));
    expect(getSessionReplay.mock.calls.length).toBeGreaterThan(1);

    // The first frame: the recorded page, rebuilt in rrweb's sandboxed iframe,
    // without the previous session's tail and without the later message.
    const iframe = screen.getByTestId("replay-mount").querySelector("iframe")!;
    expect(iframe.getAttribute("sandbox")).toBe("allow-same-origin");
    const doc = replayDocument();
    expect(doc.querySelector("#chat")?.textContent).toBe("**** ** *****");
    expect(doc.querySelector('[data-testid="recorded-message"]')).toBeNull();
    // The recording is 4 s from its meta event: the tail before it is not on the clock.
    expect(screen.getByTestId("replay-time")).toHaveTextContent("0:00 / 0:04");

    const user = userEvent.setup();
    await user.click(screen.getByTestId("replay-speed-8"));
    await user.click(screen.getByTestId("replay-play"));
    expect(screen.getByTestId("replay-play")).toHaveAttribute("aria-pressed", "true");

    // It plays: the recorded message appears, its text streams in, and the clock moves.
    await waitFor(() => expect(replayDocument().querySelector('[data-testid="recorded-message"]')?.textContent).toBe("***** **** *** *****"), { timeout: 5_000 });
    await waitFor(() => expect(screen.getByTestId("replay-time").textContent).not.toMatch(/^0:00 \//), { timeout: 5_000 });
    // And it reaches the end of the recording.
    await waitFor(() => expect(screen.getByTestId("replay-play")).toHaveAttribute("aria-pressed", "false"), { timeout: 5_000 });
    expect(screen.getByTestId("replay-time")).toHaveTextContent("0:04 / 0:04");
  });

  it("seeks the real Replayer: the scrubber lands on the frame recorded at that moment", async () => {
    const { api } = pagedApi(prodShapedRecording(), 64 * 1024);
    renderWithApi(api, <ReplayPlayer scope={SCOPE} session={SESSION} live={false} loadReplayer={realReplayer} />);
    const player = await screen.findByTestId("replay-player");
    await waitFor(() => expect(player).toHaveAttribute("data-state", "ready"));

    const seek = screen.getByTestId("replay-seek");
    // 1.8 s in: the message has been added, its text not yet streamed.
    fireEvent.change(seek, { target: { value: "1800" } });
    await waitFor(() => expect(replayDocument().querySelector('[data-testid="recorded-message"]')?.textContent).toBe("***** ****"));
    expect(screen.getByTestId("replay-time")).toHaveTextContent("0:01 / 0:04");

    // Back to the start: the first frame again.
    fireEvent.change(seek, { target: { value: "0" } });
    await waitFor(() => expect(replayDocument().querySelector('[data-testid="recorded-message"]')).toBeNull());
  });
});
