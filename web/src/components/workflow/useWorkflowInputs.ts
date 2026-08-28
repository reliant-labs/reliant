// Copyright (c) 2025 Reliant Labs

import { useState, useMemo, useCallback, useEffect, useRef } from "react";
import { fromJson } from "@bufbuild/protobuf";
import { ValueSchema } from "@bufbuild/protobuf/wkt";
import { workflowGrpc, type Workflow } from "../../api/workflow-grpc";
import { presetGrpc } from "../../api/preset-grpc";
import { usePresetsForWorkflow, type Preset } from "../../store/globalDataStore";
import { usePreferencesStore } from "../../store/preferencesStore";
import type { InputGroupDef } from "./WorkflowInputGroup";
import type { InputDef } from "../../lib/inputHelpers";
import { isConfigurableInput, getInputNestedInputs, getInputPresetConfig, getInputUI, getInputDefault } from "../../lib/inputHelpers";
import { canonicalizeBuiltinWorkflowRef } from "./workflowRef";
import { isCelTemplate } from "../../lib/celTemplate";

// ============================================
// Types
// ============================================

export interface UseWorkflowInputsOptions {
  /** Project ID for API calls */
  projectId: string | undefined;
  /** Workflow reference (e.g., "builtin://agent", "workflow://my-flow", or raw path) */
  workflowRef: string;
  /** Current input/arg values */
  values: Record<string, unknown>;
  /** Callback when values change */
  onValuesChange: (values: Record<string, unknown>) => void;
  /** Whether to enable fetching (for mode toggles). Defaults to true. */
  enabled?: boolean;
  /** Previously stored preset selections from the step (e.g., step.presets map) */
  storedPresets?: Record<string, string>;
}

export interface UseWorkflowInputsResult {
  /** Parsed workflow definition */
  workflowDef: Workflow | null;
  /** Whether workflow definition is loading */
  loadingDef: boolean;
  /** Input groups built from workflow definition */
  inputGroups: InputGroupDef[];
  /** All presets for this workflow */
  presets: Preset[];
  /** Whether presets are loading */
  presetsLoading: boolean;
  /** Currently selected preset per group */
  selectedPresets: Record<string, string | null>;
  /** Get presets filtered by tag for a group */
  getPresetsForGroup: (tag?: string) => Preset[];
  /** Handle preset selection - applies preset values */
  handlePresetSelect: (groupName: string, preset: Preset | null) => void;
  /** Handle individual input value change */
  handleInputChange: (name: string, value: unknown) => void;
  /** Get current value for an input (from values or default) */
  getInputValue: (name: string, schema: InputDef) => unknown;
}

// ============================================
// Exported Helper Functions
// ============================================

/** Normalize group name: "default" and "" both mean top-level */
function isTopLevelGroup(name: string): boolean {
  return name === "" || name === "default";
}

/** Get the full key for a param within a group */
function groupParamKey(groupName: string, paramName: string): string {
  return isTopLevelGroup(groupName) ? paramName : `${groupName}.${paramName}`;
}

/** Strip protocol prefixes from workflow reference */
export function normalizeWorkflowRef(ref: string): string {
  if (ref.startsWith("builtin://")) {
    return ref.replace("builtin://", "");
  }
  if (ref.startsWith("workflow://")) {
    return ref.replace("workflow://", "");
  }
  return ref;
}

/** Get display-friendly workflow name (strips prefix, formats nicely) */
export function getWorkflowDisplayName(ref: string, format: boolean = false): string {
  const name = normalizeWorkflowRef(ref);
  if (!format) return name;

  // Format: replace dashes/underscores with spaces, title case
  return name
    .replace(/[-_]/g, " ")
    .split(" ")
    .map(word => word.charAt(0).toUpperCase() + word.slice(1))
    .join(" ");
}

// ============================================
// Internal Helper Functions
// ============================================

/** Ensure workflow ref has a protocol prefix for preset lookup */
function ensureWorkflowRefPrefix(ref: string): string {
  if (!ref) return "";
  if (ref.startsWith("builtin://") || ref.startsWith("workflow://")) {
    return ref;
  }
  // Default to builtin:// for backward compatibility
  return `builtin://${ref}`;
}

/** Build input groups from workflow definition by iterating proto inputs directly */
function buildInputGroups(workflowDef: Workflow | null): InputGroupDef[] {
  if (!workflowDef) return [];

  const rawInputs = (workflowDef.inputs ?? (workflowDef as any).params) as Record<string, any> | undefined;
  if (!rawInputs || Object.keys(rawInputs).length === 0) return [];

  const groups: InputGroupDef[] = [];
  const topLevel: Array<{ name: string; schema: InputDef }> = [];
  const groupedMap = new Map<string, { presets?: { tag: string }; ui?: string; inputs: Array<{ name: string; schema: InputDef }> }>();

  for (const [name, rawInput] of Object.entries(rawInputs)) {
    if (rawInput?.type === "group") {
      const nestedInputs = getInputNestedInputs(rawInput);
      const presetConfig = getInputPresetConfig(rawInput);
      const ui = getInputUI(rawInput);
      const group: { presets?: { tag: string }; ui?: string; inputs: Array<{ name: string; schema: InputDef }> } = {
        presets: presetConfig?.tag ? { tag: presetConfig.tag } : undefined,
        ui,
        inputs: [],
      };
      if (nestedInputs) {
        for (const [paramName, nestedRaw] of Object.entries(nestedInputs)) {
          if (!isConfigurableInput(nestedRaw)) continue;
          group.inputs.push({ name: `${name}.${paramName}`, schema: nestedRaw });
        }
      }
      if (group.inputs.length > 0) {
        groupedMap.set(name, group);
      }
    } else {
      if (!isConfigurableInput(rawInput)) continue;
      topLevel.push({ name, schema: rawInput });
    }
  }

  if (topLevel.length > 0) {
    groups.push({
      name: "",
      label: "Parameters",
      presets: workflowDef.presets,
      inputs: topLevel,
    });
  }

  for (const [groupName, groupData] of groupedMap) {
    groups.push({
      name: groupName,
      label: groupName,
      presets: groupData.presets,
      inputs: groupData.inputs,
    });
  }

  return groups;
}

// ============================================
// Hook
// ============================================

export function useWorkflowInputs({
  projectId,
  workflowRef,
  values,
  onValuesChange,
  enabled = true,
  storedPresets: storedPresetsFromStep,
}: UseWorkflowInputsOptions): UseWorkflowInputsResult {
  // Workflow definition state
  const [workflowDef, setWorkflowDef] = useState<Workflow | null>(null);
  const [loadingDef, setLoadingDef] = useState(false);

  // Selected presets per group
  const [selectedPresets, setSelectedPresets] = useState<Record<string, string | null>>({});

  // Full ref with prefix for preset lookup
  const fullWorkflowRef = ensureWorkflowRefPrefix(workflowRef);

  // Fetch presets for this workflow
  const { presets, loading: presetsLoading } = usePresetsForWorkflow(fullWorkflowRef);

  // Fetch workflow definition when selection changes
  useEffect(() => {
    if (!projectId || !workflowRef || !enabled) {
      setWorkflowDef(null);
      return;
    }

    const fetchDef = async () => {
      setLoadingDef(true);
      try {
        const builtinWorkflowRefs = (await workflowGrpc.listWorkflows(projectId))
          .filter((workflow) => workflow.source === "builtin")
          .map((workflow) => workflow.name);
        const canonicalWorkflowRef = canonicalizeBuiltinWorkflowRef(
          workflowRef,
          builtinWorkflowRefs,
        );
        const result = await workflowGrpc.getWorkflow(projectId, {
          name: canonicalWorkflowRef,
        });
        setWorkflowDef(result.workflow ?? null);
        // Reset preset selection and the "stored presets applied" gate so the
        // stored-presets effect re-runs against the new workflow's preset list.
        setSelectedPresets({});
        setStoredPresetsApplied(false);
      } catch (error) {
        console.error("Failed to fetch workflow definition:", error);
        setWorkflowDef(null);
      } finally {
        setLoadingDef(false);
      }
    };
    fetchDef();
  }, [projectId, workflowRef, enabled]);

  // Build input groups from workflow definition
  const inputGroups = useMemo(
    () => buildInputGroups(workflowDef),
    [workflowDef]
  );

  // Ref to read latest values without re-triggering the effect
  const valuesRef = useRef(values);
  valuesRef.current = values;

  // Resolve stored preset selections from the step (step.presets map)
  const storedPresetsRef = useRef(storedPresetsFromStep);
  storedPresetsRef.current = storedPresetsFromStep;
  const [storedPresetsApplied, setStoredPresetsApplied] = useState(false);

  useEffect(() => {
    if (!workflowDef || !enabled) return;
    if (presetsLoading) return;
    if (storedPresetsApplied) return;

    const stored = storedPresetsRef.current;
    if (!stored || Object.keys(stored).length === 0) return;

    const newSelectedPresets: Record<string, string | null> = {};
    let applied = false;

    for (const [groupName, presetName] of Object.entries(stored)) {
      // CEL templates are opaque at author time — surface them so the picker can
      // display the expression instead of silently clearing it.
      const isTemplate = isCelTemplate(presetName);
      if (!isTemplate && !presets.find((p) => p.name === presetName)) continue;
      const normalizedGroup = isTopLevelGroup(groupName) ? "" : groupName;
      newSelectedPresets[normalizedGroup] = presetName;
      applied = true;
    }

    if (applied) {
      setSelectedPresets(newSelectedPresets);
      setStoredPresetsApplied(true);
    }
  }, [workflowDef, presetsLoading, presets, enabled, storedPresetsApplied]);

  // Auto-load default presets when workflow definition is loaded
  useEffect(() => {
    if (!workflowDef || !projectId || !enabled) return;
    if (presetsLoading || presets.length === 0) return;

    // Skip if user already has presets selected (including from stored presets)
    if (Object.values(selectedPresets).some((v) => v !== null)) return;

    // Skip if stored presets exist (they'll be applied by the effect above)
    if (storedPresetsFromStep && Object.keys(storedPresetsFromStep).length > 0) return;

    // Skip if values are already configured with a model set
    // If no model is set, default presets will provide one
    const currentValues = valuesRef.current;
    const hasModelValue = currentValues && Object.keys(currentValues).some(key => {
      return key === 'model' || key.endsWith('.model');
    });
    if (currentValues && Object.keys(currentValues).length > 0 && hasModelValue) return;

    const loadDefaults = async () => {
      const defaults = await presetGrpc.getDefaultPresets(projectId, fullWorkflowRef);
      if (Object.keys(defaults).length === 0) return;

      const newSelectedPresets: Record<string, string | null> = {};
      const newValues: Record<string, unknown> = { ...valuesRef.current };

      for (const [groupName, presetName] of Object.entries(defaults)) {
        const preset = presets.find((p) => p.name === presetName);
        if (!preset) continue;

        newSelectedPresets[groupName] = presetName;
        for (const [key, value] of Object.entries(preset.params)) {
          newValues[groupParamKey(groupName, key)] = value;
        }
      }

      if (Object.keys(newSelectedPresets).length > 0) {
        setSelectedPresets(newSelectedPresets);
        onValuesChange(newValues);
      }
    };

    loadDefaults();
  // eslint-disable-next-line react-hooks/exhaustive-deps -- values read via ref to avoid re-trigger loop
  }, [workflowDef, projectId, presetsLoading, presets, fullWorkflowRef, enabled, selectedPresets, storedPresetsFromStep]);

  // Get hidden preset check
  const isPresetHidden = usePreferencesStore((state) => state.isPresetHidden);

  // Get presets filtered by tag and hidden state
  const getPresetsForGroup = useCallback(
    (tag?: string): Preset[] => {
      if (!tag) return [];
      return presets.filter((p) => p.tag === tag && !isPresetHidden(p.name));
    },
    [presets, isPresetHidden]
  );

  // Handle preset selection
  const handlePresetSelect = useCallback(
    (groupName: string, preset: Preset | null) => {
      setSelectedPresets((prev) => ({ ...prev, [groupName]: preset?.name ?? null }));

      if (preset) {
        const newValues = { ...values };
        for (const [key, value] of Object.entries(preset.params)) {
          newValues[groupParamKey(groupName, key)] = value;
        }
        onValuesChange(newValues);
      }
    },
    [values, onValuesChange]
  );

  // Handle individual input change.
  //
  // Composes onto `valuesRef` rather than the `values` prop captured in this
  // closure. Callers fire several of these from one event handler — e.g.
  // WorkflowStepConfig's handleResponseToolChange writes response_tool_name,
  // response_tool_description and response_schema back to back. React has not
  // re-rendered between them, so reading the prop would give all three the
  // same stale snapshot and the last write would silently discard the others.
  const handleInputChange = useCallback(
    (name: string, value: unknown) => {
      const newValues = { ...valuesRef.current };
      if (value === undefined || value === "") {
        delete newValues[name];
      } else {
        newValues[name] = fromJson(ValueSchema, value as any);
      }
      valuesRef.current = newValues;
      onValuesChange(newValues);
    },
    [onValuesChange]
  );

  // Get current value for an input (checks: args → selected preset → default)
  const getInputValue = useCallback(
    (name: string, schema: InputDef): unknown => {
      if (values?.[name] !== undefined) {
        return values[name];
      }
      // Fall through to selected preset's params
      for (const [groupName, presetName] of Object.entries(selectedPresets)) {
        if (!presetName) continue;
        const preset = presets.find((p) => p.name === presetName);
        if (!preset) continue;
        // Check if this input belongs to this group
        const paramName = !isTopLevelGroup(groupName) && name.startsWith(`${groupName}.`)
          ? name.slice(groupName.length + 1)
          : (isTopLevelGroup(groupName) ? name : null);
        if (paramName && preset.params[paramName] !== undefined) {
          return preset.params[paramName];
        }
      }
      return getInputDefault(schema);
    },
    [values, selectedPresets, presets]
  );

  return {
    workflowDef,
    loadingDef,
    inputGroups,
    presets,
    presetsLoading,
    selectedPresets,
    getPresetsForGroup,
    handlePresetSelect,
    handleInputChange,
    getInputValue,
  };
}