// Copyright (c) 2026 Reliant Labs

/**
 * Pure reading behind Usage & billing (UsageBillingPage.tsx): the range's
 * list-price total, the labels the page uses for the org usage categories,
 * one row per project from BillingService.GetProjectCost, and the period
 * the URL names.
 *
 * The rules of organizationUsageModel.ts hold here too: an absent amount is
 * "n/a", never $0, and settled charges (AI) and list-price estimates (hosting)
 * are never added into one figure.
 */

import type { ProjectCost } from "@/services/controlPlane/observability/organizationCostsApi";
import type { OrganizationUsage } from "@/services/controlPlane/observability/organizationUsageApi";
import type { ProjectCostAmount } from "@/gen/controlplane/services/billing/v1/billing_pb";

import {
  categoryLabel as orgCategoryLabel,
  formatUsd,
  isUsageRangeId,
  pointAmount,
  type UsageRangeId,
} from "@/components/Forge/Observability/Dashboards/organizationUsageModel";

/** The range a page opens on when the URL names none. */
export const DEFAULT_USAGE_RANGE: UsageRangeId = "30d";

/** The `?range=` search value as a range: anything unknown is the default. */
export function usageRangeOf(value: unknown): UsageRangeId {
  return isUsageRangeId(value) ? value : DEFAULT_USAGE_RANGE;
}

/** The org usage categories in this page's words: machines are Agent sandboxes. */
const CATEGORY_LABELS: Record<string, string> = {
  managed_llm: "AI (managed models)",
  machines: "Agent sandboxes",
};

export function usageCategoryLabel(category: string): string {
  return CATEGORY_LABELS[category] ?? orgCategoryLabel(category);
}

export interface ListTotal {
  nanos: bigint;
  /** Dimensions the plan has no rate for: when any, the total is a lower bound. */
  unpricedDimensions: string[];
}

/**
 * Every point priced at list across the response: the range's estimated
 * spend. Settled charges are another basis and are left out. Undefined when
 * no point carried a list price (unknown or unpriced, not zero).
 */
export function listEstimateTotal(usage: OrganizationUsage): ListTotal | undefined {
  let nanos: bigint | undefined;
  const unpriced = new Set<string>();
  for (const point of usage.points) {
    const amount = pointAmount(point);
    if (amount?.basis !== "list_equivalent") continue;
    nanos = (nanos ?? 0n) + amount.nanos;
    amount.unpricedDimensions.forEach((d) => unpriced.add(d));
  }
  return nanos === undefined ? undefined : { nanos, unpricedDimensions: [...unpriced].sort() };
}

/** GetProjectCost's categories in words (ProjectCostDay.category). */
const PROJECT_CATEGORY_LABELS: Record<string, string> = {
  compute: "Hosted compute",
  databases: "Databases",
  storage: "Database storage",
  bandwidth: "CDN and bandwidth",
  static_sites: "Static sites",
  builds: "Builds",
};

export function projectCategoryLabel(category: string): string {
  return PROJECT_CATEGORY_LABELS[category] ?? category.replace(/_/g, " ");
}

/** A project amount as text: "n/a" when not measured, "≥ $x" when a lower bound. */
export function projectAmountText(amount: ProjectCostAmount | undefined): string {
  if (amount?.usdNanos === undefined) return "n/a";
  return `${amount.lowerBound ? "≥ " : ""}${formatUsd(amount.usdNanos)}`;
}

export interface ProjectCostRow {
  name: string;
  monthToDate: ProjectCostAmount | undefined;
  forecast: ProjectCostAmount | undefined;
  /** The category carrying most of the month's list price, and its share (0..1). */
  top?: { category: string; share: number };
}

/** One project's line: month to date, forecast, and what costs it the most. */
export function projectCostRow(name: string, cost: ProjectCost): ProjectCostRow {
  const byCategory = new Map<string, bigint>();
  let total = 0n;
  for (const day of cost.daily) {
    const nanos = day.list?.usdNanos;
    if (nanos === undefined) continue;
    byCategory.set(day.category, (byCategory.get(day.category) ?? 0n) + nanos);
    total += nanos;
  }
  let top: ProjectCostRow["top"];
  for (const [category, nanos] of byCategory) {
    if (nanos <= 0n) continue;
    if (!top || nanos > byCategory.get(top.category)!) top = { category, share: total > 0n ? Number(nanos) / Number(total) : 0 };
  }
  return { name, monthToDate: cost.monthToDate, forecast: cost.forecast?.amount, top };
}
