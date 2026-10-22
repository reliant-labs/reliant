// Copyright (c) 2025 Reliant Labs

/**
 * Small badges for the Workflows area, on forge's Badge (WORKFLOW_UI.md §11):
 * a workflow's source as a NEUTRAL badge — replacing the old hub's blue /
 * emerald / violet tints, which coded an attribute nobody acts on as if it
 * were a status — and "Default" as info.
 *
 * Wrapped in `.forge-ui` because forge's accent tokens resolve only inside
 * that scope (see RunStatusIndicator).
 */

import Badge from "../forge-ui/badge";

export type WorkflowSource = "builtin" | "user" | "project";

export const WORKFLOW_SOURCE_LABEL: Record<WorkflowSource, string> = {
  builtin: "Built-in",
  project: "Project",
  user: "Mine",
};

export function WorkflowBadge({
  label,
  variant,
  testId,
}: {
  label: string;
  variant: "neutral" | "info" | "warning" | "error";
  testId?: string;
}) {
  return (
    <span className="forge-ui inline-flex" data-testid={testId}>
      {/* md, not sm: sm renders under the 12px label floor. */}
      <Badge label={label} variant={variant} size="md" />
    </span>
  );
}

export function WorkflowSourceBadge({ source }: { source: WorkflowSource }) {
  return <WorkflowBadge label={WORKFLOW_SOURCE_LABEL[source]} variant="neutral" testId="workflow-source-badge" />;
}
