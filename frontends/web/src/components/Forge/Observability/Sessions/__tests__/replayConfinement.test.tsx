// Copyright (c) 2025 Reliant Labs

/**
 * A replay is rebuilt from events the viewer's own browser never saw recorded:
 * the ingest key is public and bodies are never inspected, so an uploader can
 * name any remote image, stylesheet or font. Opening such a session must not
 * contact those hosts. The sandbox (no scripts) is pinned beside the policy.
 *
 * jsdom neither loads subresources nor enforces CSP, so FakeReplayer models the
 * browser: it "requests" every remote URL the recording names unless the iframe
 * document carries a Content-Security-Policy meta that forbids it. The policy
 * itself was also checked against the real rrweb 2.1.7 in headless Chromium.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";

import type { ReplayPage, ReplayRequest, RrwebEvent, SessionSummary } from "@/services/controlPlane/observability/rumTypes";

import { createFixtureApi } from "../../__fixtures__/fixtureApi";
import { renderWithApi } from "../../__tests__/obsTestUtils";
import { ReplayPlayer, type ReplayerCtor } from "../ReplayPlayer";
import { confineReplayDocument, REPLAY_CSP } from "../replayConfinement";
import { FakeReplayer } from "./fakeReplayer";

vi.mock("uplot", async () => (await import("../../__tests__/obsMocks")).obsMocks.uplot());
vi.mock("@tanstack/react-virtual", async () => (await import("../../__tests__/obsMocks")).obsMocks.virtual());

const T = Date.UTC(2026, 9, 24, 12);
const SCOPE = { orgId: "org-fixture", projectId: "shop", envIds: ["env-prod"] };
const SESSION: SessionSummary = {
  sessionId: "sess-forged",
  service: "shop-web",
  serviceVersion: "v1",
  envId: "env-prod",
  envName: "prod",
  userId: "",
  entryPath: "/",
  startedMs: T,
  endedMs: T + 30_000,
  pageViews: 1,
  interactions: 0,
  errors: 0,
  hasReplay: true,
};

const ATTACKER = "https://attacker.example";
const el = (id: number, tagName: string, attributes: Record<string, unknown> = {}, childNodes: unknown[] = []) => ({ type: 2, id, tagName, attributes, childNodes });
const text = (id: number, textContent: string) => ({ type: 3, id, textContent });

/** A full snapshot whose every network-capable construct points at the attacker. */
const forgedSnapshot = (at: number, tag: string): RrwebEvent =>
  ({
    type: 2,
    timestamp: T + at,
    data: {
      initialOffset: { left: 0, top: 0 },
      node: {
        type: 0,
        id: 1,
        childNodes: [
          el(3, "html", {}, [
            el(4, "head", {}, [
              el(5, "link", { rel: "stylesheet", href: `${ATTACKER}/${tag}-theme.css` }),
              el(6, "style", {}, [text(7, `@font-face{font-family:x;src:url(${ATTACKER}/${tag}.woff2)} body{background:url(${ATTACKER}/${tag}-bg.png)}`)]),
            ]),
            el(8, "body", {}, [el(9, "img", { src: `${ATTACKER}/${tag}-pixel.gif` }), el(10, "div", { style: `background-image:url(${ATTACKER}/${tag}-inline.png)` }), el(11, "img", { src: "data:image/gif;base64,R0lGODlhAQABAAAAACw=" })]),
          ]),
        ],
      },
    },
  }) as unknown as RrwebEvent;

const meta = (at: number): RrwebEvent => ({ type: 4, timestamp: T + at, data: { href: "https://shop.example/", width: 1280, height: 720 } });
const step = (at: number): RrwebEvent => ({ type: 3, timestamp: T + at, data: { source: 1, positions: [] } });
const page = (events: RrwebEvent[]): ReplayPage => ({ events, unreadable: 0, nextPageToken: "", resumeToken: "r" });

function render(events: RrwebEvent[]) {
  const pages = [page(events)];
  const getSessionReplay = vi.fn(async (_req: ReplayRequest): Promise<ReplayPage> => pages.shift() ?? { events: [], unreadable: 0, nextPageToken: "", resumeToken: "" });
  const loadReplayer = async () => FakeReplayer as unknown as ReplayerCtor;
  renderWithApi({ ...createFixtureApi(), getSessionReplay }, <ReplayPlayer scope={SCOPE} session={SESSION} live={false} loadReplayer={loadReplayer} />);
}

const fetchSpy = vi.fn();
beforeEach(() => {
  FakeReplayer.reset();
  fetchSpy.mockReset();
  vi.stubGlobal("fetch", fetchSpy);
});
afterEach(() => vi.unstubAllGlobals());

describe("replay confinement", () => {
  it("pins the sandbox without scripts and puts the no-network policy in the replay document", async () => {
    render([meta(0), forgedSnapshot(10, "a"), step(1000)]);
    await waitFor(() => expect(screen.getByTestId("replay-player")).toHaveAttribute("data-state", "ready"));
    const { iframe } = FakeReplayer.last();

    expect(iframe.getAttribute("sandbox")).toBe("allow-same-origin");
    expect(iframe.getAttribute("sandbox")).not.toContain("allow-scripts");
    const csp = iframe.contentDocument!.querySelectorAll('meta[http-equiv="Content-Security-Policy"]');
    expect(csp).toHaveLength(1);
    expect(csp[0].getAttribute("content")).toBe(REPLAY_CSP);
    expect(csp[0].closest("head")).not.toBeNull();
    for (const directive of ["default-src 'none'", "img-src data: blob:", "style-src 'unsafe-inline'", "font-src data:"]) {
      expect(REPLAY_CSP).toContain(directive);
    }
    expect(REPLAY_CSP).not.toMatch(/https?:|\*|script-src/);
  });

  it("requests nothing from the hosts a forged recording names, and still allows inline images", async () => {
    render([meta(0), forgedSnapshot(10, "a"), step(1000)]);
    await waitFor(() => expect(FakeReplayer.last().blocked.length).toBeGreaterThan(0));
    const replayer = FakeReplayer.last();

    expect(replayer.requested).toEqual([]);
    expect(replayer.blocked.sort()).toEqual(
      [`${ATTACKER}/a-theme.css`, `${ATTACKER}/a.woff2`, `${ATTACKER}/a-bg.png`, `${ATTACKER}/a-pixel.gif`, `${ATTACKER}/a-inline.png`].sort()
    );
    expect(fetchSpy).not.toHaveBeenCalled();
  });

  it("stays confined when a later full snapshot replaces the tree", async () => {
    render([meta(0), forgedSnapshot(10, "a"), step(1000), forgedSnapshot(2000, "b"), step(3000)]);
    await waitFor(() => expect(FakeReplayer.last().blocked.length).toBeGreaterThanOrEqual(10));
    const replayer = FakeReplayer.last();

    expect(replayer.requested).toEqual([]);
    expect(replayer.blocked.filter((u) => u.includes("/b"))).toHaveLength(5);
    // The policy is still in the one document both snapshots were built into.
    expect(replayer.iframe.contentDocument!.querySelectorAll('meta[http-equiv="Content-Security-Policy"]')).toHaveLength(1);
  });

  it("re-applies the policy if a rebuild drops it", async () => {
    render([meta(0), forgedSnapshot(10, "a"), step(1000)]);
    await waitFor(() => expect(screen.getByTestId("replay-player")).toHaveAttribute("data-state", "ready"));
    const replayer = FakeReplayer.last();
    replayer.iframe.contentDocument!.querySelector('meta[http-equiv="Content-Security-Policy"]')!.remove();

    replayer.rebuildFullSnapshots();

    expect(replayer.iframe.contentDocument!.querySelectorAll('meta[http-equiv="Content-Security-Policy"]')).toHaveLength(1);
  });

  it("says that external assets are not loaded", async () => {
    render([meta(0), forgedSnapshot(10, "a"), step(1000)]);
    await waitFor(() => expect(screen.getByTestId("replay-player")).toHaveAttribute("data-state", "ready"));
    expect(screen.getByTestId("replay-external-note")).toHaveTextContent("External images, stylesheets and fonts are not loaded.");
  });

  // The harness must be able to see a leak, or the tests above prove nothing.
  it("negative control: without the policy the same recording does contact the attacker", async () => {
    const root = document.createElement("div");
    document.body.appendChild(root);
    const unconfined = new FakeReplayer([meta(0), forgedSnapshot(10, "a"), step(1000)], { root });
    await waitFor(() => expect(unconfined.requested.length).toBeGreaterThan(0));
    expect(unconfined.requested).toContain(`${ATTACKER}/a-pixel.gif`);
    expect(unconfined.requested).toHaveLength(5);
    root.remove();
  });
});

describe("confineReplayDocument", () => {
  it("is idempotent", () => {
    const doc = document.implementation.createHTMLDocument("");
    confineReplayDocument(doc);
    confineReplayDocument(doc);
    expect(doc.querySelectorAll("meta[http-equiv]")).toHaveLength(1);
  });

  it("creates a head when the document has none, because a CSP meta only counts inside <head>", () => {
    const doc = document.implementation.createHTMLDocument("");
    doc.head.remove();
    expect(doc.head).toBeNull();
    confineReplayDocument(doc);
    expect(doc.head?.querySelector('meta[http-equiv="Content-Security-Policy"]')).not.toBeNull();
  });

  it("goes first in the head, ahead of anything recorded", () => {
    const doc = document.implementation.createHTMLDocument("");
    doc.head.appendChild(doc.createElement("link"));
    confineReplayDocument(doc);
    expect(doc.head.firstElementChild?.tagName).toBe("META");
  });

  it("does nothing for a destroyed replayer", () => {
    expect(() => confineReplayDocument(null)).not.toThrow();
    expect(() => confineReplayDocument(undefined)).not.toThrow();
  });
});
