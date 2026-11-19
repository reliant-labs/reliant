// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// One client span per RPC, as the browser saw it.
//
// The HyperDX SDK already records a span per fetch, but a fetch span is named
// by URL and knows nothing of Connect: a failed RPC is an HTTP 200 with an
// error body to it, and a server stream's fetch span ends when the headers
// arrive, not when the stream does. This stage records the RPC itself:
//
//   name        <rpc.service>/<rpc.method>   e.g. pkg.v1.ChatService/GetChat
//   kind        CLIENT
//   attributes  rpc.system = connect_rpc, rpc.service (the fully qualified
//               service), rpc.method, forge.rpc.client = true, and on failure
//               rpc.connect_rpc.error_code (the Connect code, `not_found`)
//   status      ERROR on failure; a cancelled call (navigation, unmount, a
//               superseded query) carries its code but broke nothing, so its
//               status stays unset and it gets no forensics
//   ends        a unary call when its response or error arrives; a stream
//               when it completes, fails, or the caller stops reading
//
// `rpc.service` + `rpc.method` are what forge servers' otelconnect spans carry
// too: the join between what the browser saw and what the server did
// (docs/proposals/runtime-context.md, "Attribute contract").
//
// ── Parenting ────────────────────────────────────────────────────────────
//
// The span is a child of the context the call entered the chain with (the
// caller's span or the open interaction; interaction-context.ts), and it
// becomes that context for everything after it: it re-stamps the call, so the
// trace stage writes a traceparent naming this span (or, with the SDK, the SDK
// fetch span opened under it), and the server's span is its descendant in the
// same trace. That is why buildRuntimeInterceptors puts this stage directly
// outside traceInterceptor.
//
// ── Forensics ────────────────────────────────────────────────────────────
//
// A failed call's span gets one `forge.forensics` event, `forensics.reason =
// error`, with the request message classified deny-by-default
// (rpc-forensics.ts), at most once per procedure and error code per page
// session. For a stream, the request is the first message the caller sent.
//
// ── Cost ─────────────────────────────────────────────────────────────────
//
// With no tracer provider registered (telemetry off, SSR, a React Native app
// without an SDK), @opentelemetry/api hands out a non-recording span; this
// stage sees that, drops it, and the call proceeds exactly as it would without
// this stage. The request is only classified when a call fails.
//
// DOM-free and React-free: part of the interceptors entry point.
import { Code, ConnectError } from "@connectrpc/connect";
import {
  context,
  SpanKind,
  SpanStatusCode,
  trace,
  type Attributes,
  type Span,
} from "@opentelemetry/api";

import { codeName } from "./errors.js";
import { restampCallEntry, stampCallEntry } from "./interaction-context.js";
import {
  claimForensics,
  classifyRequest,
  FORENSICS_EVENT,
} from "./rpc-forensics.js";
import { currentRpcTelemetry, telemetryReady } from "./telemetry-gate.js";

import type {
  Interceptor,
  StreamRequest,
  StreamResponse,
  UnaryRequest,
} from "@connectrpc/connect";

/** The instrumentation scope RPC spans are recorded under. */
export const RPC_TRACER_NAME = "@reliantlabs/forge-web-runtime/rpc";

/** Options of rpcSpanInterceptor. */
export interface RpcSpanOptions {
  /**
   * Attach the redacted `forge.forensics` event to a failed RPC's span.
   * Default true. initBrowserTelemetry's `rpcForensics: false` turns it off
   * page-wide as well.
   */
  forensics?: boolean;
}

/**
 * rpcSpanInterceptor records one CLIENT span per RPC (see the module header).
 * buildRuntimeInterceptors places it directly outside traceInterceptor; a
 * hand-built chain must do the same, or the traceparent will not name it.
 */
export function rpcSpanInterceptor(options: RpcSpanOptions = {}): Interceptor {
  return (next) => async (req) => {
    const parent = stampCallEntry(req.header);
    // The SDK registers its tracer provider as it starts; a call made while
    // it does waits for it here, as the trace stage would (bounded).
    await telemetryReady();
    const gate = currentRpcTelemetry();
    if (!gate.spans) {
      return context.with(parent, () => next(req));
    }
    const { service, method } = rpcNames(req);
    const span = trace.getTracer(RPC_TRACER_NAME).startSpan(
      `${service}/${method}`,
      {
        kind: SpanKind.CLIENT,
        attributes: {
          "rpc.system": "connect_rpc",
          "rpc.service": service,
          "rpc.method": method,
          "forge.rpc.client": true,
        },
      },
      parent,
    );
    if (!span.isRecording()) {
      // No provider (or the sampler said no): not even a context change.
      span.end();
      return context.with(parent, () => next(req));
    }
    const ctx = trace.setSpan(parent, span);
    restampCallEntry(req.header, ctx);

    let request: unknown = req.stream ? undefined : req.message;
    const forensics = options.forensics !== false;
    let ended = false;
    const finish = (err: unknown) => {
      if (ended) {
        return;
      }
      ended = true;
      try {
        if (err !== undefined) {
          recordFailure(
            span,
            err,
            forensics,
            req,
            `/${service}/${method}`,
            request,
          );
        }
      } catch {
        // Telemetry must never break the call.
      }
      span.end();
    };

    let sent: UnaryRequest | StreamRequest = req;
    if (req.stream) {
      sent = {
        ...req,
        message: tapFirst(req.message, (m) => {
          request = m;
        }),
      };
    }
    try {
      const res = await context.with(ctx, () => next(sent));
      if (res.stream) {
        return {
          ...res,
          message: watchStream(res.message, finish),
        } as StreamResponse;
      }
      finish(undefined);
      return res;
    } catch (err) {
      finish(err);
      throw err;
    }
  };
}

function recordFailure(
  span: Span,
  err: unknown,
  forensics: boolean,
  req: UnaryRequest | StreamRequest,
  procedure: string,
  request: unknown,
): void {
  const code = ConnectError.from(err).code;
  const name = codeName(code);
  span.setAttribute("rpc.connect_rpc.error_code", name);
  if (code === Code.Canceled) {
    return;
  }
  span.setStatus({ code: SpanStatusCode.ERROR, message: name });
  if (
    !forensics ||
    !currentRpcTelemetry().forensics ||
    !claimForensics(procedure, name)
  ) {
    return;
  }
  const attributes: Attributes = {
    "forensics.reason": "error",
    ...classifyRequest(req.method?.input, request),
  };
  span.addEvent(FORENSICS_EVENT, attributes);
}

/** rpc.service and rpc.method, from the descriptors or, failing that, the URL. */
function rpcNames(req: UnaryRequest | StreamRequest): {
  service: string;
  method: string;
} {
  const service = (req as Partial<UnaryRequest>).service?.typeName;
  const method = (req as Partial<UnaryRequest>).method?.name;
  if (service && method) {
    return { service, method };
  }
  let path = req.url ?? "";
  try {
    path = new URL(path).pathname;
  } catch {
    // Not absolute: use it as it is.
  }
  const parts = path.split("/").filter((p) => p !== "");
  return {
    service: parts[parts.length - 2] ?? "unknown",
    method: parts[parts.length - 1] ?? "unknown",
  };
}

/** Pass a request stream through, handing its first message to `onFirst`. */
async function* tapFirst<T>(
  messages: AsyncIterable<T>,
  onFirst: (m: T) => void,
): AsyncIterable<T> {
  let first = true;
  for await (const m of messages) {
    if (first) {
      first = false;
      onFirst(m);
    }
    yield m;
  }
}

/**
 * Pass a response stream through, ending the span when it completes, fails
 * (with the error), or the caller stops reading.
 */
async function* watchStream<T>(
  messages: AsyncIterable<T>,
  finish: (err: unknown) => void,
): AsyncIterable<T> {
  try {
    for await (const m of messages) {
      yield m;
    }
  } catch (err) {
    finish(err);
    throw err;
  } finally {
    finish(undefined);
  }
}
