// Copyright (c) 2025 Reliant Labs

import { useMemo } from "react";

import { useServiceSignals } from "@/hooks/observability-queries";
import type { ObsScope, ServiceSignalsResult } from "@/services/controlPlane/observability/types";

/** Definite empty readings hide a nav row; partial, stale-on-error, loading or
 * unavailable readings never do. Direct links stay routable in all cases.
 *
 * Sessions is never optional: its empty state is where a reader learns how to
 * turn replay on, so hiding the row until a replay exists would hide that. */
export function optionalViews(signals: ServiceSignalsResult | undefined) {
  return {
    profiles: !signals || signals.truncated || signals.services.some((service) => service.emitting.includes("profiles") || service.declared.includes("profiles")),
  };
}

export function useOptionalViews(scope: ObsScope | null) {
  const signals = useServiceSignals(scope, "7d");
  const { profiles } = optionalViews(scope && signals.isSuccess && !signals.isFetching ? signals.data : undefined);
  // The same object while the answer stands: it is a prop of the console's memoized chrome.
  return useMemo(() => ({ profiles }), [profiles]);
}
