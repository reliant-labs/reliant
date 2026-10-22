/**
 * Full-screen, read-only workflow view for the mobile surface.
 *
 * Presentational: takes the workflow and (optionally) its live execution as
 * props rather than fetching them itself, so callers can drive it either
 * from a workflow name (catalog browsing) or from a running chat (execution
 * monitoring) without this component knowing which.
 */

import { useState } from "react";
import { ChevronLeft } from "lucide-react";
import { Link } from "@tanstack/react-router";
import type { Workflow } from "../../types/workflow";
import type { WorkflowExecution } from "../Chat/ExecutionSidebar/types";
import { useExtendedExecutionStatus, findStepExecutionsForNode } from "../workflow/hooks/useExecutionStatus";
import { MobileWorkflowStepList } from "./MobileWorkflowStepList";
import { MobileWorkflowNodeSheet } from "./MobileWorkflowNodeSheet";
import { getWorkflowDisplayName } from "../workflow/useWorkflowInputs";
import { MobileScreenHeader } from "./MobileChrome";
import type { RunStatusDisplay } from "../../lib/runStatus";
import { RunStatusBadge } from "../ui/RunStatusIndicator";

interface MobileWorkflowScreenProps {
  workflow: Workflow;
  /** Live execution, when this workflow is (or was) driving a chat. */
  execution?: WorkflowExecution;
  /**
   * The run's status for the header pill, from lib/runStatus. Absent when
   * there is no run (catalog browsing), which renders no pill.
   */
  runStatus?: RunStatusDisplay;
  /** Connects status to the authoritative node_execution stream — see useExtendedExecutionStatus. */
  chatId?: string | null;
  /** Where the back button goes. Defaults to the workflow catalog. */
  backTo?: string;
}

export function MobileWorkflowScreen({
  workflow,
  execution,
  runStatus,
  chatId,
  backTo = "/m/workflows",
}: MobileWorkflowScreenProps) {
  const [selectedNodeId, setSelectedNodeId] = useState<string | null>(null);

  const nodeIds = (workflow.nodes ?? [])
    .map((n) => n.id)
    .filter((id): id is string => !!id);
  const { statusMap } = useExtendedExecutionStatus(execution, nodeIds, chatId);

  const selectedStep = selectedNodeId
    ? (workflow.nodes ?? []).find((n) => n.id === selectedNodeId)
    : undefined;
  const selectedStepExecutions = selectedNodeId
    ? findStepExecutionsForNode(execution, selectedNodeId, nodeIds)
    : [];

  return (
    <div className="flex h-full min-h-0 flex-col">
      <MobileScreenHeader
        title={getWorkflowDisplayName(workflow.name || "", true)}
        titleClassName="text-base font-semibold"
        leading={
          <Link
            to={backTo}
            // Explicit px, not `h-10 w-10`: rem sizing resolves against the
            // root font-size, and at the smallest Appearance step `h-10`
            // measures under 44px.
            className="flex min-h-[44px] min-w-[44px] items-center justify-center rounded-md text-muted-foreground active:bg-muted"
            aria-label="Back"
          >
            <ChevronLeft className="h-5 w-5" />
          </Link>
        }
        trailing={runStatus ? <RunStatusBadge status={runStatus} /> : undefined}
      />

      <MobileWorkflowStepList
        workflow={workflow}
        statusMap={statusMap}
        onSelectNode={setSelectedNodeId}
        selectedNodeId={selectedNodeId}
      />

      {/* Read-only surface — editing (drag-and-drop graph, CEL/YAML) is
          desktop-only, so this is the entirety of the "edit" affordance. */}
      <p className="shrink-0 border-t border-border px-4 py-2 text-center text-xs text-muted-foreground">
        Edit this workflow on desktop.
      </p>

      {selectedNodeId && (
        <MobileWorkflowNodeSheet
          nodeId={selectedNodeId}
          step={selectedStep}
          stepExecutions={selectedStepExecutions}
          onClose={() => setSelectedNodeId(null)}
        />
      )}
    </div>
  );
}
