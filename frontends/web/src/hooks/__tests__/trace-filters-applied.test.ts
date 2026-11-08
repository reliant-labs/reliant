// @vitest-environment node
// Copyright (c) 2025 Reliant Labs

/**
 * When the traces list refuses an answer as unfiltered: a row naming another
 * value always proves the filter was ignored; rows naming none prove it only
 * when nothing narrowed the counted spans away from the spans that carry it.
 */

import { describe, expect, it } from "vitest";

import { assertTraceFiltersApplied, type TraceFilters } from "@/hooks/observability-queries";
import { FilterUnsupportedError } from "@/services/controlPlane/observability/errors";
import { NO_CLIENT } from "@/services/controlPlane/observability/client";
import type { TraceSummary } from "@/services/controlPlane/observability/types";

const base: TraceFilters = { range: "1h", rootSpanName: "", services: [], errorsOnly: false, minDurationMs: 0 };

function row(over: Partial<TraceSummary> = {}): TraceSummary {
  return {
    traceId: "t",
    rootService: "api",
    rootOperation: "GET /",
    startMs: 0,
    durationMs: 1,
    rootDurationMs: 1,
    spanCount: 1,
    errorCount: 0,
    services: ["api"],
    userId: "",
    sessionId: "",
    country: "",
    region: "",
    city: "",
    client: NO_CLIENT,
    frontendVersion: "",
    backendBuilds: [],
    source: "",
    operatorData: false,
    ...over,
  };
}
const chrome = (over: Partial<TraceSummary> = {}) => row({ client: { ...NO_CLIENT, browserName: "Chrome", osName: "macOS" }, frontendVersion: "f1", ...over });

describe("assertTraceFiltersApplied", () => {
  it("accepts rows that all name the filtered value", () => {
    expect(() => assertTraceFiltersApplied({ ...base, browser: "Chrome", os: "macOS", frontendVersion: "f1" }, [chrome(), chrome()], false)).not.toThrow();
    expect(() =>
      assertTraceFiltersApplied({ ...base, backendVersion: "v2" }, [row({ backendBuilds: [{ service: "api", version: "v1" }, { service: "worker", version: "v2" }] })], false)
    ).not.toThrow();
  });

  it("refuses a row that names another value", () => {
    expect(() => assertTraceFiltersApplied({ ...base, browser: "Firefox" }, [chrome()], true)).toThrow(FilterUnsupportedError);
    expect(() => assertTraceFiltersApplied({ ...base, userId: "u1" }, [row({ userId: "u2" })], true)).toThrow(FilterUnsupportedError);
    expect(() => assertTraceFiltersApplied({ ...base, backendVersion: "v2" }, [row({ backendBuilds: [{ service: "api", version: "v1" }] })], true)).toThrow(
      FilterUnsupportedError
    );
  });

  it("refuses rows naming nothing from an un-narrowed list: an older control plane's signature", () => {
    expect(() => assertTraceFiltersApplied({ ...base, browser: "Chrome" }, [row(), row()], false)).toThrow(FilterUnsupportedError);
    expect(() => assertTraceFiltersApplied({ ...base, sessionId: "s1" }, [row()], false)).toThrow(FilterUnsupportedError);
  });

  it("accepts rows naming nothing when a service or attribute filter narrowed the counted spans", () => {
    for (const f of [{ browser: "Chrome" }, { os: "macOS" }, { frontendVersion: "f1" }, { userId: "u1" }, { backendVersion: "v1" }]) {
      expect(() => assertTraceFiltersApplied({ ...base, services: ["api"], ...f }, [row(), row()], true)).not.toThrow();
    }
  });
});
