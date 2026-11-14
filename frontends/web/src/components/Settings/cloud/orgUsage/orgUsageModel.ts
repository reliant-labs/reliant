// Copyright (c) 2026 Reliant Labs

/**
 * The org AI usage view's pure parts: the ranges it offers, how a bucket is
 * named and how an estimated cost reads. Everything a test pins without
 * rendering lives here.
 *
 * An estimated cost is never shown as a charge: it is "~$4.50", labelled an
 * estimate wherever it appears, and absent (never "$0.00") when nothing in
 * the bucket could be priced.
 */

import { Code, ConnectError } from "@connectrpc/connect";

import { hasErrorReason } from "@/lib/errorReasons";
import { OrgLLMUsageGroupBy, type OrgLLMUsageBucket } from "@/gen/reliant/v1/org_usage_pb";

export type UsageRangeId = "month" | "30d" | "90d";

export const USAGE_RANGES: { id: UsageRangeId; label: string }[] = [
  { id: "month", label: "This month" },
  { id: "30d", label: "Last 30 days" },
  { id: "90d", label: "Last 90 days" },
];

export type UsageGroupId = "member" | "account" | "model";

export const USAGE_GROUPS: { id: UsageGroupId; label: string; groupBy: OrgLLMUsageGroupBy; column: string }[] = [
  { id: "member", label: "By member", groupBy: OrgLLMUsageGroupBy.ORG_LLM_USAGE_GROUP_BY_MEMBER, column: "Member" },
  { id: "account", label: "By account", groupBy: OrgLLMUsageGroupBy.ORG_LLM_USAGE_GROUP_BY_ACCOUNT, column: "Account" },
  { id: "model", label: "By model", groupBy: OrgLLMUsageGroupBy.ORG_LLM_USAGE_GROUP_BY_MODEL, column: "Model" },
];

const ymd = (d: Date) => d.toISOString().slice(0, 10);

/** The UTC days a range covers, both inclusive, ending today. */
export function rangeDates(id: UsageRangeId, now: Date = new Date()): { startDate: string; endDate: string } {
  const end = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate()));
  let start: Date;
  if (id === "month") {
    start = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), 1));
  } else {
    const days = id === "30d" ? 30 : 90;
    start = new Date(end.getTime() - (days - 1) * 86_400_000);
  }
  return { startDate: ymd(start), endDate: ymd(end) };
}

/** What the funding keys mean, for a person. */
const FUNDING_LABELS: Record<string, string> = {
  org_key: "Organization API key",
  org_seat: "Seat paid by the organization",
  org_wallet: "Reliant credits",
};

/**
 * The name of a bucket: a member's name from the org roster, an account's
 * label, a model id. A member who left (no roster entry) and an account since
 * deleted keep a plain description rather than a raw id.
 */
export function bucketName(group: UsageGroupId, b: Pick<OrgLLMUsageBucket, "key" | "label">, memberNames: Map<string, string>): string {
  switch (group) {
    case "member":
      return memberNames.get(b.key) ?? "Former member";
    case "account":
      if (b.label) return b.label;
      return b.key ? "Removed account" : "Account not recorded";
    default:
      return b.key || "Unknown model";
  }
}

export function fundingLabel(key: string): string {
  return FUNDING_LABELS[key] ?? key;
}

const tokenFormat = new Intl.NumberFormat("en-US", { notation: "compact", maximumFractionDigits: 1 });

/** 1234567 → "1.2M". */
export function formatTokens(n: bigint | number): string {
  return tokenFormat.format(Number(n));
}

/**
 * An estimated list cost: "~$4.50" (or "<$0.01"), or null when the bucket has
 * no priced call, so the caller shows "—", never "$0.00".
 */
export function formatEstimate(usdNanos: bigint | undefined): string | null {
  if (usdNanos === undefined) return null;
  const usd = Number(usdNanos) / 1e9;
  if (usd > 0 && usd < 0.01) return "<$0.01";
  return `~${usd.toLocaleString("en-US", { style: "currency", currency: "USD", minimumFractionDigits: 2, maximumFractionDigits: 2 })}`;
}

/** Why the read failed, in the user's words. */
export function usageErrorMessage(err: unknown, orgName: string): string {
  if (hasErrorReason(err, "missing_scope")) {
    return `Seeing ${orgName}'s AI usage needs the billing:read permission in it. Ask an owner of ${orgName} to grant it on the Members page.`;
  }
  if (hasErrorReason(err, "not_org_member")) {
    return `You're not a member of ${orgName}.`;
  }
  if (err instanceof ConnectError && err.code === Code.Unimplemented) {
    return "Organization AI usage isn't available on this server yet.";
  }
  return `${orgName}'s AI usage could not be loaded.`;
}

/** Whether the error is the caller not holding the grant: shown as a note, not an alert. */
export function isPermissionError(err: unknown): boolean {
  return hasErrorReason(err, ["missing_scope", "not_org_member"]);
}
