// Copyright (c) 2026 Reliant Labs

/**
 * A run's Profiles tab (02-views §3.8): Observability's flamegraph of the
 * trainer over the scope (GetFlamegraph, filtered by the scope's `ml.*`
 * attributes): CPU from the continuous profiler, GPU kernels from the
 * always-on CUPTI sampler, by profile type.
 */

import { MlExplorer } from "../../kit/explorer";
import { scopeRange, type RunScope } from "../runScope";

/** The service a trainer's pods report as (forge's ml/instrument skill). */
export const TRAINER_SERVICE = "trainer";

export function ProfilesTab({ scope }: { scope: RunScope }) {
  return <MlExplorer view="profiles" subject={scope.subject} label={scope.subject.label || scope.run.name} range={scopeRange(scope)} service={TRAINER_SERVICE} testId="ml-run-profiles" />;
}
