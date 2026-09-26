import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";
import {
  ArrowLeft,
  ArrowUpRight,
  BarChart3,
  CheckCircle2,
  CreditCard,
  Cpu,
  Download,
  FileText,
  Loader2,
  Pencil,
  Server,
} from "lucide-react";

import {
  Badge,
  Button,
  Card,
  CardContent,
  CardHeader,
  CardTitle,
  EmptyState,
  PageHeader,
  Table,
  Tbody,
  Td,
  Th,
  Thead,
  Tr,
} from "./ui";
import { ComputeOverageControl } from "./ComputeOverageControl";
import type { Plan } from "@/gen/controlplane/controlplane/v1/shared_pb";
import {
  useBillingEmail,
  useComputeSubscription,
  useComputeUsage,
  useCreateBillingPortalSession,
  useCurrentUserInvoices,
  usePlans,
  useSetComputeOverage,
  useUpdateBillingEmail,
  useWalletOverview,
} from "@/hooks/useCloudBillingQueries";
import {
  TOPUP_PRESETS_CENTS,
  derivePlanDisplay,
  isPurchasableComputePlan,
  sortPlansForDisplay,
  formatAllowedSizes,
  formatBillingError,
  formatCentsAsDollars,
  formatCurrencyFromWalletFields,
  formatDayLabel,
  formatOverageRate,
  formatTimestampDate,
  getWalletBalanceState,
  getWalletWarning,
  isBackendModalError,
  isUnpricedComputePlan,
  nanosFromFields,
  normalizeInvoiceStatus,
  deriveComputeCapacity,
  estimateCreditRunwayDays,
  isPlanDetailUnavailable,
} from "./billingUtils";
import { useLLMSpend } from "@/hooks/useReliantAIQueries";
import {
  ComputeSubscriptionCheckout,
  type ComputePlanOption,
} from "@/components/Billing/ComputeSubscriptionCheckout";
// The machine list, its derivation, and the uniform facts beneath it — shared
// with the onboarding compute step so one choice has one presentation.
import { PlanFinePrint, PlanTiles, describeIncludedHours } from "@/components/Billing/PlanTiles";
import { deriveMachineOptions } from "@/components/Billing/machineOptions";
import { MACHINE_BURST } from "@/components/Billing/machineSpecs";
import { LinkIdentityModal } from "@/components/Billing/LinkIdentityModal";
import { WalletTopupCheckout } from "@/components/Billing/WalletTopupCheckout";
import { formatMachineMinutesShort } from "@/lib/formatMachineMinutes";
import { buildCheckoutReturnUrls, openCheckout } from "@/lib/stripeCheckout";
import {
  VISIBLE_BILLING_TABS,
  resolveBillingTab,
  type BillingTab,
  type VisibleBillingTab,
} from "@/routeSchemas";
// The two bands. Each owns ONE product's presentation and none of its business
// rules — they take resolved props and call no mutation, so every purchase
// still passes through useCloudBillingQueries' identity chokepoint.
import { CreditBand } from "./overview/CreditBand";
import { ComputeBand } from "./overview/ComputeBand";
import { StatusLine } from "./overview/StatusLine";
// Credit's budget control — the wallet twin of ComputeOverageControl. Until it
// landed here, the auto-recharge RPCs had exactly one caller (OutOfCreditModal),
// so the feature was offered only after the user had already run out.
import { WalletAutoRechargeSection } from "./WalletAutoRechargeControl";
// Deployment (infra) usage + its spending ceiling — the cpu/memory/storage
// twin of the compute surface above, writing SetCurrentUserInfraOverage rather
// than the compute pair. Ported from control-plane's internal-console, which
// proxies to admin-api and therefore could never reach these RPCs at all.
import { DeployUsageSection } from "./usage/DeployUsageSection";
import { useDeployUsage } from "./usage/useDeployUsage";

/**
 * Three tabs, not four.
 *
 * "Plans" became "Change plan" because it is a verb and the tab is entered to
 * DO something. Invoices folded into Usage because an invoice is the settled
 * record of a period's usage — one question, "what did I spend and when", and
 * splitting it made the user check two tabs to reconcile one number.
 *
 * `invoices` survives as an accepted INBOUND value (`resolveBillingTab`),
 * because external links already carry `?tab=invoices`.
 */
const TAB_META: Record<
  VisibleBillingTab,
  { label: string; icon: typeof CreditCard }
> = {
  overview: { label: "Overview", icon: CreditCard },
  plans: { label: "Change plan", icon: Cpu },
  usage: { label: "Usage & invoices", icon: BarChart3 },
};
const TABS = VISIBLE_BILLING_TABS.map((id) => ({ id, ...TAB_META[id] }));

/**
 * BillingSection — the end-user billing dashboard rendered inside
 * `/settings/billing`. Ported from control-plane admin-web's billing pages
 * but scoped to the PUBLIC "current user" BillingService RPCs and
 * re-skinned against reliant's semantic theme tokens (bg-card / text-muted-
 * foreground / bg-primary …) so it flips with dark mode.
 *
 * Sub-navigation (overview / plans / invoices / usage) lives in the URL as
 * `?tab=`, NOT in component state. It used to be `useState` on the argument
 * that tab state "never touches the router" so it composes under the settings
 * shell — right while nothing needed to link INTO a tab, and the thing that
 * dropped the user's intent once something did. A user who clicked "Set up
 * billing" asked for plans; unaddressable state cannot carry that across a
 * Stripe or OAuth round trip, so they landed on a usage dashboard and had to
 * find the Plans tab themselves.
 *
 * The three "open Stripe" entry points (checkout, wallet top-up, billing
 * portal) hand off through `lib/stripeCheckout`, which marks the return
 * `?checkout=success|cancelled` and keeps the round trip inside the desktop
 * app. The global upgradeInterceptor handles the billing-email-missing / quota
 * modals, so this component only renders inline errors for OTHER failures.
 */
export function BillingSection() {
  const navigate = useNavigate();
  const search = useSearch({ strict: false }) as {
    // The WIDE set: `invoices` still arrives from external links.
    tab?: BillingTab;
    checkout?: "success" | "cancelled";
    planId?: string;
    from?: "onboarding";
    returnTo?: string;
  };
  // An inbound `?tab=invoices` is a real link somebody has; it resolves to the
  // merged surface rather than being stripped and silently landing on Overview.
  const tab: VisibleBillingTab = resolveBillingTab(search.tab);

  const setTab = (next: VisibleBillingTab) => {
    void navigate({
      to: ".",
      search: (prev: Record<string, unknown>) => ({ ...prev, tab: next }),
      replace: true,
    });
  };

  const clearCheckout = () => {
    void navigate({
      to: ".",
      search: (prev: Record<string, unknown>) => ({
        ...prev,
        checkout: undefined,
        planId: undefined,
      }),
      replace: true,
    });
  };

  return (
    <div className="flex min-w-0 flex-col gap-6">
      <PageHeader
        title="Billing"
        subtitle="Manage wallet credits, your compute plan, usage, and invoices."
      />

      {/* The route home, hoisted out of the Stripe-return banner.
          It used to render only alongside `?checkout=`, because a hosted
          round trip was the only way a mid-wizard user ever got back here.
          Payment now happens in place and sets no such marker, so a user who
          detoured from onboarding to look at plans would have had no way back
          at all. It is a property of HOW THEY ARRIVED, not of any purchase. */}
      {search.from === "onboarding" && (
        <BackToSetup returnTo={search.returnTo} />
      )}

      {search.checkout && (
        <CheckoutReturnBanner
          outcome={search.checkout}
          planId={search.planId}
          onDismiss={clearCheckout}
        />
      )}

      <div className="overflow-x-auto border-b border-border">
        <div role="tablist" aria-label="Billing" className="flex w-max min-w-full gap-1">
          {TABS.map(({ id, label, icon: Icon }) => {
            const active = tab === id;
            return (
              <button
                key={id}
                type="button"
                role="tab"
                aria-selected={active}
                onClick={() => setTab(id)}
                className={[
                  "inline-flex shrink-0 items-center gap-2 border-b-2 px-3 py-2 text-sm font-medium transition-colors sm:px-4",
                  active
                    ? "border-primary text-foreground"
                    : "border-transparent text-muted-foreground hover:text-foreground",
                ].join(" ")}
              >
                <Icon className="h-4 w-4" />
                {label}
              </button>
            );
          })}
        </div>
      </div>

      {tab === "overview" && (
        <OverviewTab
          onGoToPlans={() => setTab("plans")}
          onGoToUsage={() => setTab("usage")}
        />
      )}
      {tab === "plans" && <PlansTab />}
      {tab === "usage" && <UsageAndInvoicesTab />}
    </div>
  );
}

/**
 * What the user sees on the way back from Stripe.
 *
 * The success state is deliberately NOT a claim that the subscription exists.
 * `?checkout=success` is a client-side marker a user can type, and entitlement
 * only becomes real when Stripe's webhook lands and the subscription query
 * reports it. So this shows "confirming your payment…" while it polls, and only
 * names the plan once the SERVER has it. That gap used to be invisible: the
 * user returned to an unchanged Overview tab showing their old plan, with no
 * indication anything was in flight.
 */
function CheckoutReturnBanner({
  outcome,
  planId,
  onDismiss,
}: {
  outcome: "success" | "cancelled";
  planId?: string;
  onDismiss: () => void;
}) {
  const subQ = useComputeSubscription();
  const confirmedPlan = subQ.data?.subscription?.plan ?? null;
  const confirmed = !!confirmedPlan && (!planId || confirmedPlan.id === planId);

  // Poll while the webhook is in flight, and give up after a minute — an
  // unbounded poll against a purchase that genuinely failed would spin forever.
  //
  // Depends on `refetch`, which react-query keeps stable, NOT on `subQ`: the
  // query result is a fresh object on every render, so depending on it would
  // tear down and re-create the interval each time and the 60s stop would keep
  // resetting.
  const refetchSubscription = subQ.refetch;
  useEffect(() => {
    if (outcome !== "success" || confirmed) return;
    const timer = setInterval(() => void refetchSubscription(), 2000);
    const stop = setTimeout(() => clearInterval(timer), 60_000);
    return () => {
      clearInterval(timer);
      clearTimeout(stop);
    };
  }, [outcome, confirmed, refetchSubscription]);

  if (outcome === "cancelled") {
    return (
      <div className="flex flex-col gap-3 rounded-md border border-border bg-card px-4 py-3 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <p className="text-sm font-medium text-foreground">
            Checkout wasn&apos;t completed
          </p>
          <p className="text-sm text-muted-foreground">
            Nothing was charged. Your plan is unchanged — pick one below
            whenever you&apos;re ready.
          </p>
        </div>
        <Button size="sm" variant="ghost" onClick={onDismiss}>
          Dismiss
        </Button>
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-3 rounded-md border border-primary/40 bg-primary/10 px-4 py-3 sm:flex-row sm:items-center sm:justify-between">
      <div className="flex items-start gap-2">
        {confirmed ? (
          <CheckCircle2 className="mt-0.5 h-4 w-4 shrink-0 text-primary" />
        ) : (
          <Loader2 className="mt-0.5 h-4 w-4 shrink-0 animate-spin text-primary" />
        )}
        <div>
          <p className="text-sm font-medium text-foreground">
            {confirmed
              ? `Your ${confirmedPlan.name} plan is active`
              : "Confirming your payment…"}
          </p>
          <p className="text-sm text-muted-foreground">
            {confirmed
              ? "You can start a cloud machine now."
              : "Stripe has your payment. This usually takes a few seconds."}
          </p>
        </div>
      </div>
      {confirmed && (
        <Button size="sm" variant="ghost" onClick={onDismiss}>
          Dismiss
        </Button>
      )}
    </div>
  );
}

/**
 * The way back into onboarding, for a user who detoured here mid-wizard.
 *
 * This used to live inside `CheckoutReturnBanner`, which meant it rendered
 * only on the way back from a hosted Stripe round trip. Payment now completes
 * in place and sets no `?checkout=` marker at all, so tying the route home to
 * that marker would have silently deleted it — the user would arrive from the
 * compute step, buy a plan, and find no door back.
 *
 * It is a property of how the user ARRIVED (`from=onboarding`), so it renders
 * whenever that is true, purchase or no purchase.
 *
 * `returnTo` comes off the address bar and is guarded the same way
 * GitHubOAuthCallback guards its own: same-origin relative paths only, never
 * protocol-relative (`//evil.com`). Anything else falls back to /onboarding,
 * which is still the right destination — just without the resume. A bare
 * /onboarding gives deriveStep an empty plan and restarts the wizard, since
 * the whole plan lives in the `plan` search param.
 */
function BackToSetup({ returnTo }: { returnTo?: string }) {
  const navigate = useNavigate();

  const safeReturnTo =
    returnTo && returnTo.startsWith("/") && !returnTo.startsWith("//")
      ? returnTo
      : undefined;

  return (
    <div className="flex items-center justify-between gap-3 rounded-md border border-border bg-card px-4 py-3">
      <p className="text-sm text-muted-foreground">
        You came here from setup. Pick a plan and you can head straight back.
      </p>
      <Button
        size="sm"
        variant="outline"
        onClick={() => {
          if (safeReturnTo) {
            // href navigation: returnTo is an arbitrary path+query string,
            // which is exactly the cross-route schema friction
            // GitHubOAuthCallback documents. The router still owns the
            // transition (no cold boot).
            void navigate({ href: safeReturnTo });
            return;
          }
          void navigate({ to: "/onboarding", search: {} });
        }}
      >
        <ArrowLeft className="h-4 w-4" />
        Back to setup
      </Button>
    </div>
  );
}

// ── Shared bits ─────────────────────────────────────────────────────────

function ErrorBanner({ message }: { message: string }) {
  if (!message) return null;
  return (
    <div className="rounded-md border border-destructive/40 bg-destructive/10 px-4 py-3 text-sm text-destructive">
      {message}
    </div>
  );
}

function Loading({ label }: { label: string }) {
  return <div className="text-sm text-muted-foreground">{label}</div>;
}

// Groups a run of cards under one label so the Overview tab reads as three
// answers — what you have, what you are on, what you have used — instead of an
// undifferentiated stack of equally-weighted panels.
function SectionHeading({
  title,
  description,
}: {
  title: string;
  description: string;
}) {
  return (
    <div>
      <h3 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
        {title}
      </h3>
      <p className="mt-0.5 text-sm text-muted-foreground">{description}</p>
    </div>
  );
}

// Told before it happens, not discovered mid-flight. Every remaining
// navigation in this flow names its destination and says the user comes back.
function LeavingNotice({ children }: { children: React.ReactNode }) {
  return <p className="text-xs text-muted-foreground">{children}</p>;
}

// ── Overview tab ────────────────────────────────────────────────────────

/**
 * Two bands, two shapes.
 *
 * This used to build both products' UI inline, from one template — `walletUi`
 * and `planUi` were literally parallel `useMemo` blocks rendered into the same
 * `<Card><CardHeader><CardTitle><Icon/>` scaffold. The visual sameness the
 * owner called "monotone" was a faithful rendering of a structural sameness
 * that should never have existed: a wallet is a quantity you deplete, a
 * subscription is a capacity you rent.
 *
 * So this composes `CreditBand` and `ComputeBand` and owns only the
 * data-resolution between them. Neither band calls a mutation; every purchase
 * still goes through `useCloudBillingQueries`, where `assertPurchaseIdentity`
 * refuses an anonymous session.
 */
function OverviewTab({
  onGoToPlans,
  onGoToUsage,
}: {
  onGoToPlans: () => void;
  onGoToUsage: () => void;
}) {
  const subQ = useComputeSubscription();
  const walletQ = useWalletOverview();
  const usageQ = useComputeUsage("current");
  const [error, setError] = useState("");
  // The top-up amount in flight, in cents, or null when none is open. Holding
  // the AMOUNT rather than a boolean is what lets the form key its intent off
  // it: reopening the same amount reuses the in-flight intent instead of
  // minting a second one. This band only ever buys credit, so a plain number
  // says that where the old checkout union did not.
  const [topupCents, setTopupCents] = useState<number | null>(null);

  const overageMutation = useSetComputeOverage();
  const portalMutation = useCreateBillingPortalSession();

  const subscription = subQ.data?.subscription ?? null;
  const wallet = walletQ.data?.overview?.wallet ?? null;
  const usage = usageQ.data ?? null;

  // The 30-day AI spend window the AI page already loads. It is here for ONE
  // number — the runway — so it is deliberately not allowed to gate anything:
  // if it fails or is empty, the credit band drops one line and every control
  // still works.
  const orgId = walletQ.data?.overview?.organization?.id ?? "";
  const spendWindow = useMemo(() => {
    const end = new Date();
    const start = new Date(end);
    start.setDate(end.getDate() - 30);
    const iso = (d: Date) => d.toISOString().slice(0, 10);
    return { startDate: iso(start), endDate: iso(end) };
  }, []);
  const spendQ = useLLMSpend({ orgId, ...spendWindow });

  /**
   * Credit's shape: a quantity that depletes, plus how fast.
   *
   * `balance: null` is a distinct state from `$0.00` and the band renders it
   * differently — a read we could not make must never present as a balance of
   * zero next to a button that spends against it.
   */
  const creditUi = useMemo(() => {
    if (walletQ.error && !wallet) {
      return {
        balance: null,
        warning: null,
        runwayDays: null,
        meterFraction: 0,
        balanceNanos: BigInt(0),
      };
    }
    const nanos = nanosFromFields(
      wallet?.balanceUsdNanos,
      wallet?.balanceUsdMicros,
      wallet?.balanceCents,
    );
    // The day count comes from the SERVER, not from the entries.
    //
    // This used to be `spendSampleDays(entries)`, counting distinct
    // `entry.periodStart` values — a field GetLLMSpend has never populated and
    // structurally cannot, since entries are aggregated per (key, model)
    // across the whole range. The count was therefore 0 on every real
    // response, always below the minimum, and this headline feature rendered
    // for nobody while its test passed against a fixture that invented the
    // shape.
    //
    // 0 is the honest "we cannot say" and estimateCreditRunwayDays withholds
    // on it, which is why no `?? entries.length` fallback belongs here: a
    // plausible substitute denominator is exactly what turns a withheld
    // estimate into a confident wrong one.
    const runwayDays = estimateCreditRunwayDays(
      nanos,
      spendQ.data?.totalSpend ?? 0,
      spendQ.data?.sampleDays ?? 0,
    );
    return {
      balance: formatCurrencyFromWalletFields(
        wallet?.balanceUsdNanos,
        wallet?.balanceUsdMicros,
        wallet?.balanceCents,
      ),
      warning: getWalletWarning(getWalletBalanceState(nanos)),
      runwayDays,
      // The meter is a RELATIVE reading — a wallet has no maximum — so it is
      // drawn against the largest top-up on offer. Above that it simply reads
      // full, which is the honest rendering of "you have plenty".
      meterFraction: Math.min(
        Number(nanos) /
          1_000_000_000 /
          (TOPUP_PRESETS_CENTS[TOPUP_PRESETS_CENTS.length - 1] / 100),
        1,
      ),
      balanceNanos: nanos,
    };
  }, [wallet, walletQ.error, spendQ.data]);

  /** Compute's shape: a capacity, its ceiling, and how far past it we are. */
  const computeUi = useMemo(() => {
    const plan = subscription?.plan;
    const d = derivePlanDisplay(plan);
    const degraded = isPlanDetailUnavailable(plan);
    // Two roads to "we don't know what you used", and they must converge.
    //
    // The query erroring was the only one the page recognised. But the server
    // can also answer 200 with nothing to say — it did exactly that for
    // months, from a stub — and `usedMinutes: 0` on a successful response is
    // indistinguishable from a real zero unless the server states which it
    // is. `usageMeasured` is that statement, and it is `false` by default, so
    // a server too old to send it is read as "unknown" rather than as
    // "measured zero" — the direction that withholds rather than asserts.
    const usageFailed = (!!usageQ.error && !usage) || (!!usage && !usage.usageMeasured);

    const includedMinutes = usage?.includedMinutes ?? d.includedMinutes;
    // Only ever read when `usageFailed` is false. Every consumer below is
    // gated on it, so no unmeasured zero reaches a label.
    const usedMinutes = usage?.usedMinutes ?? 0;

    return {
      planName: plan?.name ?? null,
      pricePerMonthLabel:
        d.monthlyPriceCents !== null
          ? `${formatCentsAsDollars(d.monthlyPriceCents)}/mo`
          : null,
      renewsOnLabel: subscription?.currentPeriodEnd
        ? `renews ${formatTimestampDate(subscription.currentPeriodEnd)}`
        : null,
      // Degraded and absent both suppress these rather than rendering a
      // confident zero — `0 h / mo` was the whole complaint.
      includedHoursLabel:
        degraded || includedMinutes < 0
          ? null
          : `${Math.round(includedMinutes / 60)} h included`,
      // null when unmeasured, NOT "0.0 h". Every consumer already treats null
      // as "render nothing", which is the same discipline `includedHoursLabel`
      // above uses for a plan whose detail did not load.
      usedHoursLabel:
        degraded || usageFailed ? null : `${(usedMinutes / 60).toFixed(1)} h`,
      allowedSizesLabel: degraded ? null : formatAllowedSizes(d.allowedSizes),
      overageRateLabel: formatOverageRate(d.overageCentsPerMinute),
      capacity:
        degraded || usageFailed
          ? null
          : deriveComputeCapacity({
              usedMinutes,
              includedMinutes,
              overageMinutes: usage?.overageMinutes ?? 0,
            }),
      // Overage is metered too, so an unmeasured response must not produce a
      // dollar figure. Gated on `usageFailed` as well as on the value: "$0.00
      // so far" from a server that measured nothing is the same lie as
      // "0.0 h used", in the currency the user actually cares about.
      estimatedOverageCostLabel:
        !usageFailed && usage && usage.overageMinutes > 0
          ? formatCentsAsDollars(usage.estimatedOverageCostCents ?? 0)
          : null,
      grantedMinutesRemaining: usage?.grantedMinutesRemaining ?? 0,
      overageEnabled: subscription?.overageEnabled ?? false,
      // The overage control needs the raw numbers, not the formatted labels:
      // it converts a dollar limit to minutes live, and measures spend against
      // the cap. undefined budgetCents means no cap is stored.
      budgetCents: subscription?.budgetCents,
      overageCentsPerMinute: d.overageCentsPerMinute,
      // The overage control shows spend against the cap. null, not 0:
      // unmeasured rendered as zero spent tells a user their whole limit is
      // still available.
      overageSpentCents: usageFailed
        ? null
        : Number(usage?.estimatedOverageCostCents ?? 0),
      monthlyPriceCents: d.monthlyPriceCents,
      planDetailUnavailable: degraded,
      usageUnavailable: usageFailed,
    };
  }, [subscription, usage, usageQ.error]);

  /**
   * The cross-product answer to "am I about to be charged for anything?",
   * before any bar is read. Built only from facts we actually have — a status
   * line containing a dash would occupy the most prominent place on the page
   * to announce that we do not know.
   */
  const statusParts = useMemo(() => {
    const parts: string[] = [];
    if (computeUi.planName) parts.push(computeUi.planName);
    if (computeUi.usedHoursLabel && computeUi.includedHoursLabel) {
      parts.push(
        `${computeUi.usedHoursLabel} of ${computeUi.includedHoursLabel}`,
      );
    }
    if (creditUi.balance) parts.push(`${creditUi.balance} credit remaining`);
    return parts;
  }, [computeUi, creditUi]);

  // Adding credit mounts the same panel a plan purchase does. It used to mint
  // a session here and hand the URL to `openCheckout`, which navigated the
  // browser away — the AI half of "one place to spend money" was the half
  // that still left the page.
  const handleTopup = (amountCents: number) => {
    setError("");
    setTopupCents(amountCents);
  };

  // The panel reports done only once the SERVER confirmed (or the dev
  // no-Stripe path completed the purchase outright), never off Stripe's
  // in-page onComplete — so there is genuinely something to refetch.
  const handleCheckoutDone = useCallback(() => {
    setTopupCents(null);
    void walletQ.refetch();
  }, [walletQ]);

  /**
   * The wallet balance as this page already reads it everywhere else.
   *
   * Goes through `nanosFromFields` rather than reading `balanceUsdNanos`
   * directly, because the three precision fields are not all populated on
   * every response — the credit band above depends on that same fallback, and
   * a settlement check that read only one field could report "no money
   * arrived" about a balance the page is displaying.
   */
  const walletBalanceNanos = (
    data: typeof walletQ.data | undefined,
  ): bigint => {
    const w = data?.overview?.wallet;
    return nanosFromFields(w?.balanceUsdNanos, w?.balanceUsdMicros, w?.balanceCents);
  };

  /**
   * Has the top-up landed, according to the server?
   *
   * The wallet balance is the fact the whole band is drawn from, so asking it
   * again IS the confirmation — there is no separate "did it work" endpoint,
   * and adding one would be a second answer that could disagree with the
   * number on screen.
   *
   * Compares against the balance as it stood when the top-up started rather
   * than against zero: a user topping up a wallet that already has credit in
   * it would otherwise be told the payment had settled the instant they
   * pressed Pay.
   */
  const balanceAtTopupStart = useRef<bigint | null>(null);
  useEffect(() => {
    if (topupCents !== null && balanceAtTopupStart.current === null) {
      balanceAtTopupStart.current = walletBalanceNanos(walletQ.data);
    }
    if (topupCents === null) balanceAtTopupStart.current = null;
  }, [topupCents, walletQ.data]);

  const creditHasLanded = useCallback(async () => {
    try {
      const fresh = await walletQ.refetch();
      return (
        walletBalanceNanos(fresh.data) > (balanceAtTopupStart.current ?? 0n)
      );
    } catch {
      // One failed read is a reason to look again, not to call the payment
      // lost. The caller polls.
      return false;
    }
  }, [walletQ]);

  // Fires only from the control's Save button. This authorizes additional
  // spend, so it must never be reachable from an effect.
  const handleSaveOverage = (settings: {
    enabled: boolean;
    budgetCents?: bigint;
  }) => {
    setError("");
    overageMutation.mutate(settings, {
      onError: (err) => {
        if (!isBackendModalError(err)) {
          setError(formatBillingError(err, "Failed to update overage setting"));
        }
      },
    });
  };

  const handleManageStripe = () => {
    setError("");
    const returnUrl = buildCheckoutReturnUrls("/settings/billing").successUrl;
    portalMutation.mutate(returnUrl, {
      onSuccess: (res) => {
        void openCheckout(res.portalUrl, returnUrl);
      },
      onError: (err) => {
        if (!isBackendModalError(err)) {
          setError(formatBillingError(err, "Failed to open billing portal"));
        }
      },
    });
  };

  const loading = subQ.isLoading || walletQ.isLoading || usageQ.isLoading;

  const refetchAfterRedeem = () => {
    // A coupon can land as wallet credit OR as machine minutes, and the form
    // does not know which until the server answers, so refresh both readouts.
    void walletQ.refetch();
    void usageQ.refetch();
  };

  return (
    <div className="flex flex-col gap-8">
      <ErrorBanner message={error} />

      {loading ? (
        <Loading label="Loading billing…" />
      ) : (
        <>
          {/* One sentence, both products, before any bar is read. */}
          <StatusLine parts={statusParts} />

          {/* ── AI credit: a meter that drains ────────────────────────── */}
          <CreditBand
            balance={creditUi.balance}
            warning={creditUi.warning}
            runwayDays={creditUi.runwayDays}
            meterFraction={creditUi.meterFraction}
            topupInFlightCents={topupCents}
            onAddCredit={handleTopup}
            onRetryBalance={() => void walletQ.refetch()}
            onRedeemed={refetchAfterRedeem}
            checkout={
              topupCents !== null && (
                <div className="flex flex-col gap-3 border-t border-border/60 pt-4">
                  {/* Our own page. The amount is the BAND's — the preset row
                      above is the only amount picker on this surface, and it
                      stays visible and live while this is open, so changing
                      your mind is one click on the control you already used.
                      This page used to render a second grid of the same four
                      amounts under "How much credit?", which asked the user to
                      pick $25 twice. */}
                  <WalletTopupCheckout
                    amountCents={topupCents}
                    confirmSettlement={creditHasLanded}
                    onDone={handleCheckoutDone}
                  />
                  <div>
                    <Button
                      size="sm"
                      variant="ghost"
                      onClick={() => setTopupCents(null)}
                    >
                      Cancel
                    </Button>
                  </div>
                </div>
              )
            }
            spendControl={
              <WalletAutoRechargeSection
                // The same 30-day window the runway above is drawn from, so
                // the suggested top-up is sized from the user's OWN burn rate
                // rather than a constant. Null when the server could not
                // measure it — the control then falls back to a flat
                // suggestion rather than inventing a rate.
                dailySpendUsd={
                  spendQ.data && (spendQ.data.sampleDays ?? 0) > 0
                    ? spendQ.data.totalSpend / spendQ.data.sampleDays
                    : null
                }
                onAddCard={handleManageStripe}
              />
            }
          />

          {/* ── Compute: a capacity with a ceiling ────────────────────── */}
          <ComputeBand
            planName={computeUi.planName}
            pricePerMonthLabel={computeUi.pricePerMonthLabel}
            renewsOnLabel={computeUi.renewsOnLabel}
            allowedSizesLabel={computeUi.allowedSizesLabel}
            dimensions={[
              {
                id: "daemon_compute",
                includedHoursLabel: computeUi.includedHoursLabel,
                usedHoursLabel: computeUi.usedHoursLabel,
                capacity: computeUi.capacity,
                estimatedOverageCostLabel: computeUi.estimatedOverageCostLabel,
              },
            ]}
            grantedMinutesRemaining={computeUi.grantedMinutesRemaining}
            planDetailUnavailable={computeUi.planDetailUnavailable}
            usageUnavailable={computeUi.usageUnavailable}
            onChangePlan={onGoToPlans}
            onRetryUsage={() => void usageQ.refetch()}
            renderOverageControl={({ disabled, reason }) => (
              <ComputeOverageControl
                enabled={computeUi.overageEnabled}
                budgetCents={computeUi.budgetCents}
                overageCentsPerMinute={computeUi.overageCentsPerMinute}
                overageSpentCents={computeUi.overageSpentCents}
                monthlyPriceCents={computeUi.monthlyPriceCents}
                disabled={disabled}
                saving={overageMutation.isPending}
                disabledReason={reason}
                onSave={handleSaveOverage}
              />
            )}
          />

          <div>
            <button
              type="button"
              onClick={onGoToUsage}
              className="text-sm font-medium text-primary hover:underline"
            >
              See usage and invoices →
            </button>
          </div>

          {/* ── Account settings ──────────────────────────────────────── */}
          {/* Deliberately last and visibly quieter: the billing email and the
              Stripe portal are things you set once, not things you read. They
              previously sat at the same weight as the balance. */}
          <section className="flex flex-col gap-3 border-t border-border pt-6">
            <SectionHeading
              title="Billing account"
              description="Receipts, payment method, and Stripe's own portal."
            />
            <BillingEmailRow />
            <div>
              <Button
                variant="ghost"
                size="sm"
                onClick={handleManageStripe}
                disabled={portalMutation.isPending}
              >
                <ArrowUpRight className="h-4 w-4" />
                {portalMutation.isPending
                  ? "Opening…"
                  : "Manage payment method in Stripe"}
              </Button>
              <LeavingNotice>
                Opens Stripe&apos;s own billing portal. Close it when
                you&apos;re done and you&apos;ll be back here.
              </LeavingNotice>
            </div>
          </section>
        </>
      )}
    </div>
  );
}

// Billing-email row: shows which address Stripe currently sees and lets the
// user set an override inline (UpdateBillingEmail). Kept minimal — the global
// interceptor still surfaces the required-email modal on paid-action failures.
function BillingEmailRow() {
  const emailQ = useBillingEmail();
  const updateMutation = useUpdateBillingEmail();
  const [editing, setEditing] = useState(false);
  const [value, setValue] = useState("");
  const [error, setError] = useState("");

  const billingEmail = emailQ.data?.billingEmail ?? "";
  const fallbackEmail = emailQ.data?.fallbackEmail ?? "";
  const effective = billingEmail || fallbackEmail || "Not set";

  const startEdit = () => {
    setValue(billingEmail);
    setError("");
    setEditing(true);
  };

  const save = () => {
    setError("");
    updateMutation.mutate(value.trim(), {
      onSuccess: () => setEditing(false),
      onError: (err) => setError(formatBillingError(err, "Failed to save billing email")),
    });
  };

  return (
    <Card>
      <CardContent className="flex flex-col gap-3">
        <div className="flex flex-col items-start gap-3 sm:flex-row sm:items-center sm:justify-between">
          <div>
            <p className="text-sm font-medium text-foreground">Billing email</p>
            <p className="text-xs text-muted-foreground">
              Stripe issues receipts to{" "}
              <span className="font-medium text-foreground">{effective}</span>
            </p>
          </div>
          {!editing && (
            <Button size="sm" variant="ghost" onClick={startEdit}>
              <Pencil className="h-3.5 w-3.5" />
              {billingEmail ? "Change" : "Set"}
            </Button>
          )}
        </div>
        {editing && (
          <div className="flex flex-col gap-2">
            <div className="flex flex-wrap items-center gap-2">
              <input
                type="email"
                autoFocus
                placeholder="you@company.com"
                value={value}
                onChange={(e) => setValue(e.target.value)}
                disabled={updateMutation.isPending}
                className="min-w-[220px] flex-1 rounded-md border border-border bg-background px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-ring"
              />
              <Button
                size="sm"
                onClick={save}
                disabled={updateMutation.isPending}
                isLoading={updateMutation.isPending}
              >
                Save
              </Button>
              <Button
                size="sm"
                variant="ghost"
                onClick={() => setEditing(false)}
                disabled={updateMutation.isPending}
              >
                Cancel
              </Button>
            </div>
            <ErrorBanner message={error} />
          </div>
        )}
      </CardContent>
    </Card>
  );
}

// ── Plans tab: the purchase surface ─────────────────────────────────────
//
// ONE list of machines. Not a size filter, not a card grid, and not both at
// once — which is what this was.
//
// ── What was here, and why all three parts had to go ──────────────────
//
// The tab stacked THREE controls for one decision: a `SizePicker` radiogroup,
// then `ComputeSubscriptionCheckout` (which carries its OWN machine list) once
// a plan was chosen, and beneath both a four-across grid of plan `<Card>`s. So
// clicking a card mounted a second copy of the same choice ABOVE it while the
// cards stayed below. The owner's report — "the cards are the only first
// option, but when clicked pop these options up. however the cards are still
// there at the bottom" — is a description of exactly that.
//
// The size filter was worse than redundant, it was information-free. Every
// compute plan's `allowed_daemon_sizes` CONTAINS every cheaper plan's
// (small ⊂ medium ⊂ large ⊂ xl in control-plane's plans.yaml), so "which
// plans run Medium?" always answers "Medium and everything above it" and the
// filter could only ever dim the cheaper cards. Size and plan are ONE axis:
// picking a size picks the cheapest plan that runs it. Filtering one axis by
// itself is not a filter.
//
// So what remains is the list onboarding already got right — one row per
// machine size, with its specs and its price — built from the SAME
// `deriveMachineOptions` the compute step uses, so the two surfaces cannot
// drift back apart. The facts that do not vary by size (the 160 included
// hours, the burst ceiling, the overage/pause policy) are stated ONCE beneath
// the list, never per row; `PlanTiles.tsx` carries the long version of why.
//
// Payment mounts in place. Nothing here navigates: an earlier version built
// hosted return URLs and set window.location, which on desktop escaped to the
// system browser and on a phone backgrounded the tab. The checkout panel owns
// intent creation, the anonymous-user refusal, and the server-confirmation
// poll — so this component holds no billing logic of its own beyond "what did
// they pick".

function PlansTab() {
  const plansQ = usePlans();
  const subQ = useComputeSubscription();
  // Which plan the user is currently buying, or null when no checkout is open.
  // A plain plan id rather than a CheckoutRequest union: this tab only ever
  // buys compute, and the checkout component now owns switching between plans.
  const [checkoutPlanId, setCheckoutPlanId] = useState<string | null>(null);
  // Bumped by a successful identity link so the checkout remounts and retries.
  const [linkAttempt, setLinkAttempt] = useState(0);

  // Membership and order are both server facts: a plan the catalog priced is a
  // plan the user can buy, sorted by the catalog's display_order. The
  // hardcoded allowlist this replaced meant a newly added plan was invisible
  // until someone edited this file.
  const computePlans: Plan[] = sortPlansForDisplay(
    (plansQ.data?.plans ?? []).filter(isPurchasableComputePlan),
  );
  // How many compute plans the server sent that we had to drop for having no
  // price. Distinguishes "this env sells nothing" from "the catalog is there
  // but its prices never reached the database" — see the empty state below.
  // There is no deliberately unpriced compute plan any more, so every one
  // counted here is that symptom.
  const unpricedPlanCount = (plansQ.data?.plans ?? []).filter(
    isUnpricedComputePlan,
  ).length;
  const currentPlanId = subQ.data?.subscription?.plan?.id ?? null;

  const loading = plansQ.isLoading || subQ.isLoading;

  /**
   * The machine list — ONE row per size, cheapest plan that runs it.
   *
   * The same derivation onboarding renders, which is the point of it being a
   * function rather than a loop in each file. This tab differs from onboarding
   * in exactly one way, and it is not the rows: the user may already have a
   * plan, so a row can be `current`.
   */
  const planOptions: ComputePlanOption[] = useMemo(
    () => deriveMachineOptions(computePlans),
    [computePlans],
  );

  const handleCheckoutDone = useCallback(() => {
    setCheckoutPlanId(null);
    void subQ.refetch();
  }, [subQ]);

  /**
   * Has the plan the user just bought actually landed, according to the
   * SERVER?
   *
   * Compares against the plan being PURCHASED, not merely "is there a
   * subscription". Most buyers on this tab are SWITCHING plans, so they
   * already have one — a presence check would report success the instant it
   * was called, skip the wait for the webhook entirely, and leave the page
   * claiming a plan the user had not been moved to yet.
   */
  const computeHasLanded = useCallback(async () => {
    if (!checkoutPlanId) return false;
    try {
      const { data } = await subQ.refetch();
      return data?.subscription?.plan?.id === checkoutPlanId;
    } catch {
      return false;
    }
  }, [subQ, checkoutPlanId]);

  return (
    <div className="flex flex-col gap-6">
      <div>
        {/* Panel heading — the middle rung of the ladder in ui/card.tsx, the
            same weight CardTitle renders. It read `text-base` here and in the
            two panels below while every card on the page used `text-sm`, so
            three headings of one level came in two sizes. */}
        <h3 className="flex items-center gap-2 text-sm font-semibold leading-none tracking-tight text-foreground">
          <Cpu className="h-4 w-4 text-muted-foreground" />
          Choose your machine
        </h3>
        {/* Says what the list is, not how to operate it. The old copy —
            "pick the machine size you need, THEN the plan that runs it" —
            described two steps because there used to be two controls, and
            there is only ever one decision to make. */}
        <p className="text-sm text-muted-foreground">
          Bigger machines cost more per month. One subscription covers every
          machine on your account and shares one monthly bucket of hours
          across them.
        </p>
      </div>

      {loading ? (
        <Loading label="Loading plans…" />
      ) : computePlans.length === 0 ? (
        // Three different situations used to render as one alarming sentence.
        // `isPurchasableComputePlan` requires priceCents > 0, and price lives
        // in the plans table — which plansync rewrites from the catalog ON
        // BOOT. So a server running against rows written before the catalog
        // gained price_cents serves unpriced plans, every plan is filtered
        // out, and the page announced "not configured" for an environment
        // whose catalog is fully populated. Verified in dev: the rows carried
        // sizes and minutes but no price.
        //
        // "The server hasn't synced" and "this environment sells nothing" are
        // different facts and the UI can tell them apart — the plans arrived,
        // they just have no price — so it should say which one it is rather
        // than picking the most frightening reading.
        unpricedPlanCount > 0 ? (
          <EmptyState
            icon={Cpu}
            title="Plan pricing unavailable"
            description={`The server returned ${unpricedPlanCount} compute plan${unpricedPlanCount === 1 ? "" : "s"} with no price, so none can be purchased safely. This usually means the control plane has not restarted since the plan catalog changed — its prices are written from the catalog at startup. Restart the control plane, or contact support if it persists.`}
          />
        ) : (
          <EmptyState
            icon={Cpu}
            title="No plans available"
            description="Compute plans are not configured for this environment yet."
          />
        )
      ) : checkoutPlanId ? (
        /* The checkout REPLACES the list rather than sitting above it.
        
           Rendering both is the defect this tab had: the checkout carries its
           own machine list, so mounting it under the tab's list showed every
           machine twice, and switching in one did not move the other. One
           list is on screen at a time, and while a purchase is
           open it is the checkout's, because that is the one wired to the
           form collecting the card. */
        <div className="flex flex-col gap-3">
          {/* The same component onboarding and mobile mount — one compute
              checkout, not three that drift. Switching machines inside it
              updates the mounted Stripe form in place rather than tearing
              down an iframe and minting a new session. */}
          <ComputeSubscriptionCheckout
            plans={planOptions}
            selectedPlanId={checkoutPlanId}
            onSelectPlan={(option) => setCheckoutPlanId(option.planId)}
            confirmSettlement={computeHasLanded}
            onDone={handleCheckoutDone}
            currentPlanId={currentPlanId}
            renderIdentityRequired={(message) => (
              <LinkIdentityModal
                message={message}
                returnTo={`/settings/billing?tab=plans&planId=${encodeURIComponent(checkoutPlanId)}`}
                onLinked={() => setLinkAttempt((n) => n + 1)}
                onDismiss={() => undefined}
              />
            )}
            key={`compute:${linkAttempt}`}
          />
          <div>
            <Button
              size="sm"
              variant="ghost"
              onClick={() => setCheckoutPlanId(null)}
            >
              Cancel
            </Button>
          </div>
        </div>
      ) : (
        <div className="flex flex-col gap-4 rounded-xl border border-border bg-card p-5">
          {/* ONE list. Each row is a machine size, its specs and its price;
              clicking one opens the checkout for the cheapest plan that runs
              it. The row a user is already subscribed to is marked and cannot
              be bought again — `currentPlanId` is what PlanTiles needs that
              this tab knows and onboarding does not. */}
          <PlanTiles
            plans={planOptions}
            selectedPlanId={undefined}
            currentPlanId={currentPlanId}
            onSelect={(option) => setCheckoutPlanId(option.planId)}
          />

          {/* Everything true of EVERY machine, said once, beneath the list.
          
              Neither varies by size — the allowance is 9600 minutes at every
              rung of the catalog's ladder — and a per-row copy of a uniform
              fact implies it varies. PlanTiles.tsx carries the long version:
              a per-row hours figure shipped once, read four different stale
              numbers off a lagging dev database, and taught users that a
              bigger machine buys more time. It buys a bigger machine.
              
              Sized from a real row so the figures still track the catalog
              rather than being written down here a second time. */}
          {planOptions.length > 0 && (
            <div className="space-y-2 border-t border-border pt-4">
              <p
                className="text-xs leading-relaxed text-muted-foreground"
                data-testid="plans-hours-note"
              >
                {describeIncludedHours(planOptions[0])}
              </p>
              {/* The burst ceiling. Rows print the RESERVED figures, which
                  undersell a machine that compiles on four times the cores it
                  reserves — and the headroom is uniform, so it is one
                  sentence here rather than one per row. Absent
                  entirely if the ladder stops being uniform; see
                  machineSpecs' machineBurst, which withholds rather than
                  picking one tier's ratio to speak for the rest. */}
              {MACHINE_BURST && (
                <p
                  className="text-xs leading-relaxed text-muted-foreground"
                  data-testid="plans-burst-note"
                >
                  Those are the reserved figures. When a build needs more, a
                  machine can burst to {MACHINE_BURST.cpu}× the CPU and{" "}
                  {MACHINE_BURST.memory}× the memory it reserves, at no extra
                  cost.
                </p>
              )}
              {/* What happens at the end of the hours.
              
                  `rateVariesBySize` because it does: the hours are uniform
                  across the catalog but the overage RATE is priced per plan,
                  so quoting one machine's rate beneath four would assert a
                  uniformity the catalog does not have. The policy is stated
                  here; the number appears in the checkout, where exactly one
                  machine is selected. */}
              <PlanFinePrint plan={planOptions[0]} rateVariesBySize />
              <p className="text-xs text-muted-foreground">
                Payment opens on this page — you won&apos;t leave Reliant.
              </p>
            </div>
          )}
        </div>
      )}
    </div>
  );
}


// ── Usage & invoices tab ────────────────────────────────────────────────
//
// An invoice is the settled record of a period's usage, so these answer one
// question — "what did I spend and when" — and splitting them made the user
// check two tabs to reconcile one number.
//
// Invoices go ABOVE the daily chart rather than below it: a receipt is looked
// up by intent, usage is browsed. Putting the dense table under a dense chart
// would make the deliberate lookup the thing you scroll past.

function UsageAndInvoicesTab() {
  // Deployment usage sits beside compute usage because they answer the same
  // question about two products — "what have I run, and what will it cost" —
  // and a customer reconciling one bill should not have to find two tabs.
  //
  // Gated on `available`, which is false until the public proto subtree
  // carries the infra-overage pair and DeployService (see ./usage/
  // useDeployUsage.ts for the exact unblock). A tab that rendered this
  // half-wired would show a paying customer an empty dashboard about money.
  const deployUsage = useDeployUsage();

  return (
    <div className="flex flex-col gap-8">
      <InvoicesPanel />
      <div className="border-t border-border pt-6">
        <UsagePanel />
      </div>
      {deployUsage.available && (
        <div className="border-t border-border pt-6">
          <DeployUsageSection
            summary={deployUsage.summary}
            isLoading={deployUsage.isLoading}
            error={deployUsage.error}
            onRetry={deployUsage.refetch}
            onSaveCap={deployUsage.saveCap}
            isSavingCap={deployUsage.isSavingCap}
            deploymentNames={deployUsage.deploymentNames}
            environmentNames={deployUsage.environmentNames}
          />
        </div>
      )}
    </div>
  );
}

// ── Invoices ────────────────────────────────────────────────────────────

const INVOICE_STATUS_VARIANT: Record<
  string,
  "success" | "warning" | "error"
> = {
  Paid: "success",
  Pending: "warning",
  Failed: "error",
};

function InvoicesPanel() {
  const invoicesQ = useCurrentUserInvoices();
  const invoices = invoicesQ.data?.invoices ?? [];

  return (
    <div className="flex flex-col gap-4">
      <div>
        <h3 className="text-sm font-semibold leading-none tracking-tight text-foreground">
          Invoices
        </h3>
        <p className="mt-1 text-sm text-muted-foreground">
          View and download your past invoices.
        </p>
      </div>

      {invoicesQ.error && (
        <ErrorBanner
          message={formatBillingError(
            invoicesQ.error,
            "Failed to load invoices",
          )}
        />
      )}

      {invoicesQ.isLoading ? (
        <Loading label="Loading invoices…" />
      ) : invoices.length === 0 ? (
        <EmptyState
          icon={FileText}
          title="No invoices yet"
          description="Invoices appear here once your first billing period closes."
        />
      ) : (
        <Table>
          <Thead>
            <Tr>
              <Th>Date</Th>
              <Th>Amount</Th>
              <Th>Status</Th>
              <Th className="text-right">PDF</Th>
            </Tr>
          </Thead>
          <Tbody>
            {invoices.map((invoice) => {
              const status = normalizeInvoiceStatus(invoice.status);
              return (
                <Tr key={invoice.id}>
                  <Td>{formatTimestampDate(invoice.periodStart ?? invoice.createdAt)}</Td>
                  <Td>{formatCentsAsDollars(invoice.amountDue)}</Td>
                  <Td>
                    <Badge
                      variant={INVOICE_STATUS_VARIANT[status]}
                      label={status}
                    />
                  </Td>
                  <Td className="text-right">
                    {invoice.pdfUrl ? (
                      <a
                        href={invoice.pdfUrl}
                        target="_blank"
                        rel="noopener noreferrer"
                        className="inline-flex items-center gap-1 text-sm font-medium text-primary hover:underline"
                      >
                        <Download className="h-4 w-4" />
                        Download
                      </a>
                    ) : (
                      <span className="text-sm text-muted-foreground">—</span>
                    )}
                  </Td>
                </Tr>
              );
            })}
          </Tbody>
        </Table>
      )}
    </div>
  );
}

// ── Usage ───────────────────────────────────────────────────────────────

function UsagePanel() {
  const [period, setPeriod] = useState<"current" | "previous">("current");
  const usageQ = useComputeUsage(period);
  const data = usageQ.data ?? null;

  const summary = useMemo(
    () => ({
      includedHours: (data?.includedMinutes ?? 0) / 60,
      usedHours: (data?.usedMinutes ?? 0) / 60,
      overageHours: (data?.overageMinutes ?? 0) / 60,
      overageCost: formatCentsAsDollars(data?.estimatedOverageCostCents ?? 0),
      grantedMinutesRemaining: data?.grantedMinutesRemaining ?? 0,
    }),
    [data],
  );

  const byDayMax = useMemo(() => {
    if (!data?.byDay?.length) return 0;
    return Math.max(...data.byDay.map((e) => e.minutes ?? 0));
  }, [data]);

  const sortedWorkspaces = useMemo(() => {
    if (!data?.byWorkspace?.length) return [];
    return [...data.byWorkspace].sort(
      (a, b) => (b.minutes ?? 0) - (a.minutes ?? 0),
    );
  }, [data]);

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <h3 className="text-sm font-semibold leading-none tracking-tight text-foreground">
          Compute usage
        </h3>
        <div className="inline-flex w-full rounded-md border border-border p-1 sm:w-auto">
          {(["current", "previous"] as const).map((p) => (
            <button
              key={p}
              type="button"
              onClick={() => setPeriod(p)}
              className={`rounded px-3 py-1.5 text-sm font-medium transition-colors ${
                period === p
                  ? "bg-primary text-primary-foreground"
                  : "text-muted-foreground hover:text-foreground"
              }`}
            >
              {p === "current" ? "Current" : "Previous"}
            </button>
          ))}
        </div>
      </div>

      {usageQ.error && (
        <ErrorBanner
          message={formatBillingError(usageQ.error, "Failed to load usage")}
        />
      )}

      {usageQ.isLoading ? (
        <Loading label="Loading usage…" />
      ) : !data ? (
        <EmptyState
          icon={BarChart3}
          title="No usage data"
          description="Usage appears here once your machines start running."
        />
      ) : (
        <>
          <div className="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-4">
            <UsageStat label="Included" value={`${summary.includedHours.toFixed(0)} h`} />
            <UsageStat label="Used" value={`${summary.usedHours.toFixed(1)} h`} />
            <UsageStat label="Overage" value={`${summary.overageHours.toFixed(1)} h`} />
            <UsageStat label="Estimated overage" value={summary.overageCost} />
          </div>

          {/* Coupon grant is deliberately outside the four period stats above:
              included/used/overage all reset with the billing period, while a
              redeemed grant is a one-time bucket that carries over. */}
          {summary.grantedMinutesRemaining > 0 && (
            <UsageStat
              label="Coupon minutes remaining"
              value={formatMachineMinutesShort(
                summary.grantedMinutesRemaining,
              )}
              hint="One-time bonus machine time from a redeemed coupon. Does not renew each period; used after your included hours, before overage."
            />
          )}

          <Card>
            <CardHeader>
              <CardTitle>Daily usage</CardTitle>
            </CardHeader>
            <CardContent>
              {data.byDay.length === 0 ? (
                <p className="text-sm text-muted-foreground">
                  No daily usage recorded yet.
                </p>
              ) : (
                <div className="flex h-48 items-end gap-1">
                  {data.byDay.map((entry, idx) => {
                    const minutes = entry.minutes ?? 0;
                    const heightPct = byDayMax > 0 ? (minutes / byDayMax) * 100 : 0;
                    return (
                      <div
                        key={idx}
                        className="flex flex-1 flex-col items-center gap-1"
                      >
                        <div
                          title={`${formatDayLabel(entry.day)}: ${minutes.toFixed(1)} min`}
                          className="w-full rounded-t bg-primary/70 transition-all hover:bg-primary"
                          style={{
                            height: `${Math.max(heightPct, minutes > 0 ? 2 : 0)}%`,
                          }}
                        />
                        <span className="text-2xs text-muted-foreground">
                          {formatDayLabel(entry.day)}
                        </span>
                      </div>
                    );
                  })}
                </div>
              )}
            </CardContent>
          </Card>

          <div>
            <h4 className="mb-2 text-sm font-semibold text-foreground">
              By machine
            </h4>
            {sortedWorkspaces.length === 0 ? (
              <EmptyState
                icon={Server}
                title="No machine usage"
                description="No machine usage in this period."
              />
            ) : (
              <Table>
                <Thead>
                  <Tr>
                    <Th>Machine</Th>
                    <Th>Size</Th>
                    <Th className="text-right">Minutes</Th>
                    <Th className="text-right">Overage min</Th>
                  </Tr>
                </Thead>
                <Tbody>
                  {sortedWorkspaces.map((w) => (
                    <Tr key={w.workspaceId}>
                      <Td className="font-medium text-foreground">
                        {w.workspaceName || w.workspaceId.slice(0, 8)}
                      </Td>
                      <Td className="capitalize text-muted-foreground">
                        {w.size || "—"}
                      </Td>
                      <Td className="text-right">{(w.minutes ?? 0).toFixed(1)}</Td>
                      <Td className="text-right">
                        {(w.overageMinutes ?? 0).toFixed(1)}
                      </Td>
                    </Tr>
                  ))}
                </Tbody>
              </Table>
            )}
          </div>
        </>
      )}
    </div>
  );
}

function UsageStat({
  label,
  value,
  hint,
}: {
  label: string;
  value: string;
  hint?: string;
}) {
  return (
    <Card>
      <CardContent>
        {/* Stat caption, deliberately a rung below a section label: it must not
            compete with the figure under it. See ui/card.tsx. */}
        <p className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
          {label}
        </p>
        <p className="mt-1 text-2xl font-semibold text-foreground">{value}</p>
        {hint && <p className="mt-1 text-xs text-muted-foreground">{hint}</p>}
      </CardContent>
    </Card>
  );
}