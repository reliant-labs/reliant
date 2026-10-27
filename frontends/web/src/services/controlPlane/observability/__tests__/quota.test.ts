import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";
import { timestampFromMs } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError } from "@connectrpc/connect";

import { QueryQuotaExhaustedSchema, QueryQuotaLimit } from "@/gen/controlplane/services/observability/v1/observability_pb";

import { obsErrorKind } from "../errors";
import { keepsLastResult, nextQuotaResetMs, quotaOf, quotaPausedError } from "../quota";

const REASON = "x-forge-error-reason";
const NOW = Date.UTC(2026, 9, 26, 17, 51, 27);
const TOP_OF_HOUR = Date.UTC(2026, 9, 26, 18, 0, 0);

function quotaError(opts: { reason?: string; limit?: QueryQuotaLimit; lane?: string; resetsAtMs?: number; retryAfter?: string } = {}) {
  const meta: Record<string, string> = { [REASON]: opts.reason ?? "obs_query_quota" };
  if (opts.retryAfter) meta["retry-after"] = opts.retryAfter;
  return new ConnectError(
    "this organization's hourly observability query quota is used up",
    Code.ResourceExhausted,
    meta,
    [
      {
        desc: QueryQuotaExhaustedSchema,
        value: create(QueryQuotaExhaustedSchema, {
          limit: opts.limit ?? QueryQuotaLimit.QUERIES,
          lane: opts.lane ?? "interactive",
          resetsAt: timestampFromMs(opts.resetsAtMs ?? TOP_OF_HOUR),
        }),
      },
    ]
  );
}

describe("quotaOf", () => {
  it("reads the limit, lane and reset out of the typed detail", () => {
    expect(quotaOf(quotaError({ limit: QueryQuotaLimit.READ_BYTES, lane: "agent" }), NOW)).toEqual({
      limit: "read_bytes",
      lane: "agent",
      resetsAtMs: TOP_OF_HOUR,
      soft: false,
    });
    expect(quotaOf(quotaError({ limit: QueryQuotaLimit.EXECUTION_TIME }), NOW)?.limit).toBe("execution_time");
    expect(quotaOf(quotaError({ limit: QueryQuotaLimit.UNSPECIFIED }), NOW)?.limit).toBe("unknown");
  });

  it("marks a poll refused early as soft", () => {
    expect(quotaOf(quotaError({ reason: "obs_query_quota_soft" }), NOW)?.soft).toBe(true);
  });

  it("falls back to the top of the hour for a control plane without the detail", () => {
    const bare = (meta: Record<string, string>, message = "this organization's hourly observability query quota is used up") =>
      new ConnectError(message, Code.ResourceExhausted, meta);
    expect(quotaOf(bare({ [REASON]: "obs_query_quota" }), NOW)?.resetsAtMs).toBe(TOP_OF_HOUR);
    // Today's servers: only the message.
    expect(quotaOf(bare({}), NOW)?.resetsAtMs).toBe(TOP_OF_HOUR);
  });

  it("is null for every other failure, including other ResourceExhausted answers", () => {
    expect(quotaOf(new ConnectError("too many observability queries in flight; retry shortly", Code.ResourceExhausted), NOW)).toBeNull();
    expect(quotaOf(new ConnectError("the query touched too much data", Code.ResourceExhausted), NOW)).toBeNull();
    expect(quotaOf(new ConnectError("quota", Code.Unavailable), NOW)).toBeNull();
    expect(quotaOf(new Error("boom"), NOW)).toBeNull();
    expect(quotaOf(undefined, NOW)).toBeNull();
  });

  it("round-trips the error the client synthesizes while the quota is known to be out", () => {
    const synthesized = quotaPausedError({ limit: "queries", lane: "interactive", resetsAtMs: TOP_OF_HOUR, soft: false });
    expect(quotaOf(synthesized, NOW)).toEqual({ limit: "queries", lane: "interactive", resetsAtMs: TOP_OF_HOUR, soft: false });
    expect(obsErrorKind(synthesized)).toBe("over-quota");
  });
});

describe("obsErrorKind", () => {
  it("separates the hourly allowance (over-quota) from the other refusals (busy)", () => {
    expect(obsErrorKind(quotaError())).toBe("over-quota");
    expect(obsErrorKind(new ConnectError("too many observability queries in flight", Code.ResourceExhausted))).toBe("busy");
  });
});

describe("keepsLastResult", () => {
  it("holds the result only for a quota error on a query that has data", () => {
    expect(keepsLastResult(quotaError(), { rows: [] })).toBe(true);
    expect(keepsLastResult(quotaError(), undefined)).toBe(false);
    expect(keepsLastResult(new ConnectError("x", Code.Unavailable), { rows: [] })).toBe(false);
  });
});

describe("nextQuotaResetMs", () => {
  it("is the top of the next clock hour", () => {
    expect(nextQuotaResetMs(NOW)).toBe(TOP_OF_HOUR);
    expect(nextQuotaResetMs(TOP_OF_HOUR)).toBe(TOP_OF_HOUR + 3_600_000);
  });
});
