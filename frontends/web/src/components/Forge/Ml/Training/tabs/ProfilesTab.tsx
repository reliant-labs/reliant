// Copyright (c) 2026 Reliant Labs

/** PLACEHOLDER (owned by the Training-observability implementer): a run's Profiles tab. */

import type { RunScope } from "../runScope";

export function ProfilesTab({ scope }: { scope: RunScope }) {
  return <div data-testid="ml-run-profiles" className="text-meta text-ink-muted">Profiles of {scope.run.name}</div>;
}
