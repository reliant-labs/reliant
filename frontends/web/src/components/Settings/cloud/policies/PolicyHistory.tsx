// Copyright (c) 2025 Reliant Labs

/**
 * Policy history: who changed which rule, from what, to what, and when. One
 * row per field an edit changed, from the org's limit-change log (§3.4), so
 * every member can see how the limits that bind them came to be.
 */

import Button from "@/components/forge-ui/button";
import { formatAbsoluteTime, formatRelativeTime } from "@/lib/relativeTime";
import { policyErrorMessage } from "@/services/controlPlane/orgPolicy";

import { describeHistoryField, formatHistoryValue } from "./policyDocument";
import { usePolicyHistory } from "./usePolicies";

export function PolicyHistory({ orgId, nameOf }: { orgId: string; nameOf: (userId: string) => string }) {
  const history = usePolicyHistory(orgId);
  const entries = history.data?.pages.flatMap((p) => p.entries) ?? [];

  return (
    <section className="rounded-lg border border-border bg-card" aria-labelledby="policy-history-heading" data-testid="policy-history">
      <h3 id="policy-history-heading" className="border-b border-border px-4 py-3 text-sm font-semibold text-foreground">
        Policy history
      </h3>
      {history.isPending ? (
        <p className="px-4 py-3 text-sm text-muted-foreground" role="status">
          Loading history…
        </p>
      ) : history.isError ? (
        <div className="flex items-center gap-3 px-4 py-3" role="alert">
          <p className="flex-1 text-sm text-muted-foreground">{policyErrorMessage(history.error, "Could not load the policy history.")}</p>
          <Button variant="outline" size="sm" onClick={() => void history.refetch()}>
            Try again
          </Button>
        </div>
      ) : entries.length === 0 ? (
        <p className="px-4 py-3 text-sm text-muted-foreground">
          {history.hasNextPage ? "No policy changes in the most recent limit changes." : "Nobody has changed this organization's policies yet."}
        </p>
      ) : (
        <ul className="divide-y divide-border">
          {entries.map((e) => (
            <li key={e.id} className="flex flex-wrap items-baseline gap-x-3 gap-y-1 px-4 py-3" data-testid={`policy-history-${e.id}`}>
              <div className="min-w-0 flex-1">
                <p className="text-sm text-foreground">
                  <span className="font-medium">{e.actorEmail || "Reliant"}</span> changed {describeHistoryField(e.section, e.field, nameOf)}
                </p>
                <p className="text-sm text-muted-foreground">
                  {formatHistoryValue(e.oldValue)} → <span className="text-foreground">{formatHistoryValue(e.newValue)}</span>
                </p>
              </div>
              {e.at && (
                <time className="text-xs text-muted-foreground" dateTime={e.at.toISOString()} title={formatAbsoluteTime(e.at.toISOString())}>
                  {formatRelativeTime(e.at.toISOString())}
                </time>
              )}
            </li>
          ))}
        </ul>
      )}
      {history.hasNextPage && (
        <div className="border-t border-border px-4 py-3">
          <Button variant="outline" size="sm" loading={history.isFetchingNextPage} onClick={() => void history.fetchNextPage()}>
            Show older
          </Button>
        </div>
      )}
    </section>
  );
}
