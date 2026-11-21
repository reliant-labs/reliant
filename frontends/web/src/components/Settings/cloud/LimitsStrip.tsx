// Copyright (c) 2026 Reliant Labs

/**
 * Every spending limit, one row each: what it limits, whose it is, where it
 * stands, and where to change it.
 *
 * ── FOUR LIMITS, FOUR OWNERS, NO TOTAL ─────────────────────────────────────
 *
 * The hosting budget and the monitoring cap belong to the ORGANIZATION (any
 * owner or admin sets them, and they bound the org's apps). The machine
 * overage cap and the AI auto top-up ceiling belong to the signed-in PERSON
 * (their subscription, their wallet). They gate different things on different
 * schedules, so summing them would produce a number nothing enforces. Each
 * row therefore names its product and its scope, and there is no combined
 * figure.
 *
 * Each row reads its own source and degrades on its own: one unavailable read
 * says "Unavailable" in its row and never hides the others.
 */

import type { ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { ArrowRight } from "lucide-react";

import { BillingService } from "@/gen/controlplane/services/billing/v1/billing_pb";
import { useActiveOrg } from "@/hooks/useOrgs";
import { useComputeSubscription, useWalletAutoRecharge } from "@/hooks/useCloudBillingQueries";
import { useObsBillingLimits } from "@/hooks/observability-billing-queries";
import { getControlPlaneClient } from "@/services/controlPlane/client";
import { formatCents } from "./usage/usageModel";
import { hostingBillingKeys, isUnimplemented } from "./usage/hostingBillingQueries";
import type { VisibleBillingTab } from "@/routeSchemas";

export interface LimitRow {
  id: "hosting" | "monitoring" | "machine" | "ai";
  product: string;
  /** "Organization · <name>" or "You". */
  scope: string;
  value: string;
  detail?: string;
  /** Where the control lives. */
  target: { tab: VisibleBillingTab; hash?: string };
}

const cents = (value: bigint | number) => formatCents(Number(value));

/** The hosting budget row, from GetCurrentUserUsageBilling (the same read the consent card makes). */
export function hostingLimit(read: {
  data?: { enabled: boolean; agreement?: { status: string; infraBudgetCents: bigint } };
  error: unknown;
}): Pick<LimitRow, "value" | "detail"> {
  if (isUnimplemented(read.error) || (read.data && !read.data.enabled)) return { value: "Not available yet" };
  if (read.error) return { value: "Unavailable" };
  if (!read.data) return { value: "Loading…" };
  const agreement = read.data.agreement;
  if (!agreement) return { value: "Not set up", detail: "Free static site only" };
  if (agreement.status === "active") return { value: `${cents(agreement.infraBudgetCents)} / month`, detail: "New deploys pause at the budget" };
  if (agreement.status === "pending") return { value: "Checkout pending" };
  return { value: "Not active", detail: agreement.status };
}

/** The monitoring row: 0 is "no overage", spend stops at the free allowance. */
export function monitoringLimit(read: { data?: { spendCapCents: bigint }; error: unknown }): Pick<LimitRow, "value" | "detail"> {
  if (isUnimplemented(read.error)) return { value: "Not available yet" };
  if (read.error) return { value: "Unavailable" };
  if (!read.data) return { value: "Loading…" };
  return read.data.spendCapCents === 0n
    ? { value: "No overage", detail: "Kept within the free allowance" }
    : { value: `${cents(read.data.spendCapCents)} / month`, detail: "Collection narrows at the cap" };
}

/** The machine row: compute overage, where a 0 or absent cap is uncapped. */
export function machineLimit(read: {
  data?: { subscription?: { overageEnabled: boolean; budgetCents?: bigint } };
  error: unknown;
}): Pick<LimitRow, "value" | "detail"> {
  if (read.error) return { value: "Unavailable" };
  if (!read.data) return { value: "Loading…" };
  const sub = read.data.subscription;
  if (!sub) return { value: "No machine plan" };
  if (!sub.overageEnabled) return { value: "Stops at included hours" };
  return sub.budgetCents !== undefined && sub.budgetCents > 0n
    ? { value: `${cents(sub.budgetCents)} / month`, detail: "Extra machine hours" }
    : { value: "No ceiling", detail: "Extra machine hours are uncapped" };
}

/** The AI row: auto top-up's mandatory monthly ceiling. Off means spend stops at your credit. */
export function aiLimit(read: {
  data?: { autoRecharge?: { enabled: boolean; maxPerMonthCents: bigint } };
  error: unknown;
}): Pick<LimitRow, "value" | "detail"> {
  if (read.error) return { value: "Unavailable" };
  if (!read.data) return { value: "Loading…" };
  const rule = read.data.autoRecharge;
  return rule?.enabled
    ? { value: `${cents(rule.maxPerMonthCents)} / month`, detail: "Automatic top-ups" }
    : { value: "Auto top-up off", detail: "Spend stops at your credit" };
}

export function LimitsStrip({ onOpen }: { onOpen: (target: LimitRow["target"]) => void }) {
  const org = useActiveOrg();
  const orgId = org.activeOrgId ?? "";
  const orgScope = `Organization · ${org.active?.name || "this organization"}`;

  const hosting = useQuery({
    queryKey: hostingBillingKeys.agreement(orgId),
    queryFn: () => getControlPlaneClient(BillingService).getCurrentUserUsageBilling({ orgId }),
    enabled: !!orgId,
    staleTime: 10_000,
    retry: (failures, error) => !isUnimplemented(error) && failures < 2,
  });
  const monitoring = useObsBillingLimits(orgId);
  const machine = useComputeSubscription();
  const ai = useWalletAutoRecharge();

  const rows: LimitRow[] = [
    { id: "hosting", product: "App hosting budget", scope: orgScope, ...hostingLimit(hosting), target: { tab: "hosting", hash: "app-hosting" } },
    { id: "monitoring", product: "Monitoring cap", scope: orgScope, ...monitoringLimit(monitoring), target: { tab: "usage", hash: "observability" } },
    { id: "machine", product: "Machine overage cap", scope: "You", ...machineLimit(machine), target: { tab: "overview" } },
    { id: "ai", product: "AI auto top-up ceiling", scope: "You", ...aiLimit(ai), target: { tab: "overview" } },
  ];

  return (
    <section aria-labelledby="billing-limits-title" className="rounded-xl border border-border bg-card" data-testid="billing-limits-strip">
      <div className="px-4 pt-3">
        <h3 id="billing-limits-title" className="text-ui font-medium text-foreground">
          Spending limits
        </h3>
        <p className="text-meta text-muted-foreground">Each limit is separate: it gates its own product, for its own owner.</p>
      </div>
      <ul className="mt-2 divide-y divide-border-subtle">
        {rows.map((row) => (
          <LimitItem key={row.id} row={row} onOpen={() => onOpen(row.target)} />
        ))}
      </ul>
    </section>
  );
}

function LimitItem({ row, onOpen }: { row: LimitRow; onOpen: () => void }): ReactNode {
  return (
    <li data-testid={`billing-limit-${row.id}`}>
      <button
        type="button"
        onClick={onOpen}
        className="grid w-full grid-cols-[1fr_auto_auto] items-center gap-x-4 px-4 py-2.5 text-left hover:bg-foreground/5 focus-visible:ring-2 focus-visible:ring-ring"
      >
        <span className="min-w-0">
          <span className="block text-ui text-foreground">{row.product}</span>
          <span className="block text-meta text-muted-foreground">{row.scope}</span>
        </span>
        <span className="text-right">
          <span className="block text-ui tabular-nums text-foreground">{row.value}</span>
          {row.detail && <span className="block text-meta text-muted-foreground">{row.detail}</span>}
        </span>
        <ArrowRight className="h-3.5 w-3.5 text-muted-foreground" aria-hidden="true" />
      </button>
    </li>
  );
}
