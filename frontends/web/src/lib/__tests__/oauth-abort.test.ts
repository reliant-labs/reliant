/**
 * Clicking "Connect" twice must not report a failure.
 *
 * The second click aborts the first run (useCodexOAuth.start aborts the
 * previous AbortController before creating a new one), which rejects the
 * abandoned fetch. That rejection used to be classified as `daemon_error` and
 * shown as a red banner carrying the browser's raw wording —
 * "signal is aborted without reason" — *while the second flow was succeeding*.
 *
 * These pin the classification. The UI suppression (errorCode === 'cancelled')
 * is asserted separately at each call site's own level.
 */
import { describe, expect, it } from "vitest";
import { isAbort } from "../oauth-abort";

describe("isAbort", () => {
  it("recognises a DOMException AbortError", () => {
    // What a fetch severed by AbortController actually throws. Chrome's
    // message is the string users were seeing.
    const err = new DOMException("signal is aborted without reason", "AbortError");
    expect(isAbort(err)).toBe(true);
  });

  it("trusts an aborted signal whatever the error looks like", () => {
    // The daemon gRPC path surfaces an abort as a transport error that names
    // nothing about aborting, so the error alone cannot classify it. The
    // signal is the reliable witness — this is the case that made a helper
    // necessary rather than an inline instanceof.
    const controller = new AbortController();
    controller.abort();
    expect(isAbort(new Error("stream terminated"), controller.signal)).toBe(true);
    expect(isAbort(new TypeError("Failed to fetch"), controller.signal)).toBe(true);
    expect(isAbort(undefined, controller.signal)).toBe(true);
  });

  it("recognises a plain Error carrying the AbortError name", () => {
    // Some runtimes (and copilot-oauth's own AbortError class) reject with a
    // plain Error whose name is set rather than a DOMException.
    const err = new Error("Aborted");
    err.name = "AbortError";
    expect(isAbort(err)).toBe(true);
  });

  it("does NOT classify a real failure as cancelled", () => {
    // The whole point is that genuine failures still surface. A signal that
    // exists but is not aborted must not suppress anything.
    const controller = new AbortController();
    expect(isAbort(new Error("token exchange failed"), controller.signal)).toBe(false);
    expect(isAbort(new TypeError("Failed to fetch"))).toBe(false);
    expect(isAbort(new DOMException("boom", "NetworkError"))).toBe(false);
    expect(isAbort(null)).toBe(false);
    expect(isAbort("nope")).toBe(false);
  });
});
