/**
 * Add AI credit in place, over whatever ran out.
 *
 * The out-of-credit and credit-exhausted modals used to send the user to the
 * billing page to top up and leave them there. This mounts the same embedded
 * checkout billing uses (`WalletTopupCheckout`), so the payment happens where
 * the interruption did and the user's work is still on screen when it settles.
 *
 * Settlement is the SERVER's answer: the wallet balance re-read and compared
 * with the balance when this opened, never a local "Stripe said yes".
 */
import { useCallback, useRef, useState } from "react";

import { useWalletOverview } from "@/hooks/useCloudBillingQueries";
import { formatCentsAsDollars, nanosFromFields, TOPUP_PRESETS_CENTS } from "@/components/Settings/cloud/billingUtils";
import { cn } from "@/lib/utils";

import { LinkIdentityModal } from "./LinkIdentityModal";
import { WalletTopupCheckout } from "./WalletTopupCheckout";

export interface InlineCreditTopupProps {
  /** The amount to start on; the user can pick another preset. */
  defaultAmountCents?: number;
  /** The credit landed (server-confirmed). */
  onDone: () => void;
}

export function InlineCreditTopup({ defaultAmountCents, onDone }: InlineCreditTopupProps) {
  const walletQ = useWalletOverview();
  const [amountCents, setAmountCents] = useState<number>(defaultAmountCents ?? TOPUP_PRESETS_CENTS[1]);
  const [linkAttempt, setLinkAttempt] = useState(0);

  const balanceNanos = (data: typeof walletQ.data | undefined): bigint => {
    const w = data?.overview?.wallet;
    return nanosFromFields(w?.balanceUsdNanos, w?.balanceUsdMicros, w?.balanceCents);
  };
  const startBalance = useRef<bigint | null>(null);
  if (startBalance.current === null && walletQ.data) startBalance.current = balanceNanos(walletQ.data);

  const landed = useCallback(async () => {
    try {
      const fresh = await walletQ.refetch();
      return balanceNanos(fresh.data) > (startBalance.current ?? 0n);
    } catch {
      return false;
    }
    // balanceNanos is pure; walletQ is the dependency.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [walletQ]);

  return (
    <div className="space-y-3">
      <div role="radiogroup" aria-label="Amount" className="flex flex-wrap gap-2">
        {TOPUP_PRESETS_CENTS.map((cents) => (
          <button
            key={cents}
            type="button"
            role="radio"
            aria-checked={cents === amountCents}
            onClick={() => setAmountCents(cents)}
            className={cn(
              "rounded-md border px-3 py-1.5 text-sm font-medium transition-colors",
              cents === amountCents
                ? "border-primary bg-primary/10 text-foreground"
                : "border-border bg-background text-muted-foreground hover:text-foreground",
            )}
          >
            {formatCentsAsDollars(cents)}
          </button>
        ))}
      </div>
      <WalletTopupCheckout
        key={`topup:${linkAttempt}`}
        amountCents={amountCents}
        confirmSettlement={landed}
        onDone={onDone}
        renderIdentityRequired={(message) => (
          <LinkIdentityModal message={message} onLinked={() => setLinkAttempt((n) => n + 1)} onDismiss={() => undefined} />
        )}
      />
    </div>
  );
}
