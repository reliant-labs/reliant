/**
 * `/m/settings` → "Billing" section.
 *
 * Simple cards for plan, wallet balance, and a usage summary — NOT the
 * desktop `BillingSection`'s four-tab dashboard (Overview / Plans / Invoices
 * / Usage). Omitted deliberately: the usage bar chart, the invoice table,
 * and the plan-comparison grid all assume a wide viewport and are read-only
 * lookups a user can do from desktop; a phone's job here is "am I about to
 * run out of credits" and "can I upgrade," not full account admin.
 *
 * Reuses the same `useComputeSubscription` / `useWalletOverview` /
 * `useComputeUsage` / `useCreateCheckoutSession` hooks and `billingUtils`
 * formatters as desktop `billing.tsx`, so a balance or plan name never
 * disagrees between the two surfaces. "Upgrade" reuses the exact
 * `handleSubscribe` pattern from `PlansTab`: create a checkout session for
 * the cheapest compute plan and redirect to the hosted Stripe URL — Stripe
 * Checkout is mobile-native, so this needs no mobile-specific UI of its own.
 *
 * This whole screen only renders when `hasControlPlane` (see
 * `MobileSettingsScreen`'s row list) — billing has no meaning without a
 * control-plane backend.
 */

import { useMemo, useState } from "react";
import { CreditCard, Cpu, Wallet } from "lucide-react";
import {
  useComputeSubscription,
  useComputeUsage,
  useCreateCheckoutSession,
  usePlans,
  useWalletOverview,
} from "../../hooks/useCloudBillingQueries";
import {
  COMPUTE_PLAN_IDS,
  derivePlanDisplay,
  formatBillingError,
  formatCurrencyFromWalletFields,
  getWalletBalanceState,
  getWalletWarning,
  isBackendModalError,
  nanosFromFields,
} from "../Settings/cloud/billingUtils";
import { MobileSettingsSectionHeader } from "./MobileSettingsSectionHeader";

function redirectToStripe(url: string): boolean {
  if (!url) return false;
  if (url.startsWith(window.location.origin)) return false;
  window.location.href = url;
  return true;
}

function Card({ children }: { children: React.ReactNode }) {
  return (
    <div className="overflow-hidden rounded-lg elevation-1">
      <div className="p-4">{children}</div>
    </div>
  );
}

export function MobileBillingScreen({ onBack }: { onBack: () => void }) {
  const subQ = useComputeSubscription();
  const walletQ = useWalletOverview();
  const usageQ = useComputeUsage("current");
  const plansQ = usePlans();
  const checkoutMutation = useCreateCheckoutSession();
  const [error, setError] = useState("");

  const subscription = subQ.data?.subscription ?? null;
  const wallet = walletQ.data?.overview?.wallet ?? null;
  const usage = usageQ.data ?? null;

  const walletUi = useMemo(() => {
    const nanos = nanosFromFields(
      wallet?.balanceUsdNanos,
      wallet?.balanceUsdMicros,
      wallet?.balanceCents,
    );
    const state = getWalletBalanceState(nanos);
    return {
      balance: formatCurrencyFromWalletFields(
        wallet?.balanceUsdNanos,
        wallet?.balanceUsdMicros,
        wallet?.balanceCents,
      ),
      warning: getWalletWarning(state),
    };
  }, [wallet]);

  const planUi = useMemo(() => {
    const d = derivePlanDisplay(subscription?.plan);
    return {
      planName: subscription?.plan?.name ?? "No compute plan",
      includedHours:
        d.includedMinutes < 0 ? -1 : Math.round(d.includedMinutes / 60),
    };
  }, [subscription]);

  const usageUi = useMemo(() => {
    const includedHours = (usage?.includedMinutes ?? 0) / 60;
    const usedHours = (usage?.usedMinutes ?? 0) / 60;
    const pct =
      includedHours > 0 ? Math.min((usedHours / includedHours) * 100, 100) : 0;
    return { includedHours, usedHours, pct };
  }, [usage]);

  // The cheapest compute plan — a one-tap "Upgrade" needs a single target,
  // not the desktop plan-comparison grid. A user who wants a specific tier
  // still has the full picker on desktop.
  const cheapestPlan = (plansQ.data?.plans ?? [])
    .filter((plan) => (COMPUTE_PLAN_IDS as readonly string[]).includes(plan.id))
    .sort(
      (a, b) =>
        (COMPUTE_PLAN_IDS as readonly string[]).indexOf(a.id) -
        (COMPUTE_PLAN_IDS as readonly string[]).indexOf(b.id),
    )[0];

  const handleUpgrade = () => {
    if (!cheapestPlan) return;
    setError("");
    const returnUrl = window.location.href;
    checkoutMutation.mutate(
      { planId: cheapestPlan.id, successUrl: returnUrl, cancelUrl: returnUrl },
      {
        onSuccess: (res) => redirectToStripe(res.checkoutUrl),
        onError: (err) => {
          if (!isBackendModalError(err)) {
            setError(formatBillingError(err, "Failed to start checkout"));
          }
        },
      },
    );
  };

  const loading = subQ.isLoading || walletQ.isLoading || usageQ.isLoading;

  return (
    <div className="flex h-full min-h-0 flex-col">
      <MobileSettingsSectionHeader title="Billing" onBack={onBack} />

      <div className="min-h-0 flex-1 overflow-y-auto p-4">
        {loading ? (
          <p className="text-sm text-muted-foreground">Loading billing…</p>
        ) : (
          <div className="space-y-4">
            {error && (
              <div className="rounded-md border border-destructive/40 bg-destructive/10 px-4 py-3 text-sm text-destructive">
                {error}
              </div>
            )}

            <Card>
              <div className="mb-2 flex items-center gap-2">
                <Wallet className="h-4 w-4 text-muted-foreground" />
                <h3 className="text-sm font-semibold text-foreground">
                  Credit balance
                </h3>
              </div>
              <p className="text-2xl font-semibold text-foreground">
                {walletUi.balance}
              </p>
              {walletUi.warning && (
                <div className="mt-3 rounded-md border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs text-amber-600 dark:text-amber-400">
                  <p className="font-semibold">{walletUi.warning.title}</p>
                  <p>{walletUi.warning.message}</p>
                </div>
              )}
            </Card>

            <Card>
              <div className="mb-2 flex items-center gap-2">
                <Cpu className="h-4 w-4 text-muted-foreground" />
                <h3 className="text-sm font-semibold text-foreground">
                  Compute plan
                </h3>
              </div>
              <p className="text-lg font-semibold text-foreground">
                {planUi.planName}
              </p>
              <div className="mt-3">
                <div className="flex items-center justify-between text-xs">
                  <span className="text-muted-foreground">Hours used</span>
                  <span className="text-muted-foreground">
                    {usageUi.usedHours.toFixed(1)} h /{" "}
                    {planUi.includedHours < 0
                      ? "unlimited"
                      : `${usageUi.includedHours.toFixed(0)} h`}
                  </span>
                </div>
                {/* `bg-primary/10` track, not `bg-muted` — this card now uses
                    `elevation-1`, and in dark mode `--surface-raised` (the
                    card's own background) resolves to `--muted`, so a
                    `bg-muted` track would vanish into the card around it. */}
                <div className="mt-1.5 h-2 rounded-full bg-primary/10">
                  <div
                    className={
                      "h-2 rounded-full " +
                      (usageUi.pct >= 90 ? "bg-amber-500" : "bg-primary")
                    }
                    style={{ width: `${usageUi.pct}%` }}
                  />
                </div>
              </div>
              {cheapestPlan && (
                <button
                  type="button"
                  onClick={handleUpgrade}
                  disabled={checkoutMutation.isPending}
                  className="mt-4 flex min-h-[44px] w-full items-center justify-center gap-2 rounded-lg bg-primary text-sm font-medium text-primary-foreground active:opacity-80 disabled:opacity-60"
                >
                  <CreditCard className="h-4 w-4" />
                  {checkoutMutation.isPending ? "Opening checkout…" : "Upgrade"}
                </button>
              )}
            </Card>

            <p className="text-center text-xs text-muted-foreground">
              For invoices, usage charts, and plan comparisons, use desktop.
            </p>
          </div>
        )}
      </div>
    </div>
  );
}
