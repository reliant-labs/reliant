// Copyright (c) 2025 Reliant Labs

/**
 * What an IdentityService failure MEANS to the reader, which is not what its
 * Connect code says.
 *
 *   not-enabled  Unavailable. This control plane does not run identity
 *                management yet (no reconciler API wired, or it refused or
 *                could not be reached). True of prod until the reconciler's
 *                internal API is switched on. A designed state, never a toast,
 *                never retried.
 *   denied       PermissionDenied / Unauthenticated: not a member of the org,
 *                a member asking for an admin verb, or a machine credential.
 *   not-ready    FailedPrecondition: the instance is not ready yet, a declared
 *                role was named, or export is not enabled for this deployment.
 *                The server's sentence says which.
 *   not-found    NotFound: no such environment, instance, user or export.
 *   invalid      InvalidArgument: a malformed email, an unknown role.
 *   error        anything else: a network blip, a server fault. Retried once.
 */

import { Code, ConnectError } from "@connectrpc/connect";

export type IdentityErrorKind = "not-enabled" | "denied" | "not-ready" | "not-found" | "invalid" | "error";

export function identityErrorKind(error: unknown): IdentityErrorKind {
  if (!(error instanceof ConnectError)) return "error";
  switch (error.code) {
    case Code.Unavailable:
      return "not-enabled";
    case Code.PermissionDenied:
    case Code.Unauthenticated:
      return "denied";
    case Code.FailedPrecondition:
      return "not-ready";
    case Code.NotFound:
      return "not-found";
    case Code.InvalidArgument:
      return "invalid";
    default:
      return "error";
  }
}

/** The server's own words, without Connect's `[code]` prefix. */
export function identityErrorMessage(error: unknown): string {
  if (error instanceof ConnectError) return error.rawMessage || error.message.replace(/^\[[^\]]+\]\s*/, "");
  if (error instanceof Error) return error.message;
  return "";
}

/** Never retry an answer the server gave on purpose. */
export function identityRetry(failureCount: number, error: unknown): boolean {
  return identityErrorKind(error) === "error" && failureCount < 1;
}
