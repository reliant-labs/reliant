import { Cpu } from "lucide-react";

import { Button } from "../ui";
import type { ComputeCapacity } from "../billingUtils";
import { formatMachineMinutesShort } from "@/lib/formatMachineMinutes";
import { cn } from "@/lib/utils";

/**
 * Compute, drawn as A CAPACITY WITH A CEILING.
 *
 * The deliberate inverse of `CreditBand` on every axis, because the two
 * products are different kinds of thing and the page has to say so with shape
 * rather than colour (the styling contract permits no invented palette, and a
 * colour split would not survive a theme change anyway):
 *
 *   | axis            | credit                 | compute                     |
 *   |-----------------|------------------------|-----------------------------|
 *   | container       | filled, borderless     | bordered, sectioned         |
 *   | primary number  | dollars remaining      | hours used OF hours included|
 *   | bar             | one meter, depleting   | segmented, boundary marked  |
 *   | time axis       | backward (burn rate)   | forward (renews on a date)  |
 *   | verbs           | add / redeem           | change plan / set a ceiling |
 *
 * The segmented bar is the load-bearing piece: overage is drawn as its OWN
 * segment past a marked boundary, not as more fill in the same bar. Letting
 * used minutes overflow the included segment erases the boundary this band
 * exists to make legible.
 *
 * `renderOverageControl` is a slot rather than inline markup on purpose — the
 * spend-cap work replaces what sits there, and a band that owned the control's
 * internals would have to be restructured to accept it.
 */
export interface ComputeBandProps {
  /** Null when there is no subscription — the true empty. */
  planName: string | null;
  pricePerMonthLabel: string | null;
  renewsOnLabel: string | null;
  includedHoursLabel: string | null;
  usedHoursLabel: string | null;
  allowedSizesLabel: string | null;
  capacity: ComputeCapacity | null;
  grantedMinutesRemaining: number;
  /** The plan row arrived without its detail — see isPlanDetailUnavailable. */
  planDetailUnavailable: boolean;
  usageUnavailable: boolean;
  estimatedOverageCostLabel: string | null;
  onChangePlan: () => void;
  onRetryUsage: () => void;
  renderOverageControl: (args: { disabled: boolean; reason?: string }) => React.ReactNode;
}

export function ComputeBand({
  planName,
  pricePerMonthLabel,
  renewsOnLabel,
  includedHoursLabel,
  usedHoursLabel,
  allowedSizesLabel,
  capacity,
  grantedMinutesRemaining,
  planDetailUnavailable,
  usageUnavailable,
  estimatedOverageCostLabel,
  onChangePlan,
  onRetryUsage,
  renderOverageControl,
}: ComputeBandProps) {
  return (
    <section
      aria-labelledby="compute-band-heading"
      // Bordered and sectioned: a contract, not a reservoir. See CreditBand for
      // why the shape is declared rather than left implicit in the classes.
      data-band-shape="contract"
      className="rounded-lg border border-border bg-card"
    >
      <div className="flex flex-col gap-3 border-b border-border px-5 py-4 sm:flex-row sm:items-center sm:justify-between">
        <div className="flex items-center gap-2">
          <Cpu className="h-4 w-4 text-muted-foreground" />
          <h3
            id="compute-band-heading"
            className="text-xs font-semibold uppercase tracking-wide text-muted-foreground"
          >
            Compute
          </h3>
        </div>
        {/* Stays live in every degraded state. It needs the CATALOG, not this
            subscription's stale limits, and blanket-disabling a card on partial
            failure removes the escape route that would have fixed the problem. */}
        <Button
          size="sm"
          variant="outline"
          className="w-full sm:w-auto"
          onClick={onChangePlan}
        >
          Change plan
        </Button>
      </div>

      <div className="flex flex-col gap-5 px-5 py-5">
        {planName === null ? (
          // The TRUE empty, and a different fact from "the detail didn't load".
          // A new user's overview was a wall of dashes; this is a sentence and
          // a next step.
          <div>
            <p className="text-lg font-semibold text-foreground">
              No compute plan
            </p>
            <p className="mt-1 text-sm text-muted-foreground">
              Machines run on a plan — pick one to get started.
            </p>
          </div>
        ) : (
          <>
            <div className="flex flex-col gap-1 sm:flex-row sm:items-baseline sm:justify-between">
              <div>
                <p className="text-2xl font-semibold text-foreground">
                  {planName}
                </p>
                <p className="mt-0.5 text-sm text-muted-foreground">
                  {[pricePerMonthLabel, renewsOnLabel]
                    .filter(Boolean)
                    .join(" · ")}
                </p>
              </div>
              {allowedSizesLabel && (
                <p className="text-sm text-muted-foreground">
                  Runs {allowedSizesLabel.toLowerCase()}
                </p>
              )}
            </div>

            {planDetailUnavailable ? (
              // ONE line, not three dashes. `0 h / mo` was the whole defect in
              // three characters: the data arrived, was unusable, and rendered
              // as a legitimate value meaning "this plan includes no hours" —
              // next to a purchase button.
              <p className="rounded-md border border-border bg-muted/40 px-4 py-3 text-sm text-muted-foreground">
                Plan details are unavailable — the control plane may not have
                restarted since the plan catalog changed.
              </p>
            ) : usageUnavailable ? (
              // Partial failure degrades partially: the plan's own facts are
              // fine, so only the bar is replaced.
              //
              // The allowance is still stated, because it comes from the plan
              // and is true whatever metering did. What is withheld is the
              // one number that depends on measurement — and it is withheld
              // by SAYING so, not by leaving a gap. A blank where hours used
              // belongs reads as zero, which is the reading this exists to
              // prevent.
              <div className="flex flex-col gap-2 rounded-md border border-border bg-muted/40 px-4 py-3 text-sm text-muted-foreground sm:flex-row sm:items-center sm:justify-between">
                <div>
                  <p>Usage unavailable for this period.</p>
                  {includedHoursLabel && (
                    <p className="mt-0.5 text-xs">
                      Your plan includes {includedHoursLabel.replace(" included", "")}.
                    </p>
                  )}
                </div>
                <Button size="sm" variant="ghost" onClick={onRetryUsage}>
                  Retry
                </Button>
              </div>
            ) : (
              capacity && (
                <CapacityBar
                  capacity={capacity}
                  usedHoursLabel={usedHoursLabel}
                  includedHoursLabel={includedHoursLabel}
                  estimatedOverageCostLabel={estimatedOverageCostLabel}
                />
              )
            )}

          </>
        )}

        {/* Outside the plan branch on purpose: a redeemed grant is machine
            time you HAVE, and someone who redeemed a code before subscribing
            has some with no plan at all. Nesting it under the plan hid it from
            exactly the user most likely to be looking for it. */}
        {grantedMinutesRemaining > 0 && (
          <div className="flex flex-col gap-1 rounded-md border border-border bg-muted/40 px-4 py-3 text-sm sm:flex-row sm:items-center sm:justify-between">
            <div>
              <p className="font-medium text-foreground">Coupon minutes</p>
              <p className="text-xs text-muted-foreground">
                One-time, does not renew — spent after included hours, before
                overage
              </p>
            </div>
            <span className="font-medium text-foreground">
              {formatMachineMinutesShort(grantedMinutesRemaining)}
            </span>
          </div>
        )}

        {/* The overage seam. The control owns its own heading, options and
            copy — this band supplies only the two facts it cannot know:
            whether it should be live, and why not.

            Disabled ONLY when the data it depends on is the missing thing.
            `Change plan` above stays live in the same state, because it needs
            the catalog rather than this subscription's stale limits — §6.4's
            rule, which exists so a partial failure never removes the escape
            route that would have fixed it. */}
        <div className="border-t border-border pt-4">
          {planDetailUnavailable || planName === null ? (
            // The control is NOT rendered disabled here, it is replaced.
            //
            // Passing it a zero rate would make it announce "this plan doesn't
            // offer extra time beyond its included hours" — a confident claim
            // about a plan whose rate is precisely the thing that failed to
            // load. That is the `0 h / mo` defect wearing a different hat: an
            // unknown rendered as a known, and this one would talk a user out
            // of a feature they are paying for.
            <div>
              <p className="text-sm font-medium text-foreground">
                Beyond your included hours
              </p>
              <p className="mt-1 text-xs text-muted-foreground">
                {planName === null
                  ? "Pick a compute plan to choose what happens past your included hours."
                  : "Set a limit once plan details load — the per-minute rate this converts is exactly what's missing."}
              </p>
            </div>
          ) : (
            renderOverageControl({ disabled: false })
          )}
        </div>
      </div>
    </section>
  );
}

/**
 * The segmented bar: included allowance on the left, a marked boundary, and
 * overage as its own segment beyond it. The boundary is the information — a
 * single bar that simply fills past 100% says "you used a lot" where this says
 * "you crossed the line, and here is how far".
 */
function CapacityBar({
  capacity,
  usedHoursLabel,
  includedHoursLabel,
  estimatedOverageCostLabel,
}: {
  capacity: ComputeCapacity;
  usedHoursLabel: string | null;
  includedHoursLabel: string | null;
  estimatedOverageCostLabel: string | null;
}) {
  if (capacity.state === "unlimited") {
    return (
      <p className="text-sm text-muted-foreground">
        <span className="font-medium text-foreground">{usedHoursLabel}</span>{" "}
        used · unlimited hours included
      </p>
    );
  }

  return (
    <div>
      <div className="flex flex-col gap-1 text-sm sm:flex-row sm:items-baseline sm:justify-between">
        <span className="font-medium text-foreground">
          {usedHoursLabel} used
        </span>
        <span className="text-muted-foreground">
          {includedHoursLabel} included
        </span>
      </div>

      <div className="mt-2 flex items-center gap-1">
        {/* Included segment. */}
        <div className="h-2.5 flex-1 overflow-hidden rounded-full bg-muted">
          <div
            role="progressbar"
            aria-label="Included hours used"
            aria-valuemin={0}
            aria-valuemax={100}
            aria-valuenow={Math.round(capacity.usedPct)}
            className={cn(
              "h-full rounded-full",
              capacity.state === "under" ? "bg-primary" : "bg-warning",
            )}
            style={{ width: `${capacity.usedPct}%` }}
          />
        </div>
        {/* The boundary, drawn rather than implied. */}
        <span aria-hidden className="h-3 w-px bg-border" />
        {/* Overage segment — narrow, because it is the exception, not half the
            capacity. */}
        <div className="h-2.5 w-1/4 overflow-hidden rounded-full bg-muted">
          <div
            className="h-full rounded-full bg-destructive"
            style={{ width: `${capacity.overagePct}%` }}
          />
        </div>
      </div>

      <div className="mt-1.5 flex items-baseline justify-between text-xs text-muted-foreground">
        <span>included</span>
        <span>
          {capacity.state === "overage" && estimatedOverageCostLabel
            ? `overage · ${estimatedOverageCostLabel} so far`
            : "overage"}
        </span>
      </div>
    </div>
  );
}
