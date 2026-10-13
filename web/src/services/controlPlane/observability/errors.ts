// Copyright (c) 2025 Reliant Labs

/**
 * What an observability failure MEANS to the reader, which is not what its
 * Connect code says.
 *
 *   not-set-up   FailedPrecondition. The org has no tenant on the observability
 *                stack yet (control-plane provisions one per org). Nothing is
 *                broken and retrying will not help; the page explains it.
 *   denied       PermissionDenied / Unauthenticated. The caller may not read
 *                this environment's telemetry.
 *   invalid      InvalidArgument. Almost always the free-text query — the
 *                server's parse error is the message worth showing, next to
 *                the box that caused it.
 *   over-quota   ResourceExhausted. The org's ClickHouse query quota is used
 *                up for now; it refills on its own, and a narrower query
 *                spends less of it.
 *   too-broad    DeadlineExceeded. The query ran out of time; a narrower
 *                window or filter usually fixes it.
 *   unavailable  everything else: the stack is down, a network blip.
 */

import { Code, ConnectError } from "@connectrpc/connect";

export type ObsErrorKind = "not-set-up" | "denied" | "invalid" | "over-quota" | "too-broad" | "unavailable";

export function obsErrorKind(error: unknown): ObsErrorKind {
  if (!(error instanceof ConnectError)) return "unavailable";
  switch (error.code) {
    case Code.FailedPrecondition:
      return "not-set-up";
    case Code.PermissionDenied:
    case Code.Unauthenticated:
      return "denied";
    case Code.InvalidArgument:
      return "invalid";
    case Code.ResourceExhausted:
      return "over-quota";
    case Code.DeadlineExceeded:
      return "too-broad";
    default:
      return "unavailable";
  }
}

/** The server's own words, without Connect's `[code]` prefix. */
export function obsErrorMessage(error: unknown): string {
  if (error instanceof ConnectError) return error.rawMessage || error.message.replace(/^\[[^\]]+\]\s*/, "");
  if (error instanceof Error) return error.message;
  return "";
}

/** A not-set-up answer is a fact about the org, not a fault: never retry it, nor any other answer the server gave on purpose. */
export function obsRetry(failureCount: number, error: unknown): boolean {
  const kind = obsErrorKind(error);
  if (kind !== "unavailable") return false;
  return failureCount < 1;
}

/**
 * What an AlertingService failure means. Alerting adds two answers the query
 * service does not give:
 *
 *   not-configured  Unavailable "alerting is not configured on this server":
 *                   this control plane runs no Alertmanager yet (hosted
 *                   observability is off). Explained, never retried.
 *   in-use          FailedPrecondition on a receiver delete: a route in the
 *                   code still names it.
 *
 * Everything else reads as obsErrorKind does; PermissionDenied on a mutation
 * means the caller is a member but not an admin.
 */
export type AlertingErrorKind = ObsErrorKind | "not-configured" | "in-use";

export function alertingErrorKind(error: unknown): AlertingErrorKind {
  if (error instanceof ConnectError) {
    if (error.code === Code.Unavailable && /not configured/i.test(error.rawMessage || error.message)) return "not-configured";
    if (error.code === Code.FailedPrecondition) return "in-use";
  }
  return obsErrorKind(error);
}

/** Never retry an answer alerting gave on purpose (not configured, not allowed). */
export function alertingRetry(failureCount: number, error: unknown): boolean {
  const kind = alertingErrorKind(error);
  if (kind !== "unavailable") return false;
  return failureCount < 1;
}
