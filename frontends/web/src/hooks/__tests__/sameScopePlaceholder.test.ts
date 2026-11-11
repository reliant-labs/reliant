// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";

import type { ObsScope } from "@/services/controlPlane/observability/types";
import { obsKeys, sameScopePlaceholder } from "../observability-queries";

const shop: ObsScope = { orgId: "org", projectId: "shop", envIds: [], filters: [] };
const other: ObsScope = { orgId: "org", projectId: "other", envIds: [], filters: [] };

describe("sameScopePlaceholder", () => {
  it("keeps the last rows on screen when only the range changed", () => {
    expect(sameScopePlaceholder(shop, "rows", obsKeys.workloads(shop, "24h", []))).toBe("rows");
  });

  it("never shows another scope's rows in place of this one's", () => {
    expect(sameScopePlaceholder(shop, "rows", obsKeys.workloads(other, "24h", []))).toBeUndefined();
    expect(sameScopePlaceholder(shop, "rows", obsKeys.workloads({ ...shop, envIds: ["prod"] }, "24h", []))).toBeUndefined();
    expect(sameScopePlaceholder(shop, "rows", undefined)).toBeUndefined();
  });
});
