// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// Which trace an outgoing request joins while an interaction is open.
//
// ── Why a registry and not context.with ──────────────────────────────────
//
// A browser has no async context. OpenTelemetry's web context managers (the
// stack manager, and the HyperDX SDK's, which is the same thing unless async
// patching is switched on) hold the active span only for the SYNCHRONOUS
// extent of a context.with() call; the first `await` drops it. The
// interceptor stack restores the CALLER's context across its own awaits
// (#1428), which parents an RPC on a span the caller had active when it made
// the call. But "every RPC made while the chat is opening" is a statement
// about wall-clock time, not about a call stack: those RPCs start in effects,
// query functions and stream hooks that never ran inside the click handler,
// with no span active at all. So interaction.ts registers each open
// interaction here, and the choice — the caller's span if it has one, else
// the foreground interaction (outgoingContext) — is made when the call enters
// the interceptor chain and held until fetch() is called (see "Call entry").
//
// ── Carrying the context to the browser fetch span ───────────────────────
//
// With the SDK running, the span the server joins is not ours: the SDK's
// patched fetch opens a CLIENT span parented on context.active() at the
// instant fetch() is called, and overwrites the traceparent with that span's.
// The trace interceptor calls next() inside context.with(ctx), which covers a
// call whose fetch() is synchronous with next() (connect-web's unary path).
// A server stream is not: connect-web awaits the request body
// before it calls fetch(), and the context is gone by then. So the fetch
// shim telemetry.ts installs over the SDK's patched fetch
// (installFetchContext) looks up the call-entry stamp of the request's
// Headers (below) and re-enters it for the duration of that call.
//
// This module imports nothing but @opentelemetry/api: the interceptors entry
// point (React-free, DOM-free) reaches it through trace.ts.
import { context, trace, type Context } from "@opentelemetry/api";

// Oldest first. The last one is the foreground: the operation the user asked
// for most recently.
const open: Context[] = [];

/** Register an open interaction; returns the function that unregisters it. */
export function registerOpenInteraction(ctx: Context): () => void {
  open.push(ctx);
  return () => {
    const i = open.indexOf(ctx);
    if (i !== -1) {
      open.splice(i, 1);
    }
  };
}

/**
 * The context an outgoing request should be parented on, right now.
 *
 * A span the caller made active wins: an action the app traced explicitly
 * (context.with(actionSpan, () => client.call())), a span under op.run(). The
 * interceptor stack carries the caller's context across its own awaits
 * (#1428), so the caller's span is still active when this runs. With no span
 * active, a request joins the most recently started open interaction — the
 * caller's context with the interaction's span set on it, so any other value
 * the caller carries (baggage, its own keys) still reaches the call. With none
 * open it is context.active(), exactly as before.
 */
export function outgoingContext(): Context {
  const active = context.active();
  const foreground = open[open.length - 1];
  if (!foreground) {
    return active;
  }
  const sc = trace.getSpanContext(active);
  if (sc && trace.isSpanContextValid(sc)) {
    return active;
  }
  const span = trace.getSpan(foreground);
  return span ? trace.setSpan(active, span) : active;
}

// ── Call entry ───────────────────────────────────────────────────────────
//
// The choice has to be made when the CALL starts, not when the trace stage
// is reached: an earlier stage that awaits (auth's getToken, an app's own
// interceptor) can hold the call while the user starts another interaction,
// and a choice made after that wait would put the RPC in the wrong operation.
// connect-es runs the outermost interceptor synchronously inside the client
// method call (createClient → transport.unary/stream → runUnaryCall /
// runStreamingCall → the interceptor chain, with no await in between), so the
// outermost stage IS call entry.
//
// So every runtime interceptor stamps the call on entry, synchronously, if no
// earlier one has (stampCallEntry): whichever runtime stage is outermost —
// errorNormalizeInterceptor in buildRuntimeInterceptors and in every chain
// that starts with it — makes the choice, and the order of the rest does not
// matter. A chain whose outermost stages are the app's own AND await before
// any runtime stage runs puts interactionContextInterceptor() first.
//
// The stamp is keyed by the request's Headers object: connect builds a new
// one for every call (requestHeader), interceptors mutate it in place, and
// connect-web hands it to fetch() as-is — so the same entry carries the
// context to the fetch shim below. (A caller-supplied contextValues object is
// the alternative carrier, but an app may reuse one across calls.) A stage
// that REPLACES req.header loses the stamp; the trace stage then stamps the
// new object on its own entry.
const callContexts = new WeakMap<object, Context>();

/**
 * The context this call joins: chosen (outgoingContext()) the first time a
 * runtime stage sees the call, and the same answer for every later stage.
 */
export function stampCallEntry(headers: Headers): Context {
  const stamped = callContexts.get(headers);
  if (stamped) {
    return stamped;
  }
  const ctx = outgoingContext();
  callContexts.set(headers, ctx);
  return ctx;
}

function contextOfHeaders(headers: unknown): Context | undefined {
  return typeof headers === "object" && headers !== null
    ? callContexts.get(headers)
    : undefined;
}

const SHIM_MARK = Symbol.for("forge.web-runtime.fetch-context");

type FetchFn = (...args: Parameters<typeof fetch>) => ReturnType<typeof fetch>;

/**
 * Wrap globalThis.fetch so a call whose request headers were remembered runs
 * inside the context its interceptor chose. Install it AFTER the telemetry SDK
 * has patched fetch, so the SDK's span is what starts inside that context.
 * Idempotent; a no-op without fetch.
 */
export function installFetchContext(): void {
  const g = globalThis as { fetch?: FetchFn };
  const inner = g.fetch;
  if (
    typeof inner !== "function" ||
    (inner as unknown as Record<symbol, unknown>)[SHIM_MARK]
  ) {
    return;
  }
  const wrapped = function (this: unknown, ...args: Parameters<typeof fetch>) {
    let ctx: Context | undefined;
    try {
      const [input, init] = args;
      ctx =
        contextOfHeaders(init?.headers) ??
        (typeof Request !== "undefined" && input instanceof Request
          ? contextOfHeaders(input.headers)
          : undefined);
    } catch {
      ctx = undefined;
    }
    if (!ctx || !trace.getSpan(ctx) || ctx === context.active()) {
      return inner.apply(this, args);
    }
    return context.with(ctx, () => inner.apply(this, args));
  };
  Object.defineProperty(wrapped, SHIM_MARK, { value: true });
  g.fetch = wrapped;
}
