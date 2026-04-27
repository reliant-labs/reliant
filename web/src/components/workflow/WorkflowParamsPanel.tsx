// Copyright (c) 2025 Reliant Labs

import { useState, useMemo, useCallback, useEffect } from "react";
import { ChevronDown, ChevronRight, Settings2, Save, Loader2, X, Info } from "lucide-react";
import { cn } from "../../lib/utils";
import { Tooltip } from "../ui/Tooltip";
import { usePresetsForWorkflow, useGlobalDataStore, type Preset } from "../../store/globalDataStore";
import { usePreferencesStore } from "../../store/preferencesStore";
import { presetGrpc } from "../../api/preset-grpc";
import { PresetPicker } from "./PresetPicker";
import { ProtoFieldRenderer } from "./ProtoFieldRenderer";
import { inputDefToSchema } from "../../lib/nodeFieldAdapter";
import { logger } from "../../lib/logger";
import { type InputDef, getInputUI, getInputDefault, getInputTag, getInputMulti } from "../../lib/inputHelpers";

// Workflow inputs from the backend
export interface WorkflowInputs {
  [key: string]: InputDef;
}

interface WorkflowParamsPanelProps {
  // Project ID (for saving presets)
  projectId?: string;

  // Workflow name (for fetching compatible presets)
  workflowName: string;

  // Workflow inputs schema
  inputs: WorkflowInputs;

  // Current parameter values
  values: Record<string, unknown>;

  // Called when any parameter value changes
  onChange: (values: Record<string, unknown>) => void;

  // Whether the panel is disabled (e.g., during streaming)
  disabled?: boolean;

  // Additional class names
  className?: string;

  // Whether to show the panel collapsed by default
  defaultCollapsed?: boolean;

  // Whether to hide the header (for external collapse control)
  hideHeader?: boolean;

  // Workflow tag (for workflow-level params)
  workflowTag?: string;

  // Group tags mapping (groupName -> tag)
  groupTags?: Record<string, string>;

  // Selected presets per group ("" for workflow level, groupName for groups)
  selectedPresets?: Record<string, string | null>;

  // All presets (for looking up selected preset objects)
  presets?: Preset[];

  // When true, child dropdowns use chat-input layering and opaque chat surfaces
  isChatInputContext?: boolean;
}

interface ParamGroup {
  group: string; // Empty for default group
  label: string;
  params: [string, InputDef][];
  tag?: string; // Tag for this group (if any)
}

export function WorkflowParamsPanel({
  projectId,
  workflowName,
  inputs,
  values,
  onChange,
  disabled = false,
  className = "",
  defaultCollapsed = false,
  hideHeader = false,
  workflowTag,
  groupTags = {},
  selectedPresets = {},
  presets = [],
  isChatInputContext: _isChatInputContext = false,
}: WorkflowParamsPanelProps) {
  const [isCollapsed, setIsCollapsed] = useState(defaultCollapsed);
  const [expandedGroups, setExpandedGroups] = useState<Set<string>>(new Set([""])); // Default group expanded
  const [selectedPreset, setSelectedPreset] = useState<string | null>(null);

  // Save state per group
  const [savingGroup, setSavingGroup] = useState<string | null>(null);
  const [saveError, setSaveError] = useState<string | null>(null);
  const [saveMode, setSaveMode] = useState<{ group: string; mode: "new" | "update" } | null>(null);
  const [newPresetName, setNewPresetName] = useState("");

  // Save preset state (for global preset saving)
  const [isSaveMode, setIsSaveMode] = useState(false);
  const [presetName, setPresetName] = useState("");
  const [presetDescription, setPresetDescription] = useState("");
  const [isSaving, setIsSaving] = useState(false);

  // Get refetch function from global store
  const refetchPresets = useGlobalDataStore((state) => state.refetchPresets);

  // Fetch presets for this workflow
  const { presets: fetchedPresets, loading: presetsLoading } = usePresetsForWorkflow(workflowName);

  // Get hidden preset check
  const isPresetHidden = usePreferencesStore((state) => state.isPresetHidden);

  // Combine prop presets with fetched presets (prop takes precedence if provided)
  // Filter out hidden presets
  const filteredPresets = (presets.length > 0 ? presets : fetchedPresets)
    .filter(p => !isPresetHidden(p.name));

  // Group inputs by group field
  const { groups, configurableCount } = useMemo(() => {
    const entries = Object.entries(inputs);

    // Filter out hidden inputs and primary inputs (message, attachments)

    const configParams = entries.filter(([_name, schema]) =>
      getInputUI(schema) !== "hidden" &&
      schema.type !== "message" &&
      schema.type !== "attachments"
    );

    // Group params by key pattern: "GroupName.paramName" = grouped, plain name = top-level
    const groupMap = new Map<string, [string, InputDef][]>();

    for (const [name, schema] of configParams) {
      // Derive group from key pattern
      const dotIndex = name.indexOf(".");
      const group = dotIndex > 0 ? name.substring(0, dotIndex) : "";
      if (!groupMap.has(group)) {
        groupMap.set(group, []);
      }
      groupMap.get(group)!.push([name, schema]);
    }

    // Convert to array, default group first, then alphabetical
    const sortedGroups: ParamGroup[] = Array.from(groupMap.entries())
      .sort((a, b) => {
        // Default group (empty) comes first
        if (a[0] === "") return -1;
        if (b[0] === "") return 1;
        return a[0].localeCompare(b[0]);
      })
      .map(([group, params]) => ({
        group,
        label: group || "Parameters",
        params,
        // Assign tag: workflow tag for default group, group tag for named groups
        tag: group === "" ? workflowTag : groupTags[group],
      }));

    return {
      groups: sortedGroups,
      configurableCount: configParams.length,
    };
  }, [inputs, workflowTag, groupTags]);

  // Auto-expand all groups when inputs change (e.g., workflow changes)
  // This ensures params are visible when initially loading or switching workflows
  useEffect(() => {
    if (groups.length > 0) {
      // Expand all groups by default
      const allGroupNames = new Set(groups.map(g => g.group));
      setExpandedGroups(allGroupNames);
    }
  }, [groups]);

  // Get selected preset object for a group
  const getSelectedPreset = useCallback((groupName: string): Preset | null => {
    const presetName = selectedPresets[groupName];
    if (!presetName) return null;
    return presets.find(p => p.name === presetName) || null;
  }, [selectedPresets, presets]);

  // Check if a group is dirty (params differ from selected preset or defaults)
  const isGroupDirty = useCallback((group: ParamGroup): boolean => {
    const selectedPreset = getSelectedPreset(group.group);

    for (const [paramName, schema] of group.params) {
      const currentValue = values[paramName];

      if (selectedPreset) {
        // Compare to preset values
        // For group params, the preset stores them without the group prefix
        const presetParamName = group.group ? paramName.split(".")[1] : paramName;
        const presetValue = selectedPreset.params[presetParamName];

        // Normalize for comparison (handle string/number conversions)
        const normalizedCurrent = currentValue?.toString() ?? getInputDefault(schema)?.toString() ?? "";
        const normalizedPreset = presetValue?.toString() ?? getInputDefault(schema)?.toString() ?? "";

        if (normalizedCurrent !== normalizedPreset) {
          return true;
        }
      } else {
        // No preset selected - compare to defaults
        const defaultValue = getInputDefault(schema);
        if (currentValue !== undefined && currentValue !== defaultValue) {
          // Check for string/number equivalence
          if (currentValue?.toString() !== defaultValue?.toString()) {
            return true;
          }
        }
      }
    }

    return false;
  }, [values, getSelectedPreset]);

  // Toggle group expansion
  const toggleGroup = (group: string) => {
    setExpandedGroups(prev => {
      const next = new Set(prev);
      if (next.has(group)) {
        next.delete(group);
      } else {
        next.add(group);
      }
      return next;
    });
  };

  // Handle individual param change
  const handleParamChange = (name: string, value: unknown) => {
    onChange({ ...values, [name]: value });
  };

  // Get params for a specific group (for saving)
  const getGroupParams = useCallback((group: ParamGroup): Record<string, unknown> => {
    const params: Record<string, unknown> = {};
    for (const [name, schema] of group.params) {
      const value = values[name] ?? getInputDefault(schema);
      if (value !== undefined) {
        // For group params, strip the group prefix for the preset
        const paramName = group.group ? name.split(".")[1] : name;
        params[paramName] = value;
      }
    }
    return params;
  }, [values]);

  // Handle save as new preset
  const handleSaveAsNew = useCallback(async (group: ParamGroup) => {
    if (!projectId || !newPresetName.trim() || !group.tag) return;

    setSavingGroup(group.group);
    setSaveError(null);

    try {
      const params = getGroupParams(group);

      const result = await presetGrpc.createPreset(projectId, {
        name: newPresetName.trim(),
        description: "",
        params,
        tag: group.tag,
      });

      if (!result.success) {
        setSaveError(result.error || "Failed to save preset");
        return;
      }

      logger.info("[WorkflowParamsPanel] Saved new preset", {
        name: newPresetName,
        tag: group.tag,
        group: group.group
      });

      // Refresh presets
      await refetchPresets(projectId);

      // Auto-set as default for this group
      try {
        await presetGrpc.setDefaultPreset(projectId, workflowName, group.group, newPresetName.trim());
        logger.info("[WorkflowParamsPanel] Set new preset as default", {
          name: newPresetName,
          workflowName,
          group: group.group
        });
      } catch (error) {
        logger.warn("[WorkflowParamsPanel] Failed to set preset as default", { error });
        // Don't fail the save operation if setting default fails
      }

      // Reset save mode
      setSaveMode(null);
      setNewPresetName("");
    } catch (error) {
      logger.error("[WorkflowParamsPanel] Failed to save preset", { error });
      setSaveError(error instanceof Error ? error.message : "Failed to save preset");
    } finally {
      setSavingGroup(null);
    }
  }, [projectId, newPresetName, getGroupParams, refetchPresets, workflowName]);

  // Handle update existing preset
  const handleUpdatePreset = useCallback(async (group: ParamGroup) => {
    if (!projectId || !group.tag) return;

    const selectedPreset = getSelectedPreset(group.group);
    if (!selectedPreset || selectedPreset.source !== "user") return;

    setSavingGroup(group.group);
    setSaveError(null);

    try {
      const params = getGroupParams(group);

      const result = await presetGrpc.updatePreset(projectId, selectedPreset.name, {
        newParams: params,
      });

      if (!result.success) {
        setSaveError(result.error || "Failed to update preset");
        return;
      }

      logger.info("[WorkflowParamsPanel] Updated preset", {
        name: selectedPreset.name,
        group: group.group
      });

      // Refresh presets
      await refetchPresets(projectId);

      // Reset save mode
      setSaveMode(null);
    } catch (error) {
      logger.error("[WorkflowParamsPanel] Failed to update preset", { error });
      setSaveError(error instanceof Error ? error.message : "Failed to update preset");
    } finally {
      setSavingGroup(null);
    }
  }, [projectId, getGroupParams, getSelectedPreset, refetchPresets]);

  // Handle preset selection (from incoming version)
  const handlePresetChange = useCallback((preset: Preset | null) => {
    setSelectedPreset(preset?.name || null);
    if (!preset) return;

    // Apply preset values
    const newValues = { ...values };
    for (const [key, value] of Object.entries(preset.params)) {
      newValues[key] = value;
    }
    onChange(newValues);
  }, [values, onChange]);

  // Cancel save mode
  const handleCancelSave = useCallback(() => {
    setIsSaveMode(false);
    setPresetName("");
    setPresetDescription("");
    setSaveError(null);
  }, []);

  // Handle saving preset (global version from incoming)
  const handleSavePreset = useCallback(async () => {
    if (!projectId || !presetName.trim()) return;

    setIsSaving(true);
    setSaveError(null);

    try {
      // Get current param values
      const params: Record<string, unknown> = {};
      for (const [name, schema] of Object.entries(inputs)) {
        if (getInputUI(schema) !== "hidden" && schema.type !== "message" && schema.type !== "attachments" && schema.type !== "preset") {
          const value = values[name] ?? getInputDefault(schema);
          if (value !== undefined) {
            params[name] = value;
          }
        }
      }

      const result = await presetGrpc.createPreset(projectId, {
        name: presetName.trim(),
        description: presetDescription.trim() || "",
        params,
        tag: workflowTag || (workflowName.startsWith("builtin://") ? workflowName.slice(10) : workflowName),
      });

      if (!result.success) {
        setSaveError(result.error || "Failed to save preset");
        return;
      }

      logger.info("[WorkflowParamsPanel] Saved preset", {
        name: presetName,
        workflowName,
        paramCount: Object.keys(params).length,
      });

      // Refresh presets
      if (refetchPresets) {
        await refetchPresets(projectId);
      }

      // Reset form
      handleCancelSave();
    } catch (error) {
      logger.error("[WorkflowParamsPanel] Failed to save preset", { error });
      setSaveError(error instanceof Error ? error.message : "Failed to save preset");
    } finally {
      setIsSaving(false);
    }
  }, [projectId, presetName, presetDescription, inputs, values, workflowName, workflowTag, refetchPresets, handleCancelSave]);


  // Get value for a param (use current value or default)
  const getParamValue = (name: string, schema: InputDef): unknown => {
    return values[name] ?? getInputDefault(schema);
  };

  // Don't render if no configurable inputs
  if (configurableCount === 0) {
    return null;
  }

  // Check if a param is unset (required, no value, no meaningful default)
  const isParamUnset = (name: string, schema: InputDef): boolean => {
    const hasValue = values[name] !== undefined && values[name] !== null && values[name] !== "";
    // Model params can't be empty string - treat as no default
    const defVal = getInputDefault(schema);
    const hasDefault = defVal !== undefined && defVal !== null &&
      !(schema.type === "model" && defVal === "");
    return !hasValue && !hasDefault;
  };

  // Render a param input
  const renderParamInput = (name: string, schema: InputDef) => {
    const unset = isParamUnset(name, schema);

    // Render preset as multi-enum dropdown with preset names as options
    if (schema.type === "preset") {
      const tagValue = getInputTag(schema);
      const tags = tagValue ? tagValue.split(",") : [];
      const matchingPresets = filteredPresets.filter(p => p.tag && tags.includes(p.tag));
      const presetNames = new Set(matchingPresets.map(p => p.name));
      // Filter current value to only include presets that actually exist
      const currentValue = getParamValue(name, schema);
      const filteredValue = Array.isArray(currentValue)
        ? (currentValue as string[]).filter(v => presetNames.has(v))
        : currentValue;
      const presetSchema = {
        ...schema,
        type: "enum",
        multi: getInputMulti(schema) ?? false,
        enum: matchingPresets.map(p => p.name),
      } as unknown as InputDef;
      return (
        <div key={name} className={cn(unset && "pl-2 border-l-2 border-red-500")}>
          <ProtoFieldRenderer
            schema={inputDefToSchema(name, presetSchema)}
            value={filteredValue}
            onChange={(value) => handleParamChange(name, value)}
            disabled={disabled}
            hideCELToggle={true}
          />
        </div>
      );
    }

    return (
      <div key={name} className={cn(unset && "pl-2 border-l-2 border-red-500")}>
        <ProtoFieldRenderer
          schema={inputDefToSchema(name, schema)}
          value={getParamValue(name, schema)}
          onChange={(value) => handleParamChange(name, value)}
          disabled={disabled}
          hideCELToggle={true}
        />
      </div>
    );
  };

  // Render save UI for a group
  const renderGroupSaveUI = (group: ParamGroup) => {
    // Only show save UI for groups with tags
    if (!group.tag || !projectId) return null;

    const isDirty = isGroupDirty(group);
    if (!isDirty) return null;

    const selectedPreset = getSelectedPreset(group.group);
    const canUpdate = selectedPreset?.source === "user";
    const isSaving = savingGroup === group.group;
    const isInSaveMode = saveMode?.group === group.group;

    return (
      <div className="mt-3 pt-2 border-t border-border/30">
        {saveError && saveMode?.group === group.group && (
          <p className="text-xs text-destructive mb-2">{saveError}</p>
        )}

        {isInSaveMode && saveMode.mode === "new" ? (
          // New preset name input
          <div className="flex items-center gap-2">
            <input
              type="text"
              value={newPresetName}
              onChange={(e) => setNewPresetName(e.target.value)}
              placeholder="Preset name"
              disabled={isSaving}
              className={cn(
                "flex-1 px-2 py-1 text-sm rounded border border-border bg-background",
                "focus:outline-none focus:ring-1 focus:ring-ring",
                "placeholder:text-muted-foreground"
              )}
              autoFocus
              onKeyDown={(e) => {
                if (e.key === "Enter" && newPresetName.trim()) {
                  handleSaveAsNew(group);
                } else if (e.key === "Escape") {
                  setSaveMode(null);
                  setNewPresetName("");
                  setSaveError(null);
                }
              }}
            />
            <button
              type="button"
              onClick={() => handleSaveAsNew(group)}
              disabled={!newPresetName.trim() || isSaving}
              className={cn(
                "px-2 py-1 text-xs rounded transition-colors",
                newPresetName.trim() && !isSaving
                  ? "bg-primary/20 hover:bg-primary/30 text-primary"
                  : "bg-muted text-muted-foreground"
              )}
            >
              {isSaving ? <Loader2 className="w-3 h-3 animate-spin" /> : "Save"}
            </button>
            <button
              type="button"
              onClick={() => {
                setSaveMode(null);
                setNewPresetName("");
                setSaveError(null);
              }}
              disabled={isSaving}
              className="px-2 py-1 text-xs text-muted-foreground hover:text-foreground transition-colors"
            >
              Cancel
            </button>
          </div>
        ) : (
          // Save buttons
          <div className="flex items-center gap-2 text-xs">
            <button
              type="button"
              onClick={() => setSaveMode({ group: group.group, mode: "new" })}
              disabled={isSaving}
              className={cn(
                "flex items-center gap-2 px-2 py-1 rounded transition-colors",
                "text-muted-foreground hover:text-foreground hover:bg-accent"
              )}
            >
              <Save className="w-3.5 h-3.5" />
              <span>Save {group.label} preset</span>
            </button>
            <Tooltip content="Save this group's settings as a reusable component" delay={300}>
              <Info className="w-3.5 h-3.5 text-muted-foreground hover:text-foreground transition-colors cursor-help" />
            </Tooltip>
            {canUpdate && (
              <button
                type="button"
                onClick={() => handleUpdatePreset(group)}
                disabled={isSaving}
                className={cn(
                  "flex items-center gap-2 px-2 py-1 rounded transition-colors",
                  "bg-primary/10 hover:bg-primary/20 text-primary"
                )}
              >
                {isSaving ? (
                  <Loader2 className="w-3.5 h-3.5 animate-spin" />
                ) : (
                  <Save className="w-3.5 h-3.5" />
                )}
                <span>Update "{selectedPreset.name}"</span>
              </button>
            )}
            <div className="flex-1" />
            <span className="text-muted-foreground">
              {selectedPreset ? `Modified from "${selectedPreset.name}"` : "Modified"}
            </span>
          </div>
        )}
      </div>
    );
  };

  // When header is hidden, always show content (controlled externally)
  const showContent = hideHeader || !isCollapsed;

  return (
    <div className={cn(
      className
    )}>
      {/* Header - only show if not hidden */}
      {!hideHeader && (
        <button
          type="button"
          onClick={() => setIsCollapsed(!isCollapsed)}
          className={cn(
            "flex items-center justify-between w-full px-3 py-2 text-sm font-medium",
            "hover:bg-accent/50 transition-colors rounded-t-lg",
            isCollapsed && "rounded-b-lg"
          )}
        >
          <div className="flex items-center gap-2">
            <Settings2 className="w-4 h-4 text-muted-foreground" />
            <span>Workflow Parameters</span>
            <span className="text-xs text-muted-foreground">
              ({configurableCount} settings)
            </span>
          </div>
          {isCollapsed ? (
            <ChevronRight className="w-4 h-4 text-muted-foreground" />
          ) : (
            <ChevronDown className="w-4 h-4 text-muted-foreground" />
          )}
        </button>
      )}

      {/* Content */}
      {showContent && (
        <div className={cn(
          "overflow-y-auto",
          hideHeader ? "px-1 pb-1" : "px-3 pb-3"
        )}>
          {/* Save form (shown when isSaveMode is true) */}
          {projectId && isSaveMode && (
            <div className="py-2 space-y-2">
              <div className="flex items-center gap-2">
                <input
                  type="text"
                  value={presetName}
                  onChange={(e) => setPresetName(e.target.value)}
                  placeholder="Preset name"
                  disabled={isSaving}
                  className={cn(
                    "flex-1 px-2 py-1 text-sm rounded border border-border bg-background",
                    "focus:outline-none focus:ring-1 focus:ring-ring",
                    "placeholder:text-muted-foreground"
                  )}
                  autoFocus
                  onKeyDown={(e) => {
                    if (e.key === "Enter" && presetName.trim()) {
                      handleSavePreset();
                    } else if (e.key === "Escape") {
                      handleCancelSave();
                    }
                  }}
                />
                <button
                  type="button"
                  onClick={handleSavePreset}
                  disabled={!presetName.trim() || isSaving}
                  className={cn(
                    "p-1.5 rounded transition-colors",
                    presetName.trim() && !isSaving
                      ? "hover:bg-primary/20 text-primary"
                      : "text-muted-foreground opacity-50"
                  )}
                >
                  {isSaving ? (
                    <Loader2 className="w-4 h-4 animate-spin" />
                  ) : (
                    <Save className="w-4 h-4" />
                  )}
                </button>
                <button
                  type="button"
                  onClick={handleCancelSave}
                  disabled={isSaving}
                  className="p-1.5 rounded hover:bg-accent transition-colors text-muted-foreground"
                >
                  <X className="w-4 h-4" />
                </button>
              </div>
              <input
                type="text"
                value={presetDescription}
                onChange={(e) => setPresetDescription(e.target.value)}
                placeholder="Description (optional)"
                disabled={isSaving}
                className={cn(
                  "w-full px-2 py-1 text-sm rounded border border-border bg-background",
                  "focus:outline-none focus:ring-1 focus:ring-ring",
                  "placeholder:text-muted-foreground"
                )}
              />
              {saveError && (
                <p className="text-xs text-destructive">{saveError}</p>
              )}
            </div>
          )}
          {/* Param Groups */}
          {groups.map((paramGroup) => {
            // Find presets matching this group's tag
            const groupPresets = paramGroup.tag
              ? filteredPresets.filter(preset => preset.tag === paramGroup.tag)
              : [];

            return (
              <div key={paramGroup.group} className="pt-2 border-t border-border/50 first:pt-2 first:border-t-0">
                {/* Group header - collapsible for named groups */}
                {paramGroup.group ? (
                  <div className="mb-2">
                    <button
                      type="button"
                      onClick={() => toggleGroup(paramGroup.group)}
                      className="flex items-center gap-2 text-sm text-muted-foreground hover:text-foreground transition-colors"
                    >
                      {expandedGroups.has(paramGroup.group) ? (
                        <ChevronDown className="w-4 h-4" />
                      ) : (
                        <ChevronRight className="w-4 h-4" />
                      )}
                      <span className="font-medium">{paramGroup.label}</span>
                      <span className="text-xs">({paramGroup.params.length})</span>
                    </button>
                    {/* Show preset picker for group if expanded and has relevant presets */}
                    {expandedGroups.has(paramGroup.group) && groupPresets.length > 0 && (
                      <div className="ml-6 mt-2">
                        <PresetPicker
                          presets={groupPresets}
                          value={selectedPreset}
                          onChange={handlePresetChange}
                          isLoading={presetsLoading}
                          disabled={disabled}
                          projectId={projectId}
                          workflowName={workflowName}
                          groupName={paramGroup.group}
                        />
                      </div>
                    )}
                  </div>
                ) : (
                  // Default group - show preset picker if workflow has tag
                  groupPresets.length > 0 ? (
                    <div className="mb-2">
                      <PresetPicker
                        presets={groupPresets}
                        value={selectedPreset}
                        onChange={handlePresetChange}
                        isLoading={presetsLoading}
                        disabled={disabled}
                        projectId={projectId}
                        workflowName={workflowName}
                        groupName=""
                        onSave={() => setIsSaveMode(true)}
                        saveTooltip="Save workflow preset"
                      />
                    </div>
                  ) : null
                )}

                {/* Group params */}
                {(!paramGroup.group || expandedGroups.has(paramGroup.group)) && (
                  <>
                    <div className="space-y-3">
                      {paramGroup.params.map(([name, schema]) => renderParamInput(name, schema))}
                    </div>

                    {/* Per-group save UI (only for groups with tags and when dirty) */}
                    {renderGroupSaveUI(paramGroup)}
                  </>
                )}
              </div>
            );
          })}

          {/* Global Save as Preset Section (when no group-specific saving) */}
        </div>
      )}
    </div>
  );
}

export default WorkflowParamsPanel;