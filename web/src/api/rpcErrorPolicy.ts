/**
 * What a failed RPC means — and so whether it is a bug report.
 *
 * The transport's error interceptor used to send every ConnectError outside a
 * short list of codes to Sentry as an exception. Three classes of failure
 * reached Sentry that are not defects in the request that failed:
 *
 *  - ABORTED (ELECTRON-74, "missing request message", and every client
 *    timeout in ELECTRON-8X). Once the request's own signal is aborted —
 *    the caller cancelled, a reconnect superseded a stream, the transport's
 *    deadline fired — whatever error the transport produces afterwards is the
 *    abort. connect-web even throws a bare STRING for one of them: aborting a
 *    server stream while an earlier interceptor is still awaiting (the auth
 *    token) makes connect close the request iterable, and the fetch layer then
 *    finds it empty and throws "missing request message". A client timeout is
 *    reported once, as the rate-limited `rpc-timeout` warning, not once per
 *    call it cut off.
 *
 *  - MACHINE WAIT (ELECTRON-AV, 162 events/day from one user). A file, process
 *    or terminal read made while the user's machine is still starting, waking
 *    or suspended fails as Unavailable carrying the "no daemon connected"
 *    marker. That is a state the UI renders and retries (useDaemonWait), not a
 *    failure; the file tree reported it on every 2s retry.
 *
 *  - VERSION SKEW (ELECTRON-B3). A route the reliant API does not serve answers
 *    with a bare HTTP 404, which connect maps to Unimplemented with the message
 *    "HTTP 404". That is an app older than the server (desktop 1.7.16 calling
 *    the retired ChatService/CreateChat) — the fix is an update prompt.
 *
 * Classification is by code AND marker, never by message alone, so a real
 * Unavailable (server down) or a real Unimplemented sent by a handler still
 * reports.
 */

import { Code, ConnectError } from "@connectrpc/connect";
import { isDaemonConnectingError } from "../lib/daemon-errors";
import { ACCOUNT_REQUIRED_REASON } from "../lib/accountRequired";

export type RpcFailureKind =
  /** The request was cancelled; the error is a consequence of that. */
  | "aborted"
  /** The account needs a real identity first; the UI asks for one. */
  | "account-required"
  /** A code that describes the caller's input or state, not a defect. */
  | "expected"
  /** The user's machine is not serving yet; the UI waits on it. */
  | "machine-wait"
  /** This app calls an RPC the server does not route: the app is out of date. */
  | "version-skew"
  /** Anything else: worth a report. */
  | "report";

// Connect error codes that are client-side / expected and should NOT be
// reported to Sentry.
const EXPECTED_CODES: ReadonlySet<Code> = new Set([
  Code.Canceled,
  Code.InvalidArgument,
  Code.NotFound,
  Code.AlreadyExists,
  Code.PermissionDenied,
  Code.Unauthenticated,
  Code.FailedPrecondition,
  Code.Aborted,
  Code.OutOfRange,
  Code.ResourceExhausted,
]);

/** The services this app's own API serves. Other backends version separately. */
const RELIANT_API_SERVICE_PREFIX = "reliant.v1.";

export interface RpcFailureContext {
  serviceTypeName: string;
  /** Whether the request's own abort signal had fired when it failed. */
  signalAborted: boolean;
}

/**
 * True when the server answered a route it does not serve: a bare HTTP 404
 * (connect's message is exactly "HTTP 404" when the body was not a Connect
 * error), as opposed to a handler that deliberately returned Unimplemented.
 */
export function isUnroutedProcedure(error: unknown): boolean {
  return (
    error instanceof ConnectError &&
    error.code === Code.Unimplemented &&
    error.rawMessage === "HTTP 404"
  );
}

export function classifyRpcFailure(error: unknown, ctx: RpcFailureContext): RpcFailureKind {
  if (ctx.signalAborted) return "aborted";
  if (!(error instanceof ConnectError)) return "report";
  // By reason, whatever the code: it is a state the UI renders (the identity
  // modal), and must not depend on the server's choice of status code.
  if (error.metadata.get("x-reliant-reason") === ACCOUNT_REQUIRED_REASON) {
    return "account-required";
  }
  if (EXPECTED_CODES.has(error.code)) return "expected";
  if (error.code === Code.Unavailable && isDaemonConnectingError(error)) {
    return "machine-wait";
  }
  if (
    ctx.serviceTypeName.startsWith(RELIANT_API_SERVICE_PREFIX) &&
    isUnroutedProcedure(error)
  ) {
    return "version-skew";
  }
  return "report";
}
