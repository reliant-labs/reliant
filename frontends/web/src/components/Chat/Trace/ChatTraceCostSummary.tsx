// Copyright (c) 2026 Reliant Labs

/**
 * The chat's totals: model calls, tokens and time in the model for everyone
 * who may read the trace, and cost for its owner only.
 *
 * Cost keeps its two kinds apart, as the server does (ChatTraceTotals):
 * REPORTED is what the gateway charged, ESTIMATED is a list price at the
 * call's date. They are never added together. What neither covers, calls on
 * a subscription or a local model and calls with no recorded cost, is shown
 * as the unpriced share, so a small total never reads as the whole bill.
 *
 * A viewer sees no dollar figure at all.
 */

import type { ChatTraceTotals } from "@/gen/reliant/v1/chat_trace_pb";
import { formatDuration, formatPercent } from "@/components/Forge/Observability/format";

import { costSummary, formatTokenCount, plural } from "./chatTraceModel";

function Figure({ label, value, detail, testId }: { label: string; value: string; detail?: string; testId?: string }) {
  return (
    <div className="min-w-0 space-y-0.5" data-testid={testId}>
      <dt className="text-meta text-muted-foreground">{label}</dt>
      <dd className="text-2xl font-semibold tabular-nums text-foreground">{value}</dd>
      {detail && <dd className="text-meta text-muted-foreground">{detail}</dd>}
    </div>
  );
}

export function ChatTraceCostSummary({ totals, viewer }: { totals: ChatTraceTotals | undefined; viewer: boolean }) {
  const calls = Number(totals?.calls ?? 0n);
  const errors = Number(totals?.errorCalls ?? 0n);
  const cacheRead = Number(totals?.cacheReadTokens ?? 0n);
  return (
    <section aria-label="Totals" className="space-y-4" data-testid="chat-trace-summary">
      <dl className="grid grid-cols-2 gap-4 sm:grid-cols-4">
        <Figure
          label="Model calls"
          value={calls.toLocaleString()}
          detail={errors > 0 ? `${plural(errors, "failed call")}` : undefined}
          testId="chat-trace-calls"
        />
        <Figure label="Input tokens" value={formatTokenCount(totals?.inputTokens ?? 0n)} detail={cacheRead > 0 ? `${formatTokenCount(cacheRead)} from cache` : undefined} />
        <Figure label="Output tokens" value={formatTokenCount(totals?.outputTokens ?? 0n)} />
        <Figure label="Time in model" value={formatDuration(Number(totals?.latencyMsTotal ?? 0n))} />
      </dl>
      {viewer ? (
        <p className="text-meta text-muted-foreground" data-testid="chat-trace-viewer-note">
          You're viewing a shared chat: steps, timing, models and tokens. Cost and tool results stay with its owner.
        </p>
      ) : (
        <OwnerCost totals={totals} />
      )}
    </section>
  );
}

function OwnerCost({ totals }: { totals: ChatTraceTotals | undefined }) {
  const cost = costSummary(totals);
  if (cost.calls === 0) return null;
  const unpriced = cost.unpricedCalls + cost.unknownCalls;
  const unpricedParts = [
    cost.unpricedCalls > 0 ? `${plural(cost.unpricedCalls, "call")} on a subscription or local model` : "",
    cost.unknownCalls > 0 ? `${plural(cost.unknownCalls, "call")} with no recorded cost` : "",
  ].filter(Boolean);
  return (
    <dl className="grid grid-cols-1 gap-4 border-t border-border-subtle pt-4 sm:grid-cols-3" data-testid="chat-trace-cost">
      <Figure
        label="Reported cost"
        value={cost.reported?.usd ?? "—"}
        detail={cost.reported ? `charged for ${plural(cost.reported.calls, "call")}` : "No call was priced by the gateway"}
        testId="chat-trace-cost-reported"
      />
      <Figure
        label="Estimated cost"
        value={cost.estimated ? `~${cost.estimated.usd}` : "—"}
        detail={cost.estimated ? `list price for ${plural(cost.estimated.calls, "call")}; your provider bill is authoritative` : "No call was estimated"}
        testId="chat-trace-cost-estimated"
      />
      <Figure
        label="Unpriced"
        value={formatPercent(cost.unpricedShare)}
        detail={unpriced > 0 ? `${unpriced} of ${plural(cost.calls, "call")}: ${unpricedParts.join(", ")}` : `every call is priced`}
        testId="chat-trace-cost-unpriced"
      />
    </dl>
  );
}
