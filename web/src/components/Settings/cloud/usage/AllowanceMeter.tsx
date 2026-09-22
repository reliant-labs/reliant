// One dimension's consumption against its allowance.
//
// The question this answers at a glance is "how close am I, and is that
// fine or alarming" — so the bar is tinted by the LADDER RUNG the backend
// would actually apply, not by an aesthetic threshold invented here. A bar
// that turns amber at 80% because 80% looks like a lot, while the platform
// notifies at some other number, is a dashboard telling its own story.
//
// Over-100% is rendered as a FULL bar plus an explicit overage figure rather
// than a bar that overflows its track or silently clamps. Clamping loses the
// only number that costs money; overflowing makes 105% and 400% look alike.
//
// ── ELEVATION ─────────────────────────────────────────────────────────
//
// This renders INSIDE the usage panel's card, so it is an inset, not a second
// card: `bg-background` + `border-border/60`, per the rule in ../ui/card.tsx.
// The console version used `bg-surface` (its own card token) because it was a
// standalone card on a bare page. `bg-muted` is not an option here — it lifts
// in dark mode and recesses in light, so an inset built on it reads as a well
// on one theme and a floating slab on the other.

import { cn } from "@/lib/utils";

import { formatQuantity, gibMonthsFromGibHours } from "./usageModel";

import type { DimensionReading } from "./usageModel";

const rungTint: Record<DimensionReading["rung"], { bar: string; text: string }> = {
  none: { bar: "bg-primary", text: "text-muted-foreground" },
  notify: { bar: "bg-warning", text: "text-warning" },
  block_scale_up: { bar: "bg-destructive", text: "text-destructive" },
  throttle: { bar: "bg-destructive", text: "text-destructive" },
  suspend: { bar: "bg-destructive", text: "text-destructive" },
  // A rung this build does not recognise is rendered neutrally rather than
  // tinted as though it were benign — see parseRung.
  unknown: { bar: "bg-muted-foreground", text: "text-muted-foreground" },
};

function percentLabel(reading: DimensionReading): string {
  if (!reading.hasAllowance) return "no allowance";
  const rounded =
    reading.percentUsed < 1 && reading.percentUsed > 0 ? 1 : reading.percentUsed;
  return `${Math.round(rounded)}%`;
}

export interface AllowanceMeterProps {
  reading: DimensionReading;
  /**
   * When FALSE the server could not measure, and the consumption figures
   * carry no information. The card renders "unavailable" instead of a
   * confident zero — a zero here is indistinguishable from "you ran nothing"
   * and fails in the reassuring direction while real usage accrues.
   *
   * The allowance is still shown: it comes from the plan, not from metering,
   * so it remains true even when nothing measured the usage.
   */
  usageMeasured?: boolean;
}

export function AllowanceMeter({
  reading,
  usageMeasured = true,
}: AllowanceMeterProps) {
  const tint = rungTint[reading.rung];
  // The TRACK is capped at 100% so the geometry stays comparable across
  // dimensions; the overage is stated in words underneath instead.
  const fillPercent = Math.min(100, Math.max(0, reading.percentUsed));
  const isOver = reading.percentUsed >= 100;

  if (!usageMeasured) {
    return (
      <div className="rounded-md border border-border/60 bg-background p-4">
        <div className="flex items-baseline justify-between gap-3">
          <h4 className="text-sm font-semibold text-foreground">{reading.label}</h4>
          <span className="text-sm font-medium text-muted-foreground">
            unavailable
          </span>
        </div>
        <p className="mt-1 text-xs text-muted-foreground">
          {reading.hasAllowance ? (
            <>
              Your plan includes{" "}
              <span className="tabular-nums text-foreground">
                {formatQuantity(reading.allowance)}
              </span>{" "}
              {reading.unitLabel}, but current usage could not be measured.
            </>
          ) : (
            "Current usage could not be measured."
          )}
        </p>
        {/* No track fill, and no track VALUE: an empty bar reads as "nothing
            used", which is the claim this whole branch exists to refuse. */}
        <div className="mt-3 h-2 w-full rounded-full bg-muted" />
        <p className="mt-2 text-xs leading-relaxed text-muted-foreground">
          This is not a reading of zero — it means the measurement is missing.
        </p>
      </div>
    );
  }

  return (
    <div className="rounded-md border border-border/60 bg-background p-4">
      <div className="flex items-baseline justify-between gap-3">
        <h4 className="text-sm font-semibold text-foreground">{reading.label}</h4>
        <span className={cn("text-sm font-medium tabular-nums", tint.text)}>
          {percentLabel(reading)}
        </span>
      </div>

      <p className="mt-1 text-xs text-muted-foreground">
        {reading.hasAllowance ? (
          <>
            <span className="tabular-nums text-foreground">
              {formatQuantity(reading.consumed)}
            </span>
            {" of "}
            <span className="tabular-nums text-foreground">
              {formatQuantity(reading.allowance)}
            </span>{" "}
            {reading.unitLabel}
          </>
        ) : (
          <>
            <span className="tabular-nums text-foreground">
              {formatQuantity(reading.consumed)}
            </span>{" "}
            {reading.unitLabel} used — this plan includes none
          </>
        )}
      </p>

      <div
        className="mt-3 h-2 w-full overflow-hidden rounded-full bg-muted"
        role="progressbar"
        aria-label={`${reading.label} consumption against allowance`}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={Math.round(fillPercent)}
        aria-valuetext={
          reading.hasAllowance
            ? `${percentLabel(reading)} of allowance used`
            : "no allowance included in this plan"
        }
      >
        {/*
          Inline width is RUNTIME GEOMETRY — the documented exception to the
          no-inline-styles rule. The fill is a continuous percentage computed
          from live consumption, so it cannot be a static utility class, and
          Tailwind's JIT does not emit a class for a value it never sees at
          build time.
        */}
        <div
          className={cn("h-full rounded-full transition-[width]", tint.bar)}
          style={{ width: `${fillPercent}%` }}
        />
      </div>

      {isOver && reading.overage > 0 ? (
        <p className={cn("mt-2 text-xs font-medium", tint.text)}>
          {formatQuantity(reading.overage)} {reading.unitLabel} over
        </p>
      ) : null}

      {reading.id === "storage" && reading.allowance > 0 ? (
        <p className="mt-2 text-xs text-muted-foreground">
          ≈ {formatQuantity(gibMonthsFromGibHours(reading.allowance))} GiB for a
          full month
        </p>
      ) : null}

      <p className="mt-2 text-xs leading-relaxed text-muted-foreground">
        {reading.meterDescription}
      </p>
    </div>
  );
}
