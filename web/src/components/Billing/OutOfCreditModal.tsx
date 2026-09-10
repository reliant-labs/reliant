/**
 * The one interruption this feature is allowed.
 *
 * ── Why a modal here and nowhere else ─────────────────────────────────
 *
 * At a zero balance the next request actually fails. That is a categorically
 * different thing from "running low", which the ambient indicator handles
 * without ever taking the screen: low credit is information, an empty wallet is
 * a stop. Interrupting for the former would train people to dismiss the latter.
 *
 * It fires once per session. A modal that reappears on every render — or every
 * navigation — is one people learn to close without reading, which defeats the
 * only surface allowed to interrupt.
 *
 * ── Why auto-recharge is offered HERE ─────────────────────────────────
 *
 * This is the first moment the offer can be made with EVIDENCE. Onboarding's
 * amount picker had to ask "how much credit do you want?" of someone who had
 * never run a request and had no basis to answer. By now we know what they
 * actually spend, so the offer arrives with their own numbers attached: their
 * observed daily burn, and how long the suggested amount would last them.
 *
 * The rule is armed through the EXISTING server-side auto-recharge
 * (`SetCurrentUserWalletAutoRecharge`), not a reimplementation. That service
 * owns the parts that are easy to get wrong and expensive to get wrong twice —
 * the double-charge guards, the mandatory monthly ceiling, the decline ladder.
 *
 * ── What it never does ────────────────────────────────────────────────
 *
 * Charge anything. Arming a rule and taking money are different acts, and this
 * modal only does the first. A user with no saved card is sent to billing to
 * add one rather than being asked for card details in a modal that appeared
 * unbidden over their work.
 */

import { useState } from "react";
import { AlertCircle, Wallet } from "lucide-react";
import { useNavigate } from "@tanstack/react-router";

import { Modal } from "@/components/ui/Modal";
import { formatCentsAsDollars } from "@/components/Settings/cloud/billingUtils";
import {
  useSetWalletAutoRecharge,
  useWalletAutoRecharge,
} from "@/hooks/useCloudBillingQueries";
import { cn } from "@/lib/utils";

/**
 * What to suggest recharging, and the ceiling to pair it with.
 *
 * Derived from OBSERVED SPEND rather than fixed, because the right amount for
 * someone burning $2/day and someone burning $40/day are not the same number.
 * The target is roughly a fortnight of their actual usage, rounded to a whole
 * preset-sized figure so the suggestion reads as a considered amount rather
 * than an arithmetic result like "$27.43".
 *
 * The ceiling is 4x the recharge amount: enough headroom that a normal month
 * never silently stops, low enough that a runaway cannot drain a card. It is
 * MANDATORY server-side — there is no uncapped state for a charger that fires
 * with nobody watching — so it is chosen here rather than left blank.
 */
export function suggestRecharge(dailySpendUsd: number | null): {
  amountCents: number;
  maxPerMonthCents: number;
  thresholdCents: number;
} {
  const fortnightCents = dailySpendUsd ? Math.round(dailySpendUsd * 14 * 100) : 0;
  // Round up to the nearest $5, floor $10, cap $100. The cap is not a judgement
  // about what anyone can afford — it is a limit on what we will suggest
  // unprompted for an automatic charge.
  const rounded = Math.ceil(fortnightCents / 500) * 500;
  const amountCents = Math.min(Math.max(rounded, 1000), 10000);
  return {
    amountCents,
    maxPerMonthCents: amountCents * 4,
    // Recharge when about three days remain, so the refill lands before the
    // balance actually hits zero rather than after.
    thresholdCents: dailySpendUsd
      ? Math.max(Math.round(dailySpendUsd * 3 * 100), 500)
      : 500,
  };
}

export interface OutOfCreditModalProps {
  isOpen: boolean;
  onClose: () => void;
  /** Observed daily burn, used to size the offer. Null when unmeasurable. */
  dailySpendUsd: number | null;
}

export function OutOfCreditModal({
  isOpen,
  onClose,
  dailySpendUsd,
}: OutOfCreditModalProps) {
  const navigate = useNavigate();
  const autoRechargeQ = useWalletAutoRecharge(isOpen);
  const setAutoRecharge = useSetWalletAutoRecharge();
  const [error, setError] = useState("");

  const suggestion = suggestRecharge(dailySpendUsd);
  const savedCard = autoRechargeQ.data?.paymentMethod;
  const alreadyArmed = autoRechargeQ.data?.autoRecharge?.enabled ?? false;

  const goToBilling = () => {
    onClose();
    void navigate({
      to: "/settings/$section",
      params: { section: "billing" },
    });
  };

  const armAutoRecharge = async () => {
    setError("");
    try {
      await setAutoRecharge.mutateAsync({
        enabled: true,
        thresholdCents: suggestion.thresholdCents,
        amountCents: suggestion.amountCents,
        maxPerMonthCents: suggestion.maxPerMonthCents,
      });
      // Arming does not add credit — the rule fires on the NEXT balance check.
      // The user still needs a top-up now, so send them where they can do it.
      goToBilling();
    } catch (err) {
      setError(
        err instanceof Error
          ? err.message.replace(/^\[[a-z_]+\]\s*/i, "")
          : "We couldn't turn on auto-recharge.",
      );
    }
  };

  return (
    <Modal
      isOpen={isOpen}
      onClose={onClose}
      size="md"
      title="You're out of AI credit"
      titlePrefix={<AlertCircle className="h-5 w-5 text-destructive" />}
    >
      <div className="space-y-5 p-1">
        <p className="text-sm text-muted-foreground">
          Your balance is empty, so new requests to Reliant&apos;s models will
          fail until you add credit.
        </p>

        {/* The user's OWN numbers. This is the information the onboarding
            amount-picker could not have had, and it is what makes the offer
            below a recommendation rather than a guess. */}
        {dailySpendUsd !== null && (
          <dl
            className="space-y-1.5 rounded-lg border border-border bg-muted/30 px-4 py-3 text-sm"
            data-testid="out-of-credit-usage"
          >
            <div className="flex items-center justify-between">
              <dt className="text-muted-foreground">Your recent usage</dt>
              <dd className="font-medium text-foreground">
                {formatCentsAsDollars(Math.round(dailySpendUsd * 100))}/day
              </dd>
            </div>
            <div className="flex items-center justify-between">
              <dt className="text-muted-foreground">
                {formatCentsAsDollars(suggestion.amountCents)} would last about
              </dt>
              <dd className="font-medium text-foreground">
                {Math.max(
                  Math.floor(suggestion.amountCents / 100 / dailySpendUsd),
                  1,
                )}{" "}
                days
              </dd>
            </div>
          </dl>
        )}

        <button
          onClick={goToBilling}
          className="w-full rounded-lg bg-primary px-4 py-2.5 text-sm font-semibold text-primary-foreground transition-colors hover:bg-primary/90"
          data-testid="out-of-credit-topup"
        >
          Add credit
        </button>

        {/* Auto-recharge: offered, never assumed. It is off unless the user
            presses this, and it is not offered at all when it is already on. */}
        {!alreadyArmed && (
          <div className="space-y-2 rounded-lg border border-border p-4">
            <div className="flex items-start gap-2">
              <Wallet
                className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground"
                aria-hidden
              />
              <div className="space-y-1">
                <p className="text-sm font-medium text-foreground">
                  Never run out again
                </p>
                <p className="text-xs text-muted-foreground">
                  {savedCard
                    ? `Top up ${formatCentsAsDollars(suggestion.amountCents)} automatically from your ${savedCard.brand} ···· ${savedCard.last4} when your balance drops below ${formatCentsAsDollars(suggestion.thresholdCents)}. Capped at ${formatCentsAsDollars(suggestion.maxPerMonthCents)} a month — you can change or turn this off any time.`
                    : "Add a card in billing settings to turn on automatic top-ups."}
                </p>
              </div>
            </div>

            {error && (
              <p className="text-xs text-destructive" role="alert">
                {error}
              </p>
            )}

            <button
              onClick={savedCard ? () => void armAutoRecharge() : goToBilling}
              disabled={setAutoRecharge.isPending}
              className={cn(
                "w-full rounded-lg border border-border px-4 py-2 text-sm font-medium transition-colors",
                setAutoRecharge.isPending
                  ? "cursor-not-allowed text-muted-foreground"
                  : "text-foreground hover:bg-accent",
              )}
              data-testid="out-of-credit-autorecharge"
            >
              {setAutoRecharge.isPending
                ? "Turning on…"
                : savedCard
                  ? "Turn on auto-recharge"
                  : "Set up auto-recharge"}
            </button>
          </div>
        )}

        <button
          onClick={onClose}
          className="w-full text-center text-xs text-muted-foreground transition-colors hover:text-foreground"
        >
          Not now
        </button>
      </div>
    </Modal>
  );
}
