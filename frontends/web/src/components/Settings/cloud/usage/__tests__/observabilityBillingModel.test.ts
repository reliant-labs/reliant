import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { describe, expect, it } from "vitest";
import { UsageBucketSchema, GetObservabilityUsageResponseSchema } from "@/gen/controlplane/services/observability_billing/v1/observability_billing_pb";
import { csvCell, usageCsv, dailyTotals, usageStateSentence } from "../observabilityBillingModel";

describe("observability billing export", () => {
  it.each(["=SUM(A1)", "+cmd", "-1+cmd", "@cmd", "\t=cmd", "  =cmd", "\rplain", "\nplain"])("neutralizes spreadsheet formulas: %s", value => {
    expect(csvCell(value)).toBe(`"'${value}"`);
  });
  it("quotes commas, quotes and multiline names without losing exact byte counts", () => {
    const bucket = create(UsageBucketSchema, { bucketStart: timestampFromDate(new Date("2026-10-02T00:00:00Z")), signal: "logs", service: 'api,"one"\nsecond', billedBytes: 9007199254740993n, unchargedBytes: 19n });
    const csv = usageCsv([bucket]);
    expect(csv).toContain('"api,""one""\nsecond"');
    expect(csv).toContain('"9007199254740993"');
    expect(csv).toContain('"19"');
    expect(csv).toContain('"2026-10-02"');
  });
  it("aggregates chart data without losing bigint precision", () => {
    const stamp = timestampFromDate(new Date("2026-10-02T00:00:00Z"));
    const rows = [create(UsageBucketSchema, { bucketStart: stamp, signal: "logs", billedBytes: 10n }), create(UsageBucketSchema, { bucketStart: stamp, signal: "logs", billedBytes: 20n }), create(UsageBucketSchema, { bucketStart: stamp, signal: "traces", billedBytes: 4n })];
    expect(dailyTotals(rows)).toEqual([{ date: "2026-10-02", signals: [["logs", 30n], ["traces", 4n]], total: 34n }]);
  });
  it("does not turn an unknown collection state into a healthy claim", () => {
    expect(usageStateSentence(create(GetObservabilityUsageResponseSchema))).toContain("unavailable");
  });
  it("names the cap and its billing consequence", () => {
    expect(usageStateSentence(create(GetObservabilityUsageResponseSchema, { spendCapReached: true, spendCapCents: 5000n }))).toContain("Nothing above the cap is charged");
  });
});
