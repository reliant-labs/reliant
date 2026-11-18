// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// The seam between "an RPC failed" and "the dev log has a line for it".
//
// errorNormalizeInterceptor sees every failed RPC, with its procedure and the
// trace it was recorded in. Nothing wrote that down: a scaffolded app turns a
// failed mutation into a toast, and the toast event reaches
// .forge/logs/<env>/frontend_<name>.log at debug level with its message alone
// ("HTTP 500"). The trace id was only in the server's `rpc failed` line, so
// the one grep that should join the browser's report to the server's spans and
// logs found nothing (forge bug a7db1ac2).
//
// The interceptors entry point is DOM-free and must not import devlog.ts, so
// this is a dependency-free registry, like error-reporter.ts: the interceptor
// reports here, and installDevLogging installs the sink that writes the line.
// No sink — production, SSR, a React Native app, dev logging not installed —
// means no line and no cost. Production needs none: the browser telemetry SDK
// records the failed fetch itself, and the error a caller catches carries
// `.traceId` for its own reporting.
//
// The line mirrors the server's own (`"msg":"rpc failed","procedure":…,
// "code":…,"trace_id":…`), so the same words find both:
//
//   [browser:error] rpc failed procedure=/pkg.v1.Svc/Method code=deadline_exceeded message="deadline exceeded" trace_id=<32 hex>

/** One failed RPC, as the dev log records it. */
export interface RpcFailure {
  /** "/pkg.v1.Service/Method", the server's spelling of the procedure. */
  procedure: string;
  /** The Connect code's wire name: "deadline_exceeded", "not_found", … */
  codeName: string;
  /** The backend's stable reason (x-forge-error-reason), or null. */
  reason: string | null;
  /** The error's message, without the "[code] " framing. */
  message: string;
  /** The trace the request was recorded in, or null. */
  traceId: string | null;
}

/** Receives every failed RPC the runtime's interceptors see. */
export type RpcFailureSink = (failure: RpcFailure) => void;

let sink: RpcFailureSink | null = null;

/**
 * Install (or clear, with null) the sink failed RPCs are reported to, and
 * return the one it replaced. installDevLogging installs one that writes an
 * error line to the dev log, and puts the previous one back when it is
 * uninstalled; an app may install its own (a React Native dev console, a
 * test).
 */
export function setRpcFailureSink(
  next: RpcFailureSink | null,
): RpcFailureSink | null {
  const previous = sink;
  sink = next;
  return previous;
}

/** Report a failed RPC to the installed sink. Never throws. */
export function logRpcFailure(failure: RpcFailure): void {
  if (!sink) return;
  try {
    sink(failure);
  } catch {
    // Logging a failure must never become a second failure.
  }
}

/** The one-line form of a failed RPC, trace id last. */
export function formatRpcFailure(f: RpcFailure): string {
  let line = `rpc failed procedure=${f.procedure} code=${f.codeName}`;
  if (f.reason) line += ` reason=${f.reason}`;
  line += ` message=${JSON.stringify(f.message)}`;
  if (f.traceId) line += ` trace_id=${f.traceId}`;
  return line;
}
