// Copyright (c) 2025 Reliant Labs

import { useState, useCallback, useEffect } from "react";
import { ChevronDown, ChevronRight, Save, Loader2 } from "lucide-react";
import { cn } from "../../lib/utils";
import { useGlobalDataStore, type Preset } from "../../store/globalDataStore";
import { presetGrpc } from "../../api/preset-grpc";
import type { InputDef } from "../../lib/inputHelpers";
import { getInputDefault } from "../../lib/inputHelpers";
import { inputDefToSchema } from "../../lib/nodeFieldAdapter";
import { ProtoFieldRenderer } from "./ProtoFieldRenderer";
import { InlinePresetPicker } from "./InlinePresetPicker";
import { toJson } from "@bufbuild/protobuf";
import { ValueSchema } from "@bufbuild/protobuf/wkt";
import type { PresetsConfig } from "../../types/workflow";
import { Section, SectionFields, SectionLabel } from "./config/primitives";

// ============================================
// Helpers
// ============================================

/**
 * BigInt-safe stable stringify for structural value comparison.
 * `JSON.stringify` throws on BigInt; proto integer wrappers may surface them.
 */
function stableStringify(value: unknown): string {
  try {
    return JSON.stringify(value, (_k, v) => (typeof v === "bigint" ? v.toString() : v));
  } catch {
    return String(value);
  }
}

// ============================================
// Shared Types
// ============================================

export interface InputGroupDef {
  name: string; // "" for top-level, group name otherwise
  label: string;
  presets?: PresetsConfig;
  inputs: Array<{ name: string; schema: InputDef }>;
}

interface WorkflowInputGroupProps {
  /** Group definition */
  group: InputGroupDef;

  /** When true (top-level group), skip collapsible header and render inputs directly */
  isTopLevel?: boolean;
  
  /** Current input values */
  values: Record<string, unknown>;
  
  /** Called when any input value changes */
  onChange: (name: string, value: unknown) => void;
  
  /** Available presets for this group (filtered by tag) */
  presets: Preset[];
  
  /** Currently selected preset name */
  selectedPreset: string | null;
  
  /** Called when preset selection changes */
  onPresetSelect: (preset: Preset | null) => void;
  
  /** Whether presets are loading */
  presetsLoading?: boolean;
  
  /** Project ID (required for saving presets) */
  projectId?: string;
  
  /** Whether the group is expanded */
  defaultExpanded?: boolean;
  
  /** Whether inputs are disabled */
  disabled?: boolean;
}

// ============================================
// Helper Functions
// ============================================

/** Get current value for an input (from values or default) */
function getInputValue(
  name: string,
  schema: InputDef,
  values: Record<string, unknown>
): unknown {
  if (values?.[name] !== undefined) {
    const raw = values[name];
    // Unwrap proto Value wrappers (step args from google.protobuf.Struct are Value objects)
    if (raw && typeof raw === "object" && ("kind" in (raw as Record<string, unknown>) || (raw as any)?.$typeName === "google.protobuf.Value")) {
      try {
        return toJson(ValueSchema, raw as any);
      } catch {
        return raw;
      }
    }
    return raw;
  }
  return getInputDefault(schema);
}

// ============================================
// Main Component
// ============================================

export function WorkflowInputGroup({
  group,
  values,
  onChange,
  presets,
  selectedPreset,
  onPresetSelect,
  presetsLoading = false,
  projectId,
  defaultExpanded = true,
  disabled = false,
  isTopLevel = false,
}: WorkflowInputGroupProps) {
  const [isExpanded, setIsExpanded] = useState(defaultExpanded);
  
  // Sync expansion state when defaultExpanded prop changes (e.g., when switching steps)
  useEffect(() => {
    setIsExpanded(defaultExpanded);
  }, [defaultExpanded]);
  
  // Save preset state
  const [savingGroup, setSavingGroup] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);
  const [saveMode, setSaveMode] = useState<"new" | "update" | null>(null);
  const [newPresetName, setNewPresetName] = useState("");
  
  // Get refetch function from global store
  const refetchPresets = useGlobalDataStore((state) => state.refetchPresets);
  
  // Find the selected preset object
  const selectedPresetObj = presets.find(p => p.name === selectedPreset) || null;
  
  // Check if group is dirty (values differ from preset or defaults)
  const isDirty = useCallback((): boolean => {
    for (const { name: paramName, schema } of group.inputs) {
      const currentValue = values?.[paramName];

      if (selectedPresetObj) {
        // Compare to preset values
        // For group params, the preset stores them without the group prefix
        const presetParamName = group.name ? paramName.split(".")[1] : paramName;
        const presetValue = selectedPresetObj.params[presetParamName];

        // Structural comparison handles primitives, objects, and arrays;
        // BigInts (from proto integer wrappers) are coerced via replacer.
        const def = getInputDefault(schema);
        const normalizedCurrent = stableStringify(currentValue ?? def);
        const normalizedPreset = stableStringify(presetValue ?? def);

        if (normalizedCurrent !== normalizedPreset) {
          return true;
        }
      } else {
        // No preset selected - compare to defaults
        const defaultValue = getInputDefault(schema);
        if (currentValue !== undefined && currentValue !== defaultValue) {
          // Structural compare (handles object/array params; BigInt-safe)
          if (stableStringify(currentValue) !== stableStringify(defaultValue)) {
            return true;
          }
        }
      }
    }
    return false;
  }, [group, values, selectedPresetObj]);

  // Get params for saving (strips group prefix)
  const getGroupParams = useCallback((): Record<string, unknown> => {
    const params: Record<string, unknown> = {};
    for (const { name, schema } of group.inputs) {
      const value = values?.[name] ?? getInputDefault(schema);
      if (value !== undefined) {
        // For group params, strip the group prefix for the preset
        const paramName = group.name ? name.split(".")[1] : name;
        params[paramName] = value;
      }
    }
    return params;
  }, [group, values]);

  // Handle save as new preset
  const handleSaveAsNew = useCallback(async () => {
    if (!projectId || !newPresetName.trim() || !group.presets?.tag) return;

    setSavingGroup(true);
    setSaveError(null);

    try {
      const params = getGroupParams();

      const result = await presetGrpc.createPreset(projectId, {
        name: newPresetName.trim(),
        description: "",
        params,
        tag: group.presets.tag,
      });

      if (!result.success) {
        setSaveError(result.error || "Failed to save preset");
        return;
      }

      // Refresh presets
      await refetchPresets(projectId);

      // Select the new preset
      const newPreset: Preset = {
        name: newPresetName.trim(),
        description: "",
        params,
        tag: group.presets.tag,
        source: "project",
      };
      onPresetSelect(newPreset);

      // Reset save mode
      setSaveMode(null);
      setNewPresetName("");
    } catch (error) {
      console.error("Failed to save preset:", error);
      setSaveError(error instanceof Error ? error.message : "Failed to save preset");
    } finally {
      setSavingGroup(false);
    }
  }, [projectId, newPresetName, group.presets?.tag, getGroupParams, refetchPresets, onPresetSelect]);

  // Handle update existing preset
  const handleUpdatePreset = useCallback(async () => {
    if (!projectId || !group.presets?.tag || !selectedPresetObj) return;
    if (selectedPresetObj.source !== "user") return;

    setSavingGroup(true);
    setSaveError(null);

    try {
      const params = getGroupParams();

      const result = await presetGrpc.updatePreset(projectId, selectedPresetObj.name, {
        newParams: params,
      });

      if (!result.success) {
        setSaveError(result.error || "Failed to update preset");
        return;
      }

      // Refresh presets
      await refetchPresets(projectId);

      // Reset save mode
      setSaveMode(null);
    } catch (error) {
      console.error("Failed to update preset:", error);
      setSaveError(error instanceof Error ? error.message : "Failed to update preset");
    } finally {
      setSavingGroup(false);
    }
  }, [projectId, group.presets?.tag, selectedPresetObj, getGroupParams, refetchPresets]);

  const groupIsDirty = isDirty();
  const canUpdate = selectedPresetObj?.source === "user";
  const hasInputs = group.inputs.length > 0;

  // Top-level group: no collapsible header, render inputs directly
  if (isTopLevel) {
    return (
      <Section>
        <SectionLabel>Inputs</SectionLabel>
        {presets.length > 0 && (
          <div className="mb-3 flex items-center justify-end">
            <InlinePresetPicker
              presets={presets}
              value={selectedPreset}
              onChange={onPresetSelect}
              groupLabel={group.name || undefined}
              isLoading={presetsLoading}
              disabled={disabled}
            />
          </div>
        )}

        {hasInputs && (
          <>
            <SectionFields>
              {group.inputs.map(({ name, schema }) => (
                <ProtoFieldRenderer
                  key={name}
                  schema={inputDefToSchema(name, schema)}
                  value={getInputValue(name, schema, values)}
                  onChange={(value) => onChange(name, value)}
                  disabled={disabled}
                  hideCELToggle
                />
              ))}
            </SectionFields>

            {/* Save preset UI */}
            {groupIsDirty && group.presets?.tag && projectId && (
              <div className="mt-3 pt-2 border-t border-border/30">
                {saveError && (
                  <p className="text-xs text-destructive mb-2">{saveError}</p>
                )}

                {saveMode === "new" ? (
                  <div className="flex items-center gap-2">
                    <input
                      type="text"
                      value={newPresetName}
                      onChange={(e) => setNewPresetName(e.target.value)}
                      placeholder="Preset name"
                      disabled={savingGroup}
                      className={cn(
                        "flex-1 px-2 py-1 text-sm rounded border border-border bg-background",
                        "focus:outline-none focus:ring-1 focus:ring-ring",
                        "placeholder:text-muted-foreground"
                      )}
                      autoFocus
                      onKeyDown={(e) => {
                        if (e.key === "Enter" && newPresetName.trim()) {
                          handleSaveAsNew();
                        } else if (e.key === "Escape") {
                          setSaveMode(null);
                          setNewPresetName("");
                          setSaveError(null);
                        }
                      }}
                    />
                    <button
                      type="button"
                      onClick={handleSaveAsNew}
                      disabled={!newPresetName.trim() || savingGroup}
                      className={cn(
                        "px-2 py-1 text-xs rounded transition-colors",
                        newPresetName.trim() && !savingGroup
                          ? "bg-primary/20 hover:bg-primary/30 text-primary"
                          : "bg-muted text-muted-foreground"
                      )}
                    >
                      {savingGroup ? <Loader2 className="w-3 h-3 animate-spin" /> : "Save"}
                    </button>
                    <button
                      type="button"
                      onClick={() => {
                        setSaveMode(null);
                        setNewPresetName("");
                        setSaveError(null);
                      }}
                      disabled={savingGroup}
                      className="px-2 py-1 text-xs text-muted-foreground hover:text-foreground transition-colors"
                    >
                      Cancel
                    </button>
                  </div>
                ) : (
                  <div className="flex items-center gap-2 text-xs">
                    <span className="text-muted-foreground">
                      {selectedPresetObj ? `Modified from "${selectedPresetObj.name}"` : "Modified"}
                    </span>
                    <div className="flex-1" />
                    <button
                      type="button"
                      onClick={() => setSaveMode("new")}
                      disabled={savingGroup}
                      className={cn(
                        "flex items-center gap-1 px-2 py-1 rounded transition-colors",
                        "text-muted-foreground hover:text-foreground hover:bg-accent"
                      )}
                    >
                      <Save className="w-3 h-3" />
                      <span>Save as new</span>
                    </button>
                    {canUpdate && (
                      <button
                        type="button"
                        onClick={handleUpdatePreset}
                        disabled={savingGroup}
                        className={cn(
                          "flex items-center gap-1 px-2 py-1 rounded transition-colors",
                          "bg-primary/10 hover:bg-primary/20 text-primary"
                        )}
                      >
                        {savingGroup ? (
                          <Loader2 className="w-3 h-3 animate-spin" />
                        ) : (
                          <Save className="w-3 h-3" />
                        )}
                        <span>Update "{selectedPresetObj.name}"</span>
                      </button>
                    )}
                  </div>
                )}
              </div>
            )}
          </>
        )}
      </Section>
    );
  }

  // Named group: collapsible header with chevron
  return (
    <Section>
      <SectionLabel>Input Group</SectionLabel>
      <div className="cpv2-param-group">
        <button
          type="button"
          onClick={() => setIsExpanded(!isExpanded)}
          className={`cpv2-param-group-header w-full${isExpanded ? "" : " collapsed"}`}
        >
          <div className="cpv2-pgh-left">
            {isExpanded ? (
              <ChevronDown className="cpv2-pgh-chevron" />
            ) : (
              <ChevronRight className="cpv2-pgh-chevron" />
            )}
            <span className="cpv2-pgh-label">{group.label}</span>
          </div>
          <div className="cpv2-pgh-right">
            {selectedPreset && <span className="cpv2-pgh-preset">{selectedPreset}</span>}
            <span className="text-2xs text-muted-foreground">{group.inputs.length} params</span>
            {groupIsDirty && (
              <span className="w-1.5 h-1.5 rounded-full bg-amber-500" title="Modified" />
            )}
          </div>
        </button>

        {presets.length > 0 && isExpanded && (
          <div className="px-2.5 pt-2 flex justify-end">
            <InlinePresetPicker
              presets={presets}
              value={selectedPreset}
              onChange={onPresetSelect}
              groupLabel={group.name || undefined}
              isLoading={presetsLoading}
              disabled={disabled}
            />
          </div>
        )}

        {isExpanded && hasInputs && (
          <div className="cpv2-param-group-body">
            {group.inputs.map(({ name, schema }) => (
              <ProtoFieldRenderer
                key={name}
                schema={inputDefToSchema(name, schema)}
                value={getInputValue(name, schema, values)}
                onChange={(value) => onChange(name, value)}
                disabled={disabled}
                hideCELToggle
              />
            ))}

          {/* Save preset UI */}
          {groupIsDirty && group.presets?.tag && projectId && (
            <div className="mt-3 pt-2 border-t border-border/30">
              {saveError && (
                <p className="text-xs text-destructive mb-2">{saveError}</p>
              )}

              {saveMode === "new" ? (
                <div className="flex items-center gap-2">
                  <input
                    type="text"
                    value={newPresetName}
                    onChange={(e) => setNewPresetName(e.target.value)}
                    placeholder="Preset name"
                    disabled={savingGroup}
                    className={cn(
                      "flex-1 px-2 py-1 text-sm rounded border border-border bg-background",
                      "focus:outline-none focus:ring-1 focus:ring-ring",
                      "placeholder:text-muted-foreground"
                    )}
                    autoFocus
                    onKeyDown={(e) => {
                      if (e.key === "Enter" && newPresetName.trim()) {
                        handleSaveAsNew();
                      } else if (e.key === "Escape") {
                        setSaveMode(null);
                        setNewPresetName("");
                        setSaveError(null);
                      }
                    }}
                  />
                  <button
                    type="button"
                    onClick={handleSaveAsNew}
                    disabled={!newPresetName.trim() || savingGroup}
                    className={cn(
                      "px-2 py-1 text-xs rounded transition-colors",
                      newPresetName.trim() && !savingGroup
                        ? "bg-primary/20 hover:bg-primary/30 text-primary"
                        : "bg-muted text-muted-foreground"
                    )}
                  >
                    {savingGroup ? <Loader2 className="w-3 h-3 animate-spin" /> : "Save"}
                  </button>
                  <button
                    type="button"
                    onClick={() => {
                      setSaveMode(null);
                      setNewPresetName("");
                      setSaveError(null);
                    }}
                    disabled={savingGroup}
                    className="px-2 py-1 text-xs text-muted-foreground hover:text-foreground transition-colors"
                  >
                    Cancel
                  </button>
                </div>
              ) : (
                <div className="flex items-center gap-2 text-xs">
                  <span className="text-muted-foreground">
                    {selectedPresetObj ? `Modified from "${selectedPresetObj.name}"` : "Modified"}
                  </span>
                  <div className="flex-1" />
                  <button
                    type="button"
                    onClick={() => setSaveMode("new")}
                    disabled={savingGroup}
                    className={cn(
                      "flex items-center gap-1 px-2 py-1 rounded transition-colors",
                      "text-muted-foreground hover:text-foreground hover:bg-accent"
                    )}
                  >
                    <Save className="w-3 h-3" />
                    <span>Save as new</span>
                  </button>
                  {canUpdate && (
                    <button
                      type="button"
                      onClick={handleUpdatePreset}
                      disabled={savingGroup}
                      className={cn(
                        "flex items-center gap-1 px-2 py-1 rounded transition-colors",
                        "bg-primary/10 hover:bg-primary/20 text-primary"
                      )}
                    >
                      {savingGroup ? (
                        <Loader2 className="w-3 h-3 animate-spin" />
                      ) : (
                        <Save className="w-3 h-3" />
                      )}
                      <span>Update "{selectedPresetObj.name}"</span>
                    </button>
                  )}
                </div>
              )}
            </div>
          )}
          </div>
        )}
      </div>
    </Section>
  );
}

export default WorkflowInputGroup;