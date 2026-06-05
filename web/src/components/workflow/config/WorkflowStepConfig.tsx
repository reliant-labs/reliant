import { useState, useEffect, useMemo } from "react";
import { Pencil, Eye } from "lucide-react";
import type { WorkflowStep } from "../../../types/workflow";
import {
  isInlineWorkflow,
  getStepRef,
  getStepInline,
  getStepInputs,
  getStepPresets,
  withWorkflowArgs,
} from "../../../types/workflow";
import { api } from "../../../api/client";
import { useProjectStore } from "../../../store/projectStore";
import { WorkflowInputGroup } from "../WorkflowInputGroup";
import { useWorkflowInputs } from "../useWorkflowInputs";
import { celString } from "../../../lib/celAdapter";
import { ResponseToolsEditor } from "../ResponseToolsEditor";
import type { ResponseToolDefinition } from "../../../types/workflow";
import { toJson } from "@bufbuild/protobuf";
import { ValueSchema } from "@bufbuild/protobuf/wkt";
import {
  DrillRow,
  FieldInput,
  FieldLabel,
  FieldSelect,
  ModeGroup,
  ModePill,
  Section,
  SectionLabel,
} from "./primitives";

export function WorkflowStepConfig({
  step,
  onUpdate,
  currentWorkflowName,
  isReadOnly = false,
  onEditInlineBody,
}: {
  step: WorkflowStep;
  onUpdate: (step: WorkflowStep) => void;
  currentWorkflowName?: string;
  isReadOnly?: boolean;
  onEditInlineBody?: (step: WorkflowStep) => void;
}) {
  const currentProject = useProjectStore((state) => state.currentProject);
  const projectId = currentProject?.id;

  // Determine the initial mode from data
  const isInline = isInlineWorkflow(step);
  const workflowRefValue = getStepRef(step);
  const dataMode = isInline ? "inline" : "workflow";

  // Use local state for the mode toggle
  const [selectedMode, setSelectedMode] = useState<"workflow" | "inline">(
    dataMode,
  );

  // Sync selected mode when the underlying data changes (e.g., switching between steps)
  useEffect(() => {
    setSelectedMode(dataMode);
  }, [dataMode]);



  // Workflow list state (unique to this component)
  const [existingWorkflows, setExistingWorkflows] = useState<
    Array<{
      name: string;
      filename?: string;
      description?: string;
      step_count?: number;
      source?: "builtin" | "user" | "project";
    }>
  >([]);
  const [loadingWorkflows, setLoadingWorkflows] = useState(true);

  // Use shared hook for workflow inputs logic (only when using ref mode)
  const stepInputs = getStepInputs(step);
  const stepPresets = getStepPresets(step);
  const {
    loadingDef,
    inputGroups,
    presets: availablePresets,
    presetsLoading,
    selectedPresets,
    getPresetsForGroup,
    handlePresetSelect,
    handleInputChange,
  } = useWorkflowInputs({
    projectId,
    workflowRef: selectedMode === "workflow" ? workflowRefValue : "",
    values: stepInputs,
    onValuesChange: (inputs) =>
      onUpdate(withWorkflowArgs(step, { args: inputs as any }) as WorkflowStep),
    enabled: selectedMode === "workflow",
    storedPresets: stepPresets,
  });

  // Merge step args with selected preset values for display
  // Step args take priority over preset values
  const mergedValues = useMemo(() => {
    const merged: Record<string, unknown> = {};
    // Apply preset values first (lower priority)
    for (const [groupName, presetName] of Object.entries(selectedPresets)) {
      if (!presetName) continue;
      const preset = availablePresets.find((p) => p.name === presetName);
      if (!preset) continue;
      for (const [key, value] of Object.entries(preset.params)) {
        // "" and "default" both mean top-level group
        const isTopLevel = groupName === "" || groupName === "default";
        const fullKey = isTopLevel ? key : `${groupName}.${key}`;
        merged[fullKey] = value;
      }
    }
    // Override with step args (higher priority)
    for (const [key, value] of Object.entries(stepInputs)) {
      merged[key] = value;
    }
    return merged;
  }, [stepInputs, selectedPresets, availablePresets]);

  // Detect response tool fields in input groups
  const RESPONSE_TOOL_FIELDS = ["response_tool_name", "response_tool_description", "response_schema"];
  const hasResponseToolFields = inputGroups.some(group =>
    group.inputs.some(({ name }) => RESPONSE_TOOL_FIELDS.includes(name))
  );

  // Build ResponseToolDefinition from separate input values
  const getResponseTool = (): ResponseToolDefinition | null => {
    if (!hasResponseToolFields) return null;
    const getName = (v: unknown) => {
      if (!v) return "";
      if (typeof v === "object" && v !== null && "kind" in (v as any)) {
        try { return toJson(ValueSchema, v as any) as string; } catch { return ""; }
      }
      return String(v);
    };
    const name = getName(mergedValues["response_tool_name"]);
    const description = getName(mergedValues["response_tool_description"]);
    let schema: Record<string, unknown> = {};
    const rawSchema = mergedValues["response_schema"];
    if (rawSchema) {
      if (typeof rawSchema === "object" && rawSchema !== null && "kind" in (rawSchema as any)) {
        try {
          const unwrapped = toJson(ValueSchema, rawSchema as any);
          if (typeof unwrapped === "object" && unwrapped !== null) schema = unwrapped as Record<string, unknown>;
        } catch { /* empty */ }
      } else if (typeof rawSchema === "object") {
        schema = rawSchema as Record<string, unknown>;
      }
    }
    if (!name && !description && Object.keys(schema).length === 0) return null;
    return { name: name || "response", description: description || "", schema };
  };

  const handleResponseToolChange = (tool: ResponseToolDefinition | null) => {
    if (tool) {
      handleInputChange("response_tool_name", tool.name);
      handleInputChange("response_tool_description", tool.description || "");
      handleInputChange("response_schema", tool.schema);
    } else {
      handleInputChange("response_tool_name", "");
      handleInputChange("response_tool_description", "");
      handleInputChange("response_schema", "");
    }
  };

  // Filter response tool fields from input groups for normal rendering
  const filteredInputGroups = hasResponseToolFields
    ? inputGroups.map(group => ({
        ...group,
        inputs: group.inputs.filter(({ name }) => !RESPONSE_TOOL_FIELDS.includes(name)),
      })).filter(group => group.inputs.length > 0 || group.presets)
    : inputGroups;

  // Switch modes
  const switchToInline = () => {
    if (selectedMode === "inline") return;
    setSelectedMode("inline");
    // Always clear ref so the step serializes with inline only (backend rejects
    // having both set, and historically picked ref over inline).
    // Initialize inline workflow if it doesn't already exist.
    const inline = getStepInline(step) ?? {
      name: "",
      entry: [],
      nodes: [],
      edges: [],
      outputs: {},
    };
    onUpdate(
      withWorkflowArgs(step, {
        ref: undefined,
        inline: inline as any,
      }) as WorkflowStep,
    );
  };

  const switchToWorkflow = () => {
    if (selectedMode === "workflow") return;
    setSelectedMode("workflow");
    onUpdate(
      withWorkflowArgs(step, {
        inline: undefined,
      }) as WorkflowStep,
    );
  };

  // Fetch existing workflows list
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
  }, [projectId, step.id]);

  // Separate workflows by source and filter out the current workflow to prevent recursion
  const builtinWorkflows = existingWorkflows.filter(
    (wf) => wf.source === "builtin" && wf.name !== currentWorkflowName,
  );
  const userWorkflows = existingWorkflows.filter(
    (wf) => wf.source !== "builtin" && wf.name !== currentWorkflowName,
  );

  const workflowRef = workflowRefValue;
  const isBuiltin = workflowRef.startsWith("builtin://");
  const isUserWorkflow = workflowRef.startsWith("workflow://");
  const isCustomPath = !isBuiltin && !isUserWorkflow && workflowRef !== "";

  const getSelectionType = () => {
    if (isBuiltin) return workflowRef;
    if (isUserWorkflow) return workflowRef;
    if (isCustomPath) return "custom";
    return "";
  };

  const inlineWorkflow = getStepInline(step);

  return (
    <>
      <Section>
        <SectionLabel>Workflow Definition</SectionLabel>
        <ModeGroup>
          <ModePill active={selectedMode === "workflow"} onClick={() => !isReadOnly && switchToWorkflow()}>
            Reference
          </ModePill>
          <ModePill active={selectedMode === "inline"} onClick={() => !isReadOnly && switchToInline()}>
            Inline
          </ModePill>
        </ModeGroup>
      </Section>

      {/* Workflow Reference Mode */}
      {selectedMode === "workflow" && (
        <>
          <Section>
            <SectionLabel>Workflow</SectionLabel>
            <FieldLabel>Reference</FieldLabel>
            {loadingWorkflows ? (
              <div className="cpv2-field-input text-muted-foreground">
                Loading workflows...
              </div>
            ) : (
              <FieldSelect
                value={getSelectionType()}
                onChange={(e) => {
                  const nextRef = e.target.value === "custom" ? "" : e.target.value;
                  // Clear both args AND presets when ref changes — stale presets
                  // pointing at the old workflow leave the panel showing a chip
                  // that has no matching preset under the new ref.
                  onUpdate(
                    withWorkflowArgs(step, {
                      ref: celString(nextRef),
                      args: {},
                      presets: {},
                    }) as WorkflowStep,
                  );
                }}
                disabled={isReadOnly}
              >
                <option value="">Select a workflow...</option>
                {builtinWorkflows.length > 0 && (
                  <optgroup label="Built-in Workflows">
                    {builtinWorkflows.map((wf) => (
                      <option key={wf.name} value={wf.name}>
                        {wf.name.replace(/^builtin:\/\//, '')}
                      </option>
                    ))}
                  </optgroup>
                )}

                {userWorkflows.length > 0 && (
                  <optgroup label="Your Workflows">
                    {userWorkflows.map((wf) => (
                      <option key={wf.name} value={wf.name}>
                        {wf.name.replace(/^workflow:\/\//, '')}
                      </option>
                    ))}
                  </optgroup>
                )}

                <optgroup label="Other">
                  <option value="custom">Custom path...</option>
                </optgroup>
              </FieldSelect>
            )}
          </Section>

          {/* Custom path input */}
          {(getSelectionType() === "custom" || isCustomPath) && (
            <Section>
              <FieldLabel>Custom path</FieldLabel>
              <FieldInput
                type="text"
                value={workflowRefValue}
                onChange={(e) =>
                  onUpdate(
                    withWorkflowArgs(step, {
                      ref: celString(e.target.value),
                    }) as WorkflowStep,
                  )
                }
                placeholder="Workflow reference or {{expression}}"
                disabled={isReadOnly}
              />
            </Section>
          )}

          {/* Schema-aware inputs */}
          {loadingDef ? (
            <Section>
              <p className="cpv2-field-hint !mt-0 text-center">Loading workflow inputs...</p>
            </Section>
          ) : (filteredInputGroups.length > 0 || hasResponseToolFields) ? (
            <>
              {filteredInputGroups.map((group) => (
                <WorkflowInputGroup
                  key={`${step.id}-${group.name || "_default"}`}
                  group={group}
                  isTopLevel={!group.name}
                  values={mergedValues}
                  onChange={handleInputChange}
                  presets={getPresetsForGroup(group.presets?.tag)}
                  selectedPreset={selectedPresets[group.name] ?? null}
                  onPresetSelect={(preset) =>
                    handlePresetSelect(group.name, preset)
                  }
                  presetsLoading={presetsLoading}
                  projectId={projectId}
                  defaultExpanded={true}
                  disabled={isReadOnly}
                />
              ))}
              {hasResponseToolFields && (
                <Section>
                  <SectionLabel>Response Tool</SectionLabel>
                  <ResponseToolsEditor
                    tool={getResponseTool()}
                    onChange={handleResponseToolChange}
                    isReadOnly={isReadOnly}
                  />
                </Section>
              )}
            </>
          ) : null}
        </>
      )}

      {/* Inline Mode */}
      {selectedMode === "inline" && (
        <Section>
          <SectionLabel>Inline Workflow</SectionLabel>
          <DrillRow
            label={isReadOnly ? "View sub-workflow" : "Edit sub-workflow"}
            sublabel={`${inlineWorkflow?.nodes?.length || 0} nodes, ${inlineWorkflow?.edges?.length || 0} edges`}
            icon={isReadOnly ? <Eye className="w-3 h-3" /> : <Pencil className="w-3 h-3" />}
            onClick={() => onEditInlineBody?.(step)}
          />
        </Section>
      )}

    </>
  );
}