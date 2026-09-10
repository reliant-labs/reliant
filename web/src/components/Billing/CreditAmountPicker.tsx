/**
 * How much AI credit to buy.
 *
 * Extracted from `WalletTopupCheckout` for the same reason the plan tiles were
 * extracted from the compute checkout: onboarding now asks this on the MODEL
 * step, where the user is choosing a provider, and only collects a card later.
 * The amount is a decision; the card is a payment; they are no longer the same
 * screen.
 */

import {
  TOPUP_PRESETS_CENTS,
  formatCentsAsDollars,
} from "@/components/Settings/cloud/billingUtils";
import { cn } from "@/lib/utils";

export function CreditAmountPicker({
  value,
  onChange,
}: {
  value: number;
  onChange: (cents: number) => void;
}) {
  return (
    <div className="grid grid-cols-4 gap-2">
      {TOPUP_PRESETS_CENTS.map((cents) => (
        <button
          key={cents}
          type="button"
          aria-pressed={cents === value}
          onClick={() => onChange(cents)}
          className={cn(
            "rounded-lg border-2 py-2.5 text-sm font-semibold transition-all",
            cents === value
              ? "border-primary bg-primary/10 text-foreground"
              : "border-border/50 bg-background text-muted-foreground hover:border-primary/40",
          )}
        >
          {formatCentsAsDollars(cents)}
        </button>
      ))}
    </div>
  );
}
