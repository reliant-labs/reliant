import { PauseCircle } from "lucide-react";
import { Button } from "@/components/ui/Button";

/**
 * Two cards, one number each, plus the one state that needs a button.
 *
 * - Projected this period: what the invoice will total if nothing changes.
 * - Storage: disk across every machine, with the plan's included amount and,
 *   only when there is some, what the extra costs.
 * - Paused: included hours (and any coupon hours) are used up and overage is
 *   off, so machines will not start. One click turns overage on.
 *
 * Every value is the server's (GetCurrentUserComputeUsage); a card the server
 * did not measure is simply not rendered.
 */
export function BillSummary({
  projected,
  storage,
  hoursRanOut,
  turningOnOverage,
  onTurnOnOverage,
}: {
  projected: string | null;
  storage: { value: string; detail: string; extraCost: string | null } | null;
  hoursRanOut: boolean;
  turningOnOverage: boolean;
  onTurnOnOverage: () => void;
}) {
  if (!projected && !storage && !hoursRanOut) return null;
  return (
    <div className="flex flex-col gap-3">
      {hoursRanOut && (
        <div
          className="flex flex-wrap items-center gap-3 rounded-md border border-warning/40 bg-warning/10 px-4 py-3"
          data-testid="hours-ran-out"
        >
          <PauseCircle className="h-4 w-4 shrink-0 text-warning-ink" />
          <p className="flex-1 text-sm text-foreground">Machines are paused: this month&apos;s hours are used up.</p>
          <Button size="sm" onClick={onTurnOnOverage} disabled={turningOnOverage}>
            {turningOnOverage ? "Turning on…" : "Turn on overage"}
          </Button>
        </div>
      )}
      <div className="grid gap-3 sm:grid-cols-2">
        {projected && (
          <div className="rounded-md border border-border bg-card px-4 py-3" data-testid="projected-bill">
            <p className="text-xs text-muted-foreground">Projected this period</p>
            <p className="text-2xl font-semibold tabular-nums text-foreground">{projected}</p>
          </div>
        )}
        {storage && (
          <div className="rounded-md border border-border bg-card px-4 py-3" data-testid="storage-usage">
            <p className="text-xs text-muted-foreground">Storage · {storage.detail}</p>
            <p className="text-2xl font-semibold tabular-nums text-foreground">{storage.value}</p>
            {storage.extraCost && <p className="text-xs text-muted-foreground">{storage.extraCost}</p>}
          </div>
        )}
      </div>
    </div>
  );
}
