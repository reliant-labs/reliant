import { Loader2, RefreshCw } from "lucide-react";
import { cn } from "../../../lib/utils";
import type { ProviderAccount } from "../../../types/providerAccount";
import {
  effectiveWindow,
  formatAgo,
  formatResetsIn,
  isUsageStale,
  usageTone,
  type UsageTone,
} from "./accountFormat";

const FILL: Record<UsageTone, string> = {
  ok: "bg-primary",
  warn: "bg-warning",
  danger: "bg-destructive",
};

const TEXT: Record<UsageTone, string> = {
  ok: "text-muted-foreground",
  warn: "text-warning-ink",
  danger: "text-destructive-ink",
};

interface UsageBarsProps {
  account: ProviderAccount;
  now: Date;
  /** Present when the account can be re-read on demand; renders the ↻ control. */
  onRefresh?: () => void;
  refreshing?: boolean;
  /** Set briefly after a refresh the server throttled. */
  refreshNote?: string;
  /** Touch-sized controls. */
  compact?: boolean;
}

/**
 * One bar per usage window the provider reported (5-hour, Weekly, Weekly ·
 * Opus…). Degrades instead of lying: a window with no reported fraction reads
 * "Usage unknown", an account with no windows says why, and the "as of"
 * caption appears once the reading is older than ten minutes.
 */
export function UsageBars({ account, now, onRefresh, refreshing, refreshNote, compact }: UsageBarsProps) {
  const isKey = account.authKind === "api_key";
  const stale = isUsageStale(account, now);
  const showRefresh = !!onRefresh && account.authKind !== "managed" && (!isKey || account.windows.length > 0);

  return (
    <div className="space-y-2" data-testid="usage-bars">
      {account.windows.length === 0 ? (
        <p className="text-xs text-muted-foreground" title={account.usageError}>
          {isKey ? "Per-minute limits only" : "Usage not available yet"}
        </p>
      ) : (
        account.windows.map((w) => {
          const eff = effectiveWindow(w, account.usageObservedAt, now);
          const tone = usageTone(eff.used, w, account);
          const pct = eff.used === null ? null : Math.round(eff.used * 100);
          const resets = formatResetsIn(eff.resetsAt, now);
          return (
            <div key={w.name}>
              <div className="mb-1 flex items-baseline justify-between gap-3 text-xs">
                <span className="font-medium text-foreground">{w.label || w.name}</span>
                <span className={cn("tabular-nums", TEXT[tone])}>
                  {pct === null ? "Usage unknown" : `${pct}% used`}
                  {resets && ` · ${resets}`}
                </span>
              </div>
              <div
                role="progressbar"
                aria-label={`${w.label || w.name} usage`}
                aria-valuemin={0}
                aria-valuemax={100}
                aria-valuenow={pct ?? undefined}
                aria-valuetext={pct === null ? "unknown" : `${pct}% used`}
                data-tone={tone}
                className="h-1.5 overflow-hidden rounded-full bg-foreground/10"
              >
                {pct !== null && (
                  <div
                    className={cn("h-full rounded-full transition-[width]", FILL[tone])}
                    style={{ width: `${Math.max(pct, pct > 0 ? 2 : 0)}%` }}
                  />
                )}
              </div>
            </div>
          );
        })
      )}

      {(stale || showRefresh || refreshNote) && (
        <div className="flex items-center gap-2 text-2xs text-muted-foreground">
          {stale && <span>as of {formatAgo(account.usageObservedAt, now)}</span>}
          {refreshNote && <span role="status">{refreshNote}</span>}
          {showRefresh && (
            <button
              type="button"
              onClick={onRefresh}
              disabled={refreshing}
              aria-label={`Refresh usage for ${account.label}`}
              className={cn(
                "inline-flex items-center justify-center rounded-md text-muted-foreground hover:text-foreground disabled:opacity-50",
                compact ? "min-h-[44px] min-w-[44px]" : "h-6 w-6",
              )}
            >
              {refreshing ? (
                <Loader2 className="h-3.5 w-3.5 animate-spin" />
              ) : (
                <RefreshCw className="h-3.5 w-3.5" />
              )}
            </button>
          )}
        </div>
      )}
    </div>
  );
}
