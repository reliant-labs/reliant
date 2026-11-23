// Copyright (c) 2026 Reliant Labs

/**
 * Pure reading of GetProjectCost's daily rows for the app Usage page
 * (ProjectUsagePage.tsx): the days, one chart series per category, and the
 * month's split by environment and by category.
 *
 * The optional-money contract holds through every sum. A row's amount is
 * absent when no source measured it: absent is never $0. A group whose rows
 * were all unmeasured has no figure ("n/a"); a group with some measured and
 * some unmeasured rows, or a row that is itself a lower bound, sums to a
 * lower bound ("≥ $x"). An explicit $0 row is a real zero.
 */

import type { ProjectCostAmount, ProjectCostDay } from "@/gen/controlplane/services/billing/v1/billing_pb";

import { formatUsd } from "@/components/Forge/Observability/Dashboards/organizationUsageModel";

export interface AggregateAmount {
  /** The measured rows summed; undefined when no row of the group was measured. */
  nanos: bigint | undefined;
  /** The figure leaves out something that exists: render "≥ $x". */
  lowerBound: boolean;
}

interface Acc {
  nanos: bigint | undefined;
  unmeasured: boolean;
  lowerBound: boolean;
}

function add(acc: Acc | undefined, amount: ProjectCostAmount | undefined): Acc {
  const next: Acc = acc ? { ...acc } : { nanos: undefined, unmeasured: false, lowerBound: false };
  if (amount?.usdNanos === undefined) {
    next.unmeasured = true;
    return next;
  }
  next.nanos = (next.nanos ?? 0n) + amount.usdNanos;
  if (amount.lowerBound || amount.unpricedDimensions.length > 0) next.lowerBound = true;
  return next;
}

function settle(acc: Acc | undefined): AggregateAmount {
  if (!acc || acc.nanos === undefined) return { nanos: undefined, lowerBound: false };
  return { nanos: acc.nanos, lowerBound: acc.lowerBound || acc.unmeasured };
}

/** "n/a" when nothing was measured, "≥ $x" for a lower bound. */
export function aggregateAmountText(amount: AggregateAmount): string {
  if (amount.nanos === undefined) return "n/a";
  return `${amount.lowerBound ? "≥ " : ""}${formatUsd(amount.nanos)}`;
}

/** Larger first; unmeasured groups last. */
function byAmountDesc(a: [string, AggregateAmount], b: [string, AggregateAmount]): number {
  const x = a[1].nanos;
  const y = b[1].nanos;
  if (x === undefined || y === undefined) return x === undefined ? (y === undefined ? 0 : 1) : -1;
  return x > y ? -1 : x < y ? 1 : 0;
}

export interface CategorySeries {
  category: string;
  /**
   * Dollars per day. A day with no row for the category is no usage (0); a
   * day whose rows were all unmeasured is a gap (null), never a zero.
   */
  values: (number | null)[];
}

export interface ProjectDaily {
  /** Epoch seconds of every day that has a row, ascending. */
  days: number[];
  categories: CategorySeries[];
  byEnv: [string, AggregateAmount][];
  byCategory: [string, AggregateAmount][];
  /** Every row summed: undefined nanos when nothing was measured. */
  total: AggregateAmount;
}

export function projectDaily(daily: readonly ProjectCostDay[]): ProjectDaily {
  const daySeconds = (d: ProjectCostDay) => Number(d.day?.seconds ?? 0);
  const days = [...new Set(daily.map(daySeconds))].sort((a, b) => a - b);
  const categoryKeys = [...new Set(daily.map((d) => d.category))];
  const env = new Map<string, Acc>();
  const cat = new Map<string, Acc>();
  const cell = new Map<string, Acc>();
  let total: Acc | undefined;
  for (const d of daily) {
    env.set(d.environmentId, add(env.get(d.environmentId), d.list));
    cat.set(d.category, add(cat.get(d.category), d.list));
    const key = `${daySeconds(d)}|${d.category}`;
    cell.set(key, add(cell.get(key), d.list));
    total = add(total, d.list);
  }
  const cellValue = (key: string): number | null => {
    const acc = cell.get(key);
    if (!acc) return 0;
    return acc.nanos === undefined ? null : Number(acc.nanos) / 1e9;
  };
  return {
    days,
    categories: categoryKeys.map((category) => ({ category, values: days.map((t) => cellValue(`${t}|${category}`)) })),
    byEnv: [...env.entries()].map(([k, v]): [string, AggregateAmount] => [k, settle(v)]).sort(byAmountDesc),
    byCategory: [...cat.entries()].map(([k, v]): [string, AggregateAmount] => [k, settle(v)]).sort(byAmountDesc),
    total: settle(total),
  };
}
