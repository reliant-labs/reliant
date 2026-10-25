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

import { fetchDefaultPresets, useWorkflowPresets, type Preset } from "@/hooks/preset-queries";
import { useWorktreeById, useWorktreeList } from "@/hooks/worktree-queries";
import { buildDaemonChoices, daemonLabel, NO_MACHINE } from "@/components/Automations/daemonChoices";
import { useDaemonStatus } from "@/hooks/useDaemonStatus";
import { useProjectDaemonInstalls } from "@/hooks/trigger-queries";
import { CardInset } from "@/components/forge-ui/card";
import { inputDefToSchema } from "@/lib/nodeFieldAdapter";
import type { InputDef } from "@/lib/inputHelpers";
import { usePreferencesStore } from "@/store/preferencesStore";
import { WorkflowInputGroup } from "../WorkflowInputGroup";
import { TypedInputsProvider } from "../inputs/TypedInputField";
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
  /**
   * Offer an optional "Runs on" machine picker. Off by default: a host that
   * already has its own required machine field (the automation dialog) must
   * not show a second one.
   */
  showMachinePicker?: boolean;
  /**
   * Add "No machine" to the machine picker: the run uses only tools that run on
   * Reliant's servers. The value is the NO_MACHINE sentinel in `daemonId`, which
   * the host turns into the request's no_machine flag. `noMachineBlocker` is the
   * reason it cannot be chosen for this workflow, shown beside the disabled
   * option.
   */
  offerNoMachine?: boolean;
  noMachineBlocker?: string;
  /**
   * Inputs the host sets some other way and this form must neither show nor
   * require — an activation's inputs a declared trigger maps from each event,
   * which the server refuses as params (they would be overwritten every fire).
   */
  excludeInputs?: readonly string[];
  /** The host renders the workspace itself (the setup dialog's "Where it runs"). */
  hideWorktree?: boolean;
  /** No "Inputs" heading: the host's section already names them. */
  hideInputsLabel?: boolean;
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
  showMachinePicker = false,
  offerNoMachine = false,
  noMachineBlocker,
  excludeInputs,
  hideWorktree = false,
  hideInputsLabel = false,
  disabled = false,
}: RunWorkflowFormProps) {
  const ids = useId();
  const fieldId = (name: string) => `${ids}-${name}`;

  const { loading, error: definitionError, inputGroups: allInputGroups } = useWorkflowDefinition(
    projectId,
    workflowRef,
  );
  const excludeKey = (excludeInputs ?? []).join("\u0000");
  const inputGroups = useMemo(() => {
    if (!excludeKey) return allInputGroups;
    const excluded = new Set(excludeKey.split("\u0000"));
    return allInputGroups
      .map((group) => ({ ...group, inputs: group.inputs.filter((input) => !excluded.has(input.name)) }))
      .filter((group) => group.inputs.length > 0);
  }, [allInputGroups, excludeKey]);
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
  // A typed input scoped by a sibling ("labels of the chosen repositories")
  // names that sibling by its label and type.
  const typedInputs = useMemo(() => {
    const inputLabels: Record<string, string> = {};
    const inputTypes: Record<string, string> = {};
    for (const group of allInputGroups) {
      for (const input of group.inputs) {
        inputLabels[input.name] = inputDefToSchema(input.name, input.schema).label;
        if (input.schema.type) inputTypes[input.name] = input.schema.type;
      }
    }
    return { inputLabels, inputTypes };
  }, [allInputGroups]);
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
    void fetchDefaultPresets(projectId, workflowRef).then((defaults) => {
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
      {!hideWorktree && (
        <WorktreeField
          id={fieldId("worktree")}
          projectId={projectId}
          worktreeId={value.worktreeId}
          disabled={disabled}
          onChange={(worktreeId) => onChange({ ...valueRef.current, worktreeId })}
        />
      )}

      {showMachinePicker && (
        <MachineField
          id={fieldId("machine")}
          projectId={projectId}
          daemonId={value.daemonId}
          disabled={disabled}
          offerNoMachine={offerNoMachine}
          noMachineBlocker={noMachineBlocker}
          onChange={(daemonId) => onChange({ ...valueRef.current, daemonId })}
        />
      )}

      <div>
        <span id={fieldId("inputs-label")} className={hideInputsLabel ? "sr-only" : labelClass}>
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
          <TypedInputsProvider value={typedInputs}>
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
          </TypedInputsProvider>
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

export function WorktreeField({ id, projectId, worktreeId, disabled, onChange }: WorktreeFieldProps) {
  // One page at a time: a project can hold hundreds of workspaces agents
  // created, and the form needs none of them until the user opens the list.
  const worktreesQuery = useWorktreeList(projectId);
  const worktrees = worktreesQuery.worktrees;
  // A stored workspace beyond the loaded pages is looked up by id, so an edit
  // shows its real name instead of "Unavailable" just because it is on page 3.
  const stored = useWorktreeById(worktreeId);

  const options = useMemo(() => {
    const storedRows = stored && !stored.deleted_at && !worktrees.some((w) => w.id === stored.id) ? [stored] : [];
    const listed = [...worktrees, ...storedRows]
      .filter((w) => !w.is_main || w.id === worktreeId)
      .map((w) => ({ value: w.id, label: w.branch ? `${w.name} (${w.branch})` : w.name }))
      .sort((a, b) => a.label.localeCompare(b.label));
    // A stored worktree stays selectable even once archived or deleted, so an
    // edit shows what the automation is set to instead of silently moving it.
    if (worktreeId && !listed.some((o) => o.value === worktreeId)) {
      listed.unshift({ value: worktreeId, label: `Unavailable workspace (${worktreeId.slice(0, 8)})` });
    }
    return listed;
  }, [worktrees, worktreeId, stored]);

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
      {worktreesQuery.hasMore && (
        <button
          type="button"
          className="mt-1 text-xs text-primary hover:underline disabled:opacity-60"
          disabled={disabled || worktreesQuery.isLoadingMore}
          onClick={worktreesQuery.loadMore}
        >
          {worktreesQuery.isLoadingMore ? "Loading…" : "Load more workspaces"}
        </button>
      )}
      <p id={`${id}-hint`} className={hintClass}>
        {worktreesQuery.isLoading
          ? "Loading this project's workspaces…"
          : !!worktreesQuery.error
            ? "Could not load this project's workspaces; the main checkout is still available."
            : "The checkout the run works in."}
      </p>
    </div>
  );
}

interface MachineFieldProps {
  id: string;
  projectId: string;
  daemonId: string | undefined;
  disabled: boolean;
  offerNoMachine: boolean;
  noMachineBlocker?: string;
  onChange: (daemonId: string | undefined) => void;
}

function MachineField({ id, projectId, daemonId, disabled, offerNoMachine, noMachineBlocker, onChange }: MachineFieldProps) {
  const { daemons, loading: daemonsLoading } = useDaemonStatus();
  const installsQuery = useProjectDaemonInstalls();
  const ready = !daemonsLoading && !installsQuery.isLoading;

  // Only machines the server would accept. Unlike an automation, a manual run
  // has a sensible default (the runtime's own resolution), so nothing is
  // preselected even when several machines qualify.
  const choices = useMemo(
    () => buildDaemonChoices(daemons, installsQuery.data ?? [], projectId).filter((c) => c.eligible),
    [daemons, installsQuery.data, projectId],
  );
  // A prefilled machine (a re-run's original) stays selectable even when it no
  // longer qualifies, so the field shows what will be sent instead of reading
  // "Default machine" over an id it still submits.
  const storedUnlisted = daemonId && daemonId !== NO_MACHINE && ready && !choices.some((c) => c.daemonId === daemonId);
  const storedLabel = storedUnlisted
    ? `${daemonLabel(daemons.find((d) => d.daemonId === daemonId), daemonId)} (unavailable)`
    : undefined;

  return (
    <div>
      <label htmlFor={id} className={labelClass}>
        Runs on
      </label>
      <select
        id={id}
        className={fieldClass}
        value={daemonId ?? ""}
        disabled={disabled}
        onChange={(e) => onChange(e.target.value || undefined)}
        aria-describedby={`${id}-hint`}
      >
        <option value="">Default machine</option>
        {storedLabel && <option value={daemonId}>{storedLabel}</option>}
        {choices.map((choice) => (
          <option key={choice.daemonId} value={choice.daemonId}>
            {choice.label} ({choice.statusLabel}
            {choice.installed ? ", project installed" : ""})
          </option>
        ))}
        {offerNoMachine && (
          <option value={NO_MACHINE} disabled={!!noMachineBlocker}>
            {noMachineBlocker ? `No machine (unavailable: ${noMachineBlocker})` : "No machine (server tools only)"}
          </option>
        )}
      </select>
      <p id={`${id}-hint`} className={hintClass}>
        {daemonId === NO_MACHINE
          ? "The run uses only web, integration and planning tools; nothing touches files."
          : !ready
            ? "Loading your machines…"
            : choices.length === 0
              ? "No machine of yours has this project installed; the run uses the default."
              : "The machine the run's tools execute on."}
      </p>
    </div>
  );
}
