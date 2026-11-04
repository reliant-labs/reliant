// Copyright (c) 2025 Reliant Labs
//
// The browser half of the terminal websocket's trace. The server opens a
// terminal.session span per connection (internal/handlers/terminal/
// terminal_ws_telemetry.go); this opens the client span it continues.
//
// A browser WebSocket cannot set request headers, so the W3C trace context
// the Connect transport sends as headers (src/api/transport.ts) travels on
// the upgrade URL instead: `traceparent` and `tracestate` query parameters,
// which the server reads when the headers are absent.
//
// When no tracer provider is registered (VITE_OTEL_ENDPOINT unset) the span is
// a no-op and nothing is added to the URL.

import {
  context,
  propagation,
  trace,
  SpanKind,
  type Span,
} from "@opentelemetry/api";

/** One terminal connection's client span. `end` is idempotent. */
export interface TerminalSessionSpan {
  /** The server confirmed the session ("init"). */
  established(): void;
  /**
   * The connection ended: `closeCode` is the WebSocket close code when the
   * socket closed, absent when the terminal retired it (replaced or
   * unmounted); `reason` says which.
   */
  end(closeCode?: number, reason?: string): void;
}

export interface TerminalSessionSpanOptions {
  /** This terminal already had a connection that this one replaces. */
  reconnect: boolean;
  projectId?: string;
  worktreeId?: string;
}

/**
 * Starts the connection's client span and adds its trace context, and the
 * reconnect flag, to `params` (the upgrade URL's query). Never the working
 * directory or any terminal content: ids and close codes only.
 */
export function startTerminalSessionSpan(
  params: URLSearchParams,
  opts: TerminalSessionSpanOptions,
): TerminalSessionSpan {
  const span: Span = trace.getTracer("reliant-frontend").startSpan("terminal.session", {
    kind: SpanKind.CLIENT,
    attributes: {
      "terminal.reconnect": opts.reconnect,
      ...(opts.projectId ? { "terminal.project_id": opts.projectId } : {}),
      ...(opts.worktreeId ? { "terminal.worktree_id": opts.worktreeId } : {}),
    },
  });

  const carrier: Record<string, string> = {};
  propagation.inject(trace.setSpan(context.active(), span), carrier);
  for (const key of ["traceparent", "tracestate"]) {
    if (carrier[key]) params.set(key, carrier[key]);
  }
  if (opts.reconnect) params.set("reconnect", "1");

  let ended = false;
  return {
    established() {
      if (!ended) span.addEvent("terminal.established");
    },
    end(closeCode, reason) {
      if (ended) return;
      ended = true;
      if (closeCode !== undefined) span.setAttribute("terminal.close_code", closeCode);
      if (reason) span.setAttribute("terminal.end_reason", reason);
      span.end();
    },
  };
}

/**
 * The upgrade URL as it may be logged: `base` with every query parameter's
 * value replaced by `***`. The query carries the auth token, the trace context
 * and the working directory, none of which belongs in a log line; the
 * parameter names stay, so a log still says what the connection asked for.
 */
export function redactedTerminalWSURL(base: string, params: URLSearchParams): string {
  const keys = [...new Set(params.keys())];
  if (keys.length === 0) return base;
  return `${base}?${keys.map((key) => `${encodeURIComponent(key)}=***`).join("&")}`;
}
