// Copyright (c) 2025 Reliant Labs

/**
 * What a trace is called: the operation it performed, not its root span's raw
 * name.
 *
 * Raw names are written for machines. A Connect server span is
 * `reliant.v1.ChatService/SendMessage`; a browser fetch is `HTTP POST` (the
 * HyperDX SDK's name for every POST, whatever it called), so a list of
 * browser-started traces read "HTTP POST" forty times. A title names the RPC
 * (`ChatService.SendMessage`), or the method and route of anything else
 * (`GET /v1/items/{id}`), and a trace a browser started is titled by the
 * server span it caused, with the browser it came from beside it.
 */

import type { Span } from "@/services/controlPlane/observability/types";

export interface TraceTitle {
  /** The service that did the work: the first server span's, else the root's. */
  service: string;
  /** `ChatService.SendMessage`, `GET /v1/items/{id}`, or the raw name when it says more. */
  operation: string;
  /** The browser service the trace started in, when it started in one and a server span is known. */
  from?: string;
  /** The root span's name as stored: what the root-operation filter matches. */
  raw: string;
}

// `[VERB ]/?<package>.<Service>/<Method>`: a Connect or gRPC procedure.
const RPC = /^(?:([A-Z]+) )?\/?((?:[A-Za-z_]\w*\.)+)?([A-Za-z_]\w*)\/([A-Za-z_]\w*)$/;
// A name that says only the HTTP method: HyperDX's `HTTP POST`, or a bare `POST`.
const GENERIC = /^(?:HTTP )?([A-Z]+)$/;
// A path segment that is a value rather than part of the route: a number, a
// UUID, a long hex string, a long token with a digit in it, or an email.
const ID_SEGMENT =
  /^(?:\d+|[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}|[0-9a-f]{16,}|(?=[\w-]*\d)[\w-]{20,}|.*@.*)$/i;

/**
 * The readable name of one span: `Service.Method` for an RPC, `<METHOD>
 * <route>` for an HTTP span named only by its method (the route from
 * `http.route`, else the request path with id-like segments collapsed), and
 * the name unchanged otherwise.
 */
export function operationName(name: string, attributes: Record<string, string> = {}): string {
  const trimmed = name.trim();
  const rpc = RPC.exec(trimmed);
  // The package is what makes it a procedure: `GET /v1/items` is a path.
  if (rpc?.[2]) return `${rpc[3]}.${rpc[4]}`;
  const generic = GENERIC.exec(trimmed);
  if (!generic && trimmed !== "") return name;

  const verb = (attributes["http.request.method"] || attributes["http.method"] || generic?.[1] || "").toUpperCase();
  const route = attributes["http.route"];
  const path = route || pathOf(attributes["url.path"] || attributes["http.target"] || attributes["url.full"] || attributes["http.url"] || "");
  if (!path) return name;
  const asRpc = RPC.exec(path);
  if (asRpc?.[2]) return `${asRpc[3]}.${asRpc[4]}`;
  const shown = route ? route : collapseIds(path);
  return verb ? `${verb} ${shown}` : shown;
}

/** The title of a whole trace. Null for a trace with no spans. */
export function traceTitle(spans: readonly Span[]): TraceTitle | null {
  const root = spans.find((span) => !span.parentSpanId) ?? spans[0];
  if (!root) return null;
  const own: TraceTitle = { service: root.service, operation: operationName(root.name, root.attributes), raw: root.name };
  if (!isBrowserSpan(root)) return own;
  const server = firstServerSpan(spans, root.spanId);
  if (!server) return own;
  return { service: server.service, operation: operationName(server.name, server.attributes), from: root.service, raw: root.name };
}

/** A span the browser SDK recorded (the gateway stamps the ingest class; the SDK its runtime). */
export function isBrowserSpan(span: Pick<Span, "resource">): boolean {
  return span.resource["reliant.ingest.class"] === "browser" || span.resource["process.runtime.name"] === "browser";
}

// The server span a browser request caused: a direct child of the root first
// (the call the browser made), else the earliest server span anywhere.
function firstServerSpan(spans: readonly Span[], rootId: string): Span | undefined {
  const servers = spans
    .filter((span) => span.kind === "server" && !isBrowserSpan(span))
    .sort((a, b) => a.startMs - b.startMs);
  return servers.find((span) => span.parentSpanId === rootId) ?? servers[0];
}

function pathOf(value: string): string {
  if (!value) return "";
  try {
    return new URL(value, "http://base.invalid").pathname;
  } catch {
    return "";
  }
}

function collapseIds(path: string): string {
  return path
    .split("/")
    .map((segment) => (segment && ID_SEGMENT.test(decode(segment)) ? "{id}" : segment))
    .join("/");
}

function decode(segment: string): string {
  try {
    return decodeURIComponent(segment);
  } catch {
    return segment;
  }
}
