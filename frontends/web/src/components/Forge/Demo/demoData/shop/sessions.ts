// Copyright (c) 2025 Reliant Labs

/**
 * shop-web's real-user sessions: shop's customers browsing, buying and
 * checking on orders, every one on the release its environment ran then
 * (demoLedger). The only errors are the incident's: since v1.8.3 reached
 * prod, a few checkouts failed on the payment timeout (the failed Checkout
 * request links to its trace). One session is still being recorded.
 */

import { Code, ConnectError } from "@connectrpc/connect";

import type { ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import type { FrontendReplayList } from "@/services/controlPlane/observability/frontendReplayApi";
import type { ReplayPage, RrwebEvent, SessionSummary, TimelineEntry } from "@/services/controlPlane/observability/rumTypes";

import { failedIncidentRequests } from "../busyApp";
import { DEMO_CUSTOMERS, DEMO_FRONTEND, DEMO_INCIDENT, DEMO_PRODUCTS, demoVersionAt, incidentStartMs, MINUTE } from "../shopStory";
import { DEMO_CLIENTS } from "./clients";
import type { SessionKeys } from "./shopApi";

const SEC = 1000;
const PATHS = ["/", "/products/linen-tote-bag", "/cart", "/checkout", "/orders", "/products/hand-coffee-grinder", "/account", "/search", "/products/gooseneck-kettle", "/checkout"];
export const DEMO_LIVE_SESSION = "sess-live-7f3c";

interface DemoSession extends SessionSummary {
  /** The failed Checkout's trace, when the session hit the incident. */
  failedTraceId: string;
}

function sessionsOf(now: number): DemoSession[] {
  const incidentAt = incidentStartMs(now);
  const checkoutsFailed = failedIncidentRequests(now, incidentAt, now, 50).filter((r) => r.method === "Checkout");
  let failedUsed = 0;
  const make = (n: number, env: "env-prod" | "env-staging", startedMs: number): DemoSession => {
    const customer = DEMO_CUSTOMERS[(n * 7) % DEMO_CUSTOMERS.length];
    const entryPath = PATHS[n % PATHS.length];
    const version = demoVersionAt(env, startedMs, now) ?? "v1.8.2";
    const failing = env === "env-prod" && entryPath === "/checkout" && startedMs >= incidentAt && failedUsed < checkoutsFailed.length;
    const failedTraceId = failing ? checkoutsFailed[failedUsed++].traceId : "";
    return {
      sessionId: `sess-${(n * 7919 + 4099).toString(16).padStart(6, "0")}`,
      service: DEMO_FRONTEND,
      serviceVersion: version,
      envId: env,
      envName: env === "env-prod" ? "prod" : "staging",
      userId: n % 4 === 1 ? "" : customer.id,
      entryPath,
      startedMs,
      endedMs: startedMs + (1 + (n % 8)) * MINUTE + 17 * SEC,
      pageViews: 1 + (n % 6),
      interactions: 3 + ((n * 5) % 27),
      errors: failing ? 1 : 0,
      // Every seventh has no replay, but never the incident's failed checkout (n = 3): it is the replay worth watching.
      hasReplay: n % 7 !== 5,
      country: customer.country,
      region: "",
      city: customer.city,
      clientAddress: "",
      client: DEMO_CLIENTS[n % DEMO_CLIENTS.length],
      release: version,
      failedTraceId,
    };
  };
  const prod = Array.from({ length: 30 }, (_, i) => make(i + 1, "env-prod", now - (3 + i * 9) * MINUTE));
  const staging = Array.from({ length: 4 }, (_, i) => make(40 + i, "env-staging", now - (25 + i * 70) * MINUTE));
  const live: DemoSession = { ...make(0, "env-prod", now - 40 * SEC), sessionId: DEMO_LIVE_SESSION, entryPath: "/products/hand-coffee-grinder", endedMs: now - 5 * SEC, pageViews: 1, interactions: 2, errors: 0, hasReplay: true, failedTraceId: "" };
  return [live, ...prod, ...staging].sort((a, b) => b.startedMs - a.startedMs);
}

const strip = ({ failedTraceId: _f, ...s }: DemoSession): SessionSummary => s;

function entry(over: Partial<TimelineEntry> & Pick<TimelineEntry, "timeMs" | "kind">): TimelineEntry {
  return { name: "", path: "", method: "", status: 0, selector: "", element: "", errorType: "", message: "", fingerprint: "", issueId: "", traceId: "", spanId: "", durationMs: 0, props: {}, service: DEMO_FRONTEND, ...over };
}

function timelineOf(session: DemoSession): TimelineEntry[] {
  const s = session.startedMs;
  const entries = [
    entry({ timeMs: s + 10, kind: "navigation", name: "documentLoad", path: session.entryPath, durationMs: 610 }),
    entry({ timeMs: s + 4 * SEC, kind: "click", name: "click", path: session.entryPath, element: "INPUT", selector: "/html/body/main/form/input[1]" }),
    entry({ timeMs: s + 8 * SEC, kind: "click", name: "click", path: session.entryPath, element: "BUTTON", selector: "/html/body/main/form/button" }),
  ];
  if (session.failedTraceId) {
    entries.push(
      entry({
        timeMs: s + 8 * SEC + 300,
        kind: "request_failed",
        name: "POST",
        method: "POST",
        path: "/checkout.v1.CheckoutService/Checkout",
        status: 504,
        message: DEMO_INCIDENT.error,
        issueId: DEMO_INCIDENT.issueId,
        traceId: session.failedTraceId,
        durationMs: DEMO_INCIDENT.deadlineMs,
      }),
      entry({ timeMs: s + 14 * SEC, kind: "event", name: "checkout_retry", props: { attempt: "2", method: "card" } }),
    );
  } else if (session.entryPath === "/checkout") {
    entries.push(entry({ timeMs: s + 9 * SEC, kind: "event", name: "purchase", props: { items: "1", method: "card" } }));
  }
  entries.push(entry({ timeMs: s + 20 * SEC, kind: "navigation", name: "routeChange", path: "/orders", props: { from: session.entryPath } }));
  return entries.sort((a, b) => a.timeMs - b.timeMs);
}

// ── A masked rrweb recording ────────────────────────────────────────────────

const NODE = { Document: 0, DocumentType: 1, Element: 2, Text: 3 } as const;
const EVENT = { FullSnapshot: 2, Incremental: 3, Meta: 4 } as const;
const SOURCE = { Mutation: 0, MouseMove: 1, MouseInteraction: 2, Input: 5 } as const;
const CLICK = 2;
const masked = (text: string) => text.replace(/\S/g, "*");

interface SerializedNode {
  type: number;
  id: number;
  [key: string]: unknown;
}

/** The checkout page, masked the way forge-web-runtime records it. */
function recordingOf(session: DemoSession, durationMs: number): RrwebEvent[] {
  let nextId = 0;
  const id = () => ++nextId;
  const text = (value: string, extra: Record<string, unknown> = {}): SerializedNode => ({ type: NODE.Text, textContent: value, id: id(), ...extra });
  const el = (tagName: string, attributes: Record<string, string>, childNodes: SerializedNode[] = []): SerializedNode => ({ type: NODE.Element, tagName, attributes, childNodes, id: id() });
  const css = [
    "body{margin:0;font:15px system-ui,sans-serif;background:#f6f5f2;color:#1f2328}",
    "header{display:flex;gap:24px;align-items:center;padding:16px 32px;background:#1f2328;color:#fff}",
    "main{max-width:640px;margin:40px auto;padding:32px;background:#fff;border-radius:12px;box-shadow:0 1px 3px rgba(0,0,0,.1)}",
    "h1{margin:0 0 16px;font-size:24px}form{display:grid;gap:12px}",
    "input{padding:10px;border:1px solid #c9ced8;border-radius:8px}",
    "button{padding:12px;border:0;border-radius:8px;background:#2f6f4f;color:#fff;font-weight:600}",
    ".banner{margin-top:16px;padding:12px;border-radius:8px;background:#fde8e8;color:#9b1c1c}",
  ].join("");
  const product = DEMO_PRODUCTS[5];
  const heading = text(masked("Checkout"));
  const input = el("input", { type: "text", placeholder: masked("Card number"), value: masked("4242 4242 4242 4242") });
  const button = el("button", { type: "submit" }, [text(masked("Pay now"))]);
  const main = el("main", {}, [el("h1", {}, [heading]), el("p", {}, [text(masked(`${product.name}, 1 item: $${(product.price / 100).toFixed(2)}`))]), el("form", {}, [input, button])]);
  const header = el("header", {}, [el("strong", {}, [text(masked("Shop"))]), el("span", {}, [text(masked("Orders"))]), el("span", {}, [text(masked("Account"))])]);
  const html = el("html", { lang: "en" }, [el("head", {}, [el("style", {}, [text(css, { isStyle: true })])]), el("body", {}, [header, main])]);
  const body = (html.childNodes as SerializedNode[])[1];
  const doc: SerializedNode = { type: NODE.Document, childNodes: [{ type: NODE.DocumentType, name: "html", publicId: "", systemId: "", id: id() }, html], id: id() };
  const t0 = session.startedMs;
  const events: RrwebEvent[] = [
    { type: EVENT.Meta, timestamp: t0, data: { href: `https://shop.example.com${session.entryPath}`, width: 1280, height: 720 } },
    { type: EVENT.FullSnapshot, timestamp: t0 + 10, data: { node: doc, initialOffset: { left: 0, top: 0 } } },
  ];
  const move = (at: number, x: number, y: number) => events.push({ type: EVENT.Incremental, timestamp: t0 + at, data: { source: SOURCE.MouseMove, positions: [{ x, y, id: body.id, timeOffset: 0 }] } });
  for (let step = 0; step < 8; step++) move(1000 + step * 400, 300 + step * 40, 200 + step * 30);
  events.push({ type: EVENT.Incremental, timestamp: t0 + 4 * SEC, data: { source: SOURCE.MouseInteraction, type: CLICK, id: input.id, x: 640, y: 330 } });
  events.push({ type: EVENT.Incremental, timestamp: t0 + 5 * SEC, data: { source: SOURCE.Input, id: input.id, text: masked("4242 4242 4242 4242"), isChecked: false } });
  for (let step = 0; step < 6; step++) move(5500 + step * 400, 640, 340 + step * 12);
  events.push({ type: EVENT.Incremental, timestamp: t0 + 8 * SEC, data: { source: SOURCE.MouseInteraction, type: CLICK, id: button.id, x: 640, y: 410 } });
  if (session.failedTraceId) {
    const banner = el("div", { class: "banner" }, [text(masked("We couldn't take the payment. Try again."))]);
    events.push({ type: EVENT.Incremental, timestamp: t0 + 11 * SEC + 100, data: { source: SOURCE.Mutation, texts: [], attributes: [], removes: [], adds: [{ parentId: main.id, nextId: null, node: banner }] } });
  }
  events.push({ type: EVENT.Incremental, timestamp: t0 + 20 * SEC, data: { source: SOURCE.Mutation, texts: [{ id: heading.id, value: masked("Your orders") }], attributes: [], removes: [], adds: [] } });
  for (let at = 50 * SEC; at <= durationMs; at += 2500) move(at, 400 + ((at / 100) % 300), 300);
  return events.filter((e) => e.timestamp - t0 <= durationMs);
}

const PER_PAGE = 8;

export function shopSessions(now: number): Pick<ObservabilityApi, SessionKeys> {
  const all = sessionsOf(now);
  const find = (sessionId: string): DemoSession => {
    const found = all.find((s) => s.sessionId === sessionId);
    if (!found) throw new ConnectError(`session ${sessionId} not found`, Code.NotFound);
    return found;
  };
  return {
    listSessions: async (req) => {
      let rows = all.filter((s) => s.startedMs >= req.window.startMs && s.startedMs < req.window.endMs);
      if (req.services?.length) rows = rows.filter((s) => req.services!.includes(s.service));
      if (req.envIds.length) rows = rows.filter((s) => req.envIds.includes(s.envId));
      if (req.hasError) rows = rows.filter((s) => s.errors > 0);
      if (req.hasReplay) rows = rows.filter((s) => s.hasReplay);
      if (req.userId) rows = rows.filter((s) => s.userId === req.userId);
      if (req.browser) rows = rows.filter((s) => s.client.browserName === req.browser);
      if (req.os) rows = rows.filter((s) => s.client.osName === req.os);
      if (req.frontendVersion) rows = rows.filter((s) => s.serviceVersion === req.frontendVersion);
      if (req.pathContains) rows = rows.filter((s) => s.entryPath.toLowerCase().includes(req.pathContains.toLowerCase()));
      const offset = req.pageToken ? Number(req.pageToken.replace(/^s:/, "")) || 0 : 0;
      const size = req.pageSize || 25;
      return { sessions: rows.slice(offset, offset + size).map(strip), nextPageToken: offset + size < rows.length ? `s:${offset + size}` : "" };
    },
    getSession: async (req) => strip(find(req.sessionId)),
    getSessionTimeline: async (req) => ({ entries: timelineOf(find(req.sessionId)), nextPageToken: "" }),
    // shop-web records replay in both environments its sessions come from.
    listFrontendReplay: async (req): Promise<FrontendReplayList> => {
      const envs = ([["env-staging", "staging"], ["env-prod", "prod"]] as const).filter(([id]) => !req.envIds.length || req.envIds.includes(id));
      return {
        frontends: envs.map(([envId, envName]) => ({ envId, envName, frontend: DEMO_FRONTEND, state: "on", source: "live", sampleRate: 1 })),
        unavailableEnvIds: [],
      };
    },
    getSessionReplay: async (req): Promise<ReplayPage> => {
      const session = find(req.sessionId);
      const live = session.sessionId === DEMO_LIVE_SESSION;
      const recording = session.hasReplay ? recordingOf(session, live ? 10 * MINUTE : session.endedMs - session.startedMs) : [];
      const available = live ? recording.filter((e) => e.timestamp <= Date.now()) : recording;
      const offset = req.pageToken ? Number(req.pageToken.replace(/^p:/, "")) || 0 : 0;
      const events = available.slice(offset, offset + PER_PAGE);
      const next = offset + events.length;
      return { events, unreadable: 0, nextPageToken: next < available.length ? `p:${next}` : "", resumeToken: events.length ? `p:${next}` : "" };
    },
  };
}
