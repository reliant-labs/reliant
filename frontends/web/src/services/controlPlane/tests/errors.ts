// Copyright (c) 2026 Reliant Labs

/**
 * What a TestsService failure MEANS to the reader, from its Connect code.
 * Never from the server's prose.
 *
 *   not-configured  Unimplemented: a control plane (or this client) without
 *                   the service. The pages say "this control plane doesn't
 *                   store test runs yet", never show an error.
 *   denied          PermissionDenied / Unauthenticated: no tests:read in the
 *                   org (it comes with deploy:read), or a token bound to
 *                   another environment.
 *   not-found       NotFound: no such run, release or test in this project
 *                   (existence is never revealed otherwise).
 *   bad-token       InvalidArgument: a page token from another request.
 *   unavailable     everything else: the control plane did not answer.
 */

import { Code, ConnectError } from "@connectrpc/connect";

import { retryAfterMs } from "../observability/errors";

export type TestsErrorKind = "not-configured" | "denied" | "not-found" | "bad-token" | "unavailable";

export function testsErrorKind(error: unknown): TestsErrorKind {
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

/** The control plane has no TestsService (or this client cannot call it yet): a state, not an error. */
export function isTestsServiceMissing(error: unknown): boolean {
  return testsErrorKind(error) === "not-configured";
}

/** Retry a transport blip twice; every other answer was given on purpose. */
export function testsRetry(failureCount: number, error: unknown): boolean {
  return testsErrorKind(error) === "unavailable" && failureCount < 2;
}

export function testsRetryDelay(failureCount: number, error: unknown): number {
  return retryAfterMs(error) ?? Math.min(1_000 * 2 ** failureCount, 30_000);
}
