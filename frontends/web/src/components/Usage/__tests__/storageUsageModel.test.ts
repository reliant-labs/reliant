// Copyright (c) 2026 Reliant Labs

import { describe, expect, it } from "vitest";

import {
  StorageBillingBasis,
  StorageProduct,
  type GetStorageUsageResponse,
  type StorageProductUsage,
} from "@/gen/controlplane/services/billing/v1/billing_pb";

import { basisBadge, formatGib, storagePoolView, storageRow, telemetryView } from "../storageUsageModel";

const GIB = 2n ** 30n;
const product = (p: Partial<StorageProductUsage>) =>
  ({ product: StorageProduct.BUCKETS, countsTowardPool: true, billingBasis: StorageBillingBasis.TRACKED, ...p }) as StorageProductUsage;
const response = (products: StorageProductUsage[], pool: Partial<NonNullable<GetStorageUsageResponse["pool"]>> = {}) =>
  ({ pool: { freeGib: 10n, planIncludedGib: 50n, includedGib: 60n, usedBytes: 12n * GIB, billingActive: false, ...pool }, products }) as unknown as GetStorageUsageResponse;

describe("storageUsageModel", () => {
  it("formats GiB", () => {
    expect(formatGib(0n)).toBe("0");
    expect(formatGib(GIB / 20n)).toBe("< 0.1");
    expect(formatGib((64n * GIB) / 10n)).toBe("6.4");
    expect(formatGib(1200n * GIB)).toBe("1,200");
  });

  it("badges each basis, and a product outside the pool is Not counted", () => {
    expect(basisBadge(product({ billingBasis: StorageBillingBasis.BILLED })).label).toBe("Billed");
    expect(basisBadge(product({ billingBasis: StorageBillingBasis.TRACKED })).label).toBe("Not charged yet");
    expect(basisBadge(product({ billingBasis: StorageBillingBasis.NOT_COUNTED, countsTowardPool: false })).label).toBe("Not counted");
    expect(basisBadge(product({ billingBasis: StorageBillingBasis.TRACKED, countsTowardPool: false })).label).toBe("Not counted");
  });

  it("shows an unmeasured product as Not measured yet, never 0", () => {
    expect(storageRow(product({ bytesNow: undefined })).amount).toBe("Not measured yet");
    expect(storageRow(product({ bytesNow: 0n })).amount).toBe("0 GiB");
  });

  it("reads the pool headline and orders counted products first", () => {
    const view = storagePoolView(
      response([
        product({ product: StorageProduct.SBOM_COPIES, countsTowardPool: false, billingBasis: StorageBillingBasis.NOT_COUNTED, bytesNow: GIB }),
        product({ product: StorageProduct.MACHINE_DISKS, billingBasis: StorageBillingBasis.BILLED, bytesNow: 12n * GIB }),
      ]),
    );
    expect(view.headline).toBe("12 GiB of 60 GiB");
    expect(view.includedLine).toBe("10 GiB free + your plans' disk (50 GiB)");
    expect(view.rows.map((r) => r.label)).toEqual(["Machine disks", "SBOM copies"]);
    expect(view.billingActive).toBe(false);
  });

  it("never claims 0 used when no counted product is measured", () => {
    const view = storagePoolView(response([product({ bytesNow: undefined })], { usedBytes: 0n, planIncludedGib: 0n, includedGib: 10n }));
    expect(view.unmeasured).toBe(true);
    expect(view.headline).toBe("Not measured yet · 10 GiB included");
    expect(view.includedLine).toBe("10 GiB free + your plans' disk");
  });

  it("reads telemetry as x of 10 GB this month", () => {
    expect(telemetryView({ usedBytes: (64n * GIB) / 10n, allowanceBytes: 10n * GIB }).headline).toBe("6.4 of 10 GB this month");
  });
});
