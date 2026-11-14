/**
 * The draft rules the Policies page relies on: what counts as a change,
 * how an edit is reapplied onto a teammate's newer version, money in nanos,
 * and the history's values in words.
 */
import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import {
  ModelsPolicySchema,
  OrgPolicyMode,
  OrgPolicySchema,
  SpendCapSchema,
  SpendPeriod,
  SpendPolicySchema,
  McpPolicySchema,
} from "@/gen/controlplane/controlplane/v1/org_policy_pb";

import {
  changedSections,
  copyPolicy,
  describeHistoryField,
  formatHistoryValue,
  formatUsdNanos,
  parseUsd,
  rebasePolicy,
  toggled,
  usdInputValue,
} from "../policyDocument";

const models = (allowed: string[], mode = OrgPolicyMode.AUDIT) => create(ModelsPolicySchema, { mode, allowed });
const spend = (usd: bigint) =>
  create(SpendPolicySchema, { mode: OrgPolicyMode.AUDIT, memberDefaultCap: create(SpendCapSchema, { usdNanos: usd * 1_000_000_000n, period: SpendPeriod.MONTH }) });

describe("changedSections", () => {
  it("names only the sections that differ, in page order", () => {
    const base = create(OrgPolicySchema, { models: models(["a"]) });
    const draft = copyPolicy(base);
    expect(changedSections(base, draft)).toEqual([]);
    draft.spend = spend(10n);
    draft.models = models(["a", "b"]);
    expect(changedSections(base, draft)).toEqual(["models", "spend"]);
  });

  it("treats a removed section as a change and no policy as an empty one", () => {
    const base = create(OrgPolicySchema, { mcp: create(McpPolicySchema, { mode: OrgPolicyMode.AUDIT, allowAll: true }) });
    expect(changedSections(base, create(OrgPolicySchema))).toEqual(["mcp"]);
    expect(changedSections(undefined, create(OrgPolicySchema))).toEqual([]);
  });

  it("never edits the policy it copied", () => {
    const base = create(OrgPolicySchema, { models: models(["a"]) });
    const draft = copyPolicy(base);
    draft.models!.allowed.push("b");
    expect(base.models!.allowed).toEqual(["a"]);
  });
});

describe("rebasePolicy", () => {
  it("puts my changed sections on their version and keeps the rest of theirs", () => {
    const base = create(OrgPolicySchema, { models: models(["a"]), spend: spend(10n) });
    const mine = copyPolicy(base);
    mine.models = models(["a", "b"]);
    const theirs = copyPolicy(base);
    theirs.spend = spend(20n);

    const { policy, overlapping } = rebasePolicy(base, mine, theirs);
    expect(policy.models!.allowed).toEqual(["a", "b"]);
    expect(policy.spend!.memberDefaultCap!.usdNanos).toBe(20_000_000_000n);
    expect(overlapping).toEqual([]);
  });

  it("names a section both people changed, where mine replaces theirs", () => {
    const base = create(OrgPolicySchema, { spend: spend(10n) });
    const mine = copyPolicy(base);
    mine.spend = spend(30n);
    const theirs = copyPolicy(base);
    theirs.spend = spend(20n);

    const { policy, overlapping } = rebasePolicy(base, mine, theirs);
    expect(policy.spend!.memberDefaultCap!.usdNanos).toBe(30_000_000_000n);
    expect(overlapping).toEqual(["spend"]);
  });

  it("carries a section I removed onto their version", () => {
    const base = create(OrgPolicySchema, { models: models(["a"]), spend: spend(10n) });
    const mine = copyPolicy(base);
    mine.models = undefined;
    const theirs = create(OrgPolicySchema, { models: models(["a"]), spend: spend(10n), mcp: create(McpPolicySchema, { mode: OrgPolicyMode.ENFORCE, allowAll: true }) });

    const { policy } = rebasePolicy(base, mine, theirs);
    expect(policy.models).toBeUndefined();
    expect(policy.mcp?.allowAll).toBe(true);
  });
});

describe("toggled", () => {
  it("keeps the list sorted and free of duplicates, so off-then-on is no change", () => {
    expect(toggled(["b"], "a", true)).toEqual(["a", "b"]);
    expect(toggled(["a", "b"], "a", true)).toEqual(["a", "b"]);
    expect(toggled(["a", "b"], "a", false)).toEqual(["b"]);
    expect(toggled([3, 1], 2, true)).toEqual([1, 2, 3]);
  });
});

describe("money", () => {
  it("reads typed dollars into nanos and refuses what is not an amount", () => {
    expect(parseUsd("50")).toBe(50_000_000_000n);
    expect(parseUsd("$1,000.5")).toBe(1_000_500_000_000n);
    expect(parseUsd("0")).toBe(0n);
    expect(parseUsd("12.345")).toBeNull();
    expect(parseUsd("-3")).toBeNull();
    expect(parseUsd("")).toBeNull();
  });

  it("shows nanos as dollars", () => {
    expect(formatUsdNanos(50_000_000_000n)).toBe("$50");
    expect(formatUsdNanos(12_500_000_000n)).toBe("$12.50");
    expect(usdInputValue(12_500_000_000n)).toBe("12.50");
    expect(usdInputValue(7_000_000_000n)).toBe("7");
  });
});

describe("history values", () => {
  it("says what a field is and what it became, in words", () => {
    const nameOf = (id: string) => (id === "u1" ? "Ada" : id);
    expect(describeHistoryField("spend", "member_default_cap", nameOf)).toBe("Spend limits › limit per member");
    expect(describeHistoryField("spend", "member_overrides.u1", nameOf)).toBe("Spend limits › limit for Ada");
    expect(describeHistoryField("models", "mode", nameOf)).toBe("Models › mode");

    expect(formatHistoryValue("")).toBe("not set");
    expect(formatHistoryValue("null")).toBe("not set");
    expect(formatHistoryValue('"ORG_POLICY_MODE_ENFORCE"')).toBe("enforce");
    expect(formatHistoryValue('{"usd_nanos":"50000000000","period":"SPEND_PERIOD_MONTH"}')).toBe("$50 a month");
    expect(formatHistoryValue('{"unlimited":true}')).toBe("no limit");
    expect(formatHistoryValue('["claude","reliant"]')).toBe("Claude subscription, Reliant");
    expect(formatHistoryValue("[]")).toBe("none");
    expect(formatHistoryValue("true")).toBe("on");
    expect(formatHistoryValue("not json")).toBe("not json");
  });
});
