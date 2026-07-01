import { useMemo, useState } from "react";
import {
  ArrowUpRight,
  BarChart3,
  Check,
  CreditCard,
  Cpu,
  Download,
  FileText,
  Pencil,
  Server,
  Wallet,
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
import type { Plan } from "@/gen/controlplane/v1/public/shared_pb";
import {
  useBillingEmail,
  useComputeSubscription,
  useComputeUsage,
  useCreateBillingPortalSession,
  useCreateCheckoutSession,
  useCreateWalletTopupSession,
  useCurrentUserInvoices,
  usePlans,
  useSetComputeOverage,
  useUpdateBillingEmail,
  useWalletOverview,
} from "@/hooks/useCloudBillingQueries";
import {
  COMPUTE_PLAN_IDS,
  TOPUP_PRESETS_CENTS,
  derivePlanDisplay,
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
  nanosFromFields,
  normalizeInvoiceStatus,
} from "./billingUtils";

type BillingTab = "overview" | "plans" | "invoices" | "usage";

const TABS: { id: BillingTab; label: string; icon: typeof CreditCard }[] = [
  { id: "overview", label: "Overview", icon: CreditCard },
  { id: "plans", label: "Plans", icon: Cpu },
  { id: "invoices", label: "Invoices", icon: FileText },
  { id: "usage", label: "Usage", icon: BarChart3 },
];

/**
 * BillingSection — the end-user billing dashboard rendered inside
 * `/settings/billing`. Ported from control-plane admin-web's billing pages
 * but scoped to the PUBLIC "current user" BillingService RPCs and
 * re-skinned against reliant's semantic theme tokens (bg-card / text-muted-
 * foreground / bg-primary …) so it flips with dark mode.
 *
 * Sub-navigation (overview / plans / invoices / usage) is tab state local to
 * the section — it never touches the router, so it composes cleanly under the
 * settings shell that owns the `/settings/$section` route.
 *
 * The three "open Stripe" entry points (checkout, wallet top-up, billing
 * portal) redirect to hosted Stripe URLs returned by the backend; the global
 * upgradeInterceptor handles the billing-email-missing / quota modals, so
 * this component only renders inline errors for OTHER failures.
 */
export function BillingSection() {
  const [tab, setTab] = useState<BillingTab>("overview");

  return (
    <div className="flex flex-col gap-6">
      <PageHeader
        title="Billing"
        subtitle="Manage wallet credits, your compute plan, usage, and invoices."
      />

      <div className="flex flex-wrap gap-1 border-b border-border">
        {TABS.map(({ id, label, icon: Icon }) => {
          const active = tab === id;
          return (
            <button
              key={id}
              type="button"
              onClick={() => setTab(id)}
              className={[
                "inline-flex items-center gap-2 border-b-2 px-4 py-2 text-sm font-medium transition-colors",
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

      {tab === "overview" && (
        <OverviewTab
          onGoToPlans={() => setTab("plans")}
          onGoToUsage={() => setTab("usage")}
        />
      )}
      {tab === "plans" && <PlansTab />}
      {tab === "invoices" && <InvoicesTab />}
      {tab === "usage" && <UsageTab />}
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

// redirectToStripe navigates the browser to a hosted Stripe URL. In dev the
// backend returns a same-origin URL (no Stripe configured) and the action has
// already completed server-side, so we skip the redirect and let the
// invalidated queries refresh in place.
function redirectToStripe(url: string): boolean {
  if (!url) return false;
  if (url.startsWith(window.location.origin)) return false;
  window.location.href = url;
  return true;
}

// ── Overview tab ────────────────────────────────────────────────────────

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

  const topupMutation = useCreateWalletTopupSession();
  const overageMutation = useSetComputeOverage();
  const portalMutation = useCreateBillingPortalSession();

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
      pricePerMonth:
        d.monthlyPriceCents !== null ? d.monthlyPriceCents / 100 : null,
      includedHours: d.includedMinutes < 0 ? -1 : Math.round(d.includedMinutes / 60),
      overageRateLabel: formatOverageRate(d.overageCentsPerMinute),
      allowedSizes: formatAllowedSizes(d.allowedSizes),
      overageEnabled: subscription?.overageEnabled ?? false,
    };
  }, [subscription]);

  const usageUi = useMemo(() => {
    const includedHours = (usage?.includedMinutes ?? 0) / 60;
    const usedHours = (usage?.usedMinutes ?? 0) / 60;
    const overageHours = (usage?.overageMinutes ?? 0) / 60;
    const pct =
      includedHours > 0 ? Math.min((usedHours / includedHours) * 100, 100) : 0;
    return {
      includedHours,
      usedHours,
      overageHours,
      estimatedOverageCost: formatCentsAsDollars(
        usage?.estimatedOverageCostCents ?? 0,
      ),
      pct,
    };
  }, [usage]);

  const handleTopup = (amountCents: number) => {
    setError("");
    topupMutation.mutate(
      {
        amountCents: BigInt(amountCents),
        successUrl: window.location.href,
        cancelUrl: window.location.href,
      },
      {
        onSuccess: (res) => {
          redirectToStripe(res.checkoutUrl);
        },
        onError: (err) => {
          if (!isBackendModalError(err)) {
            setError(formatBillingError(err, "Failed to start wallet top-up"));
          }
        },
      },
    );
  };

  const handleToggleOverage = (enabled: boolean) => {
    setError("");
    overageMutation.mutate(enabled, {
      onError: (err) => {
        if (!isBackendModalError(err)) {
          setError(formatBillingError(err, "Failed to update overage setting"));
        }
      },
    });
  };

  const handleManageStripe = () => {
    setError("");
    portalMutation.mutate(window.location.href, {
      onSuccess: (res) => {
        redirectToStripe(res.portalUrl);
      },
      onError: (err) => {
        if (!isBackendModalError(err)) {
          setError(formatBillingError(err, "Failed to open billing portal"));
        }
      },
    });
  };

  const loading = subQ.isLoading || walletQ.isLoading || usageQ.isLoading;

  return (
    <div className="flex flex-col gap-6">
      <ErrorBanner message={error} />

      {loading ? (
        <Loading label="Loading billing…" />
      ) : (
        <>
          <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
            {/* Wallet credits */}
            <Card>
              <CardHeader>
                <CardTitle className="flex items-center gap-2">
                  <Wallet className="h-4 w-4 text-muted-foreground" />
                  Credit balance
                </CardTitle>
              </CardHeader>
              <CardContent className="flex flex-col gap-4">
                <div>
                  <p className="text-3xl font-semibold text-foreground">
                    {walletUi.balance}
                  </p>
                  <p className="text-xs text-muted-foreground">
                    Available credits for Reliant usage
                  </p>
                </div>
                {walletUi.warning && (
                  <div className="rounded-md border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs text-amber-600 dark:text-amber-400">
                    <p className="font-semibold">{walletUi.warning.title}</p>
                    <p>{walletUi.warning.message}</p>
                  </div>
                )}
                <div className="flex flex-wrap items-center gap-2">
                  <span className="text-xs font-medium text-muted-foreground">
                    Add credits:
                  </span>
                  {TOPUP_PRESETS_CENTS.map((cents) => (
                    <Button
                      key={cents}
                      size="sm"
                      variant="outline"
                      disabled={topupMutation.isPending}
                      onClick={() => handleTopup(cents)}
                    >
                      ${(cents / 100).toFixed(0)}
                    </Button>
                  ))}
                </div>
              </CardContent>
            </Card>

            {/* Compute plan */}
            <Card>
              <CardHeader className="flex-row items-center justify-between">
                <CardTitle className="flex items-center gap-2">
                  <Cpu className="h-4 w-4 text-muted-foreground" />
                  Compute plan
                </CardTitle>
                <Button size="sm" variant="outline" onClick={onGoToPlans}>
                  Change plan
                </Button>
              </CardHeader>
              <CardContent className="flex flex-col gap-4">
                <div>
                  <p className="text-2xl font-semibold text-foreground">
                    {planUi.planName}
                  </p>
                  {planUi.pricePerMonth !== null && (
                    <p className="text-sm text-muted-foreground">
                      ${planUi.pricePerMonth.toFixed(2)}/mo
                    </p>
                  )}
                </div>
                <dl className="grid grid-cols-1 gap-4 text-sm sm:grid-cols-3">
                  <div>
                    <dt className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
                      Included hours
                    </dt>
                    <dd className="mt-1 font-medium text-foreground">
                      {planUi.includedHours < 0
                        ? "Unlimited"
                        : `${planUi.includedHours} h / mo`}
                    </dd>
                  </div>
                  <div>
                    <dt className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
                      Allowed sizes
                    </dt>
                    <dd className="mt-1 font-medium text-foreground">
                      {planUi.allowedSizes}
                    </dd>
                  </div>
                  <div>
                    <dt className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
                      Overage rate
                    </dt>
                    <dd className="mt-1 font-medium text-foreground">
                      {planUi.overageRateLabel}
                    </dd>
                  </div>
                </dl>
                <div className="flex flex-col gap-3 rounded-md border border-border bg-muted/40 px-4 py-3 sm:flex-row sm:items-center sm:justify-between">
                  <div>
                    <p className="text-sm font-medium text-foreground">
                      Per-environment overage
                    </p>
                    <p className="text-xs text-muted-foreground">
                      {planUi.overageRateLabel === "—"
                        ? "Per-environment overage is not available on this plan."
                        : `When on, you'll be charged ${planUi.overageRateLabel} for usage beyond included hours.`}
                    </p>
                  </div>
                  <OverageToggle
                    enabled={planUi.overageEnabled}
                    disabled={!subscription || overageMutation.isPending}
                    title={
                      !subscription
                        ? "Subscribe to a compute plan first"
                        : undefined
                    }
                    onToggle={() => handleToggleOverage(!planUi.overageEnabled)}
                  />
                </div>
              </CardContent>
            </Card>
          </div>

          {/* Usage this period */}
          <Card>
            <CardHeader className="flex-row items-center justify-between">
              <CardTitle>Usage this period</CardTitle>
              <button
                type="button"
                onClick={onGoToUsage}
                className="text-sm font-medium text-primary hover:underline"
              >
                See detail →
              </button>
            </CardHeader>
            <CardContent>
              <div className="flex items-center justify-between text-sm">
                <span className="font-medium text-foreground">Hours used</span>
                <span className="text-muted-foreground">
                  {usageUi.usedHours.toFixed(1)} h /{" "}
                  {usageUi.includedHours.toFixed(0)} h included
                </span>
              </div>
              <div className="mt-2 h-2.5 rounded-full bg-muted">
                <div
                  className={`h-2.5 rounded-full ${
                    usageUi.pct >= 90 ? "bg-amber-500" : "bg-primary"
                  }`}
                  style={{ width: `${usageUi.pct}%` }}
                />
              </div>
              {usageUi.overageHours > 0 && (
                <div className="mt-4 flex items-center justify-between rounded-md border border-amber-500/30 bg-amber-500/10 px-4 py-3 text-sm text-amber-600 dark:text-amber-400">
                  <span className="font-medium">
                    Overage: {usageUi.overageHours.toFixed(1)} h
                  </span>
                  <span>Estimated: {usageUi.estimatedOverageCost}</span>
                </div>
              )}
            </CardContent>
          </Card>

          <BillingEmailRow />

          <div className="flex justify-end">
            <Button
              variant="outline"
              onClick={handleManageStripe}
              disabled={portalMutation.isPending}
            >
              <ArrowUpRight className="h-4 w-4" />
              {portalMutation.isPending ? "Opening…" : "Manage in Stripe"}
            </Button>
          </div>
        </>
      )}
    </div>
  );
}

function OverageToggle({
  enabled,
  disabled,
  title,
  onToggle,
}: {
  enabled: boolean;
  disabled: boolean;
  title?: string;
  onToggle: () => void;
}) {
  return (
    <span title={title} className="inline-flex">
      <button
        type="button"
        role="switch"
        aria-checked={enabled}
        aria-disabled={disabled}
        disabled={disabled}
        onClick={onToggle}
        className={`relative inline-flex h-6 w-11 shrink-0 items-center rounded-full transition-colors disabled:cursor-not-allowed disabled:opacity-50 ${
          enabled ? "bg-primary" : "bg-muted-foreground/40"
        }`}
      >
        <span
          className={`inline-block h-5 w-5 transform rounded-full bg-background shadow transition ${
            enabled ? "translate-x-5" : "translate-x-0.5"
          }`}
        />
      </button>
    </span>
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
        <div className="flex items-center justify-between gap-4">
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

// ── Plans tab ───────────────────────────────────────────────────────────

function PlansTab() {
  const plansQ = usePlans();
  const subQ = useComputeSubscription();
  const checkoutMutation = useCreateCheckoutSession();
  const [error, setError] = useState("");
  const [pendingPlanId, setPendingPlanId] = useState<string | null>(null);

  const computePlans: Plan[] = (plansQ.data?.plans ?? [])
    .filter((plan) => (COMPUTE_PLAN_IDS as readonly string[]).includes(plan.id))
    .sort(
      (a, b) =>
        (COMPUTE_PLAN_IDS as readonly string[]).indexOf(a.id) -
        (COMPUTE_PLAN_IDS as readonly string[]).indexOf(b.id),
    );
  const currentPlanId = subQ.data?.subscription?.plan?.id ?? null;

  const handleSubscribe = (plan: Plan) => {
    setError("");
    setPendingPlanId(plan.id);
    const returnUrl = window.location.href;
    checkoutMutation.mutate(
      {
        planId: plan.id,
        successUrl: returnUrl,
        cancelUrl: returnUrl,
      },
      {
        onSuccess: (res) => {
          redirectToStripe(res.checkoutUrl);
        },
        onError: (err) => {
          if (!isBackendModalError(err)) {
            setError(formatBillingError(err, "Failed to start checkout"));
          }
        },
        onSettled: () => setPendingPlanId(null),
      },
    );
  };

  const loading = plansQ.isLoading || subQ.isLoading;

  return (
    <div className="flex flex-col gap-4">
      <div>
        <h3 className="flex items-center gap-2 text-base font-semibold text-foreground">
          <Cpu className="h-5 w-5 text-muted-foreground" />
          Compute plans
        </h3>
        <p className="text-sm text-muted-foreground">
          One compute subscription powers all your environments. Each plan
          includes a monthly bucket of hours shared across them.
        </p>
      </div>

      <ErrorBanner message={error} />

      {loading ? (
        <Loading label="Loading plans…" />
      ) : computePlans.length === 0 ? (
        <EmptyState
          icon={Cpu}
          title="No plans available"
          description="Compute plans are not configured for this environment yet."
        />
      ) : (
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
          {computePlans.map((plan) => {
            const d = derivePlanDisplay(plan);
            if (d.monthlyPriceCents === null) return null;
            const isCurrent = currentPlanId === plan.id;
            const isPending = pendingPlanId === plan.id;
            const sizesLabel = formatAllowedSizes(d.allowedSizes);
            const overageLabel = formatOverageRate(d.overageCentsPerMinute);
            const includedHoursLabel =
              d.includedMinutes < 0
                ? "Unlimited"
                : `${Math.round(d.includedMinutes / 60)}`;
            return (
              <Card
                key={plan.id}
                className={isCurrent ? "ring-1 ring-primary" : undefined}
              >
                <CardContent className="flex h-full flex-col gap-4">
                  <div>
                    <h4 className="text-base font-semibold text-foreground">
                      {plan.name}
                    </h4>
                    <div className="mt-1">
                      <span className="text-3xl font-bold text-foreground">
                        ${(d.monthlyPriceCents / 100).toFixed(0)}
                      </span>
                      <span className="text-sm text-muted-foreground">/mo</span>
                    </div>
                    <p className="mt-1 text-xs text-muted-foreground">
                      Allowed sizes: {sizesLabel}
                    </p>
                  </div>
                  <ul className="flex-1 space-y-2 text-sm text-muted-foreground">
                    <li className="flex items-start gap-2">
                      <Check className="mt-0.5 h-4 w-4 shrink-0 text-primary" />
                      {includedHoursLabel === "Unlimited"
                        ? "Unlimited hours / month"
                        : `${includedHoursLabel} hours included / month`}
                    </li>
                    <li className="flex items-start gap-2">
                      <Check className="mt-0.5 h-4 w-4 shrink-0 text-primary" />
                      Overage: {overageLabel}
                    </li>
                    <li className="flex items-start gap-2">
                      <Check className="mt-0.5 h-4 w-4 shrink-0 text-primary" />
                      Runs {sizesLabel.toLowerCase()} environments
                    </li>
                  </ul>
                  <Button
                    fullWidth
                    variant={isCurrent ? "outline" : "primary"}
                    disabled={isCurrent || isPending}
                    isLoading={isPending}
                    onClick={() => handleSubscribe(plan)}
                  >
                    {isCurrent
                      ? "Current plan"
                      : currentPlanId
                        ? "Switch"
                        : "Subscribe"}
                  </Button>
                </CardContent>
              </Card>
            );
          })}
        </div>
      )}
    </div>
  );
}

// ── Invoices tab ────────────────────────────────────────────────────────

const INVOICE_STATUS_VARIANT: Record<
  string,
  "success" | "warning" | "error"
> = {
  Paid: "success",
  Pending: "warning",
  Failed: "error",
};

function InvoicesTab() {
  const invoicesQ = useCurrentUserInvoices();
  const invoices = invoicesQ.data?.invoices ?? [];

  return (
    <div className="flex flex-col gap-4">
      <div>
        <h3 className="text-base font-semibold text-foreground">Invoices</h3>
        <p className="text-sm text-muted-foreground">
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
        <Card className="overflow-hidden">
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
        </Card>
      )}
    </div>
  );
}

// ── Usage tab ───────────────────────────────────────────────────────────

function UsageTab() {
  const [period, setPeriod] = useState<"current" | "previous">("current");
  const usageQ = useComputeUsage(period);
  const data = usageQ.data ?? null;

  const summary = useMemo(
    () => ({
      includedHours: (data?.includedMinutes ?? 0) / 60,
      usedHours: (data?.usedMinutes ?? 0) / 60,
      overageHours: (data?.overageMinutes ?? 0) / 60,
      overageCost: formatCentsAsDollars(data?.estimatedOverageCostCents ?? 0),
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
      <div className="flex items-center justify-between">
        <h3 className="text-base font-semibold text-foreground">
          Compute usage
        </h3>
        <div className="inline-flex rounded-md border border-border p-1">
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
          description="Usage appears here once your environments start running."
        />
      ) : (
        <>
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
            <UsageStat label="Included" value={`${summary.includedHours.toFixed(0)} h`} />
            <UsageStat label="Used" value={`${summary.usedHours.toFixed(1)} h`} />
            <UsageStat label="Overage" value={`${summary.overageHours.toFixed(1)} h`} />
            <UsageStat label="Estimated overage" value={summary.overageCost} />
          </div>

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
                        <span className="text-[10px] text-muted-foreground">
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
              By environment
            </h4>
            {sortedWorkspaces.length === 0 ? (
              <EmptyState
                icon={Server}
                title="No environment usage"
                description="No environment usage in this period."
              />
            ) : (
              <Card className="overflow-hidden">
                <Table>
                  <Thead>
                    <Tr>
                      <Th>Environment</Th>
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
              </Card>
            )}
          </div>
        </>
      )}
    </div>
  );
}

function UsageStat({ label, value }: { label: string; value: string }) {
  return (
    <Card>
      <CardContent>
        <p className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
          {label}
        </p>
        <p className="mt-1 text-2xl font-semibold text-foreground">{value}</p>
      </CardContent>
    </Card>
  );
}
