// Copyright (c) 2026 Reliant Labs

/** PLACEHOLDER (owned by the Training-observability implementer): a run's Metrics tab. */

import type { RunScope } from "../runScope";

export function MetricsTab({ scope }: { scope: RunScope }) {
  return <div data-testid="ml-run-metrics" className="text-meta text-ink-muted">Metrics of {scope.run.name}</div>;
}
