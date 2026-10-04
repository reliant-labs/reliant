// Copyright (c) 2025 Reliant Labs

/**
 * Run a workflow with typed inputs — the controlled form.
 *
 * One form, several hosts: the Run… dialog (RunWorkflowDialog) and the
 * automation dialog's "What it runs" section today, the builder's Test run
 * later (research/WORKFLOW_UI.md §2.4). It owns no data: the host holds the
 * value and decides what submitting means.
 *
 * STORE-FREE. Every input is a prop: the project and the workflow are named
 * by the host and never read from the current chat or project. An automation
 * is edited from a page that may be showing another project entirely, and the
 * composer's controls — bound to the open chat's params and the current
 * project — are exactly what made inputs uneditable there before.
 *
 * What it renders: the workspace (worktree) the run uses, the workflow's
 * typed inputs with a preset picker per input group, and which required
 * inputs are still unset. The prompt and the machine stay with the host,
 * because they mean different things to a manual run and to a schedule.
 */

import { useCallback, useEffect, useId, useMemo, useRef } from "react";
import { useQuery } from "@tanstack/react-query";

import { presetGrpc, type Preset } from "@/api/preset-grpc";
import { worktreeGrpc } from "@/api/worktree-grpc";
import { CardInset } from "@/components/forge-ui/card";
import { inputDefToSchema } from "@/lib/nodeFieldAdapter";
import type { InputDef } from "@/lib/inputHelpers";
import { useWorkflowPresets } from "@/store/globalDataStore";
import { usePreferencesStore } from "@/store/preferencesStore";
import { WorkflowInputGroup } from "../WorkflowInputGroup";
import { useWorkflowDefinition } from "../useWorkflowDefinition";
import { errorTextClass, fieldClass, hintClass, labelClass } from "./runFormStyles";
import {
  coerceInputValue,
  countRunInputs,
  flattenParams,
  groupNamesOf,
  missingRequiredInputs,
  nestParams,
  normalizeGroupName,
  presetFlatValues,
  toDisplayValue,
  type RunWorkflowValue,
} from "./runWorkflowValues";
import "../config/config-panel.css";

export type { RunWorkflowValue } from "./runWorkflowValues";

/** What the host needs to decide whether to submit. */
export interface RunWorkflowFormStatus {
  /** The definition is still loading; required inputs are not known yet. */
  loading: boolean;
  /** Labels of required inputs with no value from a preset or an override. */
  missingRequired: string[];
  /** The definition could not be loaded or parsed. */
  definitionError: string | null;
}

export interface RunWorkflowFormProps {
  projectId: string;
  /** Workflow to run. Empty means "the owner's default", resolved at run time. */
  workflowRef: string;
  value: RunWorkflowValue;
  onChange: (value: RunWorkflowValue) => void;
  /** Reports loading and required-input state as it changes. */
  onStatusChange?: (status: RunWorkflowFormStatus) => void;
  /** Show the missing-required message. Hosts turn it on after a submit attempt. */
  showValidation?: boolean;
  /**
   * Fill in the workflow's default presets when the value has no inputs yet.
   * Right for a fresh run; wrong for an edit, where "no presets" is a choice
   * the saved automation made and must not be silently changed.
   */
  applyDefaultPresets?: boolean;
  disabled?: boolean;
}

export function RunWorkflowForm({
  projectId,
  workflowRef,
  value,
  onChange,
  onStatusChange,
  showValidation = false,
  applyDefaultPresets = false,
  disabled = false,
}: RunWorkflowFormProps) {
  const ids = useId();
  const fieldId = (name: string) => `${ids}-${name}`;

  const { loading, error: definitionError, inputGroups } = useWorkflowDefinition(
    projectId,
    workflowRef,
  );
  const { presets: availablePresets, loading: presetsLoading } = useWorkflowPresets(
    projectId,
    workflowRef,
  );
  const isPresetHidden = usePreferencesStore((state) => state.isPresetHidden);

  // Handlers compose onto the latest value, never a render's stale snapshot:
  // a default-preset fetch can resolve between renders.
  const valueRef = useRef(value);
  valueRef.current = value;

  const groupNames = useMemo(() => groupNamesOf(inputGroups), [inputGroups]);
  const schemaByName = useMemo(() => {
    const map = new Map<string, InputDef>();
    for (const group of inputGroups) {
      for (const input of group.inputs) map.set(input.name, input.schema);
    }
    return map;
  }, [inputGroups]);

  // What each input shows: the selected presets' values, overridden by the
  // explicit ones — the same order the server applies them in.
  const effectiveFlat = useMemo(
    () => ({
      ...presetFlatValues(value.presets, availablePresets),
      ...flattenParams(value.params, groupNames),
    }),
    [value.presets, value.params, availablePresets, groupNames],
  );
  const displayValues = useMemo(() => {
    const display: Record<string, unknown> = {};
    for (const [name, raw] of Object.entries(effectiveFlat)) {
      const schema = schemaByName.get(name);
      display[name] = schema ? toDisplayValue(schema, raw) : raw;
    }
    return display;
  }, [effectiveFlat, schemaByName]);

  const missing = useMemo(
    () =>
      missingRequiredInputs(inputGroups, effectiveFlat).map(
        ({ name, schema }) => inputDefToSchema(name, schema).label,
      ),
    [inputGroups, effectiveFlat],
  );

  const statusKey = JSON.stringify([loading, missing, definitionError]);
  useEffect(() => {
    onStatusChange?.({ loading, missingRequired: missing, definitionError });
    // statusKey carries every field; the callback identity is the host's concern.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [statusKey]);

  // Default presets, once per workflow, and only onto an empty value.
  const defaultsRequestedFor = useRef<string | null>(null);
  useEffect(() => {
    if (!applyDefaultPresets || !projectId || !workflowRef) return;
    const key = `${projectId}\u0000${workflowRef}`;
    if (defaultsRequestedFor.current === key) return;
    defaultsRequestedFor.current = key;
    if (countRunInputs(valueRef.current) > 0) return;

    let cancelled = false;
    void presetGrpc.getDefaultPresets(projectId, workflowRef).then((defaults) => {
      if (cancelled || Object.keys(defaults).length === 0) return;
      // The user may have started filling the form while this was in flight.
      if (countRunInputs(valueRef.current) > 0) return;
      const presets: Record<string, string> = {};
      for (const [group, name] of Object.entries(defaults)) {
        if (name) presets[normalizeGroupName(group)] = name;
      }
      onChange({ ...valueRef.current, presets });
    });
    return () => {
      cancelled = true;
    };
  }, [applyDefaultPresets, projectId, workflowRef, onChange]);

  const handleInputChange = useCallback(
    (name: string, raw: unknown) => {
      const current = valueRef.current;
      const schema = schemaByName.get(name);
      const next = schema ? coerceInputValue(schema, raw) : raw;
      const flat = flattenParams(current.params, groupNames);
      if (next === undefined) delete flat[name];
      else flat[name] = next;
      const updated = { ...current, params: nestParams(flat, groupNames) };
      valueRef.current = updated;
      onChange(updated);
    },
    [schemaByName, groupNames, onChange],
  );

  const handlePresetSelect = useCallback(
    (groupName: string, preset: Preset | null) => {
      const current = valueRef.current;
      const presets = { ...current.presets };
      // "default" is the server's other spelling of the workflow-level group.
      if (groupName === "") delete presets["default"];
      if (preset) presets[groupName] = preset.name;
      else delete presets[groupName];

      // Choosing a preset means "use its values": drop explicit overrides of
      // the inputs it sets, or they would keep winning and the choice would
      // look like it did nothing.
      let params = current.params;
      if (preset) {
        const flat = flattenParams(current.params, groupNames);
        for (const key of Object.keys(preset.params)) {
          delete flat[groupName ? `${groupName}.${key}` : key];
        }
        params = nestParams(flat, groupNames);
      }
      const updated = { ...current, presets, params };
      valueRef.current = updated;
      onChange(updated);
    },
    [groupNames, onChange],
  );

  const presetsForGroup = (tag: string | undefined, selected: string | null): Preset[] => {
    if (!tag) return [];
    // A hidden preset an automation already uses stays listed, so the picker
    // shows what is selected rather than a blank.
    return availablePresets.filter(
      (p) => p.tag === tag && (!isPresetHidden(p.name) || p.name === selected),
    );
  };

  const selectedPresetFor = (groupName: string): string | null =>
    value.presets[groupName] || (groupName === "" ? value.presets["default"] : undefined) || null;

  return (
    <div className="space-y-4">
      <WorktreeField
        id={fieldId("worktree")}
        projectId={projectId}
        worktreeId={value.worktreeId}
        disabled={disabled}
        onChange={(worktreeId) => onChange({ ...valueRef.current, worktreeId })}
      />

      <div>
        <span id={fieldId("inputs-label")} className={labelClass}>
          Inputs
        </span>
        {!workflowRef ? (
          <p className={hintClass}>
            The default workflow is chosen when it runs, so its inputs cannot be set here. Pick a
            workflow to set them.
          </p>
        ) : loading ? (
          <p className={hintClass} aria-live="polite">
            Loading this workflow&apos;s inputs…
          </p>
        ) : definitionError ? (
          <p className={errorTextClass} role="alert">
            Could not load this workflow&apos;s inputs: {definitionError}
          </p>
        ) : inputGroups.length === 0 ? (
          <p className={hintClass}>This workflow takes no inputs.</p>
        ) : (
          <CardInset
            padding="none"
            className="cpv2-scope"
            role="group"
            aria-labelledby={fieldId("inputs-label")}
          >
            {inputGroups.map((group) => {
              const selected = selectedPresetFor(group.name);
              return (
                <WorkflowInputGroup
                  key={group.name || "__top"}
                  group={group}
                  isTopLevel={group.name === ""}
                  values={displayValues}
                  onChange={handleInputChange}
                  presets={presetsForGroup(group.presets?.tag, selected)}
                  selectedPreset={selected}
                  onPresetSelect={(preset) => handlePresetSelect(group.name, preset)}
                  presetsLoading={presetsLoading}
                  disabled={disabled}
                />
              );
            })}
          </CardInset>
        )}
        {showValidation && missing.length > 0 && (
          <p className={errorTextClass} role="alert">
            Fill in {missing.length === 1 ? "the required input" : "the required inputs"}:{" "}
            {missing.join(", ")}.
          </p>
        )}
      </div>
    </div>
  );
}

interface WorktreeFieldProps {
  id: string;
  projectId: string;
  worktreeId?: string;
  disabled: boolean;
  onChange: (worktreeId: string | undefined) => void;
}

function WorktreeField({ id, projectId, worktreeId, disabled, onChange }: WorktreeFieldProps) {
  const worktreesQuery = useQuery({
    queryKey: ["run-workflow-form", "worktrees", projectId],
    queryFn: () => worktreeGrpc.list(projectId),
    enabled: !!projectId,
    staleTime: 30_000,
  });
  const worktrees = worktreesQuery.data?.worktrees;

  const options = useMemo(() => {
    const listed = (worktrees ?? [])
      .filter((w) => !w.is_main || w.id === worktreeId)
      .map((w) => ({ value: w.id, label: w.branch ? `${w.name} (${w.branch})` : w.name }))
      .sort((a, b) => a.label.localeCompare(b.label));
    // A stored worktree stays selectable even once archived or deleted, so an
    // edit shows what the automation is set to instead of silently moving it.
    if (worktreeId && !listed.some((o) => o.value === worktreeId)) {
      listed.unshift({ value: worktreeId, label: `Unavailable workspace (${worktreeId.slice(0, 8)})` });
    }
    return listed;
  }, [worktrees, worktreeId]);

  return (
    <div>
      <label htmlFor={id} className={labelClass}>
        Workspace
      </label>
      <select
        id={id}
        className={fieldClass}
        value={worktreeId ?? ""}
        disabled={disabled}
        onChange={(e) => onChange(e.target.value || undefined)}
        aria-describedby={`${id}-hint`}
      >
        <option value="">Main checkout</option>
        {options.map((option) => (
          <option key={option.value} value={option.value}>
            {option.label}
          </option>
        ))}
      </select>
      <p id={`${id}-hint`} className={hintClass}>
        {worktreesQuery.isLoading
          ? "Loading this project's workspaces…"
          : worktreesQuery.isError
            ? "Could not load this project's workspaces; the main checkout is still available."
            : "The checkout the run works in."}
      </p>
    </div>
  );
}
