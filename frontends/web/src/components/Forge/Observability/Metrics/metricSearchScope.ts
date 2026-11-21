// Copyright (c) 2025 Reliant Labs

import { useMemo } from "react";

import { DEFAULT_RANGE } from "@/services/controlPlane/observability/window";

import { rangeParam } from "../Dashboards/dashboardModel";
import { filterTokensOf, scopeFiltersOf } from "../obsFilters";

/** The catalog warmup and explorer must use the same range and filters. */
export function useMetricSearchScope({ range, f }: Record<string, unknown>) {
  const filterTokens = useMemo(() => filterTokensOf(Array.isArray(f) ? f.map(String) : typeof f === "string" ? [f] : undefined), [f]);
  const scopeFilters = useMemo(() => scopeFiltersOf(filterTokens), [filterTokens]);
  return { range: rangeParam(range) ?? DEFAULT_RANGE, filterTokens, scopeFilters };
}
