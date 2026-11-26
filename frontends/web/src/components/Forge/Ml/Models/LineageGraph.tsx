// Copyright (c) 2026 Reliant Labs

/**
 * PLACEHOLDER (owned by the Evals+Models implementer): the interactive
 * lineage graph around a subject (GetMlLineage) — datasets → phases →
 * checkpoints → model versions → releases → services, every node a link.
 * Used by the model and version pages and by a run's Config & lineage tab.
 */

import type { MlSubject } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";

export function LineageGraph({ subject }: { subject: Pick<MlSubject, "kind" | "id"> }) {
  return <div data-testid="ml-lineage" className="text-meta text-ink-muted">Lineage of {subject.id}</div>;
}
