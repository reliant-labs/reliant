// Copyright (c) 2026 Reliant Labs

/**
 * The reads the Services list and a service's page share: the app's live
 * state (LiveState/useAppLiveState.ts, the one hook the Overview's strip and
 * the Live state dashboard read too), plus the console's strip environments,
 * which a service's page needs for its recent deploys (who promoted what).
 */

import type { RangePreset } from "@/services/controlPlane/observability/window";

import { useForgeConsole } from "../Console/consoleContext";
import { useAppLiveState } from "../LiveState/useAppLiveState";
import { useStripEnvs } from "../Workloads/WorkloadPages";

export interface ServicesSearch {
  range?: RangePreset;
  metric?: string;
}

export function useServicesData(range: RangePreset) {
  const { forgeProject, env } = useForgeConsole();
  const strip = useStripEnvs();
  const live = useAppLiveState(forgeProject, env, range);
  return { ...live, envs: strip.envs, envsLoading: strip.loading || live.envsLoading };
}
