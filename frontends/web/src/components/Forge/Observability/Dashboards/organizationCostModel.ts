// Copyright (c) 2026 Reliant Labs

import type { OrganizationCostSummary } from "@/services/controlPlane/observability/organizationCostsApi";

export type CostSource = OrganizationCostSummary["sources"][number];
export type CostPeriod = OrganizationCostSummary["periods"][number];

/** Preserve the signed nanos exactly: Number(int64) silently loses precision. */
export function formatCostUSD(nanos: bigint | undefined): string {
  if (nanos === undefined) return "Not reported";
  const magnitude = nanos < 0n ? -nanos : nanos;
  const dollars = (magnitude / 1_000_000_000n).toLocaleString("en-US");
  const fraction = (magnitude % 1_000_000_000n).toString().padStart(9, "0").replace(/0+$/, "").padEnd(2, "0");
  return `USD ${nanos < 0n ? "-" : ""}${dollars}.${fraction}`;
}

export function costTimestamp(timestamp: { seconds: bigint; nanos: number } | undefined): string {
  if (!timestamp) return "Not reported";
  const date = new Date(Number(timestamp.seconds) * 1000 + timestamp.nanos / 1_000_000);
  if (!Number.isFinite(date.getTime())) return "Not reported";
  return date.toISOString().replace("T", " ").replace(/\.\d{3}Z$/, " UTC");
}

export function costPeriodLabel(id: string): string {
  if (id === "month_to_date") return "Month to date";
  if (id === "compute_billing_period") return "Compute billing period";
  return id || "Unspecified period";
}

export function costSourceLabel(category: string): string {
  return ({ managed_llm: "Managed LLM", byok: "BYOK", hosted_infrastructure: "Hosted infrastructure", machines: "Machines", static_hosting: "Static hosting", observability: "Observability" } as Record<string, string>)[category] ?? (category || "Unspecified source");
}

export function costCompletenessLabel(value: string): string {
  return ({ complete: "Complete", partial: "Partial coverage", unavailable: "Unavailable", not_measured: "Not measured" } as Record<string, string>)[value] ?? "Coverage not reported";
}

/** Group, never sum. Also retain sources whose period metadata is missing. */
export function organizationCostGroups(summary: OrganizationCostSummary) {
  const ids = [...new Set([...summary.periods.map((period) => period.id), ...summary.sources.map((source) => source.periodId)])];
  return ids.map((id) => ({ id, period: summary.periods.find((period) => period.id === id), sources: summary.sources.filter((source) => source.periodId === id) }));
}
