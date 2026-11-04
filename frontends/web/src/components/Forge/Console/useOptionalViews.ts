// Copyright (c) 2025 Reliant Labs

import { useServiceSignals } from "@/hooks/observability-queries";
import { useSessions } from "@/hooks/observability-session-queries";
import type { ObsScope, ServiceSignalsResult } from "@/services/controlPlane/observability/types";
import type { SessionList } from "@/services/controlPlane/observability/rumTypes";

/** Definite empty readings hide a nav row; partial, stale-on-error, loading or
 * unavailable readings never do. Direct links stay routable in all cases. */
export function optionalViews(signals: ServiceSignalsResult | undefined, sessions: SessionList | undefined) {
  return {
    profiles: !signals || signals.truncated || signals.services.some((service) => service.emitting.includes("profiles") || service.declared.includes("profiles")),
    sessions: !sessions || sessions.sessions.length > 0 || !!sessions.nextPageToken,
  };
}

export function useOptionalViews(scope: ObsScope | null) {
  const profiles = useServiceSignals(scope, "7d");
  const sessions = useSessions(scope, { range: "7d", hasError: false, hasReplay: false, userId: "", pathContains: "" });
  return optionalViews(
    scope && profiles.isSuccess && !profiles.isFetching ? profiles.data : undefined,
    scope && sessions.isSuccess && !sessions.isFetching && !sessions.isPlaceholderData ? sessions.data : undefined
  );
}
