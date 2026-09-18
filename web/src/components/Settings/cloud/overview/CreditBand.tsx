import type { ReactNode } from "react";
import { Wallet } from "lucide-react";

import { Button } from "../ui";
import { TOPUP_PRESETS_CENTS, formatRunway } from "../billingUtils";
import { RedeemCouponForm } from "@/components/RedeemCouponForm";
import { cn } from "@/lib/utils";

/**
 * AI credit, drawn as A METER THAT DRAINS.
 *
 * The page used to render this and the compute subscription from one template
 * — `<big number> + <label> + <row of controls>`, in the same Card, at the same
 * weight — which is a visual claim that a wallet and a rented capacity are the
 * same kind of thing. They are not: a wallet is a quantity you deplete, a
 * subscription is a capacity you rent, and everything the owner called
 * "monotone" follows from drawing the second with the first's template.
 *
 * The differentiation here is SHAPE, not colour, which is what the repo's
 * styling contract requires and also what actually reads at a glance. This
 * band is a filled, borderless reservoir — one unbroken `rounded-2xl` slab
 * with no `border` and no header rule — its primary number is DOLLARS
 * REMAINING, its bar depletes left-to-right, and its time axis looks BACKWARD
 * (burn rate, not a renewal date). `ComputeBand` is the deliberate inverse on
 * every one of those axes: bordered, ruled into sections, forward-looking.
 *
 * It fills with `bg-card`, the same elevation as the compute band beside it,
 * because both are primary surfaces sitting on the page and neither is nested
 * in the other. The fill used to be `bg-muted/40`, which is the elevation bug
 * `ui/card.tsx` documents at length: `--muted` is LIGHTER than `--card` in
 * every dark theme and DARKER in every light one, so the reservoir floated on
 * one and sank on the other while the compute band stayed put. Shape carries
 * the distinction; elevation must not, or the page tilts by theme.
 *
 * Every value is a resolved prop. This component makes no business decision,
 * calls no mutation, and knows no price: the anti-anonymous-purchase guarantee
 * lives at the mutation chokepoint in `useCloudBillingQueries`, and a band that
 * spent money directly would route around it.
 */
export interface CreditBandProps {
  /** Formatted balance, or null when the read failed. */
  balance: string | null;
  warning: { title: string; message: string } | null;
  /** Whole days of credit left, or null when we should not say. */
  runwayDays: number | null;
  /** Fraction of a full meter still filled, 0–1. */
  meterFraction: number;
  topupInFlightCents: number | null;
  onAddCredit: (amountCents: number) => void;
  onRetryBalance: () => void;
  onRedeemed: () => void;
  /** The embedded checkout panel, mounted by the parent. */
  checkout?: ReactNode;
  /**
   * The automatic-top-up control, mounted by the parent.
   *
   * Credit's answer to compute's overage control, and the reason it is a slot
   * rather than something this band builds: the band resolves no data and
   * calls no mutation, and a budget rule is a server round-trip either way.
   * Its absence from this band is complaint #3's second half — the feature was
   * built, shipped, and reachable only from the out-of-credit modal.
   */
  spendControl?: ReactNode;
}

export function CreditBand({
  balance,
  warning,
  runwayDays,
  meterFraction,
  topupInFlightCents,
  onAddCredit,
  onRetryBalance,
  onRedeemed,
  checkout,
  spendControl,
}: CreditBandProps) {
  // Never offer to spend against a number we could not read. This disables the
  // top-up presets and nothing else — the coupon form does not depend on the
  // balance, so it stays live.
  const balanceUnavailable = balance === null;

  return (
    <section
      aria-labelledby="credit-band-heading"
      // Filled and borderless on purpose: a reservoir, not a contract. The
      // compute band beside it is bordered and sectioned, and the contrast is
      // the whole point — two shapes rather than two colours.
      //
      // Declared as data rather than left implicit in the class string, so
      // "the two products are drawn differently" is a contract a test can hold
      // us to. Pinning the class instead would pin styling, break on every
      // visual tweak, and still not catch the two bands being made identical.
      data-band-shape="reservoir"
      className="rounded-2xl bg-card px-5 py-5 sm:px-6"
    >
      <div className="flex items-center gap-2">
        <Wallet className="h-4 w-4 text-muted-foreground" />
        <h3
          id="credit-band-heading"
          className="text-xs font-semibold uppercase tracking-wide text-muted-foreground"
        >
          AI credit
        </h3>
      </div>

      <div className="mt-4 flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
        <div>
          <p className="text-4xl font-semibold tracking-tight text-foreground">
            {balance ?? "$—"}
          </p>
          <p className="mt-1 text-sm text-muted-foreground">
            {balanceUnavailable
              ? "Couldn't load your balance"
              : "credit remaining"}
          </p>
        </div>

        {balanceUnavailable ? (
          <Button size="sm" variant="outline" onClick={onRetryBalance}>
            Retry
          </Button>
        ) : (
          <div className="w-full sm:max-w-[16rem]">
            {/* A DRAIN. `meter` rather than `progressbar` because this is a
                level within a known range, not the progress of a task — and
                the distinction is what assistive tech announces. */}
            <div
              role="meter"
              aria-label="Credit remaining"
              aria-valuemin={0}
              aria-valuemax={100}
              aria-valuenow={Math.round(meterFraction * 100)}
              className="h-2 w-full overflow-hidden rounded-full bg-background"
            >
              <div
                className={cn(
                  "h-full rounded-full",
                  warning ? "bg-warning" : "bg-primary",
                )}
                style={{ width: `${Math.max(meterFraction * 100, 2)}%` }}
              />
            </div>
            {/* An ESTIMATE ABOUT MONEY. Hedged in the copy, and simply absent
                when the sample cannot support it — never "~0 days" and never a
                dash, both of which read as measured values. */}
            {runwayDays !== null && (
              <p className="mt-2 text-sm text-muted-foreground">
                {formatRunway(runwayDays)}
              </p>
            )}
          </div>
        )}
      </div>

      {warning && (
        <div className="mt-4 rounded-md border border-warning/30 bg-warning/10 px-3 py-2 text-xs text-warning">
          <p className="font-semibold">{warning.title}</p>
          <p>{warning.message}</p>
        </div>
      )}

      <div className="mt-5 flex flex-col gap-3 border-t border-border/60 pt-4">
        {/* A named group, not a bare row of dollar buttons: "$25" on its own
            says nothing about what it does, and the label has to be reachable
            programmatically rather than by proximity.

            This is now the ONLY amount picker in the credit flow. The checkout
            panel below used to render a second grid of these same four amounts
            under "How much credit?", so a user who pressed $25 here was asked
            to press $25 again a few hundred pixels down. The panel confirms the
            amount instead, and these buttons stay live while it is open — so
            changing your mind is one click on the control you already used,
            rather than a decision you make twice. */}
        <div
          role="group"
          aria-label="Add credit"
          className="flex flex-wrap items-center gap-2"
        >
          <span aria-hidden className="text-sm font-medium text-foreground">
            Add credit
          </span>
          {TOPUP_PRESETS_CENTS.map((cents) => (
            <Button
              key={cents}
              size="sm"
              // Pressed, not merely coloured. Once this is the only picker, the
              // selected amount is state a screen reader has to be able to
              // read back — `variant="primary"` alone announces nothing.
              aria-pressed={topupInFlightCents === cents}
              variant={topupInFlightCents === cents ? "primary" : "outline"}
              disabled={balanceUnavailable}
              title={
                balanceUnavailable
                  ? "Add credit once your balance loads"
                  : undefined
              }
              onClick={() => onAddCredit(cents)}
            >
              ${(cents / 100).toFixed(0)}
            </Button>
          ))}
        </div>

        {checkout}

        {/* One redemption RPC, so one box — the server decides whether a code
            grants credit or machine minutes. It renders open because this is
            the answer to "where do I put my code", and an answer behind a
            disclosure is the bug being fixed.

            Hidden while `checkout` is mounted: the top-up page carries its own
            coupon field, as a first-class way to pay for the amount being
            bought. Rendering both stacks two identical "Coupon code" inputs a
            few hundred pixels apart, and a user cannot tell which one their
            code belongs in. The panel's is the more specific of the two, so
            the general one steps aside for as long as it is up. */}
        {!checkout && (
          <div>
            <RedeemCouponForm
              variant="open"
              size="sm"
              onRedeemed={onRedeemed}
            />
            <p className="mt-2 text-xs text-muted-foreground">
              One box, either kind of code — a coupon can add account credit or
              machine minutes, and we&apos;ll tell you which one it applied.
              Machine minutes show up under Compute, and are spent after your
              plan&apos;s included hours.
            </p>
          </div>
        )}
      </div>

      {/* Budgets and limits, on the page rather than behind an emergency.
          Divided off from the top-up actions above because it answers a
          different question: those spend money now, this decides what may be
          spent later. */}
      {spendControl && (
        <div className="mt-5 border-t border-border/60 pt-4">{spendControl}</div>
      )}
    </section>
  );
}
