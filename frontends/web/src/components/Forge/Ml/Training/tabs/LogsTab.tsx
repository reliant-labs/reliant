// Copyright (c) 2026 Reliant Labs

/** PLACEHOLDER (owned by the Training-observability implementer): a run's Logs tab. */

import type { RunScope } from "../runScope";

export function LogsTab({ scope }: { scope: RunScope }) {
  return <div data-testid="ml-run-logs" className="text-meta text-ink-muted">Logs of {scope.run.name}</div>;
}
