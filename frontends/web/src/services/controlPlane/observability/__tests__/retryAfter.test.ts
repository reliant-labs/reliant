import { describe, expect, it } from "vitest";
import { BinaryWriter } from "@bufbuild/protobuf/wire";
import { Code, ConnectError } from "@connectrpc/connect";

import { isInFlightLimit, obsErrorKind, retryAfterMs } from "../errors";
import { quotaPausedError } from "../quota";

const REASON = "x-forge-error-reason";

/** google.rpc.RetryInfo { retry_delay: Duration { seconds, nanos } }, as it arrives off the wire. */
function retryInfo(seconds: number, nanos = 0): Uint8Array {
  const duration = new BinaryWriter().tag(1, 0).int64(seconds).tag(2, 0).int32(nanos).finish();
  return new BinaryWriter().tag(1, 2).bytes(duration).finish();
}

function inFlight(opts: { detail?: Uint8Array; header?: string; reason?: string | null } = {}) {
  const metadata: Record<string, string> = {};
  if (opts.reason !== null) metadata[REASON] = opts.reason ?? "in_flight_limit";
  if (opts.header) metadata["retry-after"] = opts.header;
  const error = new ConnectError("too many observability queries in flight; retry shortly", Code.ResourceExhausted, metadata);
  if (opts.detail) error.details.push({ type: "google.rpc.RetryInfo", value: opts.detail });
  return error;
}

describe("isInFlightLimit", () => {
  it("is the in_flight_limit reason, not the hourly quota and not the words", () => {
    expect(isInFlightLimit(inFlight())).toBe(true);
    expect(isInFlightLimit(inFlight({ reason: null }))).toBe(false);
    expect(isInFlightLimit(inFlight({ reason: "obs_query_quota" }))).toBe(false);
    expect(isInFlightLimit(quotaPausedError({ limit: "queries", lane: "interactive", resetsAtMs: Date.now() + 60_000, soft: false }))).toBe(false);
    expect(isInFlightLimit(new ConnectError("down", Code.Unavailable, { [REASON]: "in_flight_limit" }))).toBe(false);
    expect(isInFlightLimit(new Error("boom"))).toBe(false);
  });

  it("is still a busy answer to the views that only read the kind", () => {
    expect(obsErrorKind(inFlight())).toBe("busy");
  });
});

describe("retryAfterMs", () => {
  it("reads the typed RetryInfo detail", () => {
    expect(retryAfterMs(inFlight({ detail: retryInfo(1) }))).toBe(1000);
    expect(retryAfterMs(inFlight({ detail: retryInfo(2, 500_000_000) }))).toBe(2500);
  });

  it("prefers the detail to the header, and falls back to the header", () => {
    expect(retryAfterMs(inFlight({ detail: retryInfo(3), header: "9" }))).toBe(3000);
    expect(retryAfterMs(inFlight({ header: "4" }))).toBe(4000);
  });

  it("is null when the server said nothing, and ignores nonsense", () => {
    expect(retryAfterMs(inFlight())).toBeNull();
    expect(retryAfterMs(inFlight({ header: "soon" }))).toBeNull();
    expect(retryAfterMs(inFlight({ header: "-3" }))).toBeNull();
    expect(retryAfterMs(inFlight({ detail: new Uint8Array([0xff, 0xff, 0xff]) }))).toBeNull();
    expect(retryAfterMs(new Error("boom"))).toBeNull();
  });

  it("caps a server's request at a minute", () => {
    expect(retryAfterMs(inFlight({ header: "86400" }))).toBe(60_000);
  });
});
