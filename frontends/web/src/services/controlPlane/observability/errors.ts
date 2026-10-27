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
 *   over-quota   ResourceExhausted carrying the quota answer (quota.ts). The
 *                org's hourly ClickHouse query allowance is used up; it
 *                refills at the top of the hour and the console says when.
 *   busy         any other ResourceExhausted: too many reads in flight, or a
 *                query that touched too much data. A narrower query fits.
 *   too-broad    DeadlineExceeded. The query ran out of time; a narrower
 *                window or filter usually fixes it.
 *   unavailable  everything else: the stack is down, a network blip.
 */

import { Code, ConnectError } from "@connectrpc/connect";

import { hasErrorReason } from "@/lib/errorReasons";

import { quotaOf } from "./quota";

export type ObsErrorKind = "not-set-up" | "denied" | "invalid" | "over-quota" | "busy" | "too-broad" | "unavailable";

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
      return quotaOf(error) ? "over-quota" : "busy";
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
 *   not-configured  reason observability_not_configured: this control
 *                   plane runs no Alertmanager yet (hosted
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
    if (hasErrorReason(error, "observability_not_configured")) return "not-configured";
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

/**
 * What a DeliveryInsightsService failure means. Both of its refusals are
 * PermissionDenied, and they ask for different things of the reader:
 *
 *   not-member       reason not_org_member: the org itself is closed to the
 *                    caller; no narrowing helps.
 *   env-unavailable  "one or more environments are not available to this
 *                    organization": an env id in scope is missing, deleted or
 *                    another org's (the server words all three the same, so
 *                    as not to reveal another org's environments). Widening to
 *                    every environment does help.
 *
 * Everything else reads as obsErrorKind does.
 */
export type DeliveryErrorKind = ObsErrorKind | "not-member" | "env-unavailable";

export function deliveryErrorKind(error: unknown): DeliveryErrorKind {
  if (hasErrorReason(error, "not_org_member")) return "not-member";
  if (error instanceof ConnectError && error.code === Code.PermissionDenied) {
    const message = error.rawMessage || error.message;
    if (/environments? .*not available/i.test(message)) return "env-unavailable";
  }
  return obsErrorKind(error);
}

/** An older control plane without the rpc, or one that has it but runs without the reader behind it. */
function notOnThisServer(error: unknown): boolean {
  if (!(error instanceof ConnectError)) return false;
  if (error.code === Code.Unimplemented) return true;
  return error.code === Code.Unavailable && /not configured/i.test(error.rawMessage || error.message);
}

/**
 * What an IssuesService failure means. Issues add three answers:
 *
 *   not-configured  Unimplemented (a control plane from before issues) or
 *                   Unavailable "issues are not configured on this server".
 *                   Explained in place, never retried.
 *   conflict        Aborted on a triage: the issue changed since it was read
 *                   (expected_version is stale). The view refetches and shows
 *                   what changed; it never re-sends over the other change.
 *   not-found       NotFound: no such issue, or one in an org the caller is
 *                   not in (the server does not tell the two apart).
 *
 * Everything else reads as obsErrorKind does.
 */
export type IssuesErrorKind = ObsErrorKind | "not-configured" | "conflict" | "not-found";

export function issuesErrorKind(error: unknown): IssuesErrorKind {
  if (notOnThisServer(error)) return "not-configured";
  if (error instanceof ConnectError) {
    if (error.code === Code.Aborted) return "conflict";
    if (error.code === Code.NotFound) return "not-found";
  }
  return obsErrorKind(error);
}

export function issuesRetry(failureCount: number, error: unknown): boolean {
  if (issuesErrorKind(error) !== "unavailable") return false;
  return failureCount < 1;
}

/**
 * What a GetReleaseHealth failure means:
 *
 *   not-available  Unimplemented (an older control plane) or Unavailable
 *                  "release health is not configured on this server": an
 *                  inline note, not an error page.
 *   not-found      NotFound: the environment never had that promotion.
 *
 * Everything else reads as obsErrorKind does.
 */
export type ReleaseHealthErrorKind = ObsErrorKind | "not-available" | "not-found";

export function releaseHealthErrorKind(error: unknown): ReleaseHealthErrorKind {
  if (notOnThisServer(error)) return "not-available";
  if (error instanceof ConnectError && error.code === Code.NotFound) return "not-found";
  return obsErrorKind(error);
}

export function releaseHealthRetry(failureCount: number, error: unknown): boolean {
  if (releaseHealthErrorKind(error) !== "unavailable") return false;
  return failureCount < 1;
}

/**
 * What a RumService failure means. Sessions add two answers:
 *
 *   not-configured  Unimplemented (a control plane from before sessions) or
 *                   Unavailable "sessions are not configured on this server".
 *                   Explained in place, never retried.
 *   not-found       NotFound: no such session in the scope — none at all, or
 *                   one of another environment or service (the server words
 *                   them the same).
 *
 * Everything else reads as obsErrorKind does.
 */
export type RumErrorKind = ObsErrorKind | "not-configured" | "not-found";

export function rumErrorKind(error: unknown): RumErrorKind {
  if (notOnThisServer(error)) return "not-configured";
  if (error instanceof ConnectError && error.code === Code.NotFound) return "not-found";
  return obsErrorKind(error);
}

export function rumRetry(failureCount: number, error: unknown): boolean {
  if (rumErrorKind(error) !== "unavailable") return false;
  return failureCount < 1;
}
