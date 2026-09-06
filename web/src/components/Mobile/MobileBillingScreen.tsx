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
 * `useComputeUsage` hooks and `billingUtils` formatters as desktop
 * `billing.tsx`, so a balance or plan name never disagrees between the two
 * surfaces.
 *
 * "Upgrade" mounts `EmbeddedCheckoutPanel` in place rather than redirecting to
 * a hosted Stripe URL. Mobile here is mobile WEB — the same React app at
 * `/m/settings`, on the same registered origin — so embedded checkout needs no
 * mobile-specific work at all; it is the narrowest container, not a different
 * integration. The redirect was also the worst version of this flow on a
 * phone, where returning to a backgrounded tab is least reliable.
 *
 * This whole screen only renders when `hasControlPlane` (see
 * `MobileSettingsScreen`'s row list) — billing has no meaning without a
 * control-plane backend.
 */

import { useCallback, useMemo, useState } from "react";
import { Cpu, Wallet } from "lucide-react";
import {
  useComputeSubscription,
  useComputeUsage,
  usePlans,
  useWalletOverview,
} from "../../hooks/useCloudBillingQueries";
import { CheckoutPanelWithIdentity } from "../Billing/CheckoutPanelWithIdentity";
import {
  derivePlanDisplay,
  isPurchasableComputePlan,
  sortPlansForDisplay,
  formatAllowedSizes,
  formatCentsAsDollars,
  formatCurrencyFromWalletFields,
  getWalletBalanceState,
  getWalletWarning,
  nanosFromFields,
  // The two-band derivations, shared with desktop so a phone and a laptop can
  // never disagree about whether a plan's detail loaded.
  deriveComputeCapacity,
  isPlanDetailUnavailable,
} from "../Settings/cloud/billingUtils";
import { MobileSettingsSectionHeader } from "./MobileSettingsSectionHeader";

function Card({ children }: { children: React.ReactNode }) {
  return (
    <div className="overflow-hidden rounded-lg elevation-1">
      <div className="p-4">{children}</div>
    </div>
  );
}

/**
 * The two products, as two SHAPES — the same distinction desktop makes, at a
 * phone's width.
 *
 * Credit is a filled, borderless reservoir; compute is a bordered contract.
 * They differ by container, by primary number (dollars remaining vs hours used
 * of hours included) and by time axis, not by colour: the styling contract
 * permits no invented palette, and a colour split would not survive a theme
 * change anyway. Two identically-drawn cards are exactly as monotone here as
 * they were on desktop, and the smaller screen leaves less room for the copy
 * that would otherwise disambiguate them.
 */
function Band({
  id,
  title,
  icon: Icon,
  variant,
  children,
}: {
  id: string;
  title: string;
  icon: typeof Wallet;
  variant: "reservoir" | "contract";
  children: React.ReactNode;
}) {
  return (
    <section
      aria-labelledby={id}
      // The shape is DECLARED, not merely styled.
      //
      // "Distinguish the two products by shape rather than colour" is the
      // design decision this screen exists to carry, and a test can only pin a
      // class string — which pins styling, breaks on every visual tweak, and
      // still would not say WHICH product got which treatment. Naming the
      // choice makes it a contract: swapping both bands to one container is
      // then a detectable regression rather than an invisible one.
      data-band-shape={variant}
      className={
        variant === "reservoir"
          ? "rounded-2xl bg-muted/40 p-4"
          : "rounded-lg border border-border bg-card p-4"
      }
    >
      <div className="mb-2 flex items-center gap-2">
        <Icon className="h-4 w-4 text-muted-foreground" />
        <h3
          id={id}
          className="text-xs font-semibold uppercase tracking-wide text-muted-foreground"
        >
          {title}
        </h3>
      </div>
      {children}
    </section>
  );
}

export function MobileBillingScreen({ onBack }: { onBack: () => void }) {
  const subQ = useComputeSubscription();
  const walletQ = useWalletOverview();
  const usageQ = useComputeUsage("current");
  const plansQ = usePlans();
  // The purchase in flight, or null. Holding the REQUEST (not a boolean) is
  // what lets the panel key its session off it — reopening the same purchase
  // reuses the session instead of minting another.
  const [checkout, setCheckout] = useState<{
    kind: "compute_plan";
    planId: string;
  } | null>(null);

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
    const plan = subscription?.plan;
    const d = derivePlanDisplay(plan);
    return {
      // null, not "No compute plan": absence is a different state from a plan
      // whose detail failed to load, and the two get different copy.
      planName: plan?.name ?? null,
      includedHours:
        d.includedMinutes < 0 ? -1 : Math.round(d.includedMinutes / 60),
      // The screenshot's `0 h` state. On a phone a bare zero is MORE alarming,
      // not less — there is no surrounding detail to contradict it.
      detailUnavailable: isPlanDetailUnavailable(plan),
    };
  }, [subscription]);

  const usageUi = useMemo(() => {
    const includedMinutes = usage?.includedMinutes ?? 0;
    const usedMinutes = usage?.usedMinutes ?? 0;
    // A stubbed or unmeasurable period answers 200 with used_minutes = 0, so
    // "ran nothing" and "we didn't measure" arrive identical. Rendering the
    // second as "0.0 h used" told a user they had consumed nothing while the
    // daemon start gate was refusing them against real metered usage — and a
    // phone has no surrounding detail to contradict it. `usageMeasured` is
    // false by default, so an older server is read as unknown, not as zero.
    const measured = !!usage && usage.usageMeasured;
    return {
      measured,
      includedHours: includedMinutes / 60,
      usedHours: usedMinutes / 60,
      capacity: deriveComputeCapacity({
        usedMinutes,
        includedMinutes,
        overageMinutes: usage?.overageMinutes ?? 0,
      }),
    };
  }, [usage]);

  // Every plan the catalog sells, in the catalog's own order. This screen
  // used to take the FIRST of these and buy it on one tap — a purchase
  // decision made for the user, who saw no alternative and (originally) no
  // price at all. A phone is a smaller screen, not a reason to remove the
  // choice; the plans are a short list and each one fits on a row.
  //
  // Which plans exist, what they cost and what order they come in are all
  // server facts (price_cents, display_order), not a client allowlist.
  const purchasablePlans = sortPlansForDisplay(
    (plansQ.data?.plans ?? []).filter(isPurchasableComputePlan),
  );

  // The panel reports done only after the SERVER confirmed the purchase (or
  // after the dev no-Stripe path completed it outright), never off Stripe's
  // in-page onComplete. So refetching here is safe: there is something to
  // fetch.
  const handleCheckoutDone = useCallback(() => {
    setCheckout(null);
    void subQ.refetch();
    void walletQ.refetch();
  }, [subQ, walletQ]);

  const loading = subQ.isLoading || walletQ.isLoading || usageQ.isLoading;

  return (
    <div className="flex h-full min-h-0 flex-col">
      <MobileSettingsSectionHeader title="Billing" onBack={onBack} />

      <div className="min-h-0 flex-1 overflow-y-auto p-4">
        {loading ? (
          <p className="text-sm text-muted-foreground">Loading billing…</p>
        ) : (
          <div className="space-y-4">
            {checkout && (
              <Card>
                <CheckoutPanelWithIdentity
                  request={checkout}
                  onDone={handleCheckoutDone}
                  // "Open Settings on desktop" was the old advice because this
                  // screen had no way to link an identity. It has one now — the
                  // same modal every other surface uses — so the phone is no
                  // longer a dead end.
                  returnTo="/settings/billing"
                />
                <button
                  type="button"
                  onClick={() => setCheckout(null)}
                  className="mt-3 min-h-[44px] w-full text-sm text-muted-foreground"
                >
                  Cancel
                </button>
              </Card>
            )}

            {/* AI credit: a meter that drains. */}
            <Band
              id="m-credit-band"
              title="AI credit"
              icon={Wallet}
              variant="reservoir"
            >
              <p className="text-2xl font-semibold text-foreground">
                {walletUi.balance}
              </p>
              <p className="mt-0.5 text-xs text-muted-foreground">
                credit remaining
              </p>
              {walletUi.warning && (
                // Semantic `warning` tokens, not a hardcoded amber pair. The
                // literal `amber-500` here was a brand colour by another name:
                // it ignored the theme and needed a `dark:` twin to stay legible.
                <div className="mt-3 rounded-md border border-warning/30 bg-warning/10 px-3 py-2 text-xs text-warning">
                  <p className="font-semibold">{walletUi.warning.title}</p>
                  <p>{walletUi.warning.message}</p>
                </div>
              )}
            </Band>

            {/* Compute: a capacity with a ceiling. */}
            <Band
              id="m-compute-band"
              title="Compute"
              icon={Cpu}
              variant="contract"
            >
              <p className="text-lg font-semibold text-foreground">
                {planUi.planName ?? "No compute plan"}
              </p>
              {planUi.planName === null ? (
                <p className="mt-1 text-xs text-muted-foreground">
                  Machines run on a plan — pick one below to get started.
                </p>
              ) : planUi.detailUnavailable ? (
                // ONE line, not a confident `0 h`. Naming the cause is what
                // stops someone auditing config that is already correct.
                <p className="mt-2 text-xs text-muted-foreground">
                  Plan details are unavailable — the control plane may not have
                  restarted since the plan catalog changed.
                </p>
              ) : !usageUi.measured ? (
                // The allowance is a plan fact and survives; only the metered
                // half is withheld, and it is withheld by SAYING so. A blank
                // where hours-used belongs reads as zero, which is the exact
                // reading this branch exists to prevent. No bar either — an
                // empty track is the same claim in another form.
                <div className="mt-3 text-xs text-muted-foreground">
                  <p>Usage unavailable for this period.</p>
                  {planUi.includedHours >= 0 && (
                    <p className="mt-0.5">
                      Your plan includes {usageUi.includedHours.toFixed(0)} h.
                    </p>
                  )}
                </div>
              ) : (
                <div className="mt-3">
                  <div className="flex items-center justify-between text-xs">
                    <span className="text-muted-foreground">
                      {usageUi.usedHours.toFixed(1)} h used
                    </span>
                    <span className="text-muted-foreground">
                      {planUi.includedHours < 0
                        ? "unlimited"
                        : `${usageUi.includedHours.toFixed(0)} h included`}
                    </span>
                  </div>
                  {/* `bg-primary/10` track, not `bg-muted` — in dark mode
                      `--surface-raised` resolves to `--muted`, so a `bg-muted`
                      track vanishes into the surface around it. */}
                  <div className="mt-1.5 h-2 rounded-full bg-primary/10">
                    <div
                      className={
                        "h-2 rounded-full " +
                        (usageUi.capacity.state === "under"
                          ? "bg-primary"
                          : "bg-warning")
                      }
                      style={{ width: `${usageUi.capacity.usedPct}%` }}
                    />
                  </div>
                </div>
              )}
              {purchasablePlans.length > 0 && !checkout && (
                <div className="mt-4 space-y-2">
                  <p className="text-xs font-medium text-muted-foreground">
                    {subscription ? "Switch plan" : "Choose a plan"}
                  </p>
                  {purchasablePlans.map((plan) => {
                    const d = derivePlanDisplay(plan);
                    const isCurrent = subscription?.plan?.id === plan.id;
                    const hours =
                      d.includedMinutes < 0
                        ? "unlimited hours"
                        : `${Math.round(d.includedMinutes / 60)} h/mo`;
                    return (
                      <button
                        key={plan.id}
                        type="button"
                        disabled={isCurrent}
                        onClick={() =>
                          setCheckout({ kind: "compute_plan", planId: plan.id })
                        }
                        className="flex min-h-[44px] w-full items-center justify-between gap-3 rounded-lg border border-border px-3 py-2 text-left active:opacity-80 disabled:opacity-60"
                      >
                        <span className="min-w-0">
                          <span className="block truncate text-sm font-medium text-foreground">
                            {plan.name}
                          </span>
                          <span className="block text-xs text-muted-foreground">
                            {/* Sizes are the reason to pick one plan over
                                another, so they are on the row rather than a
                                tap away on desktop. */}
                            {formatAllowedSizes(d.allowedSizes)} · {hours}
                          </span>
                        </span>
                        <span className="shrink-0 text-sm font-semibold text-foreground">
                          {isCurrent
                            ? "Current"
                            : `${formatCentsAsDollars(d.monthlyPriceCents ?? 0)}/mo`}
                        </span>
                      </button>
                    );
                  })}
                  <p className="pt-1 text-center text-xs text-muted-foreground">
                    {/* Says what now happens — payment opens on this screen and
                        never leaves it. The previous copy promised a round trip
                        ("you'll come back here"), which stopped being true when
                        the redirect went away. Deliberately names no payment
                        methods: which ones appear depends on the browser and on
                        domain registration, so Stripe's own form is the only
                        honest place that list can come from. */}
                    Pay securely with Stripe, right here.
                  </p>
                </div>
              )}
            </Band>

            <p className="text-center text-xs text-muted-foreground">
              For invoices, usage charts, and plan comparisons, use desktop.
            </p>
          </div>
        )}
      </div>
    </div>
  );
}
