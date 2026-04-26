import { useState, useEffect } from "react";
import { X, Plus, Trash2 } from "lucide-react";
import type { Step, RouterStep } from "../../../types/workflow";
import { withRouterArgs } from "../../../types/workflow";
import { CELInput, CELExpressionInput } from "../CELInput";
import { ModelDropdown, extractModelId } from "../ModelDropdown";
import { MultiSelectDropdown } from "../../ui/MultiSelectDropdown";
import { celString, normalizeCelString } from "../../../lib/celAdapter";
import { api } from "../../../api/client";
import { useProjectStore } from "../../../store/projectStore";
import { usePresetsForWorkflow } from "../../../store/globalDataStore";
import { canonicalizeBuiltinWorkflowRef } from "../workflowRef";
import type {
  RouterArgs,
  RouterWorkflowCandidate,
  NodeRouterCandidate,
} from "../../../gen/reliant/v1/workflow_v2_pb";

type RouterMode = "workflow" | "node";

interface RouterStepConfigProps {
  step: RouterStep;
  onUpdate: (step: Step) => void;
  isReadOnly?: boolean;
}

/** Read router args from step, with safe fallback. */
function getRouterArgs(step: RouterStep): Partial<RouterArgs> {
  if (step.args?.case === "router") {
    return step.args.value as Partial<RouterArgs>;
  }
  return { workflows: [] };
}

/** Detect routing mode from args: nodes populated → node mode, else workflow mode */
function detectMode(args: Partial<RouterArgs>): RouterMode {
  const nodes = args.nodes as Partial<NodeRouterCandidate>[] | undefined;
  if (nodes && nodes.length > 0) return "node";
  return "workflow";
}

/** Strip protocol prefix from a workflow ref for display */
function displayRef(ref: string): string {
  return ref.replace(/^(builtin|project|workflow):\/\//, "");
}

// ── Sub-component: one workflow candidate card with its own preset loading ──

interface RouterCandidateCardProps {
  candidate: Partial<RouterWorkflowCandidate>;
  index: number;
  workflows: {
    builtin: Array<{
      name: string;
      filename?: string;
      description?: string;
      source?: string;
    }>;
    user: Array<{
      name: string;
      filename?: string;
      description?: string;
      source?: string;
    }>;
  };
  loadingWorkflows: boolean;
  onUpdate: (index: number, updates: Partial<RouterWorkflowCandidate>) => void;
  onDelete: (index: number) => void;
  isReadOnly: boolean;
}

function RouterCandidateCard({
  candidate,
  index,
  workflows,
  loadingWorkflows,
  onUpdate,
  onDelete,
  isReadOnly,
}: RouterCandidateCardProps) {
  const builtinWorkflowRefs = workflows.builtin.map((workflow) => workflow.name);
  const { presets, loading: presetsLoading } = usePresetsForWorkflow(
    candidate.ref || "",
  );

  const presetOptions = presets.map((p) => ({
    value: p.name,
    label: p.name,
    description: p.description,
  }));

  const ref = candidate.ref ?? "";
  const isBuiltin = ref.startsWith("builtin://");
  const isUserWorkflow = ref.startsWith("workflow://");
  const isCustomPath = !isBuiltin && !isUserWorkflow && ref !== "";

  const getSelectionType = () => {
    if (isBuiltin) return ref;
    if (isUserWorkflow) return ref;
    if (isCustomPath) return "custom";
    return "";
  };

  return (
    <div className="relative border border-input rounded-md p-3 space-y-2 bg-background">
      {/* Header + Delete */}
      <div className="flex items-center justify-between">
        <span className="text-xs font-medium text-muted-foreground">
          Candidate {index + 1}
        </span>
        {!isReadOnly && (
          <button
            type="button"
            onClick={() => onDelete(index)}
            className="p-0.5 text-muted-foreground hover:text-destructive transition-colors"
            title="Remove candidate"
          >
            <X className="w-3.5 h-3.5" />
          </button>
        )}
      </div>

      {/* Workflow Ref dropdown */}
      <div>
        <label className="block text-xs text-muted-foreground mb-0.5">
          Workflow
        </label>
        {loadingWorkflows ? (
          <div className="w-full px-2.5 py-1.5 text-sm border border-input rounded-md bg-background text-muted-foreground">
            Loading workflows...
          </div>
        ) : (
          <select
            value={getSelectionType()}
            onChange={(e) => {
              if (e.target.value === "custom") {
                onUpdate(index, { ref: "", presets: [] });
              } else {
                onUpdate(index, {
                  ref: canonicalizeBuiltinWorkflowRef(
                    e.target.value,
                    builtinWorkflowRefs,
                  ),
                  presets: [],
                });
              }
            }}
            className="w-full px-2.5 py-1.5 text-sm border border-input rounded-md focus:ring-2 focus:ring-ring focus:border-ring bg-background text-foreground disabled:opacity-60 disabled:cursor-not-allowed"
            disabled={isReadOnly}
          >
            <option value="">Select a workflow...</option>
            {workflows.builtin.length > 0 && (
              <optgroup label="Built-in Workflows">
                {workflows.builtin.map((wf) => (
                  <option key={wf.name} value={wf.name}>
                    {displayRef(wf.name)}
                  </option>
                ))}
              </optgroup>
            )}
            {workflows.user.length > 0 && (
              <optgroup label="Your Workflows">
                {workflows.user.map((wf) => (
                  <option key={wf.name} value={wf.name}>
                    {displayRef(wf.name)}
                  </option>
                ))}
              </optgroup>
            )}
            <optgroup label="Other">
              <option value="custom">Custom path...</option>
            </optgroup>
          </select>
        )}

        {/* Custom path input */}
        {(getSelectionType() === "custom" || isCustomPath) && (
          <input
            type="text"
            value={ref}
            onChange={(e) =>
              onUpdate(index, {
                ref: canonicalizeBuiltinWorkflowRef(
                  e.target.value,
                  builtinWorkflowRefs,
                ),
              })
            }
            className="w-full mt-1 px-2.5 py-1.5 text-sm border border-input rounded-md bg-background text-foreground focus:ring-2 focus:ring-ring focus:border-ring disabled:opacity-60 disabled:cursor-not-allowed"
            placeholder="Workflow reference or {{expression}}"
            disabled={isReadOnly}
          />
        )}
      </div>

      {/* Presets multi-select */}
      <div>
        <label className="block text-xs text-muted-foreground mb-0.5">
          Presets
        </label>
        <MultiSelectDropdown
          options={presetOptions}
          value={candidate.presets ?? []}
          onChange={(selected) => onUpdate(index, { presets: selected })}
          placeholder={
            presetsLoading
              ? "Loading presets..."
              : "Select presets..."
          }
          emptyMessage="No presets available for this workflow"
          disabled={isReadOnly || !candidate.ref || presetsLoading}
        />
      </div>

      {/* Description */}
      <div>
        <label className="block text-xs text-muted-foreground mb-0.5">
          Description
        </label>
        <textarea
          value={candidate.description ?? ""}
          onChange={(e) => onUpdate(index, { description: e.target.value })}
          className="w-full px-2.5 py-1.5 text-sm border border-input rounded-md bg-background text-foreground focus:ring-2 focus:ring-ring focus:border-ring disabled:opacity-60 disabled:cursor-not-allowed"
          rows={2}
          placeholder="Optional routing context override"
          disabled={isReadOnly}
        />
      </div>
    </div>
  );
}

// ── Sub-component: one node candidate card (simpler than workflow) ──

interface NodeRouterCandidateCardProps {
  candidate: Partial<NodeRouterCandidate>;
  index: number;
  onUpdate: (index: number, updates: Partial<NodeRouterCandidate>) => void;
  onDelete: (index: number) => void;
  isReadOnly: boolean;
}

function NodeRouterCandidateCard({
  candidate,
  index,
  onUpdate,
  onDelete,
  isReadOnly,
}: NodeRouterCandidateCardProps) {
  return (
    <div className="relative border border-input rounded-md p-3 space-y-2 bg-background">
      {/* Header + Delete */}
      <div className="flex items-center justify-between">
        <span className="text-xs font-medium text-muted-foreground">
          Candidate {index + 1}
        </span>
        {!isReadOnly && (
          <button
            type="button"
            onClick={() => onDelete(index)}
            className="p-0.5 text-muted-foreground hover:text-destructive transition-colors"
            title="Remove candidate"
          >
            <X className="w-3.5 h-3.5" />
          </button>
        )}
      </div>

      {/* Node ID */}
      <div>
        <label className="block text-xs text-muted-foreground mb-0.5">
          Node ID
        </label>
        <input
          type="text"
          value={candidate.id ?? ""}
          onChange={(e) => onUpdate(index, { id: e.target.value })}
          className="w-full px-2.5 py-1.5 text-sm border border-input rounded-md bg-background text-foreground focus:ring-2 focus:ring-ring/40 focus:border-ring transition-colors disabled:opacity-60 disabled:cursor-not-allowed"
          placeholder="target_node_id"
          disabled={isReadOnly}
        />
      </div>

      {/* Description */}
      <div>
        <label className="block text-xs text-muted-foreground mb-0.5">
          Description
        </label>
        <textarea
          value={candidate.description ?? ""}
          onChange={(e) => onUpdate(index, { description: e.target.value })}
          className="w-full px-2.5 py-1.5 text-sm border border-input rounded-md bg-background text-foreground focus:ring-2 focus:ring-ring/40 focus:border-ring transition-colors disabled:opacity-60 disabled:cursor-not-allowed"
          rows={2}
          placeholder="When should the router select this node?"
          disabled={isReadOnly}
        />
      </div>
    </div>
  );
}

// ── Mode toggle ──

function ModeToggle({
  mode,
  onChange,
  disabled,
}: {
  mode: RouterMode;
  onChange: (mode: RouterMode) => void;
  disabled: boolean;
}) {
  return (
    <div className="inline-flex rounded-md border border-input bg-muted p-0.5">
      <button
        type="button"
        onClick={() => onChange("workflow")}
        disabled={disabled}
        className={`px-3 py-1 text-xs font-medium rounded transition-colors ${
          mode === "workflow"
            ? "bg-background text-foreground shadow-sm"
            : "text-muted-foreground hover:text-foreground"
        } disabled:opacity-60 disabled:cursor-not-allowed`}
      >
        Workflow Routing
      </button>
      <button
        type="button"
        onClick={() => onChange("node")}
        disabled={disabled}
        className={`px-3 py-1 text-xs font-medium rounded transition-colors ${
          mode === "node"
            ? "bg-background text-foreground shadow-sm"
            : "text-muted-foreground hover:text-foreground"
        } disabled:opacity-60 disabled:cursor-not-allowed`}
      >
        Node Routing
      </button>
    </div>
  );
}

// ── Main component ──

export function RouterStepConfig({
  step,
  onUpdate,
  isReadOnly = false,
}: RouterStepConfigProps) {
  const currentProject = useProjectStore((state) => state.currentProject);
  const projectId = currentProject?.id;

  const args = getRouterArgs(step);
  const mode = detectMode(args);
  const workflows = (args.workflows ?? []) as Partial<RouterWorkflowCandidate>[];
  const nodes = (args.nodes ?? []) as Partial<NodeRouterCandidate>[];
  const systemPromptValue = normalizeCelString(args.systemPrompt);
  const fallback = args.fallback ?? "";
  const modelId = args.model
    ? extractModelId(
        args.model.value?.case === "literal"
          ? args.model.value.value
          : undefined,
      )
    : "";

  // ── Mode switching ──

  const handleModeChange = (newMode: RouterMode) => {
    if (newMode === mode) return;
    if (newMode === "node") {
      // Switch to node routing: clear workflows, init nodes
      onUpdate(withRouterArgs(step, { workflows: [], nodes: nodes.length > 0 ? nodes : [] }));
    } else {
      // Switch to workflow routing: clear nodes, init workflows
      onUpdate(withRouterArgs(step, { nodes: [], workflows: workflows.length > 0 ? workflows : [] }));
    }
  };

  // ── Workflow list loading (only needed for workflow mode) ──

  const [existingWorkflows, setExistingWorkflows] = useState<
    Array<{
      name: string;
      filename?: string;
      description?: string;
      source?: string;
    }>
  >([]);
  const [loadingWorkflows, setLoadingWorkflows] = useState(true);

  useEffect(() => {
    if (!projectId) return;

    const fetchWorkflows = async () => {
      setLoadingWorkflows(true);
      try {
        const response = await api.workflows.list(projectId);
        setExistingWorkflows(response.workflows || []);
      } catch (error) {
        console.error("Failed to fetch workflows:", error);
        setExistingWorkflows([]);
      } finally {
        setLoadingWorkflows(false);
      }
    };
    fetchWorkflows();
  }, [projectId]);

  const builtinWorkflows = existingWorkflows.filter(
    (wf) => wf.source === "builtin",
  );
  const userWorkflows = existingWorkflows.filter(
    (wf) => wf.source !== "builtin",
  );

  // ── Workflow candidate helpers ──

  const updateCandidate = (
    index: number,
    updates: Partial<RouterWorkflowCandidate>,
  ) => {
    const next = workflows.map((c, i) =>
      i === index ? { ...c, ...updates } : c,
    );
    onUpdate(withRouterArgs(step, { workflows: next }));
  };

  const addCandidate = () => {
    const next = [...workflows, { ref: "", presets: [], description: "" }];
    onUpdate(withRouterArgs(step, { workflows: next }));
  };

  const deleteCandidate = (index: number) => {
    const next = workflows.filter((_, i) => i !== index);
    onUpdate(withRouterArgs(step, { workflows: next }));
  };

  // ── Node candidate helpers ──

  const updateNodeCandidate = (
    index: number,
    updates: Partial<NodeRouterCandidate>,
  ) => {
    const next = nodes.map((c, i) =>
      i === index ? { ...c, ...updates } : c,
    );
    onUpdate(withRouterArgs(step, { nodes: next }));
  };

  const addNodeCandidate = () => {
    const next = [...nodes, { id: "", description: "" }];
    onUpdate(withRouterArgs(step, { nodes: next }));
  };

  const deleteNodeCandidate = (index: number) => {
    const next = nodes.filter((_, i) => i !== index);
    onUpdate(withRouterArgs(step, { nodes: next }));
  };

  return (
    <>
      {/* Mode Toggle */}
      <div>
        <label className="block text-xs uppercase tracking-wider text-muted-foreground font-medium mb-1">
          Routing Mode
        </label>
        <ModeToggle mode={mode} onChange={handleModeChange} disabled={isReadOnly} />
      </div>

      {/* Candidates */}
      {mode === "workflow" ? (
        <div>
          <label className="block text-xs uppercase tracking-wider text-muted-foreground font-medium mb-2">
            Candidate Workflows
          </label>
          <div className="space-y-2">
            {workflows.map((candidate, index) => (
              <RouterCandidateCard
                key={index}
                candidate={candidate}
                index={index}
                workflows={{ builtin: builtinWorkflows, user: userWorkflows }}
                loadingWorkflows={loadingWorkflows}
                onUpdate={updateCandidate}
                onDelete={deleteCandidate}
                isReadOnly={isReadOnly}
              />
            ))}

            {!isReadOnly && (
              <button
                type="button"
                onClick={addCandidate}
                className="flex items-center gap-1.5 text-sm text-primary hover:text-primary/80 transition-colors py-1"
              >
                <Plus className="w-3.5 h-3.5" />
                Add Candidate
              </button>
            )}
          </div>
        </div>
      ) : (
        <div>
          <label className="block text-xs uppercase tracking-wider text-muted-foreground font-medium mb-2">
            Candidate Nodes
          </label>
          <div className="space-y-2">
            {nodes.map((candidate, index) => (
              <NodeRouterCandidateCard
                key={index}
                candidate={candidate}
                index={index}
                onUpdate={updateNodeCandidate}
                onDelete={deleteNodeCandidate}
                isReadOnly={isReadOnly}
              />
            ))}

            {!isReadOnly && (
              <button
                type="button"
                onClick={addNodeCandidate}
                className="flex items-center gap-1.5 text-sm text-primary hover:text-primary/80 transition-colors py-1"
              >
                <Plus className="w-3.5 h-3.5" />
                Add Candidate
              </button>
            )}
          </div>
        </div>
      )}

      {/* System Prompt (optional) — shared */}
      <div>
        <CELInput
          label="System Prompt"
          value={systemPromptValue}
          onChange={(val) =>
            onUpdate(
              withRouterArgs(step, { systemPrompt: celString(val) }),
            )
          }
          multiline
          rows={2}
          placeholder="Optional system prompt override"
          disabled={isReadOnly}
        />
      </div>

      {/* Model — shared */}
      <div>
        <label className="block text-xs uppercase tracking-wider text-muted-foreground font-medium mb-1">
          Model
        </label>
        <ModelDropdown
          value={modelId ? { id: modelId } : undefined}
          onChange={(val) =>
            onUpdate(
              withRouterArgs(step, {
                model: { value: { case: "literal" as const, value: val } },
              }),
            )
          }
          disabled={isReadOnly}
          placeholder="Default model"
        />
      </div>

      {/* Fallback — label changes per mode */}
      <div>
        <label className="block text-xs uppercase tracking-wider text-muted-foreground font-medium mb-1">
          {mode === "node" ? "Fallback Node" : "Fallback Preset"}
        </label>
        <input
          type="text"
          value={fallback}
          onChange={(e) =>
            onUpdate(withRouterArgs(step, { fallback: e.target.value }))
          }
          className="w-full px-2.5 py-1.5 text-sm border border-input rounded-md bg-background text-foreground focus:ring-2 focus:ring-ring focus:border-ring disabled:opacity-60 disabled:cursor-not-allowed"
          placeholder={mode === "node" ? "Node ID to use if routing fails" : "Preset name to use if routing fails"}
          disabled={isReadOnly}
        />
      </div>

      {/* Declared Outputs — only for workflow routing */}
      {mode === "workflow" && (
        <RouterOutputsEditor
          outputs={args.outputs as Record<string, string> ?? {}}
          onUpdate={(outputs) => onUpdate(withRouterArgs(step, { outputs }))}
          isReadOnly={isReadOnly}
        />
      )}
    </>
  );
}

// ── Router Outputs Editor ──

function RouterOutputsEditor({
  outputs,
  onUpdate,
  isReadOnly,
}: {
  outputs: Record<string, string>;
  onUpdate: (outputs: Record<string, string>) => void;
  isReadOnly: boolean;
}) {
  const [newKey, setNewKey] = useState("");

  const addOutput = () => {
    const key = newKey.trim();
    if (!key || outputs[key] !== undefined) return;
    onUpdate({ ...outputs, [key]: "" });
    setNewKey("");
  };

  const updateOutput = (key: string, value: string) => {
    onUpdate({ ...outputs, [key]: value });
  };

  const removeOutput = (key: string) => {
    const next = { ...outputs };
    delete next[key];
    onUpdate(next);
  };

  const entries = Object.entries(outputs);

  return (
    <div>
      <label className="block text-xs uppercase tracking-wider text-muted-foreground font-medium mb-1">
        Declared Outputs
      </label>
      <p className="text-xs text-muted-foreground mb-2">
        Map output names to CEL expressions evaluated in the router context.
        Downstream nodes access these as{" "}
        <code className="bg-muted px-1 rounded">nodes.&lt;router_id&gt;.&lt;name&gt;</code>.
      </p>

      {entries.length > 0 && (
        <div className="space-y-2 mb-2">
          {entries.map(([key, value]) => (
            <div key={key} className="space-y-1">
              <div className="flex items-center justify-between">
                <label className="text-xs font-medium text-foreground">
                  {key}
                </label>
                {!isReadOnly && (
                  <button
                    type="button"
                    onClick={() => removeOutput(key)}
                    className="p-0.5 text-muted-foreground hover:text-destructive transition-colors"
                  >
                    <Trash2 className="w-3.5 h-3.5" />
                  </button>
                )}
              </div>
              <CELExpressionInput
                value={value}
                onChange={(val) => updateOutput(key, val)}
                placeholder="outputs.result"
                hideCELHint
                disabled={isReadOnly}
              />
            </div>
          ))}
        </div>
      )}

      {!isReadOnly && (
        <div className="flex gap-2">
          <input
            type="text"
            value={newKey}
            onChange={(e) => setNewKey(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && addOutput()}
            placeholder="output_name"
            className="flex-1 px-2.5 py-1.5 text-sm border border-input rounded-md bg-background text-foreground focus:ring-2 focus:ring-ring/40 focus:border-ring transition-colors"
          />
          <button
            type="button"
            onClick={addOutput}
            disabled={!newKey.trim() || outputs[newKey] !== undefined}
            className="flex items-center gap-1 px-2.5 py-1.5 text-sm font-medium rounded-md bg-primary text-primary-foreground hover:bg-primary/90 disabled:opacity-50 disabled:cursor-not-allowed"
          >
            <Plus className="w-3.5 h-3.5" />
            Add
          </button>
        </div>
      )}

      <div className="text-xs text-muted-foreground mt-2 space-y-0.5">
        <p className="font-medium">Available in expressions:</p>
        <ul className="list-disc list-inside">
          <li><code className="text-xs">selected_workflow</code> &mdash; chosen workflow ref</li>
          <li><code className="text-xs">selected_preset</code> &mdash; chosen preset</li>
          <li><code className="text-xs">prompt</code> &mdash; prompt sent to child workflow</li>
          <li><code className="text-xs">reasoning</code> &mdash; router reasoning</li>
          <li><code className="text-xs">outputs.*</code> &mdash; child workflow outputs</li>
        </ul>
      </div>
    </div>
  );
}