// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";

import { observabilityServiceKeys } from "@/hooks/controlplane/observability-service-hooks_gen";
import { relativeWindow, workloadsRequest } from "@/services/controlPlane/observability/api";
import type { ObsScope } from "@/services/controlPlane/observability/types";
import { sameScopePlaceholder } from "../observability-queries";

const shop: ObsScope = { orgId: "org", projectId: "shop", envIds: [], filters: [] };
const other: ObsScope = { orgId: "org", projectId: "other", envIds: [], filters: [] };

/** useWorkloads' generated key: its request carries the scope and the window. */
const workloadsKey = (scope: ObsScope, lastMs = 86_400_000, services: string[] = []) =>
  observabilityServiceKeys.listWorkloads(workloadsRequest({ ...scope, window: relativeWindow(lastMs), services }));

describe("sameScopePlaceholder", () => {
  it("keeps the last rows on screen when only the range or the services changed", () => {
    expect(sameScopePlaceholder(shop, "rows", workloadsKey(shop))).toBe("rows");
    expect(sameScopePlaceholder(shop, "rows", workloadsKey(shop, 3_600_000, ["api"]))).toBe("rows");
  });

  it("never shows another scope's rows in place of this one's", () => {
    expect(sameScopePlaceholder(shop, "rows", workloadsKey(other))).toBeUndefined();
    expect(sameScopePlaceholder(shop, "rows", workloadsKey({ ...shop, envIds: ["prod"] }))).toBeUndefined();
    expect(sameScopePlaceholder(shop, "rows", workloadsKey({ ...shop, filters: [{ key: "service.name", op: "eq", value: "api" }] }))).toBeUndefined();
    expect(sameScopePlaceholder(shop, "rows", undefined)).toBeUndefined();
    expect(sameScopePlaceholder(null, "rows", workloadsKey(shop))).toBeUndefined();
  });
});
