/**
 * RPC failures whose reporting the transport has already decided.
 *
 * Every RPC failure passes the transport's error interceptor
 * (api/transport.ts), which decides once, under api/rpcErrorPolicy.ts, whether
 * it is a defect worth a report — and reports it then, or deliberately does
 * not. The transport's own deadline reports a timeout once, as the
 * rate-limited `rpc-timeout` failure, and nothing else of its aborts.
 *
 * What the caller does with the failure afterwards is a log line, not a second
 * decision: `console.error("Failed to fetch file preview info:", err)` in a
 * helper that rethrows to a viewer that renders "file not found". The browser
 * SDK records any Error passed to console.error as an exception, so each such
 * line was a second report of a failure already reported — or a report of one
 * the policy had decided is not a defect. Prod, 2026-11-03..06: a missing
 * file's NotFound, a sleeping machine's "no daemon connected for user" (once
 * per 5s viewer retry) and "archive the chat before deleting it permanently"
 * were reliant-web's top JS issues and fed FrontendJSErrorSpike.
 *
 * So the transport notes every failure it decided on here, and telemetry.ts
 * keeps a console.error naming one of them out of the SDK (it still reaches
 * the console and the log file). A failure the transport never saw — an error
 * a server stream raises mid-iteration, any non-RPC Error — is untouched.
 *
 * Dependency-free on purpose: the transport, the deadline and telemetry all
 * import it.
 */

const decided = new WeakSet<object>();

/** How far down `cause` a wrapped failure is followed. */
const MAX_CAUSE_DEPTH = 4;

/** Record that the transport decided how `error` is reported. */
export function noteRpcFailureDecided(error: unknown): void {
  if (typeof error === "object" && error !== null) decided.add(error);
}

/**
 * True when `error`, or an error it wraps through `cause` (the runtime's
 * ConnectClientError wraps the failure the transport saw), is a failure the
 * transport already decided on.
 */
export function isRpcFailureDecided(error: unknown): boolean {
  let current: unknown = error;
  for (let depth = 0; depth <= MAX_CAUSE_DEPTH; depth++) {
    if (typeof current !== "object" || current === null) return false;
    if (decided.has(current)) return true;
    current = (current as { cause?: unknown }).cause;
  }
  return false;
}
