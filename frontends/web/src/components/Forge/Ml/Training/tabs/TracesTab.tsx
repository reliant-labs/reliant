// Copyright (c) 2026 Reliant Labs

/** PLACEHOLDER (owned by the Training-observability implementer): a run's Traces tab. */

import type { RunScope } from "../runScope";

export function TracesTab({ scope }: { scope: RunScope }) {
  return <div data-testid="ml-run-traces" className="text-meta text-ink-muted">Traces of {scope.run.name}</div>;
}
