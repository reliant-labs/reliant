// Copyright (c) 2025 Reliant Labs

/**
 * What a VulnerabilityService failure MEANS to the reader, from its Connect
 * code. Never from the server's prose.
 *
 *   not-configured  Unimplemented: a control plane (or this client) without
 *                   the service, or one that runs without the findings store.
 *   denied          PermissionDenied / Unauthenticated: no vuln:read in the
 *                   org or for the environment, or a machine credential.
 *   not-found       NotFound: no such environment, release or advisory in
 *                   this project (existence is never revealed otherwise).
 *   bad-token       InvalidArgument: a page token from another request.
 *   unavailable     everything else: the control plane did not answer.
 */

import { Code, ConnectError } from "@connectrpc/connect";

import { retryAfterMs } from "../observability/errors";

export type VulnErrorKind = "not-configured" | "denied" | "not-found" | "bad-token" | "unavailable";

export function vulnErrorKind(error: unknown): VulnErrorKind {
  if (!(error instanceof ConnectError)) return "unavailable";
  switch (error.code) {
    case Code.Unimplemented:
      return "not-configured";
    case Code.PermissionDenied:
    case Code.Unauthenticated:
      return "denied";
    case Code.NotFound:
      return "not-found";
    case Code.InvalidArgument:
      return "bad-token";
    default:
      return "unavailable";
  }
}

/** Retry a transport blip twice; every other answer was given on purpose. */
export function vulnRetry(failureCount: number, error: unknown): boolean {
  return vulnErrorKind(error) === "unavailable" && failureCount < 2;
}

export function vulnRetryDelay(failureCount: number, error: unknown): number {
  return retryAfterMs(error) ?? Math.min(1_000 * 2 ** failureCount, 30_000);
}
