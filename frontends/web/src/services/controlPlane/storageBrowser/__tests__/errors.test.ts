// Copyright (c) 2025 Reliant Labs

/** Every failure the StorageBrowserService header names, read by its code and reason, never its prose. */

import { describe, expect, it } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";

import { storageErrorKind, storageRetry, storageRetryDelay } from "../errors";

const err = (code: Code, reason?: string, message = "refused") =>
  new ConnectError(message, code, reason ? new Headers({ "x-forge-error-reason": reason }) : undefined);

describe("storageErrorKind", () => {
  it.each([
    [err(Code.Unimplemented), "not-configured"],
    [err(Code.Unavailable, undefined, "storage browsing is not configured on this server"), "not-configured"],
    [err(Code.PermissionDenied), "denied"],
    [err(Code.Unauthenticated), "denied"],
    [err(Code.FailedPrecondition, "confirmation_required"), "confirmation"],
    [err(Code.FailedPrecondition, "shape_unknown"), "shape-unknown"],
    [err(Code.FailedPrecondition, "bucket_unreachable"), "unreachable"],
    [err(Code.FailedPrecondition, "not_previewable"), "not-previewable"],
    [err(Code.FailedPrecondition, "too_large"), "too-large"],
    [err(Code.FailedPrecondition), "refused"],
    [err(Code.InvalidArgument, "bad_page_token"), "bad-token"],
    [err(Code.InvalidArgument), "invalid"],
    [err(Code.NotFound), "not-found"],
    [err(Code.ResourceExhausted, "rate_limited"), "busy"],
    [err(Code.DeadlineExceeded), "too-slow"],
    [err(Code.Unavailable), "unavailable"],
    [new Error("network"), "unavailable"],
  ] as const)("%s → %s", (error, kind) => {
    expect(storageErrorKind(error)).toBe(kind);
  });

  it("does not read a reason from the message", () => {
    expect(storageErrorKind(err(Code.FailedPrecondition, undefined, "confirmation_required"))).toBe("refused");
  });
});

describe("retries", () => {
  it("retries one transport blip and nothing the server said on purpose", () => {
    expect(storageRetry(0, err(Code.Unavailable))).toBe(true);
    expect(storageRetry(1, err(Code.Unavailable))).toBe(false);
    expect(storageRetry(0, err(Code.ResourceExhausted, "rate_limited"))).toBe(false);
    expect(storageRetry(0, err(Code.FailedPrecondition, "confirmation_required"))).toBe(false);
  });

  it("waits as long as the server asked", () => {
    const limited = new ConnectError("slow down", Code.ResourceExhausted, new Headers({ "retry-after": "3" }));
    expect(storageRetryDelay(0, limited)).toBe(3000);
    expect(storageRetryDelay(2, err(Code.Unavailable))).toBe(4000);
  });
});
