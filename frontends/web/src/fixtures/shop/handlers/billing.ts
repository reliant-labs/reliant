// Copyright (c) 2025 Reliant Labs

/**
 * BillingService and ObservabilityBillingService for the signed-in app: Alex
 * is on Compute M (62 of 160 machine hours used this period, with a cloud
 * machine running), observability at 6.4 of 10 GB, three paid invoices, and
 * the org's month: shop (a ~$61 forecast, over prod,
 * staging and dev) plus ledger, a small backend and its database, so Usage &
 * billing lists two projects. LLMGatewayService's spend is the Reliant AI
 * spend the overview reports: $18.40 over 412 requests in 30 days.
 *
 * Part of the app layer only (../app.ts): the demo's console reads no
 * billing. Every amount and window is relative to the pack clock.
 */
import type { MessageInitShape } from "@bufbuild/protobuf";
import { timestampFromMs } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError } from "@connectrpc/connect";

import { PlanTier, SubscriptionStatus, type PlanSchema } from "@/gen/controlplane/controlplane/v1/shared_pb";
import { BillingService, ComputeIneligibleReason, OrganizationUsageGranularity, OrganizationUsageGroupBy, StorageBillingBasis, StorageProduct, type ProjectCostAmountSchema } from "@/gen/controlplane/services/billing/v1/billing_pb";
import { LLMGatewayService } from "@/gen/controlplane/services/llm_gateway/v1/llm_gateway_pb";
import { ObservabilityBillingService, ObservabilityQuotaState } from "@/gen/controlplane/services/observability_billing/v1/observability_billing_pb";

import { DAY, DEMO_ORG, HOUR, MINUTE, demoNow } from "../shopStory";
import { APP_USER, tsAgo } from "../workspace";
import { serve } from "./serve";

const GIB = 2 ** 30;
const NANOS = 1_000_000_000;
const ts = (ms: number) => timestampFromMs(ms);
const tsAhead = (ahead: number) => ts(demoNow() + ahead);
const nanos = (dollars: number) => BigInt(Math.round(dollars * NANOS));
/** The pack clock's calendar month, `offset` months on. */
const monthStart = (offset = 0) => {
  const now = new Date(demoNow());
  return Date.UTC(now.getUTCFullYear(), now.getUTCMonth() + offset, 1);
};
/** The subscription's billing period: 12 days in, 18 to go. */
const period = () => ({ start: demoNow() - 12 * DAY, end: demoNow() + 18 * DAY });

// ── Plans and the subscription ───────────────────────────────────────────────

// The plan catalog of control-plane's pricing.yaml (machine-pooled-v1): size, monthly cents, disk GiB.
const SIZES: [string, number, number][] = [
  ["s1", 2500, 30], ["s2", 3000, 30], ["m", 3900, 50], ["l", 6900, 80], ["xl", 14900, 120], ["2xl", 24900, 200], ["3xl", 48900, 300],
];

type PlanInit = MessageInitShape<typeof PlanSchema>;
const plans = (): PlanInit[] =>
  SIZES.map(([size, cents, disk], i) => ({
    id: `plan_compute_${size}`, productId: "prod_compute", name: `Compute ${size.toUpperCase()}`, tier: PlanTier.STARTER,
    stripePriceId: `price_shop_${size}`, createdAt: tsAgo(30 * DAY), priceCents: BigInt(cents), displayOrder: i + 1,
    structuredLimits: {
      maxSeats: 1, maxWorkspaces: 5, maxLlmKeys: 1, maxLlmSpendMonthly: 25,
      allowedDaemonSizes: SIZES.slice(0, i + 1).map(([s]) => s), machineSize: size, machineIncludedHours: 160n,
      machineIncludedStorageGib: BigInt(disk),
    },
  }));

function subscription() {
  const plan = plans().find((p) => p.id === "plan_compute_m")!;
  const { start, end } = period();
  return {
    id: "shop-sub-0001", orgId: DEMO_ORG, planId: plan.id, plan, stripeSubscriptionId: "sub_shop",
    status: SubscriptionStatus.ACTIVE, currentPeriodStart: ts(start), currentPeriodEnd: ts(end),
    createdAt: ts(start), updatedAt: tsAgo(DAY), overageEnabled: false,
  };
}

// ── Usage & billing ──────────────────────────────────────────────────────────

const amount = (dollars: number, basis = "estimated"): MessageInitShape<typeof ProjectCostAmountSchema> => ({ usdNanos: nanos(dollars), basis, lowerBound: false, unpricedDimensions: [], allocated: false });

// Per day at list price, by environment and category: prod carries most of it, dev a little.
const SHOP_DAILY: [string, string, number][] = [
  ["env-prod", "compute", 1.24],
  ["env-prod", "databases", 0.42],
  ["env-prod", "storage", 0.06],
  ["env-prod", "bandwidth", 0.11],
  ["env-staging", "compute", 0.31],
  ["env-staging", "storage", 0.02],
  ["env-dev", "static_sites", 0.03],
];

/** shop's month to date (GetProjectCost): summed daily costs, a ~$61 forecast, the api in prod the biggest driver. */
function shopCost() {
  const start = monthStart();
  const days = Math.floor((demoNow() - start) / DAY) + 1;
  const daily = Array.from({ length: days }, (_, d) =>
    SHOP_DAILY.filter(([env]) => env !== "env-dev" || d >= days - 1).map(([environmentId, category, perDay]) => {
      const wave = 1 + 0.12 * Math.sin(d / 2.2) + (d > 12 && category === "compute" ? 0.15 : 0);
      const partial = d === days - 1;
      return { day: ts(start + d * DAY), category, environmentId, list: amount(perDay * wave * (partial ? 0.65 : 1), "list"), quantities: [], partial };
    }),
  ).flat();
  const listTotal = daily.reduce((acc, d) => acc + Number(d.list.usdNanos) / NANOS, 0);
  const driver = (deploymentName: string, environmentId: string, environmentName: string, tier: string, category: string, dollars: number, requested?: { replicas?: number; cpuMilli?: number; memoryMib?: number; storageGib?: number }) => ({
    deploymentId: `dep-${environmentName}-${deploymentName}`, deploymentName, environmentId, environmentName, tier, category,
    list: amount(dollars, "list"), previousUsdNanos: nanos(dollars * 0.86), share: dollars / listTotal, quantities: [],
    ...(requested ? { requested: { replicas: 1, cpuMilli: 0, memoryMib: 0, storageGib: 0, ...requested } } : {}), deleted: false,
  });
  return {
    currency: "USD", generatedAt: tsAgo(5 * MINUTE), periodStart: ts(start), periodEnd: ts(monthStart(1)),
    measuredThrough: tsAgo(40 * MINUTE), monthToDate: amount(listTotal), monthToDateList: amount(listTotal, "list"), samePointLastPeriod: amount(33.4),
    forecast: { amount: amount(61.3), lowUsdNanos: nanos(56.8), highUsdNanos: nanos(66.9), method: "run_rate_and_trailing_7d", measuredHours: 168, unavailableReason: "", list: amount(64.2, "list") },
    lastPeriod: amount(54.75, "invoiced"),
    daily,
    drivers: [
      driver("api", "env-prod", "prod", "backend", "compute", 17.6, { replicas: 3, cpuMilli: 500, memoryMib: 1024 }),
      driver("postgres", "env-prod", "prod", "database", "databases", 8.31, { cpuMilli: 250, memoryMib: 512, storageGib: 10 }),
      driver("worker", "env-prod", "prod", "backend", "compute", 5.2, { cpuMilli: 250, memoryMib: 512 }),
      driver("api", "env-staging", "staging", "backend", "compute", 5.52, { cpuMilli: 250, memoryMib: 512 }),
      driver("shop-web", "env-prod", "prod", "static", "bandwidth", 1.98),
      driver("shop-web", "env-dev", "dev", "static", "static_sites", 0.02),
    ],
    allowances: [], caps: [],
  };
}

/** ledger's month so far: one small backend and its database. */
function ledgerCost() {
  const start = monthStart();
  const days = Math.floor((demoNow() - start) / DAY) + 1;
  const daily = Array.from({ length: days }, (_, d) =>
    ([["compute", 0.36], ["databases", 0.14]] as const).map(([category, perDay]) => ({
      day: ts(start + d * DAY), category, environmentId: "env-ledger-prod",
      list: amount(perDay * (1 + 0.08 * Math.sin(d / 1.7)), "list"), quantities: [], partial: d === days - 1,
    })),
  ).flat();
  return {
    currency: "USD", generatedAt: tsAgo(5 * MINUTE), periodStart: ts(start), periodEnd: ts(monthStart(1)),
    measuredThrough: tsAgo(40 * MINUTE), monthToDate: amount(8.94), monthToDateList: amount(9.1, "list"), samePointLastPeriod: amount(8.2),
    forecast: { amount: amount(14.9), lowUsdNanos: nanos(13), highUsdNanos: nanos(16), method: "run_rate_and_trailing_7d", measuredHours: 168, unavailableReason: "", list: amount(15.2, "list") },
    lastPeriod: amount(13.6, "invoiced"), daily, drivers: [], allowances: [], caps: [],
  };
}

// category, list or actual, dollars per day, quantities per day [id, unit, value]
const USAGE_SHAPE: [string, "list" | "actual", number, [string, string, number][]][] = [
  ["hosted_apps", "list", 1.18, [["infra_cpu", "vcpu_hours", 26], ["infra_memory", "gib_hours", 52]]],
  ["databases", "list", 0.42, [["infra_storage", "gib_hours", 240]]],
  ["static_hosting", "list", 0.07, [["egress_kib", "kib", 3_400_000]]],
  ["observability", "list", 0.11, [["stored_bytes", "bytes", 220_000_000]]],
  ["managed_llm", "actual", 3.6, [["llm_requests", "count", 1_450]]],
  ["machines", "list", 1.3, [["running_seconds", "seconds", 5.2 * 3600]]],
];

const msOf = (t: { seconds: bigint; nanos: number } | undefined, fallback: number) => (t ? Number(t.seconds) * 1000 + Math.floor(t.nanos / 1e6) : fallback);

/** The org's daily usage by category over the asked window (at most the clock), every source complete. */
function organizationUsage(req: { start?: { seconds: bigint; nanos: number }; end?: { seconds: bigint; nanos: number } }) {
  const now = demoNow();
  const end = Math.min(msOf(req.end, now), now);
  const startDay = Math.floor(msOf(req.start, end - 30 * DAY) / DAY) * DAY;
  const days = Math.max(1, Math.min(400, Math.ceil((end - startDay) / DAY)));
  const points = Array.from({ length: days }, (_, d) =>
    USAGE_SHAPE.map(([category, basis, perDay, quantities], c) => {
      // A gentle weekly shape, a launch bump in the last ten days, and a partial today.
      const weekday = new Date(startDay + d * DAY).getUTCDay();
      const wave = (weekday === 0 || weekday === 6 ? 0.72 : 1) * (1 + 0.1 * Math.sin((d + c) / 2.3)) * (d > days - 10 ? 1.22 : 1) * (d === days - 1 ? 0.6 : 1);
      const charges = BigInt(Math.round(perDay * wave * NANOS));
      return {
        bucketStart: ts(startDay + d * DAY), category, groupKey: "", groupLabel: "", detail: "",
        ...(basis === "actual" ? { actualChargesUsdNanos: charges } : { listEquivalentUsdNanos: charges }),
        quantities: quantities.map(([id, unit, value]) => ({ id, unit, value: Math.round(value * wave) })),
        unpricedDimensions: [],
      };
    }),
  ).flat();
  const coverage = (category: string) => ({ category, completeness: "complete", coverageStart: ts(startDay), coverageEnd: ts(end), measuredThrough: tsAgo(10 * MINUTE), limitations: [] });
  const inventory = (category: string, count: bigint, o: { size?: string; state?: string; environmentId?: string; environmentName?: string; storageGib?: bigint } = {}) => ({
    category, size: "", state: "ready", environmentId: "env-prod", environmentName: "prod", count, storageGib: 0n, ...o,
  });
  return {
    orgId: DEMO_ORG, currency: "USD", generatedAt: tsAgo(), start: ts(startDay), end: ts(end),
    granularity: OrganizationUsageGranularity.DAY, groupBy: OrganizationUsageGroupBy.CATEGORY,
    points,
    coverage: ["managed_llm", "machines", "hosted_infrastructure", "observability", "inventory"].map(coverage),
    spendRates: USAGE_SHAPE.map(([category, basis, perDay]) => ({
      category, basis: basis === "actual" ? "actual" : "list_equivalent",
      last24hUsdNanos: nanos(perDay * 1.15), averageDaily7dUsdNanos: nanos(perDay * 1.1),
      measuredHours: 168, unpricedDimensions: [], lowerBound: false,
    })),
    inventory: [
      inventory("hosted_apps", 3n),
      inventory("databases", 1n),
      inventory("static_hosting", 1n),
      inventory("machines", 1n, { size: "m", state: "running", environmentId: "", environmentName: "", storageGib: 50n }),
    ],
    completeness: "complete",
  };
}

/**
 * Storage: billed machine disks and app volumes, tracked images, static
 * sites and buckets, SBOM copies not counted, and test reports not measured
 * yet (no bytes_now).
 */
function storageUsage() {
  const gib = (n: number) => BigInt(Math.round(n * GIB));
  const measuredAt = tsAgo(20 * MINUTE);
  const row = (product: StorageProduct, bytes: number | undefined, billingBasis: StorageBillingBasis, countsTowardPool = true) => ({
    product, countsTowardPool, billingBasis,
    ...(bytes === undefined ? {} : { bytesNow: gib(bytes), measuredAt, gibHoursPeriodToDate: BigInt(Math.round(bytes * 24 * 20)) }),
  });
  return {
    pool: { freeGib: 10n, planIncludedGib: 50n, includedGib: 60n, usedBytes: gib(50 + 8 + 3.6 + 0.4 + 1.2), periodStart: ts(monthStart()), periodEnd: ts(monthStart(1)), billingActive: false },
    products: [
      row(StorageProduct.MACHINE_DISKS, 50, StorageBillingBasis.BILLED),
      row(StorageProduct.APP_VOLUMES, 8, StorageBillingBasis.BILLED),
      row(StorageProduct.CONTAINER_IMAGES, 3.6, StorageBillingBasis.TRACKED),
      row(StorageProduct.STATIC_SITES, 0.4, StorageBillingBasis.TRACKED),
      row(StorageProduct.BUCKETS, 1.2, StorageBillingBasis.TRACKED),
      row(StorageProduct.TEST_REPORTS, undefined, StorageBillingBasis.TRACKED),
      row(StorageProduct.SBOM_COPIES, 0.3, StorageBillingBasis.NOT_COUNTED, false),
    ],
  };
}

/** Three paid monthly invoices, newest first. */
const invoices = () =>
  [5_812n, 5_240n, 4_675n].map((cents, i) => ({
    id: `in_shop_${i + 1}`, amountDue: cents, amountPaid: cents, currency: "usd", status: "paid",
    periodStart: ts(monthStart(-1 - i)), periodEnd: ts(monthStart(-i)),
    // Bearer links in production (why ListCurrentUserInvoices declares no NO_SIDE_EFFECTS): obviously fake here.
    hostedInvoiceUrl: "https://invoice.stripe.invalid/i/shop", pdfUrl: "https://pay.stripe.invalid/invoice/shop/pdf",
    createdAt: ts(monthStart(-i) + HOUR),
  }));

export function billingScenarioHandlers() {
  return {
    ...serve(BillingService, {
      listPlans: () => ({ plans: plans() }),
      getCurrentUserComputeEligibility: () => ({
        eligible: true, reason: ComputeIneligibleReason.UNSPECIFIED, hasActiveSubscription: true,
        grantedSmallDaemonSecondsRemaining: 0n, planName: "Compute M", allowedDaemonSizes: ["s1", "s2", "m"],
      }),
      getCurrentUserSubscription: () => ({ subscription: subscription() }),
      getCurrentUserComputeSubscription: () => ({ subscription: subscription() }),
      getCurrentUserReliantOverview: () => ({
        overview: { currentPeriodSpendCents: 1840n, remainingSpendCents: 8160n, spendCapReached: false, spendCapUnlimited: false, requestRateUnlimited: true, currentPeriodRequestCount: 412n },
      }),
      getCurrentUserWalletOverview: () => ({ overview: { ledgerEntries: [], topups: [], ledgerPage: 1, ledgerPageSize: 20, totalLedgerEntries: 0 } }),
      getCurrentUserBillingEmail: () => ({ billingEmail: APP_USER.email, fallbackEmail: APP_USER.email }),
      getCurrentUserComputeUsage: () => {
        const { start, end } = period();
        return {
          usageMeasured: true, periodStart: ts(start), periodEnd: ts(end), measuredThrough: tsAgo(5 * MINUTE),
          pricingVersion: "machine-pooled-v1",
          allowanceWeightedSeconds: "37221280848000/41831851",
          consumedWeightedSeconds: "14423246328600/41831851",
          remainingWeightedSeconds: "22798034519400/41831851",
          grantRemainingWeightedSeconds: "0",
          runningCostWeightSum: "258481117/167327404",
          exhaustsAt: tsAhead(98 * HOUR),
          bySize: [{
            size: "m", exactRunningSeconds: String(62 * 3600), costWeight: "258481117/167327404",
            includedWeightedSeconds: "14423246328600/41831851", exactOverageSeconds: "0", overageCostCents: "0", pricingVersion: "machine-pooled-v1",
          }],
          storage: { provisionedGib: 50n, includedGib: 50n, extraGib: 0n, exactExtraGibSeconds: "0", costCents: "0", projectedCostCents: "0", centsPerGibMonth: "25" },
          projectedPeriodTotalCents: "3900", planPriceCents: 3900n, estimatedOverageCostCents: 0,
        };
      },
      // Answers the project asked about: shop and ledger are hosted, nothing else is.
      getProjectCost: (req) => {
        if (req.projectId === "shop") return shopCost();
        if (req.projectId === "ledger") return ledgerCost();
        throw new ConnectError("no hosted project", Code.NotFound);
      },
      getStorageUsage: () => storageUsage(),
      listCurrentUserInvoices: () => ({ invoices: invoices() }),
      listOrganizationUsage: (req) => organizationUsage(req),
    }),
    ...serve(LLMGatewayService, {
      // The org's Reliant AI spend in the window asked for (30 days): the overview's $18.40 and 412 requests, by model.
      getLLMSpend: () => {
        const entry = (model: string, spend: number, requests: bigint) => ({
          keyId: "shop-llmkey-alex", keyName: "Reliant AI", model, spend, requests, periodStart: ts(demoNow() - 30 * DAY), periodEnd: ts(demoNow()),
        });
        return { entries: [entry("claude-sonnet-5", 14.1, 268n), entry("claude-haiku-4-5", 4.3, 144n)], totalSpend: 18.4, sampleDays: 30 };
      },
    }),
    ...serve(ObservabilityBillingService, {
      getObservabilityUsage: () => ({
        periodStart: ts(monthStart()), periodEnd: ts(monthStart(1)), measuredThrough: tsAgo(10 * MINUTE),
        usedBytes: BigInt(Math.round(6.4 * GIB)), allowanceBytes: BigInt(10 * GIB), overageCents: 0n,
        projectedBytes: BigInt(Math.round(6.4 * 1.9 * GIB)), projectedOverageCents: 54n, rateCentsPerGb: "25.0000",
        state: ObservabilityQuotaState.OK, usageState: ObservabilityQuotaState.OK, funded: true, exempt: false,
        spendCapCents: 5000n, spendCapReached: false, browserBytes: BigInt(Math.round(0.9 * GIB)),
        browserBilledBytes: BigInt(Math.round(0.9 * GIB)), browserCapBytes: BigInt(2 * GIB), bySignal: [], byService: [],
        operatorBytes: 0n, metadataBytes: BigInt(Math.round(0.2 * GIB)), chargedCents: 0n, unchargedBytes: 0n,
      }),
    }),
  };
}
