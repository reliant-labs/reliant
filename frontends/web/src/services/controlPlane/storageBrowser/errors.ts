// Copyright (c) 2025 Reliant Labs

/**
 * What a StorageBrowserService failure MEANS to the reader, from its Connect
 * code and the reason code the proto header names (lib/errorReasons). Never
 * from the server's prose.
 *
 *   not-configured   Unimplemented (a control plane from before the storage
 *                    browser) or Unavailable "… not configured …".
 *   denied           PermissionDenied / Unauthenticated: no storage:read grant
 *                    (or scope), a machine credential on an audited
 *                    environment, or a deny policy.
 *   confirmation     FailedPrecondition "confirmation_required": an audited
 *                    read without confirm_env_id and a reason. Asks again.
 *   shape-unknown    FailedPrecondition "shape_unknown": no recorded release.
 *   unreachable      FailedPrecondition "bucket_unreachable".
 *   not-previewable  FailedPrecondition "not_previewable" (PreviewObject).
 *   too-large        FailedPrecondition "too_large" (PreviewObject).
 *   refused          any other FailedPrecondition: the server's words.
 *   bad-token        InvalidArgument "bad_page_token": a page link that does
 *                    not belong to this bucket and prefix (another
 *                    environment's, or an old one).
 *   invalid          any other InvalidArgument.
 *   not-found        NotFound: no such environment, bucket or object.
 *   busy             ResourceExhausted "rate_limited", with Retry-After.
 *   too-slow         DeadlineExceeded.
 *   unavailable      everything else: the backend or the audit log did not
 *                    answer, a network blip.
 */

import { Code, ConnectError } from "@connectrpc/connect";

import { hasErrorReason } from "@/lib/errorReasons";

import { retryAfterMs } from "../observability/errors";

export type StorageErrorKind =
  | "not-configured"
  | "denied"
  | "confirmation"
  | "shape-unknown"
  | "unreachable"
  | "not-previewable"
  | "too-large"
  | "refused"
  | "bad-token"
  | "invalid"
  | "not-found"
  | "busy"
  | "too-slow"
  | "unavailable";

/** An older control plane without the rpc, or one that has it but runs without the reader behind it. */
function notOnThisServer(error: ConnectError): boolean {
  if (error.code === Code.Unimplemented) return true;
  return error.code === Code.Unavailable && /not configured/i.test(error.rawMessage || error.message);
}

export function storageErrorKind(error: unknown): StorageErrorKind {
  if (!(error instanceof ConnectError)) return "unavailable";
  if (notOnThisServer(error)) return "not-configured";
  switch (error.code) {
    case Code.PermissionDenied:
    case Code.Unauthenticated:
      return "denied";
    case Code.FailedPrecondition:
      if (hasErrorReason(error, "confirmation_required")) return "confirmation";
      if (hasErrorReason(error, "shape_unknown")) return "shape-unknown";
      if (hasErrorReason(error, "bucket_unreachable")) return "unreachable";
      if (hasErrorReason(error, "not_previewable")) return "not-previewable";
      if (hasErrorReason(error, "too_large")) return "too-large";
      return "refused";
    case Code.InvalidArgument:
      return hasErrorReason(error, "bad_page_token") ? "bad-token" : "invalid";
    case Code.NotFound:
      return "not-found";
    case Code.ResourceExhausted:
      return "busy";
    case Code.DeadlineExceeded:
      return "too-slow";
    default:
      return "unavailable";
  }
}

/**
 * Retry one transport blip, nothing else. Every read but the bucket list is an
 * audit row, and every other answer was given on purpose: a rate limit says
 * so with Retry-After, and the reader asks again from the error's button.
 */
export function storageRetry(failureCount: number, error: unknown): boolean {
  return storageErrorKind(error) === "unavailable" && failureCount < 1;
}

export function storageRetryDelay(failureCount: number, error: unknown): number {
  return retryAfterMs(error) ?? Math.min(1_000 * 2 ** failureCount, 30_000);
}
