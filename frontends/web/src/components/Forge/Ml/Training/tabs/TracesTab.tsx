// Copyright (c) 2026 Reliant Labs

/**
 * A run's Traces tab (02-views §3.7): Observability's trace explorer over the
 * traces the step tracer kept for the scope (slow steps, checkpoint saves,
 * data pipeline batches, an attempt's start from Kueue admission to its
 * first step), filtered by the scope's `ml.*` attributes. A trace opens in
 * Observability's waterfall; every span carries its run, rank, node and GPU.
 */

import { MlExplorer } from "../../kit/explorer";
import { scopeRange, type RunScope } from "../runScope";

export function TracesTab({ scope }: { scope: RunScope }) {
  return <MlExplorer view="traces" subject={scope.subject} label={scope.subject.label || scope.run.name} range={scopeRange(scope)} testId="ml-run-traces" />;
}
