// Copyright (c) 2026 Reliant Labs

/**
 * What an ML read's failure MEANS to the reader, from its Connect code and
 * forge reason. Never from the server's prose.
 *
 *   not-entitled  PermissionDenied with reason FEATURE_NOT_ENTITLED: the org
 *                 has no "ml" entitlement (never granted, or revoked). The
 *                 pages show the one "Reach out to Reliant" page. The reason
 *                 alone decides it: the feature header beside it is not
 *                 CORS-exposed, and "ml" is the only feature these rpcs gate.
 *   no-data       Unimplemented: an entitled org on a control plane that does
 *                 not hold this data yet (or no control plane at all). A calm
 *                 empty state, never an error.
 *   denied        any other PermissionDenied / Unauthenticated (not a member
 *                 of the org, a machine credential).
 *   unavailable   everything else: the control plane did not answer.
 */

import { Code, ConnectError } from "@connectrpc/connect";

import { hasErrorReason } from "@/lib/errorReasons";

import { retryAfterMs } from "../observability/errors";
import type { MlOutcome } from "./types";

export type MlErrorKind = "not-entitled" | "no-data" | "denied" | "unavailable";

export function mlErrorKind(error: unknown): MlErrorKind {
  if (hasErrorReason(error, "FEATURE_NOT_ENTITLED")) return "not-entitled";
  if (!(error instanceof ConnectError)) return "unavailable";
  switch (error.code) {
    case Code.Unimplemented:
      return "no-data";
    case Code.PermissionDenied:
    case Code.Unauthenticated:
      return "denied";
    default:
      return "unavailable";
  }
}

/**
 * A read's failure as an outcome when it is one (not entitled, no data yet);
 * otherwise the error itself is rethrown for the query to show.
 */
export function outcomeOfError<T>(error: unknown): MlOutcome<T> {
  const kind = mlErrorKind(error);
  if (kind === "not-entitled" || kind === "no-data") return { kind };
  throw error;
}

/** Retry a transport blip twice; every other answer was given on purpose. */
export function mlRetry(failureCount: number, error: unknown): boolean {
  return mlErrorKind(error) === "unavailable" && failureCount < 2;
}

export function mlRetryDelay(failureCount: number, error: unknown): number {
  return retryAfterMs(error) ?? Math.min(1_000 * 2 ** failureCount, 30_000);
}
