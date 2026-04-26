import { useCallback, useState, useEffect, useRef, useMemo } from "react";
import { Check, X, Info, ChevronDown, ChevronRight } from "lucide-react";
import { CELExpressionInput } from "../CELInput";
import type {
  Step,
  RunStep,
  ActionStep,
  WorkflowStep,
  JoinStep,
  LoopStep,
  RouterStep,
} from "../../../types/workflow";
import {
  isRunStep,
  isActionStep,
  isWorkflowStep,
  isJoinStep,
  isLoopStep,
  isRouterStep,
  getStepCommand,
  getStepProject,
  getStepRef,
  getStepInline,
  getStepThread,
} from "../../../types/workflow";
import { ConfigurationPanel } from "../ConfigurationPanel";
import { workflowGrpc, type WorkflowResponse } from "../../../api/workflow-grpc";
import { useProjectStore } from "../../../store/projectStore";
import { directCel, celString } from "../../../lib/celAdapter";
import { SaveMessageConfigEditor } from "../SaveMessageConfigEditor";
import { NodeThreadConfigEditor } from "../NodeThreadConfigEditor";
import { getConditionExpression, getStringValue, AdvancedProjectSettings } from "./shared";
import { RunStepConfig } from "./RunStepConfig";
import { ActionStepConfig } from "./ActionStepConfig";
import { WorkflowStepConfig } from "./WorkflowStepConfig";
import { JoinStepConfig } from "./JoinStepConfig";
import { LoopStepConfig } from "./LoopStepConfig";
import { RouterStepConfig } from "./RouterStepConfig";
import { NodeOutputsPanel } from "./NodeOutputsPanel";
import { ConfigPanelTabBar, type ConfigTab } from "./ConfigPanelTabBar";

import { getCatalogClient } from "../../../api/grpc-client";
import type { NodeInfo } from "../../../gen/reliant/v1/catalog_pb";
import { withWorkflowArgs, withLoopArgs, withRouterArgs } from "../../../types/workflow";
import "./config-panel.css";

interface ConfigPanelProps {
  step: Step;
  onUpdate: (step: Step) => void;
  onClose: () => void;
  onDelete: () => void;
  /** Called when user wants to edit an inline loop body */
  onEditLoopBody?: (step: LoopStep) => void;
  /** Called when user wants to edit an inline workflow body */
  onEditInlineWorkflowBody?: (step: WorkflowStep) => void;
  /** Called when user renames a node - handles edge updates */
  onRename?: (oldId: string, newId: string) => void;
  /** List of existing node IDs for validation (to prevent duplicates) */
  existingNodeIds?: string[];
  /** Space to reserve at bottom when expanded (e.g., for chat panel) */
  bottomOffset?: number;
  /** Distance from top to align with left sidebar */
  topOffset?: number;
  /** Current workflow name (for filtering self-references in workflow steps) */
  currentWorkflowName?: string;
  /** Whether this node is inside a loop (affects thread config UI) */
  isInLoop?: boolean;
  /** Whether the panel is in read-only mode (for viewing builtin workflows) */
  isReadOnly?: boolean;
}

// Validate node ID format - CEL-safe identifiers only.
function isValidNodeId(id: string): boolean {
  return /^[a-zA-Z][a-zA-Z0-9_]*$/.test(id);
}

/** Whether this step type supports thread configuration */
function hasThreadSupport(step: Step): boolean {
  return isWorkflowStep(step) || isLoopStep(step) || isRouterStep(step);
}

/** Whether this step type supports project configuration */
function hasProjectSupport(step: Step): boolean {
  return isWorkflowStep(step) || isLoopStep(step) || isRouterStep(step);
}

export function ConfigPanel({
  step,
  onUpdate,
  onClose,
  onDelete,
  onEditLoopBody,
  onEditInlineWorkflowBody,
  onRename,
  existingNodeIds = [],
  bottomOffset,
  topOffset,
  currentWorkflowName,
  isInLoop = false,
  isReadOnly = false,
}: ConfigPanelProps) {
  const [_workflows, setWorkflows] = useState<WorkflowResponse[]>([]);
  const [catalogNodes, setCatalogNodes] = useState<NodeInfo[]>([]);
  const currentProject = useProjectStore((state) => state.currentProject);
  const projectId = currentProject?.id;

  // Tab state
  const [activeTab, setActiveTab] = useState("config");

  // Collapsible section state for Advanced tab
  const [isSaveMessageExpanded, setIsSaveMessageExpanded] = useState(false);
  const [isProjectExpanded, setIsProjectExpanded] = useState(false);

  // Reset tab when step changes
  useEffect(() => {
    setActiveTab("config");
  }, [step.id]);

  // Node ID editing state
  const [isEditingId, setIsEditingId] = useState(false);
  const [editedId, setEditedId] = useState(step.id);
  const [idError, setIdError] = useState<string | null>(null);
  const idInputRef = useRef<HTMLInputElement>(null);

  // Sync editedId when step changes
  useEffect(() => {
    setEditedId(step.id);
    setIsEditingId(false);
    setIdError(null);
  }, [step.id]);

  // Focus input when entering edit mode
  useEffect(() => {
    if (isEditingId && idInputRef.current) {
      idInputRef.current.focus();
      idInputRef.current.select();
    }
  }, [isEditingId]);

  // Validate and save the new ID
  const handleIdSave = useCallback(() => {
    const trimmedId = (editedId || '').trim();

    // Validation
    if (!trimmedId) {
      setIdError("ID cannot be empty");
      return;
    }
    if (!isValidNodeId(trimmedId)) {
      setIdError(
        "ID must start with a letter and contain only letters, numbers, or underscores",
      );
      return;
    }
    if (trimmedId !== step.id && existingNodeIds.includes(trimmedId)) {
      setIdError("A node with this ID already exists");
      return;
    }

    // If ID changed, call onRename
    if (trimmedId !== step.id && step.id) {
      if (onRename) {
        onRename(step.id, trimmedId);
      }
    }

    setIsEditingId(false);
    setIdError(null);
  }, [editedId, step.id, existingNodeIds, onRename]);

  // Cancel editing
  const handleIdCancel = useCallback(() => {
    setEditedId(step.id);
    setIsEditingId(false);
    setIdError(null);
  }, [step.id]);

  // Handle key presses in the ID input
  const handleIdKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      if (e.key === "Enter") {
        e.preventDefault();
        handleIdSave();
      } else if (e.key === "Escape") {
        e.preventDefault();
        handleIdCancel();
      }
    },
    [handleIdSave, handleIdCancel],
  );

  // Fetch workflows for loop step selector
  useEffect(() => {
    if (!isLoopStep(step) || !projectId) return;
    let cancelled = false;
    workflowGrpc
      .listWorkflows(projectId)
      .then((wfs) => { if (!cancelled) setWorkflows(wfs); })
      .catch(console.error);
    return () => { cancelled = true; };
  }, [step, projectId]);

  // Fetch catalog nodes for output fields
  useEffect(() => {
    let cancelled = false;
    const client = getCatalogClient();
    client.listNodes({}).then((res) => {
      if (!cancelled) setCatalogNodes(res.nodes || []);
    }).catch(console.error);
    return () => { cancelled = true; };
  }, []);

  // Fetch referenced workflow def for output resolution
  const [refWorkflowOutputs, setRefWorkflowOutputs] = useState<Record<string, string> | undefined>();

  const stepRef = (isWorkflowStep(step) || isLoopStep(step)) && !getStepInline(step)
    ? getStepRef(step)
    : '';

  useEffect(() => {
    if (!projectId || !stepRef) { setRefWorkflowOutputs(undefined); return; }

    let cancelled = false;
    workflowGrpc.getWorkflow(projectId, { name: stepRef }).then((res) => {
      if (!cancelled) {
        setRefWorkflowOutputs(res.workflow?.outputs ?? undefined);
      }
    }).catch(() => {
      if (!cancelled) setRefWorkflowOutputs(undefined);
    });
    return () => { cancelled = true; };
  }, [projectId, stepRef]);

  const updateStep = useCallback(
    (updates: Partial<Step>) => {
      onUpdate({ ...step, ...updates } as Step);
    },
    [step, onUpdate],
  );

  // Badge computation (workflow + loop thread support)
  const stepThread = getStepThread(step);
  const threadMode = stepThread?.mode || 'inherit';
  const hasThreadBadge = threadMode !== 'inherit';
  const hasInjectBadge = !!stepThread?.inject?.content;
  const hasSaveMessageConfig = !!step.saveMessage;
  const hasProjectConfig = !!getStepProject(step)?.path;
  const hasConditionConfig = !!getConditionExpression(step.condition);
  const hasAdvancedBadge = hasSaveMessageConfig || hasProjectConfig || hasConditionConfig;

  // Determine if Config tab has content
  const hasConfigContent = useMemo(() => {
    // Non-action step types always have config content
    if (!isActionStep(step)) return true;
    // Action steps: check if the catalog node has input fields
    const node = catalogNodes.find((n) => n.id === step.type);
    // While loading (no nodes yet), show Config tab
    if (catalogNodes.length === 0) return true;
    // Show if there are input fields or a description, or if it's call_llm (has response tool editor)
    return (node?.inputFields?.length ?? 0) > 0 || step.type === 'call_llm';
  }, [step, catalogNodes]);

  // Build tabs list
  const tabs = useMemo((): ConfigTab[] => {
    const result: ConfigTab[] = [];

    if (hasConfigContent) {
      result.push({ id: "config", label: "Config" });
    }

    if (hasThreadSupport(step)) {
      result.push({ id: "thread", label: "Thread", hasBadge: hasThreadBadge });
      result.push({ id: "inject", label: "Inject", hasBadge: hasInjectBadge });
    }

    if (!isJoinStep(step)) {
      result.push({ id: "advanced", label: "Advanced", hasBadge: hasAdvancedBadge });
    }

    result.push({ id: "outputs", label: "Outputs" });

    return result;
  }, [step, hasConfigContent, hasThreadBadge, hasInjectBadge, hasAdvancedBadge]);

  // If active tab is no longer available (e.g. Config hidden after catalog loads), select first tab
  useEffect(() => {
    if (tabs.length > 0 && !tabs.some(t => t.id === activeTab)) {
      setActiveTab(tabs[0].id);
    }
  }, [tabs, activeTab]);

  // Get step title - shows the actual value or defaults to type
  const getStepTitle = () => {
    if (isRunStep(step)) {
      return getStepCommand(step) || "Run";
    }
    if (isActionStep(step)) {
      // Format snake_case type to Title Case (e.g., "call_llm" -> "Call LLM")
      const formatType = (type: string) => {
        const acronyms: Record<string, string> = {
          llm: "LLM",
          api: "API",
          url: "URL",
          id: "ID",
          mcp: "MCP",
        };
        return type
          .split("_")
          .map(
            (word) =>
              acronyms[word.toLowerCase()] ||
              word.charAt(0).toUpperCase() + word.slice(1),
          )
          .join(" ");
      };
      return formatType(step.type || '') || "Action";
    }
    if (isWorkflowStep(step)) {
      return "Workflow";
    }
    if (isJoinStep(step)) {
      return `Join (${step.condition})`;
    }
    if (isLoopStep(step)) {
      return "Loop";
    }
    if (isRouterStep(step)) {
      return "Router";
    }
    return "Step";
  };

  // Helper to update project on workflow/loop/router steps
  const updateProject = useCallback(
    (p: { path?: string } | undefined) => {
      if (isWorkflowStep(step)) {
        onUpdate(
          withWorkflowArgs(step as WorkflowStep, {
            project: p ? { path: celString(p.path ?? "") } as any : undefined,
          }) as Step,
        );
      } else if (isLoopStep(step)) {
        onUpdate(
          withLoopArgs(step as LoopStep, {
            project: p ? { path: celString(p.path ?? "") } as any : undefined,
          }) as Step,
        );
      } else if (isRouterStep(step)) {
        onUpdate(
          withRouterArgs(step, {
            project: p ? { path: celString(p.path ?? "") } : undefined,
          }),
        );
      }
    },
    [step, onUpdate],
  );

  return (
    <ConfigurationPanel
      title={isReadOnly ? `${getStepTitle()} (View Only)` : getStepTitle()}
      subtitle={step.id}
      onSubtitleClick={isReadOnly ? undefined : () => setIsEditingId(true)}
      onClose={onClose}
      onDelete={isReadOnly ? undefined : onDelete}
      bottomOffset={bottomOffset}
      topOffset={topOffset}
      tabBar={
        <ConfigPanelTabBar
          tabs={tabs}
          activeTab={activeTab}
          onTabChange={setActiveTab}
        />
      }
    >
      {/* Inline Node ID Editor (only visible when editing) */}
      {isEditingId && (
        <div className="-mt-2 mb-2 space-y-1.5">
          <div className="flex items-center gap-1.5">
            <input
              ref={idInputRef}
              type="text"
              value={editedId}
              onChange={(e) => {
                setEditedId(e.target.value);
                setIdError(null);
              }}
              onKeyDown={handleIdKeyDown}
              onBlur={() => {
                // Small delay to allow button clicks to register
                setTimeout(() => {
                  if (isEditingId) handleIdCancel();
                }, 150);
              }}
              className={`flex-1 px-2 py-1 text-xs font-mono border rounded-md focus:ring-2 focus:ring-ring focus:border-ring bg-background text-foreground ${
                idError ? "border-destructive" : "border-input"
              }`}
              placeholder="node-id"
            />
            <button
              type="button"
              onClick={handleIdSave}
              className="p-1 text-success hover:text-success/80 hover:bg-success/10 rounded-md transition-colors"
              title="Save"
            >
              <Check className="w-3.5 h-3.5" />
            </button>
            <button
              type="button"
              onClick={handleIdCancel}
              className="p-1 text-muted-foreground hover:text-foreground hover:bg-muted rounded-md transition-colors"
              title="Cancel"
            >
              <X className="w-3.5 h-3.5" />
            </button>
          </div>
          {idError && <p className="text-xs text-destructive">{idError}</p>}
        </div>
      )}

      {/* ============ CONFIG TAB ============ */}
      {activeTab === "config" && (
        <>
          {/* Type-specific fields */}
          {isRunStep(step) && (
            <RunStepConfig
              step={step as RunStep}
              onUpdate={onUpdate}
              isReadOnly={isReadOnly}
            />
          )}
          {isActionStep(step) && (
            <ActionStepConfig
              step={step as ActionStep}
              onUpdate={onUpdate}
              isReadOnly={isReadOnly}
            />
          )}
          {isWorkflowStep(step) && (
            <WorkflowStepConfig
              step={step as WorkflowStep}
              onUpdate={onUpdate}
              currentWorkflowName={currentWorkflowName}
              isReadOnly={isReadOnly}
              onEditInlineBody={onEditInlineWorkflowBody}
            />
          )}
          {isJoinStep(step) && (
            <JoinStepConfig
              step={step as JoinStep}
              onUpdate={updateStep}
              isReadOnly={isReadOnly}
            />
          )}
          {isLoopStep(step) && (
            <LoopStepConfig
              step={step as LoopStep}
              onUpdate={onUpdate}
              onEditLoopBody={onEditLoopBody}
              isReadOnly={isReadOnly}
            />
          )}
          {isRouterStep(step) && (
            <RouterStepConfig
              step={step as RouterStep}
              onUpdate={onUpdate}
              isReadOnly={isReadOnly}
            />
          )}
        </>
      )}

      {/* ============ THREAD TAB ============ */}
      {activeTab === "thread" && hasThreadSupport(step) && (
        <>
          <div className="cpv2-info-banner">
            <Info className="w-3.5 h-3.5" />
            <span>Controls what thread context is passed to the child workflow.</span>
          </div>
          <NodeThreadConfigEditor
            config={stepThread}
            onChange={(thread) => {
              if (isRouterStep(step)) {
                onUpdate(withRouterArgs(step, { thread: thread as any }));
              } else if (isLoopStep(step)) {
                onUpdate(withLoopArgs(step, { thread: thread as any }));
              } else {
                onUpdate(withWorkflowArgs(step, { thread: thread as any }));
              }
            }}
            isInLoop={isInLoop || isLoopStep(step)}
            isReadOnly={isReadOnly}
            variant="flat"
            section="thread"
          />
        </>
      )}

      {/* ============ INJECT TAB ============ */}
      {activeTab === "inject" && hasThreadSupport(step) && (
        <>
          <div className="cpv2-info-banner">
            <Info className="w-3.5 h-3.5" />
            <span>Adds a message to the thread <strong>before</strong> this node executes.</span>
          </div>
          <NodeThreadConfigEditor
            config={stepThread}
            onChange={(thread) => {
              if (isRouterStep(step)) {
                onUpdate(withRouterArgs(step, { thread: thread as any }));
              } else if (isLoopStep(step)) {
                onUpdate(withLoopArgs(step, { thread: thread as any }));
              } else {
                onUpdate(withWorkflowArgs(step, { thread: thread as any }));
              }
            }}
            isInLoop={isInLoop || isLoopStep(step)}
            isReadOnly={isReadOnly}
            variant="flat"
            section="inject"
          />
        </>
      )}

      {/* ============ OUTPUTS TAB ============ */}
      {activeTab === "outputs" && (
        <NodeOutputsPanel
          step={step}
          catalogOutputFields={
            (isActionStep(step) || isRunStep(step) || isRouterStep(step))
              ? catalogNodes.find((n) => n.id === step.type)?.outputFields
              : undefined
          }
          refOutputs={refWorkflowOutputs}
        />
      )}

      {/* ============ ADVANCED TAB ============ */}
      {activeTab === "advanced" && (
        <>
          {/* Node Condition */}
          {!isJoinStep(step) && (
            <div className="space-y-1.5">
              <div className="text-xs uppercase tracking-wider text-muted-foreground/60 mb-1.5">Execution</div>
              <CELExpressionInput
                label="Condition"
                helpTooltip="CEL expression that determines if this node should execute. If false, node is skipped and outputs { skipped: true }. Available: inputs.*, nodes.*, workflow.*"
                value={getConditionExpression(step.condition)}
                onChange={(val) =>
                  updateStep({
                    condition: val ? directCel(val) : undefined,
                  })
                }
                placeholder="inputs.should_run == true"
                celContext="edge_condition"
                disabled={isReadOnly}
                hideCELHint
                showCELIndicator={false}
              />
              {getConditionExpression(step.condition) && (
                <p className="text-xs text-muted-foreground/70">
                  If false, node skipped. Downstream edges can route on skipped output.
                </p>
              )}
            </div>
          )}

          {/* Project settings - only for workflow/loop steps */}
          {hasProjectSupport(step) && (
            <div className="border-t border-border/50 pt-3">
              <button
                type="button"
                onClick={() => setIsProjectExpanded(!isProjectExpanded)}
                className="w-full flex items-center gap-2 py-1.5 text-sm font-medium text-foreground hover:text-foreground/80 transition-colors"
              >
                {isProjectExpanded ? <ChevronDown className="w-4 h-4 text-muted-foreground" /> : <ChevronRight className="w-4 h-4 text-muted-foreground" />}
                <span>Project Override</span>
                {hasProjectConfig && (
                  <span className="ml-auto text-xs px-1.5 py-0.5 rounded bg-primary/10 text-primary">configured</span>
                )}
              </button>
              {isProjectExpanded && (
                <div className="pt-2">
                  <AdvancedProjectSettings
                    project={
                      getStepProject(step)
                        ? { path: getStringValue(getStepProject(step)!.path) }
                        : undefined
                    }
                    onUpdate={updateProject}
                    isReadOnly={isReadOnly}
                    variant="flat"
                  />
                </div>
              )}
            </div>
          )}

          {/* Save Message Configuration */}
          {!(isActionStep(step) && step.type === "save_message") && (
            <div className="border-t border-border/50 pt-3">
              <button
                type="button"
                onClick={() => setIsSaveMessageExpanded(!isSaveMessageExpanded)}
                className="w-full flex items-center gap-2 py-1.5 text-sm font-medium text-foreground hover:text-foreground/80 transition-colors"
              >
                {isSaveMessageExpanded ? <ChevronDown className="w-4 h-4 text-muted-foreground" /> : <ChevronRight className="w-4 h-4 text-muted-foreground" />}
                <span>Save Message</span>
                {hasSaveMessageConfig && (
                  <span className="ml-auto text-xs px-1.5 py-0.5 rounded bg-primary/10 text-primary">configured</span>
                )}
              </button>
              {isSaveMessageExpanded && (
                <div className="pt-2">
                  <SaveMessageConfigEditor
                    config={step.saveMessage}
                    onChange={(saveMessage) => updateStep({ saveMessage })}
                    isReadOnly={isReadOnly}
                    isInLoop={isInLoop}
                    variant="flat"
                    currentNodeType={isActionStep(step) ? step.type : undefined}
                  />
                </div>
              )}
            </div>
          )}
        </>
      )}
    </ConfigurationPanel>
  );
}