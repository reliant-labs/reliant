// Copyright (c) 2026 Reliant Labs

/** PLACEHOLDER (owned by the Training-observability implementer): a run's System tab. */

import type { RunScope } from "../runScope";

export function SystemTab({ scope }: { scope: RunScope }) {
  return <div data-testid="ml-run-system" className="text-meta text-ink-muted">System of {scope.run.name}</div>;
}
