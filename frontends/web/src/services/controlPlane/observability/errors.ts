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

import { BinaryReader, WireType } from "@bufbuild/protobuf/wire";
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

/**
 * The console's concurrent-read cap (control-plane obsratelimit.InFlight): a
 * ResourceExhausted with reason `in_flight_limit`. Not the hourly quota, which
 * shares the code, and not a fault: a slot frees as soon as one of the user's
 * own reads finishes, so the answer is "wait and ask again", never "give up".
 */
export function isInFlightLimit(error: unknown): boolean {
  return error instanceof ConnectError && error.code === Code.ResourceExhausted && !quotaOf(error) && hasErrorReason(error, "in_flight_limit");
}

const RETRY_INFO_TYPE = "google.rpc.RetryInfo";
/** A server asking for a longer pause than this is a bad clock or a bad header. */
const MAX_RETRY_AFTER_MS = 60_000;

/** google.rpc.RetryInfo { Duration retry_delay = 1 } → milliseconds, or null when it carries none. */
function retryInfoMs(bytes: Uint8Array): number | null {
  try {
    const info = new BinaryReader(bytes);
    while (info.pos < info.len) {
      const [field, wire] = info.tag();
      if (field !== 1 || wire !== WireType.LengthDelimited) {
        info.skip(wire, field);
        continue;
      }
      const duration = new BinaryReader(info.bytes());
      let seconds = 0;
      let nanos = 0;
      while (duration.pos < duration.len) {
        const [f, w] = duration.tag();
        if (f === 1 && w === WireType.Varint) seconds = Number(duration.int64());
        else if (f === 2 && w === WireType.Varint) nanos = duration.int32();
        else duration.skip(w, f);
      }
      return seconds * 1000 + nanos / 1e6;
    }
  } catch {
    // A detail that does not decode is no detail.
  }
  return null;
}

/**
 * How long the server asked the caller to wait, in ms, or null when it did not
 * say. The typed RetryInfo detail travels in the error body, so any origin can
 * read it; the `Retry-After` header is the fallback (a browser reads it only
 * same-origin or when the server exposes it).
 */
export function retryAfterMs(error: unknown): number | null {
  if (!(error instanceof ConnectError)) return null;
  let ms: number | null = null;
  for (const detail of error.details) {
    if ("type" in detail && detail.type === RETRY_INFO_TYPE) {
      ms = retryInfoMs(detail.value);
      if (ms !== null) break;
    }
  }
  if (ms === null) {
    const header = Number(error.metadata.get("retry-after"));
    if (error.metadata.has("retry-after") && Number.isFinite(header) && header >= 0) ms = header * 1000;
  }
  return ms === null ? null : Math.min(Math.max(ms, 0), MAX_RETRY_AFTER_MS);
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
