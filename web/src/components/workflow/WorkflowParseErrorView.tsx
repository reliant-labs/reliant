/**
 * WorkflowParseErrorView - Shows when a workflow fails to parse
 *
 * This view is displayed when a stored workflow has invalid YAML that can't be parsed.
 * It allows the user to:
 * - See the error message
 * - Clear the corrupted workflow state
 * - Use the chat assistant to help fix the issue
 */

import { useState } from "react";
import { ArrowLeft, Trash2, ChevronDown, ChevronUp, RefreshCw, MessageCircle } from "lucide-react";
import { Button } from "../ui/Button";
import { cn } from "../../lib/utils";
import { WorkflowBuilderChat, type PanelSize } from "./WorkflowBuilderChat";
import { useProjectStore } from "../../store/projectStore";
import type { Workflow } from "../../types/workflow";

interface WorkflowParseErrorViewProps {
  /** Name of the workflow that failed to parse */
  workflowName: string;
  /** The parse error message */
  parseError: string;
  /** Raw YAML definition (if available) */
  rawDefinition?: string;
  /** Draft ID for the workflow */
  draftId?: string;
  /** Chat ID associated with this workflow */
  builderChatId?: string;
  /** Callback to go back to hub */
  onBack: () => void;
  /** Callback to clear/delete the corrupted workflow */
  onClear: () => void;
  /** Callback when the workflow has been fixed (reload attempt) */
  onFixed: () => void;
  /** Callback when a new chat is created */
  onChatIdChange?: (chatId: string) => void;
  /** Callback when draft ID changes (when backend creates a new draft) */
  onDraftIdChange?: (draftId: string) => void;
}

export function WorkflowParseErrorView({
  workflowName,
  parseError,
  rawDefinition,
  draftId,
  builderChatId,
  onBack,
  onClear,
  onFixed,
  onChatIdChange,
  onDraftIdChange,
}: WorkflowParseErrorViewProps) {
  const [showRawYaml, setShowRawYaml] = useState(false);
  const [chatOpen, setChatOpen] = useState(true);
  const [chatPanelSize, setChatPanelSize] = useState<PanelSize>("normal");
  const projectId = useProjectStore((state) => state.currentProject?.id);

  // Create a minimal workflow definition for the chat
  // This allows the chat to still function even though parsing failed
  const placeholderWorkflow: Workflow = {
    name: workflowName,
    description: "",
    nodes: [],
    edges: [],
    entry: [],
    params: {},
    outputs: {},
    apiVersion: "",
  };

  return (
    <div className="h-full w-full bg-background flex flex-col">
      {/* Header */}
      <div className="flex items-center justify-between px-4 py-3 border-b border-border bg-muted/30">
        <div className="flex items-center gap-3">
          <Button
            variant="ghost"
            size="sm"
            onClick={onBack}
            className="gap-2"
          >
            <ArrowLeft className="h-4 w-4" />
            Back
          </Button>
          <span className="text-muted-foreground">/</span>
          <span className="font-medium truncate max-w-[300px]">{workflowName}</span>
        </div>
        <div className="flex items-center gap-2">
          <Button
            variant="outline"
            size="sm"
            onClick={onFixed}
            className="gap-2"
          >
            <RefreshCw className="h-4 w-4" />
            Retry Load
          </Button>
          <Button
            variant="destructive"
            size="sm"
            onClick={onClear}
            className="gap-2"
          >
            <Trash2 className="h-4 w-4" />
            Clear Workflow
          </Button>
        </div>
      </div>

      {/* Error content */}
      <div className="flex-1 flex overflow-hidden">
        {/* Main content area */}
        <div className={cn(
          "flex-1 overflow-auto p-6 transition-all duration-200",
          chatOpen && chatPanelSize === "normal" && "mr-[400px]",
          chatOpen && chatPanelSize === "maximized" && "mr-[600px]"
        )}>
          <div className="max-w-2xl mx-auto space-y-6">
            {/* Error alert */}
            <div className="rounded-lg border border-destructive/50 bg-destructive/10 p-4">
              <h2 className="text-lg font-semibold text-destructive mb-2">
                Failed to Parse Workflow
              </h2>
              <p className="text-sm text-muted-foreground mb-3">
                The stored workflow definition contains invalid YAML that couldn't be parsed.
              </p>
              <pre className="bg-background/50 rounded p-3 text-sm font-mono overflow-x-auto whitespace-pre-wrap break-all border border-border">
                {parseError}
              </pre>
            </div>

            {/* Options */}
            <div className="space-y-4">
              <h3 className="font-medium">What would you like to do?</h3>
              
              <div className="grid gap-3">
                <div className="flex items-start gap-3 p-3 rounded-lg border border-border bg-card hover:bg-accent/50 transition-colors">
                  <MessageCircle className="h-5 w-5 text-primary mt-0.5 shrink-0" />
                  <div>
                    <p className="font-medium">Chat with the assistant</p>
                    <p className="text-sm text-muted-foreground">
                      The workflow builder assistant can help diagnose and fix the YAML error.
                      Use the chat panel on the right to describe the issue.
                    </p>
                  </div>
                </div>

                <div className="flex items-start gap-3 p-3 rounded-lg border border-border bg-card hover:bg-accent/50 transition-colors">
                  <Trash2 className="h-5 w-5 text-destructive mt-0.5 shrink-0" />
                  <div>
                    <p className="font-medium">Clear the workflow</p>
                    <p className="text-sm text-muted-foreground">
                      Delete the corrupted workflow and start fresh. This cannot be undone.
                    </p>
                  </div>
                </div>
              </div>
            </div>

            {/* Raw YAML (collapsible) */}
            {rawDefinition && (
              <div className="border border-border rounded-lg overflow-hidden">
                <button
                  onClick={() => setShowRawYaml(!showRawYaml)}
                  className="w-full flex items-center justify-between px-4 py-3 bg-muted/30 hover:bg-muted/50 transition-colors"
                >
                  <span className="font-medium text-sm">Raw YAML Definition</span>
                  {showRawYaml ? (
                    <ChevronUp className="h-4 w-4 text-muted-foreground" />
                  ) : (
                    <ChevronDown className="h-4 w-4 text-muted-foreground" />
                  )}
                </button>
                {showRawYaml && (
                  <div className="p-4 bg-background">
                    <pre className="text-xs font-mono overflow-x-auto whitespace-pre-wrap break-all max-h-[400px] overflow-y-auto">
                      {rawDefinition}
                    </pre>
                  </div>
                )}
              </div>
            )}
          </div>
        </div>

        {/* Chat panel */}
        {projectId && (
          <WorkflowBuilderChat
            workflow={placeholderWorkflow}
            onWorkflowChange={() => {
              // When the chat modifies the workflow, try to reload it
              onFixed();
            }}
            projectId={projectId}
            isOpen={chatOpen}
            onOpenChange={setChatOpen}
            panelSize={chatPanelSize}
            onPanelSizeChange={setChatPanelSize}
            builderChatId={builderChatId}
            draftId={draftId}
            onChatIdChange={onChatIdChange}
            onDraftIdChange={onDraftIdChange}
          />
        )}
      </div>
    </div>
  );
}
