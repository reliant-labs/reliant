// Copyright (c) 2025 Reliant Labs

/**
 * An in-memory RumService for the fixture app (fixtureApi.ts): a page of real
 * users' sessions, each with a timeline, and a small but genuine rrweb
 * recording — masked the way forge-web-runtime records (every character of
 * text and every input value is `*`), so the player renders exactly what a
 * reader of a real replay sees.
 *
 * The story matches the issues fixture: `sess-1a8c2f1d91e` is the checkout
 * session whose Pay click failed (a 500 from Checkout/Pay) and raised the
 * `iss-checkout-total` TypeError. `sess-live01` is still being recorded: its
 * replay grows as the clock moves, which is how the player's live polling is
 * looked at. Paging is real on both reads — sessions 25 at a time, replay 8
 * events a page — so Older/Newer and buffering ahead are exercised too.
 */

import { Code, ConnectError } from "@connectrpc/connect";

import type { ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import type { ReplayPage, RrwebEvent, SessionSummary, TimelineEntry } from "@/services/controlPlane/observability/rumTypes";

const SEC = 1000;
const MIN = 60 * SEC;
const TRACE = "4bf92f3577b34da6a3ce929d0e0e4736";

export const FIXTURE_STORY_SESSION = "sess-1a8c2f1d91e";
export const FIXTURE_LIVE_SESSION = "sess-live01";

export interface SessionsFixtureOptions {
  /** "empty": no sessions. "not-configured": a control plane from before sessions (Unimplemented). */
  sessions?: "ok" | "empty" | "not-configured";
  /**
   * What every recording looks like. "partial": it stops long before the
   * session did. "no-snapshot": its full snapshot was lost. "unreadable": two
   * events are not JSON. "none": recorded, but nothing left (aged out).
   */
  replay?: "ok" | "partial" | "no-snapshot" | "unreadable" | "none";
  /** Replay events per page. Default 8. */
  replayPageEvents?: number;
}

// ── Sessions ────────────────────────────────────────────────────────────────

const PATHS = ["/checkout/payment", "/", "/cart", "/products/espresso-grinder", "/orders", "/account", "/products/milk-jug", "/search"];
const USERS = ["u_8812", "", "u_1093", "u_5530", "", "u_2207", "u_8812", ""];

export function fixtureSessionSummaries(t0: number): SessionSummary[] {
  const base = (n: number, over: Partial<SessionSummary>): SessionSummary => {
    const startedMs = t0 - (6 + n * 23) * MIN;
    return {
      sessionId: `sess-${(n * 7919 + 4099).toString(16).padStart(6, "0")}`,
      service: n % 5 === 4 ? "admin-web" : "shop-web",
      serviceVersion: n < 6 ? "v1.8.3" : "v1.8.2",
      envId: n % 3 === 2 ? "env-staging" : "env-prod",
      envName: n % 3 === 2 ? "staging" : "prod",
      userId: USERS[n % USERS.length],
      entryPath: PATHS[n % PATHS.length],
      startedMs,
      endedMs: startedMs + (1 + (n % 9)) * MIN + 17 * SEC,
      pageViews: 1 + (n % 7),
      interactions: 3 + ((n * 5) % 31),
      errors: n % 4 === 0 ? 1 + (n % 3) : 0,
      hasReplay: n % 3 !== 1,
      ...over,
    };
  };
  const story = base(0, {
    sessionId: FIXTURE_STORY_SESSION,
    userId: "u_8812",
    entryPath: "/checkout/payment",
    startedMs: t0 - 6 * MIN,
    endedMs: t0 - 6 * MIN + 58 * SEC,
    pageViews: 2,
    interactions: 6,
    errors: 1,
    hasReplay: true,
  });
  const live = base(0, {
    sessionId: FIXTURE_LIVE_SESSION,
    userId: "u_3141",
    entryPath: "/products/espresso-grinder",
    startedMs: t0 - 40 * SEC,
    endedMs: t0 - 5 * SEC,
    pageViews: 1,
    interactions: 2,
    errors: 0,
    hasReplay: true,
  });
  return [live, story, ...Array.from({ length: 30 }, (_, i) => base(i + 1, {}))];
}

/** Any other `sess-…` id (an issue event's): a plausible session, so a link from Errors opens. */
function synthesized(sessionId: string, t0: number): SessionSummary {
  const startedMs = t0 - 3 * 60 * MIN;
  return {
    sessionId,
    service: "shop-web",
    serviceVersion: "v1.8.3",
    envId: "env-prod",
    envName: "prod",
    userId: "",
    entryPath: "/checkout",
    startedMs,
    endedMs: startedMs + 58 * SEC,
    pageViews: 2,
    interactions: 5,
    errors: 1,
    hasReplay: true,
  };
}

// ── Timeline ────────────────────────────────────────────────────────────────

function entry(over: Partial<TimelineEntry> & Pick<TimelineEntry, "timeMs" | "kind">): TimelineEntry {
  return {
    name: "",
    path: "",
    method: "",
    status: 0,
    selector: "",
    element: "",
    errorType: "",
    message: "",
    fingerprint: "",
    issueId: "",
    traceId: "",
    spanId: "",
    durationMs: 0,
    props: {},
    service: "shop-web",
    ...over,
  };
}

export function fixtureTimeline(session: SessionSummary): TimelineEntry[] {
  const s = session.startedMs;
  const entries = [
    entry({ timeMs: s + 10, kind: "navigation", name: "documentLoad", path: session.entryPath || "/checkout/payment", durationMs: 840 }),
    entry({ timeMs: s + 4 * SEC, kind: "click", name: "click", path: session.entryPath, element: "INPUT", selector: "/html/body/main/form/input[1]" }),
    entry({ timeMs: s + 8 * SEC, kind: "click", name: "click", path: session.entryPath, element: "BUTTON", selector: "/html/body/main/form/button" }),
  ];
  if (session.errors > 0) {
    entries.push(
      entry({
        timeMs: s + 8 * SEC + 300,
        kind: "request_failed",
        name: "POST",
        method: "POST",
        path: "/checkout.v1.CheckoutService/Pay",
        status: 500,
        message: "Internal Server Error",
        traceId: TRACE,
        spanId: "00f067aa0ba902b7",
        durationMs: 412,
      }),
      entry({
        timeMs: s + 8 * SEC + 450,
        kind: "error",
        name: "onerror",
        path: session.entryPath,
        errorType: "TypeError",
        message: "Cannot read properties of undefined (reading 'total')",
        fingerprint: "fp-checkout-total",
        issueId: session.sessionId === FIXTURE_STORY_SESSION ? "iss-checkout-total" : "",
        traceId: TRACE,
        spanId: "b7ad6b7169203331",
      }),
      entry({ timeMs: s + 14 * SEC, kind: "event", name: "checkout_retry", props: { attempt: "2", method: "card" } })
    );
  }
  entries.push(entry({ timeMs: s + 20 * SEC, kind: "navigation", name: "routeChange", path: "/cart", props: { from: session.entryPath } }));
  return entries.sort((a, b) => a.timeMs - b.timeMs);
}

// ── A masked rrweb recording ────────────────────────────────────────────────

/** rrweb's own numbering (NodeType, EventType, IncrementalSource, MouseInteractions). */
const NODE = { Document: 0, DocumentType: 1, Element: 2, Text: 3 } as const;
const EVENT = { FullSnapshot: 2, Incremental: 3, Meta: 4 } as const;
const SOURCE = { Mutation: 0, MouseMove: 1, MouseInteraction: 2, Input: 5 } as const;
const CLICK = 2;

/** What maskAllText leaves of a string: its shape. */
function masked(text: string): string {
  return text.replace(/\S/g, "*");
}

interface SerializedNode {
  type: number;
  id: number;
  [key: string]: unknown;
}

/** A recording of the checkout page, `durationMs` long (live: it keeps going past now). */
export function fixtureRecording(session: SessionSummary, durationMs: number, opts: { snapshot?: boolean } = {}): RrwebEvent[] {
  let nextId = 0;
  const id = () => ++nextId;
  const text = (value: string, extra: Record<string, unknown> = {}): SerializedNode => ({ type: NODE.Text, textContent: value, id: id(), ...extra });
  const el = (tagName: string, attributes: Record<string, string>, childNodes: SerializedNode[] = []): SerializedNode => ({
    type: NODE.Element,
    tagName,
    attributes,
    childNodes,
    id: id(),
  });

  // Recorded page CSS: the customer's own, inlined by rrweb at record time.
  const css = [
    "body{margin:0;font:15px system-ui,sans-serif;background:#f6f7f9;color:#1d2433}",
    "header{display:flex;gap:24px;align-items:center;padding:16px 32px;background:#1d2433;color:#fff}",
    "main{max-width:640px;margin:40px auto;padding:32px;background:#fff;border-radius:12px;box-shadow:0 1px 3px rgba(0,0,0,.1)}",
    "h1{margin:0 0 16px;font-size:24px}form{display:grid;gap:12px}",
    "input{padding:10px;border:1px solid #c9ced8;border-radius:8px}",
    "button{padding:12px;border:0;border-radius:8px;background:#2f6fed;color:#fff;font-weight:600}",
    ".banner{margin-top:16px;padding:12px;border-radius:8px;background:#fde8e8;color:#9b1c1c}",
  ].join("");
  const style = el("style", {}, [text(css, { isStyle: true })]);
  const head = el("head", {}, [style]);
  const heading = text(masked("Checkout"));
  const input = el("input", { type: "text", placeholder: masked("Card number"), value: masked("4242 4242 4242 4242") });
  const button = el("button", { type: "submit" }, [text(masked("Pay now"))]);
  const main = el("main", {}, [
    el("h1", {}, [heading]),
    el("p", {}, [text(masked("Espresso grinder, 1 item — $189.00"))]),
    el("form", {}, [input, button]),
  ]);
  const header = el("header", {}, [el("strong", {}, [text(masked("Shop"))]), el("span", {}, [text(masked("Orders"))]), el("span", {}, [text(masked("Account"))])]);
  const body = el("body", {}, [header, main]);
  const html = el("html", { lang: "en" }, [head, body]);
  const doc: SerializedNode = { type: NODE.Document, childNodes: [{ type: NODE.DocumentType, name: "html", publicId: "", systemId: "", id: id() }, html], id: id() };

  const t0 = session.startedMs;
  const events: RrwebEvent[] = [{ type: EVENT.Meta, timestamp: t0, data: { href: `https://shop.example${session.entryPath || "/"}`, width: 1280, height: 720 } }];
  if (opts.snapshot !== false) events.push({ type: EVENT.FullSnapshot, timestamp: t0 + 10, data: { node: doc, initialOffset: { left: 0, top: 0 } } });

  const move = (at: number, x: number, y: number, target: number) =>
    events.push({ type: EVENT.Incremental, timestamp: t0 + at, data: { source: SOURCE.MouseMove, positions: [{ x, y, id: target, timeOffset: 0 }] } });
  for (let step = 0; step < 8; step++) move(1000 + step * 400, 300 + step * 40, 200 + step * 30, body.id);
  events.push({ type: EVENT.Incremental, timestamp: t0 + 4 * SEC, data: { source: SOURCE.MouseInteraction, type: CLICK, id: input.id, x: 640, y: 330 } });
  events.push({ type: EVENT.Incremental, timestamp: t0 + 5 * SEC, data: { source: SOURCE.Input, id: input.id, text: masked("4242 4242 4242 4242"), isChecked: false } });
  for (let step = 0; step < 6; step++) move(5500 + step * 400, 640, 340 + step * 12, body.id);
  events.push({ type: EVENT.Incremental, timestamp: t0 + 8 * SEC, data: { source: SOURCE.MouseInteraction, type: CLICK, id: button.id, x: 640, y: 410 } });
  if (session.errors > 0) {
    const banner = el("div", { class: "banner" }, [text(masked("Something went wrong. Try again."))]);
    events.push({
      type: EVENT.Incremental,
      timestamp: t0 + 8 * SEC + 500,
      data: { source: SOURCE.Mutation, texts: [], attributes: [], removes: [], adds: [{ parentId: main.id, nextId: null, node: banner }] },
    });
  }
  events.push({
    type: EVENT.Incremental,
    timestamp: t0 + 20 * SEC,
    data: { source: SOURCE.Mutation, texts: [{ id: heading.id, value: masked("Your cart") }], attributes: [], removes: [], adds: [] },
  });
  // A long idle stretch, then a little more: what "skip inactivity" skips.
  for (let at = 50 * SEC; at <= durationMs; at += 2500) move(at, 400 + ((at / 100) % 300), 300, body.id);
  return events.filter((e) => e.timestamp - t0 <= durationMs);
}

// ── The API ─────────────────────────────────────────────────────────────────

type SessionsApi = Pick<ObservabilityApi, "listSessions" | "getSession" | "getSessionTimeline" | "getSessionReplay">;

export function createSessionsFixture(
  options: SessionsFixtureOptions,
  call: <T>(rpc: string, request: unknown, answer: () => T) => Promise<T>,
  now: () => number = Date.now
): SessionsApi {
  const t0 = now();
  const mode = options.sessions ?? "ok";
  const summaries = mode === "empty" ? [] : fixtureSessionSummaries(t0);
  const perPage = options.replayPageEvents ?? 8;
  const notConfigured = () => {
    if (mode === "not-configured") throw new ConnectError("procedure controlplane.v1.RumService not implemented", Code.Unimplemented);
  };
  const find = (sessionId: string): SessionSummary => {
    const found = summaries.find((s) => s.sessionId === sessionId);
    if (found) return found;
    if (mode !== "empty" && sessionId.startsWith("sess-")) return synthesized(sessionId, t0);
    throw new ConnectError(`session ${sessionId} not found`, Code.NotFound);
  };
  const recordings = new Map<string, RrwebEvent[]>();
  const recordingOf = (session: SessionSummary): RrwebEvent[] => {
    let events = recordings.get(session.sessionId);
    if (!events) {
      const live = session.sessionId === FIXTURE_LIVE_SESSION;
      const replay = live ? "ok" : (options.replay ?? "ok");
      const duration = live ? 10 * MIN : replay === "partial" ? 21 * SEC : Math.max(0, session.endedMs - session.startedMs);
      events = replay === "none" || !session.hasReplay ? [] : fixtureRecording(session, duration, { snapshot: replay !== "no-snapshot" });
      recordings.set(session.sessionId, events);
    }
    // A live recording has only got as far as now.
    return session.sessionId === FIXTURE_LIVE_SESSION ? events.filter((e) => e.timestamp <= now()) : events;
  };
  const partialSession = (session: SessionSummary) =>
    options.replay === "partial" && session.sessionId !== FIXTURE_LIVE_SESSION ? { ...session, endedMs: session.startedMs + 2 * MIN } : session;

  return {
    listSessions: (req) =>
      call("ListSessions", req, () => {
        notConfigured();
        let rows = summaries.filter((s) => s.startedMs >= req.window.startMs && s.startedMs < req.window.endMs);
        if (req.services?.length) rows = rows.filter((s) => req.services!.includes(s.service));
        if (req.envIds.length) rows = rows.filter((s) => req.envIds.includes(s.envId));
        if (req.hasError) rows = rows.filter((s) => s.errors > 0);
        if (req.hasReplay) rows = rows.filter((s) => s.hasReplay);
        if (req.userId) rows = rows.filter((s) => s.userId === req.userId);
        if (req.pathContains) rows = rows.filter((s) => s.entryPath.toLowerCase().includes(req.pathContains.toLowerCase()));
        const offset = req.pageToken ? Number(req.pageToken.replace(/^s:/, "")) || 0 : 0;
        const size = req.pageSize || 25;
        const page = rows.slice(offset, offset + size).map(partialSession);
        return { sessions: page, nextPageToken: offset + size < rows.length ? `s:${offset + size}` : "" };
      }),
    getSession: (req) =>
      call("GetSession", req, () => {
        notConfigured();
        return partialSession(find(req.sessionId));
      }),
    getSessionTimeline: (req) =>
      call("GetSessionTimeline", req, () => {
        notConfigured();
        return { entries: fixtureTimeline(find(req.sessionId)), nextPageToken: "" };
      }),
    getSessionReplay: (req) =>
      call("GetSessionReplay", req, (): ReplayPage => {
        notConfigured();
        const available = recordingOf(find(req.sessionId));
        const offset = req.pageToken ? Number(req.pageToken.replace(/^p:/, "")) || 0 : 0;
        const events = available.slice(offset, offset + perPage);
        const next = offset + events.length;
        return {
          events,
          unreadable: options.replay === "unreadable" && offset === 0 ? 2 : 0,
          nextPageToken: next < available.length ? `p:${next}` : "",
          resumeToken: events.length ? `p:${next}` : "",
        };
      }),
  };
}
