// Copyright (c) 2026 Reliant Labs

/**
 * Pure reading behind Usage & billing's Storage and Telemetry cards
 * (UsageBillingPage.tsx): BillingService.GetStorageUsage's pool and
 * per-product rows, and ObservabilityBillingService.GetObservabilityUsage's
 * monthly ingest.
 *
 * Honest figures only: a product nothing has measured reads "Not measured
 * yet", never 0 GiB. 1 GiB = 2^30 bytes; telemetry keeps its existing "GB"
 * label (the observability allowance is defined in 2^30-byte GB).
 */

import {
  StorageBillingBasis,
  StorageProduct,
  type GetStorageUsageResponse,
  type StorageProductUsage,
} from "@/gen/controlplane/services/billing/v1/billing_pb";
import type { GetObservabilityUsageResponse } from "@/gen/controlplane/services/observability_billing/v1/observability_billing_pb";

const GIB = 2 ** 30;

/** Bytes as GiB with one decimal under 100, whole above ("6.4", "120"). */
export function formatGib(bytes: bigint | number): string {
  const gib = Number(bytes) / GIB;
  if (gib > 0 && gib < 0.1) return "< 0.1";
  return gib >= 100 ? Math.round(gib).toLocaleString("en-US") : (Math.round(gib * 10) / 10).toString();
}

const PRODUCT_LABELS: Record<StorageProduct, string> = {
  [StorageProduct.UNSPECIFIED]: "Other",
  [StorageProduct.MACHINE_DISKS]: "Machine disks",
  [StorageProduct.APP_VOLUMES]: "App volumes and databases",
  [StorageProduct.CONTAINER_IMAGES]: "Container images",
  [StorageProduct.STATIC_SITES]: "Static sites",
  [StorageProduct.BUCKETS]: "Buckets",
  [StorageProduct.TEST_REPORTS]: "Test reports",
  [StorageProduct.SBOM_COPIES]: "SBOM copies",
};

export type BasisTone = "success" | "info" | "neutral";

export interface StorageRow {
  key: string;
  label: string;
  /** "6.4 GiB", or "Not measured yet" when the product has no measurement. */
  amount: string;
  measured: boolean;
  basis: { label: string; tone: BasisTone };
}

/** The badge for a product's billing basis today. Not counted wins: it is never in the pool. */
export function basisBadge(p: Pick<StorageProductUsage, "billingBasis" | "countsTowardPool">): StorageRow["basis"] {
  if (p.billingBasis === StorageBillingBasis.NOT_COUNTED || !p.countsTowardPool) return { label: "Not counted", tone: "neutral" };
  if (p.billingBasis === StorageBillingBasis.BILLED) return { label: "Billed", tone: "success" };
  return { label: "Not charged yet", tone: "info" };
}

export function storageRow(p: StorageProductUsage): StorageRow {
  const measured = p.bytesNow !== undefined;
  return {
    key: String(p.product),
    label: PRODUCT_LABELS[p.product] ?? "Other",
    amount: measured ? `${formatGib(p.bytesNow!)} GiB` : "Not measured yet",
    measured,
    basis: basisBadge(p),
  };
}

export interface StoragePoolView {
  /** "x GiB of N GiB". */
  headline: string;
  usedGib: number;
  includedGib: number;
  /** "10 GiB free + your plans' disk", with the plans' figure when there is one. */
  includedLine: string;
  /** True when no counted product is measured: the used figure is unknown, not 0. */
  unmeasured: boolean;
  billingActive: boolean;
  rows: StorageRow[];
}

/** The Storage card from GetStorageUsage. Counted products first, then the rest, in product order. */
export function storagePoolView(res: GetStorageUsageResponse): StoragePoolView {
  const pool = res.pool;
  const included = Number(pool?.includedGib ?? 0n);
  const free = Number(pool?.freeGib ?? 0n);
  const plan = Number(pool?.planIncludedGib ?? 0n);
  const unmeasured = !res.products.some((p) => p.countsTowardPool && p.bytesNow !== undefined);
  const used = pool?.usedBytes ?? 0n;
  const ordered = [...res.products].sort((a, b) => Number(b.countsTowardPool) - Number(a.countsTowardPool) || a.product - b.product);
  return {
    headline: unmeasured ? `Not measured yet · ${included} GiB included` : `${formatGib(used)} GiB of ${included} GiB`,
    usedGib: unmeasured ? 0 : Number(used) / GIB,
    includedGib: included,
    includedLine: plan > 0 ? `${free} GiB free + your plans' disk (${plan} GiB)` : `${free} GiB free + your plans' disk`,
    unmeasured,
    billingActive: pool?.billingActive ?? false,
    rows: ordered.map(storageRow),
  };
}

export interface TelemetryView {
  headline: string;
  used: number;
  allowance: number;
}

/** "x of 10 GB this month" from GetObservabilityUsage. */
export function telemetryView(res: Pick<GetObservabilityUsageResponse, "usedBytes" | "allowanceBytes">): TelemetryView {
  return {
    headline: `${formatGib(res.usedBytes)} of ${formatGib(res.allowanceBytes)} GB this month`,
    used: Number(res.usedBytes) / GIB,
    allowance: Number(res.allowanceBytes) / GIB,
  };
}
