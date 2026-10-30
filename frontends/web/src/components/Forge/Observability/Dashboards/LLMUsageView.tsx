// Copyright (c) 2026 Reliant Labs

/**
 * Dashboard 2, "LLM usage" (`org-llm`): every model call, whichever
 * credential made it.
 *
 * Two scopes, kept apart rather than merged into one chart:
 *
 *   YOUR OWN USAGE (reliant `UsageService.GetLLMUsage`, caller-scoped):
 *   tokens by access mode, calls and errors, per-model counts and cache hits,
 *   across Reliant AI, your API keys, subscriptions and local models, with
 *   the server's own completeness and retention note. A server that does not
 *   serve the RPC gets a "not available here" state that draws nothing.
 *   Organization admins never see members' own-key or subscription usage.
 *
 *   THE ORGANIZATION'S RELIANT AI SPEND (control-plane
 *   `ListOrganizationUsage`, category managed_llm): settled charges by model
 *   and month-to-date. It needs billing:read; without it only those panels
 *   say so, because the rest of the dashboard is the caller's own.
 *
 * Subscription windows come from the caller's connected provider accounts.
 */

import { useMemo, useState, type ReactNode } from "react";

import ProgressBar from "@/components/forge-ui/progress_bar";
import { useOwnLLMUsage } from "@/hooks/llm-usage-queries";
import { useOrganizationUsage } from "@/hooks/organization-usage-queries";
import { useProviderAccountsView } from "@/hooks/provider-account-queries";
import type { OrganizationUsage } from "@/services/controlPlane/observability/organizationUsageApi";
import type { ProviderAccount } from "@/types/providerAccount";

import { effectiveWindow, formatResetsIn, windowShortLabel } from "@/components/Settings/providers/accountFormat";
import { formatCount, formatDuration, formatPercent } from "../format";
import { AUTH_MODE_LABELS, cacheHitRatio, callsAndErrors, dayStarts, errorRate, modeTotals, tokensByAuthMode, type LLMUsage, type OwnLLMUsage } from "./llmUsageModel";
import { categoryAmount, coverageOf, groupTotals, monthToDateRequest, usageWindow, type UsageRangeId } from "./organizationUsageModel";
import {
  AmountText,
  amountCaption,
  BasisTag,
  CompletenessBadge,
  Money,
  PurposeTable,
  ElsewhereNote,
  isPermissionDenied,
  Loaded,
  RangePicker,
  RefreshButton,
  RowsTable,
  StatTile,
  UsageChart,
  UsagePanel,
  UsageViewHeader,
} from "./usageParts";

export interface LLMUsageViewProps {
  orgId: string;
  /** The clock, for tests and previews. */
  nowMs?: number;
}

const BILLING_SETTINGS = "/settings/billing";

export function LLMUsageView({ orgId, nowMs: fixedNow }: LLMUsageViewProps) {
  const [range, setRange] = useState<UsageRangeId>("30d");
  const [clock, setClock] = useState(() => fixedNow ?? Date.now());
  const nowMs = fixedNow ?? clock;
  const span = useMemo(() => usageWindow(range, nowMs), [range, nowMs]);
  const own = useOwnLLMUsage({ startMs: span.startMs, endMs: span.endMs });
  const byModelOrg = useOrganizationUsage(orgId ? { orgId, ...span, groupBy: "model" } : null);
  const mtd = useOrganizationUsage(orgId ? monthToDateRequest(orgId, nowMs) : null);
  const accounts = useProviderAccountsView();
  const managedDenied = isPermissionDenied(byModelOrg.error) || isPermissionDenied(mtd.error);
  const refresh = () => {
    if (fixedNow === undefined) setClock(Date.now());
    void byModelOrg.refetch();
    void mtd.refetch();
    own.refresh();
  };

  return (
    <section className="min-w-0 space-y-4 p-4 text-ink sm:p-6" aria-labelledby="llm-usage-title" data-testid="llm-usage-view">
      <UsageViewHeader
        id="llm-usage-title"
        title="LLM usage"
        scope="Your own model calls across every access mode · Reliant AI spend for the organization · UTC"
        controls={
          <>
            <RangePicker value={range} onChange={setRange} />
            <RefreshButton onClick={refresh} busy={byModelOrg.isFetching || mtd.isFetching || own.fetching} />
          </>
        }
      />
      {own.status === "not-deployed" && (
        <p role="status" className="rounded-md border border-border/60 bg-surface-sunken p-3 text-sm text-ink-muted" data-testid="llm-usage-coming-soon">
          Your own usage by model and access mode (your API keys, subscriptions, local models and Reliant AI) is not available from this server yet, so those panels are
          empty rather than estimated. Reliant AI spend and subscription windows below are live.
        </p>
      )}
      {own.status === "ready" && <OwnCoverage usage={own.usage} />}
      <div className="grid min-w-0 gap-4 lg:grid-cols-2">
        <UsagePanel title="Tokens per day by access mode" description="Every token of your calls (input, output and cached), stacked." testId="llm-tokens-by-mode">
          <OwnBody own={own}>{(u) => <TokensByMode usage={u} />}</OwnBody>
        </UsagePanel>
        <UsagePanel title="Calls and errors per day" description="Your calls, and how many failed." testId="llm-calls-errors">
          <OwnBody own={own}>{(u) => <CallsAndErrors usage={u} />}</OwnBody>
        </UsagePanel>
      </div>
      <UsagePanel title="Calls by purpose" description="Why your calls were made: chat turns, context compaction, chat titles and automation drafts." testId="llm-by-purpose">
        <OwnBody own={own}>{(u) => <PurposeTable buckets={u.byPurpose} />}</OwnBody>
      </UsagePanel>
      <UsagePanel title="By model" description="Your calls and tokens per model. Your keys and subscriptions are billed by your provider; list-price estimates are not available yet." testId="llm-by-model">
        <OwnBody own={own}>{(u) => <ByModel usage={u} />}</OwnBody>
      </UsagePanel>
      <div className="grid min-w-0 gap-4 lg:grid-cols-2">
        <UsagePanel title="Reliant AI spend by model" description="Settled charges for the whole organization over the selected range." testId="llm-managed-by-model" actions={<BasisTag basis="actual" />}>
          {managedDenied ? (
            <ManagedDenied />
          ) : (
            <Loaded query={byModelOrg} orgId={orgId} what="Reliant AI spend">
              {(u) => <ManagedByModel usage={u} />}
            </Loaded>
          )}
        </UsagePanel>
        <UsagePanel title="Reliant AI spend against your cap" description="Organization month to date (UTC)." testId="llm-managed-cap">
          {managedDenied ? (
            <ManagedDenied />
          ) : (
            <Loaded query={mtd} orgId={orgId} what="Reliant AI spend">
              {(u) => <ManagedMonthToDate usage={u} />}
            </Loaded>
          )}
        </UsagePanel>
      </div>
      <div className="grid min-w-0 gap-4 lg:grid-cols-2">
        <UsagePanel title="Subscription windows" description="Current utilisation and reset of each connected subscription, as the provider reports it." testId="llm-subscription-windows">
          <SubscriptionWindows view={accounts} nowMs={nowMs} />
        </UsagePanel>
        <UsagePanel title="Cache hit ratio by model" description="Share of your prompt tokens served from the provider's cache." testId="llm-cache-hits">
          <OwnBody own={own}>{(u) => <CacheHits usage={u} />}</OwnBody>
        </UsagePanel>
      </div>
      <div className="grid min-w-0 gap-4 lg:grid-cols-3">
        <UsagePanel title="Messages and active chats" description="Messages you sent and chats active per day." testId="llm-messages">
          <ComingSoon>Not available yet.</ComingSoon>
        </UsagePanel>
        <UsagePanel title="Workflow runs by outcome" description="Your runs, by how they ended." testId="llm-workflows">
          <ComingSoon>Not available yet.</ComingSoon>
        </UsagePanel>
        <UsagePanel title="Top projects by tokens" description="Where your tokens went." testId="llm-top-projects">
          <ComingSoon>Not available yet: the usage record does not carry a project.</ComingSoon>
        </UsagePanel>
      </div>
    </section>
  );
}

function ComingSoon({ children }: { children: ReactNode }) {
  return (
    <p className="text-sm text-ink-muted" data-testid="llm-panel-coming-soon">
      {children}
    </p>
  );
}

function OwnBody({ own, children }: { own: OwnLLMUsage; children: (usage: LLMUsage) => ReactNode }) {
  switch (own.status) {
    case "not-deployed":
      return <ComingSoon>Not available from this server yet. Nothing is estimated in its place.</ComingSoon>;
    case "loading":
      return (
        <p role="status" className="text-sm text-ink-muted">
          Loading your usage…
        </p>
      );
    case "error":
      return (
        <p role="alert" className="text-sm text-ink-muted">
          Could not load your usage. It is unknown, not zero.
        </p>
      );
    default:
      return <>{children(own.usage)}</>;
  }
}

function ManagedDenied() {
  return (
    <p role="alert" className="text-sm text-ink-muted" data-testid="llm-managed-denied">
      Organization spend needs billing:read. Your own usage on this page does not.
    </p>
  );
}

/** How complete the caller's own figures are, in the server's words. */
function OwnCoverage({ usage }: { usage: LLMUsage }) {
  return (
    <div className="flex flex-wrap items-center gap-2 text-xs text-ink-muted" data-testid="llm-own-coverage">
      <span>Your usage</span>
      <CompletenessBadge value={usage.completeness} label="Your usage" />
      {usage.note && <span className="min-w-0 break-words">{usage.note}</span>}
      {usage.asOf && <span>· As of {usage.asOf.replace("T", " ").replace(/(\.\d+)?Z$/, " UTC")}; today is still accumulating.</span>}
    </div>
  );
}

function TokensByMode({ usage }: { usage: LLMUsage }) {
  const totals = modeTotals(usage);
  return (
    <div className="space-y-3">
      <UsageChart label="Tokens per day by access mode" bucketsMs={dayStarts(usage)} series={tokensByAuthMode(usage)} unit="count" stacked empty="No model calls in this range." />
      <RowsTable
        caption="Your usage by access mode"
        head={["Access mode", "Calls", "Tokens"]}
        rows={totals.map((t) => [AUTH_MODE_LABELS[t.mode], formatCount(t.calls), formatCount(t.tokens)])}
        empty="No model calls in this range."
      />
    </div>
  );
}

function CallsAndErrors({ usage }: { usage: LLMUsage }) {
  const rate = errorRate(usage.totals);
  return (
    <div className="space-y-2">
      <UsageChart label="Calls and errors per day" bucketsMs={dayStarts(usage)} series={callsAndErrors(usage)} unit="count" empty="No model calls in this range." />
      <p className="text-xs text-ink-muted">
        {formatCount(usage.totals.calls)} calls · {formatCount(usage.totals.errorCalls)} errors · error rate {rate === undefined ? "n/a" : formatPercent(rate)}
      </p>
    </div>
  );
}

function ByModel({ usage }: { usage: LLMUsage }) {
  const rows = usage.byModel.map((m) => [
    m.key || "Unspecified model",
    formatCount(m.calls),
    formatCount(m.inputTokens),
    formatCount(m.outputTokens),
    formatCount(m.cacheReadTokens + m.cacheWriteTokens),
    m.calls > 0 ? formatDuration(m.avgLatencyMs) : "n/a",
  ]);
  return <RowsTable caption="Your usage by model" head={["Model", "Calls", "Input (uncached)", "Output", "Cached", "Avg latency"]} rows={rows} empty="No model calls in this range." />;
}

function CacheHits({ usage }: { usage: LLMUsage }) {
  const rows = usage.byModel.map((m) => {
    const ratio = cacheHitRatio(m);
    return [m.key || "Unspecified model", ratio === undefined ? "n/a" : formatPercent(ratio)];
  });
  return <RowsTable caption="Cache hit ratio by model" head={["Model", "Cache hits"]} rows={rows} empty="No model calls in this range." />;
}

function ManagedByModel({ usage }: { usage: OrganizationUsage }) {
  const totals = groupTotals(usage, ["managed_llm"]);
  return (
    <div className="space-y-2">
      <RowsTable
        caption="Reliant AI spend by model"
        head={["Model", "Charged"]}
        rows={totals.map((t) => [t.label === "Unattributed" ? "Unspecified model" : t.label, <Money key="m" nanos={t.nanos} unpricedDimensions={t.unpricedDimensions} />])}
        empty={categoryAmount(usage, "managed_llm").kind === "unavailable" ? "Reliant AI spend is unavailable, not zero." : "No Reliant AI charges in this range."}
      />
      <p className="flex flex-wrap items-center gap-2 text-xs text-ink-muted">
        Coverage <CompletenessBadge value={coverageOf(usage, "managed_llm")?.completeness} label="Managed LLM" />
      </p>
    </div>
  );
}

function ManagedMonthToDate({ usage }: { usage: OrganizationUsage }) {
  const amount = categoryAmount(usage, "managed_llm");
  return (
    <div className="space-y-3">
      <StatTile
        caption="Reliant AI, month to date"
        testId="llm-managed-mtd"
        value={<AmountText amount={amount} />}
        footnote={amountCaption(amount)}
        badge={<CompletenessBadge value={coverageOf(usage, "managed_llm")?.completeness} label="Managed LLM" />}
      />
      <ElsewhereNote href={BILLING_SETTINGS} linkLabel="Open Usage & limits">
        The Reliant AI spend cap and the distance to it are in Usage & limits.
      </ElsewhereNote>
    </div>
  );
}

function SubscriptionWindows({ view, nowMs }: { view: ReturnType<typeof useProviderAccountsView>; nowMs: number }) {
  if (view.status === "loading") {
    return (
      <p role="status" className="text-sm text-ink-muted">
        Loading connected accounts…
      </p>
    );
  }
  if (view.status === "unavailable") return <p className="text-sm text-ink-muted">Connected accounts are not available from this server.</p>;
  const now = new Date(nowMs);
  const withWindows = view.accounts.filter((a: ProviderAccount) => a.windows.length > 0);
  if (withWindows.length === 0) return <p className="text-sm text-ink-muted">No connected subscription reports usage windows. API keys have per-minute limits only.</p>;
  return (
    <ul className="space-y-4">
      {withWindows.map((account) => (
        <li key={account.id} className="min-w-0 space-y-2" data-testid="llm-subscription-account">
          <div className="text-sm text-ink">
            {account.providerDisplayName} <span className="text-ink-muted">· {account.label}</span>
          </div>
          {account.windows.map((w) => {
            const eff = effectiveWindow(w, account.usageObservedAt, now);
            const name = w.label || windowShortLabel(w);
            return (
              <div key={w.name} className="space-y-1">
                <div className="flex flex-wrap items-baseline justify-between gap-2 text-xs">
                  <span className="text-ink-muted">{name}</span>
                  <span className="tabular-nums text-ink">
                    {eff.used === null ? "Usage not reported" : `${Math.round(eff.used * 100)}% used`}
                    {eff.resetsAt ? <span className="text-ink-muted"> · {formatResetsIn(eff.resetsAt, now)}</span> : null}
                  </span>
                </div>
                {eff.used !== null && <ProgressBar value={eff.used * 100} max={100} ariaLabel={`${account.providerDisplayName} ${name} used`} size="sm" />}
              </div>
            );
          })}
        </li>
      ))}
    </ul>
  );
}
