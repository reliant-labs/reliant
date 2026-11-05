// Copyright (c) 2025 Reliant Labs

/**
 * The forge shell's view of the organization's spend: hosting and the
 * infrastructure forge provisions, never Reliant's managed LLM.
 *
 * The billing RPCs (ListOrganizationUsage, GetOrganizationCostSummary) answer
 * for the whole organization. There is no server-side category filter, so
 * the forge view narrows the answers here, on the client, before any panel
 * sees them: a panel that sums "everything it was given" then sums only
 * forge's categories, and no LLM figure can leak into a total.
 */

import type { OrganizationCostSummary } from "@/services/controlPlane/observability/organizationCostsApi";
import type { OrganizationUsage } from "@/services/controlPlane/observability/organizationUsageApi";

/**
 * Usage categories forge accounts for (organizationUsageModel USAGE_CATEGORIES
 * minus managed_llm): machines, hosted apps (compute), managed databases,
 * static hosting (storage and egress), builds and registry, observability.
 */
export const FORGE_USAGE_CATEGORIES = [
  "hosted_apps",
  "static_hosting",
  "databases",
  "machines",
  "hosted_other",
  "observability",
] as const;

/** Cost-summary sources forge accounts for (OrganizationCostSource.category). */
export const FORGE_COST_SOURCES = ["hosted_infrastructure", "machines", "observability", "inventory"] as const;

const USAGE = new Set<string>(FORGE_USAGE_CATEGORIES);
const SOURCES = new Set<string>(FORGE_COST_SOURCES);

/** True for a usage category the forge view shows. */
export function isForgeUsageCategory(category: string): boolean {
  return USAGE.has(category);
}

/**
 * The history narrowed to forge's categories: points, coverage, spend rates
 * and inventory. `completeness` is left as the server said it: it describes
 * the read, and the forge categories are a subset of what it covered.
 */
export function forgeScopedUsage(usage: OrganizationUsage): OrganizationUsage {
  return {
    ...usage,
    points: usage.points.filter((p) => USAGE.has(p.category)),
    coverage: usage.coverage.filter((c) => USAGE.has(c.category)),
    spendRates: usage.spendRates.filter((r) => USAGE.has(r.category)),
    inventory: usage.inventory.filter((i) => USAGE.has(i.category)),
  };
}

/** The cost summary narrowed to forge's sources (allowances and credit per source). */
export function forgeScopedCostSummary(summary: OrganizationCostSummary): OrganizationCostSummary {
  return { ...summary, sources: summary.sources.filter((s) => SOURCES.has(s.category)) };
}

/** One row of provisioned infrastructure, from the history's inventory. */
export interface ProvisionedRow {
  category: string;
  /** Machine size, or the resource's size class. */
  size: string;
  state: string;
  environment: string;
  count: number;
  storageGib: number;
}

/** What is provisioned now, largest first. The inventory is per size and state, not per machine. */
export function provisionedRows(usage: OrganizationUsage): ProvisionedRow[] {
  return usage.inventory
    .filter((i) => USAGE.has(i.category))
    .map((i) => ({
      category: i.category,
      size: i.size,
      state: i.state,
      environment: i.environmentName || i.environmentId || "",
      count: Number(i.count),
      storageGib: Number(i.storageGib),
    }))
    .sort((a, b) => b.count - a.count || a.category.localeCompare(b.category) || a.size.localeCompare(b.size));
}
