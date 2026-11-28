/**
 * Reporting a failure the app caught and handled itself.
 *
 * The telemetry SDK records uncaught exceptions and unhandled rejections on
 * its own. A failure the app catches — a failed rpc, an OAuth exchange the
 * provider refused, a chat that would not load — is invisible to it, and
 * reaches the error pipeline (ClickStack's error_events) only through here.
 *
 * Both calls are no-ops unless browser telemetry is running, and they never
 * throw: reporting a failure must not become a second one. The runtime's
 * reportException does nothing until the SDK has started, and telemetry.ts
 * starts it only with an endpoint (telemetryEndpoint.ts) and the user's
 * crash-reporting consent, never in dev; withdrawing consent calls the
 * runtime's disableTelemetry(), which turns reportException off again. So
 * consent is enforced there, not re-read here.
 *
 * Dependency-light on purpose. It imports the runtime's dependency-free
 * barrel, not the `/telemetry` subpath that loads the SDK, and not
 * hooks/privacy-queries: the transport calls this, and privacy-queries reaches
 * the transport through the generated settings hooks, so importing it here
 * is an import cycle. api/rpcDeadline imports only @connectrpc/connect and
 * the dependency-free lib/handledRpcFailures, so reaching it here is not one.
 *
 * ── Typed transport failures group by type and message alone ─────────────
 *
 * The control plane groups a browser error by its type, its normalized message
 * and its top stack frames (control-plane internal/symbolicate/fingerprint.go,
 * fp1); an error with no frames groups by type and message only (its fpNoStack
 * path). For a client-side timeout or stall the frames say nothing: they are
 * the deadline timer inside the transport, the same for every RPC. Yet on a
 * build whose source maps did not resolve they forked the issue per build
 * (hashed asset names, mangled function names) and per browser ("Object.onTimeout"
 * in V8, "onTimeout" in WebKit): prod, 2026-11-26, one ListInbox timeout was
 * issues a9b5dd6f and 7a1069dd. So these reports carry no frames (owner
 * decision, 2026-11-27): one issue per message, which names the RPC, so each
 * RPC keeps an issue of its own. What they knew travels as attributes.
 */

import { Code, ConnectError } from "@connectrpc/connect";
import {
  reportException,
  SERVER_TRACE_ID_ATTRIBUTE,
  serverTraceIdOf,
} from "@reliantlabs/forge-web-runtime";
import { isClientInitiatedAbort } from "../api/rpcDeadline";
import { clickstackEnabled } from "./telemetryEndpoint";

/**
 * Where a handled failure happened, as span attributes: low-cardinality names
 * (`component`, `operation`, `provider`) and ids. Never user content — a chat
 * message, a file path or a token does not belong here. Undefined values are
 * dropped.
 */
export type ErrorContext = Record<string, string | number | boolean | undefined>;

function attributesOf(
  context: ErrorContext | undefined,
): Record<string, string | number | boolean> | undefined {
  if (!context) return undefined;
  const attributes: Record<string, string | number | boolean> = {};
  for (const [key, value] of Object.entries(context)) {
    if (value !== undefined) attributes[key] = value;
  }
  return attributes;
}

/**
 * An Error of type `type` with no stack frames, so the error pipeline groups
 * it by type and message alone (see the header). Its stack is the line an
 * engine prints first, `<type>: <message>`, which no stack parser reads as a
 * frame.
 */
function framelessError(type: string, message: string): Error {
  const error = new Error(message);
  error.name = type;
  error.stack = `${type}: ${message}`;
  return error;
}

/** How far down `cause` a wrapped failure is followed (handledRpcFailures.ts). */
const MAX_CAUSE_DEPTH = 4;

/**
 * The transport's own deadline abort inside `error`, if it is a client-side
 * timeout: the deadline's ConnectError itself, or one the runtime's
 * ConnectClientError wraps through `cause`.
 */
function clientTimeoutIn(error: unknown): ConnectError | null {
  let current: unknown = error;
  for (let depth = 0; depth <= MAX_CAUSE_DEPTH; depth++) {
    if (typeof current !== "object" || current === null) return null;
    if (
      isClientInitiatedAbort(current) &&
      current instanceof ConnectError &&
      current.code === Code.DeadlineExceeded
    ) {
      return current;
    }
    current = (current as { cause?: unknown }).cause;
  }
  return null;
}

/**
 * What is handed to the runtime for `error`: the error itself, except for a
 * client-side timeout, which goes frameless under the caller's error type and
 * the deadline's own message ("ListMessages timed out after 10000ms", without
 * the "[deadline_exceeded] " framing), with the trace it was sent in kept as
 * the attribute the runtime would have added.
 */
function reportable(
  error: unknown,
  attributes: Record<string, string | number | boolean> | undefined,
): [unknown, Record<string, string | number | boolean> | undefined] {
  const timeout = clientTimeoutIn(error);
  if (!timeout) return [error, attributes];
  const type = error instanceof Error && error.name ? error.name : timeout.name;
  const traceId = serverTraceIdOf(error);
  return [
    framelessError(type, timeout.rawMessage),
    traceId ? { [SERVER_TRACE_ID_ATTRIBUTE]: traceId, ...attributes } : attributes,
  ];
}

/** Report an error the app caught and handled. */
export function reportError(error: unknown, context?: ErrorContext): void {
  try {
    if (!clickstackEnabled()) return;
    reportException(...reportable(error, attributesOf(context)));
  } catch {
    // Telemetry must never break the app.
  }
}

/**
 * Report a handled failure that has no exception object of its own (a
 * provider's refusal, a state mismatch): an Error carrying `message`, so it
 * groups by that message in the error pipeline.
 */
export function reportFailure(message: string, context?: ErrorContext): void {
  reportError(new Error(message), context);
}

/**
 * reportFailure under an exception type of its own: an Error named `type`, so
 * the issue is titled `<type>: <message>` and the control plane can tell the
 * kind of failure from the type alone (the type is what the error pipeline
 * keeps of a report; its attributes stay on the event). The client-side
 * timeouts use it: `RpcTimeout` and `RpcStall` issues page only once a second
 * browser session has hit them.
 *
 * The Error carries no stack frames, so its issue is its type and message
 * alone (see the header): the message is the identity, by design, and the
 * reporter's own frames would only fork it per build and per browser.
 */
export function reportTypedFailure(type: string, message: string, context?: ErrorContext): void {
  reportError(framelessError(type, message), context);
}
