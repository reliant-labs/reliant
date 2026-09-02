/**
 * The single coupon-redemption form.
 *
 * WHY ONE COMPONENT: this existed in three near-identical copies (Settings →
 * Reliant AI, onboarding's ModelStep, onboarding's ComputeStep), each with its
 * own state, its own `[code]`-prefix stripping, and its own success wording.
 * Three copies of a money path is three places for a bug to hide and two places
 * to forget when one is fixed.
 *
 * The variation between those copies was entirely PRESENTATION — collapsed
 * link vs always-open panel, and prose vs compact sizing — so that is what the
 * props expose. The behavior (trim, validate, submit, show the server's
 * per-case message verbatim, report what was granted) is shared and no longer
 * forkable.
 */
import React, { useState } from "react";

import { useRedeemCoupon } from "@/hooks/useReliantAIQueries";
import {
  RedeemedCouponKind,
  type RedeemCouponResult,
} from "@/services/controlPlane/reliantAI";
import { onboardingService } from "@/services/controlPlane/onboarding";
import { getEventBus } from "@/lib/events";
import { logger } from "@/lib/logger";
import { formatMachineMinutes } from "@/lib/formatMachineMinutes";
import { cn } from "@/lib/utils";

export interface RedeemCouponFormProps {
  /**
   * Called after a successful redemption. Callers use this to refetch whatever
   * the coupon just changed — compute eligibility, a wallet balance, a grant
   * total — so the surrounding UI updates in place rather than going stale.
   *
   * Receives what was granted, so a caller can react to the KIND: onboarding
   * auto-starts a cloud daemon after a code that unblocked compute, which
   * would be wrong after one that only added wallet credit. Callers that do
   * not care may ignore the argument.
   */
  onRedeemed?: (result: RedeemCouponResult) => void;
  /**
   * "collapsed" renders a "Have a coupon code?" link that expands on click —
   * right when most visits are not redemptions and a permanent input invites
   * hunting for a code the user does not have.
   *
   * "button" is the same disclosure with a full-width solid button as the
   * trigger, for screens where redeeming is one of the few things the user can
   * actually do and the affordance has to carry the weight of a primary
   * control rather than sit under one as a link.
   *
   * "open" renders the input immediately, for places where redeeming is the
   * point.
   *
   * NOTE: this seeds the INITIAL state only. It is not a controlled prop, so
   * passing a value that flips with some other piece of state (e.g.
   * `variant={eligible ? "collapsed" : "open"}`) will not collapse or expand
   * an already-mounted form — it will just look stuck. Pick one and leave it.
   */
  variant?: "collapsed" | "open" | "button";
  /** Compact sizing for dense cards. */
  size?: "sm" | "md";
  className?: string;
}

/** Strips the `[code]` prefix Connect puts in front of the server's message. */
function userFacingMessage(err: unknown): string {
  if (err instanceof Error && err.message) {
    return err.message.replace(/^\[[a-z_]+\]\s*/i, "");
  }
  return "Could not redeem that code.";
}

export function RedeemCouponForm({
  onRedeemed,
  variant = "collapsed",
  size = "md",
  className,
}: RedeemCouponFormProps) {
  const [open, setOpen] = useState(variant === "open");
  const [code, setCode] = useState("");
  const [error, setError] = useState("");
  const [redeemed, setRedeemed] = useState("");
  const [syncWarning, setSyncWarning] = useState("");
  const redeem = useRedeemCoupon();

  const small = size === "sm";

  const submit = (e?: React.FormEvent) => {
    e?.preventDefault();
    setError("");
    setRedeemed("");
    setSyncWarning("");

    const trimmed = code.trim();
    if (!trimmed) {
      setError("Enter a coupon code.");
      return;
    }

    redeem.mutate(trimmed, {
      onSuccess: (res) => {
        setRedeemed(
          res.kind === RedeemedCouponKind.COMPUTE_MINUTES
            ? `Added ${formatMachineMinutes(res.computeMinutes)}. ` +
                `You now have ${formatMachineMinutes(res.newComputeMinutesRemaining)} available.`
            : `Added $${(res.amountCents / 100).toFixed(2)} to your balance.`,
        );
        setCode("");
        onRedeemed?.(res);

        // Only WALLET_CREDIT actually buys AI access — COMPUTE_MINUTES pays
        // for machine time and grants no provider entitlement, so syncing a
        // key after one would (incorrectly) check off "Add an API key" for a
        // coupon that never funded any AI usage.
        if (res.kind === RedeemedCouponKind.WALLET_CREDIT) {
          onboardingService.provisionManagedKey().then(
            (result) => {
              if (!result.synced) return;
              // Established pattern for this provider — see
              // CombinedGeneralSettings' handleEnableReliant.
              getEventBus().emit("api-key:saved", { provider: "reliant" });
            },
            (err: unknown) => {
              // The redemption itself already succeeded; don't relitigate it.
              // But don't silently mark the checklist complete either — tell
              // the user there's a follow-up step outstanding.
              logger.warn(
                "[RedeemCouponForm] Reliant provider sync failed after redemption",
                err,
              );
              setSyncWarning(
                "Credit added, but we couldn't sync your Reliant API key automatically. " +
                  "Try again from Settings, or reopen this page.",
              );
            },
          );
        }
      },
      // The server's message is already user-facing and per-case (unknown code
      // / already redeemed / fully claimed / expired), so it is shown verbatim
      // rather than flattened into one generic string.
      onError: (err: unknown) => setError(userFacingMessage(err)),
    });
  };

  if (!open) {
    return (
      <button
        type="button"
        onClick={() => setOpen(true)}
        className={cn(
          variant === "button"
            ? cn(
                // border-transparent, not no border: this button is normally
                // stacked with bordered siblings, and without it the two sit
                // 2px apart in height.
                "inline-flex w-full items-center justify-center rounded-lg border border-transparent bg-primary font-semibold text-primary-foreground transition-colors hover:bg-primary/90",
                small ? "px-3 py-2 text-xs" : "px-4 py-2.5 text-sm",
              )
            : "text-sm font-medium text-primary hover:underline",
          className,
        )}
      >
        Have a coupon code?
      </button>
    );
  }

  return (
    <form className={cn("space-y-2", className)} onSubmit={submit}>
      <label
        htmlFor="coupon-code"
        className={cn(
          "block font-medium text-foreground",
          small ? "text-xs" : "text-xs uppercase tracking-wide text-muted-foreground",
        )}
      >
        Coupon code
      </label>
      <div className="flex gap-2">
        <input
          id="coupon-code"
          value={code}
          onChange={(e) => setCode(e.target.value)}
          placeholder="Enter code"
          disabled={redeem.isPending}
          autoComplete="off"
          aria-label="Coupon code"
          className={cn(
            "flex-1 rounded-md border border-border bg-background focus:outline-none focus:ring-2 focus:ring-ring",
            small ? "px-2.5 py-1.5 text-xs" : "px-3 py-2 text-sm",
          )}
        />
        <button
          type="submit"
          disabled={redeem.isPending}
          className={cn(
            "rounded-md font-semibold transition-colors",
            small ? "px-3 py-1.5 text-xs" : "px-4 py-2 text-sm",
            redeem.isPending
              ? "cursor-not-allowed bg-muted text-muted-foreground"
              : "bg-primary text-primary-foreground hover:bg-primary/90",
          )}
        >
          {redeem.isPending ? "Redeeming…" : "Redeem"}
        </button>
      </div>
      {error && (
        <p className={cn("text-destructive", small ? "text-xs" : "text-sm")}>
          {error}
        </p>
      )}
      {redeemed && (
        <p className={cn("text-success", small ? "text-xs" : "text-sm")}>
          {redeemed}
        </p>
      )}
      {syncWarning && (
        <p className={cn("text-warning", small ? "text-xs" : "text-sm")}>
          {syncWarning}
        </p>
      )}
    </form>
  );
}
